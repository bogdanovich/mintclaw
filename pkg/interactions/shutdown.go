package interactions

import (
	"context"
	"fmt"
)

// ObserverDrainError distinguishes unfinished work from a deliberately
// abandoned gated stream that needs a fresh snapshot. PendingWork is -1 if
// shutdown could not acquire the registry mutex to inspect admitted work.
type ObserverDrainError struct {
	Cause          error
	PendingWork    int
	ResyncRequired bool
}

func (e *ObserverDrainError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf(
			"%s: pending=%d resync_required=%t: %v",
			ErrObserverPending,
			e.PendingWork,
			e.ResyncRequired,
			e.Cause,
		)
	}
	return ErrObserverResyncRequired.Error()
}
func (e *ObserverDrainError) Unwrap() error { return e.Cause }
func (e *ObserverDrainError) Is(target error) bool {
	return target == ErrObserverPending && e.Cause != nil || target == ErrObserverResyncRequired && e.ResyncRequired
}

func (r *Registry) Close() error { return r.CloseContext(context.Background()) }

// CloseContext stops new write/observer admission immediately and waits at most
// one second (or the earlier caller deadline) for accepted observations. It
// preserves already admitted active deliveries and reports gated cancellation
// explicitly. A timeout leaves the callback running and permits a later retry.
func (r *Registry) CloseContext(ctx context.Context) error {
	if r == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, DefaultObserverShutdownTimeout)
	defer cancel()
	r.closed.Store(true)
	if err := retryStoreLock(ctx, func() (bool, error) { return r.mu.TryLock(), nil }); err != nil {
		return &ObserverDrainError{Cause: err, PendingWork: -1}
	}
	observers := append([]*observerDelivery(nil), r.observers...)
	r.mu.Unlock()
	resync := false
	for _, delivery := range observers {
		resync = delivery.unsubscribe() || resync
	}
	err := r.notificationBudget.wait(ctx)
	if err != nil || resync {
		pending, _ := r.notificationBudget.usage()
		return &ObserverDrainError{Cause: err, PendingWork: pending, ResyncRequired: resync}
	}
	return nil
}
