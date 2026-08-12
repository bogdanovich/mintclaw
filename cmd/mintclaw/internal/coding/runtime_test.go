package coding

import (
	"encoding/json"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestCodingRuntimeConfigIsolatesAgentContextAndSelection(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.Defaults.ContextManagerConfig = json.RawMessage(`{"dbPath":"/personal/context.db"}`)
	cfg.Agents.Defaults.ModelName = "default-model"
	cfg.Agents.Defaults.Provider = "personal-provider"
	cfg.Agents.List = []config.AgentConfig{{ID: "personal"}, {ID: "support"}}
	cfg.Agents.Dispatch = &config.DispatchConfig{}
	selected := &config.ModelConfig{
		ModelName: "coding-model",
		Provider:  "configured-provider",
		Model:     "configured-id",
		Enabled:   true,
		Fallbacks: []string{"fallback"},
	}
	fallback := &config.ModelConfig{
		ModelName: "fallback",
		Provider:  "fallback-provider",
		Model:     "fallback-id",
		Enabled:   true,
	}
	cfg.ModelList = config.SecureModelList{selected, fallback}

	runtimeCfg, modelName, providerName, err := codingRuntimeConfig(cfg, thread.Metadata{
		Model:    "coding-model",
		Provider: "configured-provider",
	})
	if err != nil {
		t.Fatal(err)
	}
	if modelName != "coding-model" || providerName != "configured-provider" {
		t.Fatalf("selection = model %q provider %q", modelName, providerName)
	}
	if runtimeCfg.Agents.Defaults.ContextManager != "seahorse" ||
		len(runtimeCfg.Agents.Defaults.ContextManagerConfig) != 0 {
		t.Fatalf(
			"coding context = %q config %s",
			runtimeCfg.Agents.Defaults.ContextManager,
			runtimeCfg.Agents.Defaults.ContextManagerConfig,
		)
	}
	if len(runtimeCfg.Agents.List) != 1 || runtimeCfg.Agents.List[0].ID != "main" ||
		runtimeCfg.Agents.Dispatch != nil {
		t.Fatalf("coding agents = %#v dispatch = %#v", runtimeCfg.Agents.List, runtimeCfg.Agents.Dispatch)
	}
	if len(runtimeCfg.ModelList) != 2 || runtimeCfg.ModelList[0].Provider != "configured-provider" {
		t.Fatalf("runtime models = %#v", runtimeCfg.ModelList)
	}
	if cfg.Agents.Defaults.ContextManager != "none" ||
		string(cfg.Agents.Defaults.ContextManagerConfig) != `{"dbPath":"/personal/context.db"}` ||
		selected.Provider != "configured-provider" ||
		cfg.Agents.List[0].ID != "personal" {
		t.Fatalf("source config was mutated: %#v %#v", cfg.Agents, selected)
	}
	runtimeCfg.ModelList[0].Fallbacks[0] = "changed"
	if selected.Fallbacks[0] != "fallback" {
		t.Fatal("runtime model slice aliases the source model")
	}
}

func TestCodingRuntimeConfigCanonicalizesInferredModelBeforePersistedProvider(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = config.SecureModelList{&config.ModelConfig{
		ModelName: "coding-model",
		Model:     "openai/gpt-4o",
		Enabled:   true,
	}}

	runtimeCfg, modelName, providerName, err := codingRuntimeConfig(cfg, thread.Metadata{
		Model:    "coding-model",
		Provider: "openai",
	})
	if err != nil {
		t.Fatal(err)
	}
	if modelName != "coding-model" || providerName != "openai" {
		t.Fatalf("selection = model %q provider %q", modelName, providerName)
	}
	if got := runtimeCfg.ModelList[0].Model; got != "gpt-4o" {
		t.Fatalf("canonical runtime model = %q, want gpt-4o", got)
	}
	if cfg.ModelList[0].Model != "openai/gpt-4o" {
		t.Fatalf("source model was mutated: %q", cfg.ModelList[0].Model)
	}
}

func TestCodingRuntimeConfigKeepsLoadBalancedAliasBoundToPersistedProvider(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = config.SecureModelList{
		&config.ModelConfig{
			ModelName: "balanced",
			Provider:  "openai",
			Model:     "gpt-4o",
			APIBase:   "https://openai.example.test",
			Enabled:   true,
		},
		&config.ModelConfig{
			ModelName: "balanced",
			Provider:  "anthropic",
			Model:     "claude-sonnet",
			APIBase:   "https://anthropic.example.test",
			Enabled:   true,
		},
	}

	runtimeCfg, _, providerName, err := codingRuntimeConfig(cfg, thread.Metadata{
		Model:    "balanced",
		Provider: "anthropic",
	})
	if err != nil {
		t.Fatal(err)
	}
	selected := runtimeCfg.ModelList[0]
	if providerName != "anthropic" || selected.Provider != "anthropic" ||
		selected.Model != "claude-sonnet" || selected.APIBase != "https://anthropic.example.test" {
		t.Fatalf("selected mismatched alias entry: provider=%q config=%#v", providerName, selected)
	}
}

func TestCodingRuntimeConfigPinsSameProviderAliasToFirstConfiguredEntry(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = config.SecureModelList{
		&config.ModelConfig{
			ModelName: "balanced",
			Provider:  "openai",
			Model:     "gpt-first",
			APIBase:   "https://first.example.test",
			Enabled:   true,
		},
		&config.ModelConfig{
			ModelName: "balanced",
			Provider:  "openai",
			Model:     "gpt-second",
			APIBase:   "https://second.example.test",
			Enabled:   true,
		},
	}

	for _, metadata := range []thread.Metadata{
		{Model: "balanced"},
		{Model: "balanced", Provider: "openai"},
	} {
		for attempt := 0; attempt < 4; attempt++ {
			runtimeCfg, _, providerName, err := codingRuntimeConfig(cfg, metadata)
			if err != nil {
				t.Fatal(err)
			}
			selected := runtimeCfg.ModelList[0]
			if providerName != "openai" || selected.Model != "gpt-first" ||
				selected.APIBase != "https://first.example.test" {
				t.Fatalf("attempt %d reconstructed %#v with provider %q", attempt, selected, providerName)
			}
		}
	}
}
