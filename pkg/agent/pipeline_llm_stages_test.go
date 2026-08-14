// MintClaw - Ultra-lightweight personal AI agent

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type durableProjectionTestTool struct{}

func (durableProjectionTestTool) Name() string        { return "protected_test" }
func (durableProjectionTestTool) Description() string { return "test protected arguments" }
func (durableProjectionTestTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}},
		"required": []string{"value"}, "additionalProperties": false,
	}
}

func (durableProjectionTestTool) Execute(context.Context, map[string]any) *toolshared.ToolResult {
	return &toolshared.ToolResult{ForLLM: "ok"}
}

func (durableProjectionTestTool) DurableArguments(map[string]any) (map[string]any, error) {
	return map[string]any{"value": "*"}, nil
}

func (durableProjectionTestTool) ProtectedDurableArguments(map[string]any) bool { return true }

func TestLLMCallStagesKeepPreparationInvocationAndNormalizationSeparate(t *testing.T) {
	provider := &sequenceProvider{responses: []*providers.LLMResponse{{
		Content:      "stage response",
		FinishReason: "stop",
		Usage: &providers.UsageInfo{
			PromptTokens:     11,
			CompletionTokens: 7,
			TotalTokens:      18,
		},
	}}}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()

	pipeline := NewPipeline(al)
	ts := newTurnState(agent, makeTestProcessOpts("llm-stage-session"), turnEventScope{
		turnID:  "llm-stage-turn",
		context: newTurnContext(nil, nil, nil),
	})
	exec, err := pipeline.SetupTurn(t.Context(), ts)
	if err != nil {
		t.Fatalf("SetupTurn() error = %v", err)
	}
	llm := newLLMIterationState(1)

	prepared, err := pipeline.prepareLLMRequest(t.Context(), ts, exec, llm)
	if err != nil {
		t.Fatalf("prepareLLMRequest() error = %v", err)
	}
	if prepared.disposition == llmStageComplete {
		t.Fatalf("prepareLLMRequest() completed turn: %+v", prepared.outcome)
	}
	if provider.callCount != 0 || llm.response != nil {
		t.Fatalf(
			"preparation invoked provider or populated response: calls=%d response=%+v",
			provider.callCount,
			llm.response,
		)
	}
	if len(llm.callMessages) == 0 || llm.llmModel == "" || llm.llmOpts == nil {
		t.Fatalf("preparation did not populate request state: %+v", llm)
	}

	invoked, err := pipeline.invokeLLMWithRetry(t.Context(), t.Context(), ts, exec, llm)
	if err != nil {
		t.Fatalf("invokeLLMWithRetry() error = %v", err)
	}
	if invoked.disposition == llmStageComplete {
		t.Fatalf("invokeLLMWithRetry() completed turn: %+v", invoked.outcome)
	}
	if provider.callCount != 1 || llm.response == nil || llm.response.Content != "stage response" {
		t.Fatalf("invocation result = calls=%d response=%+v", provider.callCount, llm.response)
	}
	if calls, _, _, _ := ts.llmUsageTotals(); calls != 0 {
		t.Fatalf("invocation recorded usage before normalization: calls=%d", calls)
	}

	outcome, err := pipeline.normalizeAndDispatchLLMResponse(context.Background(), ts, exec, llm)
	if err != nil {
		t.Fatalf("normalizeAndDispatchLLMResponse() error = %v", err)
	}
	if outcome.Control != ControlBreak || outcome.FinalContent != "stage response" {
		t.Fatalf("normalizeAndDispatchLLMResponse() outcome = %+v", outcome)
	}
	if calls, prompt, completion, total := ts.llmUsageTotals(); calls != 1 || prompt != 11 || completion != 7 ||
		total != 18 {
		t.Fatalf(
			"usage totals = calls=%d prompt=%d completion=%d total=%d",
			calls,
			prompt,
			completion,
			total,
		)
	}
}

