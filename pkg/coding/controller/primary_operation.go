package controller

import (
	"context"
	"errors"
	"fmt"
)

type primaryReviewOperation struct {
	id          string
	cancelCause error
	committed   bool
}

// primaryOperation is the actor-owned state for the one mutually exclusive
// turn, compaction, or review operation. Evidence reads remain independently
// queued because they are allowed to overlap a primary operation.
type primaryOperation struct {
	kind                operationKind
	cancelFunc          context.CancelCauseFunc
	hardCancelRequested bool
	review              *primaryReviewOperation
}

func (operation *primaryOperation) active() bool {
	return operation.kind != operationNone
}

func (operation *primaryOperation) is(kind operationKind) bool {
	return operation.kind == kind
}

// admissionError is the complete primary-operation conflict matrix.
func (operation *primaryOperation) admissionError() error {
	switch operation.kind {
	case operationNone:
		return nil
	case operationTurn:
		return ErrTurnActive
	case operationCompaction:
		return ErrCompactionActive
	case operationReview:
		return ErrReviewActive
	default:
		return fmt.Errorf("invalid coding primary operation kind %d", operation.kind)
	}
}

func (operation *primaryOperation) start(rootCtx context.Context, kind operationKind) context.Context {
	operation.assertIdle()
	if kind != operationTurn && kind != operationCompaction {
		panic(fmt.Sprintf("cannot start coding primary operation kind %d", kind))
	}
	operationCtx, cancel := context.WithCancelCause(rootCtx)
	*operation = primaryOperation{kind: kind, cancelFunc: cancel}
	return operationCtx
}

func (operation *primaryOperation) startReview(rootCtx context.Context, reviewID string) context.Context {
	operation.assertIdle()
	operationCtx, cancel := context.WithCancelCause(rootCtx)
	*operation = primaryOperation{
		kind:       operationReview,
		cancelFunc: cancel,
		review:     &primaryReviewOperation{id: reviewID},
	}
	return operationCtx
}

func (operation *primaryOperation) assertIdle() {
	if err := operation.admissionError(); err != nil {
		panic(fmt.Sprintf("cannot replace active coding primary operation: %v", err))
	}
}

func (operation *primaryOperation) matchesReview(reviewID string) bool {
	return operation.kind == operationReview && operation.review != nil && operation.review.id == reviewID
}

func (operation *primaryOperation) commitReview(reviewID string) error {
	if !operation.matchesReview(reviewID) {
		return fmt.Errorf("coding review publication is no longer active")
	}
	if operation.review.cancelCause != nil {
		return operation.review.cancelCause
	}
	operation.review.committed = true
	return nil
}

// cancel records review cancellation before signaling the runtime. A review
// publication commit is the linearization point after which cancellation is a
// successful no-op.
func (operation *primaryOperation) cancel(cause error) {
	if operation.kind == operationReview {
		if operation.review == nil || operation.review.committed {
			return
		}
		operation.review.cancelCause = cause
	}
	if operation.cancelFunc != nil {
		operation.cancelFunc(cause)
	}
}

func (operation *primaryOperation) turnHardCancelRequested() bool {
	return operation.kind == operationTurn && operation.hardCancelRequested
}

func (operation *primaryOperation) recordTurnHardCancel() {
	if operation.kind != operationTurn {
		panic(fmt.Sprintf("cannot record hard cancel for coding primary operation kind %d", operation.kind))
	}
	operation.hardCancelRequested = true
}

// finish validates operation-specific settlement and returns the final error.
// The state is cleared even for an impossible mismatched result so the actor
// cannot remain permanently busy after reporting the invariant violation.
func (operation *primaryOperation) finish(result operationResult) error {
	settled := *operation
	*operation = primaryOperation{}

	err := result.err
	if settled.kind != result.kind {
		return errors.Join(
			err,
			fmt.Errorf(
				"coding primary operation result kind %d does not match active kind %d",
				result.kind,
				settled.kind,
			),
		)
	}
	if result.kind != operationReview {
		return err
	}
	if settled.review == nil || settled.review.id != result.reviewID {
		return errors.Join(err, fmt.Errorf("coding review result does not match active review"))
	}
	if err == nil && !result.reviewCommitted {
		err = fmt.Errorf("coding review returned before publication commit")
	}
	if result.reviewCommitted != settled.review.committed {
		err = errors.Join(err, fmt.Errorf("coding review publication state mismatch"))
	}
	if !settled.review.committed && settled.review.cancelCause != nil {
		err = errors.Join(err, settled.review.cancelCause)
	}
	return err
}
