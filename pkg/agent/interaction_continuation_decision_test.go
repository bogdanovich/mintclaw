package agent

import (
	"context"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
)

func TestInteractionContinuationDecisionRequiresAnsweredLiveHandoff(t *testing.T) {
	tests := []struct {
		name string
		spec turnSpec
		want bool
	}{
		{
			name: "answered live handoff receipt",
			spec: turnSpec{
				InteractionContinuation: interactionContinuationPromptContext{
					Kind: interactions.KindQuestion, Outcome: interactions.OutcomeAnswered,
				},
				InitialReceipts: []taskresult.Receipt{{Kind: taskresult.ObjectiveKindLiveHandoff}},
			},
			want: true,
		},
		{
			name: "answered live handoff objective",
			spec: turnSpec{
				InteractionContinuation: interactionContinuationPromptContext{
					Kind: interactions.KindQuestion, Outcome: interactions.OutcomeAnswered,
				},
				ObjectiveChecklist: []runtimeObjectiveItem{{Kind: taskresult.ObjectiveKindLiveHandoff}},
			},
			want: true,
		},
		{
			name: "ordinary answered question",
			spec: turnSpec{
				InteractionContinuation: interactionContinuationPromptContext{
					Kind: interactions.KindQuestion, Outcome: interactions.OutcomeAnswered,
				},
			},
		},
		{
			name: "live handoff approval",
			spec: turnSpec{
				InteractionContinuation: interactionContinuationPromptContext{
					Kind: interactions.KindApproval, Outcome: interactions.OutcomeAllowed,
				},
				InitialReceipts: []taskresult.Receipt{{Kind: taskresult.ObjectiveKindLiveHandoff}},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := newInteractionContinuationDecisionState(freezeTurnInput(test.spec))
			if state.pending() != test.want {
				t.Fatalf("decision pending = %t, want %t", state.pending(), test.want)
			}
		})
	}
}

func TestInteractionContinuationDecisionDefersToNewerGuidance(t *testing.T) {
	loop, agent, cleanup := newTurnCoordTestLoop(t, &sequenceProvider{})
	defer cleanup()
	pipeline := newTestPipeline(loop)
	pipeline.Context.Steering = &oneShotLoopGuardSteering{messages: []providers.Message{{
		Role: "user", Content: "Actually, finish and release the live resource.",
	}}}
	spec := makeTestTurnSpec("continuation-decision-steering")
	spec.InteractionContinuation = interactionContinuationPromptContext{
		Kind: interactions.KindQuestion, Outcome: interactions.OutcomeAnswered,
	}
	spec.InitialReceipts = []taskresult.Receipt{{Kind: taskresult.ObjectiveKindLiveHandoff}}
	ts := newTurnState(agent, spec, turnEventScope{
		turnID: "continuation-decision-steering-turn", context: newTurnContext(nil, nil, nil),
	})
	exec, err := pipeline.SetupTurn(t.Context(), ts)
	if err != nil {
		t.Fatal(err)
	}
	llm := newLLMIterationState(1)
	if _, err = pipeline.prepareLLMRequest(t.Context(), ts, exec, llm); err != nil {
		t.Fatal(err)
	}
	llm.response = interactionContinuationDecisionResponse("continue", "")
	outcome, err := pipeline.normalizeAndDispatchLLMResponse(context.Background(), ts, exec, llm)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Control != turnStepContinue || !exec.continuationDecision.pending() ||
		exec.pendingInputs.Len() != 1 || exec.continuationDecision.invalidAttempts != 0 ||
		exec.continuationDecision.modelCalls != 1 {
		t.Fatalf(
			"superseded decision = outcome:%#v state:%#v pending:%d",
			outcome,
			exec.continuationDecision,
			exec.pendingInputs.Len(),
		)
	}
}

func TestParseInteractionContinuationDecision(t *testing.T) {
	tests := []struct {
		name       string
		response   *providers.LLMResponse
		wantAction string
		wantFinal  string
		wantValid  bool
	}{
		{
			name:       "continue",
			response:   interactionContinuationDecisionResponse("continue", ""),
			wantAction: "continue", wantValid: true,
		},
		{
			name:       "redirect",
			response:   interactionContinuationDecisionResponse("redirect", ""),
			wantAction: "redirect", wantValid: true,
		},
		{
			name:       "finish with localized response",
			response:   interactionContinuationDecisionResponse("finish", "Задача завершена."),
			wantAction: "finish", wantFinal: "Задача завершена.", wantValid: true,
		},
		{
			name: "external tool is not a decision",
			response: &providers.LLMResponse{ToolCalls: []providers.ToolCall{{
				ID: "call-browser", Name: "browser_session", Arguments: map[string]any{"operation": "resume"},
			}}},
		},
		{
			name: "multiple decisions are ambiguous",
			response: &providers.LLMResponse{ToolCalls: []providers.ToolCall{
				{
					ID: "call-one", Name: interactionContinuationDecisionTool,
					Arguments: map[string]any{"action": "continue"},
				},
				{
					ID: "call-two", Name: interactionContinuationDecisionTool,
					Arguments: map[string]any{"action": "finish"},
				},
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			action, finalResponse, valid := parseInteractionContinuationDecision(test.response)
			if action != test.wantAction || finalResponse != test.wantFinal || valid != test.wantValid {
				t.Fatalf(
					"parse decision = (%q, %q, %t), want (%q, %q, %t)",
					action,
					finalResponse,
					valid,
					test.wantAction,
					test.wantFinal,
					test.wantValid,
				)
			}
		})
	}
}
