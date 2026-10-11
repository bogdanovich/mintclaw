package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	taskregistry "github.com/bogdanovich/mintclaw/pkg/tasks"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestChildTurnEventMetadataCarriesParentCorrelation(t *testing.T) {
	state := &turnState{
		workspace: "/workspace/browser", turnID: "browser-turn-2", agentID: "browser",
		sessionKey: "durable-session", parentTurnID: "main-turn-1", childTurnID: "subturn-1",
	}
	meta := state.eventMeta("test", "turn.start").
		withModelAttemptID("model-attempt-1").
		withToolCallCorrelation("tool-call-1", "tool-call-generation-1")
	correlation := runtimeCorrelationFromHookMeta(meta)
	if meta.ParentTurnID != "main-turn-1" ||
		meta.ChildTurnID != "subturn-1" ||
		correlation.ParentTurnID != "main-turn-1" ||
		correlation.ChildTurnID != "subturn-1" ||
		correlation.ModelAttemptID != "model-attempt-1" ||
		correlation.ToolCallID != "tool-call-1" ||
		correlation.ToolCallGeneration != "tool-call-generation-1" {
		t.Fatalf("child event metadata = %#v", meta)
	}
}

func TestToolLoopDecisionTraceRetainsExactCallGeneration(t *testing.T) {
	started := time.Now()
	correlation := runtimeevents.Correlation{
		ModelAttemptID:     "model-attempt-1",
		ToolCallID:         "provider-call-1",
		ToolCallGeneration: "tool-call-generation-1",
	}
	eventCorrelation := runtimeevents.Correlation{ModelAttemptID: correlation.ModelAttemptID}
	record, _, ok := runtimeEventRecord(
		traceCaptureSettings{contentMode: diagnostictrace.ContentMetadataOnly},
		&activeTraceCapture{startedAt: started, turnID: "turn-tool-loop"},
		runtimeevents.Event{
			ID: "event-tool-loop", Kind: runtimeevents.KindAgentToolLoopDecision,
			Time: started.Add(time.Millisecond), Correlation: eventCorrelation,
			Payload: ToolLoopDecisionPayload{
				ToolCallID:         correlation.ToolCallID,
				ToolCallGeneration: correlation.ToolCallGeneration,
				Tool:               "read_file",
				ArgsHash:           "args-hash",
				Action:             "block",
				Code:               "repeated_exact_failure_block",
				Count:              3,
				Threshold:          3,
			},
		},
	)
	if !ok || record.Kind != diagnostictrace.RecordToolLoopDecision {
		t.Fatalf("loop decision event produced kind %q, ok=%v", record.Kind, ok)
	}
	if record.Correlation.ModelAttemptID != correlation.ModelAttemptID ||
		record.Correlation.ToolCallID != correlation.ToolCallID ||
		record.Correlation.ToolCallGeneration != correlation.ToolCallGeneration {
		t.Fatalf("loop decision correlation = %+v", record.Correlation)
	}
}

func TestTraceCaptureRetainsLateAsyncCompletionAndDeliveryCorrelation(t *testing.T) {
	workspace := traceTestWorkspace(t)
	eventBus := runtimeevents.NewBus()
	manager := newTraceCaptureManager(traceTestConfig(workspace), eventBus)
	t.Cleanup(func() {
		manager.close()
		_ = eventBus.Close()
	})

	startedAt := time.Now().UTC()
	parentTraceScope := runtimeevents.NewTraceScope(workspace, "turn-async-parent")
	completionTraceScope := runtimeevents.NewTraceScope(workspace, "async-completion-1")
	scope := runtimeevents.Scope{
		TraceScope: parentTraceScope, AgentID: "main", SessionKey: "session-async-delivery",
		Channel: "telegram", ChatID: "chat-async-delivery",
	}
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "start", Kind: runtimeevents.KindAgentTurnStart, Time: startedAt, Scope: scope,
		Payload: TurnStartPayload{Workspace: workspace},
	})
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "end", Kind: runtimeevents.KindAgentTurnEnd,
		Time: startedAt.Add(time.Millisecond), Scope: scope,
		Payload: TurnEndPayload{Workspace: workspace, Status: TurnEndStatusCompleted},
	})
	parentPath := waitForTraceFile(t, workspace)

	completionScope := scope
	completionScope.TraceScope = completionTraceScope
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "async-completion", Kind: runtimeevents.KindAgentAsyncCompletion,
		Time: startedAt.Add(2 * time.Millisecond), Scope: completionScope,
		Correlation: runtimeevents.Correlation{
			ParentTurnID:       parentTraceScope.TurnID,
			ModelAttemptID:     "model-attempt-1",
			ToolCallID:         "tool-call-1",
			ToolCallGeneration: "tool-call-generation-1",
		},
		Payload: AsyncCompletionPayload{
			SourceTool: "coding_task", TaskID: "task-1", TaskGenerationID: "task-generation-1",
			CompletionID: "completion-1", DeliveryMode: string(toolshared.AsyncDeliveryUserOnly),
			RemoteCoding: &RemoteCodingCompletionEvidence{
				WorkerGenerationID: "worker-generation-1", RemoteThreadID: "remote-thread-1",
				ScopeAlias: "mintclaw-dev", ProfileAlias: "project-yolo",
				TaskStatus: "succeeded", RemoteState: "completed",
			},
			WillUser: true, ContentLen: 12,
		},
	})
	channelScope := runtimeevents.Scope{Channel: "telegram", ChatID: "chat-async-delivery"}
	channelPayload := channels.ChannelOutboundPayload{
		DeliveryID: "delivery-1", TraceScopes: []runtimeevents.TraceScope{completionTraceScope},
		TraceSettlement: true, ContentLen: 12,
	}
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "queued", Kind: runtimeevents.KindChannelMessageOutboundQueued,
		Time: startedAt.Add(3 * time.Millisecond), Scope: channelScope, Payload: channelPayload,
	})
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "sent", Kind: runtimeevents.KindChannelMessageOutboundSent,
		Time: startedAt.Add(4 * time.Millisecond), Scope: channelScope, Payload: channelPayload,
	})

	paths := waitForTraceFiles(t, workspace, 2)
	parentTrace := readCapturedTrace(t, parentPath)
	var trace diagnostictrace.Trace
	for _, path := range paths {
		candidate := readCapturedTrace(t, path)
		if candidate.Metadata.RootTurnID == completionTraceScope.TurnID {
			trace = candidate
		}
	}
	if trace.TraceID == "" {
		t.Fatalf("completion trace not found in %v", paths)
	}
	if err := diagnostictrace.Validate(parentTrace); err != nil {
		t.Fatalf("validate parent trace: %v", err)
	}
	if err := diagnostictrace.Validate(trace); err != nil {
		t.Fatalf("validate async-delivery trace: %v", err)
	}
	if parentTrace.Metadata.RootTurnID != parentTraceScope.TurnID || parentTrace.Outcome == nil ||
		parentTrace.Outcome.Status != string(TurnEndStatusCompleted) {
		t.Fatalf("parent trace metadata/outcome = %#v / %#v", parentTrace.Metadata, parentTrace.Outcome)
	}
	if trace.Metadata.ParentTurnID != parentTraceScope.TurnID || trace.Outcome != nil ||
		trace.Truncation.Incomplete {
		t.Fatalf("completion trace envelope = metadata %#v outcome %#v truncation %#v",
			trace.Metadata, trace.Outcome, trace.Truncation)
	}
	var decision, attempt, outcome *diagnostictrace.Record
	for index := range parentTrace.Records {
		if parentTrace.Records[index].Kind == diagnostictrace.RecordDeliveryDecision {
			t.Fatalf("parent trace unexpectedly retained late completion: %#v", parentTrace.Records[index])
		}
	}
	for index := range trace.Records {
		record := &trace.Records[index]
		switch record.Kind {
		case diagnostictrace.RecordDeliveryDecision:
			decision = record
		case diagnostictrace.RecordDeliveryAttempt:
			attempt = record
		case diagnostictrace.RecordDeliveryOutcome:
			outcome = record
		}
	}
	if decision == nil || attempt == nil || outcome == nil {
		t.Fatalf("async-delivery records = %#v", trace.Records)
	}
	if decision.Scope.TaskID != "task-1" ||
		decision.Scope.ScopeAlias != "mintclaw-dev" ||
		decision.Scope.ProfileAlias != "project-yolo" ||
		decision.Correlation.TaskGenerationID != "task-generation-1" ||
		decision.Correlation.CompletionID != "completion-1" ||
		decision.Correlation.WorkerGenerationID != "worker-generation-1" ||
		decision.Correlation.RemoteThreadID != "remote-thread-1" ||
		decision.Correlation.ParentTurnID != parentTraceScope.TurnID ||
		decision.Correlation.ModelAttemptID != "model-attempt-1" ||
		decision.Correlation.ToolCallID != "tool-call-1" ||
		decision.Correlation.ToolCallGeneration != "tool-call-generation-1" {
		t.Fatalf("delivery decision correlation = scope %#v correlation %#v", decision.Scope, decision.Correlation)
	}
	var decisionPayload diagnostictrace.DeliveryPayload
	if err := json.Unmarshal(decision.Data, &decisionPayload); err != nil {
		t.Fatal(err)
	}
	if decisionPayload.TaskStatus != "succeeded" || decisionPayload.RemoteState != "completed" ||
		decisionPayload.FailureCode != "" {
		t.Fatalf("remote coding delivery decision = %#v", decisionPayload)
	}
	if attempt.Correlation.DeliveryID != "delivery-1" || outcome.Correlation.DeliveryID != "delivery-1" {
		t.Fatalf("delivery attempt/outcome correlation = %#v / %#v", attempt.Correlation, outcome.Correlation)
	}
}

