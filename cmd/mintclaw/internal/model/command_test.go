package model

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

var configPath = ""

func initTest(t *testing.T) {
	tmpDir := t.TempDir()
	configPath = filepath.Join(tmpDir, "config.json")
	_ = os.Setenv("MINTCLAW_CONFIG", configPath)
}

// captureStdout captures stdout during the execution of fn and returns the captured output
func captureStdout(fn func()) string {
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	fn()

	_ = w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}

func TestNewModelCommand(t *testing.T) {
	cmd := NewModelCommand()

	require.NotNil(t, cmd)

	assert.Equal(t, "model [model_name]", cmd.Use)
	assert.Equal(t, "Show or change the default model", cmd.Short)

	assert.Len(t, cmd.Aliases, 0)

	assert.False(t, cmd.HasFlags())

	assert.Nil(t, cmd.Run)
	assert.NotNil(t, cmd.RunE)

	assert.Nil(t, cmd.PersistentPreRunE)
	assert.Nil(t, cmd.PersistentPreRun)
	assert.Nil(t, cmd.PersistentPostRun)
}

func TestShowCurrentModel_WithDefaultModel(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				ModelName: "gpt-4",
			},
		},
		ModelList: []*config.ModelConfig{
			{
				ModelName: "gpt-4", Provider: "openai", Model: "gpt-4",
				APIKeys: config.SecureStrings{config.NewSecureString("test")},
				Enabled: true,
			},
			{
				ModelName: "claude-3", Provider: "anthropic", Model: "claude-3",
				APIKeys: config.SecureStrings{config.NewSecureString("test")},
				Enabled: true,
			},
		},
	}

	output := captureStdout(func() {
		showCurrentModel(cfg)
	})

	assert.Contains(t, output, "Current default model: gpt-4")
	assert.Contains(t, output, "Available models in your config:")
	assert.Contains(t, output, "gpt-4")
	assert.Contains(t, output, "claude-3")
}

func TestShowCurrentModel_NoDefaultModel(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				ModelName: "",
			},
		},
		ModelList: []*config.ModelConfig{
			{
				ModelName: "gpt-4", Provider: "openai", Model: "gpt-4",
				APIKeys: config.SecureStrings{config.NewSecureString("test")},
				Enabled: true,
			},
		},
	}

	output := captureStdout(func() {
		showCurrentModel(cfg)
	})

	assert.Contains(t, output, "No default model is currently set.")
	assert.Contains(t, output, "Available models in your config:")
}

func TestListAvailableModels_Empty(t *testing.T) {
	cfg := &config.Config{
		ModelList: []*config.ModelConfig{},
	}

	output := captureStdout(func() {
		listAvailableModels(cfg)
	})

	assert.Contains(t, output, "No models configured in model_list")
}

func TestListAvailableModels_WithModels(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				ModelName: "gpt-4",
			},
		},
		ModelList: []*config.ModelConfig{
			{
				ModelName: "gpt-4", Provider: "openai", Model: "gpt-4",
				APIKeys: config.SecureStrings{config.NewSecureString("test")},
				Enabled: true,
			},
			{
				ModelName: "claude-3", Provider: "anthropic", Model: "claude-3",
				APIKeys: config.SecureStrings{config.NewSecureString("test")},
				Enabled: true,
			},
			{ModelName: "no-key-model", Provider: "openai", Model: "test"},
		},
	}

	output := captureStdout(func() {
		listAvailableModels(cfg)
	})

	assert.NotEmpty(t, output)
	assert.Contains(t, output, "> - gpt-4 (provider=openai, model=gpt-4)")
	assert.Contains(t, output, "claude-3 (provider=anthropic, model=claude-3)")
	assert.NotContains(t, output, "no-key-model")
}

