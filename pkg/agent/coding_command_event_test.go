package agent

import (
	"context"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/routing"
	"github.com/bogdanovich/mintclaw/pkg/session"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type codingProgressTool struct{}

func (*codingProgressTool) Name() string        { return "mock_custom" }
func (*codingProgressTool) Description() string { return "Publish a coding command observation" }
func (*codingProgressTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object", "properties": map[string]any{}, "additionalProperties": true,
	}
}

func (*codingProgressTool) CodingStartObservation(map[string]any) *toolshared.ToolObservation {
	return &toolshared.ToolObservation{Command: &toolshared.CommandObservation{
		Action: "run", Command: "printf progress", Source: "agent", Status: "running", OwnsProcess: true,
	}}
}

func (*codingProgressTool) Execute(ctx context.Context, _ map[string]any) *toolshared.ToolResult {
	toolshared.PublishCommandObservation(ctx, toolshared.CommandObservation{
		Action: "run", Command: "printf progress", Source: "agent", Status: "running", OwnsProcess: true,
		Transcript: []toolshared.CommandTranscriptEntry{{Sequence: 1, Stream: "stdout", Text: "progress"}},
	})
	exitCode := 0
	return toolshared.SilentResult("done").WithObservation(toolshared.CommandObservation{
		Action: "run", Command: "printf progress", Source: "agent", Status: "succeeded", OwnsProcess: true,
		ExitCode:   &exitCode,
		Transcript: []toolshared.CommandTranscriptEntry{{Sequence: 1, Stream: "stdout", Text: "progress"}},
	})
}

func TestCodingTurnEmitsCorrelatedCommandStartProgressAndEnd(t *testing.T) {
	cfg := &config.Config{Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
		Workspace: t.TempDir(), ModelName: "test-model", MaxTokens: 4096, MaxToolIterations: 10,
	}}}
	loop := NewAgentLoop(cfg, bus.NewMessageBus(), &scriptedToolProvider{})
	t.Cleanup(loop.Close)
	loop.RegisterTool(&codingProgressTool{})
	agent := loop.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("default agent is missing")
	}
	events, closeEvents := subscribeRuntimeEventsForTest(
		t,
		loop,
		8,
		runtimeevents.KindAgentToolExecStart,
		runtimeevents.KindAgentToolExecProgress,
		runtimeevents.KindAgentToolExecEnd,
	)
	defer closeEvents()

	_, err := loop.runAgentLoop(t.Context(), agent, turnSpec{
		Dispatch: DispatchRequest{
			SessionKey: "thread-1", UserMessage: "run tool",
			InboundContext: &bus.InboundContext{
				Channel: "cli", ChatID: "direct", ChatType: "direct", SenderID: "tester",
			},
			RouteResult: &routing.ResolvedRoute{
				AgentID: "main", Channel: "cli", AccountID: routing.DefaultAccountID,
				SessionPolicy: routing.SessionPolicy{Dimensions: []string{"sender"}}, MatchedBy: "default",
			},
			SessionScope: &session.SessionScope{
				Version: session.ScopeVersion, AgentID: "main", Channel: "cli", Account: routing.DefaultAccountID,
				Dimensions: []string{"sender"}, Values: map[string]string{"sender": "tester"},
			},
		},
		CodingContext:   CodingPromptContext{SessionKey: "thread-1"},
		DefaultResponse: "done",
	})
	if err != nil {
		t.Fatal(err)
	}
	received := collectRuntimeEventStream(events)
	if len(received) != 3 {
		t.Fatalf("command lifecycle event count = %d, want 3: %+v", len(received), received)
	}
	start, ok := received[0].Payload.(ToolExecStartPayload)
	if !ok || start.ToolCallID != "call-1" || start.Observation == nil || start.Observation.Command == nil ||
		start.Observation.Command.Status != "running" {
		t.Fatalf("command start = %#v", received[0].Payload)
	}
	progress, ok := received[1].Payload.(ToolExecProgressPayload)
	if !ok || progress.ToolCallID != start.ToolCallID || progress.Observation == nil ||
		progress.Observation.Command == nil || len(progress.Observation.Command.Transcript) != 1 {
		t.Fatalf("command progress = %#v", received[1].Payload)
	}
	end, ok := received[2].Payload.(ToolExecEndPayload)
	if !ok || end.ToolCallID != start.ToolCallID || end.Observation == nil || end.Observation.Command == nil ||
		end.Observation.Command.Status != "succeeded" {
		t.Fatalf("command end = %#v", received[2].Payload)
	}
}
