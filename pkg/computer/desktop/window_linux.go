//go:build linux

package desktop

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/res"
	"github.com/jezek/xgb/xproto"
	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/pkg/computer"
)

type x11Window struct {
	mu         sync.Mutex
	connection *xgb.Conn
	socket     net.Conn
	pidfd      int
	window     xproto.Window
	root       xproto.Window
	closed     bool
	lost       atomic.Bool
	eventStop  chan struct{}
	eventDone  chan struct{}
	fences     chan chan struct{}
}

func watchNativeWindow(ctx context.Context, request WindowRequest) (windowBackend, error) {
	endpoint, err := x11Display(request.Display)
	if err != nil {
		return nil, err
	}
	cookie, err := x11Cookie(request.XAuthority, endpoint.number)
	if err != nil {
		return nil, err
	}
	// A kernel reference cannot silently become a reused PID. X-Resource below
	// attests the server's actual client PID, not a forgeable _NET_WM_PID label.
	pidfd, err := unix.PidfdOpen(int(request.PID), 0)
	if err != nil {
		if errors.Is(err, unix.ENOSYS) {
			return nil, ErrUnsupported
		}
		if errors.Is(err, unix.ESRCH) {
			return nil, computer.ErrStale
		}
		return nil, computer.ErrDenied
	}
	guard := &x11Window{pidfd: pidfd, window: xproto.Window(request.WindowID)}
	failed := true
	defer func() {
		if failed {
			_ = guard.close()
		}
	}()
	info, err := os.Stat("/proc/" + strconv.FormatUint(uint64(request.PID), 10))
	if err != nil || !ownedByCurrentUser(info) {
		return nil, computer.ErrDenied
	}
	openCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	guard.socket, err = (&net.Dialer{}).DialContext(openCtx, "unix", endpoint.socket)
	if err != nil {
		return nil, computer.ErrUnavailable
	}
	deadline, _ := openCtx.Deadline()
	if err = guard.socket.SetDeadline(deadline); err != nil {
		return nil, computer.ErrUnavailable
	}
	stopOpening := context.AfterFunc(openCtx, func() { _ = guard.socket.SetDeadline(time.Now()) })
	defer stopOpening()
	guard.connection, err = xgb.NewConnNetWithCookieHex(guard.socket, cookie)
	if err != nil {
		return nil, computer.ErrDenied
	}
	guard.eventStop = make(chan struct{})
	guard.eventDone = make(chan struct{})
	guard.fences = make(chan chan struct{})
	go guard.pumpEvents()
	setup := xproto.Setup(guard.connection)
	if endpoint.screen >= len(setup.Roots) {
		return nil, computer.ErrInvalid
	}
	guard.root = setup.Roots[endpoint.screen].Root
	if err = res.Init(guard.connection); err != nil {
		return nil, ErrUnsupported
	}
	version, err := res.QueryVersion(guard.connection, 1, 2).Reply()
	if err != nil || version == nil || version.ServerMajor < 1 || version.ServerMajor == 1 && version.ServerMinor < 2 {
		return nil, ErrUnsupported
	}
	if err = xproto.ChangeWindowAttributesChecked(guard.connection, guard.window, xproto.CwEventMask,
		[]uint32{xproto.EventMaskStructureNotify}).Check(); err != nil {
		return nil, computer.ErrStale
	}
	owners, err := res.QueryClientIds(guard.connection, 1, []res.ClientIdSpec{
		{Client: request.WindowID, Mask: res.ClientIdMaskLocalClientPID},
	}).Reply()
	if err != nil || owners == nil || len(owners.Ids) != 1 ||
		owners.Ids[0].Spec.Mask != res.ClientIdMaskLocalClientPID || len(owners.Ids[0].Value) != 1 ||
		owners.Ids[0].Value[0] != request.PID {
		return nil, computer.ErrDenied
	}
	// A checked round trip and drain include any destruction/reuse that raced
	// subscription or ownership validation. Losing the transport is terminal.
	if !guard.alive(openCtx) {
		return nil, computer.ErrStale
	}
	if err = guard.socket.SetDeadline(time.Time{}); err != nil {
		return nil, computer.ErrUnavailable
	}
	failed = false
	return guard, nil
}

func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid())
}

func (guard *x11Window) alive(ctx context.Context) bool {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if guard.closed || guard.lost.Load() || ctx.Err() != nil {
		return false
	}
	poll := []unix.PollFd{{Fd: int32(guard.pidfd), Events: unix.POLLIN}}
	if _, err := unix.Poll(poll, 0); err != nil || poll[0].Revents != 0 {
		guard.lost.Store(true)
		return false
	}
	deadline := time.Now().Add(2 * time.Second)
	if caller, ok := ctx.Deadline(); ok && caller.Before(deadline) {
		deadline = caller
	}
	if err := guard.socket.SetDeadline(deadline); err != nil {
		guard.lost.Store(true)
		return false
	}
	stop := context.AfterFunc(ctx, func() { _ = guard.socket.SetDeadline(time.Now()) })
	defer func() { stop(); _ = guard.socket.SetDeadline(time.Time{}) }()
	if reply, err := xproto.GetInputFocus(guard.connection).Reply(); err != nil || reply == nil {
		guard.lost.Store(true)
		return false
	}
	// The event consumer continuously drains XGB's bounded queue. Fence its
	// processing after the server round trip so an earlier destruction cannot
	// be hidden behind scheduler lag or a full queue during human control.
	ack := make(chan struct{})
	select {
	case guard.fences <- ack:
	case <-ctx.Done():
		return false
	case <-guard.eventDone:
		guard.lost.Store(true)
		return false
	}
	select {
	case <-ack:
	case <-ctx.Done():
		return false
	case <-guard.eventDone:
		guard.lost.Store(true)
		return false
	}
	if guard.lost.Load() {
		return false
	}
	if reply, err := xproto.GetGeometry(guard.connection, xproto.Drawable(guard.window)).
		Reply(); err != nil || reply == nil ||
		reply.Root != guard.root {
		guard.lost.Store(true)
		return false
	}
	return ctx.Err() == nil && !guard.lost.Load()
}

func (guard *x11Window) pumpEvents() {
	defer close(guard.eventDone)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	drain := func() {
		for range 1024 {
			event, eventErr := guard.connection.PollForEvent()
			if eventErr != nil {
				guard.lost.Store(true)
			}
			if event == nil {
				return
			}
			if destroyed, ok := event.(xproto.DestroyNotifyEvent); ok && destroyed.Window == guard.window {
				guard.lost.Store(true)
			}
		}
		// An unbounded event flood cannot establish a drained identity fence.
		guard.lost.Store(true)
	}
	for {
		select {
		case <-guard.eventStop:
			drain()
			return
		case ack := <-guard.fences:
			drain()
			close(ack)
		case <-ticker.C:
			drain()
		}
	}
}

func (guard *x11Window) close() error {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if guard.closed {
		return nil
	}
	guard.closed = true
	// Force bounded teardown before asking XGB to stop: its graceful Close
	// otherwise performs a round trip that can wait on a stalled server.
	if guard.socket != nil {
		_ = guard.socket.Close()
	}
	if guard.connection != nil {
		guard.connection.Close()
	}
	if guard.eventStop != nil {
		close(guard.eventStop)
		<-guard.eventDone
	}
	return unix.Close(guard.pidfd)
}
