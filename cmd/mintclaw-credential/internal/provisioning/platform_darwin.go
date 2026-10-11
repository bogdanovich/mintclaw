//go:build darwin

package provisioning

import "golang.org/x/sys/unix"

func protectProcess() error {
	return ErrUnsupported
}

func flushTerminal(fd int) error {
	return unix.IoctlSetPointerInt(fd, unix.TIOCFLUSH, unix.TCIFLUSH)
}
