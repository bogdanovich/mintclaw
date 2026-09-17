package agentadapter

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/bus"
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
		Arguments:  map[string]any{"command": "sk-123456789abcdef", "timeout": 10},
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
	publish(runtimeevents.KindAgentContextCompressStart, agent.ContextCompressLifecyclePayload{
		AttemptID: "attempt-1", ThreadID: "thread-1", TranscriptRevision: 9, TranscriptCount: 14,
		Reason: agent.ContextCompressReasonRetry, Status: agent.ContextCompressLifecycleStarted,
	})
	publish(runtimeevents.KindAgentContextCompressProgress, agent.ContextCompressLifecyclePayload{
		AttemptID: "attempt-1", ThreadID: "thread-1", TranscriptRevision: 9, TranscriptCount: 14,
		Reason: agent.ContextCompressReasonRetry, Status: agent.ContextCompressLifecycleProgress, TokensSaved: 200,
	})
	publish(runtimeevents.KindAgentContextCompressEnd, agent.ContextCompressLifecyclePayload{
		AttemptID: "attempt-1", ThreadID: "thread-1", TranscriptRevision: 9, TranscriptCount: 14,
		Reason: agent.ContextCompressReasonRetry, Status: agent.ContextCompressLifecycleCompleted,
		TokensSaved: 400, TokensBefore: 1800, TokensAfter: 1400, TokenCountsObserved: true,
		SummariesCreated: 3, LeafSummaries: 2, CondensedSummaries: 1, Duration: 1500 * time.Millisecond,
	})
	publish(runtimeevents.KindAgentTurnEnd, agent.TurnEndPayload{
		Status:             agent.TurnEndStatusCompleted,
		FinalContent:       "done",
		ContextUsedTokens:  120,
		ContextLimitTokens: 1000,
	})

	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Activity != frontend.ActivityIdle || snapshot.Status != "completed" {
		t.Fatalf("terminal state = %+v", snapshot)
	}
	if len(snapshot.Messages()) != 2 || snapshot.Messages()[1].Text != "done" {
		t.Fatalf("entries = %+v", snapshot.Messages())
	}
	if len(snapshot.ToolStates()) != 1 || snapshot.ToolStates()[0].Status != frontend.ToolSucceeded {
		t.Fatalf("tools = %+v", snapshot.ToolStates())
	}
	if snapshot.ToolStates()[0].TurnID != "turn-1" || len(snapshot.ToolStates()[0].WriteAudit) != 1 ||
		snapshot.ToolStates()[0].WriteAudit[0].Target != "main.go" {
		t.Fatalf("tool correlation/write audit = %+v", snapshot.ToolStates()[0])
	}
	if snapshot.LastCompaction == nil || snapshot.LastCompaction.AttemptID != "attempt-1" ||
		snapshot.LastCompaction.ThreadID != "thread-1" || snapshot.LastCompaction.TranscriptRevision != 9 ||
		snapshot.LastCompaction.TranscriptCount != 14 {
		t.Fatalf("compaction correlation = %+v", snapshot.LastCompaction)
	}
	if !snapshot.LastCompaction.TokenCountsObserved || snapshot.LastCompaction.TokensBefore != 1800 ||
		snapshot.LastCompaction.TokensAfter != 1400 || snapshot.LastCompaction.TokensSaved != 400 ||
		snapshot.LastCompaction.SummariesCreated != 3 || snapshot.LastCompaction.LeafSummaries != 2 ||
		snapshot.LastCompaction.CondensedSummaries != 1 ||
		snapshot.LastCompaction.Duration != 1500*time.Millisecond {
		t.Fatalf("compaction metrics = %+v", snapshot.LastCompaction)
	}
	if strings.Contains(snapshot.ToolStates()[0].Arguments, "sk-123456789abcdef") ||
		snapshot.ToolStates()[0].Arguments != "fields: command, timeout" {
		t.Fatalf("argument projection = %q", snapshot.ToolStates()[0].Arguments)
	}
	if snapshot.ToolStates()[0].Output != "" {
		t.Fatalf("ordinary tool projected non-presentational output = %q", snapshot.ToolStates()[0].Output)
	}
	if snapshot.ContextUsage.UsedTokens != 120 || snapshot.ContextUsage.LimitTokens != 1000 {
		t.Fatalf("context usage = %+v", snapshot.ContextUsage)
	}
}

