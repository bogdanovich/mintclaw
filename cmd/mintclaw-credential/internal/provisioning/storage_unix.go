//go:build linux || darwin

package provisioning

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func trustedDirectory(fd int, owner uint32, private bool) bool {
	var info unix.Stat_t
	if unix.Fstat(fd, &info) != nil || info.Mode&unix.S_IFMT != unix.S_IFDIR || info.Uid != owner ||
		info.Mode&0o022 != 0 {
		return false
	}
	return !private || info.Mode&0o7777 == 0o700
}

func openCredentialDirectory(root *os.File, owner uint32) (*os.File, error) {
	fd, err := unix.FcntlInt(root.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, ErrStorage
	}
	for index, name := range []string{"etc", "credstore.encrypted", "mintclaw-credential"} {
		if !trustedDirectory(fd, owner, false) {
			_ = unix.Close(fd)
			return nil, ErrStorage
		}
		next, openErr := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(openErr, unix.ENOENT) && index > 0 {
			mkdirErr := unix.Mkdirat(fd, name, 0o700)
			if (mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST)) || (mkdirErr == nil && unix.Fsync(fd) != nil) {
				_ = unix.Close(fd)
				return nil, ErrStorage
			}
			next, openErr = unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, ErrStorage
		}
		fd = next
	}
	if !trustedDirectory(fd, owner, true) {
		_ = unix.Close(fd)
		return nil, ErrStorage
	}
	return os.NewFile(uintptr(fd), "private credential directory"), nil
}

func requireAbsent(dir *os.File) error {
	var info unix.Stat_t
	err := unix.Fstatat(int(dir.Fd()), credentialName, &info, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err == nil {
		return ErrExists
	}
	return ErrStorage
}

func persistCiphertext(ctx context.Context, dir *os.File, data []byte) error {
	if ctx.Err() != nil || !validCiphertext(data) {
		return ErrStorage
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return ErrStorage
	}
	name := ".pending-" + hex.EncodeToString(nonce[:])
	fd, err := unix.Openat(int(dir.Fd()), name,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return ErrStorage
	}
	file := os.NewFile(uintptr(fd), "private encrypted credential")
	defer func() { _ = file.Close() }()
	defer func() { _ = unix.Unlinkat(int(dir.Fd()), name, 0) }()
	if file.Chmod(0o600) != nil {
		return ErrStorage
	}
	if _, err := file.Write(data); err != nil || file.Sync() != nil || ctx.Err() != nil {
		return ErrStorage
	}
	if err := unix.Linkat(int(dir.Fd()), name, int(dir.Fd()), credentialName, 0); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return ErrExists
		}
		return ErrStorage
	}
	if unix.Unlinkat(int(dir.Fd()), name, 0) != nil || unix.Fsync(int(dir.Fd())) != nil {
		return ErrCommitUncertain
	}
	return nil
}
