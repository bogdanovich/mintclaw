//go:build unix && !aix

package thread

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func tryAcquireThreadLeaseFile(file *os.File) error {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return ErrLeaseBusy
	}
	return err
}

func releaseThreadLeaseFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
