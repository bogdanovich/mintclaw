package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func documentLiveUserInputTestTurn(
	t *testing.T,
	provider *sequenceProvider,
	input string,
) (*Pipeline, *turnState, *turnExecution) {
	t.Helper()
	loop, agent, cleanup := newTurnCoordTestLoop(t, provider)
	t.Cleanup(cleanup)
	agent.Tools.Register(semanticDialogueTestTool{
		DocumentTool: tools.NewDocumentTool(),
		results: map[string]*toolshared.ToolResult{"route": {
			ForLLM: `{"schema_version":"document.form_task_routing.v1","operation":"form",` +
				`"form_action":"route","state":"granted","handoff_fresh":true}`,
			ContextText:  "The background draft is preserved. Current input:\n" + input,
			ContextInput: &toolshared.ToolContextInput{Text: input},
		}},
	})
	pipeline := newTestPipeline(loop)
	opts := makeTestTurnSpec("live-native-user-input")
	const receipt = "Protected form input is ready. Continue through its opaque receipt."
	opts.Dispatch = testDispatchRequest("live-native-user-input", "cli", "test-chat", receipt)
	ts := newTurnState(agent, opts, turnEventScope{
		turnID: "live-native-user-input", context: newTurnContext(nil, nil, nil),
	})
	exec, err := pipeline.SetupTurn(t.Context(), ts)
	if err != nil {
		t.Fatal(err)
	}
	llm := newLLMIterationState(1)
	llm.response = &providers.LLMResponse{ToolCalls: []providers.ToolCall{
		{
			ID:        "grant",
			Name:      "semantic_document_test",
			Arguments: map[string]any{"action": "form", "form_action": "route"},
		},
	}}
	outcome, err := pipeline.normalizeAndDispatchLLMResponse(t.Context(), ts, exec, llm)
	if err != nil || outcome.Control != turnStepExecuteTools {
		t.Fatal("grant did not reach its tool")
	}
	if executed := pipeline.ExecuteTools(t.Context(), t.Context(), ts, exec, llm); executed.TurnErr != nil {
		t.Fatal(executed.TurnErr)
	}
	return pipeline, ts, exec
}

func TestDocumentGrantPresentsCurrentInputAsLiveUserWithoutChangingCanonicalReceipt(t *testing.T) {
	const input = "LIVE_AUTHORIZED_FOLLOW_UP_71: Why?"
	pipeline, ts, exec := documentLiveUserInputTestTurn(t, &sequenceProvider{}, input)
	next := newLLMIterationState(2)
	if _, err := pipeline.prepareLLMRequest(t.Context(), ts, exec, next); err != nil {
		t.Fatal(err)
	}
	last := next.callMessages[len(next.callMessages)-1]
	if last.Role != "user" || last.Content != input {
		t.Fatal("provider did not receive the freshly authorized current user input")
	}
	for _, messages := range [][]providers.Message{ts.persistedMessagesSnapshot(), exec.messages, next.diagnosticMessagesForAttempt()} {
		encoded, encodeErr := json.Marshal(messages)
		if encodeErr != nil || strings.Contains(string(encoded), input) {
			t.Fatal("live user input escaped into canonical or diagnostic context")
		}
	}
	if ts.userMessage != "Protected form input is ready. Continue through its opaque receipt." ||
		!next.protectedDiagnosticContext {
		t.Fatal("native root or diagnostic protection changed")
	}
}

func TestDocumentGrantOverflowBudgetsLiveUserInputWithoutDroppingIt(t *testing.T) {
	input := strings.Repeat("authorized input ", 300)
	provider := &sequenceProvider{errors: []error{errors.New("context_window_exceeded")}}
	pipeline, ts, exec := documentLiveUserInputTestTurn(t, provider, input)
	history := []providers.Message{
		{Role: "user", Content: strings.Repeat("old history ", 600)},
		{Role: "assistant", Content: "old response"},
	}
	tracker := &trackingContextManager{assembleResponse: &AssembleResponse{History: history}}
	pipeline.Context.Runtime = tracker
	protected := ts.liveTurnMessagesSnapshot()
	full := pipeline.buildTurnMessagesWithProtectedTurnBoundary(
		ts, append(append([]providers.Message(nil), history...), protected...), nil, "", nil, nil, len(protected),
	)
	inputTokens := EstimateMessageTokens(providers.Message{Role: "user", Content: input})
	tools := filterToolsByTurnProfile(ts.agent.Tools.ToProviderDefs(), ts.profile)
	ts.agent.ContextWindow = ts.agent.MaxTokens + EstimateToolDefsTokens(tools) + inputTokens/2
	for _, message := range full {
		ts.agent.ContextWindow += EstimateMessageTokens(message)
	}
	baseReserve := pipeline.estimateNonHistoryPromptReserve(ts, nil, tools, pipeline.maxMediaSize())
	llm := newLLMIterationState(2)
	if _, err := pipeline.CallLLM(t.Context(), t.Context(), ts, exec, llm); err != nil {
		t.Fatal(err)
	}
	if provider.callCount != 2 || tracker.lastAssemble == nil || tracker.lastCompact == nil {
		t.Fatal("fixture did not exercise provider overflow and context rebuild")
	}
	if tracker.lastAssemble.ReserveTokens != baseReserve+inputTokens ||
		tracker.lastCompact.Budget != effectiveHistoryBudget(
			ts.agent.ContextWindow, ts.agent.MaxTokens, baseReserve+inputTokens,
		) {
		t.Fatal("overflow assembly/compaction did not reserve authorized live user input")
	}
	retry := provider.requests[1]
	if isOverContextBudget(ts.agent.ContextWindow, retry, provider.toolRequests[1], ts.agent.MaxTokens) ||
		messageContentPresent(retry, history[0].Content) {
		t.Fatal("retry kept historical context that fits only without its live user input")
	}
	count := 0
	for _, message := range retry {
		if message.Role == "user" && message.Content == input {
			count++
		}
	}
	if count != 1 || provider.options[1]["max_tokens"] != ts.agent.MaxTokens {
		t.Fatal("retry dropped/duplicated native input or changed the provider output limit")
	}
}
