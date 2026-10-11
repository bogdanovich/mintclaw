//go:build linux

package credentialipc

import (
	"errors"
	"net"

	"golang.org/x/sys/unix"
)

// Go's preemption signals may interrupt even a zero-timeout poll. Retry the
// observation on the same pinned objects; interruption supplies no death or
// liveness evidence. Bound retries so signal storms still deny promptly.
func pollNow(fds []unix.PollFd) bool {
	for range 8 {
		_, err := unix.Poll(fds, 0)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		return err == nil
	}
	return false
}

// ConnectionAlive observes hangup/error flags without reading or peeking a
// packet, which could install ancillary rights. It supplies no process or
// grant identity; callers must separately retain and validate their Peer.
func ConnectionAlive(connection *net.UnixConn) bool {
	if connection == nil {
		return false
	}
	raw, err := connection.SyscallConn()
	if err != nil {
		return false
	}
	alive := false
	err = raw.Control(func(fd uintptr) {
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLRDHUP}}
		alive = pollNow(fds) && fds[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL|unix.POLLRDHUP) == 0
	})
	return err == nil && alive
}
