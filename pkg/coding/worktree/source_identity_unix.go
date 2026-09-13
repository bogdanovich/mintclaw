//go:build !windows

package worktree

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func inspectDirectoryIdentity(path string) (FilesystemIdentity, error) {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return FilesystemIdentity{}, err
	}
	file := os.NewFile(uintptr(descriptor), path)
	if file == nil {
		_ = unix.Close(descriptor)
		return FilesystemIdentity{}, fmt.Errorf("invalid source directory descriptor")
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return FilesystemIdentity{}, err
	}
	current, err := os.Lstat(path)
	if err != nil {
		return FilesystemIdentity{}, err
	}
	if !opened.IsDir() || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(opened, current) {
		return FilesystemIdentity{}, fmt.Errorf("source directory was replaced or redirected")
	}
	stat, ok := opened.Sys().(*syscall.Stat_t)
	if !ok {
		return FilesystemIdentity{}, fmt.Errorf("source directory identity is unavailable")
	}
	return FilesystemIdentity{
		Volume: identityStatUint64(stat.Dev),
		File:   identityStatUint64(stat.Ino),
	}, nil
}

func identityStatUint64[Value ~int | ~int32 | ~int64 | ~uint | ~uint32 | ~uint64](value Value) uint64 {
	return uint64(value)
}
