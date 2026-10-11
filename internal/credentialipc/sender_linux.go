//go:build linux

package credentialipc

import (
	"net"

	"golang.org/x/sys/unix"
)

// PrepareListener requires sequenced packets and enables per-message identity
// before any accepted call. Startup still owns path, identity and role admission.
func PrepareListener(listener *net.UnixListener) error {
	if listener == nil {
		return ErrUnavailable
	}
	raw, err := listener.SyscallConn()
	if err != nil {
		return ErrUnavailable
	}
	var socketErr error
	err = raw.Control(func(fd uintptr) {
		kind, typeErr := unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_TYPE)
		if typeErr != nil || kind != unix.SOCK_SEQPACKET {
			socketErr = ErrUnavailable
			return
		}
		socketErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_PASSCRED, 1)
	})
	if err != nil || socketErr != nil {
		return ErrUnavailable
	}
	return nil
}

// ValidSender rejects socket handoff and ancillary descriptors. Every received
// descriptor is closed, including those delivered before a truncation error.
func ValidSender(data []byte, peer Peer) bool {
	messages, err := unix.ParseSocketControlMessage(data)
	valid := err == nil && len(messages) == 1
	for _, message := range messages {
		if message.Header.Level != unix.SOL_SOCKET {
			valid = false
			continue
		}
		switch message.Header.Type {
		case unix.SCM_CREDENTIALS:
			credentials, credErr := unix.ParseUnixCredentials(&message)
			if credErr != nil || credentials.Pid != peer.PID || credentials.Uid != peer.UID ||
				credentials.Gid != peer.GID {
				valid = false
			}
		case unix.SCM_RIGHTS:
			rights, _ := unix.ParseUnixRights(&message)
			for _, fd := range rights {
				_ = unix.Close(fd)
			}
			valid = false
		default:
			valid = false
		}
	}
	return valid
}
