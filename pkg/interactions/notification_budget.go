package interactions

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// ObserverBacklogError rejects a write before persistence. Retrying after
// observers settle is safe; no committed notification was discarded.
type ObserverBacklogError struct {
	PendingObservations int
	PendingBytes        int
}

func (e *ObserverBacklogError) Error() string {
	return fmt.Sprintf(
		"%s: %d observations, %d accounted bytes",
		ErrObserverBacklog,
		e.PendingObservations,
		e.PendingBytes,
	)
}
func (e *ObserverBacklogError) Is(target error) bool { return target == ErrObserverBacklog }

// notificationBudget accounts for each immutable committed observation until
// the registry drainer and every subscriber (including gated/in-flight ones)
// release it. Its mutex never acquires a registry or delivery mutex.
type notificationBudget struct {
	mu    sync.Mutex
	count int
	bytes int
	idle  chan struct{}
}

func (b *notificationBudget) check(count, bytes, maxCount, maxBytes int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.checkLocked(count, bytes, maxCount, maxBytes)
}

func (b *notificationBudget) checkLocked(count, bytes, maxCount, maxBytes int) error {
	if count > maxCount-b.count || bytes > maxBytes-b.bytes {
		return &ObserverBacklogError{PendingObservations: b.count, PendingBytes: b.bytes}
	}
	return nil
}

func (b *notificationBudget) reserve(count, bytes, maxCount, maxBytes int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.checkLocked(count, bytes, maxCount, maxBytes); err != nil {
		return err
	}
	if b.count == 0 && count > 0 {
		b.idle = make(chan struct{})
	}
	b.count += count
	b.bytes += bytes
	return nil
}

func (b *notificationBudget) release(count, bytes int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.count -= count
	b.bytes -= bytes
	if b.count == 0 && count > 0 {
		close(b.idle)
	}
}

func (b *notificationBudget) usage() (int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.count, b.bytes
}

func (q *queuedObservation) done() {
	if q.remaining.Add(-1) == 0 {
		q.budget.release(1, q.bytes)
	}
}

type notificationBatch struct {
	registry  *Registry
	observers []*observerDelivery
	items     []*queuedObservation
	bytes     int
	released  bool
}

func (r *Registry) newNotificationBatchLocked() *notificationBatch {
	r.compactObserversLocked()
	observers := make([]*observerDelivery, 0, len(r.observers))
	if !r.closed.Load() {
		for _, delivery := range r.observers {
			if delivery.beginAdmission() {
				observers = append(observers, delivery)
			}
		}
	}
	return &notificationBatch{registry: r, observers: observers}
}

// Each batch owns a producer admission until it publishes committed work or
// rolls back. Unsubscribe can therefore settle without taking the store mutex,
// and cannot report idle while an accepted producer is still persisting.
func (b *notificationBatch) releaseAdmissions() {
	if b.released {
		return
	}
	b.released = true
	for _, delivery := range b.observers {
		delivery.finishWork(1)
	}
}

func (b *notificationBatch) append(event Event, record Record) error {
	if len(b.observers) == 0 {
		return nil
	}
	observation := EventObservation{Event: cloneEvent(event), Record: cloneRecord(record)}
	encoded, err := json.Marshal(observation)
	if err != nil {
		return err
	}
	// Charge the immutable shared payload plus a full isolated callback copy for
	// every subscriber. Gated buffers share the immutable payload. This is an
	// accounted payload bound, not a claim about the Go heap allocator.
	copies := len(b.observers) + 1
	maxBytes := b.registry.options.MaxPendingObservationBytes
	if len(encoded) > maxBytes/copies {
		return fmt.Errorf(
			"%w: %d-byte payload with %d copies exceeds %d bytes",
			ErrObserverPayloadOverBudget,
			len(encoded),
			copies,
			maxBytes,
		)
	}
	bytes := len(encoded) * copies
	if err := b.registry.notificationBudget.check(len(b.items)+1, b.bytes+bytes,
		b.registry.options.MaxPendingObservations, maxBytes); err != nil {
		return err
	}
	item := &queuedObservation{
		observation: observation,
		observers:   b.observers,
		budget:      &b.registry.notificationBudget,
		bytes:       bytes,
	}
	item.remaining.Store(int64(copies))
	b.items = append(b.items, item)
	b.bytes += bytes
	return nil
}

func (r *Registry) commitEventsLocked(events []Event) (bool, error) {
	batch := r.newNotificationBatchLocked()
	defer batch.releaseAdmissions()
	for _, event := range events {
		if err := batch.append(event, r.records[event.InteractionID]); err != nil {
			return false, err
		}
	}
	return r.commitNotificationBatchLocked(batch)
}

func (r *Registry) commitNotificationBatchLocked(batch *notificationBatch) (bool, error) {
	defer batch.releaseAdmissions()
	if err := r.notificationBudget.reserve(len(batch.items), batch.bytes,
		r.options.MaxPendingObservations, r.options.MaxPendingObservationBytes); err != nil {
		return false, err
	}
	if err := r.saveLocked(); err != nil {
		r.notificationBudget.release(len(batch.items), batch.bytes)
		return false, err
	}
	for _, delivery := range batch.observers {
		delivery.addWork(len(batch.items))
	}
	r.notifications = append(r.notifications, batch.items...)
	if len(batch.items) == 0 || r.notifying {
		return false, nil
	}
	r.notifying = true
	return true, nil
}

func (r *Registry) drainNotifications() {
	for {
		r.mu.Lock()
		if len(r.notifications) == 0 {
			r.notifying = false
			r.mu.Unlock()
			return
		}
		queued := r.notifications[0]
		r.notifications[0] = nil
		r.notifications = r.notifications[1:]
		r.mu.Unlock()
		for _, delivery := range queued.observers {
			delivery.enqueue(queued)
		}
		queued.done()
	}
}

func (b *notificationBudget) wait(ctx context.Context) error {
	b.mu.Lock()
	if b.count == 0 {
		b.mu.Unlock()
		return nil
	}
	idle := b.idle
	b.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		if count, _ := b.usage(); count == 0 {
			return nil
		}
		return ctx.Err()
	}
}
