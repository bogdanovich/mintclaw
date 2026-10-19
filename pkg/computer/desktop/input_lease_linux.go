//go:build linux

package desktop

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/bogdanovich/mintclaw/pkg/computer"
)

// Stable across profiles, displays and versions. This is a private control
// selection, never CLIPBOARD/PRIMARY or a request for selection contents.
const x11InputSelection = "_MINTCLAW_COMPUTER_INPUT_CONTROL"

const x11InputTransactionLimit = 250 * time.Millisecond

type x11InputLease struct {
	guard     *x11Window
	window    xproto.Window
	selection xproto.Atom
	ended     chan struct{}
	endOnce   sync.Once
	lost      atomic.Bool
	closed    bool // protected by guard.mu
}

func (guard *x11Window) invalidate() {
	guard.lost.Store(true)
	if lease := guard.input.Load(); lease != nil {
		lease.invalidate()
	}
}

func (lease *x11InputLease) invalidate() {
	lease.lost.Store(true)
	lease.endOnce.Do(func() { close(lease.ended) })
}

func (lease *x11InputLease) done() <-chan struct{} { return lease.ended }

// Fail a possibly accepted server transaction by closing the original
// connection. X11 close-down releases its server grab, selection and unmapped
// control window. This never reconnects or closes the application's connection.
func (guard *x11Window) abortInputTransaction() {
	guard.invalidate()
	if guard.socket != nil {
		_ = guard.socket.Close()
	}
}

// The caller holds guard.mu. A deadline is also enforced on the actual socket,
// not only a Go context around a synchronous XGB round trip. The cancellation
// callback is joined before restoring deadlines, so it cannot poison a later
// operation after a successful return.
func (guard *x11Window) inputTransaction(
	ctx context.Context, operation func() error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	transactionCtx, cancel := context.WithTimeout(ctx, x11InputTransactionLimit)
	defer cancel()
	deadline, _ := transactionCtx.Deadline()
	if err := guard.socket.SetDeadline(deadline); err != nil {
		guard.abortInputTransaction()
		return computer.ErrUnavailable
	}
	interrupted := make(chan struct{})
	stop := context.AfterFunc(transactionCtx, func() {
		defer close(interrupted)
		guard.abortInputTransaction()
	})
	err := operation()
	if !stop() {
		<-interrupted
	}
	if ctx.Err() != nil {
		guard.abortInputTransaction()
		return ctx.Err()
	}
	if transactionCtx.Err() != nil {
		guard.abortInputTransaction()
		return computer.ErrUnavailable
	}
	if err != nil && !errors.Is(err, computer.ErrBusy) {
		guard.abortInputTransaction()
		return computer.ErrUnavailable
	}
	if guard.lost.Load() || guard.socket.SetDeadline(time.Time{}) != nil {
		guard.abortInputTransaction()
		return computer.ErrUnavailable
	}
	return err
}

func (guard *x11Window) acquireInputLease(ctx context.Context) (inputLeaseBackend, error) {
	if !guard.mu.TryLock() {
		return nil, computer.ErrBusy
	}
	defer guard.mu.Unlock()
	if !guard.aliveLocked(ctx) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, computer.ErrStale
	}
	if guard.input.Load() != nil {
		return nil, computer.ErrBusy
	}
	lease := &x11InputLease{guard: guard, ended: make(chan struct{})}
	err := guard.inputTransaction(ctx, func() error {
		atom, err := xproto.InternAtom(guard.connection, false, uint16(len(x11InputSelection)), x11InputSelection).
			Reply()
		if err != nil || atom == nil {
			return computer.ErrUnavailable
		}
		lease.selection = atom.Atom
		lease.window, err = xproto.NewWindowId(guard.connection)
		if err != nil {
			return err
		}
		// An unmapped InputOnly window is just a server-owned lifetime token.
		// It cannot front, focus, capture, type into or close an application.
		if err = xproto.CreateWindowChecked(
			guard.connection,
			0,
			lease.window,
			guard.root,
			0,
			0,
			1,
			1,
			0,
			xproto.WindowClassInputOnly,
			0,
			xproto.CwEventMask,
			[]uint32{xproto.EventMaskStructureNotify},
		).Check(); err != nil {
			return err
		}
		protocol := x11SelectionProtocol{
			connection: guard.connection, selection: lease.selection, guard: guard, lease: lease,
		}
		if err = claimX11InputSelection(protocol, lease.window); err != nil {
			if errors.Is(err, computer.ErrBusy) {
				if destroyErr := xproto.DestroyWindowChecked(guard.connection, lease.window).
					Check(); destroyErr != nil {
					return computer.ErrUnavailable
				}
			}
			return err
		}
		return nil
	})
	if err != nil {
		lease.invalidate()
		return nil, err
	}
	if guard.lost.Load() {
		lease.invalidate()
		return nil, computer.ErrStale
	}
	return lease, nil
}

