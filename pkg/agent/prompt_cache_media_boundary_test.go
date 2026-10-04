package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/testharness/llmscenario"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestPromptCacheMediaBoundaryUsesAdmittedSnapshotAcrossRetries(t *testing.T) {
	ts := &turnState{agent: &AgentInstance{ID: "agent"}, sessionKey: "session"}
	exec := &turnExecution{}
	key := func() string {
		return buildPromptCacheLineageKey(promptCacheScopeForTurn(ts, exec, promptCachePurposeTurn),
			"openai", "test", buildPromptCachePrefixSnapshot(nil, nil))
	}
	plain := key()
	ts.captureCanonicalRestorePoint([]providers.Message{{Role: "user", Content: "ordinary old turn"}}, "")
	if key() != plain {
		t.Fatal("ordinary history changed cache lineage")
	}
	ts.captureCanonicalRestorePoint(
		[]providers.Message{{Role: "user", Media: []string{"media://private-image-ref"}}},
		"",
	)
	retired := key()
	if retired == plain || retired == "" {
		t.Fatal("historical media did not create a compatibility boundary")
	}
	exec.messages = append(
		exec.messages,
		providers.Message{Role: "tool", Media: []string{"media://current-tool-image"}},
	)
	if key() != retired {
		t.Fatal("growing active tool transcript rotated lineage")
	}
	// A new root captures the now-historical tool reference. Later plain roots
	// retain this generation even when the image blob is missing on disk.
	ts.captureCanonicalRestorePoint([]providers.Message{
		{Role: "user", Media: []string{"media://private-image-ref"}},
		{Role: "tool", Media: []string{"media://current-tool-image"}},
	}, "")
	selectedRetired := key()
	if selectedRetired == retired {
		t.Fatal("historical tool media did not rotate lineage")
	}
	ts.canonicalRestoreHistory = append(
		ts.canonicalRestoreHistory,
		providers.Message{Role: "user", Content: "plain followup"},
	)
	if key() != selectedRetired {
		t.Fatal("plain followup rotated retired-media lineage")
	}
	exec.checkpoint = &ContextCheckpoint{Content: "checkpoint", Generation: "generation-2"}
	if key() == selectedRetired {
		t.Fatal("checkpoint boundary did not rotate media-bearing lineage")
	}
}

func TestPromptCacheMediaBoundaryTracksGenericOneShotContextWithoutRetainingIt(t *testing.T) {
	ts := &turnState{agent: &AgentInstance{ID: "agent"}, sessionKey: "session"}
	exec := &turnExecution{}
	key := func() string {
		return buildPromptCacheLineageKey(promptCacheScopeForTurn(ts, exec, promptCachePurposeTurn),
			"openai", "test", buildPromptCachePrefixSnapshot(nil, nil))
	}
	plain := key()
	result := &toolshared.ToolResult{
		ForLLM: "receipt", ContextText: "PRIVATE_CONTEXT_TEXT", ContextMedia: []string{"media://private-context-media"},
	}
	message := buildToolResultJournalMessage("one-shot", result, result.ContentForModel())
	exec.messages = []providers.Message{message}
	exec.liveToolContexts = []liveToolContextProjection{{
		toolCallID: "one-shot", messageID: message.LiveToolContextID, durableContent: "receipt",
	}}
	live := key()
	if live == plain || live == "" || live != key() {
		t.Fatal("live projection key is missing, unstable or unscoped")
	}
	exec.consumeLiveToolContexts(ts)
	if key() != plain || len(exec.messages[0].Media) != 0 || exec.messages[0].Content != "receipt" {
		t.Fatal("one-shot context was retained or its retirement was not a compatibility boundary")
	}
	exec.liveToolContexts = []liveToolContextProjection{{toolCallID: "missing", messageID: "missing"}}
	if key() != "" {
		t.Fatal("missing live projection did not disable cache intent")
	}
}

