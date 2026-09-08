package controller

import "context"

type evidenceOperation struct {
	id      uint64
	kind    operationKind
	request command
	ctx     context.Context
	cancel  context.CancelCauseFunc
}

// evidenceQueueState is actor-owned. It serializes repository and workspace
// reads without moving execution or result projection into another owner.
type evidenceQueueState struct {
	active *evidenceOperation
	queued []evidenceOperation
	nextID uint64
}

func (state *evidenceQueueState) empty() bool {
	return state.active == nil && len(state.queued) == 0
}

func (state *evidenceQueueState) admit(kind operationKind, request command) {
	state.nextID++
	operationCtx, cancel := context.WithCancelCause(request.ctx)
	state.queued = append(state.queued, evidenceOperation{
		id:      state.nextID,
		kind:    kind,
		request: request,
		ctx:     operationCtx,
		cancel:  cancel,
	})
}

func (state *evidenceQueueState) pruneCanceled() {
	retained := state.queued[:0]
	for _, operation := range state.queued {
		if err := operation.ctx.Err(); err != nil {
			operation.cancel(context.Canceled)
			operation.request.replyError(err)
			continue
		}
		retained = append(retained, operation)
	}
	clear(state.queued[len(retained):])
	state.queued = retained
}

func (state *evidenceQueueState) startNext(closing bool) (evidenceOperation, bool) {
	state.pruneCanceled()
	if closing || state.active != nil || len(state.queued) == 0 {
		return evidenceOperation{}, false
	}
	operation := state.queued[0]
	clear(state.queued[:1])
	state.queued = state.queued[1:]
	state.active = &operation
	return operation, true
}

func (state *evidenceQueueState) complete(id uint64, resultErr error) (error, bool) {
	if state.active == nil || state.active.id != id {
		return resultErr, false
	}
	operation := state.active
	if err := operation.ctx.Err(); err != nil {
		resultErr = err
	}
	operation.cancel(context.Canceled)
	state.active = nil
	return resultErr, true
}

func (state *evidenceQueueState) workspaceRefreshPending() bool {
	if state.active != nil && state.active.kind == operationWorkspaceRefresh {
		return true
	}
	for _, operation := range state.queued {
		if operation.kind == operationWorkspaceRefresh && operation.ctx.Err() == nil {
			return true
		}
	}
	return false
}

func (state *evidenceQueueState) cancelAll(cause error) {
	if state.active != nil {
		state.active.cancel(cause)
	}
	for _, operation := range state.queued {
		operation.cancel(cause)
		operation.request.replyError(cause)
	}
	clear(state.queued)
	state.queued = nil
}
