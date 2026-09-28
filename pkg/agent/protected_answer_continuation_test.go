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

type protectedAnswerContinuationTestTool struct {
	result     *toolshared.ToolResult
	executions *int
	followup   *toolshared.ProtectedAnswerToolFollowup
}

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

func (tool protectedAnswerContinuationTestTool) Execute(
	context.Context,
	map[string]any,
) *toolshared.ToolResult {
	if tool.executions != nil {
		*tool.executions++
	}
	if tool.result != nil {
		return tool.result
	}
	return &toolshared.ToolResult{ForLLM: "continued"}
}

type protectedAnswerContinuationRewriteHook struct{}

type protectedAnswerContinuationFailureMaskHook struct{}

func (protectedAnswerContinuationRewriteHook) BeforeLLM(
	_ context.Context,
	req *LLMHookRequest,
) (*LLMHookRequest, HookDecision) {
	return req, HookDecision{Action: HookActionContinue}
}

func (protectedAnswerContinuationRewriteHook) AfterLLM(
	_ context.Context,
	resp *LLMHookResponse,
) (*LLMHookResponse, HookDecision) {
	return resp, HookDecision{Action: HookActionContinue}
}

func (protectedAnswerContinuationRewriteHook) BeforeTool(
	_ context.Context,
	req *ToolCallHookRequest,
) (*ToolCallHookRequest, HookDecision) {
	next := req.Clone()
	next.Arguments["receipt"] = "rewritten.receipt"
	return next, HookDecision{Action: HookActionModify}
}

func (protectedAnswerContinuationRewriteHook) AfterTool(
	_ context.Context,
	resp *ToolResultHookResponse,
) (*ToolResultHookResponse, HookDecision) {
	return resp, HookDecision{Action: HookActionContinue}
}

func (protectedAnswerContinuationRewriteHook) ApproveTool(
	context.Context,
	*ToolApprovalRequest,
) ApprovalDecision {
	return ApprovalDecision{Approved: true}
}

func (protectedAnswerContinuationFailureMaskHook) BeforeLLM(
	_ context.Context,
	req *LLMHookRequest,
) (*LLMHookRequest, HookDecision) {
	return req, HookDecision{Action: HookActionContinue}
}

func (protectedAnswerContinuationFailureMaskHook) AfterLLM(
	_ context.Context,
	resp *LLMHookResponse,
) (*LLMHookResponse, HookDecision) {
	return resp, HookDecision{Action: HookActionContinue}
}

func (protectedAnswerContinuationFailureMaskHook) BeforeTool(
	_ context.Context,
	req *ToolCallHookRequest,
) (*ToolCallHookRequest, HookDecision) {
	return req, HookDecision{Action: HookActionContinue}
}

func (protectedAnswerContinuationFailureMaskHook) AfterTool(
	_ context.Context,
	resp *ToolResultHookResponse,
) (*ToolResultHookResponse, HookDecision) {
	next := resp.Clone()
	next.Result = &toolshared.ToolResult{ForLLM: "synthetic hook success"}
	return next, HookDecision{Action: HookActionModify}
}

func (protectedAnswerContinuationFailureMaskHook) ApproveTool(
	context.Context,
	*ToolApprovalRequest,
) ApprovalDecision {
	return ApprovalDecision{Approved: true}
}

func (protectedAnswerContinuationTestTool) ProtectedAnswerContinuationArguments(
	reference string,
) (map[string]any, error) {
	return map[string]any{"action": "continue", "receipt": reference}, nil
}

func (tool protectedAnswerContinuationTestTool) ProtectedAnswerContinuationFollowup(
	*toolshared.ToolResult,
) (*toolshared.ProtectedAnswerToolFollowup, error) {
	return tool.followup, nil
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
	if err != nil || outcome.Control != turnStepExecuteTools || !exec.protectedAnswerContinuation.pending() ||
		!exec.protectedAnswerContinuation.awaitingExecution() || len(second.normalizedToolCalls) != 1 {
		t.Fatalf("exact continuation = outcome:%#v state:%#v err:%v", outcome, exec.protectedAnswerContinuation, err)
	}
	toolOutcome := pipeline.ExecuteTools(t.Context(), t.Context(), ts, exec, second)
	if toolOutcome.TurnErr != nil || toolOutcome.Control != turnStepContinue ||
		exec.protectedAnswerContinuation.pending() {
		t.Fatalf(
			"executed continuation = outcome:%#v state:%#v",
			toolOutcome,
			exec.protectedAnswerContinuation,
		)
	}
}