func TestAdapterProjectsSemanticMCPLifecycleAndIgnoresDiscovery(t *testing.T) {
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
	scope := runtimeevents.Scope{
		SessionKey: "thread-1", TraceScope: runtimeevents.NewTraceScope("/repo", "turn-1"),
	}
	publishAgent := func(kind runtimeevents.Kind, payload any) {
		wrapped.PublishNonBlocking(runtimeevents.Event{
			Kind: kind, Source: runtimeevents.Source{Component: "agent"}, Scope: scope, Payload: payload,
		})
	}
	publishAgent(runtimeevents.KindAgentTurnStart, agent.TurnStartPayload{UserMessage: "inspect vault"})

	tests := []struct {
		callID    string
		outcome   toolshared.MCPOutcome
		result    string
		errorText string
		failed    bool
		want      frontend.ToolStatus
		halt      bool
	}{
		{
			callID:  "success-1",
			outcome: toolshared.MCPOutcomeSucceeded,
			result:  "42 notes",
			want:    frontend.ToolSucceeded,
		},
		{
			callID: "failed", outcome: toolshared.MCPOutcomeFailed, errorText: "permission denied",
			failed: true, want: frontend.ToolFailed,
		},
		{
			callID: "canceled", outcome: toolshared.MCPOutcomeCanceled, errorText: "canceled",
			failed: true, want: frontend.ToolInterrupted,
		},
		{
			callID: "timeout", outcome: toolshared.MCPOutcomeTimedOut, errorText: "timed out",
			failed: true, want: frontend.ToolFailed,
		},
		{
			callID: "success-2", outcome: toolshared.MCPOutcomeSucceeded, result: "42 notes",
			want: frontend.ToolSucceeded, halt: true,
		},
	}
	for _, test := range tests {
		start := toolshared.NewMCPObservation(toolshared.MCPObservation{
			Server: "obsidian", Tool: "get_vault_stats", Purpose: "Read vault statistics",
			Outcome: toolshared.MCPOutcomeRunning,
		})
		publishAgent(runtimeevents.KindAgentToolExecStart, agent.ToolExecStartPayload{
			ToolCallID: test.callID, Tool: "mcp_obsidian_get_vault_stats",
			Arguments: map[string]any{"token": "sk-123456789abcdef", "recent": 5}, Observation: start,
		})
		endObservation := toolshared.MCPObservation{
			Server: "obsidian", Tool: "get_vault_stats", Purpose: "Read vault statistics",
			Outcome: test.outcome, Result: test.result, Error: test.errorText,
		}
		if test.halt {
			endObservation.LoopHaltCode = "identical_call_emergency_halt"
			endObservation.LoopHaltCount = 4
			endObservation.LoopHaltThreshold = 4
		}
		publishAgent(runtimeevents.KindAgentToolExecEnd, agent.ToolExecEndPayload{
			ToolCallID: test.callID, Tool: "mcp_obsidian_get_vault_stats",
			Duration: 250 * time.Millisecond, IsError: test.failed,
			Observation: toolshared.NewMCPObservation(endObservation),
		})
	}

	wrapped.PublishNonBlocking(runtimeevents.Event{
		Kind:   runtimeevents.KindMCPToolDiscovered,
		Source: runtimeevents.Source{Component: "mcp", Name: "obsidian"},
		Scope:  scope,
	})
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.ToolStates()) != len(tests) {
		t.Fatalf("tools = %+v, discovery must not create executed work", snapshot.ToolStates())
	}
	for index, test := range tests {
		tool := snapshot.ToolStates()[index]
		if tool.CallID != test.callID || tool.Status != test.want || tool.MCP == nil ||
			tool.MCP.Outcome != frontend.MCPOutcome(test.outcome) || tool.Duration != 250*time.Millisecond ||
			tool.Arguments != "fields: recent, token" {
			t.Fatalf("tool %d = %+v", index, tool)
		}
		if (tool.MCP.LoopHaltCode != "") != test.halt {
			t.Fatalf("tool %d halt = %+v", index, tool.MCP)
		}
	}
	encoded := fmt.Sprintf("%+v", snapshot)
	if strings.Contains(encoded, "123456789abcdef") || strings.Contains(encoded, "sk-") {
		t.Fatalf("MCP projection leaked argument values: %s", encoded)
	}
}

func TestAdapterProjectsExactTypedPlanWithoutParsingArgumentsOrOutput(t *testing.T) {
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
	publish(runtimeevents.KindAgentToolExecStart, agent.ToolExecStartPayload{
		ToolCallID: "call-1", Tool: "update_plan",
		Arguments: map[string]any{"plan": "misleading argument plan", "secret": "sk-123456789abcdef"},
	})
	publish(runtimeevents.KindAgentToolExecEnd, agent.ToolExecEndPayload{
		ToolCallID: "call-1", Tool: "update_plan", ForLLMLen: 1_000_000,
		Observation: &toolshared.ToolObservation{Plan: &toolshared.PlanObservation{
			Explanation: "Starting implementation.",
			Steps: []toolshared.PlanStepObservation{
				{Step: "Inspect", Status: toolshared.PlanStepCompleted},
				{Step: "Implement", Status: toolshared.PlanStepInProgress},
				{Step: "Verify", Status: toolshared.PlanStepPending},
			},
		}},
	})

	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Items) != 2 || snapshot.Items[0].Tool == nil || !snapshot.Items[0].Tool.PlanObserved ||
		snapshot.Items[1].Kind != frontend.PresentationPlanUpdate || snapshot.Items[1].Plan == nil {
		t.Fatalf("typed plan items = %+v", snapshot.Items)
	}
	plan := snapshot.Items[1].Plan
	want := []frontend.PlanStepState{
		{Step: "Inspect", Status: frontend.PlanStepCompleted},
		{Step: "Implement", Status: frontend.PlanStepInProgress},
		{Step: "Verify", Status: frontend.PlanStepPending},
	}
	if plan.Explanation != "Starting implementation." || !reflect.DeepEqual(plan.Steps, want) {
		t.Fatalf("typed plan = %+v", plan)
	}
	encoded := fmt.Sprintf("%+v", snapshot)
	if strings.Contains(encoded, "misleading argument plan") || strings.Contains(encoded, "sk-123456789abcdef") {
		t.Fatalf("argument/output content entered presentation: %s", encoded)
	}
	if len(snapshot.ToolStates()) != 1 || snapshot.ToolStates()[0].Arguments != "fields: plan, secret" ||
		snapshot.ToolStates()[0].Output != "" {
		t.Fatalf("generic fallback changed = %+v", snapshot.ToolStates())
	}
}

