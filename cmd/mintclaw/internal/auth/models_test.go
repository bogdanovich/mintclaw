package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bogdanovich/mintclaw/pkg/config"
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
			model: &config.ModelConfig{ModelName: "gpt", Provider: "openai", Model: "gpt-5.4"},
			configure: func(cfg *config.Config) {
				configureOpenAIAuth(cfg, "oauth")
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
