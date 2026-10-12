package agent

import (
	"testing"

	"github.com/bogdanovich/mintclaw/internal/secretvalues"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestPipelineFilterToolContentForLLMUsesConfig(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.FilterSensitiveData = true
	cfg.Tools.FilterMinLength = 8
	cfg.ModelList = config.SecureModelList{
		&config.ModelConfig{
			ModelName: "test",
			APIKeys:   config.SimpleSecureStrings("sk-long-key-12345"),
		},
	}
	pipeline := &Pipeline{Cfg: cfg, turnPolicy: newPipelineTurnPolicy(cfg)}

	got := pipeline.filterToolContentForLLM("token sk-long-key-12345 should be hidden")
	if got != "token [FILTERED] should be hidden" {
		t.Fatal("expected config to redact sensitive tool content")
	}
}

func TestPipelineGuardsCredentialsRegisteredAfterTurnPolicySnapshot(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.FilterSensitiveData = false
	cfg.Tools.FilterMinLength = 4096
	pipeline := &Pipeline{Cfg: cfg, turnPolicy: newPipelineTurnPolicy(cfg)}
	release, err := secretvalues.Register([]byte("698127"))
	if err != nil {
		t.Fatal("credential guard unavailable")
	}
	defer release()
	for _, candidate := range []*Pipeline{nil, pipeline} {
		if candidate.filterToolContentForLLM("698127") != secretvalues.Marker ||
			candidate.filterPendingResultForLLM("698127") != secretvalues.Marker {
			t.Fatal("cached turn policy exposed a dynamically retrieved credential")
		}
		if candidate.filterToolContentForLLM("alias=ebay outcome=success") != "alias=ebay outcome=success" {
			t.Fatal("public credential metadata was changed")
		}
	}
}

func TestPipelineFilterPendingResultForLLM_UsesConfigPath(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.FilterSensitiveData = true
	cfg.Tools.FilterMinLength = 8
	cfg.ModelList = config.SecureModelList{
		&config.ModelConfig{
			ModelName: "test",
			APIKeys:   config.SimpleSecureStrings("sk-long-key-12345"),
		},
	}
	pipeline := &Pipeline{Cfg: cfg, turnPolicy: newPipelineTurnPolicy(cfg)}

	got := pipeline.filterPendingResultForLLM("pending sk-long-key-12345 result")
	if got != "pending [FILTERED] result" {
		t.Fatal("expected pending result filter to use config redaction path")
	}
}
