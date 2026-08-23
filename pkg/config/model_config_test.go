// MintClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestGetModelConfig_Found(t *testing.T) {
	cfg := &Config{
		Version: CurrentVersion,
		ModelList: []*ModelConfig{
			{
				ModelName: "test-model", Provider: "openai", Model: "gpt-4o",
				APIKeys: SimpleSecureStrings("key1"), Enabled: true,
			},
			{
				ModelName: "other-model", Provider: "anthropic", Model: "claude",
				APIKeys: SimpleSecureStrings("key2"), Enabled: true,
			},
		},
	}

	result, err := cfg.GetModelConfig("test-model")
	if err != nil {
		t.Fatalf("GetModelConfig() error = %v", err)
	}
	if result.Model != "gpt-4o" {
		t.Errorf("Model = %q, want %q", result.Model, "gpt-4o")
	}
}

func TestGetModelConfig_RequiresExplicitEnabled(t *testing.T) {
	cfg := &Config{ModelList: []*ModelConfig{
		{
			ModelName: "key-only", Provider: "openai", Model: "gpt-5.4",
			APIKeys: SimpleSecureStrings("key"),
		},
		{ModelName: "local-model", Provider: "openai", Model: "local-model"},
	}}

	for _, modelName := range []string{"key-only", "local-model"} {
		if _, err := cfg.GetModelConfig(modelName); err == nil {
			t.Fatalf("GetModelConfig(%q) resolved a disabled model", modelName)
		}
	}
}

func TestGetModelConfig_NotFound(t *testing.T) {
	cfg := &Config{
		ModelList: []*ModelConfig{
			{ModelName: "test-model", Provider: "openai", Model: "gpt-4o", APIKeys: SimpleSecureStrings("key1")},
		},
	}

	_, err := cfg.GetModelConfig("nonexistent")
	if err == nil {
		t.Fatal("GetModelConfig() expected error for nonexistent model")
	}
}

func TestGetModelConfig_EmptyList(t *testing.T) {
	cfg := &Config{
		ModelList: []*ModelConfig{},
	}

	_, err := cfg.GetModelConfig("any-model")
	if err == nil {
		t.Fatal("GetModelConfig() expected error for empty model list")
	}
}

func TestGetModelConfig_RoundRobin(t *testing.T) {
	cfg := &Config{
		ModelList: []*ModelConfig{
			{
				ModelName: "lb-model", Provider: "openai", Model: "gpt-4o-1",
				APIKeys: SimpleSecureStrings("key1"), Enabled: true,
			},
			{
				ModelName: "lb-model", Provider: "openai", Model: "gpt-4o-2",
				APIKeys: SimpleSecureStrings("key2"), Enabled: true,
			},
			{
				ModelName: "lb-model", Provider: "openai", Model: "gpt-4o-3",
				APIKeys: SimpleSecureStrings("key3"), Enabled: true,
			},
		},
	}

	// Test round-robin distribution
	results := make(map[string]int)
	for range 30 {
		result, err := cfg.GetModelConfig("lb-model")
		if err != nil {
			t.Fatalf("GetModelConfig() error = %v", err)
		}
		results[result.Model]++
	}

	// Each model should appear roughly 10 times (30 calls / 3 models)
	for model, count := range results {
		if count < 5 || count > 15 {
			t.Errorf("Model %s appeared %d times, expected ~10", model, count)
		}
	}
}

func TestGetModelConfig_RoundRobinStartsFromFirstMatch(t *testing.T) {
	rrCounter.Store(0)

	cfg := &Config{
		ModelList: []*ModelConfig{
			{
				ModelName: "lb-model", Provider: "openai", Model: "gpt-4o-1",
				APIKeys: SimpleSecureStrings("key1"), Enabled: true,
			},
			{
				ModelName: "lb-model", Provider: "openai", Model: "gpt-4o-2",
				APIKeys: SimpleSecureStrings("key2"), Enabled: true,
			},
			{
				ModelName: "lb-model", Provider: "openai", Model: "gpt-4o-3",
				APIKeys: SimpleSecureStrings("key3"), Enabled: true,
			},
		},
	}

	wantOrder := []string{
		"gpt-4o-1",
		"gpt-4o-2",
		"gpt-4o-3",
		"gpt-4o-1",
		"gpt-4o-2",
	}

	for i, want := range wantOrder {
		result, err := cfg.GetModelConfig("lb-model")
		if err != nil {
			t.Fatalf("GetModelConfig() call %d error = %v", i, err)
		}
		if result.Model != want {
			t.Fatalf("GetModelConfig() call %d model = %q, want %q", i, result.Model, want)
		}
	}
}