func TestLiveToolContextReusedIDKeepsCacheIntentAndHistoricalResults(t *testing.T) {
	for _, identical := range []bool{false, true} {
		t.Run(map[bool]string{false: "distinct-content", true: "identical-content"}[identical], func(t *testing.T) {
			ts := &turnState{agent: &AgentInstance{ID: "agent"}, sessionKey: "session"}
			exec := &turnExecution{}
			result := &toolshared.ToolResult{
				ForLLM:       "new receipt",
				ContextText:  "one-shot text",
				ContextMedia: []string{"data:image/png;base64,eA=="},
			}
			live := buildToolResultJournalMessage("reused", result, result.ContentForModel())
			durable := durableToolResultJournalMessage(live, result, result.ContentForLLM())
			old := providers.Message{Role: "tool", ToolCallID: "reused", Content: "old receipt"}
			if identical {
				old.Content = live.Content
				old.Media = append([]string(nil), live.Media...)
			}
			runner := &toolLoopRunner{exec: exec, messages: []providers.Message{old, live}}
			ts.recordPersistedMessagePair(old, old)
			ts.recordPersistedMessagePair(live, durable)
			runner.registerLiveToolContext("reused", durable, true, true, false)
			exec.messages = append([]providers.Message{{Role: "system", Content: "rebuilt prefix"}}, runner.messages...)
			key := func() string {
				return buildPromptCacheLineageKey(promptCacheScopeForTurn(ts, exec, promptCachePurposeTurn),
					"openai", "test", buildPromptCachePrefixSnapshot(nil, nil))
			}
			if key() == "" {
				t.Error("valid one-shot context lost cache intent because a historical call ID was reused")
			}
			exec.consumeLiveToolContexts(ts)
			if !reflect.DeepEqual(exec.messages[1], old) || !reflect.DeepEqual(ts.liveTurnMessagesSnapshot()[0], old) {
				t.Error("one-shot cleanup rewrote the older tool result")
			}
			if exec.messages[2].Content != durable.Content || len(exec.messages[2].Media) != 0 ||
				ts.liveTurnMessagesSnapshot()[1].Content != durable.Content {
				t.Error("active one-shot context was not retired from the live and retry snapshots")
			}
		})
	}
}

func TestLiveToolContextMissingOccurrenceDoesNotBorrowHistoricalID(t *testing.T) {
	ts := &turnState{agent: &AgentInstance{ID: "agent"}, sessionKey: "session"}
	exec := &turnExecution{}
	result := &toolshared.ToolResult{ForLLM: "receipt", ContextText: "one-shot"}
	live := buildToolResultJournalMessage("reused", result, result.ContentForModel())
	durable := durableToolResultJournalMessage(live, result, result.ContentForLLM())
	runner := &toolLoopRunner{exec: exec, messages: []providers.Message{live}}
	runner.registerLiveToolContext("reused", durable, true, false, false)
	exec.messages = []providers.Message{{Role: "tool", ToolCallID: "reused", Content: "older result"}}
	key := buildPromptCacheLineageKey(promptCacheScopeForTurn(ts, exec, promptCachePurposeTurn),
		"openai", "test", buildPromptCachePrefixSnapshot(nil, nil))
	if key != "" {
		t.Fatal("missing active occurrence borrowed an unrelated historical result for cache intent")
	}
}

func TestLiveToolContextIdentitySurvivesRebuildButNotProviderOrDurableProjection(t *testing.T) {
	result := &toolshared.ToolResult{ForLLM: "receipt", ContextText: "one-shot"}
	live := buildToolResultJournalMessage("reused", result, result.ContentForModel())
	durable := durableToolResultJournalMessage(live, result, result.ContentForLLM())
	if live.LiveToolContextID == "" || durable.LiveToolContextID != "" {
		t.Fatal("live-only identity is missing or retained by the durable result")
	}
	history := []providers.Message{
		{Role: "user", Content: "read"},
		{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "reused", Name: "read"}}},
		live,
	}
	prepared := prepareHistoryForProvider(history, true)
	if len(prepared) != 3 || prepared[2].LiveToolContextID != live.LiveToolContextID {
		t.Fatal("internal context rebuild dropped the occurrence identity")
	}
	prepared[2].Content += "\n[resolved media tag]"
	prepared[2].Media = []string{"data:image/png;base64,eA=="}
	projection := liveToolContextProjection{toolCallID: "reused", messageID: live.LiveToolContextID}
	if !projection.matches(prepared[2]) {
		t.Fatal("media resolution changed occurrence identity")
	}
	for _, visible := range []providers.Message{
		projectTurnEnvelopesForProvider(prepared)[2], providerVisibleMessage(live), sanitizeHistoryForProvider(history)[2],
	} {
		if visible.LiveToolContextID != "" {
			t.Fatal("private occurrence identity crossed a provider boundary")
		}
	}
	encoded, err := json.Marshal(live)
	if err != nil || strings.Contains(string(encoded), live.LiveToolContextID) {
		t.Fatalf("private identity entered message JSON: err=%v", err)
	}
	var restored providers.Message
	if err := json.Unmarshal(encoded, &restored); err != nil || restored.LiveToolContextID != "" {
		t.Fatal("private identity survived message serialization")
	}
	equivalent := live
	equivalent.LiveToolContextID = "another-private-identity"
	if !messagesEquivalent(live, equivalent) {
		t.Fatal("private identity changed canonical semantic comparison")
	}
	ts := &turnState{agent: &AgentInstance{ID: "agent"}, sessionKey: "session"}
	key := func(message providers.Message) string {
		exec := &turnExecution{
			messages:         []providers.Message{message},
			liveToolContexts: []liveToolContextProjection{{toolCallID: "reused", messageID: message.LiveToolContextID}},
		}
		return buildPromptCacheLineageKey(promptCacheScopeForTurn(ts, exec, promptCachePurposeTurn),
			"openai", "test", buildPromptCachePrefixSnapshot(nil, nil))
	}
	if key(live) == "" || key(live) != key(equivalent) {
		t.Fatal("private identity changed otherwise identical cache lineage")
	}
}

