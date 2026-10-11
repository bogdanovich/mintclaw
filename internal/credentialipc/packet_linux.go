//go:build linux

package credentialipc

import (
	"errors"
	"net"

	"golang.org/x/sys/unix"
)

// ReadPacket makes received descriptors close-on-exec atomically. Callers still
// reject ancillary rights and close installed descriptors; a concurrent trusted
// child launch must never inherit them during that rejection window.
func ReadPacket(connection *net.UnixConn, data, control []byte) (count, controlCount, flags int, resultErr error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return 0, 0, 0, ErrUnavailable
	}
	var readErr error
	err = raw.Read(func(fd uintptr) bool {
		count, controlCount, flags, _, readErr = unix.Recvmsg(
			int(fd), data, control, unix.MSG_CMSG_CLOEXEC|unix.MSG_DONTWAIT,
		)
		return !errors.Is(readErr, unix.EAGAIN) && !errors.Is(readErr, unix.EINTR)
	})
	if err != nil || readErr != nil {
		return count, controlCount, flags, ErrUnavailable
	}
	return count, controlCount, flags, nil
}
