package desktop

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/computer"
)

type fakeInputLease struct {
	ended  chan struct{}
	once   sync.Once
	checks atomic.Int32
	closes atomic.Int32
	err    error
}

func (lease *fakeInputLease) check(ctx context.Context) error {
	lease.checks.Add(1)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	select {
	case <-lease.ended:
		return computer.ErrStale
	default:
		return lease.err
	}
}

func (lease *fakeInputLease) done() <-chan struct{} { return lease.ended }
func (lease *fakeInputLease) close() error {
	lease.closes.Add(1)
	lease.once.Do(func() { close(lease.ended) })
	return nil
}

type fakeInputWindow struct {
	fakeWindowBackend
	lease    *fakeInputLease
	acquires atomic.Int32
	err      error
}

func (window *fakeInputWindow) acquireInputLease(context.Context) (inputLeaseBackend, error) {
	window.acquires.Add(1)
	if window.err != nil {
		return nil, window.err
	}
	return window.lease, nil
}

func TestInputLeaseRequiresNativeProviderAndLiveGuard(t *testing.T) {
	if lease, err := (*WindowGuard)(nil).AcquireInputLease(t.Context()); lease != nil ||
		!errors.Is(err, computer.ErrInvalid) {
		t.Fatal("nil native guard admitted input ownership")
	}
	unsupported := &WindowGuard{backend: &fakeWindowBackend{}}
	if _, err := unsupported.AcquireInputLease(t.Context()); !errors.Is(err, ErrUnsupported) {
		t.Fatal("unsupported backend silently selected another control route")
	}
	backend := &fakeInputWindow{lease: &fakeInputLease{ended: make(chan struct{})}}
	guard := &WindowGuard{backend: backend}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := guard.AcquireInputLease(ctx); !errors.Is(err, context.Canceled) || backend.acquires.Load() != 0 {
		t.Fatal("pre-canceled acquisition performed native I/O")
	}
	guard.lost.Store(true)
	if _, err := guard.AcquireInputLease(t.Context()); !errors.Is(err, computer.ErrStale) ||
		backend.acquires.Load() != 0 {
		t.Fatal("lost original guard reacquired ownership")
	}
}

func TestInputLeaseCanceledCallerCannotUnlockOrResurrect(t *testing.T) {
	backend := &fakeInputWindow{lease: &fakeInputLease{ended: make(chan struct{})}}
	guard := &WindowGuard{backend: backend}
	ctx, cancel := context.WithCancel(t.Context())
	lease, err := guard.AcquireInputLease(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if !errors.Is(lease.Check(ctx), context.Canceled) {
		t.Fatal("canceled check admitted control")
	}
	select {
	case <-lease.Done():
		t.Fatal("acquisition context cancellation unlocked before owned-input cleanup")
	default:
	}
	if lease.Check(t.Context()) != nil || backend.acquires.Load() != 1 {
		t.Fatal("caller cancellation lost or reacquired native ownership")
	}
	var group sync.WaitGroup
	for range 20 {
		group.Go(func() {
			if err := lease.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	if backend.lease.closes.Load() != 1 || !errors.Is(lease.Check(t.Context()), computer.ErrStale) ||
		backend.acquires.Load() != 1 {
		t.Fatal("release repeated cleanup, resurrected or reacquired the lease")
	}
	select {
	case <-lease.Done():
	default:
		t.Fatal("release did not revoke ownership notification")
	}
}

func TestInputLeaseFailedFinalCheckNeverReturnsOwnership(t *testing.T) {
	for _, err := range []error{computer.ErrBusy, computer.ErrStale, computer.ErrUnavailable} {
		backend := &fakeInputWindow{lease: &fakeInputLease{ended: make(chan struct{}), err: err}}
		guard := &WindowGuard{backend: backend}
		lease, got := guard.AcquireInputLease(t.Context())
		if lease != nil || !errors.Is(got, err) || backend.lease.closes.Load() != 1 {
			t.Fatal("unverified ownership escaped admission or leaked cleanup", err, got)
		}
	}
	backend := &fakeInputWindow{err: computer.ErrBusy}
	if lease, err := (&WindowGuard{backend: backend}).AcquireInputLease(t.Context()); lease != nil ||
		!errors.Is(err, computer.ErrBusy) {
		t.Fatal("native contention became ownership")
	}
}

func TestInputLeaseZeroAndGuardCloseFailClosed(t *testing.T) {
	for _, lease := range []*InputLease{nil, {}} {
		if lease.Check(t.Context()) == nil || lease.Close() != nil {
			t.Fatal("zero input lease admitted ownership")
		}
		select {
		case <-lease.Done():
		default:
			t.Fatal("zero lease's terminal signal remained open")
		}
	}
	backend := &fakeInputWindow{lease: &fakeInputLease{ended: make(chan struct{})}}
	guard := &WindowGuard{backend: backend}
	lease, err := guard.AcquireInputLease(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = guard.Close(); err != nil || !errors.Is(lease.Check(t.Context()), computer.ErrStale) {
		t.Fatal("closed native guard admitted input ownership", err)
	}
	_ = lease.Close()
}
