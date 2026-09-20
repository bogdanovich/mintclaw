package agent

import (
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
)

const interactionContinuationDecisionTool = "runtime_interaction_continuation_decision"

const maxInteractionContinuationDecisionAttempts = 2

type interactionContinuationDecisionPhase uint8

const (
	interactionContinuationDecisionInactive interactionContinuationDecisionPhase = iota
	interactionContinuationDecisionPending
	interactionContinuationDecisionProceed
	interactionContinuationDecisionFinalize
)

type interactionContinuationDecisionState struct {
	enabled         bool
	phase           interactionContinuationDecisionPhase
	invalidAttempts int
	modelCalls      int
}

func newInteractionContinuationDecisionState(opts turnInput) interactionContinuationDecisionState {
	continuation := opts.InteractionContinuation
	if continuation.Kind != interactions.KindQuestion || continuation.Outcome != interactions.OutcomeAnswered ||
		!turnInputOwnsLiveHandoff(opts) {
		return interactionContinuationDecisionState{}
	}
	return interactionContinuationDecisionState{
		enabled: true,
		phase:   interactionContinuationDecisionPending,
	}
}

func turnInputOwnsLiveHandoff(opts turnInput) bool {
	for _, item := range opts.ObjectiveChecklist {
		if strings.TrimSpace(item.Kind) == taskresult.ObjectiveKindLiveHandoff {
			return true
		}
	}
	for _, receipt := range opts.InitialReceipts {
		if strings.TrimSpace(receipt.Kind) == taskresult.ObjectiveKindLiveHandoff {
			return true
		}
	}
	return false
}

func (state *interactionContinuationDecisionState) pending() bool {
	if state == nil {
		return false
	}
	return state.phase == interactionContinuationDecisionPending
}

func (state *interactionContinuationDecisionState) finalizing() bool {
	if state == nil {
		return false
	}
	return state.phase == interactionContinuationDecisionFinalize
}

func (state *interactionContinuationDecisionState) requiresModelCall() bool {
	if state == nil {
		return false
	}
	return state.pending() || state.finalizing()
}

func (state *interactionContinuationDecisionState) rearmForNewGuidance() bool {
	if state == nil || !state.enabled {
		return false
	}
	state.phase = interactionContinuationDecisionPending
	state.invalidAttempts = 0
	return true
}

func interactionContinuationDecisionToolDefinition() providers.ToolDefinition {
	return providers.ToolDefinition{
		Type: "function",
		Function: providers.ToolFunctionDefinition{
			Name: interactionContinuationDecisionTool,
			Description: "Classify whether the latest human answer continues, redirects, or finishes the suspended " +
				"live-resource request. This is an internal decision only and performs no external action.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action": map[string]any{
						"type": "string",
						"enum": []string{"continue", "redirect", "finish"},
						"description": "continue resumes the suspended plan; redirect follows changed live-resource " +
							"instructions; finish ends the task and releases the resource",
					},
					"final_response": map[string]any{
						"type":        "string",
						"description": "For finish, a concise user-facing completion response in the user's language",
					},
				},
				"required":             []string{"action"},
				"additionalProperties": false,
			},
		},
	}
}

func interactionContinuationDecisionInstruction(attempt int) providers.Message {
	retry := ""
	if attempt > 0 {
		retry = " The previous response did not call the required decision tool with a valid action; correct it now."
	}
	return providers.Message{Role: "user", Content: `<runtime_interaction_continuation_preflight>
Before any external tool can run, classify the latest human answer against the suspended live-resource request.
Use the answer's meaning in its original language and the accumulated conversation context; do not use keyword matching.
- continue: the answer completes the manual step or otherwise asks to resume the previously requested sequence.
- redirect: the answer changes what should happen next while keeping work on the live resource active.
- finish: the answer cancels, ends, or says the task is complete; the runtime will release the live resource.
Call runtime_interaction_continuation_decision exactly once. No external action is available in this preflight.` + retry + `
</runtime_interaction_continuation_preflight>`}
}

func interactionContinuationProceedMessage(action string) providers.Message {
	direction := "Continue the suspended request from the latest human answer."
	if action == "redirect" {
		direction = "The latest human answer redirects the suspended request. Follow it as authoritative and do not " +
			"revive superseded steps."
	}
	return providers.Message{Role: "user", Content: "<runtime_interaction_continuation_decision>" + direction +
		" External tools are now available when required.</runtime_interaction_continuation_decision>"}
}

func interactionContinuationFinalizeMessage() providers.Message {
	return providers.Message{Role: "user", Content: `<runtime_interaction_continuation_decision>
The latest human answer ends the suspended request. Do not call any tool or continue prior steps. Return only a concise
final response in the user's language. The runtime will release the live resource during terminal cleanup.
</runtime_interaction_continuation_decision>`}
}

func parseInteractionContinuationDecision(response *providers.LLMResponse) (string, string, bool) {
	if response == nil || len(response.ToolCalls) != 1 {
		return "", "", false
	}
	call := providers.NormalizeToolCall(response.ToolCalls[0])
	if call.Name != interactionContinuationDecisionTool {
		return "", "", false
	}
	action, _ := call.Arguments["action"].(string)
	action = strings.ToLower(strings.TrimSpace(action))
	switch action {
	case "continue", "redirect", "finish":
	default:
		return "", "", false
	}
	finalResponse, _ := call.Arguments["final_response"].(string)
	return action, strings.TrimSpace(finalResponse), true
}
