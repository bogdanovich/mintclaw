//go:build !darwin && !linux

package remote

import (
	"errors"
	"net"
)

func currentUID() uint32 { return 0 }

func unixPeerUID(*net.UnixConn) (uint32, error) {
	return 0, errors.New("coding remote peer authentication is unsupported on this platform")
}
