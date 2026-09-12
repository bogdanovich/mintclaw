//go:build !windows

package worktree

import (
	"errors"
	"os"
	"syscall"
)

func tryPlatformFileLock(file *os.File) (func() error, error) {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return nil, errFileLockBusy
	}
	if err != nil {
		return nil, err
	}
	return func() error { return syscall.Flock(int(file.Fd()), syscall.LOCK_UN) }, nil
}
