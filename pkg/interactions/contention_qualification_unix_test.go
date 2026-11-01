//go:build !windows

package interactions

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestRegistryContentionQualificationLiveLockHolder(t *testing.T) {
	for attempt := range 3 {
		t.Run(fmt.Sprintf("attempt-%d", attempt+1), qualifyRegistryLiveLockHolder)
	}
}

func qualifyRegistryLiveLockHolder(t *testing.T) {
	registry, clock, path := newTestRegistry(t)
	unsubscribe := registry.Subscribe(func(EventObservation) {})
	defer unsubscribe()
	release, err := acquireStoreFileLock(context.Background(), path+".lock")
	if err != nil {
		t.Fatal(err)
	}
	release = sync.OnceFunc(release)
	defer release()
	started := time.Now()
	const writers = 4
	done := make(chan error, writers)
	for i := range writers {
		go func() {
			_, err := registry.Create(
				validCreate(clock, fmt.Sprintf("interaction_lock%dqualify", i), fmt.Sprintf("lock-session-%d", i)),
			)
			done <- err
		}()
	}
	readDone := make(chan struct{})
	go func() { registry.Get("interaction_missing"); unsubscribe(); close(readDone) }()
	select {
	case <-readDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("file-lock admission blocked reads/unsubscribe")
	}
	for range writers {
		select {
		case err := <-done:
			var busy *StoreBusyError
			if !errors.As(err, &busy) || !errors.Is(err, ErrStoreBusy) || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("expected retryable deadline, got %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("writers serialized their admission budgets")
		}
	}
	waited := time.Since(started)
	if waited > 2*time.Second {
		t.Fatalf("independent admission took %s", waited)
	}
	if len(registry.List()) != 0 {
		t.Fatal("busy writers mutated records")
	}
	release()
	// Admission can recover on the same registry after the holder releases.
	record, err := registry.Create(validCreate(clock, "interaction_lockqualify", "lock-session"))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := registry.Get(record.ID); !ok || got.Revision != 1 {
		t.Fatalf("retry lost: %#v", got)
	}
	t.Logf(
		"live lock holder: writers=%d admission=%s; reads/unsubscribe available; retry committed revision=%d",
		writers,
		waited,
		record.Revision,
	)
}
