package thread

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestReserveThreadLeaseIsExclusiveAndDoesNotPublishMetadata(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "coding"))
	if err != nil {
		t.Fatal(err)
	}
	contenderStore, err := NewStore(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	threadID := uuid.NewString()
	lease, err := store.ReserveThreadLease(threadID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Release() }()
	if err := store.ValidateLease(lease, threadID); err != nil {
		t.Fatalf("ValidateLease() error = %v", err)
	}
	if contender, err := contenderStore.AcquireLease(threadID); !errors.Is(err, ErrLeaseBusy) {
		if contender != nil {
			_ = contender.Release()
		}
		t.Fatalf("AcquireLease(contender) error = %v, want %v", err, ErrLeaseBusy)
	}
	root, err := store.ThreadRoot(threadID)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("reserved thread mode = %v", info.Mode())
	}
	if _, err := store.Load(threadID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reserved thread published metadata: %v", err)
	}
	marker := filepath.Join(root, "partial-state")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if duplicate, err := store.ReserveThreadLease(threadID); !errors.Is(err, ErrThreadExists) {
		if duplicate != nil {
			_ = duplicate.Release()
		}
		t.Fatalf("second ReserveThreadLease() error = %v, want %v", err, ErrThreadExists)
	}
	if content, err := os.ReadFile(marker); err != nil || string(content) != "keep" {
		t.Fatalf("second reservation changed partial state: %q, %v", content, err)
	}
}

func TestReserveThreadLeaseHasOneConcurrentWinner(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "coding"))
	if err != nil {
		t.Fatal(err)
	}
	threadID := uuid.NewString()
	start := make(chan struct{})
	var winners atomic.Int32
	var unexpected atomic.Int32
	unexpectedErrors := make(chan error, 8)
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			lease, err := store.ReserveThreadLease(threadID)
			switch {
			case err == nil:
				winners.Add(1)
				if releaseErr := lease.Release(); releaseErr != nil {
					unexpected.Add(1)
					unexpectedErrors <- releaseErr
				}
			case errors.Is(err, ErrThreadExists):
			default:
				unexpected.Add(1)
				unexpectedErrors <- err
			}
		}()
	}
	close(start)
	wait.Wait()
	close(unexpectedErrors)
	if winners.Load() != 1 || unexpected.Load() != 0 {
		var observed []error
		for err := range unexpectedErrors {
			observed = append(observed, err)
		}
		t.Fatalf(
			"reservation outcomes = %d winners, %d unexpected: %v",
			winners.Load(),
			unexpected.Load(),
			observed,
		)
	}
}

func TestReserveThreadLeaseDoesNotExposeUnlockedReservation(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "coding"))
	if err != nil {
		t.Fatal(err)
	}
	contenderStore, err := NewStore(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	reserved := make(chan struct{})
	continueReservation := make(chan struct{})
	store.afterThreadReservation = func() {
		close(reserved)
		<-continueReservation
	}
	threadID := uuid.NewString()
	type leaseResult struct {
		lease *Lease
		err   error
	}
	creatorResult := make(chan leaseResult, 1)
	go func() {
		lease, reserveErr := store.ReserveThreadLease(threadID)
		creatorResult <- leaseResult{lease: lease, err: reserveErr}
	}()
	<-reserved

	contenderResult := make(chan leaseResult, 1)
	go func() {
		lease, acquireErr := contenderStore.AcquireLease(threadID)
		contenderResult <- leaseResult{lease: lease, err: acquireErr}
	}()
	select {
	case result := <-contenderResult:
		if result.lease != nil {
			_ = result.lease.Release()
		}
		close(continueReservation)
		t.Fatalf("AcquireLease() completed before reservation handoff: %v", result.err)
	case <-time.After(100 * time.Millisecond):
	}
	close(continueReservation)

	creator := <-creatorResult
	if creator.err != nil {
		t.Fatalf("ReserveThreadLease() error = %v", creator.err)
	}
	defer func() { _ = creator.lease.Release() }()
	contender := <-contenderResult
	if contender.lease != nil {
		_ = contender.lease.Release()
	}
	if !errors.Is(contender.err, ErrLeaseBusy) {
		t.Fatalf("AcquireLease() error = %v, want %v", contender.err, ErrLeaseBusy)
	}
}
