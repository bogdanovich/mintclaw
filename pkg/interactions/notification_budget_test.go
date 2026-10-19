package interactions

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

func TestRegistryNotificationByteAdmissionAndGatedRecovery(t *testing.T) {
	_, clock, path := newTestRegistry(t)
	registry := NewRegistryWithOptions(
		path,
		Options{Now: clock.Now, MaxPendingObservations: 100, MaxPendingObservationBytes: 8192},
	)
	var sequences []uint64
	_, activate, unsubscribe := registry.SubscribeSnapshot(
		func(o EventObservation) { sequences = append(sequences, o.Event.CommitSequence) },
	)
	defer unsubscribe()
	var rejected CreateRequest
	for i := range 100 {
		request := validCreate(clock, fmt.Sprintf("interaction_%08dbyte", i), fmt.Sprintf("session-%d", i))
		before, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		_, err = registry.Create(request)
		if errors.Is(err, ErrObserverBacklog) {
			rejected = request
			after, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(before) != string(after) {
				t.Fatal("backlog rejection mutated durable state")
			}
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	stats := registry.Stats()
	if rejected.ID == "" || stats.PendingObservations == 0 || stats.PendingObservations >= 100 ||
		stats.PendingObservationBytes > 8192 {
		t.Fatalf("byte admission did not bound gated buffer: %+v", stats)
	}
	if len(sequences) != 0 {
		t.Fatal("gated observations escaped")
	}
	activate()
	for i, sequence := range sequences {
		if sequence != uint64(i+1) {
			t.Fatal("gated delivery reordered")
		}
	}
	if len(sequences) != stats.PendingObservations {
		t.Fatal("gated delivery lost committed event")
	}
	if stats := registry.Stats(); stats.PendingObservations != 0 || stats.PendingObservationBytes != 0 {
		t.Fatalf("gated budget leak: %+v", stats)
	}
	if _, err := registry.Create(rejected); err != nil {
		t.Fatal(err)
	}
	reopened := NewRegistryWithOptions(path, Options{Now: clock.Now})
	if reopened.LastLoadError() != nil || len(reopened.List()) != len(sequences) {
		t.Fatal("durable retry lost records")
	}
}

func TestRegistryNotificationBudgetConcurrentAdmission(t *testing.T) {
	_, clock, _ := newTestRegistry(t)
	// Allow queued writers to reach notification admission under race-instrumented load.
	registry := NewRegistryWithOptions(
		"",
		Options{Now: clock.Now, MaxPendingObservations: 32, StoreLockTimeout: 10 * time.Second},
	)
	var sequences []uint64
	_, activate, unsubscribe := registry.SubscribeSnapshot(
		func(o EventObservation) { sequences = append(sequences, o.Event.CommitSequence) },
	)
	defer unsubscribe()
	var writers sync.WaitGroup
	results := make(chan error, 128)
	for i := range 128 {
		writers.Add(1)
		go func() {
			defer writers.Done()
			_, err := registry.Create(
				validCreate(clock, fmt.Sprintf("interaction_%08dparallel", i), fmt.Sprintf("session-%d", i)),
			)
			results <- err
		}()
	}
	writers.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else if !errors.Is(err, ErrObserverBacklog) {
			t.Fatal(err)
		}
	}
	if accepted != 32 || len(registry.List()) != 32 || registry.Stats().PendingObservations != 32 {
		t.Fatalf("concurrent admission: accepted=%d stats=%+v", accepted, registry.Stats())
	}
	activate()
	if len(sequences) != 32 {
		t.Fatal("missing committed observations")
	}
	for i, sequence := range sequences {
		if sequence != uint64(i+1) {
			t.Fatal("commit identity gap")
		}
	}
}

func TestRegistryNotificationBudgetReentrantAtCapacity(t *testing.T) {
	_, clock, _ := newTestRegistry(t)
	registry := NewRegistryWithOptions("", Options{Now: clock.Now, MaxPendingObservations: 1})
	var reentrantErr error
	registry.Subscribe(func(o EventObservation) {
		if o.Event.Type == EventCreated {
			_, reentrantErr = registry.Cancel(o.Record.ID, o.Record.Revision, "reentrant")
		}
	})
	record, err := registry.Create(validCreate(clock, "interaction_reentrantlimit", "session"))
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(reentrantErr, ErrObserverBacklog) {
		t.Fatalf("reentry did not reject immediately: %v", reentrantErr)
	}
	if got, _ := registry.Get(record.ID); got.Revision != 1 {
		t.Fatal("rejected reentry changed record")
	}
	if _, err := registry.Cancel(record.ID, record.Revision, "retry"); err != nil {
		t.Fatal(err)
	}
	events := registry.ListEvents(record.ID)
	if len(events) != 2 || events[1].CommitSequence != 2 {
		t.Fatal("rejected reentry consumed commit identity")
	}
}

func TestRegistryNotificationBudgetPersistenceFailureReleasesReservation(t *testing.T) {
	_, clock, path := newTestRegistry(t)
	registry := NewRegistryWithOptions(path, Options{Now: clock.Now, MaxPendingObservations: 1, MaxSnapshotBytes: 1})
	_, activate, unsubscribe := registry.SubscribeSnapshot(func(EventObservation) {})
	defer unsubscribe()
	request := validCreate(clock, "interaction_failedreservation", "session")
	if _, err := registry.Create(request); !errors.Is(err, ErrSnapshotOverBudget) {
		t.Fatalf("persistence failure: %v", err)
	}
	if stats := registry.Stats(); stats.PendingObservations != 0 || stats.PendingObservationBytes != 0 ||
		stats.RecordCount != 0 {
		t.Fatalf("failed save leaked reservation: %+v", stats)
	}
	registry.options.MaxSnapshotBytes = DefaultMaxBytes
	if _, err := registry.Create(request); err != nil {
		t.Fatal(err)
	}
	activate()
	if registry.Stats().PendingObservations != 0 {
		t.Fatal("retry leaked budget")
	}
}

func TestRegistryNotificationBudgetOverdueBatchesFitAdmission(t *testing.T) {
	_, clock, _ := newTestRegistry(t)
	registry := NewRegistryWithOptions("", Options{Now: clock.Now, MaxPendingObservations: 2})
	for i := range 5 {
		if _, err := registry.Create(
			validCreate(clock, fmt.Sprintf("interaction_%08dexpiry", i), fmt.Sprintf("session-%d", i)),
		); err != nil {
			t.Fatal(err)
		}
	}
	clock.Advance(DefaultRetention)
	_, activate, unsubscribe := registry.SubscribeSnapshot(func(EventObservation) {})
	defer unsubscribe()
	claimed, err := registry.ClaimOverdue(clock.Now())
	if err != nil || len(claimed) != 2 {
		t.Fatalf("first bounded batch: %d %v", len(claimed), err)
	}
	if _, err := registry.ClaimOverdue(clock.Now()); !errors.Is(err, ErrObserverBacklog) {
		t.Fatalf("full gated budget: %v", err)
	}
	activate()
	total := len(claimed)
	for total < 5 {
		claimed, err = registry.ClaimOverdue(clock.Now())
		if err != nil || len(claimed) == 0 || len(claimed) > 2 {
			t.Fatalf("retry batch: %d %v", len(claimed), err)
		}
		total += len(claimed)
	}
	if registry.Stats().PendingObservations != 0 {
		t.Fatal("overdue batches leaked budget")
	}
}

func TestRegistryNotificationBudgetIncludesDrainerAndCallbackCopies(t *testing.T) {
	_, clock, _ := newTestRegistry(t)
	registry := NewRegistryWithOptions("", Options{Now: clock.Now})
	entered := make(chan EventObservation, 1)
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	registry.Subscribe(func(o EventObservation) { entered <- o; <-release })
	_, activate, unsubscribe := registry.SubscribeSnapshot(func(EventObservation) {})
	defer unsubscribe()
	done := make(chan error, 1)
	go func() {
		_, err := registry.Create(validCreate(clock, "interaction_copyaccounting", "session"))
		done <- err
	}()
	var observation EventObservation
	select {
	case observation = <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("callback did not start")
	}
	encoded, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	stats := registry.Stats()
	if stats.PendingObservations != 1 || stats.PendingObservationBytes != 3*len(encoded) {
		t.Fatalf("running and future callbacks escaped accounting: %+v", stats)
	}
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("drainer did not settle")
	}
	if registry.Stats().PendingObservations != 1 {
		t.Fatal("gated subscription released its observation early")
	}
	activate()
	if stats := registry.Stats(); stats.PendingObservations != 0 || stats.PendingObservationBytes != 0 {
		t.Fatalf("copy accounting leak: %+v", stats)
	}
}

func TestRegistryNotificationOversizedPayloadHasDistinctDisposition(t *testing.T) {
	_, clock, _ := newTestRegistry(t)
	registry := NewRegistryWithOptions("", Options{Now: clock.Now, MaxPendingObservationBytes: 1})
	_, _, unsubscribe := registry.SubscribeSnapshot(func(EventObservation) {})
	request := validCreate(clock, "interaction_oversizedpayload", "session")
	if _, err := registry.Create(
		request,
	); !errors.Is(err, ErrObserverPayloadOverBudget) ||
		errors.Is(err, ErrObserverBacklog) {
		t.Fatalf("intrinsically oversized payload disposition: %v", err)
	}
	if stats := registry.Stats(); stats.PendingObservations != 0 || stats.RecordCount != 0 {
		t.Fatalf("oversized payload mutated registry: %+v", stats)
	}
	unsubscribe()
	if _, err := registry.Create(request); err != nil {
		t.Fatal(err)
	}
}
