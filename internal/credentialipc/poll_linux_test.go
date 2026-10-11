//go:build linux

package credentialipc

import (
	"net"
	"os"
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func observationPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		t.Fatal("synthetic observation pair unavailable")
	}
	connections := make([]*net.UnixConn, 0, 2)
	for _, fd := range fds {
		file := os.NewFile(uintptr(fd), "synthetic observation endpoint")
		connection, err := net.FileConn(file)
		_ = file.Close()
		if err != nil {
			t.Fatal("synthetic observation conversion failed")
		}
		packet, ok := connection.(*net.UnixConn)
		if !ok {
			t.Fatal("synthetic observation socket unavailable")
		}
		t.Cleanup(func() { _ = packet.Close() })
		connections = append(connections, packet)
	}
	return connections[0], connections[1]
}

func TestConnectionObservationRejectsHalfCloseAndClosedHandle(t *testing.T) {
	first, second := observationPair(t)
	if !ConnectionAlive(first) || !ConnectionAlive(second) {
		t.Fatal("live connection denied")
	}
	if second.CloseWrite() != nil || ConnectionAlive(first) {
		t.Fatal("peer half-close remained live")
	}
	_ = first.Close()
	if ConnectionAlive(first) {
		t.Fatal("closed handle remained live")
	}
}

func TestObservationsSurvivePreemptionSignals(t *testing.T) {
	first, _ := observationPair(t)
	lifetime, err := OpenProcess(int32(os.Getpid()))
	if err != nil {
		t.Fatal("synthetic process pin unavailable")
	}
	defer lifetime.Close()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	tid := unix.Gettid()
	stop, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(100 * time.Microsecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				_ = unix.Tgkill(os.Getpid(), tid, unix.SIGURG)
			}
		}
	}()
	defer func() { close(stop); <-finished }()
	for range 20000 {
		if !ConnectionAlive(first) || !lifetime.Alive() {
			t.Fatal("preemption revoked a healthy observation")
		}
	}
	lifetime.Close()
	if lifetime.Alive() {
		t.Fatal("closed process handle remained live after signals")
	}
}
