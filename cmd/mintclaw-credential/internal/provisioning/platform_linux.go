//go:build linux

package provisioning

import (
	"os"

	"golang.org/x/sys/unix"
)

func protectProcess() error {
	if os.Getuid() != 0 || os.Geteuid() != 0 {
		return ErrIdentity
	}
	if err := unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{}); err != nil {
		return ErrIsolation
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return ErrIsolation
	}
	return nil
}

func flushTerminal(fd int) error {
	return unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIFLUSH)
}