func TestLLMNormalizationPersistsProjectionButRetainsExecutionArguments(t *testing.T) {
	secret := "ephemeral-browser-fill-canary"
	provider := &sequenceProvider{responses: []*providers.LLMResponse{{
		Content:          "ephemeral-browser-fill-canary",
		ReasoningContent: "reasoning repeats ephemeral-browser-fill-canary",
		ToolCalls: []providers.ToolCall{{
			ID: "call-protected", Name: "protected_test",
			Arguments: map[string]any{"value": secret},
			Function:  &providers.FunctionCall{ThoughtSignature: secret},
			ExtraContent: &providers.ExtraContent{
				Google:                  &providers.GoogleExtra{ThoughtSignature: secret},
				ToolFeedbackExplanation: "explanation repeats " + secret,
			},
		}},
	}}}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	agent.Tools.Register(durableProjectionTestTool{})

	pipeline := NewPipeline(al)
	contextCapture := &trackingContextManager{}
	pipeline.Context.Runtime = contextCapture
	ts := newTurnState(agent, makeTestProcessOpts("projection-session"), turnEventScope{
		turnID: "projection-turn", context: newTurnContext(nil, nil, nil),
	})
	exec, err := pipeline.SetupTurn(t.Context(), ts)
	if err != nil {
		t.Fatal(err)
	}
	llm := newLLMIterationState(1)
	if stage, prepareErr := pipeline.prepareLLMRequest(t.Context(), ts, exec, llm); prepareErr != nil ||
		stage.disposition == llmStageComplete {
		t.Fatalf("prepare = %+v, %v", stage, prepareErr)
	}
	if stage, invokeErr := pipeline.invokeLLMWithRetry(t.Context(), t.Context(), ts, exec, llm); invokeErr != nil ||
		stage.disposition == llmStageComplete {
		t.Fatalf("invoke = %+v, %v", stage, invokeErr)
	}
	outcome, err := pipeline.normalizeAndDispatchLLMResponse(t.Context(), ts, exec, llm)
	if err != nil || outcome.Control != ControlToolLoop {
		t.Fatalf("normalize = %+v, %v", outcome, err)
	}
	if got := llm.normalizedToolCalls[0].Arguments["value"]; got != secret {
		t.Fatalf("execution value = %#v", got)
	}
	call := exec.messages[len(exec.messages)-1].ToolCalls[0]
	if call.Function == nil || call.Function.Arguments != `{"value":"*"}` {
		t.Fatalf("durable tool call = %#v", call)
	}
	message := exec.messages[len(exec.messages)-1]
	if message.Content != "" || message.ReasoningContent != "" || call.ExtraContent != nil ||
		call.Function.ThoughtSignature != "" || call.ThoughtSignature != "" {
		t.Fatalf("protected sibling response fields were retained: %#v", message)
	}
	history, err := json.Marshal(agent.Sessions.GetHistory(ts.sessionKey))
	if err != nil || bytes.Contains(history, []byte(secret)) {
		t.Fatalf("canonical session retained protected value: %s, %v", history, err)
	}
	contextCapture.mu.Lock()
	ingested := contextCapture.lastIngest
	contextCapture.mu.Unlock()
	ingestedJSON, err := json.Marshal(ingested)
	if err != nil || bytes.Contains(ingestedJSON, []byte(secret)) {
		t.Fatalf("context ingest retained protected value: %s, %v", ingestedJSON, err)
	}
}

func TestLLMNormalizationRejectsProtectedMultiCallBatchBeforePersistence(t *testing.T) {
	provider := &sequenceProvider{responses: []*providers.LLMResponse{{
		Content: "must not persist",
		ToolCalls: []providers.ToolCall{
			{ID: "call-protected-one", Name: "protected_test", Arguments: map[string]any{"value": "first"}},
			{ID: "call-protected-two", Name: "protected_test", Arguments: map[string]any{"value": "second"}},
		},
	}}}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	agent.Tools.Register(durableProjectionTestTool{})

	pipeline := NewPipeline(al)
	ts := newTurnState(agent, makeTestProcessOpts("protected-batch-session"), turnEventScope{
		turnID: "protected-batch-turn", context: newTurnContext(nil, nil, nil),
	})
	exec, err := pipeline.SetupTurn(t.Context(), ts)
	if err != nil {
		t.Fatal(err)
	}
	baselineMessages := len(exec.messages)
	llm := newLLMIterationState(1)
	if stage, prepareErr := pipeline.prepareLLMRequest(t.Context(), ts, exec, llm); prepareErr != nil ||
		stage.disposition == llmStageComplete {
		t.Fatalf("prepare = %+v, %v", stage, prepareErr)
	}
	if stage, invokeErr := pipeline.invokeLLMWithRetry(t.Context(), t.Context(), ts, exec, llm); invokeErr != nil ||
		stage.disposition == llmStageComplete {
		t.Fatalf("invoke = %+v, %v", stage, invokeErr)
	}
	if _, err = pipeline.normalizeAndDispatchLLMResponse(t.Context(), ts, exec, llm); err == nil {
		t.Fatal("protected multi-call batch was accepted")
	}
	if len(exec.messages) != baselineMessages || len(agent.Sessions.GetHistory(ts.sessionKey)) != 0 {
		t.Fatalf("protected multi-call batch persisted: messages=%d baseline=%d history=%#v",
			len(exec.messages), baselineMessages, agent.Sessions.GetHistory(ts.sessionKey))
	}
}