func TestAdapterProjectsCommittedAssistantPhasesInCausalOrder(t *testing.T) {
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
	scope := runtimeevents.Scope{
		SessionKey: "thread-1", TraceScope: runtimeevents.NewTraceScope("/repo", "turn-1"),
	}
	publish := func(kind runtimeevents.Kind, payload any) {
		wrapped.PublishNonBlocking(runtimeevents.Event{
			Kind: kind, Source: runtimeevents.Source{Component: "agent"}, Scope: scope, Payload: payload,
		})
	}
	publish(runtimeevents.KindAgentTurnStart, agent.TurnStartPayload{UserMessage: "fix it"})
	publish(runtimeevents.KindAgentAssistantMessageCommitted, agent.AssistantMessageCommittedPayload{
		MessageID: "provider-message-1", Phase: agent.AssistantMessagePhaseCommentary,
		Content: "I found the failing parser path.", ReasoningContent: "separate reasoning",
	})
	publish(runtimeevents.KindAgentToolExecStart, agent.ToolExecStartPayload{
		ToolCallID: "call-1", Tool: "read_file",
	})
	publish(runtimeevents.KindAgentAssistantMessageCommitted, agent.AssistantMessageCommittedPayload{
		MessageID: "provider-message-2", Phase: agent.AssistantMessagePhaseFinal,
		Content: "The parser is fixed.",
	})
	publish(runtimeevents.KindAgentTurnEnd, agent.TurnEndPayload{
		Status: agent.TurnEndStatusCompleted, FinalContent: "The parser is fixed.",
	})

	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Messages()) != 4 || snapshot.Messages()[1].Kind != frontend.EntryReasoning ||
		snapshot.Messages()[1].Phase != "" ||
		snapshot.Messages()[2].Phase != frontend.AssistantPhaseCommentary ||
		snapshot.Messages()[3].Phase != frontend.AssistantPhaseFinal {
		t.Fatalf("assistant entries = %+v", snapshot.Messages())
	}
	if len(snapshot.Items) != 6 || snapshot.Items[1].Kind != frontend.PresentationReasoning ||
		snapshot.Items[2].Kind != frontend.PresentationAssistantMessage ||
		snapshot.Items[3].Kind != frontend.PresentationToolCall ||
		snapshot.Items[4].Kind != frontend.PresentationTurnSeparator ||
		snapshot.Items[5].Kind != frontend.PresentationFinalAnswer {
		t.Fatalf("causal presentation order = %+v", snapshot.Items)
	}
	if snapshot.Messages()[3].Text != "The parser is fixed." {
		t.Fatalf("turn end duplicated or replaced final content: %+v", snapshot.Messages())
	}
}

func TestAdapterDropsInvalidOrAmbiguousPlanObservations(t *testing.T) {
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
	exitCode := 0
	wrapped.PublishNonBlocking(runtimeevents.Event{
		Kind: runtimeevents.KindAgentToolExecEnd, Source: runtimeevents.Source{Component: "agent"}, Scope: scope,
		Payload: agent.ToolExecEndPayload{
			ToolCallID: "call-1", Tool: "update_plan",
			Observation: &toolshared.ToolObservation{
				Command: &toolshared.CommandObservation{ExitCode: &exitCode},
				Plan: &toolshared.PlanObservation{Steps: []toolshared.PlanStepObservation{{
					Step: "invalid", Status: "blocked",
				}}},
			},
		},
	})
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Items) != 1 || snapshot.Items[0].Tool == nil || snapshot.Items[0].Plan != nil {
		t.Fatalf("invalid observation entered presentation = %+v", snapshot.Items)
	}
}

func TestAdapterProjectsBoundedToolOwnedCommandObservation(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{TextBytes: 64})
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
	publish(runtimeevents.KindAgentToolExecStart, agent.ToolExecStartPayload{ToolCallID: "call-1", Tool: "exec"})
	exitCode := -1
	publish(runtimeevents.KindAgentToolExecEnd, agent.ToolExecEndPayload{
		ToolCallID: "call-1", Tool: "exec", IsError: true,
		ForLLMLen: 999999, ForUserLen: 999999,
		Observation: &toolshared.ToolObservation{Command: &toolshared.CommandObservation{
			Stdout: strings.Repeat("o", 512), Stderr: strings.Repeat("e", 512), Status: "canceled",
			ExitCode: &exitCode, Truncated: true, Background: true, OwnsProcess: true,
			Canceled: true, SessionID: "session-1",
		}},
	})

	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.ToolStates()) != 1 || snapshot.ToolStates()[0].Command == nil {
		t.Fatalf("command tool = %+v", snapshot.ToolStates())
	}
	tool := snapshot.ToolStates()[0]
	if tool.Status != frontend.ToolInterrupted || tool.Command.Status != frontend.CommandCanceled ||
		!tool.Command.Truncated || !tool.Command.Background || tool.Command.ExitCode == nil ||
		*tool.Command.ExitCode != -1 {
		t.Fatalf("command state = %+v", tool)
	}
	if len(tool.Command.Stdout) > 64 || len(tool.Command.Stderr) > 64 || len(tool.Output) > 64 ||
		strings.Contains(tool.Output, "result available") {
		t.Fatalf("unbounded or prose-derived command output = %+v", tool)
	}
}

