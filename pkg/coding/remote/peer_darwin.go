//go:build darwin

package remote

import (
	"net"
	"os"

	"golang.org/x/sys/unix"
)

func currentUID() uint32 { return uint32(os.Geteuid()) }

func unixPeerUID(connection *net.UnixConn) (uint32, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return 0, err
	}
	var uid uint32
	var socketErr error
	if err = raw.Control(func(fd uintptr) {
		credentials, credentialsErr := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if credentialsErr != nil {
			socketErr = credentialsErr
			return
		}
		uid = credentials.Uid
	}); err != nil {
		return 0, err
	}
	return uid, socketErr
}
