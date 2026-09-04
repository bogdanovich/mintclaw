package agentadapter

import (
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
	if len(snapshot.ChangedFiles) != 1 || snapshot.ChangedFiles[0].Path != "main.go" ||
		snapshot.ChangedFiles[0].CallID != "call-1" {
		t.Fatalf("verified changed files = %+v", snapshot.ChangedFiles)
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
	if strings.Contains(snapshot.Tools[0].Arguments, "sk-123456789abcdef") ||
		snapshot.Tools[0].Arguments != "fields: command, timeout" {
		t.Fatalf("argument projection = %q", snapshot.Tools[0].Arguments)
	}
	if snapshot.Tools[0].Output != "" {
		t.Fatalf("ordinary tool projected non-presentational output = %q", snapshot.Tools[0].Output)
	}
	if snapshot.ContextUsage.UsedTokens != 120 || snapshot.ContextUsage.LimitTokens != 1000 {
		t.Fatalf("context usage = %+v", snapshot.ContextUsage)
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
	if len(snapshot.Items) != 2 || snapshot.Items[1].Kind != frontend.PresentationPlanUpdate ||
		snapshot.Items[1].Plan == nil {
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
	if len(snapshot.Tools) != 1 || snapshot.Tools[0].Arguments != "fields: plan, secret" ||
		snapshot.Tools[0].Output != "" {
		t.Fatalf("generic fallback changed = %+v", snapshot.Tools)
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
			ExitCode: &exitCode, Truncated: true, Background: true, Canceled: true, SessionID: "session-1",
		}},
	})

	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Tools) != 1 || snapshot.Tools[0].Command == nil {
		t.Fatalf("command tool = %+v", snapshot.Tools)
	}
	tool := snapshot.Tools[0]
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
		Reason: agent.ContextCompressReasonSummarize, Background: true,
		Status: agent.ContextCompressLifecycleStarted,
	})
	publish(runtimeevents.KindAgentContextCompressEnd, "", agent.ContextCompressLifecyclePayload{
		Reason: agent.ContextCompressReasonSummarize, Background: true,
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
				Reason: agent.ContextCompressReasonRetry, Status: agent.ContextCompressLifecycleStarted,
			},
		},
		{
			Kind:   runtimeevents.KindAgentContextCompressEnd,
			Source: runtimeevents.Source{Component: "agent"}, Scope: scope,
			Payload: agent.ContextCompressLifecyclePayload{
				Reason: agent.ContextCompressReasonRetry, Status: agent.ContextCompressLifecycleFailed,
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
		len(snapshot.Tools) != 1 || snapshot.Tools[0].Status != frontend.ToolFailed ||
		snapshot.Tools[0].TurnID != "turn-1" || len(snapshot.Items) != 2 ||
		snapshot.Items[1].Lifecycle != frontend.PresentationFailed {
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
	if len(snapshot.Tools) != 1 || snapshot.Tools[0].Status != frontend.ToolInterrupted ||
		len(snapshot.Items) != 1 || snapshot.Items[0].Lifecycle != frontend.PresentationInterrupted {
		t.Fatalf("interrupted tools = %+v", snapshot.Tools)
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
	if len(snapshot.Tools) != 1 || snapshot.Tools[0].Status != frontend.ToolSuspended ||
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
	streamedItems := streamed.Items
	nonStreamedItems := nonStreamed.Items
	streamed.Items = nil
	nonStreamed.Items = nil
	if !reflect.DeepEqual(streamed, nonStreamed) {
		t.Fatalf("streamed state = %+v, want non-streamed %+v", streamed, nonStreamed)
	}
	if len(streamedItems) != 2 || len(nonStreamedItems) != 2 ||
		streamedItems[1].Kind != frontend.PresentationAssistantMessage ||
		streamedItems[1].Lifecycle != frontend.PresentationCompleted ||
		nonStreamedItems[1].Lifecycle != frontend.PresentationCompleted {
		t.Fatalf("streamed items = %+v, non-streamed items = %+v", streamedItems, nonStreamedItems)
	}
	if len(streamed.Entries) != 2 || streamed.Entries[1].Text != "hello" ||
		!streamed.Entries[1].Complete {
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
			if len(snapshot.Entries) != 1 || snapshot.Entries[0].Text != tt.wantText ||
				snapshot.Entries[0].Complete != tt.wantComplete || snapshot.LastTurn == nil ||
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
	if len(snapshot.Entries) != 20 {
		t.Fatalf("lossless projection entries = %d", len(snapshot.Entries))
	}
	if dropped := eventBus.Stats().Dropped; dropped == 0 {
		t.Fatal("test did not force ordinary event subscriber loss")
	}
}
