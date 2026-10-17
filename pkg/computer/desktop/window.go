package desktop

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"sync"
	"sync/atomic"

	"github.com/bogdanovich/mintclaw/pkg/computer"
)

// WindowRequest is an operator-selected local resource, never model authority.
// Linux requires an explicit local X11 display and private authority file. No
// ambient DISPLAY, home directory, or alternate desktop is selected on failure.
type WindowRequest struct {
	PID        uint32
	WindowID   uint32
	Display    string
	XAuthority string
}

// WindowIdentity is a fresh admission of one native window incarnation. Numeric
// PID/window IDs alone are not identity. A replacement requires a new guard and
// generation, even if the original numbers are reused by the same process.
type WindowIdentity struct {
	PID        uint32
	WindowID   uint32
	Generation uint64
}

type windowBackend interface {
	alive(context.Context) bool
	close() error
}

type WindowGuard struct {
	identity  WindowIdentity
	backend   windowBackend
	lost      atomic.Bool
	closeOnce sync.Once
	closeErr  error
}

// WatchWindow attests the exact native resource and subscribes to destruction
// before returning. Missing native ownership/lifetime support refuses admission;
// it never substitutes a PID-only check, title match or polling-only window ID.
func WatchWindow(ctx context.Context, request WindowRequest) (*WindowGuard, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.PID == 0 || request.PID > 1<<31-1 || request.WindowID == 0 {
		return nil, computer.ErrInvalid
	}
	backend, err := watchNativeWindow(ctx, request)
	if err != nil {
		return nil, err
	}
	var generation [8]byte
	if _, err = rand.Read(generation[:]); err != nil || binary.LittleEndian.Uint64(generation[:]) == 0 {
		_ = backend.close()
		return nil, computer.ErrUnavailable
	}
	if err = ctx.Err(); err != nil {
		_ = backend.close()
		return nil, err
	}
	return &WindowGuard{identity: WindowIdentity{
		PID: request.PID, WindowID: request.WindowID, Generation: binary.LittleEndian.Uint64(generation[:]),
	}, backend: backend}, nil
}

func (guard *WindowGuard) Identity() WindowIdentity { return guard.identity }

// Alive never reacquires a resource. A destruction, process exit, transport
// failure or close permanently invalidates this guard. A pre-canceled caller
// cannot by itself declare the underlying resource destroyed.
func (guard *WindowGuard) Alive(ctx context.Context) bool {
	if guard == nil || guard.backend == nil || ctx.Err() != nil || guard.lost.Load() {
		return false
	}
	if !guard.backend.alive(ctx) {
		if ctx.Err() == nil {
			guard.lost.Store(true)
		}
		return false
	}
	return ctx.Err() == nil && !guard.lost.Load()
}

// Close releases only our observer/connection/process handles, never the app.
func (guard *WindowGuard) Close() error {
	if guard == nil {
		return nil
	}
	guard.lost.Store(true)
	guard.closeOnce.Do(func() {
		if guard.backend != nil {
			guard.closeErr = guard.backend.close()
		}
	})
	return guard.closeErr
}