func TestAdapterProjectsCommandStartProgressAndCompletionByCallID(t *testing.T) {
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
	scope := runtimeevents.Scope{
		SessionKey: "thread-1", TraceScope: runtimeevents.NewTraceScope("/repo", "turn-1"),
	}
	publish := func(kind runtimeevents.Kind, payload any) {
		wrapped.PublishNonBlocking(runtimeevents.Event{
			Kind: kind, Source: runtimeevents.Source{Component: "agent"}, Scope: scope, Payload: payload,
		})
	}
	for _, callID := range []string{"call-a", "call-b"} {
		publish(runtimeevents.KindAgentToolExecStart, agent.ToolExecStartPayload{
			ToolCallID: callID, Tool: "exec",
			Observation: &toolshared.ToolObservation{Command: &toolshared.CommandObservation{
				Action: "run", Command: "printf " + callID, CWD: "/repo", Source: "agent",
				Status: "running", OwnsProcess: true,
			}},
		})
	}
	publish(runtimeevents.KindAgentToolExecProgress, agent.ToolExecProgressPayload{
		ToolCallID: "call-b", Tool: "exec",
		Observation: &toolshared.ToolObservation{Command: &toolshared.CommandObservation{
			Status: "running", OwnsProcess: true,
			Transcript: []toolshared.CommandTranscriptEntry{{Sequence: 1, Stream: "stdout", Text: "only-b"}},
		}},
	})
	exitCode := 0
	publish(runtimeevents.KindAgentToolExecEnd, agent.ToolExecEndPayload{
		ToolCallID: "call-a", Tool: "exec", Duration: time.Second,
		Observation: &toolshared.ToolObservation{Command: &toolshared.CommandObservation{
			Status: "succeeded", OwnsProcess: true, ExitCode: &exitCode,
			Transcript: []toolshared.CommandTranscriptEntry{{Sequence: 1, Stream: "stdout", Text: "only-a"}},
		}},
	})

	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	byCall := make(map[string]frontend.ToolState, len(snapshot.ToolStates()))
	for _, tool := range snapshot.ToolStates() {
		byCall[tool.CallID] = tool
	}
	if len(byCall) != 2 || byCall["call-a"].Command == nil || byCall["call-b"].Command == nil {
		t.Fatalf("projected commands = %+v", snapshot.ToolStates())
	}
	if byCall["call-a"].Command.Command != "printf call-a" || byCall["call-a"].Status != frontend.ToolSucceeded ||
		!strings.Contains(byCall["call-a"].Output, "only-a") ||
		strings.Contains(byCall["call-a"].Output, "only-b") {
		t.Fatalf("call-a projection = %+v", byCall["call-a"])
	}
	if byCall["call-b"].Command.Command != "printf call-b" || byCall["call-b"].Status != frontend.ToolRunning ||
		!strings.Contains(byCall["call-b"].Output, "only-b") ||
		strings.Contains(byCall["call-b"].Output, "only-a") {
		t.Fatalf("call-b projection = %+v", byCall["call-b"])
	}
}

func TestAdapterProjectsTypedExplorationStartByCallID(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{TextBytes: 32})
	if err != nil {
		t.Fatal(err)
	}
	eventBus := runtimeevents.NewBus()
	wrapped, err := WrapBus(eventBus, projector, "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wrapped.Close() })
	scope := runtimeevents.Scope{
		SessionKey: "thread-1", TraceScope: runtimeevents.NewTraceScope("/repo", "turn-1"),
	}
	wrapped.PublishNonBlocking(runtimeevents.Event{
		Kind: runtimeevents.KindAgentToolExecStart, Source: runtimeevents.Source{Component: "agent"}, Scope: scope,
		Payload: agent.ToolExecStartPayload{
			ToolCallID: "call-search", Tool: "search_files",
			Arguments: map[string]any{"pattern": "must remain shape-only"},
			Observation: &toolshared.ToolObservation{Exploration: &toolshared.ExplorationObservation{
				Operation: toolshared.ExplorationSearch,
				Path:      strings.Repeat("p", 64),
				Pattern:   "ToolStarted",
			}},
		},
	})

	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.ToolStates()) != 1 || snapshot.ToolStates()[0].CallID != "call-search" ||
		snapshot.ToolStates()[0].Exploration == nil ||
		snapshot.ToolStates()[0].Exploration.Operation != frontend.ExplorationSearch ||
		snapshot.ToolStates()[0].Exploration.Pattern != "ToolStarted" ||
		len(snapshot.ToolStates()[0].Exploration.Path) > 32 || !snapshot.ToolStates()[0].Exploration.Truncated ||
		strings.Contains(snapshot.ToolStates()[0].Arguments, "must remain") {
		t.Fatalf("projected exploration = %+v", snapshot.ToolStates())
	}
}

