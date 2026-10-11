//go:build linux

package supervision

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestUnnamedControlUsesKernelAddressLength(t *testing.T) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal("synthetic socketpair unavailable")
	}
	defer func() { _ = unix.Close(pair[0]); _ = unix.Close(pair[1]) }()
	for _, fd := range pair {
		if !unnamedUnixEndpoint(fd, unix.SYS_GETSOCKNAME) || !unnamedUnixEndpoint(fd, unix.SYS_GETPEERNAME) {
			t.Fatal("unnamed socketpair not recognized")
		}
	}
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal("synthetic named socket unavailable")
	}
	defer func() { _ = unix.Close(fd) }()
	if unix.Bind(fd, &unix.SockaddrUnix{Name: t.TempDir() + "/named.sock"}) != nil {
		t.Fatal("synthetic named socket bind failed")
	}
	if unnamedUnixEndpoint(fd, unix.SYS_GETSOCKNAME) || unnamedUnixEndpoint(fd, unix.SYS_GETPEERNAME) {
		t.Fatal("named or disconnected socket admitted")
	}
	if unnamedUnixEndpoint(-1, unix.SYS_GETSOCKNAME) {
		t.Fatal("missing socket admitted")
	}
}
