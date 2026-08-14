package agentadapter

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestAdapterProjectsRuntimeLifecycleWithoutArgumentValues(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	eventBus := runtimeevents.NewBus()
	wrapped, err := WrapBus(eventBus, projector, "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wrapped.Close() })

	scope := runtimeevents.Scope{SessionKey: "thread-1", TraceScope: runtimeevents.NewTraceScope("/repo", "turn-1")}
	publish := func(kind runtimeevents.Kind, payload any) {
		t.Helper()
		wrapped.PublishNonBlocking(runtimeevents.Event{
			Kind:    kind,
			Source:  runtimeevents.Source{Component: "agent", Name: "coding"},
			Scope:   scope,
			Payload: payload,
		})
	}
	publish(runtimeevents.KindAgentTurnStart, agent.TurnStartPayload{UserMessage: "fix it"})
	publish(runtimeevents.KindAgentToolExecStart, agent.ToolExecStartPayload{
		ToolCallID: "call-1",
		Tool:       "exec",
		Arguments:  map[string]any{"command": "secret command", "timeout": 10},
	})
	publish(runtimeevents.KindAgentToolExecEnd, agent.ToolExecEndPayload{
		ToolCallID: "call-1",
		Tool:       "exec",
		Duration:   time.Second,
		ForLLMLen:  20,
		ForUserLen: 10,
		WriteAudit: []toolshared.WriteAuditEntry{{
			Kind: "file", Target: "main.go", Action: "update", Success: true,
		}},
	})
	publish(runtimeevents.KindAgentContextCompress, agent.ContextCompressPayload{TokensSaved: 400})
	publish(runtimeevents.KindAgentTurnEnd, agent.TurnEndPayload{
		Status:       agent.TurnEndStatusCompleted,
		FinalContent: "done",
	})

	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision != 6 || snapshot.Activity != frontend.ActivityIdle || snapshot.Status != "completed" {
		t.Fatalf("terminal state = %+v", snapshot)
	}
	if len(snapshot.Entries) != 2 || snapshot.Entries[1].Text != "done" {
		t.Fatalf("entries = %+v", snapshot.Entries)
	}
	if len(snapshot.Tools) != 1 || snapshot.Tools[0].Status != frontend.ToolSucceeded {
		t.Fatalf("tools = %+v", snapshot.Tools)
	}
	if snapshot.Tools[0].TurnID != "turn-1" || len(snapshot.Tools[0].WriteAudit) != 1 ||
		snapshot.Tools[0].WriteAudit[0].Target != "main.go" {
		t.Fatalf("tool correlation/write audit = %+v", snapshot.Tools[0])
	}
	if strings.Contains(snapshot.Tools[0].Arguments, "secret command") ||
		snapshot.Tools[0].Arguments != "fields: command, timeout" {
		t.Fatalf("argument projection = %q", snapshot.Tools[0].Arguments)
	}
}

func TestAdapterProjectsMetadataRetryFallbackAndRedactedError(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if err = ProjectThreadMetadata(projector, thread.Metadata{
		Title: "Fix tests", Preview: "Fix the slow tests", Model: "coding-model", Provider: "provider",
		UpdatedAt: time.Unix(10, 0),
		Project:   thread.ProjectIdentity{ProjectRoot: "/repo", InvocationCWD: "/repo/subdir"},
	}); err != nil {
		t.Fatal(err)
	}
	eventBus := runtimeevents.NewBus()
	wrapped, err := WrapBus(eventBus, projector, "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wrapped.Close() })
	scope := runtimeevents.Scope{SessionKey: "thread-1", TraceScope: runtimeevents.NewTraceScope("/repo", "turn-1")}
	publish := func(kind runtimeevents.Kind, payload any) {
		wrapped.PublishNonBlocking(runtimeevents.Event{
			Kind: kind, Source: runtimeevents.Source{Component: "agent"}, Scope: scope, Payload: payload,
		})
	}
	publish(runtimeevents.KindAgentLLMRetry, agent.LLMRetryPayload{
		Attempt: 1, MaxRetries: 3, Reason: "rate_limit", Error: "secret-token=abc",
	})
	publish(runtimeevents.KindAgentLLMRetry, agent.LLMRetryPayload{
		Attempt: 1, MaxRetries: 3, Reason: "rate_limit", Error: "different-secret=xyz",
	})
	publish(runtimeevents.KindAgentLLMFallbackAttempt, agent.LLMFallbackAttemptPayload{
		Attempt: 1, Provider: "openai", Model: "gpt-5", Status: "succeeded", Reason: "rate_limit",
		DiagnosticMessage: "secret-token=abc",
	})
	publish(runtimeevents.KindAgentError, agent.ErrorPayload{Stage: "llm", Message: "secret-token=abc"})

	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Metadata.Title != "Fix tests" || snapshot.Metadata.CWD != "/repo/subdir" {
		t.Fatalf("metadata = %+v", snapshot.Metadata)
	}
	if len(snapshot.Entries) != 4 || snapshot.Entries[0].TurnID != "turn-1" ||
		!strings.Contains(snapshot.Entries[0].Text, "rate_limit") {
		t.Fatalf("retry/fallback entries = %+v", snapshot.Entries)
	}
	if snapshot.Entries[0].ID == snapshot.Entries[1].ID {
		t.Fatalf("repeated retry notices share ID %q", snapshot.Entries[0].ID)
	}
	encoded := fmt.Sprintf("%+v", snapshot)
	if strings.Contains(encoded, "secret-token") {
		t.Fatalf("frontend projection leaked diagnostic content: %s", encoded)
	}
	if snapshot.Status != "agent error during llm" {
		t.Fatalf("status = %q", snapshot.Status)
	}
}

func TestAdapterBackgroundCompactionPreservesCompletedTurnState(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	eventBus := runtimeevents.NewBus()
	wrapped, err := WrapBus(eventBus, projector, "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wrapped.Close() })
	publish := func(kind runtimeevents.Kind, turnID string, payload any) {
		wrapped.PublishNonBlocking(runtimeevents.Event{
			Kind: kind, Source: runtimeevents.Source{Component: "agent"},
			Scope: runtimeevents.Scope{
				SessionKey: "thread-1", TraceScope: runtimeevents.NewTraceScope("/repo", turnID),
			},
			Payload: payload,
		})
	}
	publish(runtimeevents.KindAgentTurnEnd, "turn-1", agent.TurnEndPayload{Status: agent.TurnEndStatusCompleted})
	publish(runtimeevents.KindAgentContextCompress, "", agent.ContextCompressPayload{
		Reason: agent.ContextCompressReasonSummarize, TokensSaved: 500,
	})

	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Activity != frontend.ActivityIdle || snapshot.Status != "completed" {
		t.Fatalf("background compaction reopened completed turn: %+v", snapshot)
	}
	deltas, err := projector.ChangesSince(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 2 || deltas[1].Kind != frontend.DeltaCompactionComplete ||
		deltas[1].Activity != frontend.ActivityIdle {
		t.Fatalf("background compaction deltas = %+v", deltas)
	}
}

func TestAdapterProjectsWorkspaceSnapshot(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	eventBus := runtimeevents.NewBus()
	wrapped, err := WrapBus(eventBus, projector, "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wrapped.Close() })

	wrapped.PublishNonBlocking(runtimeevents.Event{
		Kind:   runtimeevents.KindAgentWorkspaceSnapshot,
		Source: runtimeevents.Source{Component: "agent", Name: "coding"},
		Scope: runtimeevents.Scope{
			SessionKey: "thread-1",
			TraceScope: runtimeevents.NewTraceScope("/repo", "turn-1"),
		},
		Payload: agent.WorkspaceSnapshotPayload{Snapshot: codingworkspace.Snapshot{
			ProjectRoot: "/repo",
			CWD:         "/repo",
			Git:         codingworkspace.GitState{Available: true, Branch: "main", Dirty: true},
			ChangedPaths: []codingworkspace.ChangedPath{
				{Path: "changed.go", Status: " M"},
			},
		}},
	})

	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision != 1 || snapshot.Workspace == nil ||
		snapshot.Workspace.ChangedPaths[0].Path != "changed.go" {
		t.Fatalf("projected workspace = %+v", snapshot.Workspace)
	}
}

func TestAdapterProjectsToolFailureAndInterruptionInOrder(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	eventBus := runtimeevents.NewBus()
	wrapped, err := WrapBus(eventBus, projector, "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wrapped.Close() })
	scope := runtimeevents.Scope{SessionKey: "thread-1", TraceScope: runtimeevents.NewTraceScope("/repo", "turn-1")}
	publish := func(kind runtimeevents.Kind, payload any) {
		wrapped.PublishNonBlocking(runtimeevents.Event{
			Kind: kind, Source: runtimeevents.Source{Component: "agent"}, Scope: scope, Payload: payload,
		})
	}
	publish(runtimeevents.KindAgentTurnStart, agent.TurnStartPayload{UserMessage: "run it"})
	publish(runtimeevents.KindAgentToolExecStart, agent.ToolExecStartPayload{ToolCallID: "call-1", Tool: "exec"})
	publish(runtimeevents.KindAgentToolExecEnd, agent.ToolExecEndPayload{
		ToolCallID: "call-1", Tool: "exec", IsError: true,
	})
	publish(runtimeevents.KindAgentInterruptReceived, agent.InterruptReceivedPayload{})
	publish(runtimeevents.KindAgentTurnEnd, agent.TurnEndPayload{Status: agent.TurnEndStatusAborted})

	deltas, err := projector.ChangesSince(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := []frontend.DeltaKind{
		frontend.DeltaTurnStarted,
		frontend.DeltaToolStarted,
		frontend.DeltaToolCompleted,
		frontend.DeltaInterruptRequested,
		frontend.DeltaTurnInterrupted,
	}
	if len(deltas) != len(wantKinds) {
		t.Fatalf("deltas = %+v, want %d events", deltas, len(wantKinds))
	}
	for i, want := range wantKinds {
		if deltas[i].Kind != want || deltas[i].Revision != frontend.Revision(i+1) {
			t.Fatalf("delta %d = %+v, want kind %q at revision %d", i, deltas[i], want, i+1)
		}
	}
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Activity != frontend.ActivityIdle || snapshot.Status != "interrupted" ||
		len(snapshot.Tools) != 1 || snapshot.Tools[0].Status != frontend.ToolFailed ||
		snapshot.Tools[0].TurnID != "turn-1" {
		t.Fatalf("interrupted snapshot = %+v", snapshot)
	}
}

func TestAdapterInterruptionTerminalizesRunningTool(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	reducer, err := frontend.NewReducer(initial)
	if err != nil {
		t.Fatal(err)
	}
	eventBus := runtimeevents.NewBus()
	wrapped, err := WrapBus(eventBus, projector, "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wrapped.Close() })
	scope := runtimeevents.Scope{SessionKey: "thread-1", TraceScope: runtimeevents.NewTraceScope("/repo", "turn-1")}
	for _, event := range []runtimeevents.Event{
		{
			Kind: runtimeevents.KindAgentToolExecStart, Source: runtimeevents.Source{Component: "agent"}, Scope: scope,
			Payload: agent.ToolExecStartPayload{ToolCallID: "call-1", Tool: "exec"},
		},
		{
			Kind: runtimeevents.KindAgentTurnEnd, Source: runtimeevents.Source{Component: "agent"}, Scope: scope,
			Payload: agent.TurnEndPayload{Status: agent.TurnEndStatusAborted},
		},
	} {
		wrapped.PublishNonBlocking(event)
	}
	deltas, err := projector.ChangesSince(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 2 || !deltas[1].RequiresSnapshot {
		t.Fatalf("interruption deltas = %+v", deltas)
	}
	if err = reducer.Apply(deltas[0]); err != nil {
		t.Fatal(err)
	}
	if err = reducer.ApplyOrResync(t.Context(), projector, deltas[1]); err != nil {
		t.Fatal(err)
	}
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Tools) != 1 || snapshot.Tools[0].Status != frontend.ToolInterrupted {
		t.Fatalf("interrupted tools = %+v", snapshot.Tools)
	}
	if got := reducer.State(); len(got.Tools) != 1 || got.Tools[0].Status != frontend.ToolInterrupted {
		t.Fatalf("resynchronized interrupted tools = %+v", got.Tools)
	}
}

func TestAdapterProjectsSuspendedToolWithoutCompletingIt(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	reducer, err := frontend.NewReducer(initial)
	if err != nil {
		t.Fatal(err)
	}
	eventBus := runtimeevents.NewBus()
	wrapped, err := WrapBus(eventBus, projector, "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wrapped.Close() })
	scope := runtimeevents.Scope{SessionKey: "thread-1", TraceScope: runtimeevents.NewTraceScope("/repo", "turn-1")}
	for _, event := range []runtimeevents.Event{
		{
			Kind: runtimeevents.KindAgentToolExecStart, Source: runtimeevents.Source{Component: "agent"}, Scope: scope,
			Payload: agent.ToolExecStartPayload{ToolCallID: "call-1", Tool: "request_human_input"},
		},
		{
			Kind: runtimeevents.KindAgentToolExecEnd, Source: runtimeevents.Source{Component: "agent"}, Scope: scope,
			Payload: agent.ToolExecEndPayload{
				ToolCallID: "call-1", Tool: "request_human_input", Duration: time.Second, Suspended: true,
			},
		},
		{
			Kind: runtimeevents.KindAgentTurnEnd, Source: runtimeevents.Source{Component: "agent"}, Scope: scope,
			Payload: agent.TurnEndPayload{Status: agent.TurnEndStatusSuspended},
		},
	} {
		wrapped.PublishNonBlocking(event)
	}
	deltas, err := projector.ChangesSince(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 3 || deltas[1].Kind != frontend.DeltaToolSuspended ||
		deltas[2].Kind != frontend.DeltaTurnSuspended || deltas[2].TurnID != "turn-1" ||
		deltas[2].EntityID != "turn-1" {
		t.Fatalf("suspension deltas = %+v", deltas)
	}
	for _, delta := range deltas {
		if err = reducer.Apply(delta); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Tools) != 1 || snapshot.Tools[0].Status != frontend.ToolSuspended ||
		snapshot.Activity != frontend.ActivityWaitingInput || snapshot.Status != "waiting for input" {
		t.Fatalf("suspended snapshot = %+v", snapshot)
	}
	if got := reducer.State(); !reflect.DeepEqual(got, snapshot) {
		t.Fatalf("reduced state = %+v, want %+v", got, snapshot)
	}
}

func TestWrappedBusProjectionRemainsLosslessWhenOrdinarySubscriberDrops(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	eventBus := runtimeevents.NewBus()
	_, _, err = eventBus.Channel().SubscribeChan(t.Context(), runtimeevents.SubscribeOptions{
		Name:         "intentionally-slow",
		Buffer:       1,
		Backpressure: runtimeevents.DropNewest,
	})
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := WrapBus(eventBus, projector, "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wrapped.Close() })

	for i := range 20 {
		wrapped.PublishNonBlocking(runtimeevents.Event{
			Kind:   runtimeevents.KindAgentTurnStart,
			Source: runtimeevents.Source{Component: "agent", Name: "coding"},
			Scope: runtimeevents.Scope{
				SessionKey: "thread-1",
				TraceScope: runtimeevents.NewTraceScope("/repo", fmt.Sprintf("turn-%d", i)),
			},
			Payload: agent.TurnStartPayload{UserMessage: "fix it"},
		})
	}
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision != 20 || len(snapshot.Entries) != 20 {
		t.Fatalf("lossless projection = revision %d entries %d", snapshot.Revision, len(snapshot.Entries))
	}
	if dropped := eventBus.Stats().Dropped; dropped == 0 {
		t.Fatal("test did not force ordinary event subscriber loss")
	}
}
