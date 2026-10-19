package interactions

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSubscriptionUnsubscribeDoesNotNeedRegistryMutex(t *testing.T) {
	registry, _, _ := newTestRegistry(t)
	unsubscribe := registry.Subscribe(func(EventObservation) {})
	registry.mu.Lock()
	done := make(chan struct{})
	go func() { unsubscribe(); close(done) }()
	select {
	case <-done:
		registry.mu.Unlock()
	case <-time.After(time.Second):
		registry.mu.Unlock()
		t.Fatal("unsubscribe waited for registry mutex")
	}
}

func TestSubscriptionCloseAccountsForProducerAdmission(t *testing.T) {
	registry, _, _ := newTestRegistry(t)
	subscription, err := registry.SubscribeWithHandle(func(EventObservation) {})
	if err != nil {
		t.Fatal(err)
	}
	registry.mu.Lock()
	batch := registry.newNotificationBatchLocked()
	registry.mu.Unlock()
	defer batch.releaseAdmissions()
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	err = subscription.CloseContext(ctx)
	cancel()
	var pending *ObserverDrainError
	if !errors.As(err, &pending) || !errors.Is(err, ErrObserverPending) || pending.PendingWork != 1 {
		t.Fatalf("accepted producer was declared idle: %v", err)
	}
	batch.releaseAdmissions()
	if err := subscription.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSubscriptionClosePreservesAdmittedActiveDeliveries(t *testing.T) {
	registry, clock, _ := newTestRegistry(t)
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	var sequences []uint64
	subscription, err := registry.SubscribeWithHandle(func(o EventObservation) {
		if o.Event.CommitSequence == 1 {
			close(entered)
			<-release
		}
		sequences = append(sequences, o.Event.CommitSequence)
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := registry.Create(validCreate(clock, "interaction_activeclose", "session"))
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("callback did not start")
	}
	record, _ := registry.Get("interaction_activeclose")
	if _, err := registry.Cancel(record.ID, record.Revision, "queued_before_close"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	err = subscription.CloseContext(ctx)
	cancel()
	if !errors.Is(err, ErrObserverPending) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked callback falsely settled: %v", err)
	}
	if _, err := registry.Create(validCreate(clock, "interaction_afterclose", "other-session")); err != nil {
		t.Fatal(err)
	}
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("accepted observations did not settle")
	}
	if err := subscription.Close(); err != nil {
		t.Fatal(err)
	}
	if len(sequences) != 2 || sequences[0] != 1 || sequences[1] != 2 {
		t.Fatalf("accepted delivery lost or future admission escaped: %v", sequences)
	}
	if registry.Stats().PendingObservations != 0 {
		t.Fatal("settled callback leaked budget")
	}
}

func TestSubscriptionCloseGatedRequiresSnapshotWithoutReplaying(t *testing.T) {
	registry, clock, _ := newTestRegistry(t)
	callbacks := 0
	_, subscription, err := registry.SubscribeSnapshotWithHandle(func(EventObservation) { callbacks++ })
	if err != nil {
		t.Fatal(err)
	}
	record, err := registry.Create(validCreate(clock, "interaction_gatedclose", "session"))
	if err != nil {
		t.Fatal(err)
	}
	err = subscription.Close()
	if !errors.Is(err, ErrObserverResyncRequired) || errors.Is(err, ErrObserverPending) {
		t.Fatalf("gated cancellation disposition: %v", err)
	}
	subscription.Activate()
	if callbacks != 0 || registry.Stats().PendingObservations != 0 {
		t.Fatal("abandoned gate ran callback or leaked budget")
	}
	if !errors.Is(subscription.Close(), ErrObserverResyncRequired) {
		t.Fatal("resync requirement disappeared")
	}
	snapshot, replacement, err := registry.SubscribeSnapshotWithHandle(func(o EventObservation) {
		callbacks++
		if o.Event.CommitSequence != 2 {
			t.Error("snapshot recovery replayed an old event")
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Unsubscribe()
	if snapshot.Through != 1 || len(snapshot.Records) != 1 {
		t.Fatal("fresh snapshot lost authoritative commit")
	}
	replacement.Activate()
	if _, err := registry.Cancel(record.ID, record.Revision, "new_commit"); err != nil {
		t.Fatal(err)
	}
	if callbacks != 1 {
		t.Fatal("resnapshot replayed effects or lost a future event")
	}
}

func TestRegistryCloseBoundsRunningCallbackAndAllowsSettlementRetry(t *testing.T) {
	registry, clock, _ := newTestRegistry(t)
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	registry.Subscribe(func(EventObservation) { close(entered); <-release })
	done := make(chan error, 1)
	go func() {
		_, err := registry.Create(validCreate(clock, "interaction_registryclose", "session"))
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("callback did not start")
	}
	started := time.Now()
	err := registry.Close()
	elapsed := time.Since(started)
	if !errors.Is(err, ErrObserverPending) || !errors.Is(err, context.DeadlineExceeded) || elapsed > 2*time.Second {
		t.Fatalf("unbounded or false callback settlement: elapsed=%s error=%v", elapsed, err)
	}
	select {
	case <-done:
		t.Fatal("close claimed to interrupt executing callback")
	default:
	}
	if _, err := registry.Create(
		validCreate(clock, "interaction_rejectedclose", "other"),
	); !errors.Is(
		err,
		ErrStoreClosed,
	) {
		t.Fatalf("closed store admitted write: %v", err)
	}
	if _, err := registry.SubscribeWithHandle(func(EventObservation) {}); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("closed store admitted observer: %v", err)
	}
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("callback did not finish")
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("blocked observer shutdown: elapsed=%s timeout_explicit=true retry_settled=true", elapsed)
}

func TestRegistryCloseAdmissionDeadlineSealsBeforeTakingMutex(t *testing.T) {
	registry, clock, _ := newTestRegistry(t)
	registry.mu.Lock()
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	err := registry.CloseContext(ctx)
	cancel()
	if !errors.Is(err, ErrObserverPending) || !errors.Is(err, context.DeadlineExceeded) {
		registry.mu.Unlock()
		t.Fatalf("mutex deadline: %v", err)
	}
	if _, err := registry.Create(
		validCreate(clock, "interaction_sealedclose", "session"),
	); !errors.Is(
		err,
		ErrStoreClosed,
	) {
		registry.mu.Unlock()
		t.Fatalf("close did not seal admission: %v", err)
	}
	registry.mu.Unlock()
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryCloseReportsAbandonedGate(t *testing.T) {
	registry, clock, _ := newTestRegistry(t)
	_, _, unsubscribe := registry.SubscribeSnapshot(func(EventObservation) { t.Error("gate escaped") })
	defer unsubscribe()
	if _, err := registry.Create(validCreate(clock, "interaction_closegate", "session")); err != nil {
		t.Fatal(err)
	}
	if err := registry.Close(); !errors.Is(err, ErrObserverResyncRequired) || errors.Is(err, ErrObserverPending) {
		t.Fatalf("closed gate disposition: %v", err)
	}
	if registry.Stats().PendingObservations != 0 {
		t.Fatal("closed gate retained payload")
	}
}

func TestSubscriptionCloseFailedPersistenceRetiresProducer(t *testing.T) {
	_, clock, path := newTestRegistry(t)
	registry := NewRegistryWithOptions(path, Options{Now: clock.Now, MaxSnapshotBytes: 1})
	subscription, err := registry.SubscribeWithHandle(
		func(EventObservation) { t.Error("failed persistence notified observer") },
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Create(
		validCreate(clock, "interaction_closefailedsave", "session"),
	); !errors.Is(
		err,
		ErrSnapshotOverBudget,
	) {
		t.Fatalf("save failure: %v", err)
	}
	if err := subscription.Close(); err != nil {
		t.Fatalf("failed producer leaked admission: %v", err)
	}
}

func TestRegistryCloseConcurrentProducersLeaveNoLateCallbacks(t *testing.T) {
	_, clock, _ := newTestRegistry(t)
	registry := NewRegistryWithOptions("", Options{Now: clock.Now})
	var returned atomic.Bool
	var late atomic.Int64
	registry.Subscribe(func(EventObservation) {
		if returned.Load() {
			late.Add(1)
		}
	})
	var writers sync.WaitGroup
	results := make(chan error, 32)
	for i := range 32 {
		writers.Add(1)
		go func() {
			defer writers.Done()
			_, err := registry.Create(
				validCreate(clock, fmt.Sprintf("interaction_%08dclosing", i), fmt.Sprintf("session-%d", i)),
			)
			results <- err
		}()
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
	returned.Store(true)
	writers.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else if !errors.Is(err, ErrStoreClosed) {
			t.Fatal(err)
		}
	}
	if late.Load() != 0 || len(registry.List()) != accepted || registry.Stats().PendingObservations != 0 {
		t.Fatalf("shutdown lost ownership: late=%d accepted=%d stats=%+v", late.Load(), accepted, registry.Stats())
	}
}

func TestSubscriptionCloseFromCallbackReportsItsOwnPendingWork(t *testing.T) {
	_, clock, _ := newTestRegistry(t)
	registry := NewRegistryWithOptions("", Options{Now: clock.Now})
	var subscription *Subscription
	var closeErr error
	subscription, err := registry.SubscribeWithHandle(func(EventObservation) {
		ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
		defer cancel()
		closeErr = subscription.CloseContext(ctx)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Create(validCreate(clock, "interaction_selfclose", "session")); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(closeErr, ErrObserverPending) {
		t.Fatalf("reentrant close falsely settled: %v", closeErr)
	}
	if err := subscription.Close(); err != nil {
		t.Fatal(err)
	}
}
