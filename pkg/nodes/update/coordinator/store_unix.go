//go:build linux || darwin

package coordinator

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/pkg/nodes/internal/jsonstrict"
)

const (
	stateFileName = "state.json"
	lockFileName  = ".coordinator.lock"
)

type Store struct {
	root  *os.File
	lock  *os.File
	owner uint64
	mu    sync.Mutex
	fault func(string) error
}

func OpenStore(root string) (*Store, error) {
	return openStoreOwned(root, uint64(os.Geteuid()))
}

func openStoreOwned(root string, expectedOwner uint64) (*Store, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, errors.New("coordinator root must be a clean absolute path")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, fmt.Errorf("inspect coordinator root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("coordinator root must be a private non-symlink directory")
	}
	if _, owner, ok := unixFileIdentity(info); !ok || owner != expectedOwner {
		return nil, errors.New("coordinator root must be owned by the coordinator account")
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open coordinator root: %w", err)
	}
	rootFile := os.NewFile(uintptr(fd), root)
	openedInfo, err := rootFile.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		_ = rootFile.Close()
		return nil, errors.New("coordinator root identity changed while opening")
	}
	lock, err := openFixedFileAt(fd, lockFileName, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		_ = rootFile.Close()
		return nil, fmt.Errorf("open coordinator lock: %w", err)
	}
	lockInfo, statErr := lock.Stat()
	if statErr != nil {
		_ = lock.Close()
		_ = rootFile.Close()
		return nil, fmt.Errorf("inspect coordinator lock: %w", statErr)
	}
	links, owner, identityOK := unixFileIdentity(lockInfo)
	if !identityOK || !lockInfo.Mode().IsRegular() || lockInfo.Mode().Perm() != 0o600 || links != 1 ||
		owner != expectedOwner {
		_ = lock.Close()
		_ = rootFile.Close()
		return nil, errors.New("coordinator lock must be a private owned regular file")
	}
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lock.Close()
		_ = rootFile.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, errors.New("coordinator instance is already owned by another process")
		}
		return nil, fmt.Errorf("lock coordinator instance: %w", err)
	}
	store := &Store{root: rootFile, lock: lock, owner: expectedOwner}
	if err = store.reconcileTemporaryFiles(); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

func (store *Store) reconcileTemporaryFiles() error {
	if _, err := store.root.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind coordinator root: %w", err)
	}
	names, err := store.root.Readdirnames(-1)
	if err != nil {
		return fmt.Errorf("list coordinator root: %w", err)
	}
	removed := false
	for _, name := range names {
		if name == stateFileName || name == lockFileName || name == payloadAFileName || name == payloadBFileName {
			continue
		}
		if !validTemporaryName(name) {
			return fmt.Errorf("unexpected coordinator store entry %q", name)
		}
		file, openErr := openFixedFileAt(
			int(store.root.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0,
		)
		if openErr != nil {
			return fmt.Errorf("open coordinator temporary %q: %w", name, openErr)
		}
		info, statErr := file.Stat()
		closeErr := file.Close()
		if statErr != nil || closeErr != nil {
			return fmt.Errorf("inspect coordinator temporary %q", name)
		}
		links, owner, identityOK := unixFileIdentity(info)
		modeOK := info.Mode().Perm() == 0o600 ||
			(strings.HasPrefix(name, ".candidate-") && info.Mode().Perm() == 0o500)
		if !identityOK || !info.Mode().IsRegular() ||
			!modeOK || links != 1 || owner != store.owner {
			return fmt.Errorf("coordinator temporary %q is not a private owned regular file", name)
		}
		if unlinkErr := unix.Unlinkat(int(store.root.Fd()), name, 0); unlinkErr != nil {
			return fmt.Errorf("remove coordinator temporary %q: %w", name, unlinkErr)
		}
		removed = true
	}
	if removed {
		if err = unix.Fsync(int(store.root.Fd())); err != nil {
			return fmt.Errorf("sync reconciled coordinator root: %w", err)
		}
	}
	return nil
}

func validTemporaryName(name string) bool {
	validPrefix := false
	for _, prefix := range []string{".state-", ".archive-", ".candidate-"} {
		if strings.HasPrefix(name, prefix) {
			name = strings.TrimPrefix(name, prefix)
			validPrefix = true
			break
		}
	}
	if !validPrefix || !strings.HasSuffix(name, ".tmp") {
		return false
	}
	hexadecimal := strings.TrimSuffix(name, ".tmp")
	if len(hexadecimal) != 32 {
		return false
	}
	_, err := hex.DecodeString(hexadecimal)
	return err == nil
}

