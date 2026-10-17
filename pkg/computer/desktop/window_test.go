package desktop

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/computer"
)

type fakeWindowBackend struct {
	valid  atomic.Bool
	checks atomic.Int32
	closes atomic.Int32
}

func (backend *fakeWindowBackend) alive(context.Context) bool {
	backend.checks.Add(1)
	return backend.valid.Load()
}
func (backend *fakeWindowBackend) close() error { backend.closes.Add(1); return nil }

func TestWindowGuardNeverReacquiresAfterLoss(t *testing.T) {
	backend := &fakeWindowBackend{}
	backend.valid.Store(true)
	guard := &WindowGuard{identity: WindowIdentity{PID: 123, WindowID: 456, Generation: 789}, backend: backend}
	if !guard.Alive(t.Context()) {
		t.Fatal("live initial resource refused")
	}
	identity := guard.Identity()
	identity.Generation++
	if guard.Identity().Generation == identity.Generation {
		t.Fatal("caller changed native identity")
	}
	backend.valid.Store(false)
	if guard.Alive(t.Context()) {
		t.Fatal("lost resource accepted")
	}
	backend.valid.Store(true)
	if guard.Alive(t.Context()) || backend.checks.Load() != 2 {
		t.Fatal("lost window ID was reacquired")
	}
	var group sync.WaitGroup
	for range 20 {
		group.Go(func() { _ = guard.Close() })
	}
	group.Wait()
	if backend.closes.Load() != 1 {
		t.Fatal("native handles closed multiple times")
	}
}

func TestWindowGuardCanceledCallerDoesNotDestroyIdentity(t *testing.T) {
	backend := &fakeWindowBackend{}
	backend.valid.Store(true)
	guard := &WindowGuard{backend: backend}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if guard.Alive(ctx) || backend.checks.Load() != 0 {
		t.Fatal("canceled caller performed native IO")
	}
	if !guard.Alive(t.Context()) {
		t.Fatal("caller cancellation destroyed native resource identity")
	}
	if err := guard.Close(); err != nil || guard.Alive(t.Context()) {
		t.Fatal("closed guard accepted", err)
	}
}

func TestWindowGuardRefusesInvalidRequestsWithoutNativeIO(t *testing.T) {
	for _, request := range []WindowRequest{{}, {PID: 1}, {WindowID: 1}, {PID: 1 << 31, WindowID: 1}} {
		if _, err := WatchWindow(t.Context(), request); !errors.Is(err, computer.ErrInvalid) {
			t.Fatal(request, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := WatchWindow(ctx, WindowRequest{PID: 1, WindowID: 1}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var guard WindowGuard
	if guard.Alive(t.Context()) || guard.Close() != nil {
		t.Fatal("zero guard must fail closed")
	}
}
