// MintClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"testing"

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
		ToolCalls: []providers.ToolCall{{
			ID: "call-protected", Name: "protected_test",
			Arguments: map[string]any{"value": secret},
		}},
	}}}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	agent.Tools.Register(durableProjectionTestTool{})

	pipeline := NewPipeline(al)
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
}
