package interactions

import (
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Options struct {
	TerminalRetention time.Duration
	MaxRecords        int
	MaxEvents         int
	MaxSnapshotBytes  int
	Now               func() time.Time
}

type Snapshot struct {
	SchemaVersion  string   `json:"schema_version"`
	CommitSequence uint64   `json:"commit_sequence,omitempty"`
	Records        []Record `json:"records"`
	Events         []Event  `json:"events,omitempty"`
}

type Observer func(EventObservation)

type observerEntry struct {
	id       uint64
	delivery *observerDelivery
}

type queuedObservation struct {
	observation EventObservation
	observers   []*observerDelivery
}

var observerSequence atomic.Uint64

type Registry struct {
	mu             sync.RWMutex
	storePath      string
	options        Options
	records        map[string]Record
	events         []Event
	observers      []observerEntry
	notifications  []queuedObservation
	notifying      bool
	commitSequence uint64
	loadErr        error
}

var _ Store = (*Registry)(nil)

func NewRegistry(storePath string) *Registry {
	return NewRegistryWithOptions(storePath, Options{})
}

func NewRegistryWithOptions(storePath string, opts Options) *Registry {
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
	if opts.Now == nil {
		opts.Now = time.Now
	}
	r := &Registry{
		storePath: strings.TrimSpace(storePath),
		options:   opts,
		records:   make(map[string]Record),
	}
	if r.storePath != "" {
		r.mu.Lock()
		release, err := r.lockAndReloadLocked()
		if err != nil {
			r.loadErr = err
		} else {
			if r.pruneLocked(r.nowMillis()) {
				r.loadErr = r.saveLocked()
			}
			release()
		}
		r.mu.Unlock()
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

func (r *Registry) LastLoadError() error {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.loadErr
}

func (r *Registry) Subscribe(observer Observer) func() {
	if r == nil || observer == nil {
		return func() {}
	}
	entry := observerEntry{
		id: observerSequence.Add(1), delivery: newObserverDelivery(observer, true),
	}
	r.mu.Lock()
	r.observers = append(r.observers, entry)
	r.mu.Unlock()
	return r.unsubscribe(entry)
}

// SubscribeSnapshot atomically installs a gated observer and captures retained
// state. Events committed before the boundary appear only in the snapshot;
// later commits are buffered in order until the caller applies the snapshot
// and invokes the returned activate function.
func (r *Registry) SubscribeSnapshot(
	observer Observer,
) (ObservationSnapshot, func(), func()) {
	if r == nil || observer == nil {
		return ObservationSnapshot{}, func() {}, func() {}
	}
	entry := observerEntry{
		id: observerSequence.Add(1), delivery: newObserverDelivery(observer, false),
	}
	r.mu.Lock()
	r.observers = append(r.observers, entry)
	snapshot := r.observationSnapshotLocked()
	r.mu.Unlock()
	return snapshot, entry.delivery.activate, r.unsubscribe(entry)
}

func (r *Registry) unsubscribe(entry observerEntry) func() {
	return func() {
		r.mu.Lock()
		for i := range r.observers {
			if r.observers[i].id != entry.id {
				continue
			}
			r.observers = append(r.observers[:i], r.observers[i+1:]...)
			break
		}
		r.mu.Unlock()
		entry.delivery.unsubscribe()
	}
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
