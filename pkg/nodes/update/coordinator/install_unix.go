//go:build linux || darwin

package coordinator

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	nodeupdate "github.com/bogdanovich/mintclaw/pkg/nodes/update"
)

const (
	StoreDirectoryName = "update"
	payloadAFileName   = "payload-a"
	payloadBFileName   = "payload-b"
)

type Adoption struct {
	root      string
	parent    *os.File
	store     *Store
	committed bool
	created   bool
	ownerUID  int
}

func BeginAdoption(
	stateDirectory string,
	installation Installation,
	activeExecutable string,
	activeRelease string,
	activeVersion string,
	ownerUID int,
	ownerGID int,
) (*Adoption, error) {
	if !filepath.IsAbs(stateDirectory) || filepath.Clean(stateDirectory) != stateDirectory {
		return nil, errors.New("node state directory must be a clean absolute path")
	}
	if err := installation.Validate(); err != nil {
		return nil, err
	}
	if activeRelease != activeVersion || !nodeupdate.ValidReleaseVersion(activeRelease) || ownerUID < 0 ||
		ownerGID < 0 {
		return nil, errors.New("invalid active payload or service account identity")
	}
	parent, err := openPrivateDirectory(stateDirectory, ownerUID)
	if err != nil {
		return nil, err
	}
	adoption := &Adoption{
		parent: parent, root: filepath.Join(stateDirectory, StoreDirectoryName), ownerUID: ownerUID,
	}
	if err = unix.Mkdirat(int(parent.Fd()), StoreDirectoryName, 0o700); err != nil {
		_ = parent.Close()
		if errors.Is(err, unix.EEXIST) {
			return nil, errors.New("managed update coordinator is already installed")
		}
		return nil, fmt.Errorf("create coordinator root: %w", err)
	}
	adoption.created = true
	store, err := OpenStore(adoption.root)
	if err != nil {
		_ = adoption.Rollback()
		return nil, err
	}
	adoption.store = store
	payload, err := store.copyInitialPayload(activeExecutable, activeRelease, activeVersion, installation)
	if err != nil {
		_ = adoption.Rollback()
		return nil, err
	}
	state := State{
		SchemaVersion: StateSchemaVersion,
		Generation:    1,
		Installation:  installation,
		Active:        payload,
	}
	if err = store.Initialize(state); err != nil {
		_ = adoption.Rollback()
		return nil, err
	}
	if err = store.transferOwnership(ownerUID, ownerGID); err != nil {
		_ = adoption.Rollback()
		return nil, err
	}
	return adoption, nil
}

func (adoption *Adoption) Commit() error {
	if adoption == nil || !adoption.created || adoption.committed {
		return errors.New("invalid coordinator adoption commit")
	}
	if err := adoption.ReleaseCoordinatorLock(); err != nil {
		return err
	}
	if adoption.parent != nil {
		if err := unix.Fsync(int(adoption.parent.Fd())); err != nil {
			return fmt.Errorf("sync node state directory: %w", err)
		}
		if err := adoption.parent.Close(); err != nil {
			return err
		}
		adoption.parent = nil
	}
	adoption.committed = true
	return nil
}

func (adoption *Adoption) ReleaseCoordinatorLock() error {
	if adoption == nil || !adoption.created || adoption.committed {
		return errors.New("invalid coordinator adoption release")
	}
	if adoption.store == nil {
		return nil
	}
	if err := adoption.store.Close(); err != nil {
		return err
	}
	adoption.store = nil
	return nil
}

func (adoption *Adoption) Rollback() error {
	if adoption == nil || !adoption.created || adoption.committed {
		return nil
	}
	var errs []error
	if adoption.store == nil {
		store, err := openStoreOwned(adoption.root, uint64(adoption.ownerUID))
		if err != nil {
			return fmt.Errorf("reacquire coordinator ownership for rollback: %w", err)
		}
		adoption.store = store
	}
	if adoption.parent != nil {
		rootFD := int(adoption.store.root.Fd())
		for _, name := range []string{stateFileName, payloadAFileName, payloadBFileName, lockFileName} {
			unlinkErr := unix.Unlinkat(rootFD, name, 0)
			if unlinkErr != nil && !errors.Is(unlinkErr, unix.ENOENT) {
				errs = append(errs, fmt.Errorf("remove coordinator file %s: %w", name, unlinkErr))
			}
		}
		errs = append(errs, adoption.store.Close())
		adoption.store = nil
		if removeErr := unix.Unlinkat(
			int(adoption.parent.Fd()), StoreDirectoryName, unix.AT_REMOVEDIR,
		); removeErr != nil && !errors.Is(removeErr, unix.ENOENT) {
			errs = append(errs, fmt.Errorf("remove coordinator root: %w", removeErr))
		}
		errs = append(errs, unix.Fsync(int(adoption.parent.Fd())))
		errs = append(errs, adoption.parent.Close())
		adoption.parent = nil
	}
	adoption.created = false
	return errors.Join(errs...)
}

