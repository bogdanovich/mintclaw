package interactions

import (
	"fmt"
	"regexp"
	"strings"
)

var conditionRefPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,256}$`)

func validKind(kind Kind) bool {
	return kind == KindQuestion || kind == KindApproval || kind == KindExternalWait
}

func validateExternalWait(kind Kind, reference string, origin *Origin) error {
	if kind != KindExternalWait {
		if reference != "" {
			return fmt.Errorf("%w: condition requires an external wait", ErrInvalidInteraction)
		}
		return nil
	}
	if !conditionRefPattern.MatchString(reference) {
		return fmt.Errorf("%w: external wait requires a bounded opaque condition reference", ErrInvalidInteraction)
	}
	if origin != nil && (strings.TrimSpace(origin.ExecutionID) == "" || origin.ExecutionContext == nil) {
		return fmt.Errorf("%w: external wait requires its original execution identity", ErrInvalidInteraction)
	}
	return nil
}

// ClaimExternalWait records a trusted tool's condition result, never a chat
// answer. The caller resolves authority through the registered origin tool;
// the reference alone grants no permission. Revision and expiry fence races.
func (r *Registry) ClaimExternalWait(id string, revision int64, outcome Outcome) (Record, error) {
	if outcome != OutcomeReady && outcome != OutcomeUnavailable {
		return Record{}, fmt.Errorf("%w: invalid external-wait outcome", ErrInvalidInteraction)
	}
	return r.update(id, revision, func(rec *Record, now int64) (EventType, string, *bool, error) {
		if rec.Kind != KindExternalWait || rec.Status != StatusWaiting {
			return "", "", nil, ErrInvalidTransition
		}
		if now >= rec.ExpiresAt {
			outcome = OutcomeTimedOut
		}
		rec.Status = StatusClaimed
		rec.Outcome = outcome
		rec.Answer = &Answer{ReceivedAt: now}
		return EventAnswerClaimed, "external_condition", nil, nil
	})
}

// InvalidateExternalWait prevents a claimed condition from authorizing a new
// continuation after its originating tool loses the condition on restart or
// revocation. It cannot revive a canceled or terminal interaction.
func (r *Registry) InvalidateExternalWait(id string, revision int64) (Record, error) {
	return r.update(id, revision, func(rec *Record, _ int64) (EventType, string, *bool, error) {
		if rec.Kind != KindExternalWait || rec.Outcome != OutcomeReady ||
			(rec.Status != StatusClaimed && rec.Status != StatusResuming) {
			return "", "", nil, ErrInvalidTransition
		}
		rec.Outcome = OutcomeUnavailable
		return EventRecoveryObserved, "external_condition_unavailable", nil, nil
	})
}