type x11SelectionTransport interface {
	grab() error
	owner() (xproto.Window, error)
	claim(xproto.Window) error
	publish()
	ungrab() error
	abort()
}

// The server, not a profile/desktop/display alias, serializes this one
// check-and-claim transaction. Never overwrite an existing owner's selection.
func claimX11InputSelection(protocol x11SelectionTransport, window xproto.Window) error {
	if err := protocol.grab(); err != nil {
		protocol.abort()
		return computer.ErrUnavailable
	}
	owner, err := protocol.owner()
	if err != nil {
		protocol.abort()
		return computer.ErrUnavailable
	}
	if owner != xproto.WindowNone {
		if err = protocol.ungrab(); err != nil {
			protocol.abort()
			return computer.ErrUnavailable
		}
		return computer.ErrBusy
	}
	if err = protocol.claim(window); err != nil {
		protocol.abort()
		return computer.ErrUnavailable
	}
	owner, err = protocol.owner()
	if err != nil || owner != window {
		protocol.abort()
		return computer.ErrUnavailable
	}
	// Publish before releasing the atomic server transaction: a theft-and-
	// restoration after ungrab must still invalidate this lease permanently.
	protocol.publish()
	if err = protocol.ungrab(); err != nil {
		protocol.abort()
		return computer.ErrUnavailable
	}
	return nil
}

type x11SelectionProtocol struct {
	connection *xgb.Conn
	selection  xproto.Atom
	guard      *x11Window
	lease      *x11InputLease
}

func (protocol x11SelectionProtocol) grab() error {
	return xproto.GrabServerChecked(protocol.connection).Check()
}

func (protocol x11SelectionProtocol) owner() (xproto.Window, error) {
	reply, err := xproto.GetSelectionOwner(protocol.connection, protocol.selection).Reply()
	if err != nil || reply == nil {
		return 0, computer.ErrUnavailable
	}
	return reply.Owner, nil
}

func (protocol x11SelectionProtocol) claim(window xproto.Window) error {
	return xproto.SetSelectionOwnerChecked(protocol.connection, window, protocol.selection, xproto.TimeCurrentTime).
		Check()
}

func (protocol x11SelectionProtocol) publish() { protocol.guard.input.Store(protocol.lease) }

func (protocol x11SelectionProtocol) ungrab() error {
	return xproto.UngrabServerChecked(protocol.connection).Check()
}

func (protocol x11SelectionProtocol) abort() { protocol.guard.abortInputTransaction() }

func (lease *x11InputLease) check(ctx context.Context) error {
	guard := lease.guard
	if lease.lost.Load() {
		return computer.ErrStale
	}
	if !guard.mu.TryLock() {
		return computer.ErrBusy
	}
	defer guard.mu.Unlock()
	if lease.closed || !guard.aliveLocked(ctx) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return computer.ErrStale
	}
	var owner xproto.Window
	err := guard.inputTransaction(ctx, func() error {
		var err error
		owner, err = (x11SelectionProtocol{connection: guard.connection, selection: lease.selection}).owner()
		return err
	})
	if err != nil {
		return err
	}
	if owner != lease.window || lease.lost.Load() {
		lease.invalidate()
		return computer.ErrStale
	}
	return nil
}

func (lease *x11InputLease) close() error {
	lease.invalidate()
	guard := lease.guard
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if lease.closed {
		return nil
	}
	lease.closed = true
	defer guard.input.CompareAndSwap(lease, nil)
	if guard.closed || guard.lost.Load() {
		// Closing the original observer connection relinquishes only that
		// client's resources, even if another client took the selection.
		guard.abortInputTransaction()
		return computer.ErrStale
	}
	return guard.inputTransaction(context.Background(), func() error {
		// Never SetSelectionOwner(None): destroying our own lifetime token
		// cannot clear a different client's newer ownership.
		err := xproto.DestroyWindowChecked(guard.connection, lease.window).Check()
		var absent xproto.WindowError
		if errors.As(err, &absent) && absent.BadValue == uint32(lease.window) {
			return nil // the server independently proves this own lifetime token is already gone
		}
		return err
	})
}
