package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func TestNewModelsCommand(t *testing.T) {
	cmd := newModelsCommand()

	require.NotNil(t, cmd)

	assert.Equal(t, "models", cmd.Use)
	assert.Equal(t, "Show available models", cmd.Short)

	assert.False(t, cmd.HasFlags())
}

func TestConfigureAuthEnablesMatchedModel(t *testing.T) {
	tests := []struct {
		name      string
		model     *config.ModelConfig
		configure func(*config.Config)
	}{
		{
			name:  "openai",
			model: &config.ModelConfig{ModelName: "gpt", Provider: "openai", Model: providers.CodexDefaultModel},
			configure: func(cfg *config.Config) {
				configureOpenAIAuth(cfg, "oauth", providers.DefaultCodexModelInfo())
			},
		},
		{
			name:  "anthropic",
			model: &config.ModelConfig{ModelName: "claude", Provider: "anthropic", Model: "claude-sonnet-4-6"},
			configure: func(cfg *config.Config) {
				configureAnthropicAuth(cfg, "oauth", false)
			},
		},
		{
			name:  "antigravity",
			model: &config.ModelConfig{ModelName: "gemini", Provider: "antigravity", Model: "gemini-3-flash"},
			configure: func(cfg *config.Config) {
				configureAntigravityAuth(cfg, "oauth")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{ModelList: config.SecureModelList{tt.model}}
			tt.configure(cfg)
			if !tt.model.Enabled {
				t.Fatal("configured model was not enabled")
			}
		})
	}
}

func TestConfigureOpenAIAuthAddsPreferredModelWithContextMetadata(t *testing.T) {
	cfg := &config.Config{ModelList: config.SecureModelList{
		{ModelName: "older", Provider: "openai", Model: "gpt-5.4", Enabled: true},
	}}
	selected := providers.CodexModelInfo{
		Slug: "gpt-next", ContextWindow: 300_000, MaxContextWindow: 900_000,
	}
	configureOpenAIAuth(cfg, "oauth", selected)

	if cfg.Agents.Defaults.ModelName != "gpt-next" || len(cfg.ModelList) != 2 {
		t.Fatalf("configured defaults = %q, models = %+v", cfg.Agents.Defaults.ModelName, cfg.ModelList)
	}
	model := cfg.ModelList[1]
	if model.Model != "gpt-next" || model.AuthMethod != "oauth" || !model.Enabled ||
		model.ContextWindow != 300_000 || model.MaxContextWindow != 900_000 {
		t.Fatalf("preferred model config = %+v", model)
	}
}

func TestConfigureOpenAIAuthPreservesNamespacedModelAlias(t *testing.T) {
	model := &config.ModelConfig{
		ModelName: "fast", Provider: "openai", Model: "openai/gpt-next", Enabled: true,
	}
	cfg := &config.Config{ModelList: config.SecureModelList{model}}
	selected := providers.CodexModelInfo{Slug: "gpt-next", ContextWindow: 300_000}

	configureOpenAIAuth(cfg, "oauth", selected)

	if len(cfg.ModelList) != 1 || cfg.Agents.Defaults.ModelName != "fast" {
		t.Fatalf("default = %q, models = %+v", cfg.Agents.Defaults.ModelName, cfg.ModelList)
	}
	if model.AuthMethod != "oauth" || model.ContextWindow != 300_000 {
		t.Fatalf("namespaced model = %+v", model)
	}
}

func TestConfigureOpenAIAuthAvoidsAmbiguousLoadBalancedAlias(t *testing.T) {
	cfg := &config.Config{ModelList: config.SecureModelList{
		{ModelName: "balanced", Provider: "openai", Model: "gpt-old", Enabled: true},
		{ModelName: "balanced", Provider: "openai", Model: "gpt-next", Enabled: true},
	}}
	selected := providers.CodexModelInfo{Slug: "gpt-next", ContextWindow: 300_000}

	configureOpenAIAuth(cfg, "oauth", selected)

	if cfg.Agents.Defaults.ModelName != "gpt-next" || len(cfg.ModelList) != 3 {
		t.Fatalf("default = %q, models = %+v", cfg.Agents.Defaults.ModelName, cfg.ModelList)
	}
	model := cfg.ModelList[2]
	if model.ModelName != "gpt-next" || model.Model != "gpt-next" || model.AuthMethod != "oauth" {
		t.Fatalf("dedicated model = %+v", model)
	}
}

func TestConfiguredDefaultModelFallsBackWithoutConfigSnapshot(t *testing.T) {
	if got := configuredDefaultModel(nil, "gpt-next"); got != "gpt-next" {
		t.Fatalf("configured default = %q, want gpt-next", got)
	}
}
