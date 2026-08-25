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
	got := buildAgentRuntimeConfig(defaults, cfg, "remote")
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
	got := buildAgentRuntimeConfig(defaults, cfg, providers.CodexDefaultModel)
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
	got := buildAgentRuntimeConfig(defaults, cfg, providers.CodexDefaultModel)
	if got.contextWindow != 123_456 {
		t.Fatalf("context window = %d, want explicit override 123456", got.contextWindow)
	}
}