func TestLiveToolContextAmbiguousIdentityDisablesCacheIntent(t *testing.T) {
	ts := &turnState{agent: &AgentInstance{ID: "agent"}, sessionKey: "session"}
	result := &toolshared.ToolResult{ForLLM: "receipt", ContextText: "one-shot"}
	live := buildToolResultJournalMessage("call", result, result.ContentForModel())
	exec := &turnExecution{
		messages: []providers.Message{live, live},
		liveToolContexts: []liveToolContextProjection{{
			toolCallID: "call", messageID: live.LiveToolContextID, durableContent: "receipt",
		}},
	}
	key := buildPromptCacheLineageKey(promptCacheScopeForTurn(ts, exec, promptCachePurposeTurn),
		"openai", "test", buildPromptCachePrefixSnapshot(nil, nil))
	if key != "" {
		t.Fatal("ambiguous live occurrence did not disable cache intent")
	}
	exec.consumeLiveToolContexts(ts)
	for _, message := range exec.messages {
		if message.Content != "receipt" || message.LiveToolContextID != "" {
			t.Fatal("a duplicate copy of owned one-shot context was not consumed")
		}
	}
}

func TestLiveToolContextReusedIDsAcrossRealToolIterations(t *testing.T) {
	var steps []llmscenario.ProviderStep
	for _, value := range []string{"first", "second", "third"} {
		steps = append(steps, llmscenario.ProviderStep{Response: llmscenario.ToolCallResponse("", llmscenario.ToolCall(
			"reused", "read_context", map[string]any{"value": value},
		))})
	}
	steps = append(steps, llmscenario.ProviderStep{Response: llmscenario.TextResponse("done")})
	provider := llmscenario.NewScriptedProvider("test-model", steps...)
	fixture := newAgentLoopTestFixture(t, provider, func(cfg *config.Config) {
		cfg.Agents.Defaults.Provider = "fixture"
		cfg.ModelList = config.SecureModelList{&config.ModelConfig{
			ModelName: "test-model", Provider: "fixture", Model: "test-model", Enabled: true,
		}}
	})
	loop, agent := fixture.Loop, fixture.Agent
	tool := llmscenario.NewStubTool("read_context", nil)
	tool.ExecuteFunc = func(_ context.Context, args map[string]any) *toolshared.ToolResult {
		value, _ := args["value"].(string)
		return &toolshared.ToolResult{ForLLM: "receipt:" + value, ContextText: "ONE_SHOT_" + value}
	}
	agent.Tools.Register(tool)
	ts := newTurnState(agent, makeTestTurnSpec("reused-context-ids"), turnEventScope{
		turnID: "reused-context-ids", context: newTurnContext(nil, nil, nil),
	})
	if _, err := runTestTurn(loop, t.Context(), ts, newTestPipeline(loop)); err != nil {
		t.Fatal(err)
	}
	if err := provider.AssertExhausted(); err != nil {
		t.Fatal(err)
	}
	for iteration, call := range provider.Calls() {
		if key, _ := call.Options["prompt_cache_key"].(string); key == "" {
			t.Fatalf("iteration %d lost cache intent", iteration)
		}
		var results []providers.Message
		for _, message := range call.Messages {
			if message.LiveToolContextID != "" {
				t.Fatal("provider received a private occurrence identity")
			}
			if message.Role == "tool" {
				results = append(results, message)
			}
		}
		if len(results) != iteration {
			t.Fatalf("iteration %d has %d tool results", iteration, len(results))
		}
		for index, value := range []string{"first", "second", "third"}[:iteration] {
			want := "receipt:" + value
			if index == iteration-1 {
				want += "\nONE_SHOT_" + value
			}
			if results[index].Content != want {
				t.Fatalf("iteration %d result %d = %q, want %q", iteration, index, results[index].Content, want)
			}
		}
	}
	for _, message := range agent.Sessions.GetHistory(ts.sessionKey) {
		if message.LiveToolContextID != "" || strings.Contains(message.Content, "ONE_SHOT_") {
			t.Fatal("one-shot context or occurrence identity entered the canonical journal")
		}
	}
}