func TestAdapterProjectsRepositoryDiffEndByExactCallID(t *testing.T) {
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
	scope := runtimeevents.Scope{
		SessionKey: "thread-1", TraceScope: runtimeevents.NewTraceScope("/repo", "turn-1"),
	}
	for _, call := range []struct{ id, tool string }{
		{id: "call-a", tool: "repository_diff"},
		{id: "call-b", tool: "repository_diff"},
		{id: "call-wrong-tool", tool: "read_file"},
	} {
		wrapped.PublishNonBlocking(runtimeevents.Event{
			Kind: runtimeevents.KindAgentToolExecStart, Source: runtimeevents.Source{Component: "agent"}, Scope: scope,
			Payload: agent.ToolExecStartPayload{ToolCallID: call.id, Tool: call.tool},
		})
	}
	wrapped.PublishNonBlocking(runtimeevents.Event{
		Kind: runtimeevents.KindAgentToolExecEnd, Source: runtimeevents.Source{Component: "agent"}, Scope: scope,
		Payload: agent.ToolExecEndPayload{
			ToolCallID: "call-b", Tool: "repository_diff", Duration: time.Second,
			Observation: toolshared.NewRepositoryDiffObservation(codingworkspace.DiffResult{
				SchemaVersion: codingworkspace.RepositoryDiffSchemaV1,
				Target:        codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetCurrent},
				Files:         []codingworkspace.DiffFile{{Path: "only-b.go"}},
			}),
		},
	})
	wrapped.PublishNonBlocking(runtimeevents.Event{
		Kind: runtimeevents.KindAgentToolExecEnd, Source: runtimeevents.Source{Component: "agent"}, Scope: scope,
		Payload: agent.ToolExecEndPayload{
			ToolCallID: "call-wrong-tool", Tool: "read_file",
			Observation: toolshared.NewRepositoryDiffObservation(codingworkspace.DiffResult{
				SchemaVersion: codingworkspace.RepositoryDiffSchemaV1,
				Target:        codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetCurrent},
				Files:         []codingworkspace.DiffFile{{Path: "must-not-project.go"}},
			}),
		},
	})

	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	byCall := make(map[string]frontend.ToolState, len(snapshot.ToolStates()))
	for _, tool := range snapshot.ToolStates() {
		byCall[tool.CallID] = tool
	}
	if len(byCall) != 3 || byCall["call-a"].RepositoryDiff != nil ||
		byCall["call-b"].RepositoryDiff == nil || byCall["call-b"].RepositoryDiff.Files[0].Path != "only-b.go" ||
		byCall["call-b"].Status != frontend.ToolSucceeded || byCall["call-wrong-tool"].RepositoryDiff != nil ||
		byCall["call-wrong-tool"].Status != frontend.ToolSucceeded {
		t.Fatalf("repository diff call correlation = %#v", byCall)
	}
}

func TestAdapterKeepsSkippedExplorationVisibleAsFailure(t *testing.T) {
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
	scope := runtimeevents.Scope{
		SessionKey: "thread-1", TraceScope: runtimeevents.NewTraceScope("/repo", "turn-1"),
	}
	publish := func(kind runtimeevents.Kind, payload any) {
		wrapped.PublishNonBlocking(runtimeevents.Event{
			Kind: kind, Source: runtimeevents.Source{Component: "agent"}, Scope: scope, Payload: payload,
		})
	}
	publish(runtimeevents.KindAgentToolExecStart, agent.ToolExecStartPayload{
		ToolCallID: "call-read", Tool: "read_file",
		Observation: &toolshared.ToolObservation{Exploration: &toolshared.ExplorationObservation{
			Operation: toolshared.ExplorationRead, Path: "pkg/missing.go",
		}},
	})
	publish(runtimeevents.KindAgentToolExecSkipped, agent.ToolExecSkippedPayload{
		ToolCallID: "call-read", Tool: "read_file",
	})

	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.ToolStates()) != 1 || snapshot.ToolStates()[0].Exploration == nil ||
		snapshot.ToolStates()[0].Status != frontend.ToolFailed ||
		snapshot.ToolStates()[0].Output != "tool skipped" {
		t.Fatalf("skipped exploration projection = %+v", snapshot.ToolStates())
	}
}

