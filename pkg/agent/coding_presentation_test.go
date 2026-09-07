package agent

import (
	"context"
	"errors"
	"testing"

	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

type assistantPersistTrackingStore struct {
	session.SessionStore
	assistantPersisted bool
	failAssistant      error
}

func (s *assistantPersistTrackingStore) AppendTurnMessage(
	ctx context.Context,
	sessionKey string,
	message providers.Message,
) error {
	if message.Role == "assistant" && s.failAssistant != nil {
		return s.failAssistant
	}
	if err := s.SessionStore.AppendTurnMessage(ctx, sessionKey, message); err != nil {
		return err
	}
	if message.Role == "assistant" {
		s.assistantPersisted = true
	}
	return nil
}

type orderedCodingPresentationEmitter struct {
	store             *assistantPersistTrackingStore
	events            []capturedRuntimeEvent
	emittedBeforeSave bool
}

type orderedFinalPresentationStreamer struct {
	recordingStreamer
	emitter             *orderedCodingPresentationEmitter
	eventBeforeFinalize bool
}

func (s *orderedFinalPresentationStreamer) Finalize(ctx context.Context, content string) error {
	s.eventBeforeFinalize = s.emitter != nil && len(s.emitter.events) > 0
	return s.recordingStreamer.Finalize(ctx, content)
}

func (e *orderedCodingPresentationEmitter) emitEvent(kind runtimeevents.Kind, _ HookMeta, payload any) {
	if kind == runtimeevents.KindAgentAssistantMessageCommitted {
		if e.store != nil && !e.store.assistantPersisted {
			e.emittedBeforeSave = true
		}
		e.events = append(e.events, capturedRuntimeEvent{kind: kind, payload: payload})
	}
}

func TestCodingToolCommentaryIsCommittedAfterHistoryAndBeforeStreamRollback(t *testing.T) {
	_, agent, cleanup := newTurnCoordTestLoop(t, &sequenceProvider{})
	defer cleanup()
	agent.Tools.Register(resultOnlyDurabilityTestTool{})
	store := &assistantPersistTrackingStore{SessionStore: agent.Sessions}
	agent.Sessions = store
	emitter := &orderedCodingPresentationEmitter{store: store}
	pipeline := &Pipeline{events: emitter}
	opts := makeTestTurnSpec("coding-commentary-session")
	opts.mode = turnModeCoding
	ts := newTurnState(agent, opts, turnEventScope{
		turnID: "coding-commentary-turn", context: newTurnContext(nil, nil, nil),
	})
	exec := &turnExecution{model: turnExecutionModel{llmModelName: "test-model"}}
	streamer := &recordingStreamer{}
	llm := newLLMIterationState(3)
	llm.streamingPublisher = &streamingChunkPublisher{streamer: streamer}
	llm.response = &providers.LLMResponse{
		Content: "I found the parser boundary.", ReasoningContent: "separate reasoning",
		ToolCalls: []providers.ToolCall{{
			ID: "call-1", Name: "result_only_test", Arguments: map[string]any{"value": "safe"},
		}},
	}

	outcome, err := pipeline.normalizeAndDispatchLLMResponse(t.Context(), ts, exec, llm)
	if err != nil || outcome.Control != turnStepExecuteTools {
		t.Fatalf("normalize outcome = %+v, error = %v", outcome, err)
	}
	if !store.assistantPersisted || emitter.emittedBeforeSave {
		t.Fatalf(
			"persistence/event order: persisted=%v premature=%v",
			store.assistantPersisted,
			emitter.emittedBeforeSave,
		)
	}
	if streamer.canceled != 1 || llm.streamingPublisher != nil {
		t.Fatalf("provisional stream rollback = %d, publisher=%#v", streamer.canceled, llm.streamingPublisher)
	}
	if len(emitter.events) != 1 {
		t.Fatalf("committed events = %+v", emitter.events)
	}
	payload, ok := emitter.events[0].payload.(AssistantMessageCommittedPayload)
	if !ok || payload.MessageID != "provider-message-3" ||
		payload.Phase != AssistantMessagePhaseCommentary || payload.Content != "I found the parser boundary." ||
		payload.ReasoningContent != "separate reasoning" {
		t.Fatalf("commentary payload = %#v", emitter.events[0].payload)
	}
}

func TestFailedHistoryWriteDoesNotCommitCodingCommentary(t *testing.T) {
	_, agent, cleanup := newTurnCoordTestLoop(t, &sequenceProvider{})
	defer cleanup()
	agent.Tools.Register(resultOnlyDurabilityTestTool{})
	store := &assistantPersistTrackingStore{
		SessionStore:  agent.Sessions,
		failAssistant: errors.New("injected assistant history failure"),
	}
	agent.Sessions = store
	emitter := &orderedCodingPresentationEmitter{store: store}
	pipeline := &Pipeline{events: emitter}
	opts := makeTestTurnSpec("coding-commentary-failure")
	opts.mode = turnModeCoding
	ts := newTurnState(agent, opts, turnEventScope{
		turnID: "coding-commentary-failure-turn", context: newTurnContext(nil, nil, nil),
	})
	exec := &turnExecution{model: turnExecutionModel{llmModelName: "test-model"}}
	streamer := &recordingStreamer{}
	llm := newLLMIterationState(1)
	llm.streamingPublisher = &streamingChunkPublisher{streamer: streamer}
	llm.response = &providers.LLMResponse{
		Content: "must remain provisional",
		ToolCalls: []providers.ToolCall{{
			ID: "call-1", Name: "result_only_test", Arguments: map[string]any{"value": "safe"},
		}},
	}

	outcome, err := pipeline.normalizeAndDispatchLLMResponse(t.Context(), ts, exec, llm)
	if err != nil || outcome.Control != turnStepExecuteTools {
		t.Fatalf("normalize outcome = %+v, error = %v", outcome, err)
	}
	if len(emitter.events) != 0 || store.assistantPersisted {
		t.Fatalf("failed write committed commentary: events=%+v persisted=%v", emitter.events, store.assistantPersisted)
	}
	if streamer.canceled != 1 || llm.streamingPublisher != nil {
		t.Fatalf("failed attempt rollback = %d, publisher=%#v", streamer.canceled, llm.streamingPublisher)
	}
}

func TestCodingFinalMessageIsCommittedBeforeStreamFinalization(t *testing.T) {
	_, agent, cleanup := newTurnCoordTestLoop(t, &sequenceProvider{})
	defer cleanup()
	store := &assistantPersistTrackingStore{SessionStore: agent.Sessions}
	agent.Sessions = store
	emitter := &orderedCodingPresentationEmitter{store: store}
	pipeline := &Pipeline{events: emitter}
	opts := makeTestTurnSpec("coding-final-session")
	opts.mode = turnModeCoding
	ts := newTurnState(agent, opts, turnEventScope{
		turnID: "coding-final-turn", context: newTurnContext(nil, nil, nil),
	})
	exec := &turnExecution{model: turnExecutionModel{
		llmModelName: "test-model", defaultModelName: "test-model",
	}}
	streamer := &orderedFinalPresentationStreamer{emitter: emitter}
	llm := newLLMIterationState(2)
	llm.response = &providers.LLMResponse{
		Content: "The parser is fixed.", ReasoningContent: "separate final reasoning",
	}
	llm.streamingPublisher = &streamingChunkPublisher{streamer: streamer}

	result, err := pipeline.finalizeTurn(
		t.Context(),
		ts,
		exec,
		llm,
		TurnEndStatusCompleted,
		terminalContent{content: llm.response.Content},
	)
	if err != nil || result.finalContent != llm.response.Content {
		t.Fatalf("finalize result = %+v, error = %v", result, err)
	}
	if !store.assistantPersisted || emitter.emittedBeforeSave || !streamer.eventBeforeFinalize {
		t.Fatalf(
			"final ordering: persisted=%v premature=%v before_finalize=%v",
			store.assistantPersisted,
			emitter.emittedBeforeSave,
			streamer.eventBeforeFinalize,
		)
	}
	if len(emitter.events) != 1 {
		t.Fatalf("final events = %+v", emitter.events)
	}
	payload, ok := emitter.events[0].payload.(AssistantMessageCommittedPayload)
	if !ok || payload.MessageID != "provider-message-2" || payload.Phase != AssistantMessagePhaseFinal ||
		payload.Content != "The parser is fixed." || payload.ReasoningContent != "separate final reasoning" {
		t.Fatalf("final payload = %#v", emitter.events[0].payload)
	}
}

func TestCodingToolCommentarySurvivesDistinctTerminalCommit(t *testing.T) {
	_, agent, cleanup := newTurnCoordTestLoop(t, &sequenceProvider{})
	defer cleanup()
	agent.Tools.Register(resultOnlyDurabilityTestTool{})
	emitter := &orderedCodingPresentationEmitter{}
	pipeline := &Pipeline{events: emitter}
	opts := makeTestTurnSpec("coding-commentary-terminal-session")
	opts.mode = turnModeCoding
	ts := newTurnState(agent, opts, turnEventScope{
		turnID: "coding-commentary-terminal-turn", context: newTurnContext(nil, nil, nil),
	})
	exec := &turnExecution{model: turnExecutionModel{
		llmModelName: "test-model", defaultModelName: "test-model",
	}}
	llm := newLLMIterationState(3)
	llm.response = &providers.LLMResponse{
		Content: "I found the parser boundary.", ReasoningContent: "tool-round reasoning",
		ToolCalls: []providers.ToolCall{{
			ID: "call-1", Name: "result_only_test", Arguments: map[string]any{"value": "safe"},
		}},
	}

	outcome, err := pipeline.normalizeAndDispatchLLMResponse(t.Context(), ts, exec, llm)
	if err != nil || outcome.Control != turnStepExecuteTools {
		t.Fatalf("normalize outcome = %+v, error = %v", outcome, err)
	}
	result, err := pipeline.finalizeTurn(
		t.Context(),
		ts,
		exec,
		llm,
		TurnEndStatusCompleted,
		exactTerminalContent("The tool loop was stopped safely."),
	)
	if err != nil || result.finalContent != "The tool loop was stopped safely." {
		t.Fatalf("finalize result = %+v, error = %v", result, err)
	}
	if len(emitter.events) != 2 {
		t.Fatalf("committed events = %+v", emitter.events)
	}
	commentary, commentaryOK := emitter.events[0].payload.(AssistantMessageCommittedPayload)
	final, finalOK := emitter.events[1].payload.(AssistantMessageCommittedPayload)
	if !commentaryOK || commentary.MessageID != "provider-message-3" ||
		commentary.Phase != AssistantMessagePhaseCommentary ||
		commentary.Content != "I found the parser boundary." {
		t.Fatalf("commentary payload = %#v", emitter.events[0].payload)
	}
	if !finalOK || final.MessageID != "terminal-message-3" ||
		final.Phase != AssistantMessagePhaseFinal || final.Content != "The tool loop was stopped safely." {
		t.Fatalf("final payload = %#v", emitter.events[1].payload)
	}
	if commentary.MessageID == final.MessageID {
		t.Fatalf("commentary and final reused message ID %q", commentary.MessageID)
	}

	history := agent.Sessions.GetHistory(opts.Dispatch.SessionKey)
	var assistants []providers.Message
	for _, message := range history {
		if message.Role == "assistant" {
			assistants = append(assistants, message)
		}
	}
	if len(assistants) != 2 || assistants[0].Content != "I found the parser boundary." ||
		assistants[1].Content != "The tool loop was stopped safely." {
		t.Fatalf("canonical assistant history = %+v", assistants)
	}
}

func TestAssistantPresentationEventIsCodingOnlyAndDropsEmptyMessages(t *testing.T) {
	for _, test := range []struct {
		name      string
		mode      turnMode
		content   string
		reasoning string
		want      int
	}{
		{name: "coding", mode: turnModeCoding, content: "progress", want: 1},
		{name: "ordinary chat", mode: turnModeInbound, content: "progress", want: 0},
		{name: "empty coding message", mode: turnModeCoding, content: "  ", reasoning: "\n", want: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			emitter := &captureRuntimeEmitter{}
			pipeline := &Pipeline{events: emitter}
			opts := makeTestTurnSpec("presentation-mode")
			opts.mode = test.mode
			ts := newTurnState(nil, opts, turnEventScope{turnID: "turn-1"})
			pipeline.emitCodingAssistantMessageCommitted(
				ts,
				"provider-message-1",
				AssistantMessagePhaseCommentary,
				test.content,
				test.reasoning,
			)
			if len(emitter.events) != test.want {
				t.Fatalf("events = %+v, want %d", emitter.events, test.want)
			}
		})
	}
}

func TestRuntimeEventLoggerRedactsCommittedAssistantText(t *testing.T) {
	payload := AssistantMessageCommittedPayload{
		MessageID: "provider-message-2", Phase: AssistantMessagePhaseFinal,
		Content: "final secret", ReasoningContent: "reasoning secret", ContentLen: 12, ReasoningLen: 16,
	}
	safe, ok := runtimeEventLogSafePayload(payload).(AssistantMessageCommittedPayload)
	if !ok || safe.Content != "" || safe.ReasoningContent != "" ||
		safe.MessageID != payload.MessageID || safe.ContentLen != payload.ContentLen ||
		safe.ReasoningLen != payload.ReasoningLen {
		t.Fatalf("safe payload = %#v", safe)
	}
	fields := runtimeEventLogFields(runtimeevents.Event{
		Kind: runtimeevents.KindAgentAssistantMessageCommitted, Payload: payload,
	})
	if fields["content_len"] != 12 || fields["reasoning_len"] != 16 ||
		fields["phase"] != AssistantMessagePhaseFinal {
		t.Fatalf("runtime event fields = %#v", fields)
	}
}
