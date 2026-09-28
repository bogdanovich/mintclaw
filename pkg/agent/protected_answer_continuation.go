package agent

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

const maxProtectedAnswerContinuationAttempts = 2

type protectedAnswerContinuationState struct {
	enabled         bool
	awaitingExecute bool
	toolName        string
	arguments       map[string]any
	followup        *toolshared.ToolOnlyFollowup
	followupReceipt bool
	setupErr        error
	invalidAttempts int
	modelCalls      int
}

func newProtectedAnswerContinuationState(
	opts turnInput,
	registry *tools.ToolRegistry,
) protectedAnswerContinuationState {
	continuation := opts.InteractionContinuation
	if strings.TrimSpace(continuation.ProtectedAnswer) == "" {
		return protectedAnswerContinuationState{}
	}
	fail := func(message string) protectedAnswerContinuationState {
		return protectedAnswerContinuationState{setupErr: errors.New(message)}
	}
	if continuation.Kind != interactions.KindQuestion || continuation.Outcome != interactions.OutcomeAnswered {
		return fail("protected answer continuation context is invalid")
	}
	if strings.TrimSpace(continuation.OriginToolName) == "" || registry == nil {
		return fail("protected answer continuation origin is unavailable")
	}
	tool, ok := registry.GetRegistered(continuation.OriginToolName)
	if !ok {
		return fail("protected answer continuation tool is unavailable")
	}
	provider, ok := tool.(toolshared.ProtectedAnswerContinuationProvider)
	if !ok {
		return fail("protected answer continuation tool is unsupported")
	}
	arguments, err := provider.ProtectedAnswerContinuationArguments(continuation.ProtectedAnswer)
	if err != nil || len(arguments) == 0 {
		return fail("protected answer continuation arguments are invalid")
	}
	return protectedAnswerContinuationState{
		enabled:   true,
		toolName:  strings.TrimSpace(continuation.OriginToolName),
		arguments: cloneStringAnyMap(arguments),
	}
}

func (state *protectedAnswerContinuationState) pending() bool {
	return state != nil && state.enabled
}

func (state *protectedAnswerContinuationState) awaitingExecution() bool {
	return state != nil && state.enabled && state.awaitingExecute
}

func (state *protectedAnswerContinuationState) awaitingInitialExecution() bool {
	return state != nil && state.awaitingExecution() && state.followup == nil
}

func (state *protectedAnswerContinuationState) awaitingFollowupExecution() bool {
	return state != nil && state.awaitingExecution() && state.followup != nil
}

func (state *protectedAnswerContinuationState) awaitExecution() {
	if state != nil && state.enabled {
		state.awaitingExecute = true
	}
}

func (state *protectedAnswerContinuationState) complete() {
	if state != nil {
		state.enabled = false
		state.awaitingExecute = false
		state.followup = nil
		state.followupReceipt = false
	}
}

func (state *protectedAnswerContinuationState) beginFollowup(
	followup *toolshared.ToolOnlyFollowup,
) error {
	if state == nil || !state.enabled || state.followup != nil || followup == nil ||
		!validRequiredFollowup(followup) {
		return errors.New("protected answer follow-up is invalid")
	}
	cloned := *followup
	state.followup = &cloned
	state.followupReceipt = true
	state.awaitingExecute = false
	state.invalidAttempts = 0
	return nil
}

func (state *protectedAnswerContinuationState) beginToolResultFollowup(
	toolName string,
	followup *toolshared.ToolOnlyFollowup,
) error {
	toolName = strings.TrimSpace(toolName)
	if state == nil || state.enabled || toolName == "" || followup == nil ||
		!validRequiredFollowup(followup) {
		return errors.New("tool result follow-up is invalid")
	}
	cloned := *followup
	state.enabled = true
	state.toolName = toolName
	state.arguments = nil
	state.followup = &cloned
	state.followupReceipt = false
	state.awaitingExecute = false
	state.invalidAttempts = 0
	state.modelCalls = 0
	return nil
}

