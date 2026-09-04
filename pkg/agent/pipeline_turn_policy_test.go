package agent

import (
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

func TestPipelineTurnPolicyIsStableUntilRuntimeReplacement(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Web.Enabled = true
	cfg.Tools.Web.PreferNative = true
	cfg.Tools.FilterSensitiveData = true
	cfg.Tools.FilterMinLength = 8
	cfg.Agents.Defaults.MaxLLMRetries = 4
	cfg.Agents.Defaults.LLMRetryBackoffSecs = 6
	cfg.Agents.Defaults.MaxMediaSize = 5678
	cfg.Agents.Defaults.FinalTurnRenderMode = "llm"
	cfg.ModelList = config.SecureModelList{
		&config.ModelConfig{
			ModelName: "test",
			APIKeys:   config.SimpleSecureStrings("sk-long-key-12345"),
		},
	}

	current := &Pipeline{Cfg: cfg, turnPolicy: newPipelineTurnPolicy(cfg)}
	cfg.Tools.Web.Enabled = false
	cfg.Tools.Web.PreferNative = false
	cfg.Tools.FilterSensitiveData = false
	cfg.Agents.Defaults.MaxLLMRetries = 7
	cfg.Agents.Defaults.LLMRetryBackoffSecs = 9
	cfg.Agents.Defaults.MaxMediaSize = 9012
	cfg.Agents.Defaults.FinalTurnRenderMode = ""

	if !current.nativeSearchEnabled(config.EffectiveTurnProfile{}, &nativeSearchProvider{supported: true}) {
		t.Fatal("current runtime generation observed reloaded native-search policy")
	}
	if retries, backoff := current.llmRetrySettings(); retries != 4 || backoff != 6 {
		t.Fatalf("current retry policy = (%d, %d), want (4, 6)", retries, backoff)
	}
	if got := current.maxMediaSize(); got != 5678 {
		t.Fatalf("current max media size = %d, want 5678", got)
	}
	if !current.shouldFinalizeAfterToolLoop(
		&turnExecution{sawSteering: true},
		newLLMIterationState(1),
	) {
		t.Fatal("current runtime generation observed reloaded final-render policy")
	}
	if got := current.filterToolContentForLLM(
		"token sk-long-key-12345 should be hidden",
	); got != "token [FILTERED] should be hidden" {
		t.Fatalf("current sensitive-data policy returned %q", got)
	}
	profile := config.EffectiveTurnProfile{
		Enabled:          true,
		SystemPromptMode: config.TurnProfileModeOff,
		ToolsMode:        config.TurnProfileModeCustom,
		AllowedTools:     []string{"web_search"},
	}
	turn := &turnState{
		agent: &AgentInstance{
			Provider: &nativeSearchProvider{supported: true},
			Tools:    tools.NewToolRegistry(),
		},
		opts:    freezeTurnInput(turnSpec{Dispatch: DispatchRequest{SessionKey: "policy-snapshot"}}),
		profile: profile,
	}
	currentPrompt := current.promptRequestForTurn(turn, nil, "", "search", nil)
	if currentPrompt.SuppressToolUseRule || !currentPrompt.ToolUseFallback {
		t.Fatalf("current prompt lost callable native search: %#v", currentPrompt)
	}

	replacement := &Pipeline{Cfg: cfg, turnPolicy: newPipelineTurnPolicy(cfg)}
	if replacement.nativeSearchEnabled(config.EffectiveTurnProfile{}, &nativeSearchProvider{supported: true}) {
		t.Fatal("replacement runtime generation retained old native-search policy")
	}
	if retries, backoff := replacement.llmRetrySettings(); retries != 7 || backoff != 9 {
		t.Fatalf("replacement retry policy = (%d, %d), want (7, 9)", retries, backoff)
	}
	if got := replacement.maxMediaSize(); got != 9012 {
		t.Fatalf("replacement max media size = %d, want 9012", got)
	}
	if replacement.shouldFinalizeAfterToolLoop(
		&turnExecution{sawSteering: true},
		newLLMIterationState(1),
	) {
		t.Fatal("replacement runtime generation retained old final-render policy")
	}
	if got := replacement.filterToolContentForLLM(
		"token sk-long-key-12345 should stay",
	); got != "token sk-long-key-12345 should stay" {
		t.Fatalf("replacement sensitive-data policy returned %q", got)
	}
	replacementPrompt := replacement.promptRequestForTurn(turn, nil, "", "search", nil)
	if !replacementPrompt.SuppressToolUseRule || replacementPrompt.ToolUseFallback {
		t.Fatalf("replacement prompt retained old callable native search: %#v", replacementPrompt)
	}
}