func TestToolResultTraceRetainsDurableTaskIdentity(t *testing.T) {
	started := time.Now()
	record, _, ok := runtimeEventRecord(
		traceCaptureSettings{contentMode: diagnostictrace.ContentMetadataOnly},
		&activeTraceCapture{startedAt: started, turnID: "turn-coding-task"},
		runtimeevents.Event{
			ID: "event-coding-task", Kind: runtimeevents.KindAgentToolExecEnd,
			Time: started.Add(time.Millisecond),
			Payload: ToolExecEndPayload{
				ToolCallID: "tool-call-1", ToolCallGeneration: "tool-generation-1",
				TaskID: "coding-task-1", TaskGenerationID: "task-generation-1",
				Tool: "coding_task",
			},
		},
	)
	if !ok || record.Kind != diagnostictrace.RecordToolResult {
		t.Fatalf("coding task result produced kind %q, ok=%v", record.Kind, ok)
	}
	if record.Scope.TaskID != "coding-task-1" ||
		record.Correlation.TaskGenerationID != "task-generation-1" ||
		record.Correlation.ToolCallID != "tool-call-1" ||
		record.Correlation.ToolCallGeneration != "tool-generation-1" {
		t.Fatalf("coding task result correlation = scope %#v correlation %#v", record.Scope, record.Correlation)
	}
}

func TestRemoteCodingCompletionTraceDistinguishesTerminalStates(t *testing.T) {
	tests := []struct {
		name        string
		taskStatus  string
		remoteState string
		failureCode string
	}{
		{name: "completed", taskStatus: "succeeded", remoteState: "completed"},
		{name: "failed", taskStatus: "failed", remoteState: "failed", failureCode: "worker_failed"},
		{name: "canceled", taskStatus: string(taskregistry.StatusCancelled), remoteState: "canceled"},
		{name: "uncertain", taskStatus: "failed", remoteState: "uncertain", failureCode: "worker_lost"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			started := time.Now()
			record, _, ok := runtimeEventRecord(
				traceCaptureSettings{contentMode: diagnostictrace.ContentMetadataOnly},
				&activeTraceCapture{startedAt: started, turnID: "coding-completion-1"},
				runtimeevents.Event{
					ID: "event-" + test.name, Kind: runtimeevents.KindAgentAsyncCompletion,
					Time: started.Add(time.Millisecond),
					Payload: AsyncCompletionPayload{
						SourceTool: "coding_task", TaskID: "coding-task-1",
						TaskGenerationID: "task-generation-1", CompletionID: "completion-1",
						RemoteCoding: &RemoteCodingCompletionEvidence{
							WorkerGenerationID: "worker-generation-1", RemoteThreadID: "thread-1",
							ScopeAlias: "mintclaw-dev", ProfileAlias: "project-yolo",
							TaskStatus: test.taskStatus, RemoteState: test.remoteState,
							FailureCode: test.failureCode,
						},
					},
				},
			)
			if !ok || record.Kind != diagnostictrace.RecordDeliveryDecision {
				t.Fatalf("remote completion produced kind %q, ok=%v", record.Kind, ok)
			}
			var payload diagnostictrace.DeliveryPayload
			if err := json.Unmarshal(record.Data, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.TaskStatus != test.taskStatus || payload.RemoteState != test.remoteState ||
				payload.FailureCode != test.failureCode {
				t.Fatalf("remote completion terminal evidence = %#v", payload)
			}
		})
	}
}