func TestSetDefaultModel_ValidModel(t *testing.T) {
	initTest(t)

	cfg := &config.Config{
		Version: config.CurrentVersion,
		Agents: config.AgentsConfig{
			List: []config.AgentConfig{config.DefaultAgentConfig()},
			Defaults: config.AgentDefaults{
				ModelName: "old-model",
			},
		},
		ModelList: []*config.ModelConfig{
			{
				ModelName: "new-model", Provider: "openai", Model: "new-model",
				APIKeys: config.SecureStrings{config.NewSecureString("test")},
				Enabled: true,
			},
			{
				ModelName: "old-model", Provider: "openai", Model: "old-model",
				APIKeys: config.SecureStrings{config.NewSecureString("test")},
				Enabled: true,
			},
		},
	}
	require.NoError(t, config.SaveConfig(configPath, cfg))

	output := captureStdout(func() {
		err := setDefaultModel(configPath, "new-model")
		assert.NoError(t, err)
	})

	assert.Contains(t, output, "Default model changed from 'old-model' to 'new-model'")

	// Verify config was updated
	updatedCfg, err := config.LoadConfig(configPath)
	require.NoError(t, err)
	assert.Equal(t, "new-model", updatedCfg.Agents.Defaults.ModelName)
}

func TestSetDefaultModel_PreservesConcurrentConfigChange(t *testing.T) {
	initTest(t)
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{
		{ModelName: "old-model", Provider: "openai", Model: "old", Enabled: true},
		{ModelName: "new-model", Provider: "openai", Model: "new", Enabled: true},
	}
	cfg.Agents.Defaults.ModelName = "old-model"
	repository := config.NewRepository(configPath)
	if _, err := repository.Save(cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if _, err := repository.Update(func(current *config.Config) error {
		current.Gateway.Port = 23456
		return nil
	}); err != nil {
		t.Fatalf("concurrent Update() error = %v", err)
	}
	if err := setDefaultModel(configPath, "new-model"); err != nil {
		t.Fatalf("setDefaultModel() error = %v", err)
	}

	current, err := repository.ReadOnly()
	if err != nil {
		t.Fatalf("ReadOnly() error = %v", err)
	}
	if current.Config.Gateway.Port != 23456 {
		t.Fatalf("gateway.port = %d, want concurrent value 23456", current.Config.Gateway.Port)
	}
	if current.Config.Agents.Defaults.ModelName != "new-model" {
		t.Fatalf("default model = %q, want new-model", current.Config.Agents.Defaults.ModelName)
	}
}

func TestSetDefaultModel_InvalidModel(t *testing.T) {
	initTest(t)

	cfg := &config.Config{
		Version: config.CurrentVersion,
		Agents: config.AgentsConfig{
			List: []config.AgentConfig{config.DefaultAgentConfig()},
			Defaults: config.AgentDefaults{
				ModelName: "existing-model",
			},
		},
		ModelList: []*config.ModelConfig{
			{
				ModelName: "existing-model", Provider: "openai", Model: "existing",
				APIKeys: config.SecureStrings{config.NewSecureString("test")},
				Enabled: true,
			},
		},
	}
	require.NoError(t, config.SaveConfig(configPath, cfg))

	err := setDefaultModel(configPath, "nonexistent-model")
	assert.EqualError(t, err, "cannot found model 'nonexistent-model' in config")
}

func TestSetDefaultModel_RejectsUnconfiguredLocalModel(t *testing.T) {
	initTest(t)

	cfg := &config.Config{
		Version: config.CurrentVersion,
		Agents:  config.AgentsConfig{List: []config.AgentConfig{config.DefaultAgentConfig()}},
		ModelList: []*config.ModelConfig{{
			ModelName: "existing-model",
			Provider:  "openai",
			Model:     "existing",
			Enabled:   true,
		}},
	}
	require.NoError(t, config.SaveConfig(configPath, cfg))

	err := setDefaultModel(configPath, "local-model")
	assert.EqualError(t, err, "cannot found model 'local-model' in config")

	updated, loadErr := config.LoadConfig(configPath)
	require.NoError(t, loadErr)
	assert.Empty(t, updated.Agents.Defaults.ModelName)
}

func TestSetDefaultModel_ModelWithoutAPIKey(t *testing.T) {
	initTest(t)

	cfg := &config.Config{
		Version: config.CurrentVersion,
		Agents: config.AgentsConfig{
			List: []config.AgentConfig{config.DefaultAgentConfig()},
			Defaults: config.AgentDefaults{
				ModelName: "existing-model",
			},
		},
		ModelList: []*config.ModelConfig{
			{
				ModelName: "existing-model", Provider: "openai", Model: "existing",
				APIKeys: config.SecureStrings{config.NewSecureString("test")},
				Enabled: true,
			},
			{ModelName: "no-key-model", Provider: "openai", Model: "nokey"},
		},
	}
	require.NoError(t, config.SaveConfig(configPath, cfg))

	assert.Error(t, setDefaultModel(configPath, "no-key-model"))
}

func TestSetDefaultModel_SaveConfigError(t *testing.T) {
	// Use an invalid path to trigger save error
	invalidPath := "/nonexistent/directory/config.json"

	err := setDefaultModel(invalidPath, "new-model")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to save config")
}

func TestFormatModelName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"empty string", "", "(none)"},
		{"simple model", "gpt-4", "gpt-4"},
		{"model with version", "claude-sonnet-4.6", "claude-sonnet-4.6"},
		{"model with spaces", "my model", "my model"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatModelName(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestModelCommandExecution_Show(t *testing.T) {
	initTest(t)

	// Create a test config
	cfg := &config.Config{
		Version: config.CurrentVersion,
		Agents: config.AgentsConfig{
			List: []config.AgentConfig{config.DefaultAgentConfig()},
			Defaults: config.AgentDefaults{
				ModelName: "test-model",
			},
		},
		ModelList: []*config.ModelConfig{
			{
				ModelName: "test-model", Provider: "openai", Model: "test",
				APIKeys: config.SecureStrings{config.NewSecureString("test")},
				Enabled: true,
			},
		},
	}

	err := config.SaveConfig(configPath, cfg)
	require.NoError(t, err)

	cmd := NewModelCommand()

	output := captureStdout(func() {
		err = cmd.RunE(cmd, []string{})
		assert.NoError(t, err)
	})

	assert.Contains(t, output, "Current default model: test-model")
}

func TestModelCommandExecution_Set(t *testing.T) {
	initTest(t)

	cfg := &config.Config{
		Version: config.CurrentVersion,
		Agents: config.AgentsConfig{
			List: []config.AgentConfig{config.DefaultAgentConfig()},
			Defaults: config.AgentDefaults{
				ModelName: "old-model",
			},
		},
		ModelList: []*config.ModelConfig{
			{
				ModelName: "old-model", Provider: "openai", Model: "old",
				APIKeys: config.SecureStrings{config.NewSecureString("test")},
				Enabled: true,
			},
			{
				ModelName: "new-model", Provider: "openai", Model: "new",
				APIKeys: config.SecureStrings{config.NewSecureString("test")},
				Enabled: true,
			},
		},
	}

	err := config.SaveConfig(configPath, cfg)
	require.NoError(t, err)

	cmd := NewModelCommand()

	output := captureStdout(func() {
		err = cmd.RunE(cmd, []string{"new-model"})
		assert.NoError(t, err)
	})

	assert.Contains(t, output, "Default model changed from 'old-model' to 'new-model'")
}

func TestModelCommandExecution_TooManyArgs(t *testing.T) {
	cmd := NewModelCommand()

	err := cmd.RunE(cmd, []string{"model1", "model2"})

	assert.Error(t, err)
}

func TestListAvailableModels_MarkerLogic(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				ModelName: "middle-model",
			},
		},
		ModelList: []*config.ModelConfig{
			{
				ModelName: "first-model", Provider: "openai", Model: "first",
				APIKeys: config.SecureStrings{config.NewSecureString("test")},
				Enabled: true,
			},
			{
				ModelName: "middle-model", Provider: "openai", Model: "middle",
				APIKeys: config.SecureStrings{config.NewSecureString("test")},
				Enabled: true,
			},
			{
				ModelName: "last-model", Provider: "openai", Model: "last",
				APIKeys: config.SecureStrings{config.NewSecureString("test")},
				Enabled: true,
			},
		},
	}

	output := captureStdout(func() {
		listAvailableModels(cfg)
	})

	assert.Contains(t, output, "  - first-model (provider=openai, model=first)")
	assert.Contains(t, output, "> - middle-model (provider=openai, model=middle)")
	assert.Contains(t, output, "  - last-model (provider=openai, model=last)")
}
