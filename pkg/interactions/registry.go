package interactions

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Options struct {
	// StoreLockTimeout bounds admission, including local writer contention.
	StoreLockTimeout           time.Duration
	TerminalRetention          time.Duration
	MaxRecords                 int
	MaxEvents                  int
	MaxSnapshotBytes           int
	MaxPendingObservations     int
	MaxPendingObservationBytes int
	Now                        func() time.Time
}

type Snapshot struct {
	SchemaVersion  string   `json:"schema_version"`
	CommitSequence uint64   `json:"commit_sequence"`
	Records        []Record `json:"records"`
	Events         []Event  `json:"events,omitempty"`
}

type Observer func(EventObservation)

type queuedObservation struct {
	observation EventObservation
	observers   []*observerDelivery
	budget      *notificationBudget
	bytes       int
	remaining   atomic.Int64
}

type Registry struct {
	mu                 sync.RWMutex
	storePath          string
	options            Options
	records            map[string]Record
	events             []Event
	observers          []*observerDelivery
	closed             atomic.Bool
	notifications      []*queuedObservation
	notificationBudget notificationBudget
	notifying          bool
	commitSequence     uint64
	loadErr            error
}

var _ Store = (*Registry)(nil)

func NewRegistry(storePath string) *Registry {
	return NewRegistryWithOptions(storePath, Options{})
}

func NewRegistryWithOptions(storePath string, opts Options) *Registry {
	if opts.StoreLockTimeout <= 0 {
		opts.StoreLockTimeout = DefaultStoreLockTimeout
	}
	if opts.TerminalRetention <= 0 {
		opts.TerminalRetention = DefaultRetention
	}
	if opts.MaxRecords <= 0 {
		opts.MaxRecords = DefaultMaxRecords
	}
	if opts.MaxEvents <= 0 {
		opts.MaxEvents = DefaultMaxEvents
	}
	if opts.MaxSnapshotBytes <= 0 {
		opts.MaxSnapshotBytes = DefaultMaxBytes
	}
	if opts.MaxPendingObservations <= 0 {
		opts.MaxPendingObservations = DefaultMaxPendingObservations
	}
	if opts.MaxPendingObservationBytes <= 0 {
		opts.MaxPendingObservationBytes = DefaultMaxPendingObservationBytes
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	r := &Registry{
		storePath: strings.TrimSpace(storePath),
		options:   opts,
		records:   make(map[string]Record),
	}
	if r.storePath != "" {
		release, err := r.lockAndReload(context.Background())
		if err != nil {
			r.loadErr = err
		} else {
			if r.pruneLocked(r.nowMillis()) {
				r.loadErr = r.saveLocked()
			}
			release()
			r.mu.Unlock()
		}
	}
	return r
}

func WorkspaceStorePath(workspace string) string {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return ""
	}
	return filepath.Join(workspace, "state", "interaction_registry.json")
}

// ValidateSnapshot reads and validates a registry snapshot without locking,
// pruning, or writing it.
func ValidateSnapshot(storePath string) error {
	r := &Registry{
		storePath: strings.TrimSpace(storePath),
		records:   make(map[string]Record),
		events:    make([]Event, 0),
	}
	return r.load()
}

// InspectSnapshot validates bounded snapshot bytes using the registry's load
// contract, without opening a store, locking, pruning, or writing. It does not
// establish freshness or authorize an answer; callers must verify the current
// route, status, expiry and prompt binding. Returned records are sensitive local
// evidence, not a model or transport projection, and may contain opaque bindings.
func InspectSnapshot(data []byte) (Snapshot, error) {
	if len(data) > DefaultMaxBytes {
		return Snapshot{}, ErrSnapshotOverBudget
	}
	r := &Registry{records: make(map[string]Record)}
	if err := r.loadData(data); err != nil {
		return Snapshot{}, err
	}
	return r.snapshotLocked(), nil
}

func (r *Registry) LastLoadError() error {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.loadErr
}

// Subscribe returns a nonblocking unsubscribe function. It stops new delivery
// admission; callbacks accepted before unsubscribe may still finish. Use
// SubscribeWithHandle when the caller needs explicit settlement information.
func (r *Registry) Subscribe(observer Observer) func() {
	subscription, err := r.SubscribeWithHandle(observer)
	if err != nil {
		return func() {}
	}
	return subscription.Unsubscribe
}

// SubscribeSnapshot installs a gated observer at an atomic snapshot boundary.
// Explicit unsubscribe abandons the gated buffer. The handle variant reports
// whether resynchronization is required and supports bounded settlement.
func (r *Registry) SubscribeSnapshot(observer Observer) (ObservationSnapshot, func(), func()) {
	snapshot, subscription, err := r.SubscribeSnapshotWithHandle(observer)
	if err != nil {
		return ObservationSnapshot{}, func() {}, func() {}
	}
	return snapshot, subscription.Activate, subscription.Unsubscribe
}

func (r *Registry) compactObserversLocked() {
	kept := r.observers[:0]
	for _, delivery := range r.observers {
		if delivery.subscribed() {
			kept = append(kept, delivery)
		}
	}
	clear(r.observers[len(kept):])
	r.observers = kept
}

func (r *Registry) observationSnapshotLocked() ObservationSnapshot {
	records := make([]Record, 0, len(r.records))
	for _, record := range r.records {
		records = append(records, cloneRecord(record))
	}
	sortRecords(records)
	return ObservationSnapshot{
		Through: r.commitSequence,
		Records: records,
		Events:  cloneEvents(r.events),
	}
}