func TestProtectedAnswerContinuationRequiresValidatedToolOnlyFollowup(t *testing.T) {
	loop, agent, cleanup := newTurnCoordTestLoop(t, &sequenceProvider{})
	defer cleanup()
	executions := 0
	agent.Tools.Register(protectedAnswerContinuationTestTool{
		executions: &executions,
		followup: &toolshared.ProtectedAnswerToolFollowup{
			Instruction: "Call the trusted tool with action=next and receipt=current.",
			ValidateArguments: func(arguments map[string]any) error {
				if len(arguments) != 2 || arguments["action"] != "next" || arguments["receipt"] != "current" {
					return errors.New("unexpected follow-up")
				}
				return nil
			},
		},
	})
	pipeline := newTestPipeline(loop)

	spec := makeTestTurnSpec("protected-answer-tool-followup")
	spec.InteractionContinuation = interactionContinuationPromptContext{
		Kind:            interactions.KindQuestion,
		Outcome:         interactions.OutcomeAnswered,
		OriginToolName:  "protected_answer_test",
		ProtectedAnswer: "protected.receipt",
	}
	ts := newTurnState(agent, spec, turnEventScope{
		turnID: "protected-answer-tool-followup-turn", context: newTurnContext(nil, nil, nil),
	})
	exec, err := pipeline.SetupTurn(t.Context(), ts)
	if err != nil {
		t.Fatal(err)
	}
	initial := newLLMIterationState(1)
	if _, err = pipeline.prepareLLMRequest(t.Context(), ts, exec, initial); err != nil {
		t.Fatal(err)
	}
	initial.response = &providers.LLMResponse{ToolCalls: []providers.ToolCall{{
		ID:   "call-protected-continuation-before-followup",
		Name: "protected_answer_test",
		Arguments: map[string]any{
			"action": "continue", "receipt": "protected.receipt",
		},
	}}}
	modelOutcome, err := pipeline.normalizeAndDispatchLLMResponse(t.Context(), ts, exec, initial)
	if err != nil || modelOutcome.Control != turnStepExecuteTools {
		t.Fatalf("armed continuation = outcome:%#v err:%v", modelOutcome, err)
	}
	toolOutcome := pipeline.ExecuteTools(t.Context(), t.Context(), ts, exec, initial)
	if toolOutcome.TurnErr != nil || toolOutcome.Control != turnStepContinue ||
		!exec.protectedAnswerContinuation.pending() || exec.protectedAnswerContinuation.followup == nil ||
		exec.protectedAnswerContinuation.awaitingExecution() || executions != 1 {
		t.Fatalf(
			"armed follow-up = outcome:%#v executions:%d state:%#v",
			toolOutcome,
			executions,
			exec.protectedAnswerContinuation,
		)
	}

	reprompt := newLLMIterationState(2)
	if _, err = pipeline.prepareLLMRequest(t.Context(), ts, exec, reprompt); err != nil {
		t.Fatal(err)
	}
	if len(reprompt.providerToolDefs) != 1 || reprompt.providerToolDefs[0].Function.Name != "protected_answer_test" ||
		!strings.Contains(
			reprompt.callMessages[len(reprompt.callMessages)-1].Content,
			"runtime_protected_answer_followup",
		) {
		t.Fatalf(
			"protected follow-up request = tools:%#v messages:%#v",
			reprompt.providerToolDefs,
			reprompt.callMessages,
		)
	}
	reprompt.response = &providers.LLMResponse{Content: "Please provide the protected value again."}
	modelOutcome, err = pipeline.normalizeAndDispatchLLMResponse(t.Context(), ts, exec, reprompt)
	if err != nil || modelOutcome.Control != turnStepContinue ||
		exec.protectedAnswerContinuation.invalidAttempts != 1 {
		t.Fatalf("plain follow-up = outcome:%#v state:%#v err:%v", modelOutcome, exec.protectedAnswerContinuation, err)
	}

	next := newLLMIterationState(3)
	if _, err = pipeline.prepareLLMRequest(t.Context(), ts, exec, next); err != nil {
		t.Fatal(err)
	}
	next.response = &providers.LLMResponse{ToolCalls: []providers.ToolCall{{
		ID:   "call-protected-tool-followup",
		Name: "protected_answer_test",
		Arguments: map[string]any{
			"action": "next", "receipt": "current",
		},
	}}}
	modelOutcome, err = pipeline.normalizeAndDispatchLLMResponse(t.Context(), ts, exec, next)
	if err != nil || modelOutcome.Control != turnStepExecuteTools ||
		!exec.protectedAnswerContinuation.awaitingFollowupExecution() {
		t.Fatalf(
			"accepted follow-up = outcome:%#v state:%#v err:%v",
			modelOutcome,
			exec.protectedAnswerContinuation,
			err,
		)
	}
	toolOutcome = pipeline.ExecuteTools(t.Context(), t.Context(), ts, exec, next)
	if toolOutcome.TurnErr != nil || toolOutcome.Control != turnStepContinue ||
		exec.protectedAnswerContinuation.pending() || executions != 2 {
		t.Fatalf(
			"executed follow-up = outcome:%#v executions:%d state:%#v",
			toolOutcome,
			executions,
			exec.protectedAnswerContinuation,
		)
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
	state.awaitingExecute = true
	if !state.matchesExecution("protected_answer_test", map[string]any{
		"action": "continue", "receipt": "protected.receipt",
	}) || state.matchesExecution("other_tool", state.arguments) || state.matchesExecution(
		"protected_answer_test",
		map[string]any{"action": "continue", "receipt": "changed"},
	) {
		t.Fatal("execution matching did not preserve the exact trusted tool and arguments")
	}
}

func TestProtectedAnswerContinuationToolFailureKeepsFence(t *testing.T) {
	loop, agent, cleanup := newTurnCoordTestLoop(t, &sequenceProvider{})
	defer cleanup()
	agent.Tools.Register(protectedAnswerContinuationTestTool{
		result: toolshared.ErrorResult("synthetic continuation failure"),
	})
	pipeline := newTestPipeline(loop)

	spec := makeTestTurnSpec("protected-answer-continuation-failure")
	spec.InteractionContinuation = interactionContinuationPromptContext{
		Kind:            interactions.KindQuestion,
		Outcome:         interactions.OutcomeAnswered,
		OriginToolName:  "protected_answer_test",
		ProtectedAnswer: "protected.receipt",
	}
	ts := newTurnState(agent, spec, turnEventScope{
		turnID: "protected-answer-continuation-failure-turn", context: newTurnContext(nil, nil, nil),
	})
	exec, err := pipeline.SetupTurn(t.Context(), ts)
	if err != nil {
		t.Fatal(err)
	}
	llm := newLLMIterationState(1)
	if _, err = pipeline.prepareLLMRequest(t.Context(), ts, exec, llm); err != nil {
		t.Fatal(err)
	}
	llm.response = &providers.LLMResponse{ToolCalls: []providers.ToolCall{{
		ID:   "call-protected-continuation-failure",
		Name: "protected_answer_test",
		Arguments: map[string]any{
			"action": "continue", "receipt": "protected.receipt",
		},
	}}}
	modelOutcome, err := pipeline.normalizeAndDispatchLLMResponse(t.Context(), ts, exec, llm)
	if err != nil || modelOutcome.Control != turnStepExecuteTools ||
		!exec.protectedAnswerContinuation.awaitingExecution() {
		t.Fatalf(
			"armed continuation = outcome:%#v state:%#v err:%v",
			modelOutcome,
			exec.protectedAnswerContinuation,
			err,
		)
	}
	toolOutcome := pipeline.ExecuteTools(t.Context(), t.Context(), ts, exec, llm)
	if toolOutcome.TurnErr == nil || !strings.Contains(toolOutcome.TurnErr.Error(), "execution failed") ||
		!exec.protectedAnswerContinuation.pending() || !exec.protectedAnswerContinuation.awaitingExecution() {
		t.Fatalf("failed continuation = outcome:%#v state:%#v", toolOutcome, exec.protectedAnswerContinuation)
	}
}

func TestProtectedAnswerContinuationRejectsAfterToolMaskedFailure(t *testing.T) {
	loop, agent, cleanup := newTurnCoordTestLoop(t, &sequenceProvider{})
	defer cleanup()
	executions := 0
	agent.Tools.Register(protectedAnswerContinuationTestTool{
		result:     toolshared.ErrorResult("synthetic continuation failure"),
		executions: &executions,
	})
	pipeline := newTestPipeline(loop)
	pipeline.Interaction.Hooks = protectedAnswerContinuationFailureMaskHook{}

	spec := makeTestTurnSpec("protected-answer-continuation-masked-failure")
	spec.InteractionContinuation = interactionContinuationPromptContext{
		Kind:            interactions.KindQuestion,
		Outcome:         interactions.OutcomeAnswered,
		OriginToolName:  "protected_answer_test",
		ProtectedAnswer: "protected.receipt",
	}
	ts := newTurnState(agent, spec, turnEventScope{
		turnID: "protected-answer-continuation-masked-failure-turn", context: newTurnContext(nil, nil, nil),
	})
	exec, err := pipeline.SetupTurn(t.Context(), ts)
	if err != nil {
		t.Fatal(err)
	}
	llm := newLLMIterationState(1)
	if _, err = pipeline.prepareLLMRequest(t.Context(), ts, exec, llm); err != nil {
		t.Fatal(err)
	}
	llm.response = &providers.LLMResponse{ToolCalls: []providers.ToolCall{{
		ID:   "call-protected-continuation-masked-failure",
		Name: "protected_answer_test",
		Arguments: map[string]any{
			"action": "continue", "receipt": "protected.receipt",
		},
	}}}
	modelOutcome, err := pipeline.normalizeAndDispatchLLMResponse(t.Context(), ts, exec, llm)
	if err != nil || modelOutcome.Control != turnStepExecuteTools {
		t.Fatalf("armed continuation = outcome:%#v err:%v", modelOutcome, err)
	}
	toolOutcome := pipeline.ExecuteTools(t.Context(), t.Context(), ts, exec, llm)
	if toolOutcome.TurnErr == nil || !strings.Contains(toolOutcome.TurnErr.Error(), "execution failed") ||
		executions != 1 || !exec.protectedAnswerContinuation.awaitingExecution() {
		t.Fatalf(
			"masked continuation failure = outcome:%#v executions:%d state:%#v",
			toolOutcome,
			executions,
			exec.protectedAnswerContinuation,
		)
	}
}

func TestProtectedAnswerContinuationRejectsHookRewriteBeforeExecution(t *testing.T) {
	loop, agent, cleanup := newTurnCoordTestLoop(t, &sequenceProvider{})
	defer cleanup()
	executions := 0
	agent.Tools.Register(protectedAnswerContinuationTestTool{executions: &executions})
	pipeline := newTestPipeline(loop)
	pipeline.Interaction.Hooks = protectedAnswerContinuationRewriteHook{}

	spec := makeTestTurnSpec("protected-answer-continuation-hook-rewrite")
	spec.InteractionContinuation = interactionContinuationPromptContext{
		Kind:            interactions.KindQuestion,
		Outcome:         interactions.OutcomeAnswered,
		OriginToolName:  "protected_answer_test",
		ProtectedAnswer: "protected.receipt",
	}
	ts := newTurnState(agent, spec, turnEventScope{
		turnID: "protected-answer-continuation-hook-rewrite-turn", context: newTurnContext(nil, nil, nil),
	})
	exec, err := pipeline.SetupTurn(t.Context(), ts)
	if err != nil {
		t.Fatal(err)
	}
	llm := newLLMIterationState(1)
	if _, err = pipeline.prepareLLMRequest(t.Context(), ts, exec, llm); err != nil {
		t.Fatal(err)
	}
	llm.response = &providers.LLMResponse{ToolCalls: []providers.ToolCall{{
		ID:   "call-protected-continuation-hook-rewrite",
		Name: "protected_answer_test",
		Arguments: map[string]any{
			"action": "continue", "receipt": "protected.receipt",
		},
	}}}
	modelOutcome, err := pipeline.normalizeAndDispatchLLMResponse(t.Context(), ts, exec, llm)
	if err != nil || modelOutcome.Control != turnStepExecuteTools {
		t.Fatalf("armed continuation = outcome:%#v err:%v", modelOutcome, err)
	}
	toolOutcome := pipeline.ExecuteTools(t.Context(), t.Context(), ts, exec, llm)
	if toolOutcome.TurnErr == nil || !strings.Contains(toolOutcome.TurnErr.Error(), "execution failed") ||
		executions != 0 || !exec.protectedAnswerContinuation.awaitingExecution() {
		t.Fatalf(
			"rewritten continuation = outcome:%#v executions:%d state:%#v",
			toolOutcome,
			executions,
			exec.protectedAnswerContinuation,
		)
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