func TestLLMNormalizationRejectsConflictingBrowserRepresentationsBeforePersistence(t *testing.T) {
	secret := "conflicting-browser-fill-canary"
	provider := &sequenceProvider{responses: []*providers.LLMResponse{{
		Content: secret,
		ToolCalls: []providers.ToolCall{{
			ID: "call-conflicting-browser", Name: "read_file",
			Arguments: map[string]any{
				"action": map[string]any{"kind": "select", "ref": "ref_1", "value": "CA"},
			},
			Function: &providers.FunctionCall{
				Name:      "browser_act",
				Arguments: `{"action":{"kind":"fill","ref":"ref_1","value":"` + secret + `"}}`,
			},
		}},
	}}}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	diagnosticSub, diagnosticCh, err := al.RuntimeEvents().OfKind(
		runtimeevents.KindAgentLLMResponse,
	).SubscribeChan(t.Context(), runtimeevents.SubscribeOptions{Name: "protected-conflict", Buffer: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := diagnosticSub.Close(); closeErr != nil {
			t.Errorf("close diagnostic subscription: %v", closeErr)
		}
	}()

	pipeline := NewPipeline(al)
	contextCapture := &trackingContextManager{}
	pipeline.Context.Runtime = contextCapture
	ts := newTurnState(agent, makeTestProcessOpts("conflicting-browser-session"), turnEventScope{
		turnID: "conflicting-browser-turn", context: newTurnContext(nil, nil, nil),
	})
	exec, err := pipeline.SetupTurn(t.Context(), ts)
	if err != nil {
		t.Fatal(err)
	}
	baselineMessages := len(exec.messages)
	baselineHistory, err := json.Marshal(agent.Sessions.GetHistory(ts.sessionKey))
	if err != nil {
		t.Fatal(err)
	}
	llm := newLLMIterationState(1)
	if stage, prepareErr := pipeline.prepareLLMRequest(t.Context(), ts, exec, llm); prepareErr != nil ||
		stage.disposition == llmStageComplete {
		t.Fatalf("prepare = %+v, %v", stage, prepareErr)
	}
	if stage, invokeErr := pipeline.invokeLLMWithRetry(t.Context(), t.Context(), ts, exec, llm); invokeErr != nil ||
		stage.disposition == llmStageComplete {
		t.Fatalf("invoke = %+v, %v", stage, invokeErr)
	}
	if _, err = pipeline.normalizeAndDispatchLLMResponse(t.Context(), ts, exec, llm); err == nil {
		t.Fatal("conflicting browser argument representations were accepted")
	}
	select {
	case event := <-diagnosticCh:
		encoded, _ := json.Marshal(event)
		t.Fatalf("conflicting protected call emitted an LLM response diagnostic: %s", encoded)
	default:
	}
	history, marshalErr := json.Marshal(agent.Sessions.GetHistory(ts.sessionKey))
	contextCapture.mu.Lock()
	ingested := contextCapture.lastIngest
	contextCapture.mu.Unlock()
	if marshalErr != nil || len(exec.messages) != baselineMessages || !bytes.Equal(history, baselineHistory) ||
		bytes.Contains(history, []byte(secret)) || ingested != nil {
		t.Fatalf("conflicting browser call persisted: messages=%d baseline=%d history=%s ingest=%#v error=%v",
			len(exec.messages), baselineMessages, history, ingested, marshalErr)
	}
}