func TestTraceCaptureProjectsInteractionLifecycleAndLateContinuations(t *testing.T) {
	workspace := traceTestWorkspace(t)
	eventBus := runtimeevents.NewBus()
	manager := newTraceCaptureManager(traceTestConfig(workspace), eventBus)
	t.Cleanup(func() {
		manager.close()
		_ = eventBus.Close()
	})

	startedAt := time.Now().UTC()
	parentScope := runtimeevents.NewTraceScope(workspace, "turn-interaction-parent")
	scope := runtimeevents.Scope{
		TraceScope: parentScope, AgentID: "main", SessionKey: "session-interaction",
		Channel: "telegram", ChatID: "chat-interaction",
	}
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "turn-start", Kind: runtimeevents.KindAgentTurnStart, Time: startedAt,
		Scope: scope, Payload: TurnStartPayload{Workspace: workspace},
	})

	interactionID := "interaction-1"
	toolCorrelation := runtimeevents.Correlation{ToolCallID: "tool-call-question"}
	early := []struct {
		id       string
		kind     runtimeevents.Kind
		event    interactions.EventType
		status   interactions.Status
		revision int64
		delivery string
	}{
		{
			"interaction-created", runtimeevents.KindAgentInteractionCreated,
			interactions.EventCreated, interactions.StatusCreated, 1, "",
		},
		{
			"interaction-prompt", runtimeevents.KindAgentInteractionDelivery,
			interactions.EventPromptDelivery, interactions.StatusCreated, 2, "delivery-prompt",
		},
		{
			"interaction-waiting", runtimeevents.KindAgentInteractionWaiting,
			interactions.EventWaiting, interactions.StatusWaiting, 3, "delivery-prompt",
		},
	}
	for index, fixture := range early {
		publishCaptureEvent(t, eventBus, runtimeevents.Event{
			ID: fixture.id, Kind: fixture.kind, Time: startedAt.Add(time.Duration(index+1) * time.Millisecond),
			Scope: scope, Correlation: toolCorrelation,
			Payload: InteractionEventPayload{
				InteractionID: interactionID, InteractionEventID: fixture.id,
				TaskID: "task-1", DeliveryID: fixture.delivery,
				Kind: interactions.KindQuestion, Event: fixture.event, Status: fixture.status,
				Revision: fixture.revision, Sequence: fixture.revision,
			},
		})
	}
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "turn-end", Kind: runtimeevents.KindAgentTurnEnd, Time: startedAt.Add(4 * time.Millisecond),
		Scope: scope, Payload: TurnEndPayload{Workspace: workspace, Status: TurnEndStatusCompleted},
	})
	parentPath := waitForTraceFile(t, workspace)

	success := true
	late := []struct {
		id       string
		kind     runtimeevents.Kind
		event    interactions.EventType
		status   interactions.Status
		outcome  interactions.Outcome
		revision int64
		delivery string
		code     string
	}{
		{
			"interaction-answer", runtimeevents.KindAgentInteractionAnswer,
			interactions.EventAnswerClaimed, interactions.StatusClaimed,
			interactions.OutcomeAnswered, 4, "delivery-prompt", "answer_accepted",
		},
		{
			"interaction-resume", runtimeevents.KindAgentInteractionResume,
			interactions.EventResumeStarted, interactions.StatusResuming,
			interactions.OutcomeAnswered, 5, "delivery-prompt", "resume_started",
		},
		{
			"interaction-final-delivery", runtimeevents.KindAgentInteractionDelivery,
			interactions.EventFinalDelivery, interactions.StatusResuming,
			interactions.OutcomeAnswered, 6, "delivery-final", "final_bound",
		},
		{
			"interaction-end", runtimeevents.KindAgentInteractionEnd,
			interactions.EventResolved, interactions.StatusResolved,
			interactions.OutcomeAnswered, 7, "delivery-final", "completed",
		},
	}
	for index, fixture := range late {
		publishCaptureEvent(t, eventBus, runtimeevents.Event{
			ID: fixture.id, Kind: fixture.kind, Time: startedAt.Add(time.Duration(index+5) * time.Millisecond),
			Scope: scope, Correlation: toolCorrelation,
			Payload: InteractionEventPayload{
				InteractionID: interactionID, InteractionEventID: fixture.id,
				TaskID: "task-1", DeliveryID: fixture.delivery,
				Kind: interactions.KindQuestion, Event: fixture.event, Status: fixture.status,
				Outcome: fixture.outcome, Revision: fixture.revision, Sequence: fixture.revision,
				Code: fixture.code, Success: &success,
			},
		})
	}

	paths := waitForTraceFiles(t, workspace, 1+len(late))
	parentTrace := readCapturedTrace(t, parentPath)
	if err := diagnostictrace.Validate(parentTrace); err != nil {
		t.Fatalf("validate parent trace: %v", err)
	}
	interactionRecords := make([]diagnostictrace.Record, 0, len(early))
	for _, record := range parentTrace.Records {
		if record.Kind == diagnostictrace.RecordInteractionLifecycle {
			interactionRecords = append(interactionRecords, record)
		}
	}
	if len(interactionRecords) != len(early) {
		t.Fatalf("parent interaction records = %#v", interactionRecords)
	}
	if interactionRecords[1].Correlation.InteractionID != interactionID ||
		interactionRecords[1].Correlation.InteractionRevision != 2 ||
		interactionRecords[1].Correlation.InteractionEventID != "interaction-prompt" ||
		interactionRecords[1].Correlation.DeliveryID != "delivery-prompt" ||
		interactionRecords[1].Correlation.ToolCallID != "tool-call-question" ||
		interactionRecords[1].Scope.TaskID != "task-1" {
		t.Fatalf("parent interaction correlation = scope %#v correlation %#v",
			interactionRecords[1].Scope, interactionRecords[1].Correlation)
	}

	lateEvents := make(map[string]diagnostictrace.Trace, len(late))
	for _, path := range paths {
		trace := readCapturedTrace(t, path)
		if path == parentPath {
			continue
		}
		if err := diagnostictrace.Validate(trace); err != nil {
			t.Fatalf("validate interaction continuation %s: %v", path, err)
		}
		if !strings.HasPrefix(trace.Metadata.RootTurnID, "interaction-") ||
			trace.Metadata.ParentTurnID != parentScope.TurnID || len(trace.Records) != 1 ||
			trace.Records[0].Kind != diagnostictrace.RecordInteractionLifecycle ||
			trace.Truncation.Incomplete {
			t.Fatalf("interaction continuation = %#v", trace)
		}
		var payload diagnostictrace.InteractionPayload
		if err := json.Unmarshal(trace.Records[0].Data, &payload); err != nil {
			t.Fatal(err)
		}
		lateEvents[payload.Event] = trace
	}
	if len(lateEvents) != len(late) {
		t.Fatalf("late interaction events = %#v", lateEvents)
	}
	terminal := lateEvents[string(interactions.EventResolved)].Records[0]
	if terminal.Correlation.InteractionID != interactionID ||
		terminal.Correlation.InteractionRevision != 7 ||
		terminal.Correlation.InteractionEventID != "interaction-end" ||
		terminal.Correlation.DeliveryID != "delivery-final" ||
		terminal.Correlation.ParentTurnID != parentScope.TurnID ||
		terminal.Scope.TaskID != "task-1" {
		t.Fatalf("terminal interaction correlation = scope %#v correlation %#v",
			terminal.Scope, terminal.Correlation)
	}
}

func TestTraceCaptureWaitsForParentOnlySynthesisDelivery(t *testing.T) {
	workspace := traceTestWorkspace(t)
	eventBus := runtimeevents.NewBus()
	manager := newTraceCaptureManager(traceTestConfig(workspace), eventBus)
	t.Cleanup(func() {
		manager.close()
		_ = eventBus.Close()
	})

	traceScope := runtimeevents.NewTraceScope(workspace, "async-completion-parent-only")
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "async-parent-only", Kind: runtimeevents.KindAgentAsyncCompletion,
		Time: time.Now().UTC(),
		Scope: runtimeevents.Scope{
			TraceScope: traceScope, AgentID: "main", SessionKey: "session-parent-only",
		},
		Correlation: runtimeevents.Correlation{ParentTurnID: "turn-parent-only"},
		Payload: AsyncCompletionPayload{
			SourceTool: "spawn", TaskID: "task-parent-only",
			TaskGenerationID: "task-generation-parent-only", CompletionID: "completion-parent-only",
			DeliveryMode: string(toolshared.AsyncDeliveryParentOnly), WillParent: true,
		},
	})
	if matches, _ := filepath.Glob(filepath.Join(workspace, "state", "diagnostics", "traces", "*.json")); len(
		matches,
	) != 0 {
		t.Fatalf("parent-only completion persisted before synthesized delivery: %v", matches)
	}
	channelPayload := channels.ChannelOutboundPayload{
		DeliveryID: "delivery-parent-only", TraceScopes: []runtimeevents.TraceScope{traceScope},
		TraceSettlement: true, ContentLen: 18,
	}
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "parent-only-queued", Kind: runtimeevents.KindChannelMessageOutboundQueued,
		Time: time.Now().UTC(), Payload: channelPayload,
	})
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "parent-only-sent", Kind: runtimeevents.KindChannelMessageOutboundSent,
		Time: time.Now().UTC(), Payload: channelPayload,
	})

	trace := readCapturedTrace(t, waitForTraceFile(t, workspace))
	if err := diagnostictrace.Validate(trace); err != nil {
		t.Fatalf("validate parent-only completion trace: %v", err)
	}
	if trace.Metadata.RootTurnID != traceScope.TurnID || trace.Metadata.ParentTurnID != "turn-parent-only" ||
		len(trace.Records) != 3 || trace.Records[0].Kind != diagnostictrace.RecordDeliveryDecision ||
		trace.Records[1].Correlation.DeliveryID != "delivery-parent-only" ||
		trace.Records[2].Correlation.DeliveryID != "delivery-parent-only" ||
		trace.Truncation.Incomplete {
		t.Fatalf("parent-only completion trace = %#v", trace)
	}
}

func TestTraceCaptureKeepsUserAndParentCompletionOpenThroughFinalDelivery(t *testing.T) {
	workspace := traceTestWorkspace(t)
	eventBus := runtimeevents.NewBus()
	manager := newTraceCaptureManager(traceTestConfig(workspace), eventBus)
	t.Cleanup(func() {
		manager.close()
		_ = eventBus.Close()
	})

	startedAt := time.Now().UTC()
	traceScope := runtimeevents.NewTraceScope(workspace, "async-completion-user-and-parent")
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "async-user-and-parent", Kind: runtimeevents.KindAgentAsyncCompletion,
		Time: startedAt,
		Scope: runtimeevents.Scope{
			TraceScope: traceScope, AgentID: "main", SessionKey: "session-user-and-parent",
		},
		Correlation: runtimeevents.Correlation{ParentTurnID: "turn-user-and-parent"},
		Payload: AsyncCompletionPayload{
			SourceTool: "spawn", TaskID: "task-user-and-parent",
			TaskGenerationID: "task-generation-user-and-parent", CompletionID: "completion-user-and-parent",
			DeliveryMode: string(toolshared.AsyncDeliveryUserAndParent), WillUser: true, WillParent: true,
		},
	})
	interimPayload := channels.ChannelOutboundPayload{
		DeliveryID: "delivery-interim", TraceScopes: []runtimeevents.TraceScope{traceScope},
		ContentLen: 12,
	}
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "interim-queued", Kind: runtimeevents.KindChannelMessageOutboundQueued,
		Time: startedAt.Add(time.Millisecond), Payload: interimPayload,
	})
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "interim-sent", Kind: runtimeevents.KindChannelMessageOutboundSent,
		Time: startedAt.Add(2 * time.Millisecond), Payload: interimPayload,
	})
	if matches, _ := filepath.Glob(filepath.Join(workspace, "state", "diagnostics", "traces", "*.json")); len(
		matches,
	) != 0 {
		t.Fatalf("completion trace settled on intermediate delivery: %v", matches)
	}

	finalPayload := channels.ChannelOutboundPayload{
		DeliveryID: "delivery-final", TraceScopes: []runtimeevents.TraceScope{traceScope},
		TraceSettlement: true, ContentLen: 18,
	}
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "final-queued", Kind: runtimeevents.KindChannelMessageOutboundQueued,
		Time: startedAt.Add(3 * time.Millisecond), Payload: finalPayload,
	})
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "final-sent", Kind: runtimeevents.KindChannelMessageOutboundSent,
		Time: startedAt.Add(4 * time.Millisecond), Payload: finalPayload,
	})

	trace := readCapturedTrace(t, waitForTraceFile(t, workspace))
	if err := diagnostictrace.Validate(trace); err != nil {
		t.Fatalf("validate user-and-parent completion trace: %v", err)
	}
	if len(trace.Records) != 5 || trace.Records[1].Correlation.DeliveryID != "delivery-interim" ||
		trace.Records[2].Correlation.DeliveryID != "delivery-interim" ||
		trace.Records[3].Correlation.DeliveryID != "delivery-final" ||
		trace.Records[4].Correlation.DeliveryID != "delivery-final" || trace.Truncation.Incomplete {
		t.Fatalf("user-and-parent completion trace = %#v", trace)
	}
}

