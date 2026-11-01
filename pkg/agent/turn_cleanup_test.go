package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestTerminalLifecycleCleanupDispositionMatchesCanonicalHistory(t *testing.T) {
	loop, agent, closeFixture := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer closeFixture()
	cleanup := &turnCleanupTestTool{
		countingTestTool: &countingTestTool{name: "turn-cleanup"},
		result: tools.TurnCleanupResult{Receipts: []taskresult.Receipt{
			{
				ID:       "browser_cleanup_receipt",
				Kind:     taskresult.ReceiptKindResourceCleanup,
				Target:   "browser:gateway/managed",
				Action:   "close",
				Tool:     "browser_session",
				Summary:  "Browser cleanup confirmed.",
				Metadata: map[string]string{"state": "closed"},
			},
		}},
	}
	agent.Tools.Register(cleanup)
	state := newTurnState(
		agent,
		makeTestTurnSpec("cleanup-history-probe"),
		turnEventScope{turnID: "cleanup-history-probe", context: newTurnContext(nil, nil, nil)},
	)
	emitter := &captureRuntimeEmitter{}
	pipeline := newTestPipeline(loop)
	pipeline.events = emitter
	result, err := runTestTurn(loop, t.Context(), state, pipeline)
	if err != nil {
		t.Fatal(err)
	}
	if result.deliverable == nil || result.deliverable.ObjectiveOutcome == nil ||
		result.deliverable.ObjectiveOutcome.Status != taskresult.OutcomeBlocked {
		t.Fatal("probe did not reach receipt-backed blocked outcome")
	}
	assertOneTerminalOutcome(t, emitter.events, result, 1)
	if state.finalContentSnapshot() != result.finalContent {
		t.Fatal("turn snapshot differs from returned content")
	}
	history := agent.Sessions.GetHistory(state.sessionKey)
	if len(history) == 0 {
		t.Fatal("canonical history absent")
	}
	last := history[len(history)-1]
	if last.Role != "assistant" || last.Content != result.finalContent {
		t.Fatalf("canonical final=%q; returned final=%q", last.Content, result.finalContent)
	}
}

type terminalCleanupDeliveryTool struct {
	fixedToolResultTool
	cleanupCalls int
	executions   int
	onCleanup    func(context.Context)
}

func (tool *terminalCleanupDeliveryTool) Execute(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	tool.executions++
	return tool.fixedToolResultTool.Execute(ctx, args)
}

func (tool *terminalCleanupDeliveryTool) CleanupTurn(ctx context.Context) error {
	tool.cleanupCalls++
	if tool.onCleanup != nil {
		tool.onCleanup(ctx)
	}
	return errors.New("private-cleanup-error-canary")
}

func TestTerminalLifecycleCleanupFailureOwnership(t *testing.T) {
	for _, kind := range []string{"unused", "used", "initial receipt", "continuation origin", "already delivered"} {
		t.Run(kind, func(t *testing.T) {
			var provider providers.LLMProvider = &simpleConvProvider{}
			if kind == "used" || kind == "already delivered" {
				provider = &toolCallRespProvider{toolName: "cleanup_owner", response: "Task completed."}
			}
			loop, agent, closeFixture := newTurnCoordTestLoop(t, provider)
			defer closeFixture()
			tool := &terminalCleanupDeliveryTool{fixedToolResultTool: fixedToolResultTool{
				name: "cleanup_owner", result: toolshared.SilentResult("operation executed"),
			}}
			if kind == "already delivered" {
				tool.result.WithDeliveryIntent(toolshared.DeliveryFinalHandled)
			}
			agent.Tools.Register(tool)
			spec := makeTestTurnSpec("cleanup-ownership-" + kind)
			if kind == "initial receipt" {
				spec.InitialReceipts = []taskresult.Receipt{{Tool: tool.Name()}}
			}
			if kind == "continuation origin" {
				spec.InteractionContinuation.OriginToolName = tool.Name()
			}
			ts := newTurnState(
				agent,
				spec,
				turnEventScope{turnID: "cleanup-ownership", context: newTurnContext(nil, nil, nil)},
			)
			emitter := &captureRuntimeEmitter{}
			pipeline := newTestPipeline(loop)
			pipeline.events = emitter
			result, err := runTestTurn(loop, t.Context(), ts, pipeline)
			if err != nil {
				t.Fatal(err)
			}
			blocked := result.deliverable != nil && result.deliverable.ObjectiveOutcome != nil &&
				result.deliverable.ObjectiveOutcome.Status == taskresult.OutcomeBlocked
			if blocked != (kind != "unused") {
				t.Fatalf("blocked=%v for %s: %#v", blocked, kind, result)
			}
			if tool.cleanupCalls != 1 {
				t.Fatalf("cleanup calls=%d", tool.cleanupCalls)
			}
			if strings.Contains(result.finalContent, "private-cleanup-error-canary") {
				t.Fatal("private cleanup error leaked")
			}
			if kind == "already delivered" &&
				(tool.executions != 1 || result.responseDisposition != finalResponsePending) {
				t.Fatalf(
					"delivery replayed or cleanup notice suppressed: executions=%d result=%#v",
					tool.executions,
					result,
				)
			}
			history := agent.Sessions.GetHistory(ts.sessionKey)
			last := history[len(history)-1]
			if last.Content != result.finalContent || ts.finalContentSnapshot() != result.finalContent {
				t.Fatalf(
					"inconsistent terminal: history=%q snapshot=%q returned=%q",
					last.Content,
					ts.finalContentSnapshot(),
					result.finalContent,
				)
			}
			assertOneTerminalOutcome(t, emitter.events, result, ts.currentIteration())
		})
	}
}