func TestProjectCommandMapsCompletedNonzeroExitToFailure(t *testing.T) {
	exitCode := 7
	command := projectCommand(toolshared.CommandObservation{Status: "done", ExitCode: &exitCode})
	if command.Status != frontend.CommandFailed || command.ExitCode == nil || *command.ExitCode != 7 {
		t.Fatalf("completed nonzero command = %+v", command)
	}
	exitCode = 0
	command = projectCommand(toolshared.CommandObservation{Status: "exited", ExitCode: &exitCode})
	if command.Status != frontend.CommandSucceeded {
		t.Fatalf("completed zero command = %+v", command)
	}
	for _, status := range []string{"failed", "error"} {
		command = projectCommand(toolshared.CommandObservation{Status: status})
		if command.Status != frontend.CommandFailed {
			t.Fatalf("%s command = %+v", status, command)
		}
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
	if len(snapshot.Messages()) != 4 || snapshot.Messages()[0].TurnID != "turn-1" ||
		!strings.Contains(snapshot.Messages()[0].Text, "rate_limit") {
		t.Fatalf("retry/fallback entries = %+v", snapshot.Messages())
	}
	if snapshot.Messages()[0].ID == snapshot.Messages()[1].ID {
		t.Fatalf("repeated retry notices share ID %q", snapshot.Messages()[0].ID)
	}
	if len(snapshot.Items) != 4 || snapshot.Items[0].Kind != frontend.PresentationWarning ||
		snapshot.Items[2].Kind != frontend.PresentationWarning ||
		snapshot.Items[3].Kind != frontend.PresentationError ||
		snapshot.Items[3].Lifecycle != frontend.PresentationFailed {
		t.Fatalf("ordered retry/fallback items = %+v", snapshot.Items)
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
	publish(runtimeevents.KindAgentContextCompressStart, "", agent.ContextCompressLifecyclePayload{
		AttemptID: "attempt-1",
		Reason:    agent.ContextCompressReasonSummarize, Background: true,
		Status: agent.ContextCompressLifecycleStarted,
	})
	publish(runtimeevents.KindAgentContextCompressEnd, "", agent.ContextCompressLifecyclePayload{
		AttemptID: "attempt-1",
		Reason:    agent.ContextCompressReasonSummarize, Background: true,
		Status: agent.ContextCompressLifecycleCompleted, TokensSaved: 500,
	})

	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Activity != frontend.ActivityIdle || snapshot.Status != "completed" {
		t.Fatalf("background compaction reopened completed turn: %+v", snapshot)
	}
	if snapshot.LastCompaction == nil ||
		snapshot.LastCompaction.Status != frontend.CompactionCompleted || !snapshot.LastCompaction.Background {
		t.Fatalf("background compaction view = %+v", snapshot)
	}
	if len(snapshot.Items) != 1 || snapshot.Items[0].Kind != frontend.PresentationCompaction ||
		snapshot.Items[0].Compaction == nil || snapshot.Items[0].Compaction.AttemptID != "attempt-1" ||
		snapshot.Items[0].Lifecycle != frontend.PresentationCompleted || snapshot.Items[0].Revision != 2 {
		t.Fatalf("background compaction presentation = %+v", snapshot.Items)
	}
}

func TestAdapterProjectsCorrelatedForegroundCompactionFailure(t *testing.T) {
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
	for _, event := range []runtimeevents.Event{
		{
			Kind: runtimeevents.KindAgentTurnStart, Source: runtimeevents.Source{Component: "agent"}, Scope: scope,
			Payload: agent.TurnStartPayload{UserMessage: "continue"},
		},
		{
			Kind:   runtimeevents.KindAgentContextCompressStart,
			Source: runtimeevents.Source{Component: "agent"}, Scope: scope,
			Payload: agent.ContextCompressLifecyclePayload{
				AttemptID: "attempt-1",
				Reason:    agent.ContextCompressReasonRetry, Status: agent.ContextCompressLifecycleStarted,
			},
		},
		{
			Kind:   runtimeevents.KindAgentContextCompressEnd,
			Source: runtimeevents.Source{Component: "agent"}, Scope: scope,
			Payload: agent.ContextCompressLifecyclePayload{
				AttemptID: "attempt-1",
				Reason:    agent.ContextCompressReasonRetry, Status: agent.ContextCompressLifecycleFailed,
			},
		},
	} {
		wrapped.PublishNonBlocking(event)
	}
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Activity != frontend.ActivityRunning || snapshot.LastCompaction == nil ||
		snapshot.LastCompaction.Status != frontend.CompactionFailed || snapshot.Status != "context compaction failed" {
		t.Fatalf("failed compaction snapshot = %+v", snapshot)
	}
	if len(snapshot.Items) != 2 || snapshot.Items[1].Kind != frontend.PresentationCompaction ||
		snapshot.Items[1].Compaction == nil || snapshot.Items[1].Compaction.Status != frontend.CompactionFailed ||
		snapshot.Items[1].Lifecycle != frontend.PresentationFailed || snapshot.Items[1].Revision != 2 {
		t.Fatalf("failed compaction presentation = %+v", snapshot.Items)
	}
}

func TestAdapterUsesOwnerSuppliedCompactionMode(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	adapter := Adapter{projector: projector}
	payload := agent.ContextCompressLifecyclePayload{
		Reason: agent.ContextCompressReasonSummarize, Background: false,
	}
	adapter.projectCompaction("", payload, frontend.CompactionRunning)
	started, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if started.Activity != frontend.ActivityCompacting || started.LastCompaction == nil ||
		started.LastCompaction.Background {
		t.Fatalf("foreground summarize start = %+v", started)
	}
	adapter.projectCompaction("", payload, frontend.CompactionCompleted)
	completed, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if completed.Activity != frontend.ActivityIdle || completed.LastCompaction == nil ||
		completed.LastCompaction.Status != frontend.CompactionCompleted || completed.LastCompaction.Background {
		t.Fatalf("foreground summarize completion = %+v", completed)
	}
}

func TestAdapterLateCompactionStartPreservesAcceptedInterrupt(t *testing.T) {
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
	for _, event := range []runtimeevents.Event{
		{
			Kind: runtimeevents.KindAgentTurnStart, Source: runtimeevents.Source{Component: "agent"}, Scope: scope,
			Payload: agent.TurnStartPayload{UserMessage: "continue"},
		},
		{
			Kind: runtimeevents.KindAgentInterruptReceived, Source: runtimeevents.Source{Component: "agent"}, Scope: scope,
			Payload: agent.InterruptReceivedPayload{},
		},
		{
			Kind:   runtimeevents.KindAgentContextCompressStart,
			Source: runtimeevents.Source{Component: "agent"}, Scope: scope,
			Payload: agent.ContextCompressLifecyclePayload{
				Reason: agent.ContextCompressReasonRetry, Status: agent.ContextCompressLifecycleStarted,
			},
		},
	} {
		wrapped.PublishNonBlocking(event)
	}
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Activity != frontend.ActivityInterrupting || snapshot.Status != "interrupt requested" ||
		snapshot.LastCompaction == nil || snapshot.LastCompaction.Status != frontend.CompactionRunning {
		t.Fatalf("late compaction snapshot = %+v", snapshot)
	}
}

func TestAdapterProjectsCodingSteeringWithoutInterruptingActiveTurn(t *testing.T) {
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
	publish(runtimeevents.KindAgentTurnStart, agent.TurnStartPayload{UserMessage: "inspect"})
	publish(runtimeevents.KindAgentInterruptReceived, agent.InterruptReceivedPayload{
		Kind: agent.InterruptKindSteering, CodingSteerID: "steer-1", CodingSteerText: "focus on parser",
	})

	pending, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if pending.Activity != frontend.ActivityRunning || len(pending.PendingInputs) != 1 ||
		len(pending.Messages()) != 1 {
		t.Fatalf("accepted steering projection = %+v", pending)
	}
	publish(runtimeevents.KindAgentSteeringInjected, agent.SteeringInjectedPayload{
		Count:        1,
		CodingSteers: []agent.CodingSteerReceipt{{ID: "steer-1", Text: "focus on parser"}},
	})
	injected, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if injected.Activity != frontend.ActivityRunning || len(injected.PendingInputs) != 0 ||
		len(injected.Messages()) != 2 || injected.Messages()[1].Text != "focus on parser" {
		t.Fatalf("injected steering projection = %+v", injected)
	}
}

func TestCodingSteeringReceiptFieldsAreNotSerialized(t *testing.T) {
	secret := "private-guidance-not-for-event-json"
	for _, payload := range []any{
		agent.InterruptReceivedPayload{
			Kind: agent.InterruptKindSteering, CodingSteerID: "private-id", CodingSteerText: secret,
		},
		agent.SteeringInjectedPayload{
			Count: 1, CodingSteers: []agent.CodingSteerReceipt{{ID: "private-id", Text: secret}},
		},
	} {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "private-id") {
			t.Fatalf("serialized coding steer receipt leaked internal fields: %s", encoded)
		}
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
	if snapshot.Workspace == nil ||
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

	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Activity != frontend.ActivityIdle || snapshot.Status != "interrupted" ||
		len(snapshot.ToolStates()) != 1 || snapshot.ToolStates()[0].Status != frontend.ToolFailed ||
		snapshot.ToolStates()[0].TurnID != "turn-1" || len(snapshot.Items) != 3 ||
		snapshot.Items[1].Lifecycle != frontend.PresentationFailed ||
		snapshot.Items[2].Kind != frontend.PresentationTurnSeparator ||
		snapshot.Items[2].Lifecycle != frontend.PresentationInterrupted {
		t.Fatalf("interrupted snapshot = %+v", snapshot)
	}
}

func TestAdapterInterruptionTerminalizesRunningTool(t *testing.T) {
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
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.ToolStates()) != 1 || snapshot.ToolStates()[0].Status != frontend.ToolInterrupted ||
		len(snapshot.Items) != 2 || snapshot.Items[0].Lifecycle != frontend.PresentationInterrupted ||
		snapshot.Items[1].Kind != frontend.PresentationTurnSeparator ||
		snapshot.Items[1].Lifecycle != frontend.PresentationInterrupted {
		t.Fatalf("interrupted tools = %+v", snapshot.ToolStates())
	}
}

func TestAdapterProjectsSuspendedToolWithoutCompletingIt(t *testing.T) {
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
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.ToolStates()) != 1 || snapshot.ToolStates()[0].Status != frontend.ToolSuspended ||
		snapshot.Activity != frontend.ActivityWaitingInput || snapshot.Status != "waiting for input" {
		t.Fatalf("suspended snapshot = %+v", snapshot)
	}
}