func TestTraceCaptureMarksPendingAsyncDeliveryOnShutdown(t *testing.T) {
	workspace := traceTestWorkspace(t)
	eventBus := runtimeevents.NewBus()
	manager := newTraceCaptureManager(traceTestConfig(workspace), eventBus)
	t.Cleanup(func() { _ = eventBus.Close() })

	traceScope := runtimeevents.NewTraceScope(workspace, "async-completion-pending")
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "async-pending", Kind: runtimeevents.KindAgentAsyncCompletion,
		Time: time.Now().UTC(),
		Scope: runtimeevents.Scope{
			TraceScope: traceScope, AgentID: "main", SessionKey: "session-pending",
		},
		Correlation: runtimeevents.Correlation{ParentTurnID: "turn-pending"},
		Payload: AsyncCompletionPayload{
			SourceTool: "spawn", TaskID: "task-pending", TaskGenerationID: "task-generation-pending",
			CompletionID: "completion-pending", DeliveryMode: string(toolshared.AsyncDeliveryUserOnly),
			WillUser: true,
		},
	})
	manager.close()

	trace := readCapturedTrace(t, waitForTraceFile(t, workspace))
	if !trace.Truncation.Incomplete ||
		!slices.Contains(trace.Truncation.Reasons, traceIncompleteRuntimeClosedBeforeDeliverySettlement) {
		t.Fatalf("pending completion truncation = %#v", trace.Truncation)
	}
}

func TestTraceCaptureRecordsBoundedRedactedTurn(t *testing.T) {
	workspace := traceTestWorkspace(t)
	cfg := traceTestConfig(workspace)
	cfg.Diagnostics.TraceCapture.ContentMode = "redacted_content"
	eventBus := runtimeevents.NewBus()
	manager := newTraceCaptureManager(cfg, eventBus)
	t.Cleanup(func() {
		manager.close()
		_ = eventBus.Close()
	})

	secret := "sk-secret-that-must-not-appear"
	start := time.Now().UTC()
	scope := runtimeevents.Scope{
		TraceScope: runtimeevents.NewTraceScope(workspace, "turn-1"),
		AgentID:    "main",
		SessionKey: "session:" + secret,
		Channel:    "telegram",
		ChatID:     "chat-1",
	}
	modelCorrelation := runtimeevents.Correlation{ModelAttemptID: "model-attempt-1"}
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "evt-start", Kind: runtimeevents.KindAgentTurnStart, Time: start,
		Source: runtimeevents.Source{Component: "agent"}, Scope: scope,
		Correlation: runtimeevents.Correlation{
			ParentTurnID: "parent-turn", ChildTurnID: "subturn-1",
		},
		Payload: TurnStartPayload{UserMessage: "use " + secret, Workspace: workspace},
	})
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "evt-model-request", Kind: runtimeevents.KindAgentLLMRequest,
		Time: start.Add(time.Millisecond), Source: runtimeevents.Source{Component: "agent"}, Scope: scope,
		Correlation: modelCorrelation,
		Payload: LLMRequestPayload{
			Provider: "openai", Model: "gpt-test", MessagesCount: 1, PromptCacheMode: "enabled",
			PromptCache: PromptCacheFingerprint{
				StablePrefixVersion:           "v1",
				StablePrefixHash:              "stable-prefix-hash",
				StableSystemHash:              "stable-system-hash",
				DynamicSystemHash:             "dynamic-system-hash",
				ToolSchemaHash:                "tool-schema-hash",
				HistoryHash:                   "history-hash",
				DynamicTailHash:               "dynamic-tail-hash",
				StableSystemParts:             2,
				DynamicSystemParts:            1,
				HistoryMessages:               4,
				DynamicTailMessages:           1,
				TailBoundaryFound:             true,
				DynamicSystemBeforeTranscript: true,
			},
			DiagnosticMessages: diagnosticMessagesPreview(cfg, []providers.Message{{
				Role: "user", Content: "investigate " + secret,
			}}),
		},
	})
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "evt-tool", Kind: runtimeevents.KindAgentToolExecStart, Time: start.Add(time.Millisecond),
		Source: runtimeevents.Source{Component: "agent"}, Scope: scope,
		Payload: ToolExecStartPayload{
			Tool:      "read_file",
			Arguments: map[string]any{"path": "/tmp/diagnostic.txt", "token": secret},
		},
	})
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "evt-tool-end", Kind: runtimeevents.KindAgentToolExecEnd, Time: start.Add(2 * time.Millisecond),
		Source: runtimeevents.Source{Component: "agent"}, Scope: scope,
		Payload: ToolExecEndPayload{
			Tool: "read_file", IsError: true,
			DiagnosticResult: diagnosticTextPreview(
				cfg, "permission denied while reading "+secret, diagnosticToolResultBytes,
			),
		},
	})
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "evt-retry", Kind: runtimeevents.KindAgentLLMRetry, Time: start.Add(3 * time.Millisecond),
		Source: runtimeevents.Source{Component: "agent"}, Scope: scope,
		Correlation: modelCorrelation,
		Payload: LLMRetryPayload{
			Attempt: 1, Reason: "provider_error", Error: "Bearer " + secret,
		},
	})
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "evt-model-response", Kind: runtimeevents.KindAgentLLMResponse,
		Time: start.Add(4 * time.Millisecond), Source: runtimeevents.Source{Component: "agent"}, Scope: scope,
		Correlation: modelCorrelation,
		Payload: LLMResponsePayload{
			Provider: "openai", Model: "gpt-test", ContentLen: len("diagnosis complete"),
			CacheReadKnown: true, CacheReadInputTokens: 64,
			CacheWriteKnown: true, CacheWriteInputTokens: 8, CacheOutcome: "hit",
			DiagnosticContent: diagnosticTextPreview(
				cfg, "diagnosis complete "+secret, diagnosticModelResponseBytes,
			),
		},
	})
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "evt-error", Kind: runtimeevents.KindAgentError, Time: start.Add(5 * time.Millisecond),
		Source: runtimeevents.Source{Component: "agent"}, Scope: scope,
		Payload: ErrorPayload{
			Stage:   "tool",
			Message: "-----BEGIN PRIVATE KEY-----\n" + secret + "\n-----END PRIVATE KEY-----",
		},
	})
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "evt-fallback", Kind: runtimeevents.KindAgentLLMFallbackAttempt, Time: start.Add(6 * time.Millisecond),
		Source: runtimeevents.Source{Component: "agent"}, Scope: scope,
		Correlation: modelCorrelation,
		Payload: LLMFallbackAttemptPayload{
			Provider:             "openai",
			Model:                "fallback",
			IdentityKey:          "model:fallback",
			Attempt:              2,
			Status:               "failed",
			Reason:               string(providers.FailoverRateLimit),
			ErrorCode:            string(providers.FailoverRateLimit),
			ClassificationSource: "provider_structured",
			ProviderErrorKind:    string(providers.ProviderErrorRateLimit),
			HTTPStatus:           429,
			RetryAfter:           3 * time.Second,
			RequestID:            secret,
			DiagnosticMessage:    "Bearer " + secret + " request rate limited",
		},
	})
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "evt-context", Kind: runtimeevents.KindAgentContextSnapshot, Time: start.Add(7 * time.Millisecond),
		Source: runtimeevents.Source{Component: "agent"}, Scope: scope,
		Payload: ContextSnapshotPayload{
			MessageCount:     3,
			SnapshotHash:     "snapshot-hash",
			GoalHash:         "goal-hash",
			ToolPairingValid: true,
		},
	})
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "evt-end", Kind: runtimeevents.KindAgentTurnEnd, Time: start.Add(8 * time.Millisecond),
		Source: runtimeevents.Source{Component: "agent"}, Scope: scope,
		Payload: TurnEndPayload{
			Status:          TurnEndStatusCompleted,
			Workspace:       workspace,
			FinalContent:    "done " + secret,
			FinalContentLen: 12,
		},
	})

	tracePath := waitForTraceFile(t, workspace)
	data, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatalf("trace leaked secret: %s", data)
	}
	for _, expected := range []string{
		"input_preview", "messages_preview", "arguments_preview", "result_preview",
		"error_preview", "final_preview", "investigate", "permission denied",
		"diagnosis complete", "stable_system_hash", "dynamic_system_before_transcript",
		"[REDACTED]", "[PRIVATE KEY REDACTED]",
	} {
		if !strings.Contains(string(data), expected) {
			t.Fatalf("trace lacks %q: %s", expected, data)
		}
	}
	var trace diagnostictrace.Trace
	if err := json.Unmarshal(data, &trace); err != nil {
		t.Fatalf("decode trace: %v", err)
	}
	if err := diagnostictrace.Validate(trace); err != nil {
		t.Fatalf("validate trace: %v", err)
	}
	if trace.Metadata.ParentTurnID != "parent-turn" || trace.Metadata.ChildTurnID != "subturn-1" {
		t.Fatalf("trace metadata = %#v", trace.Metadata)
	}
	assertModelAttemptCorrelation(
		t,
		trace,
		"model-attempt-1",
		diagnostictrace.RecordModelRequest,
		diagnostictrace.RecordModelRetry,
		diagnostictrace.RecordModelResponse,
		diagnostictrace.RecordModelFallbackAttempt,
	)
	requestPayload := findModelPayload(t, trace, diagnostictrace.RecordModelRequest)
	if requestPayload.PromptCacheMode != "enabled" ||
		requestPayload.StablePrefixVersion != "v1" ||
		requestPayload.StablePrefixHash != "stable-prefix-hash" ||
		requestPayload.StableSystemHash != "stable-system-hash" ||
		requestPayload.DynamicSystemHash != "dynamic-system-hash" ||
		requestPayload.ToolSchemaHash != "tool-schema-hash" ||
		requestPayload.HistoryHash != "history-hash" ||
		requestPayload.DynamicTailHash != "dynamic-tail-hash" ||
		requestPayload.StableSystemParts != 2 ||
		requestPayload.DynamicSystemParts != 1 ||
		requestPayload.HistoryMessages != 4 ||
		requestPayload.DynamicTailMessages != 1 ||
		!requestPayload.TailBoundaryFound ||
		!requestPayload.DynamicSystemBeforeTranscript {
		t.Fatalf("request cache fingerprint = %#v", requestPayload)
	}
	responsePayload := findModelPayload(t, trace, diagnostictrace.RecordModelResponse)
	if responsePayload.Provider != "openai" || responsePayload.Model != "gpt-test" ||
		!responsePayload.CacheReadKnown || responsePayload.CacheReadInputTokens != 64 ||
		!responsePayload.CacheWriteKnown || responsePayload.CacheWriteInputTokens != 8 ||
		responsePayload.CacheOutcome != "hit" {
		t.Fatalf("response cache usage = %#v", responsePayload)
	}
	fallbackPayload := findModelPayload(t, trace, diagnostictrace.RecordModelFallbackAttempt)
	if fallbackPayload.ClassificationSource != "provider_structured" ||
		fallbackPayload.ProviderErrorKind != string(providers.ProviderErrorRateLimit) ||
		fallbackPayload.HTTPStatus != 429 || fallbackPayload.RetryAfterMS != 3000 ||
		fallbackPayload.RequestID != "[REDACTED]" ||
		!strings.Contains(fallbackPayload.ErrorPreview, "request rate limited") {
		t.Fatalf("fallback payload = %#v", fallbackPayload)
	}
	if trace.Outcome == nil || trace.Outcome.Status != string(TurnEndStatusCompleted) {
		t.Fatalf("outcome = %#v", trace.Outcome)
	}
	if len(trace.Records) != 10 {
		t.Fatalf("records = %d, want 10", len(trace.Records))
	}
	if mode := fileModeForTraceTest(t, tracePath); mode.Perm() != 0o600 {
		t.Fatalf("trace mode = %o", mode.Perm())
	}
}

