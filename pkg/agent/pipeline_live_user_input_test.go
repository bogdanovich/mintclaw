package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/providers"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func liveUserInputFixture() ([]providers.Message, liveToolContextProjection) {
	messages := []providers.Message{
		{Role: "system", Content: "stable prefix"},
		{Role: "user", Content: "opaque native receipt", RootTurnStart: true},
		{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "grant"}, {ID: "sibling"}}},
		{Role: "tool", ToolCallID: "grant", Content: "safe grant metadata", LiveToolContextID: "owned-live-grant"},
		{Role: "tool", ToolCallID: "sibling", Content: "sibling result"},
		{Role: "user", Content: "later steering wins"},
	}
	return messages, liveToolContextProjection{
		toolCallID: "grant", messageID: "owned-live-grant", durableContent: "safe grant metadata",
		turnScopedInput: true, userInputText: "CURRENT_PRIVATE_INPUT_828",
	}
}

func TestLiveUserInputProjectionKeepsPairingSteeringAndCanonicalBytes(t *testing.T) {
	messages, projection := liveUserInputFixture()
	before, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	projected, err := projectLiveUserInputs(messages, []liveToolContextProjection{projection})
	if err != nil || len(projected) != len(messages)+1 || projected[4].ToolCallID != "sibling" ||
		projected[5].Role != "user" || projected[5].Content != projection.userInputText ||
		projected[6].Content != "later steering wins" || projected[0].Content != "stable prefix" {
		t.Fatal("projection broke pairing, input identity, steering order or stable prefix")
	}
	after, err := json.Marshal(messages)
	if err != nil || string(before) != string(after) {
		t.Fatal("projection changed canonical input")
	}
	projection.turnScopedInput = false
	plain, err := projectLiveUserInputs(messages, []liveToolContextProjection{projection})
	if err != nil || !reflect.DeepEqual(plain, messages) {
		t.Fatal("generic tool data acquired user-message authority")
	}
}

func TestLiveUserInputDoesNotBorrowHistoricalOrAmbiguousCallIdentities(t *testing.T) {
	messages, projection := liveUserInputFixture()
	older := messages[3]
	older.LiveToolContextID = "older-grant-with-reused-provider-id"
	messages = append([]providers.Message{older}, messages...)
	projected, err := projectLiveUserInputs(messages, []liveToolContextProjection{projection})
	if err != nil || projected[0].Content != older.Content {
		t.Fatal("historical reused provider ID changed native ownership")
	}
	messages[4].LiveToolContextID = "missing"
	if _, err = projectLiveUserInputs(messages, []liveToolContextProjection{projection}); err == nil {
		t.Fatal("missing live identity borrowed the older provider ID")
	}
	messages[4].LiveToolContextID = projection.messageID
	messages[0].LiveToolContextID = projection.messageID
	if _, err = projectLiveUserInputs(messages, []liveToolContextProjection{projection}); err == nil {
		t.Fatal("ambiguous live identity was accepted")
	}
}

func TestLiveUserInputKeepsToolBlockOrderAndRejectsRepeatedOwners(t *testing.T) {
	messages, first := liveUserInputFixture()
	messages[4].LiveToolContextID = "owned-sibling"
	second := liveToolContextProjection{
		toolCallID: "sibling", messageID: "owned-sibling", turnScopedInput: true, userInputText: "second input",
	}
	projected, err := projectLiveUserInputs(messages, []liveToolContextProjection{second, first})
	if err != nil || projected[5].Content != first.userInputText || projected[6].Content != second.userInputText {
		t.Fatal("projection reordered native inputs within the paired tool block")
	}
	if _, err = projectLiveUserInputs(messages, []liveToolContextProjection{first, first}); err == nil {
		t.Fatal("repeated native owner produced duplicate user input")
	}
}

