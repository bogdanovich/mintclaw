package browser

import (
	"context"
	"errors"
	"testing"
)

func TestClassifyAcceptedOutcomeFailure(t *testing.T) {
	tests := []struct {
		name       string
		executeErr error
		contextErr error
		want       OutcomeFailureClass
	}{
		{
			name:       "deadline",
			executeErr: ErrWorkerUnavailable,
			contextErr: context.DeadlineExceeded,
			want:       OutcomeFailureTimeout,
		},
		{name: "canceled", contextErr: context.Canceled, want: OutcomeFailureCanceled},
		{name: "policy", executeErr: ErrDenied, want: OutcomeFailurePolicyDenied},
		{
			name:       "rejection before joined worker loss",
			executeErr: errors.Join(ErrDriverRejected, ErrWorkerUnavailable),
			want:       OutcomeFailureDriverRejected,
		},
		{name: "transport", executeErr: ErrWorkerLost, want: OutcomeFailureWorkerUnavailable},
		{name: "incompatible", executeErr: ErrDriverIncompatible, want: OutcomeFailureDriverIncompatible},
		{name: "stale", executeErr: ErrStale, want: OutcomeFailureStale},
		{name: "unknown", executeErr: errors.New("private driver detail"), want: OutcomeFailureUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyAcceptedOutcomeFailure(test.executeErr, test.contextErr); got != test.want {
				t.Fatalf("classifyAcceptedOutcomeFailure() = %q, want %q", got, test.want)
			}
		})
	}
}