func TestTraceCaptureMetadataOnlyOmitsContentPreviews(t *testing.T) {
	workspace := traceTestWorkspace(t)
	cfg := traceTestConfig(workspace)
	eventBus := runtimeevents.NewBus()
	manager := newTraceCaptureManager(cfg, eventBus)
	t.Cleanup(func() {
		manager.close()
		_ = eventBus.Close()
	})

	secret := "metadata-secret-content"
	credential := "sk-secret-that-must-not-appear"
	start := time.Now().UTC()
	scope := runtimeevents.Scope{
		TraceScope: runtimeevents.NewTraceScope(workspace, "turn-metadata"),
	}
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "start", Kind: runtimeevents.KindAgentTurnStart, Time: start, Scope: scope,
		Payload: TurnStartPayload{UserMessage: secret, Workspace: workspace},
	})
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "tool", Kind: runtimeevents.KindAgentToolExecStart,
		Time: start.Add(time.Millisecond), Scope: scope,
		Payload: ToolExecStartPayload{
			Tool: "read_file", Arguments: map[string]any{"path": secret},
		},
	})
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "fallback", Kind: runtimeevents.KindAgentLLMFallbackAttempt,
		Time: start.Add(2 * time.Millisecond), Scope: scope,
		Payload: LLMFallbackAttemptPayload{
			Provider: "openai", Model: "gpt-test", Attempt: 1, Status: "failed",
			Reason: string(providers.FailoverRateLimit), ErrorCode: string(providers.FailoverRateLimit),
			ClassificationSource: "message_pattern", RequestID: credential, DiagnosticMessage: secret,
		},
	})
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "end", Kind: runtimeevents.KindAgentTurnEnd,
		Time: start.Add(3 * time.Millisecond), Scope: scope,
		Payload: TurnEndPayload{
			Status: TurnEndStatusCompleted, Workspace: workspace, FinalContent: secret,
		},
	})

	data, err := os.ReadFile(waitForTraceFile(t, workspace))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) || strings.Contains(string(data), "_preview") {
		t.Fatalf("metadata trace retained content: %s", data)
	}
	var trace diagnostictrace.Trace
	if err := json.Unmarshal(data, &trace); err != nil {
		t.Fatalf("decode trace: %v", err)
	}
	fallbackPayload := findModelPayload(t, trace, diagnostictrace.RecordModelFallbackAttempt)
	if fallbackPayload.ClassificationSource != "message_pattern" || fallbackPayload.ErrorPreview != "" ||
		fallbackPayload.RequestID != "[REDACTED]" {
		t.Fatalf("metadata fallback payload = %#v", fallbackPayload)
	}
}