func openPrivateDirectory(path string, expectedOwner int) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect node state directory: %w", err)
	}
	_, owner, identityOK := unixFileIdentity(info)
	if !identityOK || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 ||
		owner != uint64(expectedOwner) {
		return nil, errors.New("node state directory must be a private non-symlink directory")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open node state directory: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		_ = file.Close()
		return nil, errors.New("node state directory identity changed while opening")
	}
	return file, nil
}

func (store *Store) copyInitialPayload(
	sourcePath string,
	release string,
	version string,
	installation Installation,
) (Payload, error) {
	source, info, err := openExecutable(sourcePath)
	if err != nil {
		return Payload{}, err
	}
	defer func() { _ = source.Close() }()
	if err = validateExecutableFile(source, installation.Platform, installation.Architecture); err != nil {
		return Payload{}, err
	}
	if _, err = source.Seek(0, io.SeekStart); err != nil {
		return Payload{}, fmt.Errorf("rewind active companion payload: %w", err)
	}
	destination, err := openFixedFileAt(
		int(store.root.Fd()), payloadAFileName,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return Payload{}, fmt.Errorf("create active companion slot: %w", err)
	}
	removeDestination := true
	defer func() {
		_ = destination.Close()
		if removeDestination {
			_ = unix.Unlinkat(int(store.root.Fd()), payloadAFileName, 0)
		}
	}()
	digest := sha256.New()
	written, err := io.Copy(io.MultiWriter(destination, digest), io.LimitReader(source, MaxPayloadBytes+1))
	if err != nil {
		return Payload{}, fmt.Errorf("copy active companion payload: %w", err)
	}
	if written != info.Size() || written <= 0 || written > MaxPayloadBytes {
		return Payload{}, errors.New("active companion payload changed or exceeds its bound")
	}
	if err = destination.Sync(); err != nil {
		return Payload{}, fmt.Errorf("sync active companion payload: %w", err)
	}
	if err = destination.Chmod(0o500); err != nil {
		return Payload{}, fmt.Errorf("make active companion payload executable: %w", err)
	}
	if err = destination.Sync(); err != nil {
		return Payload{}, fmt.Errorf("sync active companion payload mode: %w", err)
	}
	if err = destination.Close(); err != nil {
		return Payload{}, fmt.Errorf("close active companion payload: %w", err)
	}
	if err = unix.Fsync(int(store.root.Fd())); err != nil {
		return Payload{}, fmt.Errorf("sync coordinator payload directory: %w", err)
	}
	removeDestination = false
	return Payload{
		Slot: SlotA, Release: release, Version: version,
		SHA256: hex.EncodeToString(digest.Sum(nil)), Size: written,
	}, nil
}

func (store *Store) transferOwnership(uid int, gid int) error {
	if uid == os.Geteuid() && gid == os.Getegid() {
		return nil
	}
	if os.Geteuid() != 0 {
		return errors.New("changing coordinator ownership requires root")
	}
	for _, name := range []string{stateFileName, lockFileName, payloadAFileName} {
		if err := unix.Fchownat(int(store.root.Fd()), name, uid, gid, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return fmt.Errorf("set coordinator file ownership: %w", err)
		}
	}
	if err := unix.Fchown(int(store.root.Fd()), uid, gid); err != nil {
		return fmt.Errorf("set coordinator root ownership: %w", err)
	}
	store.owner = uint64(uid)
	return unix.Fsync(int(store.root.Fd()))
}

func payloadFileName(slot Slot) (string, error) {
	switch slot {
	case SlotA:
		return payloadAFileName, nil
	case SlotB:
		return payloadBFileName, nil
	default:
		return "", errors.New("invalid payload slot")
	}
}
