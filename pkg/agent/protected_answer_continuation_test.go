package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type protectedAnswerContinuationTestTool struct{}

func (protectedAnswerContinuationTestTool) Name() string { return "protected_answer_test" }

func (protectedAnswerContinuationTestTool) Description() string {
	return "Consumes a protected test receipt"
}

func (protectedAnswerContinuationTestTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action":  map[string]any{"type": "string"},
			"receipt": map[string]any{"type": "string"},
		},
		"required":             []string{"action", "receipt"},
		"additionalProperties": false,
	}
}

func (protectedAnswerContinuationTestTool) Execute(
	context.Context,
	map[string]any,
) *toolshared.ToolResult {
	return &toolshared.ToolResult{ForLLM: "continued"}
}

func (protectedAnswerContinuationTestTool) ProtectedAnswerContinuationArguments(
	reference string,
) (map[string]any, error) {
	return map[string]any{"action": "continue", "receipt": reference}, nil
}

type invalidProtectedAnswerContinuationTestTool struct {
	empty bool
}

type unsupportedProtectedAnswerContinuationTestTool struct{}

func (unsupportedProtectedAnswerContinuationTestTool) Name() string {
	return "unsupported_protected_answer_test"
}

func (unsupportedProtectedAnswerContinuationTestTool) Description() string {
	return "Does not consume protected test receipts"
}