func TestProtectedTurnFinalUsesContentIndependentTraceReceipt(t *testing.T) {
	const canary = "browser-diagnostics-trace-canary-8f2c4e"
	settings := traceCaptureSettings{contentMode: diagnostictrace.ContentRedacted}
	record, critical, ok := runtimeEventRecord(
		settings,
		&activeTraceCapture{startedAt: time.Now()},
		runtimeevents.Event{
			Kind: runtimeevents.KindAgentTurnEnd,
			Time: time.Now(),
			Payload: TurnEndPayload{
				Status: TurnEndStatusCompleted, FinalContent: canary, FinalContentLen: len(canary),
				FinalContentProtected: true,
			},
		},
	)
	if !ok || !critical {
		t.Fatalf("protected turn record = (ok=%v critical=%v)", ok, critical)
	}
	if bytes.Contains(record.Data, []byte(canary)) {
		t.Fatalf("protected turn record leaked canary: %s", record.Data)
	}
	var payload diagnostictrace.TurnPayload
	if err := json.Unmarshal(record.Data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.FinalPreview != protectedTurnFinalDiagnosticReceipt ||
		payload.FinalHash != safeHash(settings, protectedTurnFinalDiagnosticReceipt) ||
		payload.FinalLen != len(protectedTurnFinalDiagnosticReceipt) {
		t.Fatalf("protected turn projection = %#v", payload)
	}

	other := TurnEndPayload{
		FinalContent: "different-private-value", FinalContentLen: len("different-private-value"),
		FinalContentProtected: true,
	}
	content, contentLen := diagnosticTurnFinalContent(other)
	if content != protectedTurnFinalDiagnosticReceipt || contentLen != len(protectedTurnFinalDiagnosticReceipt) {
		t.Fatalf("second protected turn projection = (%q, %d)", content, contentLen)
	}
}

func TestLocalDocumentPathIsOmittedFromTurnStartTrace(t *testing.T) {
	path := "/private/workspace/sensitive-tax-return.pdf"
	settings := traceCaptureSettings{contentMode: diagnostictrace.ContentRedacted}
	record, critical, ok := runtimeEventRecord(
		settings,
		&activeTraceCapture{startedAt: time.Now()},
		runtimeevents.Event{
			Kind: runtimeevents.KindAgentTurnStart,
			Time: time.Now(),
			Payload: TurnStartPayload{
				UserMessage: "Read " + path + ".",
			},
		},
	)
	if !ok || !critical || bytes.Contains(record.Data, []byte(path)) {
		t.Fatalf("local-path turn start was not safely projected: %s", record.Data)
	}
	var payload diagnostictrace.TurnPayload
	if err := json.Unmarshal(record.Data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.InputPreview != "" || payload.InputHash == "" || payload.InputLen == 0 {
		t.Fatalf("local-path turn projection = %#v", payload)
	}
}

func TestProjectedLocalDocumentPromptIsOmittedFromTurnStartTrace(t *testing.T) {
	const privateValue = "TRACE_VALUE_CANARY"
	message := protectedLocalPDFSelectorReceipt + " fill family name " + privateValue
	settings := traceCaptureSettings{contentMode: diagnostictrace.ContentRedacted}
	record, critical, ok := runtimeEventRecord(
		settings,
		&activeTraceCapture{startedAt: time.Now()},
		runtimeevents.Event{
			Kind: runtimeevents.KindAgentTurnStart,
			Time: time.Now(),
			Payload: TurnStartPayload{
				UserMessage: message,
			},
		},
	)
	if !ok || !critical || bytes.Contains(record.Data, []byte(privateValue)) ||
		bytes.Contains(record.Data, []byte(protectedLocalPDFSelectorReceipt)) {
		t.Fatalf("projected local-path turn start was not safely projected: %s", record.Data)
	}
	var payload diagnostictrace.TurnPayload
	if err := json.Unmarshal(record.Data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.InputPreview != "" || payload.InputHash == "" || payload.InputLen != len(message) {
		t.Fatalf("projected local-path turn projection = %#v", payload)
	}
}

func TestTraceCaptureDisabledWritesNothing(t *testing.T) {
	workspace := traceTestWorkspace(t)
	cfg := config.DefaultConfig()
	eventBus := runtimeevents.NewBus()
	manager := newTraceCaptureManager(cfg, eventBus)
	if manager.turns.sub != nil || manager.writer != nil {
		t.Fatal("disabled capture started background workers")
	}
	start := time.Now().UTC()
	scope := runtimeevents.Scope{
		TraceScope: runtimeevents.NewTraceScope(workspace, "turn-disabled"),
	}
	eventBus.Publish(
		context.Background(),
		runtimeevents.Event{
			ID:      "start",
			Kind:    runtimeevents.KindAgentTurnStart,
			Time:    start,
			Scope:   scope,
			Payload: TurnStartPayload{Workspace: workspace},
		},
	)
	eventBus.Publish(
		context.Background(),
		runtimeevents.Event{
			ID:      "end",
			Kind:    runtimeevents.KindAgentTurnEnd,
			Time:    start.Add(time.Millisecond),
			Scope:   scope,
			Payload: TurnEndPayload{Workspace: workspace, Status: TurnEndStatusCompleted},
		},
	)
	manager.close()
	_ = eventBus.Close()
	if matches, _ := filepath.Glob(filepath.Join(workspace, "state", "diagnostics", "traces", "*.json")); len(
		matches,
	) != 0 {
		t.Fatalf("disabled capture wrote traces: %v", matches)
	}
}

func TestTraceCaptureStartsLazilyAfterConfigEnable(t *testing.T) {
	workspace := traceTestWorkspace(t)
	cfg := config.DefaultConfig()
	eventBus := runtimeevents.NewBus()
	manager := newTraceCaptureManager(cfg, eventBus)
	enabled := traceTestConfig(workspace)
	manager.updateConfig(enabled)
	if manager.turns.sub == nil || manager.writer == nil {
		t.Fatal("enabling capture did not start workers")
	}
	start := time.Now().UTC()
	scope := runtimeevents.Scope{
		TraceScope: runtimeevents.NewTraceScope(workspace, "turn-enabled"),
	}
	publishCaptureEvent(
		t,
		eventBus,
		runtimeevents.Event{
			ID:      "start",
			Kind:    runtimeevents.KindAgentTurnStart,
			Time:    start,
			Scope:   scope,
			Payload: TurnStartPayload{Workspace: workspace},
		},
	)
	publishCaptureEvent(
		t,
		eventBus,
		runtimeevents.Event{
			ID:      "end",
			Kind:    runtimeevents.KindAgentTurnEnd,
			Time:    start.Add(time.Millisecond),
			Scope:   scope,
			Payload: TurnEndPayload{Workspace: workspace, Status: TurnEndStatusCompleted},
		},
	)
	_ = waitForTraceFile(t, workspace)
	manager.close()
	_ = eventBus.Close()
}

func TestTraceCaptureShutdownDoesNotWaitForIncompleteActiveTurn(t *testing.T) {
	workspace := traceTestWorkspace(t)
	eventBus := runtimeevents.NewBus()
	manager := newTraceCaptureManager(traceTestConfig(workspace), eventBus)
	startedAt := time.Now().UTC()
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "start", Kind: runtimeevents.KindAgentTurnStart, Time: startedAt,
		Scope: runtimeevents.Scope{
			TraceScope: runtimeevents.NewTraceScope(workspace, "active-turn"),
		},
		Payload: TurnStartPayload{Workspace: workspace},
	})

	startedClosing := time.Now()
	manager.close()
	if elapsed := time.Since(startedClosing); elapsed > time.Second {
		t.Fatalf("capture shutdown took %v; diagnostics must not delay runtime shutdown", elapsed)
	}
	trace := readCapturedTrace(t, waitForTraceFile(t, workspace))
	if !trace.Truncation.Incomplete ||
		!slices.Contains(trace.Truncation.Reasons, traceIncompleteRuntimeClosedBeforeTerminalOutcome) {
		t.Fatalf("active-turn truncation = %#v", trace.Truncation)
	}
	if trace.Outcome != nil {
		t.Fatalf("active-turn outcome = %#v, want nil", trace.Outcome)
	}
	_ = eventBus.Close()
}

func TestTraceCaptureShutdownDistinguishesPendingDeliverySettlement(t *testing.T) {
	workspace := traceTestWorkspace(t)
	eventBus := runtimeevents.NewBus()
	manager := newTraceCaptureManager(traceTestConfig(workspace), eventBus)
	startedAt := time.Now().UTC()
	scope := runtimeevents.Scope{
		TraceScope: runtimeevents.NewTraceScope(workspace, "pending-delivery-turn"),
		Channel:    "telegram",
		ChatID:     "chat-pending-delivery",
	}
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "start", Kind: runtimeevents.KindAgentTurnStart, Time: startedAt,
		Scope: scope, Payload: TurnStartPayload{Workspace: workspace},
	})
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "end", Kind: runtimeevents.KindAgentTurnEnd, Time: startedAt.Add(time.Millisecond),
		Scope: scope, Payload: TurnEndPayload{
			Workspace: workspace, Status: TurnEndStatusCompleted, DeliveryExpected: true,
		},
	})

	manager.close()
	trace := readCapturedTrace(t, waitForTraceFile(t, workspace))
	if trace.Outcome == nil || trace.Outcome.Status != string(TurnEndStatusCompleted) {
		t.Fatalf("pending-delivery outcome = %#v", trace.Outcome)
	}
	if !trace.Truncation.Incomplete ||
		!slices.Contains(trace.Truncation.Reasons, traceIncompleteRuntimeClosedBeforeDeliverySettlement) ||
		slices.Contains(trace.Truncation.Reasons, traceIncompleteRuntimeClosedBeforeTerminalOutcome) {
		t.Fatalf("pending-delivery truncation = %#v", trace.Truncation)
	}
	_ = eventBus.Close()
}