func TestStreamingAndNonStreamingTurnsConvergeWithoutDuplicateFinalContent(t *testing.T) {
	project := func(t *testing.T, streaming bool) frontend.ThreadSnapshot {
		t.Helper()
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
		scope := runtimeevents.Scope{
			SessionKey: "thread-1", TraceScope: runtimeevents.NewTraceScope("/repo", "turn-1"),
		}
		publish := func(kind runtimeevents.Kind, payload any) {
			wrapped.PublishNonBlocking(runtimeevents.Event{
				Kind: kind, Source: runtimeevents.Source{Component: "agent"}, Scope: scope, Payload: payload,
			})
		}
		publish(runtimeevents.KindAgentTurnStart, agent.TurnStartPayload{UserMessage: "hello"})
		if streaming {
			streamer, ok := frontend.NewStreamDelegate(projector, "thread-1").GetStreamer(
				t.Context(), "coding", "thread-1", "thread-1", "", scope.TraceScope,
			)
			if !ok {
				t.Fatal("matching stream was rejected")
			}
			if err = streamer.Update(t.Context(), "hel"); err != nil {
				t.Fatal(err)
			}
			withUsage := streamer.(bus.ContextUsageStreamer)
			if err = withUsage.FinalizeWithContext(t.Context(), "hello", &bus.ContextUsage{
				UsedTokens: 12, TotalTokens: 100,
			}); err != nil {
				t.Fatal(err)
			}
		}
		publish(runtimeevents.KindAgentTurnEnd, agent.TurnEndPayload{
			Status: agent.TurnEndStatusCompleted, FinalContent: "hello",
			ContextUsedTokens: 12, ContextLimitTokens: 100,
		})
		snapshot, err := projector.Snapshot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return snapshot
	}

	streamed := project(t, true)
	nonStreamed := project(t, false)
	streamedMessages := streamed.Messages()
	streamedItems := streamed.Items
	nonStreamedItems := nonStreamed.Items
	streamed.Items = nil
	nonStreamed.Items = nil
	if !reflect.DeepEqual(streamed, nonStreamed) {
		t.Fatalf("streamed state = %+v, want non-streamed %+v", streamed, nonStreamed)
	}
	if len(streamedItems) != 2 || len(nonStreamedItems) != 2 ||
		streamedItems[1].Kind != frontend.PresentationFinalAnswer ||
		streamedItems[1].Lifecycle != frontend.PresentationCompleted ||
		nonStreamedItems[1].Lifecycle != frontend.PresentationCompleted {
		t.Fatalf("streamed items = %+v, non-streamed items = %+v", streamedItems, nonStreamedItems)
	}
	if len(streamedMessages) != 2 || streamedMessages[1].Text != "hello" ||
		!streamedMessages[1].Complete {
		t.Fatalf("stream finalization view = %+v", streamed)
	}
}

