//go:build linux

package approval

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const ownerFile = "owner-key.json"

type fileStore struct {
	dir      *os.File
	uid, gid uint32
}

func openOwnerStore(directory string) (ownerStore, error) {
	uid, gid := os.Geteuid(), os.Getegid()
	if uid == 0 || gid == 0 || uid != os.Getuid() || gid != os.Getgid() ||
		len(directory) < 2 || len(directory) > 4096 || !filepath.IsAbs(directory) ||
		filepath.Clean(directory) != directory || strings.Count(directory, "/") > 16 {
		return nil, ErrUnavailable
	}
	for _, character := range []byte(directory) {
		if character < 0x21 || character > 0x7e {
			return nil, ErrUnavailable
		}
	}
	descriptor, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, ErrUnavailable
	}
	for _, part := range strings.Split(strings.TrimPrefix(directory, "/"), "/") {
		var parent unix.Stat_t
		if unix.Fstat(descriptor, &parent) != nil || parent.Uid != 0 || parent.Mode&0o022 != 0 {
			_ = unix.Close(descriptor)
			return nil, ErrUnavailable
		}
		next, openErr := unix.Openat(descriptor, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(descriptor)
		if openErr != nil {
			return nil, ErrUnavailable
		}
		descriptor = next
	}
	store := &fileStore{
		dir: os.NewFile(uintptr(descriptor), "private owner directory"),
		uid: uint32(uid),
		gid: uint32(gid),
	}
	if store.check(descriptor, true) != nil || unix.Flock(descriptor, unix.LOCK_EX|unix.LOCK_NB) != nil {
		_ = store.close()
		return nil, ErrUnavailable
	}
	return store, nil
}

func (store *fileStore) check(descriptor int, directory bool) error {
	var stat unix.Stat_t
	if unix.Fstat(descriptor, &stat) != nil || stat.Uid != store.uid || stat.Gid != store.gid {
		return ErrUnavailable
	}
	if directory {
		if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o7777 != 0o700 {
			return ErrUnavailable
		}
	} else if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o7777 != 0o600 || stat.Nlink != 1 ||
		stat.Size <= 0 || stat.Size > maxOwnerRecordBytes {
		return ErrUnavailable
	}
	var acl [128]byte
	_, err := unix.Fgetxattr(descriptor, "system.posix_acl_access", acl[:])
	if !errors.Is(err, unix.ENODATA) && !errors.Is(err, unix.ENOTSUP) {
		return ErrUnavailable
	}
	return nil
}

func (store *fileStore) load() (ownerRecord, error) {
	descriptor, err := unix.Openat(
		int(store.dir.Fd()),
		ownerFile,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if errors.Is(err, unix.ENOENT) {
		return ownerRecord{}, nil
	}
	if err != nil {
		return ownerRecord{}, ErrUnavailable
	}
	file := os.NewFile(uintptr(descriptor), "private owner record")
	defer func() { _ = file.Close() }()
	if store.check(descriptor, false) != nil {
		return ownerRecord{}, ErrUnavailable
	}
	encoded, err := io.ReadAll(io.LimitReader(file, maxOwnerRecordBytes+1))
	if err != nil || len(encoded) == 0 || len(encoded) > maxOwnerRecordBytes {
		return ownerRecord{}, ErrUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var owner persistedOwner
	if decoder.Decode(&owner) != nil || decoder.Decode(new(any)) != io.EOF || owner.Version != 1 {
		return ownerRecord{}, ErrUnavailable
	}
	return ownerRecord(owner), nil
}

// commit acknowledges only after file and directory sync. Any error, including
// an uncertain post-rename result, permanently disables this controller. A
// restart reloads the actual durable record; it never replays a grant issuance.
func (store *fileStore) commit(owner ownerRecord) error {
	if store.check(int(store.dir.Fd()), true) != nil {
		return ErrUnavailable
	}
	encoded, err := encodeOwner(owner)
	if err != nil {
		return ErrUnavailable
	}
	var random [32]byte
	if _, err = io.ReadFull(rand.Reader, random[:]); err != nil {
		return ErrUnavailable
	}
	name := ".owner-" + hex.EncodeToString(random[:])
	descriptor, err := unix.Openat(
		int(store.dir.Fd()),
		name,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return ErrUnavailable
	}
	file := os.NewFile(uintptr(descriptor), "private owner write")
	defer func() { _ = file.Close(); _ = unix.Unlinkat(int(store.dir.Fd()), name, 0) }()
	if count, writeErr := file.Write(encoded); writeErr != nil || count != len(encoded) {
		return ErrUnavailable
	}
	if store.check(descriptor, false) != nil || file.Sync() != nil || file.Close() != nil {
		return ErrUnavailable
	}
	if unix.Renameat(int(store.dir.Fd()), name, int(store.dir.Fd()), ownerFile) != nil || store.dir.Sync() != nil {
		return ErrUnavailable
	}
	return nil
}

func (store *fileStore) close() error { return store.dir.Close() }
