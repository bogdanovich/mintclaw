//go:build linux

package approval

import (
	"bytes"
	"context"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/internal/credentialipc"
)

const consoleRequest = "mintclaw-owner-enrollment-v1"

// ServeConsole borrows a startup-qualified, service-created Unix packet
// listener. It admits only live root peers whose packet identity matches the
// socket creator. A root-owned socket-activation listener is not interchangeable:
// the operator client requires SO_PEERCRED to identify the dedicated service.
// Startup must prove that agents cannot obtain root or control this service;
// UID 0 alone does not prove that a request came from a human operator.
func (enrollment *Enrollment) ServeConsole(ctx context.Context, listener *net.UnixListener) error {
	if !enrollment.valid() || ctx == nil || ctx.Err() != nil || listener == nil ||
		os.Getuid() == 0 || os.Getgid() == 0 || os.Getuid() != os.Geteuid() || os.Getgid() != os.Getegid() ||
		!enrollment.serving.CompareAndSwap(false, true) {
		return ErrUnavailable
	}
	defer enrollment.serving.Store(false)
	if credentialipc.PrepareListener(listener) != nil {
		return ErrUnavailable
	}
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	watchDone := make(chan struct{})
	go func() { defer close(watchDone); <-serveCtx.Done(); _ = listener.Close() }()
	var workers sync.WaitGroup
	defer func() { cancel(); <-watchDone; workers.Wait() }()
	slots := make(chan struct{}, 8)
	for {
		connection, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return ErrUnavailable
		}
		select {
		case slots <- struct{}{}:
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer func() { <-slots }()
				enrollment.handleConsole(serveCtx, connection)
			}()
		default:
			_ = connection.Close()
		}
	}
}

func (enrollment *Enrollment) handleConsole(parent context.Context, connection *net.UnixConn) {
	defer func() { _ = connection.Close() }()
	peer, err := credentialipc.ConnectedPeer(connection)
	if err != nil {
		return
	}
	defer peer.Close()
	if peer.UID != 0 || peer.GID != 0 {
		return
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	if connection.SetDeadline(deadline) != nil {
		return
	}
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				_ = connection.SetDeadline(time.Now())
				return
			case <-ticker.C:
				if !peer.Alive() {
					cancel()
				}
			}
		}
	}()
	defer func() { cancel(); <-watchDone }()
	var data [128]byte
	control := make([]byte, unix.CmsgSpace(12)+unix.CmsgSpace(16*4))
	defer clear(control)
	count, controlCount, flags, readErr := credentialipc.ReadPacket(connection, data[:], control)
	if !credentialipc.ValidSender(control[:controlCount], peer) || readErr != nil ||
		flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 || !bytes.Equal(data[:count], []byte(consoleRequest)) ||
		ctx.Err() != nil || !peer.Alive() {
		return
	}
	invite, issueErr := enrollment.issue(ctx)
	if issueErr != nil {
		return
	}
	defer clear(invite)
	if ctx.Err() != nil || !peer.Alive() {
		return
	}
	_, _, _ = connection.WriteMsgUnix(invite, nil, nil)
}