func (state *protectedAnswerContinuationState) matchesExecution(
	toolName string,
	arguments map[string]any,
) bool {
	if state == nil || !state.awaitingExecution() || toolName != state.toolName {
		return false
	}
	if state.followup == nil {
		return reflect.DeepEqual(arguments, state.arguments)
	}
	if state.followup.ResponseOnly {
		return false
	}
	return state.followup.ValidateArguments(cloneStringAnyMap(arguments)) == nil
}

func (state *protectedAnswerContinuationState) restrictToolDefinitions(
	definitions []providers.ToolDefinition,
) []providers.ToolDefinition {
	if state == nil || !state.enabled {
		return definitions
	}
	if state.followup != nil && state.followup.ResponseOnly {
		return nil
	}
	for _, definition := range definitions {
		if definition.Function.Name == state.toolName {
			return []providers.ToolDefinition{definition}
		}
	}
	return nil
}

func (state *protectedAnswerContinuationState) instruction() providers.Message {
	if state.followup != nil {
		if state.followup.ResponseOnly {
			retry := ""
			if state.invalidAttempts > 0 {
				retry = " The previous response attempted a tool call or omitted the required user-facing response; correct it now."
			}
			return providers.Message{Role: "user", Content: `<runtime_response_only_followup>
The preceding trusted tool result reached a human review checkpoint. No tools are available in this iteration.
` + strings.TrimSpace(state.followup.Instruction) + retry + `
</runtime_response_only_followup>`}
		}
		lead := "The preceding trusted tool result requires one bounded transition through the only available trusted tool."
		if state.followupReceipt {
			lead = "The protected answer receipt was consumed successfully. The workflow must now make one bounded transition through the only available trusted tool."
		}
		retry := ""
		if state.invalidAttempts > 0 {
			retry = " The previous response did not make an allowed tool-only follow-up; correct it now."
		}
		return providers.Message{Role: "user", Content: `<runtime_tool_only_followup>
` + lead + `
` + strings.TrimSpace(state.followup.Instruction) + retry + `
</runtime_tool_only_followup>`}
	}
	arguments, _ := json.Marshal(state.arguments)
	retry := ""
	if state.invalidAttempts > 0 {
		retry = " The previous response did not make the required exact tool call; correct it now."
	}
	return providers.Message{Role: "user", Content: `<runtime_protected_answer_continuation>
The user's protected answer has already been accepted and is represented only by an opaque receipt.
Call the only available trusted tool exactly once with these exact arguments: ` + string(arguments) + `.
Do not answer in prose, repeat the question, request the value again, or select the next field yet.` + retry + `
</runtime_protected_answer_continuation>`}
}

func (state *protectedAnswerContinuationState) accept(response *providers.LLMResponse) bool {
	if state == nil || !state.enabled || state.awaitingExecute || response == nil {
		return false
	}
	if state.followup != nil && state.followup.ResponseOnly {
		return strings.TrimSpace(response.Content) != "" && len(response.ToolCalls) == 0
	}
	if strings.TrimSpace(response.Content) != "" || len(response.ToolCalls) != 1 {
		return false
	}
	call := providers.NormalizeToolCall(response.ToolCalls[0])
	if call.Name != state.toolName {
		return false
	}
	if state.followup == nil {
		return reflect.DeepEqual(call.Arguments, state.arguments)
	}
	return state.followup.ValidateArguments(cloneStringAnyMap(call.Arguments)) == nil
}

func (state *protectedAnswerContinuationState) responseOnly() bool {
	return state != nil && state.enabled && state.followup != nil && state.followup.ResponseOnly
}

func validRequiredFollowup(followup *toolshared.ToolOnlyFollowup) bool {
	if followup == nil || strings.TrimSpace(followup.Instruction) == "" {
		return false
	}
	return followup.ResponseOnly == (followup.ValidateArguments == nil)
}