func (unsupportedProtectedAnswerContinuationTestTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (unsupportedProtectedAnswerContinuationTestTool) Execute(
	context.Context,
	map[string]any,
) *toolshared.ToolResult {
	return &toolshared.ToolResult{ForLLM: "unexpected"}
}

func (invalidProtectedAnswerContinuationTestTool) Name() string {
	return "invalid_protected_answer_test"
}

func (invalidProtectedAnswerContinuationTestTool) Description() string {
	return "Rejects a protected test receipt"
}

func (invalidProtectedAnswerContinuationTestTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (invalidProtectedAnswerContinuationTestTool) Execute(
	context.Context,
	map[string]any,
) *toolshared.ToolResult {
	return &toolshared.ToolResult{ForLLM: "unexpected"}
}

func (tool invalidProtectedAnswerContinuationTestTool) ProtectedAnswerContinuationArguments(
	string,
) (map[string]any, error) {
	if tool.empty {
		return nil, nil
	}
	return nil, errors.New("synthetic private setup detail")
}

func TestProtectedAnswerContinuationRequiresExactOriginatingToolCall(t *testing.T) {
	loop, agent, cleanup := newTurnCoordTestLoop(t, &sequenceProvider{})
	defer cleanup()
	agent.Tools.Register(protectedAnswerContinuationTestTool{})
	pipeline := newTestPipeline(loop)

	spec := makeTestTurnSpec("protected-answer-continuation")
	spec.InteractionContinuation = interactionContinuationPromptContext{
		Kind:            interactions.KindQuestion,
		Outcome:         interactions.OutcomeAnswered,
		OriginToolName:  "protected_answer_test",
		ProtectedAnswer: "protected.receipt",
	}
	ts := newTurnState(agent, spec, turnEventScope{
		turnID: "protected-answer-continuation-turn", context: newTurnContext(nil, nil, nil),
	})
	exec, err := pipeline.SetupTurn(t.Context(), ts)
	if err != nil {
		t.Fatal(err)
	}
	if !exec.protectedAnswerContinuation.pending() {
		t.Fatal("protected answer continuation is not pending")
	}

	first := newLLMIterationState(1)
	if _, err = pipeline.prepareLLMRequest(t.Context(), ts, exec, first); err != nil {
		t.Fatal(err)
	}
	if len(first.providerToolDefs) != 1 ||
		first.providerToolDefs[0].Function.Name != "protected_answer_test" {
		t.Fatalf("restricted tools = %#v", first.providerToolDefs)
	}
	if got := first.callMessages[len(first.callMessages)-1].Content; !strings.Contains(
		got,
		`{"action":"continue","receipt":"protected.receipt"}`,
	) {
		t.Fatalf("continuation instruction = %q", got)
	}
	first.response = &providers.LLMResponse{Content: "Please provide the value again."}
	outcome, err := pipeline.normalizeAndDispatchLLMResponse(t.Context(), ts, exec, first)
	if err != nil || outcome.Control != turnStepContinue || len(exec.actionLog) != 0 ||
		exec.protectedAnswerContinuation.invalidAttempts != 1 {
		t.Fatalf("invalid continuation = outcome:%#v state:%#v err:%v", outcome, exec.protectedAnswerContinuation, err)
	}

	second := newLLMIterationState(2)
	if _, err = pipeline.prepareLLMRequest(t.Context(), ts, exec, second); err != nil {
		t.Fatal(err)
	}
	if got := second.callMessages[len(second.callMessages)-1].Content; !strings.Contains(got, "correct it now") {
		t.Fatalf("retry instruction = %q", got)
	}
	second.response = &providers.LLMResponse{ToolCalls: []providers.ToolCall{{
		ID:   "call-protected-continuation",
		Name: "protected_answer_test",
		Arguments: map[string]any{
			"action": "continue", "receipt": "protected.receipt",
		},
	}}}
	outcome, err = pipeline.normalizeAndDispatchLLMResponse(t.Context(), ts, exec, second)
	if err != nil || outcome.Control != turnStepExecuteTools || exec.protectedAnswerContinuation.pending() ||
		len(second.normalizedToolCalls) != 1 {
		t.Fatalf("exact continuation = outcome:%#v state:%#v err:%v", outcome, exec.protectedAnswerContinuation, err)
	}
}

func TestProtectedAnswerContinuationRejectsMutatedArguments(t *testing.T) {
	state := protectedAnswerContinuationState{
		enabled:  true,
		toolName: "protected_answer_test",
		arguments: map[string]any{
			"action": "continue", "receipt": "protected.receipt",
		},
	}
	for _, response := range []*providers.LLMResponse{
		{
			Content: "Please provide the value again.",
			ToolCalls: []providers.ToolCall{{
				Name: "protected_answer_test",
				Arguments: map[string]any{
					"action": "continue", "receipt": "protected.receipt",
				},
			}},
		},
		{ToolCalls: []providers.ToolCall{{
			Name: "other_tool", Arguments: map[string]any{"action": "continue", "receipt": "protected.receipt"},
		}}},
		{ToolCalls: []providers.ToolCall{{
			Name: "protected_answer_test", Arguments: map[string]any{"action": "continue", "receipt": "changed"},
		}}},
		{ToolCalls: []providers.ToolCall{{
			Name: "protected_answer_test",
			Arguments: map[string]any{
				"action": "continue", "receipt": "protected.receipt", "extra": true,
			},
		}}},
	} {
		if state.accept(response) {
			t.Fatalf("accepted mutated response: %#v", response)
		}
	}
}

func TestProtectedAnswerContinuationSetupFailuresAbortTurnAdmission(t *testing.T) {
	for _, test := range []struct {
		name       string
		originTool string
		register   toolshared.Tool
	}{
		{name: "missing origin"},
		{name: "missing tool", originTool: "missing_protected_answer_test"},
		{
			name:       "unsupported tool",
			originTool: "unsupported_protected_answer_test",
			register:   unsupportedProtectedAnswerContinuationTestTool{},
		},
		{
			name:       "provider error",
			originTool: "invalid_protected_answer_test",
			register:   invalidProtectedAnswerContinuationTestTool{},
		},
		{
			name:       "empty arguments",
			originTool: "invalid_protected_answer_test",
			register:   invalidProtectedAnswerContinuationTestTool{empty: true},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			loop, agent, cleanup := newTurnCoordTestLoop(t, &sequenceProvider{})
			defer cleanup()
			if test.register != nil {
				agent.Tools.Register(test.register)
			}
			pipeline := newTestPipeline(loop)
			spec := makeTestTurnSpec("protected-answer-setup-failure")
			spec.InteractionContinuation = interactionContinuationPromptContext{
				Kind:            interactions.KindQuestion,
				Outcome:         interactions.OutcomeAnswered,
				OriginToolName:  test.originTool,
				ProtectedAnswer: "protected.receipt",
			}
			ts := newTurnState(agent, spec, turnEventScope{
				turnID: "protected-answer-setup-failure-turn", context: newTurnContext(nil, nil, nil),
			})
			if _, err := pipeline.SetupTurn(t.Context(), ts); err == nil ||
				!strings.Contains(err.Error(), "initialize protected answer continuation") {
				t.Fatalf("SetupTurn() error = %v", err)
			}
		})
	}
}
