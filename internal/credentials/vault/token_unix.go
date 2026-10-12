//go:build linux || darwin

package vault

import (
	"bytes"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const serviceAccountTokenFile = "service-account-token"

// StoreServiceAccountToken provisions an initial owner-only token beside the
// approval state. It accepts no environment/argv input and never overwrites
// an existing file. The caller owns and clears token after this operation.
func StoreServiceAccountToken(directory string, token []byte) error {
	if !validServiceAccountToken(token) {
		return ErrUnavailable
	}
	dir, err := openTokenDirectory(directory)
	if err != nil {
		return ErrUnavailable
	}
	defer func() { _ = dir.Close() }()
	fd, err := unix.Openat(int(dir.Fd()), serviceAccountTokenFile,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return ErrUnavailable
	}
	file := os.NewFile(uintptr(fd), "private vault token")
	defer func() { _ = file.Close() }()
	committed := false
	defer func() {
		if !committed {
			_ = unix.Unlinkat(int(dir.Fd()), serviceAccountTokenFile, 0)
			_ = dir.Sync()
		}
	}()
	if unix.Fchmod(fd, 0o600) != nil {
		return ErrUnavailable
	}
	if n, writeErr := file.Write(token); writeErr != nil || n != len(token) || file.Sync() != nil ||
		dir.Sync() != nil {
		return ErrUnavailable
	}
	committed = true
	return nil
}

// UseServiceAccountToken lends the private file content to trusted startup.
// No token object or raw filesystem/provider error crosses this boundary.
func UseServiceAccountToken(directory string, use func([]byte) error) (resultErr error) {
	defer func() {
		if recover() != nil {
			resultErr = ErrUnavailable
		}
	}()
	if use == nil {
		return ErrUnavailable
	}
	dir, err := openTokenDirectory(directory)
	if err != nil {
		return ErrUnavailable
	}
	defer func() { _ = dir.Close() }()
	fd, err := unix.Openat(int(dir.Fd()), serviceAccountTokenFile,
		unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return ErrUnavailable
	}
	file := os.NewFile(uintptr(fd), "private vault token")
	defer func() { _ = file.Close() }()
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG ||
		stat.Mode&0o7777 != 0o600 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 ||
		stat.Size <= 0 || stat.Size > maxTokenBytes {
		return ErrUnavailable
	}
	token, err := io.ReadAll(io.LimitReader(file, maxTokenBytes+1))
	defer clear(token)
	if err != nil || !validServiceAccountToken(token) || use(token) != nil {
		return ErrUnavailable
	}
	return nil
}

func openTokenDirectory(directory string) (*os.File, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory ||
		os.Getuid() != os.Geteuid() || os.Getgid() != os.Getegid() {
		return nil, ErrUnavailable
	}
	fd, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrUnavailable
	}
	file := os.NewFile(uintptr(fd), "private vault directory")
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR ||
		stat.Mode&0o7777 != 0o700 || stat.Uid != uint32(os.Geteuid()) {
		_ = file.Close()
		return nil, ErrUnavailable
	}
	return file, nil
}

func validServiceAccountToken(token []byte) bool {
	return len(token) > 0 && len(token) <= maxTokenBytes &&
		bytes.IndexFunc(token, func(char rune) bool { return char < '!' || char > '~' }) == -1
}
