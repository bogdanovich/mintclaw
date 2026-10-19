package interactions

import (
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
	b.count += count
	b.bytes += bytes
	return nil
}

func (b *notificationBudget) release(count, bytes int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.count -= count
	b.bytes -= bytes
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
}

func (r *Registry) newNotificationBatchLocked() *notificationBatch {
	observers := make([]*observerDelivery, 0, len(r.observers))
	for _, entry := range r.observers {
		observers = append(observers, entry.delivery)
	}
	return &notificationBatch{registry: r, observers: observers}
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
	for _, event := range events {
		if err := batch.append(event, r.records[event.InteractionID]); err != nil {
			return false, err
		}
	}
	return r.commitNotificationBatchLocked(batch)
}

func (r *Registry) commitNotificationBatchLocked(batch *notificationBatch) (bool, error) {
	if err := r.notificationBudget.reserve(len(batch.items), batch.bytes,
		r.options.MaxPendingObservations, r.options.MaxPendingObservationBytes); err != nil {
		return false, err
	}
	if err := r.saveLocked(); err != nil {
		r.notificationBudget.release(len(batch.items), batch.bytes)
		return false, err
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
