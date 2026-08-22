package agent

import (
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestPipelineModelResolutionUsesConfig(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{Provider: "openrouter"},
		},
		ModelList: []*config.ModelConfig{{
			ModelName: "kimi",
			Provider:  "openrouter",
			Model:     "moonshotai/kimi-k2",
		}},
	}
	pipeline := &Pipeline{Cfg: cfg}

	candidates := pipeline.modelCandidates("kimi", nil)
	if len(candidates) != 1 {
		t.Fatalf("modelCandidates() len = %d, want 1", len(candidates))
	}
	if candidates[0].Provider != "openrouter" || candidates[0].Model != "moonshotai/kimi-k2" {
		t.Fatalf("modelCandidates()[0] = %#v, want openrouter/moonshotai/kimi-k2", candidates[0])
	}

	active := pipeline.activeModelConfig("/workspace", candidates, "kimi")
	if active == nil {
		t.Fatal("activeModelConfig() = nil, want model config")
	}
	if active.ModelName != "kimi" || active.Workspace != "/workspace" {
		t.Fatalf("activeModelConfig() = %#v, want kimi config with workspace", active)
	}
}
