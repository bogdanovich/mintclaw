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
	input      atomic.Pointer[x11InputLease]
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
	// XGB's PollForEvent deliberately hides ordinary transport errors as an
	// empty event. Observe its actual retained connection instead: a terminal
	// reader/writer or explicit close must revoke authority without a new call.
	guard.socket = &x11ObservedTransport{Conn: guard.socket, guard: guard}
	deadline, _ := openCtx.Deadline()
	if err = guard.socket.SetDeadline(deadline); err != nil {
		return nil, computer.ErrUnavailable
	}
	openingInterrupted := make(chan struct{})
	stopOpening := context.AfterFunc(openCtx, func() {
		defer close(openingInterrupted)
		_ = guard.socket.SetDeadline(time.Now())
	})
	finishOpening := sync.OnceFunc(func() {
		if !stopOpening() {
			<-openingInterrupted
		}
	})
	defer finishOpening()
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
	finishOpening()
	if openCtx.Err() != nil {
		return nil, openCtx.Err()
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

type x11ObservedTransport struct {
	net.Conn
	guard *x11Window
}

func (transport *x11ObservedTransport) Read(value []byte) (int, error) {
	n, err := transport.Conn.Read(value)
	if err != nil {
		_ = transport.Close()
	}
	return n, err
}

func (transport *x11ObservedTransport) Write(value []byte) (int, error) {
	n, err := transport.Conn.Write(value)
	if err != nil {
		_ = transport.Close()
	}
	return n, err
}

func (transport *x11ObservedTransport) Close() error {
	// Never call XGB.Close here: it can wait for the very reader/writer that
	// reported this error. Closing only our socket also releases our X11 tokens.
	transport.guard.invalidate()
	return transport.Conn.Close()
}

func (guard *x11Window) alive(ctx context.Context) bool {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	return guard.aliveLocked(ctx)
}

func (guard *x11Window) aliveLocked(ctx context.Context) bool {
	if guard.closed || guard.lost.Load() || ctx.Err() != nil {
		return false
	}
	checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if !pidfdAlive(checkCtx, guard.pidfd, unix.Poll) {
		if ctx.Err() == nil {
			guard.invalidate()
		}
		return false
	}
	deadline, _ := checkCtx.Deadline()
	if err := guard.socket.SetDeadline(deadline); err != nil {
		guard.invalidate()
		return false
	}
	interrupted := make(chan struct{})
	stop := context.AfterFunc(checkCtx, func() {
		defer close(interrupted)
		_ = guard.socket.SetDeadline(time.Now())
	})
	defer func() {
		if !stop() {
			<-interrupted
		}
		_ = guard.socket.SetDeadline(time.Time{})
	}()
	if reply, err := xproto.GetGeometry(guard.connection, xproto.Drawable(guard.window)).Reply(); err != nil ||
		reply == nil || reply.Root != guard.root {
		guard.invalidate()
		return false
	}
	// The event consumer continuously drains XGB's bounded queue. Fence its
	// processing after the server round trip so an earlier destruction cannot
	// be hidden behind scheduler lag or a full queue during human control.
	ack := make(chan struct{})
	select {
	case guard.fences <- ack:
	case <-checkCtx.Done():
		return false
	case <-guard.eventDone:
		guard.invalidate()
		return false
	}
	select {
	case <-ack:
	case <-checkCtx.Done():
		return false
	case <-guard.eventDone:
		guard.invalidate()
		return false
	}
	if guard.lost.Load() {
		return false
	}
	return checkCtx.Err() == nil && !guard.lost.Load()
}

// An interrupted syscall is not evidence of process exit. Retry against the
// same retained handle within the caller's bounded liveness check. The poll
// function is explicit so interruption and cancellation are deterministic tests.
func pidfdAlive(ctx context.Context, fd int, poll func([]unix.PollFd, int) (int, error)) bool {
	descriptors := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	for ctx.Err() == nil {
		descriptors[0].Revents = 0
		_, err := poll(descriptors, 0)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		return err == nil && descriptors[0].Revents == 0
	}
	return false
}

func (guard *x11Window) pumpEvents() {
	defer close(guard.eventDone)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	drain := func() {
		for range 1024 {
			event, eventErr := guard.connection.PollForEvent()
			if eventErr != nil {
				guard.invalidate()
			}
			if event == nil {
				return
			}
			if destroyed, ok := event.(xproto.DestroyNotifyEvent); ok && destroyed.Window == guard.window {
				guard.invalidate()
			}
			if lease := guard.input.Load(); lease != nil {
				switch event := event.(type) {
				case xproto.SelectionClearEvent:
					if event.Owner == lease.window && event.Selection == lease.selection {
						lease.invalidate()
					}
				case xproto.DestroyNotifyEvent:
					if event.Window == lease.window {
						lease.invalidate()
					}
				}
			}
		}
		// An unbounded event flood cannot establish a drained identity fence.
		guard.invalidate()
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
			guard.pollInputProcess(unix.Poll)
		}
	}
}

func (guard *x11Window) pollInputProcess(poll func([]unix.PollFd, int) (int, error)) {
	if guard.input.Load() == nil {
		return
	}
	// An app may retain X resources after process exit. Supplement destruction
	// events with the original kernel handle. EINTR is unknown, never exit.
	process := []unix.PollFd{{Fd: int32(guard.pidfd), Events: unix.POLLIN}}
	_, err := poll(process, 0)
	if err == nil && process[0].Revents != 0 || err != nil && !errors.Is(err, unix.EINTR) {
		guard.invalidate()
	}
}

func (guard *x11Window) close() error {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if guard.closed {
		return nil
	}
	guard.closed = true
	guard.invalidate()
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
