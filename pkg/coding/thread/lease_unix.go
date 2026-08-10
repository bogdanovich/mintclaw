//go:build unix

package thread

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func openThreadLeaseFile(root *catalogDirectory) (*os.File, error) {
	if root == nil || root.file == nil {
		return nil, fmt.Errorf("coding thread lease: thread directory is closed")
	}
	fd, err := unix.Openat(
		int(root.file.Fd()),
		leaseFileName,
		unix.O_CREAT|unix.O_RDWR|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0o600,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), filepath.Join(root.file.Name(), leaseFileName))
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("coding thread lease: create file handle")
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Nlink != 1 {
		_ = file.Close()
		return nil, fmt.Errorf("coding thread lease: lock file is not a singly linked regular file")
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