func TestTraceCaptureDirectTurnSettlesWithoutChannelDelivery(t *testing.T) {
	workspace := traceTestWorkspace(t)
	fixture := newAgentLoopTestFixtureWithWorkspace(
		t,
		workspace,
		&simpleConvProvider{},
		func(cfg *config.Config) {
			cfg.Diagnostics.TraceCapture.Enabled = true
			cfg.Diagnostics.TraceCapture.ContentMode = "metadata_only"
		},
	)

	response, err := fixture.Loop.ProcessDirectWithOptions(
		t.Context(),
		"finish without channel delivery",
		"direct-trace-session",
		"cli",
		"direct",
		DirectTurnOptions{Stateless: true},
	)
	if err != nil || response == "" {
		t.Fatalf("ProcessDirectWithOptions() = (%q, %v)", response, err)
	}
	trace := readCapturedTrace(t, waitForTraceFile(t, workspace))
	if trace.Outcome == nil || trace.Outcome.Status != string(TurnEndStatusCompleted) {
		t.Fatalf("direct-turn outcome = %#v", trace.Outcome)
	}
	if trace.Truncation.Incomplete {
		t.Fatalf("direct-turn truncation = %#v", trace.Truncation)
	}
	foundTurnEnd := false
	for _, record := range trace.Records {
		foundTurnEnd = foundTurnEnd || record.Kind == diagnostictrace.RecordTurnEnd
	}
	if !foundTurnEnd {
		t.Fatalf("direct-turn records = %#v", trace.Records)
	}
}

func TestTraceCaptureWaitsForExpectedDeliveryOutcome(t *testing.T) {
	workspace := traceTestWorkspace(t)
	eventBus := runtimeevents.NewBus()
	manager := newTraceCaptureManager(traceTestConfig(workspace), eventBus)
	t.Cleanup(func() {
		manager.close()
		_ = eventBus.Close()
	})

	start := time.Now().UTC()
	scope := runtimeevents.Scope{
		TraceScope: runtimeevents.NewTraceScope(workspace, "turn-delivery"),
		SessionKey: "session-delivery",
		Channel:    "telegram",
		ChatID:     "chat-delivery",
	}
	publishCaptureEvent(
		t,
		eventBus,
		runtimeevents.Event{
			ID:      "start",
			Kind:    runtimeevents.KindAgentTurnStart,
			Time:    start,
			Scope:   scope,
			Payload: TurnStartPayload{Workspace: workspace},
		},
	)
	publishCaptureEvent(
		t,
		eventBus,
		runtimeevents.Event{
			ID:    "end",
			Kind:  runtimeevents.KindAgentTurnEnd,
			Time:  start.Add(time.Millisecond),
			Scope: scope,
			Payload: TurnEndPayload{
				Workspace:        workspace,
				Status:           TurnEndStatusCompleted,
				DeliveryExpected: true,
			},
		},
	)
	if matches, _ := filepath.Glob(filepath.Join(workspace, "state", "diagnostics", "traces", "*.json")); len(
		matches,
	) != 0 {
		t.Fatalf("trace persisted before delivery outcome: %v", matches)
	}
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "observed", Kind: runtimeevents.KindChannelMessageOutboundSent, Time: start.Add(2 * time.Millisecond),
		Scope: runtimeevents.Scope{Channel: "telegram", ChatID: "chat-delivery"},
		Payload: channels.ChannelOutboundPayload{
			TraceScopes: []runtimeevents.TraceScope{scope.TraceScope}, ContentLen: 4,
		},
	})
	if matches, _ := filepath.Glob(filepath.Join(workspace, "state", "diagnostics", "traces", "*.json")); len(
		matches,
	) != 0 {
		t.Fatalf("non-settling delivery event persisted trace: %v", matches)
	}
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "sent", Kind: runtimeevents.KindChannelMessageOutboundSent, Time: start.Add(3 * time.Millisecond),
		Scope: runtimeevents.Scope{Channel: "telegram", ChatID: "chat-delivery"},
		Payload: channels.ChannelOutboundPayload{
			TraceScopes: []runtimeevents.TraceScope{scope.TraceScope}, TraceSettlement: true, ContentLen: 4,
		},
	})

	tracePath := waitForTraceFile(t, workspace)
	data, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatal(err)
	}
	var trace diagnostictrace.Trace
	if err := json.Unmarshal(data, &trace); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range trace.Records {
		found = found || record.Kind == diagnostictrace.RecordDeliveryOutcome
	}
	if !found {
		t.Fatalf("trace does not contain delivery outcome: %#v", trace.Records)
	}
}

func TestTraceCaptureClampsLateEventTimestampToAppendOrder(t *testing.T) {
	workspace := traceTestWorkspace(t)
	eventBus := runtimeevents.NewBus()
	manager := newTraceCaptureManager(traceTestConfig(workspace), eventBus)
	t.Cleanup(func() {
		manager.close()
		_ = eventBus.Close()
	})

	startedAt := time.Now().UTC()
	traceScope := runtimeevents.NewTraceScope(workspace, "turn-late-delivery")
	scope := runtimeevents.Scope{TraceScope: traceScope}
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "start", Kind: runtimeevents.KindAgentTurnStart, Time: startedAt, Scope: scope,
		Payload: TurnStartPayload{Workspace: workspace},
	})
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "end", Kind: runtimeevents.KindAgentTurnEnd,
		Time: startedAt.Add(10 * time.Millisecond), Scope: scope,
		Payload: TurnEndPayload{
			Workspace: workspace, Status: TurnEndStatusCompleted, DeliveryExpected: true,
		},
	})
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "sent", Kind: runtimeevents.KindChannelMessageOutboundSent,
		Time: startedAt.Add(5 * time.Millisecond),
		Payload: channels.ChannelOutboundPayload{
			TraceScopes: []runtimeevents.TraceScope{traceScope}, TraceSettlement: true,
		},
	})

	trace := readCapturedTrace(t, waitForTraceFile(t, workspace))
	if err := diagnostictrace.Validate(trace); err != nil {
		t.Fatalf("validate late-delivery trace: %v", err)
	}
	if len(trace.Records) != 3 {
		t.Fatalf("late-delivery trace records = %d, want 3", len(trace.Records))
	}
	if trace.Records[1].Kind != diagnostictrace.RecordTurnEnd ||
		trace.Records[2].Kind != diagnostictrace.RecordDeliveryOutcome ||
		trace.Records[2].OffsetNanos != trace.Records[1].OffsetNanos {
		t.Fatalf("late-delivery offsets = %#v", trace.Records)
	}
}

func TestTraceCaptureSeparatesIdenticalTurnIDsAcrossWorkspaces(t *testing.T) {
	workspaceA := traceTestWorkspace(t)
	workspaceB := traceTestWorkspace(t)
	eventBus := runtimeevents.NewBus()
	manager := newTraceCaptureManager(traceTestConfig(workspaceA), eventBus)
	t.Cleanup(func() {
		manager.close()
		_ = eventBus.Close()
	})

	startedAt := time.Now().UTC()
	for index, item := range []struct {
		workspace string
		tool      string
	}{
		{workspace: workspaceA, tool: "tool-a"},
		{workspace: workspaceB, tool: "tool-b"},
	} {
		scope := runtimeevents.Scope{
			TraceScope: runtimeevents.NewTraceScope(item.workspace, "shared-turn"),
			SessionKey: "shared-session", Channel: "telegram", ChatID: "shared-chat",
		}
		publishCaptureEvent(t, eventBus, runtimeevents.Event{
			ID: "start-" + item.tool, Kind: runtimeevents.KindAgentTurnStart,
			Time: startedAt.Add(time.Duration(index) * time.Millisecond), Scope: scope,
			Payload: TurnStartPayload{Workspace: item.workspace},
		})
		publishCaptureEvent(t, eventBus, runtimeevents.Event{
			ID: "tool-" + item.tool, Kind: runtimeevents.KindAgentToolExecStart,
			Time: startedAt.Add(time.Duration(index+2) * time.Millisecond), Scope: scope,
			Payload: ToolExecStartPayload{Tool: item.tool},
		})
		publishCaptureEvent(t, eventBus, runtimeevents.Event{
			ID: "end-" + item.tool, Kind: runtimeevents.KindAgentTurnEnd,
			Time: startedAt.Add(time.Duration(index+4) * time.Millisecond), Scope: scope,
			Payload: TurnEndPayload{Workspace: item.workspace, Status: TurnEndStatusCompleted},
		})
	}

	traceA := readCapturedTrace(t, waitForTraceFile(t, workspaceA))
	traceB := readCapturedTrace(t, waitForTraceFile(t, workspaceB))
	if traceA.TraceID == traceB.TraceID {
		t.Fatalf("workspace-colliding turns share trace ID %q", traceA.TraceID)
	}
	assertCapturedTools(t, traceA, "tool-a")
	assertCapturedTools(t, traceB, "tool-b")
}

