package agent

import (
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestPipelineLLMRetrySettingsUsesConfig(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.MaxLLMRetries = 4
	cfg.Agents.Defaults.LLMRetryBackoffSecs = 6
	pipeline := &Pipeline{Cfg: cfg, turnPolicy: newPipelineTurnPolicy(cfg)}

	maxRetries, backoffSecs := pipeline.llmRetrySettings()
	if maxRetries != 4 || backoffSecs != 6 {
		t.Fatalf("llmRetrySettings() = (%d, %d), want (4, 6)", maxRetries, backoffSecs)
	}
}

func TestPipelineLLMRetrySettings_DefaultsInvalidConfig(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.MaxLLMRetries = 0
	cfg.Agents.Defaults.LLMRetryBackoffSecs = 0
	pipeline := &Pipeline{Cfg: cfg, turnPolicy: newPipelineTurnPolicy(cfg)}

	maxRetries, backoffSecs := pipeline.llmRetrySettings()
	if maxRetries != 2 || backoffSecs != 2 {
		t.Fatalf("llmRetrySettings() = (%d, %d), want (2, 2)", maxRetries, backoffSecs)
	}
}
