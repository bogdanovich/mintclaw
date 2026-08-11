package agentadapter

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
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
	if strings.Contains(snapshot.Tools[0].Arguments, "secret command") ||
		snapshot.Tools[0].Arguments != "fields: command, timeout" {
		t.Fatalf("argument projection = %q", snapshot.Tools[0].Arguments)
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
