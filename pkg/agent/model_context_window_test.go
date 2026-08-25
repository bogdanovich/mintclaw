package agent

import (
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func TestBuildAgentRuntimeConfigUsesConfiguredModelContextWindow(t *testing.T) {
	cfg := &config.Config{ModelList: config.SecureModelList{
		{
			ModelName: "remote", Provider: "openai", Model: "gpt-future", AuthMethod: "oauth",
			ContextWindow: 345_000, Enabled: true,
		},
	}}
	defaults := &config.AgentDefaults{MaxTokens: 32_768}
	got := buildAgentRuntimeConfig(defaults, cfg.ModelList[0])
	if got.contextWindow != 345_000 {
		t.Fatalf("context window = %d, want 345000", got.contextWindow)
	}
}

func TestBuildAgentRuntimeConfigUsesBundledCodexMetadata(t *testing.T) {
	cfg := &config.Config{ModelList: config.SecureModelList{
		{
			ModelName: providers.CodexDefaultModel, Provider: "openai", Model: providers.CodexDefaultModel,
			AuthMethod: "oauth", Enabled: true,
		},
	}}
	defaults := &config.AgentDefaults{MaxTokens: 32_768}
	got := buildAgentRuntimeConfig(defaults, cfg.ModelList[0])
	if got.contextWindow != providers.CodexDefaultContextWindow {
		t.Fatalf("context window = %d, want %d", got.contextWindow, providers.CodexDefaultContextWindow)
	}
}

func TestBuildAgentRuntimeConfigPreservesExplicitContextWindow(t *testing.T) {
	cfg := &config.Config{ModelList: config.SecureModelList{
		{
			ModelName: providers.CodexDefaultModel, Provider: "openai", Model: providers.CodexDefaultModel,
			AuthMethod: "oauth", ContextWindow: providers.CodexDefaultContextWindow, Enabled: true,
		},
	}}
	defaults := &config.AgentDefaults{MaxTokens: 32_768, ContextWindow: 123_456}
	got := buildAgentRuntimeConfig(defaults, cfg.ModelList[0])
	if got.contextWindow != 123_456 {
		t.Fatalf("context window = %d, want explicit override 123456", got.contextWindow)
	}
}

func TestRuntimeConfigBindsContextWindowToLoadBalancedProviderSelection(t *testing.T) {
	workspace := t.TempDir()
	cfg := &config.Config{
		Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
			Workspace: workspace,
			ModelName: "balanced",
		}},
		ModelList: config.SecureModelList{
			{
				ModelName: "balanced", Provider: "openai", Model: "gpt-first",
				APIBase: "https://example.invalid/v1", ContextWindow: 111_000, Enabled: true,
			},
			{
				ModelName: "balanced", Provider: "openai", Model: "gpt-second",
				APIBase: "https://example.invalid/v1", ContextWindow: 222_000, Enabled: true,
			},
		},
	}

	fallback := &mockProvider{}
	provider, selected := resolvePrimaryProviderForAgent(
		cfg,
		workspace,
		"main",
		"balanced",
		fallback,
		newProviderOwnership(fallback),
	)
	if providersShareIdentity(provider, fallback) || selected == nil {
		t.Fatalf("provider selection fell back: provider = %T, selected = %+v", provider, selected)
	}
	selectedConfig := selected.modelConfig

	wantByModel := map[string]int{"gpt-first": 111_000, "gpt-second": 222_000}
	want, ok := wantByModel[selectedConfig.Model]
	if !ok {
		t.Fatalf("selected provider model = %q", selectedConfig.Model)
	}
	// A second lookup advances round-robin and demonstrates why runtime metadata
	// must use the entry returned with the provider rather than resolving again.
	next, err := cfg.GetModelConfig("balanced")
	if err != nil {
		t.Fatal(err)
	}
	if next.Model == selectedConfig.Model {
		t.Fatalf("round-robin did not advance: selected = %q, next = %q", selectedConfig.Model, next.Model)
	}

	runtimeCfg := buildAgentRuntimeConfig(&cfg.Agents.Defaults, selectedConfig)
	if runtimeCfg.contextWindow != want {
		t.Fatalf(
			"context window = %d, want %d for selected provider model %q",
			runtimeCfg.contextWindow,
			want,
			selectedConfig.Model,
		)
	}
	routingCfg := buildAgentRoutingConfig(
		cfg,
		&cfg.Agents.Defaults,
		workspace,
		selected,
		nil,
		"main",
		newProviderOwnership(provider),
	)
	if len(routingCfg.candidates) != 1 || routingCfg.candidates[0].Model != selectedConfig.Model ||
		routingCfg.candidates[0].ConfigOrdinal != selected.configOrdinal {
		t.Fatalf("primary candidates = %+v, selected = %+v", routingCfg.candidates, selected)
	}
}
