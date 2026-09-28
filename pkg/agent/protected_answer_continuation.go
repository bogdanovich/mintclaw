package agent

import (
	"encoding/json"
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
	toolName        string
	arguments       map[string]any
	invalidAttempts int
	modelCalls      int
}

func newProtectedAnswerContinuationState(
	opts turnInput,
	registry *tools.ToolRegistry,
) protectedAnswerContinuationState {
	continuation := opts.InteractionContinuation
	if continuation.Kind != interactions.KindQuestion ||
		continuation.Outcome != interactions.OutcomeAnswered ||
		strings.TrimSpace(continuation.ProtectedAnswer) == "" ||
		strings.TrimSpace(continuation.OriginToolName) == "" || registry == nil {
		return protectedAnswerContinuationState{}
	}
	tool, ok := registry.GetRegistered(continuation.OriginToolName)
	if !ok {
		return protectedAnswerContinuationState{}
	}
	provider, ok := tool.(toolshared.ProtectedAnswerContinuationProvider)
	if !ok {
		return protectedAnswerContinuationState{}
	}
	arguments, err := provider.ProtectedAnswerContinuationArguments(continuation.ProtectedAnswer)
	if err != nil || len(arguments) == 0 {
		return protectedAnswerContinuationState{}
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

func (state *protectedAnswerContinuationState) complete() {
	if state != nil {
		state.enabled = false
	}
}

func (state *protectedAnswerContinuationState) restrictToolDefinitions(
	definitions []providers.ToolDefinition,
) []providers.ToolDefinition {
	if state == nil || !state.enabled {
		return definitions
	}
	for _, definition := range definitions {
		if definition.Function.Name == state.toolName {
			return []providers.ToolDefinition{definition}
		}
	}
	return nil
}

func (state *protectedAnswerContinuationState) instruction() providers.Message {
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
	if state == nil || !state.enabled || response == nil || len(response.ToolCalls) != 1 {
		return false
	}
	call := providers.NormalizeToolCall(response.ToolCalls[0])
	return call.Name == state.toolName && reflect.DeepEqual(call.Arguments, state.arguments)
}
