package interactions

import (
	"context"
	"fmt"
)

// Subscription owns observer admission and settlement. Unsubscribe is immediate;
// CloseContext reports pending work instead of claiming a running callback was
// canceled. A canceled gated subscription requires a fresh snapshot if it had
// accepted work. Neither method interrupts an executing callback.
type Subscription struct{ delivery *observerDelivery }

func (r *Registry) SubscribeWithHandle(observer Observer) (*Subscription, error) {
	_, subscription, err := r.subscribe(observer, true)
	return subscription, err
}

func (r *Registry) SubscribeSnapshotWithHandle(observer Observer) (ObservationSnapshot, *Subscription, error) {
	return r.subscribe(observer, false)
}

func (r *Registry) subscribe(observer Observer, active bool) (ObservationSnapshot, *Subscription, error) {
	if r == nil {
		return ObservationSnapshot{}, nil, ErrStoreUnavailable
	}
	if observer == nil {
		return ObservationSnapshot{}, nil, fmt.Errorf("interaction observer is required")
	}
	if r.closed.Load() {
		return ObservationSnapshot{}, nil, ErrStoreClosed
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed.Load() {
		return ObservationSnapshot{}, nil, ErrStoreClosed
	}
	r.compactObserversLocked()
	delivery := newObserverDelivery(observer, active)
	r.observers = append(r.observers, delivery)
	snapshot := ObservationSnapshot{}
	if !active {
		snapshot = r.observationSnapshotLocked()
	}
	return snapshot, &Subscription{delivery: delivery}, nil
}

func (s *Subscription) Activate() {
	if s != nil && s.delivery != nil {
		s.delivery.activate()
	}
}

func (s *Subscription) Unsubscribe() {
	if s != nil && s.delivery != nil {
		s.delivery.unsubscribe()
	}
}
func (s *Subscription) Close() error { return s.CloseContext(context.Background()) }
func (s *Subscription) CloseContext(ctx context.Context) error {
	if s == nil || s.delivery == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, DefaultObserverShutdownTimeout)
	defer cancel()
	s.delivery.unsubscribe()
	pending, resync, err := s.delivery.wait(ctx)
	if err != nil || resync {
		return &ObserverDrainError{Cause: err, PendingWork: pending, ResyncRequired: resync}
	}
	return nil
}
