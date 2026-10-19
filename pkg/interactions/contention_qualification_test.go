package interactions

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

// This bounded qualification checks delivery after a controlled stalled
// observer. Queue measurements are observations, not a production queue limit.
func TestRegistryContentionQualificationStalledObserver(t *testing.T) {
	for attempt := range 3 {
		t.Run(fmt.Sprintf("attempt-%d", attempt+1), qualifyRegistryStalledObserver)
	}
}

func qualifyRegistryStalledObserver(t *testing.T) {
	_, clock, path := newTestRegistry(t)
	registry := NewRegistryWithOptions(
		path,
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
	const records = 128
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
	registry.mu.RLock()
	queued := len(registry.notifications)
	observations := make([]EventObservation, 0, queued)
	for _, notification := range registry.notifications {
		observations = append(observations, notification.observation)
	}
	encoded, err := json.Marshal(observations)
	registry.mu.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	stats := registry.Stats()
	t.Logf(
		"stalled observer: concurrent_commits=%d elapsed=%s queued=%d queue_json_bytes=%d retained_records=%d retained_events=%d snapshot_bytes=%d snapshot_limit=%d",
		2*records,
		commitsElapsed,
		queued,
		len(encoded),
		stats.RecordCount,
		stats.EventCount,
		stats.SnapshotBytes,
		stats.MaxSnapshotBytes,
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
	if len(sequences) != 1+2*records {
		t.Fatalf("lost committed notifications: got=%d want=%d", len(sequences), 1+2*records)
	}
	for index, sequence := range sequences {
		if sequence != uint64(index+1) {
			t.Fatalf("reordered notification: index=%d sequence=%d", index, sequence)
		}
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	if len(registry.notifications) != 0 || registry.notifying {
		t.Fatal("notification drain did not finish")
	}
}