func TestStreamingFallbackAndVisibleFailureRemainUnambiguous(t *testing.T) {
	tests := []struct {
		name         string
		visible      bool
		turnStatus   agent.TurnEndStatus
		finalContent string
		wantText     string
		wantComplete bool
		wantOutcome  frontend.TurnOutcome
	}{
		{
			name: "fallback before visible output", turnStatus: agent.TurnEndStatusCompleted,
			finalContent: "fallback answer", wantText: "fallback answer", wantComplete: true,
			wantOutcome: frontend.TurnOutcomeCompleted,
		},
		{
			name: "failure after visible output", visible: true, turnStatus: agent.TurnEndStatusError,
			wantText: "partial answer", wantOutcome: frontend.TurnOutcomeFailed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
			scope := runtimeevents.Scope{
				SessionKey: "thread-1", TraceScope: runtimeevents.NewTraceScope("/repo", "turn-1"),
			}
			streamer, ok := frontend.NewStreamDelegate(projector, "thread-1").GetStreamer(
				t.Context(), "coding", "thread-1", "thread-1", "", scope.TraceScope,
			)
			if !ok {
				t.Fatal("matching stream was rejected")
			}
			if tt.visible {
				if err = streamer.Update(t.Context(), "partial answer"); err != nil {
					t.Fatal(err)
				}
			} else {
				reasoning := streamer.(bus.ReasoningStreamer)
				if err = reasoning.UpdateReasoning(t.Context(), "failed provider reasoning"); err != nil {
					t.Fatal(err)
				}
				streamer.Cancel(t.Context())
			}
			wrapped.PublishNonBlocking(runtimeevents.Event{
				Kind: runtimeevents.KindAgentTurnEnd, Source: runtimeevents.Source{Component: "agent"}, Scope: scope,
				Payload: agent.TurnEndPayload{Status: tt.turnStatus, FinalContent: tt.finalContent},
			})
			snapshot, err := projector.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Messages()) != 1 || snapshot.Messages()[0].Text != tt.wantText ||
				snapshot.Messages()[0].Complete != tt.wantComplete || snapshot.LastTurn == nil ||
				snapshot.LastTurn.Outcome != tt.wantOutcome {
				t.Fatalf("stream terminal state = %+v", snapshot)
			}
		})
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
	if len(snapshot.Messages()) != 20 {
		t.Fatalf("lossless projection entries = %d", len(snapshot.Messages()))
	}
	if dropped := eventBus.Stats().Dropped; dropped == 0 {
		t.Fatal("test did not force ordinary event subscriber loss")
	}
}

func TestWrappedBusPreservesCommittedCommentaryWhenOrdinarySubscriberDrops(t *testing.T) {
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

	scope := runtimeevents.Scope{
		SessionKey: "thread-1",
		TraceScope: runtimeevents.NewTraceScope("/repo", "turn-1"),
	}
	wrapped.PublishNonBlocking(runtimeevents.Event{
		Kind:    runtimeevents.KindAgentTurnStart,
		Source:  runtimeevents.Source{Component: "agent", Name: "coding"},
		Scope:   scope,
		Payload: agent.TurnStartPayload{UserMessage: "fix it"},
	})

	streamer, ok := frontend.NewStreamDelegate(projector, "thread-1").GetStreamer(
		t.Context(), "coding", "thread-1", "thread-1", "", scope.TraceScope,
	)
	if !ok {
		t.Fatal("matching stream was rejected")
	}
	messageStream, ok := streamer.(interface{ SetAssistantMessageID(string) })
	if !ok {
		t.Fatal("stream does not support assistant message identity")
	}
	messageStream.SetAssistantMessageID("provider-message-1")
	if err = streamer.Update(t.Context(), "I found the parser boundary."); err != nil {
		t.Fatal(err)
	}

	result := wrapped.PublishNonBlocking(runtimeevents.Event{
		Kind:   runtimeevents.KindAgentAssistantMessageCommitted,
		Source: runtimeevents.Source{Component: "agent", Name: "coding"},
		Scope:  scope,
		Payload: agent.AssistantMessageCommittedPayload{
			MessageID: "provider-message-1",
			Phase:     agent.AssistantMessagePhaseCommentary,
			Content:   "I found the parser boundary.",
		},
	})
	if result.Dropped == 0 {
		t.Fatal("test did not drop the committed event from the ordinary subscriber")
	}
	streamer.Cancel(t.Context())

	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Messages()) != 2 {
		t.Fatalf("entries = %+v", snapshot.Messages())
	}
	commentary := snapshot.Messages()[1]
	if commentary.Kind != frontend.EntryAssistant ||
		commentary.ID != "turn-1:assistant:provider-message-1" ||
		commentary.Phase != frontend.AssistantPhaseCommentary ||
		commentary.Text != "I found the parser boundary." || !commentary.Complete {
		t.Fatalf("committed commentary after subscriber drop = %+v", commentary)
	}
}