func TestTraceCaptureSettlesEveryScopeOnAggregatedDelivery(t *testing.T) {
	workspace := traceTestWorkspace(t)
	eventBus := runtimeevents.NewBus()
	manager := newTraceCaptureManager(traceTestConfig(workspace), eventBus)
	t.Cleanup(func() {
		manager.close()
		_ = eventBus.Close()
	})

	startedAt := time.Now().UTC()
	traceScopes := []runtimeevents.TraceScope{
		runtimeevents.NewTraceScope(workspace, "turn-1"),
		runtimeevents.NewTraceScope(workspace, "turn-2"),
	}
	for index, traceScope := range traceScopes {
		scope := runtimeevents.Scope{TraceScope: traceScope}
		publishCaptureEvent(t, eventBus, runtimeevents.Event{
			ID: "start-" + traceScope.TurnID, Kind: runtimeevents.KindAgentTurnStart,
			Time: startedAt.Add(time.Duration(index) * time.Millisecond), Scope: scope,
			Payload: TurnStartPayload{Workspace: workspace},
		})
		publishCaptureEvent(t, eventBus, runtimeevents.Event{
			ID: "end-" + traceScope.TurnID, Kind: runtimeevents.KindAgentTurnEnd,
			Time: startedAt.Add(time.Duration(index+2) * time.Millisecond), Scope: scope,
			Payload: TurnEndPayload{
				Workspace: workspace, Status: TurnEndStatusCompleted, DeliveryExpected: true,
			},
		})
	}
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "aggregated-sent", Kind: runtimeevents.KindChannelMessageOutboundSent,
		Time: startedAt.Add(5 * time.Millisecond),
		Payload: channels.ChannelOutboundPayload{
			TraceScopes: traceScopes, TraceSettlement: true, ContentLen: 8,
		},
	})

	for _, path := range waitForTraceFiles(t, workspace, 2) {
		trace := readCapturedTrace(t, path)
		found := false
		for _, record := range trace.Records {
			found = found || record.Kind == diagnostictrace.RecordDeliveryOutcome
		}
		if !found {
			t.Fatalf("aggregated trace %s lacks delivery outcome", trace.Metadata.RootTurnID)
		}
	}
}

func TestTraceCaptureRetainsEarlyTerminalDeliveryUntilTurnEnd(t *testing.T) {
	workspace := traceTestWorkspace(t)
	eventBus := runtimeevents.NewBus()
	manager := newTraceCaptureManager(traceTestConfig(workspace), eventBus)
	t.Cleanup(func() {
		manager.close()
		_ = eventBus.Close()
	})

	startedAt := time.Now().UTC()
	traceScope := runtimeevents.NewTraceScope(workspace, "turn-early-delivery")
	scope := runtimeevents.Scope{TraceScope: traceScope}
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "start", Kind: runtimeevents.KindAgentTurnStart, Time: startedAt, Scope: scope,
		Payload: TurnStartPayload{Workspace: workspace},
	})
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "sent", Kind: runtimeevents.KindChannelMessageOutboundSent,
		Time: startedAt.Add(time.Millisecond),
		Payload: channels.ChannelOutboundPayload{
			TraceScopes: []runtimeevents.TraceScope{traceScope}, TraceSettlement: true,
		},
	})
	if matches, _ := filepath.Glob(filepath.Join(workspace, "state", "diagnostics", "traces", "*.json")); len(
		matches,
	) != 0 {
		t.Fatalf("trace persisted before turn end: %v", matches)
	}
	publishCaptureEvent(t, eventBus, runtimeevents.Event{
		ID: "end", Kind: runtimeevents.KindAgentTurnEnd,
		Time: startedAt.Add(2 * time.Millisecond), Scope: scope,
		Payload: TurnEndPayload{
			Workspace: workspace, Status: TurnEndStatusCompleted, DeliveryExpected: true,
		},
	})

	trace := readCapturedTrace(t, waitForTraceFile(t, workspace))
	if len(trace.Records) != 3 {
		t.Fatalf("early-settled trace records = %d, want 3", len(trace.Records))
	}
}

func TestTraceStoreRootRejectsRelativeTraversal(t *testing.T) {
	workspace := traceTestWorkspace(t)
	settings := traceCaptureSettings{stateDir: "../../outside"}
	want := filepath.Join(workspace, "state", "diagnostics", "traces")
	if got := traceStoreRoot(settings, workspace); got != want {
		t.Fatalf("traceStoreRoot = %q, want %q", got, want)
	}
}

func traceTestWorkspace(t *testing.T) string {
	t.Helper()
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve trace test workspace: %v", err)
	}
	return workspace
}

func traceTestConfig(workspace string) *config.Config {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Diagnostics.TraceCapture.Enabled = true
	cfg.Diagnostics.TraceCapture.ContentMode = "metadata_only"
	return cfg
}

func publishCaptureEvent(t *testing.T, eventBus runtimeevents.Bus, event runtimeevents.Event) {
	t.Helper()
	result := eventBus.Publish(context.Background(), event)
	if result.Delivered == 0 {
		t.Fatalf("event %s was not delivered: %#v", event.Kind, result)
	}
}

func waitForTraceFile(t *testing.T, workspace string) string {
	t.Helper()
	return waitForTraceFiles(t, workspace, 1)[0]
}

func waitForTraceFiles(t *testing.T, workspace string, count int) []string {
	t.Helper()
	pattern := filepath.Join(workspace, "state", "diagnostics", "traces", "*.json")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		matches, _ := filepath.Glob(pattern)
		if len(matches) >= count {
			return matches
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d trace(s) at %s", count, pattern)
	return nil
}

func readCapturedTrace(t *testing.T, path string) diagnostictrace.Trace {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var trace diagnostictrace.Trace
	if err := json.Unmarshal(data, &trace); err != nil {
		t.Fatal(err)
	}
	return trace
}

func findModelPayload(
	t *testing.T,
	trace diagnostictrace.Trace,
	kind diagnostictrace.RecordKind,
) diagnostictrace.ModelPayload {
	t.Helper()
	for _, record := range trace.Records {
		if record.Kind != kind {
			continue
		}
		var payload diagnostictrace.ModelPayload
		if err := json.Unmarshal(record.Data, &payload); err != nil {
			t.Fatalf("decode %s payload: %v", kind, err)
		}
		return payload
	}
	t.Fatalf("trace lacks %s record", kind)
	return diagnostictrace.ModelPayload{}
}

func assertModelAttemptCorrelation(
	t *testing.T,
	trace diagnostictrace.Trace,
	want string,
	kinds ...diagnostictrace.RecordKind,
) {
	t.Helper()
	for _, kind := range kinds {
		found := false
		for _, record := range trace.Records {
			if record.Kind != kind {
				continue
			}
			found = true
			if record.Correlation.ModelAttemptID != want {
				t.Fatalf(
					"%s model attempt = %q, want %q",
					kind,
					record.Correlation.ModelAttemptID,
					want,
				)
			}
		}
		if !found {
			t.Fatalf("trace lacks %s record", kind)
		}
	}
}

func assertCapturedTools(t *testing.T, trace diagnostictrace.Trace, want ...string) {
	t.Helper()
	got := make([]string, 0, len(want))
	for _, record := range trace.Records {
		if record.Kind != diagnostictrace.RecordToolCall {
			continue
		}
		var payload diagnostictrace.ToolPayload
		if err := json.Unmarshal(record.Data, &payload); err != nil {
			t.Fatal(err)
		}
		got = append(got, payload.Tool)
	}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("captured tools = %v, want %v", got, want)
	}
}

func fileModeForTraceTest(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode()
}
