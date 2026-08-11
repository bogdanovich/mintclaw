package browser

import (
	"context"
	"errors"
)

// OutcomeFailureClass is a bounded, model-safe diagnostic for an accepted
// action whose outcome cannot be proven. It never changes replay authority.
type OutcomeFailureClass string

const (
	OutcomeFailureTimeout            OutcomeFailureClass = "timeout"
	OutcomeFailureCanceled           OutcomeFailureClass = "canceled"
	OutcomeFailurePolicyDenied       OutcomeFailureClass = "policy_denied"
	OutcomeFailureDriverRejected     OutcomeFailureClass = "driver_rejected"
	OutcomeFailureWorkerUnavailable  OutcomeFailureClass = "worker_unavailable"
	OutcomeFailureDriverIncompatible OutcomeFailureClass = "driver_incompatible"
	OutcomeFailureStale              OutcomeFailureClass = "stale"
	OutcomeFailureInvalidResult      OutcomeFailureClass = "invalid_result"
	OutcomeFailureUnknown            OutcomeFailureClass = "unknown"
)

type InvocationDiagnostic struct {
	FailureClass OutcomeFailureClass
}

func classifyAcceptedOutcomeFailure(executeErr, executionContextErr error) OutcomeFailureClass {
	if errors.Is(executionContextErr, context.DeadlineExceeded) ||
		errors.Is(executeErr, context.DeadlineExceeded) {
		return OutcomeFailureTimeout
	}
	if errors.Is(executionContextErr, context.Canceled) || errors.Is(executeErr, context.Canceled) {
		return OutcomeFailureCanceled
	}
	if errors.Is(executeErr, ErrDenied) {
		return OutcomeFailurePolicyDenied
	}
	// A driver rejection can also retire the worker. Preserve the causal
	// rejection rather than reducing the joined error to transport loss.
	if errors.Is(executeErr, ErrDriverRejected) {
		return OutcomeFailureDriverRejected
	}
	if errors.Is(executeErr, ErrWorkerUnavailable) {
		return OutcomeFailureWorkerUnavailable
	}
	if errors.Is(executeErr, ErrDriverIncompatible) {
		return OutcomeFailureDriverIncompatible
	}
	if errors.Is(executeErr, ErrStale) {
		return OutcomeFailureStale
	}
	return OutcomeFailureUnknown
}

// diagnoseRecoveredOutcome projects a safe class from durable terminal state
// when the process that observed the original failure is no longer available.
// The projection remains ephemeral and cannot authorize a retry.
func diagnoseRecoveredOutcome(invocation Invocation) Invocation {
	if invocation.State != InvocationUnknown || invocation.Diagnostic != nil {
		return invocation
	}
	class := OutcomeFailureUnknown
	switch invocation.SafeFailure {
	case "gateway_restarted", "worker_lost", "worker_unavailable":
		class = OutcomeFailureWorkerUnavailable
	case "canceled", "session_closed":
		class = OutcomeFailureCanceled
	case "policy_changed":
		class = OutcomeFailurePolicyDenied
	case "result_invalid":
		class = OutcomeFailureInvalidResult
	}
	invocation.Diagnostic = &InvocationDiagnostic{FailureClass: class}
	return invocation
}
