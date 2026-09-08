package controller

import (
	"context"
	"errors"
	"testing"
)

func TestPrimaryOperationAdmissionConflictMatrix(t *testing.T) {
	tests := []struct {
		name string
		kind operationKind
		want error
	}{
		{name: "idle", kind: operationNone},
		{name: "turn", kind: operationTurn, want: ErrTurnActive},
		{name: "compaction", kind: operationCompaction, want: ErrCompactionActive},
		{name: "review", kind: operationReview, want: ErrReviewActive},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operation := primaryOperation{kind: test.kind}
			err := operation.admissionError()
			if !errors.Is(err, test.want) {
				t.Fatalf("admissionError() = %v, want %v", err, test.want)
			}
		})
	}
}

func TestPrimaryOperationReviewCommitDominatesCancellation(t *testing.T) {
	var operation primaryOperation
	operationCtx := operation.startReview(t.Context(), "review-1")
	if err := operation.commitReview("review-1"); err != nil {
		t.Fatal(err)
	}
	operation.cancel(context.Canceled)
	if err := operationCtx.Err(); err != nil {
		t.Fatalf("committed review context was canceled: %v", err)
	}
	if err := operation.finish(operationResult{
		kind:            operationReview,
		reviewID:        "review-1",
		reviewCommitted: true,
	}); err != nil {
		t.Fatalf("finish() error = %v", err)
	}
	if operation.active() {
		t.Fatal("finished review left the primary operation active")
	}
}

func TestPrimaryOperationReviewCancellationDominatesSuccess(t *testing.T) {
	var operation primaryOperation
	operationCtx := operation.startReview(t.Context(), "review-1")
	operation.cancel(ErrHardCanceled)
	if !errors.Is(context.Cause(operationCtx), ErrHardCanceled) {
		t.Fatalf("review cancellation cause = %v, want %v", context.Cause(operationCtx), ErrHardCanceled)
	}
	if err := operation.commitReview("review-1"); !errors.Is(err, ErrHardCanceled) {
		t.Fatalf("commitReview() error = %v, want %v", err, ErrHardCanceled)
	}
	err := operation.finish(operationResult{kind: operationReview, reviewID: "review-1"})
	if !errors.Is(err, ErrHardCanceled) {
		t.Fatalf("finish() error = %v, want cancellation cause %v", err, ErrHardCanceled)
	}
	if operation.active() {
		t.Fatal("finished review left the primary operation active")
	}
}
