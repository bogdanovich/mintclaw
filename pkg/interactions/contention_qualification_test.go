package interactions

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// This qualification enforces the production count limit, including a stalled
// callback, while retained records/events stay small. Durable rollback and byte
// admission are exercised separately in notification_budget_test.go.
func TestRegistryContentionQualificationStalledObserver(t *testing.T) {
	for attempt := range 3 {
		t.Run(fmt.Sprintf("attempt-%d", attempt+1), qualifyRegistryStalledObserver)
	}
}

func qualifyRegistryStalledObserver(t *testing.T) {
	_, clock, _ := newTestRegistry(t)
	registry := NewRegistryWithOptions(
		"",
		Options{
			Now:               clock.Now,
			TerminalRetention: time.Millisecond,
			MaxRecords:        2,
			MaxEvents:         4,
			MaxSnapshotBytes:  16 * 1024,
		},
	)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	var observedMu sync.Mutex
	var sequences []uint64
	unsubscribe := registry.Subscribe(func(observation EventObservation) {
		if observation.Event.CommitSequence == 1 {
			close(entered)
			<-release
		}
		observedMu.Lock()
		sequences = append(sequences, observation.Event.CommitSequence)
		observedMu.Unlock()
	})
	// Unsubscribe is awaited with a deadline below. A synchronous deferred
	// unsubscribe could block failure reporting behind a stalled producer.
	firstDone := make(chan error, 1)
	go func() {
		_, err := registry.Create(validCreate(clock, "interaction_00000000qualify", "session-first"))
		firstDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("observer did not enter")
	}
	const records = 127
	start := time.Now()
	produce := func() error {
		for i := range records {
			record, err := registry.Create(
				validCreate(clock, fmt.Sprintf("interaction_%08dqualify", i+1), fmt.Sprintf("session-%d", i)),
			)
			if err != nil {
				return fmt.Errorf("create interaction %d: %w", i, err)
			}
			if _, err := registry.Cancel(record.ID, record.Revision, "qualification_cancel"); err != nil {
				return fmt.Errorf("cancel interaction %d: %w", i, err)
			}
			clock.Advance(2 * time.Millisecond)
			if err := registry.Prune(clock.Now()); err != nil {
				return fmt.Errorf("prune interaction %d: %w", i, err)
			}
		}
		return nil
	}
	producerDone := make(chan error, 1)
	go func() { producerDone <- produce() }()
	select {
	case err := <-producerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		// A blocked observer must not prevent the harness from reporting failure.
		// Release it before waiting for producer cleanup or failing the subtest.
		unblock()
		select {
		case <-producerDone:
		case <-time.After(3 * time.Second):
			t.Fatal("producer did not settle after releasing the stalled callback")
		}
		t.Fatal("producer burst exceeded its deadline while the callback was stalled")
	}
	commitsElapsed := time.Since(start)
	last, err := registry.Create(validCreate(clock, "interaction_99999999qualify", "session-last"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Cancel(last.ID, last.Revision, "over_budget"); !errors.Is(err, ErrObserverBacklog) {
		t.Fatalf("count admission: %v", err)
	}
	stats := registry.Stats()
	if stats.PendingObservations != DefaultMaxPendingObservations ||
		stats.PendingObservationBytes > DefaultMaxPendingObservationBytes {
		t.Fatalf("outstanding work escaped bounds: %+v", stats)
	}
	if got, _ := registry.Get(last.ID); got.Revision != last.Revision {
		t.Fatal("rejected mutation changed record")
	}
	t.Logf(
		"stalled observer: admitted=%d elapsed=%s outstanding=%d accounted_payload_bytes=%d count_limit=%d byte_limit=%d retained_records=%d retained_events=%d",
		2+2*records,
		commitsElapsed,
		stats.PendingObservations,
		stats.PendingObservationBytes,
		stats.MaxPendingObservations,
		stats.MaxPendingObservationBytes,
		stats.RecordCount,
		stats.EventCount,
	)
	// Removing a subscriber must return while its callback is blocked. Already
	// committed notifications still belong to it; later commits do not.
	unsubscribeDone := make(chan struct{})
	go func() { unsubscribe(); close(unsubscribeDone) }()
	select {
	case <-unsubscribeDone:
	case <-time.After(3 * time.Second):
		t.Fatal("unsubscribe blocked behind callback")
	}
	unblock()
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("observer queue failed to drain")
	}
	observedMu.Lock()
	defer observedMu.Unlock()
	if len(sequences) != 2+2*records {
		t.Fatalf("lost committed notifications: got=%d want=%d", len(sequences), 2+2*records)
	}
	for index, sequence := range sequences {
		if sequence != uint64(index+1) {
			t.Fatalf("reordered notification: index=%d sequence=%d", index, sequence)
		}
	}
	if stats := registry.Stats(); stats.PendingObservations != 0 || stats.PendingObservationBytes != 0 {
		t.Fatalf("budget leak: %+v", stats)
	}
	if _, err := registry.Cancel(last.ID, last.Revision, "retry_after_drain"); err != nil {
		t.Fatal(err)
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	if len(registry.notifications) != 0 || registry.notifying {
		t.Fatal("notification drain did not finish")
	}
}
