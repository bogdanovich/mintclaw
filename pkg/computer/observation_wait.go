package computer

import (
	"context"
	"time"
)

// ObservationCondition inspects protected, live-only evidence. It must be a
// fast, side-effect-free check, honor ctx and neither mutate nor retain the
// observation. False means observe again; an error ends the wait. A condition
// does not authorize input or establish an effect that it has not inspected.
type ObservationCondition func(ctx context.Context, observation Observation) (bool, error)

// ObserveUntil waits for a condition in fresh captures of this original session.
// It reuses Observe's stored authority and capture/cancellation boundary, never
// changes control, and never dispatches or retries input. It does not extend the
// session or observation lease. The caller must still revalidate authority and
// evidence immediately before any subsequent action.
//
// The wait is capped at one minute, including capture and condition evaluation;
// a shorter caller deadline wins. The 100 ms cadence limits read-only polling,
// not application completion: only the condition can establish a match. As with
// CaptureDriver, a condition must cooperate with cancellation.
func (host *ReadOnlyHost) ObserveUntil(
	ctx context.Context, session Session, condition ObservationCondition,
) (Observation, error) {
	if condition == nil {
		return Observation{}, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	for {
		observation, err := host.Observe(ctx, session)
		if err != nil {
			return Observation{}, err
		}
		matched, conditionErr := condition(ctx, observation)
		// A callback can outlive a control/policy change or the evidence lease.
		// Its answer must not revive the old observation, even when it matched.
		host.arbiter.mu.Lock()
		entry, err := host.bound(ctx, session.Binding)
		if err == nil {
			err = observation.Validate(entry.session, host.now())
		}
		host.arbiter.mu.Unlock()
		if err != nil {
			return Observation{}, err
		}
		if conditionErr != nil {
			return Observation{}, ErrUnavailable // Never copy private condition details.
		}
		if matched {
			return observation, nil
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Observation{}, ctx.Err()
		case <-timer.C:
		}
	}
}