func TestGetModelConfig_Concurrent(t *testing.T) {
	cfg := &Config{
		ModelList: []*ModelConfig{
			{
				ModelName: "concurrent-model",
				Provider:  "openai",
				Model:     "gpt-4o-1",
				APIKeys:   SimpleSecureStrings("key1"),
				Enabled:   true,
			},
			{
				ModelName: "concurrent-model",
				Provider:  "openai",
				Model:     "gpt-4o-2",
				APIKeys:   SimpleSecureStrings("key2"),
				Enabled:   true,
			},
		},
	}

	const goroutines = 100
	const iterations = 10

	var wg sync.WaitGroup
	errors := make(chan error, goroutines*iterations)

	for range goroutines {
		wg.Go(func() {
			for range iterations {
				_, err := cfg.GetModelConfig("concurrent-model")
				if err != nil {
					errors <- err
				}
			}
		})
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Errorf("Concurrent GetModelConfig() error: %v", err)
	}
}

func TestModelConfig_StreamingConfig(t *testing.T) {
	t.Run("loads streaming enabled", func(t *testing.T) {
		var cfg ModelConfig
		err := json.Unmarshal([]byte(`{
			"model_name": "stream-model",
			"model": "openai/gpt-5.4",
			"streaming": {"enabled": true}
		}`), &cfg)
		if err != nil {
			t.Fatalf("Unmarshal() error = %v", err)
		}
		if !cfg.Streaming.Enabled {
			t.Fatal("Streaming.Enabled = false, want true")
		}
	})

	t.Run("defaults disabled", func(t *testing.T) {
		var cfg ModelConfig
		err := json.Unmarshal([]byte(`{
			"model_name": "plain-model",
			"model": "openai/gpt-5.4"
		}`), &cfg)
		if err != nil {
			t.Fatalf("Unmarshal() error = %v", err)
		}
		if cfg.Streaming.Enabled {
			t.Fatal("Streaming.Enabled = true, want false by default")
		}
	})

	t.Run("model streaming only has enabled", func(t *testing.T) {
		typ := reflect.TypeOf(ModelStreamingConfig{})
		if typ.NumField() != 1 {
			t.Fatalf("ModelStreamingConfig field count = %d, want 1", typ.NumField())
		}
		if _, ok := typ.FieldByName("Enabled"); !ok {
			t.Fatal("ModelStreamingConfig missing Enabled field")
		}
	})
}

func TestModelConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  ModelConfig
		wantErr bool
	}{
		{
			name: "valid config",
			config: ModelConfig{
				ModelName: "test", Provider: "openai", Model: "gpt-4o",
			},
			wantErr: false,
		},
		{
			name: "valid tool schema transform",
			config: ModelConfig{
				ModelName: "test", Provider: "openai", Model: "gpt-4o",
				ToolSchemaTransform: "simple",
			},
			wantErr: false,
		},
		{
			name:    "missing model_name",
			config:  ModelConfig{Provider: "openai", Model: "gpt-4o"},
			wantErr: true,
		},
		{
			name: "missing model",
			config: ModelConfig{
				ModelName: "test", Provider: "openai",
			},
			wantErr: true,
		},
		{
			name:    "missing provider",
			config:  ModelConfig{ModelName: "test", Model: "gpt-4o"},
			wantErr: true,
		},
		{
			name: "provider with surrounding whitespace",
			config: ModelConfig{
				ModelName: "test", Provider: " openai ", Model: "gpt-4o",
			},
			wantErr: true,
		},
		{
			name: "model_name with surrounding whitespace",
			config: ModelConfig{
				ModelName: " test ", Provider: "openai", Model: "gpt-4o",
			},
			wantErr: true,
		},
		{
			name:    "empty config",
			config:  ModelConfig{},
			wantErr: true,
		},
		{
			name: "invalid tool schema transform",
			config: ModelConfig{
				ModelName: "test", Provider: "openai", Model: "gpt-4o",
				ToolSchemaTransform: "invalid",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestModelConfig_MarshalAlwaysIncludesEnabled(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		data, err := json.Marshal(ModelConfig{
			ModelName: "test",
			Provider:  "openai",
			Model:     "gpt-5.4",
			Enabled:   enabled,
		})
		if err != nil {
			t.Fatalf("Marshal() error = %v", err)
		}
		want := `"enabled":false`
		if enabled {
			want = `"enabled":true`
		}
		if !strings.Contains(string(data), want) {
			t.Fatalf("Marshal() = %s, want %s", data, want)
		}
	}
}

func TestModelConfig_VisionCapabilities(t *testing.T) {
	var cfg ModelConfig
	err := json.Unmarshal([]byte(`{
		"model_name": "deepseek-main",
		"model": "openrouter/deepseek/deepseek-chat",
		"capabilities": {
			"vision": {
				"model": "openai/gpt-4.1-mini",
				"fallbacks": ["anthropic/claude-sonnet-4"]
			}
		}
	}`), &cfg)
	if err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if cfg.Capabilities == nil || cfg.Capabilities.Vision == nil {
		t.Fatal("expected vision capabilities to be populated")
	}
	if got := cfg.Capabilities.Vision.Model; got != "openai/gpt-4.1-mini" {
		t.Fatalf("Vision.Model = %q, want %q", got, "openai/gpt-4.1-mini")
	}
	if len(cfg.Capabilities.Vision.Fallbacks) != 1 ||
		cfg.Capabilities.Vision.Fallbacks[0] != "anthropic/claude-sonnet-4" {
		t.Fatalf("Vision.Fallbacks = %#v", cfg.Capabilities.Vision.Fallbacks)
	}
}

func TestConfig_ValidateModelList(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
		errMsg  string // partial error message to check
	}{
		{
			name: "valid list",
			config: &Config{
				ModelList: []*ModelConfig{
					{ModelName: "test1", Provider: "openai", Model: "gpt-4o"},
					{ModelName: "test2", Provider: "anthropic", Model: "claude"},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid entry",
			config: &Config{
				ModelList: []*ModelConfig{
					{ModelName: "test1", Provider: "openai", Model: "gpt-4o"},
					{ModelName: "", Provider: "anthropic", Model: "claude"}, // missing model_name
				},
			},
			wantErr: true,
			errMsg:  "model_name is required",
		},
		{
			name: "empty list",
			config: &Config{
				ModelList: []*ModelConfig{},
			},
			wantErr: false,
		},
		{
			// Load balancing: multiple entries with same model_name are allowed
			name: "duplicate model_name for load balancing",
			config: &Config{
				ModelList: []*ModelConfig{},
			},
			wantErr: false, // Changed: duplicates are allowed for load balancing
		},
		{
			// Load balancing: non-adjacent entries with same model_name are also allowed
			name: "duplicate model_name non-adjacent for load balancing",
			config: &Config{
				ModelList: []*ModelConfig{
					{ModelName: "model-a", Provider: "openai", Model: "gpt-4o"},
					{ModelName: "model-b", Provider: "anthropic", Model: "claude"},
					{ModelName: "model-a", Provider: "openai", Model: "gpt-4-turbo"},
				},
			},
			wantErr: false, // Changed: duplicates are allowed for load balancing
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.ValidateModelList()
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateModelList() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && tt.errMsg != "" {
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("ValidateModelList() error = %v, want error containing %q", err, tt.errMsg)
				}
			}
		})
	}
}

func TestConfig_ValidateModelReferences(t *testing.T) {
	newConfig := func() *Config {
		return &Config{
			ModelList: []*ModelConfig{
				{ModelName: "primary", Provider: "openai", Model: "gpt-5.4", Enabled: true},
				{ModelName: "fallback", Provider: "anthropic", Model: "claude-sonnet-4-6", Enabled: true},
				{ModelName: "provider/native", Provider: "nvidia", Model: "z-ai/glm-5.1", Enabled: true},
			},
		}
	}

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name: "all selectors use exact model names",
			mutate: func(cfg *Config) {
				cfg.ModelList[0].Fallbacks = []string{"fallback"}
				cfg.ModelList[0].Capabilities = &ModelCapabilities{Vision: &ModelCapabilityOverride{
					Model:     "provider/native",
					Fallbacks: []string{"fallback"},
				}}
				cfg.Agents.Defaults.ModelName = "primary"
				cfg.Agents.Defaults.ModelFallbacks = []string{"fallback"}
				cfg.Agents.Defaults.Routing = &RoutingConfig{LightModel: "provider/native"}
				cfg.Agents.Defaults.Subagents = &SubagentsConfig{Model: &AgentModelConfig{
					Primary:   "fallback",
					Fallbacks: []string{"primary"},
				}}
				cfg.Agents.List = []AgentConfig{{
					Model: &AgentModelConfig{Primary: "provider/native", Fallbacks: []string{"fallback"}},
					Subagents: &SubagentsConfig{Model: &AgentModelConfig{
						Primary:   "primary",
						Fallbacks: []string{"provider/native"},
					}},
				}}
				cfg.Voice.ModelName = "provider/native"
				cfg.Voice.TTSModelName = "fallback"
			},
		},
		{
			name: "load balanced aliases remain valid",
			mutate: func(cfg *Config) {
				cfg.ModelList = append(
					cfg.ModelList,
					&ModelConfig{ModelName: "primary", Provider: "openai", Model: "gpt-5.4-mini", Enabled: true},
				)
				cfg.Agents.Defaults.ModelName = "primary"
			},
		},
		{
			name: "disabled model",
			mutate: func(cfg *Config) {
				cfg.ModelList[0].Enabled = false
				cfg.Agents.Defaults.ModelName = "primary"
			},
			wantErr: `agents.defaults.model_name references unknown or disabled model_name "primary"`,
		},
		{
			name: "default model",
			mutate: func(cfg *Config) {
				cfg.Agents.Defaults.ModelName = "openai/gpt-5.4"
			},
			wantErr: `agents.defaults.model_name references unknown or disabled model_name "openai/gpt-5.4"`,
		},
		{
			name: "default fallback",
			mutate: func(cfg *Config) {
				cfg.Agents.Defaults.ModelFallbacks = []string{""}
			},
			wantErr: "agents.defaults.model_fallbacks[0] must not be empty",
		},
		{
			name: "model fallback",
			mutate: func(cfg *Config) {
				cfg.ModelList[0].Fallbacks = []string{"openai/gpt-5.4"}
			},
			wantErr: `model_list[0].fallbacks[0] references unknown or disabled model_name "openai/gpt-5.4"`,
		},
		{
			name: "vision model",
			mutate: func(cfg *Config) {
				cfg.ModelList[0].Capabilities = &ModelCapabilities{Vision: &ModelCapabilityOverride{Model: "unknown"}}
			},
			wantErr: `model_list[0].capabilities.vision.model references unknown or disabled model_name "unknown"`,
		},
		{
			name: "vision fallback",
			mutate: func(cfg *Config) {
				cfg.ModelList[0].Capabilities = &ModelCapabilities{Vision: &ModelCapabilityOverride{
					Fallbacks: []string{"unknown"},
				}}
			},
			wantErr: `model_list[0].capabilities.vision.fallbacks[0] references unknown or disabled model_name "unknown"`,
		},
		{
			name: "routing light model",
			mutate: func(cfg *Config) {
				cfg.Agents.Defaults.Routing = &RoutingConfig{LightModel: "unknown"}
			},
			wantErr: `agents.defaults.routing.light_model references unknown or disabled model_name "unknown"`,
		},
		{
			name: "default subagent model",
			mutate: func(cfg *Config) {
				cfg.Agents.Defaults.Subagents = &SubagentsConfig{Model: &AgentModelConfig{Primary: "unknown"}}
			},
			wantErr: `agents.defaults.subagents.model.primary references unknown or disabled model_name "unknown"`,
		},
		{
			name: "default subagent fallback",
			mutate: func(cfg *Config) {
				cfg.Agents.Defaults.Subagents = &SubagentsConfig{Model: &AgentModelConfig{
					Fallbacks: []string{"unknown"},
				}}
			},
			wantErr: `agents.defaults.subagents.model.fallbacks[0] references unknown or disabled model_name "unknown"`,
		},
		{
			name: "agent model",
			mutate: func(cfg *Config) {
				cfg.Agents.List = []AgentConfig{{Model: &AgentModelConfig{Primary: "unknown"}}}
			},
			wantErr: `agents.list[0].model.primary references unknown or disabled model_name "unknown"`,
		},
		{
			name: "agent fallback",
			mutate: func(cfg *Config) {
				cfg.Agents.List = []AgentConfig{{Model: &AgentModelConfig{Fallbacks: []string{"unknown"}}}}
			},
			wantErr: `agents.list[0].model.fallbacks[0] references unknown or disabled model_name "unknown"`,
		},
		{
			name: "agent subagent model",
			mutate: func(cfg *Config) {
				cfg.Agents.List = []AgentConfig{{
					Subagents: &SubagentsConfig{Model: &AgentModelConfig{Primary: "unknown"}},
				}}
			},
			wantErr: `agents.list[0].subagents.model.primary references unknown or disabled model_name "unknown"`,
		},
		{
			name: "agent subagent fallback",
			mutate: func(cfg *Config) {
				cfg.Agents.List = []AgentConfig{{
					Subagents: &SubagentsConfig{Model: &AgentModelConfig{Fallbacks: []string{"unknown"}}},
				}}
			},
			wantErr: `agents.list[0].subagents.model.fallbacks[0] references unknown or disabled model_name "unknown"`,
		},
		{
			name: "voice model",
			mutate: func(cfg *Config) {
				cfg.Voice.ModelName = "unknown"
			},
			wantErr: `voice.model_name references unknown or disabled model_name "unknown"`,
		},
		{
			name: "voice tts model",
			mutate: func(cfg *Config) {
				cfg.Voice.TTSModelName = "unknown"
			},
			wantErr: `voice.tts_model_name references unknown or disabled model_name "unknown"`,
		},
		{
			name: "surrounding whitespace is rejected",
			mutate: func(cfg *Config) {
				cfg.Agents.Defaults.ModelName = " primary "
			},
			wantErr: "agents.defaults.model_name must not have surrounding whitespace",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := newConfig()
			tt.mutate(cfg)
			err := cfg.ValidateModelReferences()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateModelReferences() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateModelReferences() error = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadConfigRejectsUnknownModelReferenceWithoutRewriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	before := []byte(`{
		"version": 3,
		"agents": {"defaults": {"model_name": "openai/gpt-5.4"}},
		"model_list": [{"model_name": "primary", "provider": "openai", "model": "gpt-5.4"}]
	}`)
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(),
		`agents.defaults.model_name references unknown or disabled model_name "openai/gpt-5.4"`) {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("ReadFile() error = %v", readErr)
	}
	if string(after) != string(before) {
		t.Fatalf("LoadConfig() rewrote rejected config:\n%s", after)
	}
}

func TestModelConfig_RequestTimeoutParsing(t *testing.T) {
	jsonData := `{
		"model_name": "slow-local",
		"model": "openai/local-model",
		"api_base": "http://localhost:11434/v1",
		"request_timeout": 300
	}`

	var cfg ModelConfig
	if err := json.Unmarshal([]byte(jsonData), &cfg); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if cfg.RequestTimeout != 300 {
		t.Fatalf("RequestTimeout = %d, want 300", cfg.RequestTimeout)
	}
}

func TestModelConfig_RequestTimeoutDefaultZeroValue(t *testing.T) {
	jsonData := `{
		"model_name": "default-timeout",
		"model": "openai/gpt-4o",
		"api_key": "test-key"
	}`

	var cfg ModelConfig
	if err := json.Unmarshal([]byte(jsonData), &cfg); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if cfg.RequestTimeout != 0 {
		t.Fatalf("RequestTimeout = %d, want 0", cfg.RequestTimeout)
	}
}
