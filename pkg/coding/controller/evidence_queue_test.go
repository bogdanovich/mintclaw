package controller

import (
	"context"
	"errors"
	"testing"
)

func TestEvidenceQueueStateSerializesOperationsAndRejectsStaleResults(t *testing.T) {
	var state evidenceQueueState
	firstRequest := command{
		kind:        commandRepositoryStatus,
		ctx:         t.Context(),
		statusReply: make(chan repositoryStatusResponse, 1),
	}
	secondRequest := command{
		kind:      commandRepositoryDiff,
		ctx:       t.Context(),
		diffReply: make(chan repositoryDiffResponse, 1),
	}
	state.admit(operationRepositoryStatus, firstRequest)
	state.admit(operationRepositoryDiff, secondRequest)

	first, ok := state.startNext(false)
	if !ok || first.id != 1 || first.kind != operationRepositoryStatus {
		t.Fatalf("first operation = %+v, started=%t", first, ok)
	}
	if _, started := state.startNext(false); started {
		t.Fatal("started a second evidence operation while one was active")
	}

	reportedErr := errors.New("reported failure")
	if got, matched := state.complete(first.id+1, reportedErr); matched || !errors.Is(got, reportedErr) {
		t.Fatalf("stale completion = (%v, %t), want unchanged error and no match", got, matched)
	}
	if state.empty() {
		t.Fatal("stale completion cleared the active operation")
	}
	if got, matched := state.complete(first.id, reportedErr); !matched || !errors.Is(got, reportedErr) {
		t.Fatalf("first completion = (%v, %t), want reported failure and match", got, matched)
	}

	second, ok := state.startNext(false)
	if !ok || second.id != 2 || second.kind != operationRepositoryDiff {
		t.Fatalf("second operation = %+v, started=%t", second, ok)
	}
	if got, matched := state.complete(second.id, nil); !matched || got != nil {
		t.Fatalf("second completion = (%v, %t), want nil and match", got, matched)
	}
	if !state.empty() {
		t.Fatal("queue not empty after both operations completed")
	}
}

func TestEvidenceQueueStatePrunesCanceledWorkspaceRefresh(t *testing.T) {
	var state evidenceQueueState
	canceledCtx, cancel := context.WithCancel(t.Context())
	canceledRequest := command{
		kind:  commandRefreshWorkspace,
		ctx:   canceledCtx,
		reply: make(chan error, 1),
	}
	state.admit(operationWorkspaceRefresh, canceledRequest)
	cancel()
	if state.workspaceRefreshPending() {
		t.Fatal("canceled queued refresh still excluded primary work")
	}

	liveRequest := command{
		kind:        commandRepositoryStatus,
		ctx:         t.Context(),
		statusReply: make(chan repositoryStatusResponse, 1),
	}
	state.admit(operationRepositoryStatus, liveRequest)
	operation, ok := state.startNext(false)
	if !ok || operation.kind != operationRepositoryStatus {
		t.Fatalf("operation = %+v, started=%t, want live repository status", operation, ok)
	}
	if err := <-canceledRequest.reply; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled refresh error = %v, want context.Canceled", err)
	}
	if got, matched := state.complete(operation.id, nil); !matched || got != nil {
		t.Fatalf("live completion = (%v, %t), want nil and match", got, matched)
	}
}

func TestEvidenceQueueStateCloseCancelsAndDrainsBeforeEmpty(t *testing.T) {
	var state evidenceQueueState
	activeRequest := command{
		kind:  commandRefreshWorkspace,
		ctx:   t.Context(),
		reply: make(chan error, 1),
	}
	queuedRequest := command{
		kind:        commandRepositoryStatus,
		ctx:         t.Context(),
		statusReply: make(chan repositoryStatusResponse, 1),
	}
	state.admit(operationWorkspaceRefresh, activeRequest)
	active, ok := state.startNext(false)
	if !ok {
		t.Fatal("active operation did not start")
	}
	state.admit(operationRepositoryStatus, queuedRequest)
	if !state.workspaceRefreshPending() {
		t.Fatal("active workspace refresh was not reported as pending")
	}

	state.cancelAll(context.Canceled)
	if !errors.Is(active.ctx.Err(), context.Canceled) {
		t.Fatalf("active context error = %v, want context.Canceled", active.ctx.Err())
	}
	if response := <-queuedRequest.statusReply; !errors.Is(response.err, context.Canceled) {
		t.Fatalf("queued status error = %v, want context.Canceled", response.err)
	}
	if state.empty() {
		t.Fatal("queue became empty before the active operation returned")
	}
	if got, matched := state.complete(active.id, nil); !matched || !errors.Is(got, context.Canceled) {
		t.Fatalf("canceled completion = (%v, %t), want context.Canceled and match", got, matched)
	}
	if !state.empty() {
		t.Fatal("queue not empty after canceled active operation completed")
	}
	state.admit(operationRepositoryStatus, queuedRequest)
	if _, started := state.startNext(true); started {
		t.Fatal("started evidence while closing")
	}
	state.cancelAll(context.Canceled)
	if response := <-queuedRequest.statusReply; !errors.Is(response.err, context.Canceled) {
		t.Fatalf("closing status error = %v, want context.Canceled", response.err)
	}
}
