//go:build linux

// Package credentialipc owns Linux process-lifetime handles for private
// credential transport. It contains no provider access or model tools.
package credentialipc

import (
	"errors"
	"net"
	"sync"

	"golang.org/x/sys/unix"
)

var ErrUnavailable = errors.New("credential peer identity is unavailable")

type lifetimeState struct {
	mu sync.Mutex
	fd int
}

// Lifetime copies share ownership; Close cannot race an Alive check or allow a
// reused descriptor to stand in for a terminated process. Zero values deny.
type Lifetime struct{ state *lifetimeState }

func OpenProcess(pid int32) (Lifetime, error) {
	if pid <= 0 {
		return Lifetime{}, ErrUnavailable
	}
	fd, err := unix.PidfdOpen(int(pid), 0)
	if err != nil {
		return Lifetime{}, ErrUnavailable
	}
	return takeLifetime(fd)
}

func takeLifetime(fd int) (Lifetime, error) {
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		_ = unix.Close(fd)
		return Lifetime{}, ErrUnavailable
	}
	return Lifetime{&lifetimeState{fd: fd}}, nil
}

func (lifetime Lifetime) Alive() bool {
	if lifetime.state == nil {
		return false
	}
	lifetime.state.mu.Lock()
	defer lifetime.state.mu.Unlock()
	if lifetime.state.fd < 0 {
		return false
	}
	fds := []unix.PollFd{{Fd: int32(lifetime.state.fd), Events: unix.POLLIN}}
	_, err := unix.Poll(fds, 0)
	return err == nil && fds[0].Revents == 0
}

func (lifetime Lifetime) Close() {
	if lifetime.state == nil {
		return
	}
	lifetime.state.mu.Lock()
	defer lifetime.state.mu.Unlock()
	if lifetime.state.fd >= 0 {
		_ = unix.Close(lifetime.state.fd)
		lifetime.state.fd = -1
	}
}

type Peer struct {
	PID int32
	UID uint32
	GID uint32
	Lifetime
}

// ConnectedPeer requires SO_PEERPIDFD; it never falls back to a numeric PID
// lookup. The socket creator must also be alive when authority is exercised.
// Per-message credentials are still required to prevent descriptor handoff.
func ConnectedPeer(connection *net.UnixConn) (Peer, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return Peer{}, ErrUnavailable
	}
	var peer Peer
	var socketErr error
	err = raw.Control(func(fd uintptr) {
		credentials, credentialsErr := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if credentialsErr != nil || credentials.Pid <= 0 {
			socketErr = ErrUnavailable
			return
		}
		processFD, processErr := unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_PEERPIDFD)
		if processErr != nil {
			socketErr = ErrUnavailable
			return
		}
		peer.Lifetime, socketErr = takeLifetime(processFD)
		peer.PID, peer.UID, peer.GID = credentials.Pid, credentials.Uid, credentials.Gid
	})
	if err != nil || socketErr != nil || !peer.Alive() {
		peer.Close()
		return Peer{}, ErrUnavailable
	}
	return peer, nil
}
