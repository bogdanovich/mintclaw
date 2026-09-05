package agent

import (
	"context"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/providers"
)

type abortingPipelineHook struct {
	action HookAction
}

func (h abortingPipelineHook) BeforeLLM(
	_ context.Context,
	req *LLMHookRequest,
) (*LLMHookRequest, HookDecision) {
	return req, HookDecision{Action: h.action}
}

func (h abortingPipelineHook) AfterLLM(
	_ context.Context,
	resp *LLMHookResponse,
) (*LLMHookResponse, HookDecision) {
	return resp, HookDecision{}
}

func (h abortingPipelineHook) BeforeTool(
	_ context.Context,
	req *ToolCallHookRequest,
) (*ToolCallHookRequest, HookDecision) {
	return req, HookDecision{Action: h.action}
}

func (h abortingPipelineHook) AfterTool(
	_ context.Context,
	resp *ToolResultHookResponse,
) (*ToolResultHookResponse, HookDecision) {
	return resp, HookDecision{}
}

func (abortingPipelineHook) ApproveTool(context.Context, *ToolApprovalRequest) ApprovalDecision {
	return ApprovalDecision{Approved: true}
}

func TestPipelinePhaseOutcomesCarryAbortCause(t *testing.T) {
	tests := []struct {
		name   string
		action HookAction
		want   turnAbortCause
	}{
		{name: "hook abort", action: HookActionAbortTurn, want: turnAbortHook},
		{name: "hard abort", action: HookActionHardAbort, want: turnAbortHard},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
			defer cleanup()

			pipeline := newTestPipeline(al)
			pipeline.Interaction.Hooks = abortingPipelineHook{action: test.action}
			ts := newTurnState(agent, makeTestTurnSpec("outcome-abort"), turnEventScope{
				turnID:  "turn-outcome-abort",
				context: newTurnContext(nil, nil, nil),
			})
			exec, err := pipeline.SetupTurn(t.Context(), ts)
			if err != nil {
				t.Fatalf("SetupTurn() error = %v", err)
			}
			if exec.model.cleanup != nil {
				defer exec.model.cleanup()
			}

			llm := newLLMIterationState(1)
			llmOutcome, err := pipeline.CallLLM(t.Context(), t.Context(), ts, exec, llm)
			if err != nil {
				t.Fatalf("CallLLM() error = %v", err)
			}
			if llmOutcome.Control != turnStepAbort || llmOutcome.AbortCause != test.want {
				t.Fatalf("LLM outcome = %#v, want abort with cause %v", llmOutcome, test.want)
			}

			toolTS := newTurnState(agent, makeTestTurnSpec("outcome-tool-abort"), turnEventScope{
				turnID:  "turn-outcome-tool-abort",
				context: newTurnContext(nil, nil, nil),
			})
			toolExec := newTurnExecution(agent, toolTS.opts, nil, "", nil)
			toolLLM := &LLMIterationState{
				iteration:           1,
				normalizedToolCalls: []providers.ToolCall{{ID: "call-1", Name: "unused"}},
			}
			toolOutcome := pipeline.ExecuteTools(t.Context(), t.Context(), toolTS, toolExec, toolLLM)
			if toolOutcome.Control != turnStepAbort || toolOutcome.AbortCause != test.want {
				t.Fatalf("tool outcome = %#v, want abort with cause %v", toolOutcome, test.want)
			}
		})
	}
}

func TestLLMCallOutcomeTerminalCandidate(t *testing.T) {
	tests := []struct {
		name    string
		outcome LLMCallOutcome
		want    terminalContent
	}{
		{
			name:    "continue retains prior answer",
			outcome: LLMCallOutcome{Control: turnStepContinue},
			want:    terminalContent{content: "retained answer", protected: true},
		},
		{
			name:    "tool loop retains prior answer",
			outcome: LLMCallOutcome{Control: turnStepExecuteTools},
			want:    terminalContent{content: "retained answer", protected: true},
		},
		{
			name: "terminal answer replaces prior answer",
			outcome: LLMCallOutcome{
				Control:      turnStepFinalize,
				FinalContent: "replacement answer",
			},
			want: terminalContent{content: "replacement answer"},
		},
		{
			name:    "empty terminal answer clears prior answer",
			outcome: LLMCallOutcome{Control: turnStepFinalize},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			retained := terminalContent{content: "retained answer", protected: true}
			if got := test.outcome.terminalCandidate(retained); got != test.want {
				t.Fatalf("terminal candidate = %#v, want %#v", got, test.want)
			}
		})
	}
}
