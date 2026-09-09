package agent

import (
	"context"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/routing"
	"github.com/bogdanovich/mintclaw/pkg/session"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	fstools "github.com/bogdanovich/mintclaw/pkg/tools/fs"
	"github.com/bogdanovich/mintclaw/pkg/tools/loopguard"
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

func TestCodingToolStartObservationAdmitsNativeExplorationOnlyForCodingTurns(t *testing.T) {
	registry := tools.NewToolRegistry()
	read := fstools.NewReadFileTool(t.TempDir(), true, fstools.MaxReadFileSize)
	registry.Register(read)
	arguments := map[string]any{"path": "pkg/agent/pipeline.go", "ignored": "do not project"}

	ts := &turnState{opts: turnInput{turnPromptInput: turnPromptInput{
		CodingContext: CodingPromptContext{SessionKey: "thread-1"},
	}}}
	observation := codingToolStartObservation(ts, registry, read.Name(), arguments)
	if observation == nil || observation.Exploration == nil ||
		observation.Exploration.Operation != toolshared.ExplorationRead ||
		observation.Exploration.Path != "pkg/agent/pipeline.go" ||
		observation.Command != nil || observation.Plan != nil {
		t.Fatalf("coding exploration observation = %#v", observation)
	}
	if observation := codingToolStartObservation(&turnState{}, registry, read.Name(), arguments); observation != nil {
		t.Fatalf("chat turn leaked coding observation = %#v", observation)
	}
}

func TestCodingMCPObservationRetainsExecutionOutcomeAndAddsLoopHalt(t *testing.T) {
	ts := &turnState{opts: turnInput{turnPromptInput: turnPromptInput{
		CodingContext: CodingPromptContext{SessionKey: "thread-1"},
	}}}
	for _, test := range []struct {
		name      string
		outcome   toolshared.MCPOutcome
		result    string
		errorText string
		code      string
	}{
		{
			name: "successful no progress", outcome: toolshared.MCPOutcomeSucceeded, result: "42 notes",
			code: "identical_call_emergency_halt",
		},
		{
			name: "repeated failures", outcome: toolshared.MCPOutcomeFailed, errorText: "permission denied",
			code: "same_tool_failure_halt",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := toolshared.NewMCPObservation(toolshared.MCPObservation{
				Server: "obsidian", Tool: "get_vault_stats", Purpose: "Get vault statistics",
				Outcome: test.outcome, Result: test.result, Error: test.errorText,
			})
			got := codingToolObservationWithLoopDecision(ts, original, loopguard.Decision{
				Action: loopguard.ActionHalt, Code: test.code, Count: 4, Threshold: 4,
			})
			if got == nil || got.MCP == nil || got.MCP.Outcome != test.outcome ||
				got.MCP.Result != test.result || got.MCP.Error != test.errorText || got.MCP.LoopHaltCode != test.code ||
				got.MCP.LoopHaltCount != 4 || got.MCP.LoopHaltThreshold != 4 {
				t.Fatalf("MCP loop-halt observation = %#v", got)
			}
			if original.MCP.LoopHaltCode != "" {
				t.Fatalf("loop annotation mutated tool result observation: %#v", original)
			}
		})
	}
}