func TestTerminalLifecycleHardAbortDuringCleanupSkipsFinalCommit(t *testing.T) {
	loop, agent, closeFixture := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer closeFixture()
	ts := newTurnState(agent, makeTestTurnSpec("abort-in-cleanup"), turnEventScope{})
	tool := &terminalCleanupDeliveryTool{fixedToolResultTool: fixedToolResultTool{name: "cleanup_owner"}}
	tool.onCleanup = func(ctx context.Context) {
		if ctx.Err() != nil {
			t.Error("cleanup context was canceled")
		}
		_ = ts.requestHardAbort()
	}
	agent.Tools.Register(tool)
	emitter := &captureRuntimeEmitter{}
	pipeline := newTestPipeline(loop)
	pipeline.events = emitter
	result, err := runTestTurn(loop, t.Context(), ts, pipeline)
	if err != nil || result.status != TurnEndStatusAborted || ts.snapshot().Phase != TurnPhaseAborted {
		t.Fatalf("cleanup abort lost: result=%#v err=%v phase=%v", result, err, ts.snapshot().Phase)
	}
	if tool.cleanupCalls != 1 {
		t.Fatalf("cleanup calls=%d", tool.cleanupCalls)
	}
	for _, message := range agent.Sessions.GetHistory(ts.sessionKey) {
		if message.Role == "assistant" {
			t.Fatal("aborted terminal was committed")
		}
	}
}

func TestTerminalLifecycleCleanupSnapshotReachesCodingEventAndStream(t *testing.T) {
	_, agent, closeFixture := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer closeFixture()
	store := &assistantPersistTrackingStore{SessionStore: agent.Sessions}
	agent.Sessions = store
	emitter := &orderedCodingPresentationEmitter{store: store}
	pipeline := &Pipeline{events: emitter}
	spec := makeTestTurnSpec("cleanup-stream")
	spec.mode = turnModeCoding
	ts := newTurnState(agent, spec, turnEventScope{})
	tool := &turnCleanupTestTool{
		countingTestTool: &countingTestTool{name: "cleanup_owner"},
		cleanupErr:       errors.New("private cleanup error"),
	}
	agent.Tools.Register(tool)
	ts.recordToolKind(tool.Name())
	streamer := &orderedFinalPresentationStreamer{emitter: emitter}
	llm := newLLMIterationState(2)
	llm.response = &providers.LLMResponse{ReasoningContent: "unsupported producer reasoning"}
	llm.streamingPublisher = &streamingChunkPublisher{streamer: streamer}
	exec := &turnExecution{model: turnExecutionModel{llmModelName: "test-model"}}
	result, err := pipeline.finalizeTurn(
		t.Context(),
		ts,
		exec,
		llm,
		TurnEndStatusCompleted,
		terminalContent{content: "Task complete."},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !store.assistantPersisted || emitter.emittedBeforeSave || !streamer.eventBeforeFinalize {
		t.Fatal("cleanup terminal did not preserve commit/event/stream ordering")
	}
	if len(streamer.finalized) != 1 || streamer.finalized[0] != result.finalContent || len(emitter.events) != 1 {
		t.Fatalf("stream/event mismatch: stream=%v events=%v result=%#v", streamer.finalized, emitter.events, result)
	}
	payload, ok := emitter.events[0].payload.(AssistantMessageCommittedPayload)
	if !ok || payload.Content != result.finalContent || payload.MessageID != "terminal-message-2" ||
		payload.ReasoningContent != "" {
		t.Fatalf("coding final retained unsupported claims: %#v", emitter.events[0].payload)
	}
	history := agent.Sessions.GetHistory(ts.sessionKey)
	if len(history) != 1 || history[0].Content != result.finalContent ||
		history[0].Deliverable.ObjectiveOutcome.Status != taskresult.OutcomeBlocked {
		t.Fatalf("cleanup terminal history mismatch: %#v", history)
	}
}