func (store *Store) Close() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	var errs []error
	if store.lock != nil {
		if err := unix.Flock(int(store.lock.Fd()), unix.LOCK_UN); err != nil {
			errs = append(errs, err)
		}
		errs = append(errs, store.lock.Close())
		store.lock = nil
	}
	if store.root != nil {
		errs = append(errs, store.root.Close())
		store.root = nil
	}
	return errors.Join(errs...)
}

func (store *Store) Load() (State, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.loadLocked()
}

func (store *Store) Commit(expectedGeneration uint64, next State) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	current, err := store.loadLocked()
	if err != nil {
		return err
	}
	if current.Generation != expectedGeneration || next.Generation != expectedGeneration+1 {
		return errors.New("coordinator state generation conflict")
	}
	if current.Installation != next.Installation {
		return errors.New("coordinator installation identity is immutable")
	}
	if err = next.Validate(); err != nil {
		return err
	}
	return store.writeLocked(next, false)
}

func (store *Store) Initialize(state State) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if state.Generation != 1 {
		return errors.New("initial coordinator generation must be one")
	}
	if err := state.Validate(); err != nil {
		return err
	}
	return store.writeLocked(state, true)
}

func (store *Store) loadLocked() (State, error) {
	if store.root == nil {
		return State{}, errors.New("coordinator store is closed")
	}
	file, err := openFixedFileAt(int(store.root.Fd()), stateFileName, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return State{}, fmt.Errorf("open coordinator state: %w", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return State{}, errors.New("inspect coordinator state")
	}
	links, owner, identityOK := unixFileIdentity(info)
	if !identityOK || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 ||
		links != 1 || owner != store.owner || info.Size() > MaxStateBytes {
		return State{}, errors.New("coordinator state is not a bounded private regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxStateBytes+1))
	if err != nil || len(data) > MaxStateBytes {
		return State{}, errors.New("read bounded coordinator state")
	}
	var state State
	if err = decodeState(data, &state); err != nil {
		return State{}, fmt.Errorf("decode coordinator state: %w", err)
	}
	if err = state.Validate(); err != nil {
		return State{}, err
	}
	return state, nil
}

func decodeState(data []byte, state *State) error {
	_, err := jsonstrict.Decode(data)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(state); err != nil {
		return err
	}
	if err = decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("coordinator state contains multiple JSON values")
		}
		return err
	}
	return nil
}

func (store *Store) writeLocked(state State, exclusive bool) error {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode coordinator state: %w", err)
	}
	data = append(data, '\n')
	if len(data) > MaxStateBytes {
		return errors.New("encoded coordinator state exceeds size limit")
	}
	if exclusive {
		existing, openErr := openFixedFileAt(
			int(store.root.Fd()), stateFileName, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0,
		)
		if openErr == nil {
			_ = existing.Close()
			return os.ErrExist
		}
		if !errors.Is(openErr, os.ErrNotExist) {
			return fmt.Errorf("inspect initial coordinator state: %w", openErr)
		}
	}
	temporaryName, err := randomTemporaryName()
	if err != nil {
		return err
	}
	temporary, err := openFixedFileAt(
		int(store.root.Fd()),
		temporaryName,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return fmt.Errorf("create coordinator state temporary: %w", err)
	}
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = unix.Unlinkat(int(store.root.Fd()), temporaryName, 0)
		}
	}()
	if _, err = temporary.Write(data); err != nil {
		return fmt.Errorf("write coordinator state temporary: %w", err)
	}
	if err = temporary.Sync(); err != nil {
		return fmt.Errorf("sync coordinator state temporary: %w", err)
	}
	if err = temporary.Close(); err != nil {
		return fmt.Errorf("close coordinator state temporary: %w", err)
	}
	if err = store.injectFault("state_before_publish"); err != nil {
		return err
	}
	if err = unix.Renameat(
		int(store.root.Fd()), temporaryName, int(store.root.Fd()), stateFileName,
	); err != nil {
		return fmt.Errorf("publish coordinator state: %w", err)
	}
	removeTemporary = false
	if err = store.injectFault("state_after_publish"); err != nil {
		return err
	}
	if err = unix.Fsync(int(store.root.Fd())); err != nil {
		return fmt.Errorf("sync coordinator state directory: %w", err)
	}
	return nil
}

func (store *Store) injectFault(point string) error {
	if store.fault == nil {
		return nil
	}
	return store.fault(point)
}

func openFixedFileAt(directory int, name string, flags int, mode uint32) (*os.File, error) {
	fd, err := unix.Openat(directory, name, flags, mode)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func randomTemporaryName() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("create coordinator temporary identity: %w", err)
	}
	return ".state-" + hex.EncodeToString(value[:]) + ".tmp", nil
}

func unsigned64[Value ~uint16 | ~uint32 | ~uint64](value Value) uint64 {
	return uint64(value)
}
