package agentadapter

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
)

func TestAdapterProjectsRuntimeLifecycleWithoutArgumentValues(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	eventBus := runtimeevents.NewBus()
	t.Cleanup(func() { _ = eventBus.Close() })
	adapter, err := Subscribe(t.Context(), eventBus.Channel(), projector, "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })

	scope := runtimeevents.Scope{SessionKey: "thread-1", TraceScope: runtimeevents.NewTraceScope("/repo", "turn-1")}
	publish := func(kind runtimeevents.Kind, payload any) {
		t.Helper()
		result := eventBus.Publish(context.Background(), runtimeevents.Event{
			Kind:    kind,
			Source:  runtimeevents.Source{Component: "agent", Name: "coding"},
			Scope:   scope,
			Payload: payload,
		})
		if result.Delivered != 1 {
			t.Fatalf("publish %s delivered = %d", kind, result.Delivered)
		}
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

	deadline := time.Now().Add(2 * time.Second)
	for {
		snapshot, snapshotErr := projector.Snapshot(t.Context())
		if snapshotErr != nil {
			t.Fatal(snapshotErr)
		}
		if snapshot.Revision >= 6 {
			if snapshot.Activity != frontend.ActivityIdle || snapshot.Status != "completed" {
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
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for projection: %+v", snapshot)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
