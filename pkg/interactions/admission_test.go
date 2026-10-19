package interactions

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

func TestRegistryAdmissionCancellationPreservesStore(t *testing.T) {
	registry, clock, path := newTestRegistry(t)
	before, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	release, err := acquireStoreFileLock(context.Background(), path+".lock")
	if err != nil {
		t.Fatal(err)
	}
	release = sync.OnceFunc(release)
	defer release()
	for _, canceled := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		if canceled {
			cancel()
		}
		_, err := registry.CreateContext(ctx, validCreate(clock, "interaction_canceladmission", "session-cancel"))
		cancel()
		if !errors.Is(err, ErrStoreBusy) {
			t.Fatalf("expected busy: %v", err)
		}
		cause := context.DeadlineExceeded
		if canceled {
			cause = context.Canceled
		}
		if !errors.Is(err, cause) {
			t.Fatalf("lost context cause: %v", err)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if string(before) != string(after) || len(registry.List()) != 0 || len(registry.ListEvents("")) != 0 {
		t.Fatal("canceled admission changed durable or cached state")
	}
}

func TestRegistryStartupBusyCanRefreshAndRetry(t *testing.T) {
	original, clock, path := newTestRegistry(t)
	first, err := original.Create(validCreate(clock, "interaction_beforebusy", "session-before"))
	if err != nil {
		t.Fatal(err)
	}
	release, err := acquireStoreFileLock(context.Background(), path+".lock")
	if err != nil {
		t.Fatal(err)
	}
	release = sync.OnceFunc(release)
	defer release()
	registry := NewRegistryWithOptions(path, Options{StoreLockTimeout: 40 * time.Millisecond, Now: clock.Now})
	if !errors.Is(registry.LastLoadError(), ErrStoreBusy) {
		t.Fatalf("startup error: %v", registry.LastLoadError())
	}
	release()
	if err := registry.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if registry.LastLoadError() != nil {
		t.Fatalf("transient load error survived: %v", registry.LastLoadError())
	}
	if got, ok := registry.Get(first.ID); !ok || got.Revision != first.Revision {
		t.Fatal("refresh lost existing durable record")
	}
	if _, err := registry.Create(validCreate(clock, "interaction_afterbusy", "session-after")); err != nil {
		t.Fatal(err)
	}
	reopened := NewRegistry(path)
	if reopened.LastLoadError() != nil || len(reopened.List()) != 2 {
		t.Fatal("retry did not preserve both records")
	}
}

func TestRegistryLocalAdmissionSharesCallerBudget(t *testing.T) {
	registry, clock, _ := newTestRegistry(t)
	registry.mu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := registry.CreateContext(ctx, validCreate(clock, "interaction_localbusy", "local-session"))
		done <- err
	}()
	select {
	case err := <-done:
		registry.mu.Unlock()
		if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrStoreBusy) {
			t.Fatalf("local admission: %v", err)
		}
	case <-time.After(time.Second):
		registry.mu.Unlock()
		t.Fatal("local mutex ignored caller admission budget")
	}
	if _, err := registry.Create(validCreate(clock, "interaction_localretry", "local-session")); err != nil {
		t.Fatal(err)
	}
}
