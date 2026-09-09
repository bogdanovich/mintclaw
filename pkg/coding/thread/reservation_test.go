package thread

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

const (
	reservationCrashRootEnv = "MINTCLAW_TEST_RESERVATION_CRASH_ROOT"
	reservationCrashIDEnv   = "MINTCLAW_TEST_RESERVATION_CRASH_ID"
	reservationCrashExit    = 73
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

func TestReserveThreadLeaseKeepsAuthorityWhenPreparationRootCloseFails(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "coding"))
	if err != nil {
		t.Fatal(err)
	}
	injectedErr := errors.New("injected root close failure")
	store.closeReservationRoots = func(threadRoot, threadsRoot *os.Root) error {
		return errors.Join(threadRoot.Close(), threadsRoot.Close(), injectedErr)
	}
	threadID := uuid.NewString()
	lease, err := store.ReserveThreadLease(threadID)
	if err != nil {
		t.Fatalf("ReserveThreadLease() error = %v", err)
	}
	if err := store.ValidateLease(lease, threadID); err != nil {
		t.Fatalf("ValidateLease() error = %v", err)
	}
	contenderStore, err := NewStore(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	if contender, err := contenderStore.AcquireLease(threadID); !errors.Is(err, ErrLeaseBusy) {
		if contender != nil {
			_ = contender.Release()
		}
		t.Fatalf("AcquireLease(contender) error = %v, want %v", err, ErrLeaseBusy)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	successor, err := contenderStore.AcquireLease(threadID)
	if err != nil {
		t.Fatalf("AcquireLease(after release) error = %v", err)
	}
	if err := successor.Release(); err != nil {
		t.Fatalf("successor Release() error = %v", err)
	}
}

func TestReserveThreadLeaseRetainsAuthorityWhenPublishedQuarantineFails(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "coding"))
	if err != nil {
		t.Fatal(err)
	}
	threadID := uuid.NewString()
	injectedSyncErr := errors.New("injected published reservation sync failure")
	injectedQuarantineErr := errors.New("injected reservation quarantine failure")
	originalRename := store.renameReservation
	store.renameReservation = func(root *os.Root, oldName, newName string) error {
		if oldName == threadID {
			return injectedQuarantineErr
		}
		return originalRename(root, oldName, newName)
	}
	store.afterThreadReservationPublished = func() {
		store.syncRoot = func(*os.Root) error { return injectedSyncErr }
	}
	var retained *Lease
	store.retainReservationLease = func(lease *Lease) { retained = lease }

	lease, err := store.ReserveThreadLease(threadID)
	if lease != nil {
		_ = lease.Release()
		t.Fatal("ReserveThreadLease() returned a lease with a failed reservation")
	}
	if !errors.Is(err, injectedSyncErr) || !errors.Is(err, injectedQuarantineErr) {
		t.Fatalf("ReserveThreadLease() error = %v, want sync and quarantine failures", err)
	}
	if retained == nil {
		t.Fatal("failed active reservation did not retain its writer lease")
	}
	if err := store.ValidateLease(retained, threadID); err != nil {
		t.Fatalf("ValidateLease(retained) error = %v", err)
	}

	contenderStore, err := NewStore(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	if contender, err := contenderStore.AcquireLease(threadID); !errors.Is(err, ErrLeaseBusy) {
		if contender != nil {
			_ = contender.Release()
		}
		t.Fatalf("AcquireLease(contender) error = %v, want %v", err, ErrLeaseBusy)
	}
	if err := retained.Release(); err != nil {
		t.Fatalf("Release(retained) error = %v", err)
	}
	successor, err := contenderStore.AcquireLease(threadID)
	if err != nil {
		t.Fatalf("AcquireLease(after retained release) error = %v", err)
	}
	if err := successor.Release(); err != nil {
		t.Fatalf("successor Release() error = %v", err)
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
	continuePreparation := make(chan struct{})
	published := make(chan struct{})
	continuePublication := make(chan struct{})
	store.afterThreadReservationPrepared = func(string) {
		close(reserved)
		<-continuePreparation
	}
	store.afterThreadReservationPublished = func() {
		close(published)
		<-continuePublication
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
	if _, err := os.Stat(filepath.Join(store.Root(), "threads", threadID)); !errors.Is(err, os.ErrNotExist) {
		close(continuePreparation)
		close(continuePublication)
		t.Fatalf("prepared reservation was active before its first lease: %v", err)
	}

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
		close(continuePreparation)
		close(continuePublication)
		t.Fatalf("AcquireLease() completed before reservation handoff: %v", result.err)
	case <-time.After(100 * time.Millisecond):
	}
	close(continuePreparation)
	select {
	case <-published:
	case creator := <-creatorResult:
		close(continuePublication)
		t.Fatalf("ReserveThreadLease() failed before publication: %v", creator.err)
	case <-time.After(5 * time.Second):
		close(continuePublication)
		t.Fatal("ReserveThreadLease() did not publish")
	}
	if _, err := os.Stat(filepath.Join(store.Root(), "threads", threadID)); err != nil {
		close(continuePublication)
		t.Fatalf("published reservation is not active: %v", err)
	}
	select {
	case result := <-contenderResult:
		if result.lease != nil {
			_ = result.lease.Release()
		}
		close(continuePublication)
		t.Fatalf("AcquireLease() bypassed the publication gate: %v", result.err)
	case <-time.After(100 * time.Millisecond):
	}
	close(continuePublication)

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

func TestReserveThreadLeaseCrashBeforePublicationLeavesNoActiveThread(t *testing.T) {
	if root := os.Getenv(reservationCrashRootEnv); root != "" {
		store, err := NewStore(root)
		if err != nil {
			os.Exit(reservationCrashExit + 1)
		}
		store.afterThreadReservationPrepared = func(string) { os.Exit(reservationCrashExit) }
		_, _ = store.ReserveThreadLease(os.Getenv(reservationCrashIDEnv))
		os.Exit(reservationCrashExit + 2)
	}

	root := filepath.Join(t.TempDir(), "coding")
	threadID := uuid.NewString()
	command := exec.Command(os.Args[0], "-test.run=^TestReserveThreadLeaseCrashBeforePublicationLeavesNoActiveThread$")
	command.Env = append(
		os.Environ(),
		reservationCrashRootEnv+"="+root,
		reservationCrashIDEnv+"="+threadID,
	)
	err := command.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != reservationCrashExit {
		t.Fatalf("crash helper error = %v, want exit %d", err, reservationCrashExit)
	}
	activePath := filepath.Join(root, "threads", threadID)
	if _, err := os.Stat(activePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("active thread after pre-publication crash: %v", err)
	}
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if lease, err := store.AcquireLease(threadID); !errors.Is(err, os.ErrNotExist) {
		if lease != nil {
			_ = lease.Release()
		}
		t.Fatalf("AcquireLease(crashed preparation) error = %v, want not-exist", err)
	}
}

func TestReserveThreadLeaseRemovesFailedPreparation(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "coding"))
	if err != nil {
		t.Fatal(err)
	}
	store.afterThreadReservationPrepared = func(stagingName string) {
		lockPath := filepath.Join(store.Root(), "threads", stagingName, leaseFileName)
		if mkdirErr := os.Mkdir(lockPath, 0o700); mkdirErr != nil {
			t.Errorf("prepare invalid lock: %v", mkdirErr)
		}
	}
	threadID := uuid.NewString()
	if lease, err := store.ReserveThreadLease(threadID); err == nil {
		if lease != nil {
			_ = lease.Release()
		}
		t.Fatal("ReserveThreadLease() accepted a directory as its lock file")
	}
	entries, err := os.ReadDir(filepath.Join(store.Root(), "threads"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed preparation left thread entries: %v", entries)
	}
}