func TestLiveUserInputSurvivesDetachedHookAndRebuildWithoutDiagnosticOrHashPayload(t *testing.T) {
	messages, projection := liveUserInputFixture()
	clone := stripCanonicalMessageStateFromAll(messages)
	if err := restoreLiveUserInputIdentities(messages, clone, []liveToolContextProjection{projection}); err != nil {
		t.Fatal(err)
	}
	state := newLLMIterationState(2)
	state.liveUserInputs = []liveToolContextProjection{projection}
	if err := state.setCanonicalAttemptMessages(clone, promptCacheTailBoundary{Start: 1, Found: true}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		encoded, err := json.Marshal(state.callMessages)
		if err != nil || strings.Count(string(encoded), projection.userInputText) != 1 {
			t.Fatal("native input was missing or duplicated in the provider request")
		}
		for _, safe := range [][]providers.Message{state.diagnosticMessagesForAttempt(), state.promptHashMessagesForAttempt()} {
			encoded, err = json.Marshal(safe)
			if err != nil || strings.Contains(string(encoded), projection.userInputText) {
				t.Fatal("native input leaked into diagnostic or prompt-hash source")
			}
		}
		rebuilt := append([]providers.Message{{Role: "system", Content: "rebuilt prefix"}}, clone...)
		if err = state.replaceCanonicalAttemptMessages(rebuilt); err != nil {
			t.Fatal(err)
		}
	}
	clone[3].Content = "hook changed grant owner payload"
	if err := restoreLiveUserInputIdentities(messages, clone, []liveToolContextProjection{projection}); err == nil {
		t.Fatal("hook replacement borrowed native grant identity")
	}
}

func TestLiveUserInputUsesOnlyWholeAdmittedTypedText(t *testing.T) {
	pipeline := &Pipeline{}
	const input = "AUTHORIZED_PRIVATE_INPUT_71"
	for _, admitted := range []string{"unrelated document text", "prefix\n" + input[:4]} {
		if _, _, err := pipeline.splitLiveUserInput(&toolshared.ToolContextInput{Text: input}, admitted); err == nil {
			t.Fatal("unadmitted or truncated text was promoted to user input")
		}
	}
	prefix, user, err := pipeline.splitLiveUserInput(&toolshared.ToolContextInput{Text: input}, "prefix\n"+input)
	if err != nil || prefix != "prefix\n" || user != input || len(prefix)+len(user) != len("prefix\n"+input) {
		t.Fatal("whole typed input was lost, duplicated or recounted")
	}
	plain, user, err := pipeline.splitLiveUserInput(nil, input)
	if err != nil || plain != input || user != "" {
		t.Fatal("generic extracted text was promoted to user input")
	}
}

func TestLiveUserInputParticipatesInExistingCacheProjectionFingerprint(t *testing.T) {
	messages, projection := liveUserInputFixture()
	ts := &turnState{agent: &AgentInstance{ID: "agent"}, sessionKey: "owned-session"}
	exec := &turnExecution{messages: messages, liveToolContexts: []liveToolContextProjection{projection}}
	first := promptCacheScopeForTurn(ts, exec, promptCachePurposeTurn)
	if first.LiveToolProjection == "" {
		t.Fatal("native live input disabled cache intent")
	}
	exec.liveToolContexts[0].userInputText = "a different freshly authorized input"
	second := promptCacheScopeForTurn(ts, exec, promptCachePurposeTurn)
	if first.LiveToolProjection == second.LiveToolProjection || second.LiveToolProjection == "" {
		t.Fatal("cache projection ignored changed live user input")
	}
}

func TestLiveUserInputTokenReserveIgnoresGenericToolData(t *testing.T) {
	_, projection := liveUserInputFixture()
	if got := liveUserInputTokenReserve([]liveToolContextProjection{projection}); got !=
		EstimateMessageTokens(providers.Message{Role: "user", Content: projection.userInputText}) {
		t.Fatal("native user-message framing was not included in its token reserve")
	}
	projection.turnScopedInput = false
	if liveUserInputTokenReserve(nil) != 0 || liveUserInputTokenReserve([]liveToolContextProjection{projection}) != 0 {
		t.Fatal("generic tool context changed existing token budgets")
	}
}
