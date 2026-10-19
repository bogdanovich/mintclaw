//go:build !windows

package interactions

import (
	"fmt"
	"os"
	"sync"
	"syscall"
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
	lock, err := os.OpenFile(path+".lock", os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	var release sync.Once
	unlock := func() {
		release.Do(func() {
			if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
				t.Error(err)
			}
		})
	}
	defer unlock()
	start := time.Now()
	commitDone := make(chan error, 1)
	go func() {
		_, err := registry.Create(validCreate(clock, "interaction_lockqualify", "lock-session"))
		commitDone <- err
	}()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for registry.mu.TryLock() {
		registry.mu.Unlock()
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("writer did not reach store lock")
		}
	}
	readDone := make(chan struct{})
	go func() { registry.Get("interaction_lockqualify"); close(readDone) }()
	unsubscribeDone := make(chan struct{})
	go func() { unsubscribe(); close(unsubscribeDone) }()
	select {
	case err := <-commitDone:
		t.Fatalf("commit escaped held lock: %v", err)
	case <-readDone:
		t.Fatal("read escaped writer-held registry mutex")
	case <-unsubscribeDone:
		t.Fatal("unsubscribe escaped writer-held registry mutex")
	case <-time.After(1250 * time.Millisecond):
	}
	heldFor := time.Since(start)
	unlock()
	select {
	case err := <-commitDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("writer failed to recover")
	}
	select {
	case <-readDone:
	case <-time.After(3 * time.Second):
		t.Fatal("reader failed to recover")
	}
	select {
	case <-unsubscribeDone:
	case <-time.After(3 * time.Second):
		t.Fatal("unsubscribe failed to recover")
	}
	record, ok := registry.Get("interaction_lockqualify")
	if !ok || record.Status != StatusCreated {
		t.Fatalf("commit lost: %#v", record)
	}
	t.Logf(
		"live lock holder: held_for=%s total_recovery=%s; commit/read/unsubscribe waited for release",
		heldFor,
		time.Since(start),
	)
}
