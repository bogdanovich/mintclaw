//go:build !windows

package mcp

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func exclusiveLeaseReservationKey(path string) string { return path }

func infoSysStat(info os.FileInfo) (*syscall.Stat_t, bool) {
	if info == nil {
		return nil, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return stat, ok
}

func openExclusiveLeaseFile(path string) (*os.File, *exclusiveLeaseParent, error) {
	parent, leaf, err := openExclusiveLeaseParent(path)
	if err != nil {
		return nil, nil, err
	}
	fd, err := unix.Openat(
		int(parent.file.Fd()),
		leaf,
		unix.O_CREAT|unix.O_EXCL|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0o600,
	)
	if errors.Is(err, syscall.EEXIST) {
		fd, err = unix.Openat(
			int(parent.file.Fd()),
			leaf,
			unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
			0,
		)
	}
	if err != nil {
		parent.close()
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		parent.close()
		return nil, nil, err
	}
	stat, statOK := infoSysStat(info)
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !statOK ||
		stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) {
		_ = file.Close()
		parent.close()
		return nil, nil, errExclusiveLeaseUnsafe
	}
	parent.leaf = leaf
	if err = parent.validateLeaf(file); err != nil {
		_ = file.Close()
		parent.close()
		return nil, nil, err
	}
	return file, parent, nil
}

func openExclusiveLeaseParent(path string) (*exclusiveLeaseParent, string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, "", errExclusiveLeaseUnsafe
	}
	leaf := filepath.Base(path)
	if leaf == "." || leaf == string(filepath.Separator) {
		return nil, "", errExclusiveLeaseUnsafe
	}
	parentPath := filepath.Dir(path)
	configuredInfo, err := os.Lstat(parentPath)
	if err != nil || !configuredInfo.IsDir() || configuredInfo.Mode()&os.ModeSymlink != 0 {
		return nil, "", errExclusiveLeaseUnsafe
	}
	resolvedParent, err := filepath.EvalSymlinks(parentPath)
	if err != nil {
		return nil, "", err
	}
	resolvedInfo, err := os.Lstat(resolvedParent)
	if err != nil || !os.SameFile(configuredInfo, resolvedInfo) {
		return nil, "", errExclusiveLeaseUnsafe
	}
	fd, err := unix.Open(
		resolvedParent,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, "", err
	}
	parent := os.NewFile(uintptr(fd), resolvedParent)
	anchored := &exclusiveLeaseParent{
		file: parent, path: parentPath, identity: configuredInfo,
	}
	if err = anchored.validate(); err != nil {
		anchored.close()
		return nil, "", errExclusiveLeaseUnsafe
	}
	return anchored, leaf, nil
}

func tryAcquireExclusiveFileLock(file *os.File) error {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return errExclusiveLeaseBusy
	}
	return err
}

func releaseExclusiveFileLock(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
