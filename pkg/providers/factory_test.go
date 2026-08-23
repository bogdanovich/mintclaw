package providers

import (
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/auth"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestCreateProviderReturnsHTTPProviderForOpenRouter(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = "test-openrouter"
	modelCfg := &config.ModelConfig{
		ModelName: "test-openrouter", Provider: "openrouter", Model: "auto",
		APIBase: "https://openrouter.ai/api/v1", Enabled: true,
	}
	modelCfg.SetAPIKey("sk-or-test")
	cfg.ModelList = []*config.ModelConfig{modelCfg}

	provider, _, err := CreateProvider(cfg)
	if err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}

	if _, ok := provider.(*HTTPProvider); !ok {
		t.Fatalf("provider type = %T, want *HTTPProvider", provider)
	}
}

func TestCreateProviderReturnsCodexCliProviderForCodexCode(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = "test-codex"
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "test-codex", Provider: "codex-cli", Model: "codex-model",
			Workspace: "/tmp/workspace", Enabled: true,
		},
	}

	provider, _, err := CreateProvider(cfg)
	if err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}

	if _, ok := provider.(*CodexCliProvider); !ok {
		t.Fatalf("provider type = %T, want *CodexCliProvider", provider)
	}
}

func TestCreateProviderReturnsClaudeCliProviderForClaudeCli(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = "test-claude-cli"
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "test-claude-cli", Provider: "claude-cli", Model: "claude-sonnet",
			Workspace: "/tmp/workspace", Enabled: true,
		},
	}

	provider, _, err := CreateProvider(cfg)
	if err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}

	if _, ok := provider.(*ClaudeCliProvider); !ok {
		t.Fatalf("provider type = %T, want *ClaudeCliProvider", provider)
	}
}

func TestCreateProviderReturnsClaudeProviderForAnthropicOAuth(t *testing.T) {
	originalGetCredential := getCredential
	t.Cleanup(func() { getCredential = originalGetCredential })

	getCredential = func(provider string) (*auth.AuthCredential, error) {
		if provider != "anthropic" {
			t.Fatalf("provider = %q, want anthropic", provider)
		}
		return &auth.AuthCredential{
			AccessToken: "anthropic-token",
		}, nil
	}

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = "test-claude-oauth"
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "test-claude-oauth", Provider: "anthropic", Model: "claude-sonnet-4.6",
			AuthMethod: "oauth", Enabled: true,
		},
	}

	provider, _, err := CreateProvider(cfg)
	if err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}

	if _, ok := provider.(*ClaudeProvider); !ok {
		t.Fatalf("provider type = %T, want *ClaudeProvider", provider)
	}
	// TODO: Test custom APIBase when createClaudeAuthProvider supports it
}

func TestCreateImageGenerationProviderFromModelUsesCodexOAuth(t *testing.T) {
	originalGetCredential := getCredential
	t.Cleanup(func() { getCredential = originalGetCredential })

	getCredential = func(provider string) (*auth.AuthCredential, error) {
		if provider != "openai" {
			t.Fatalf("provider = %q, want openai", provider)
		}
		return &auth.AuthCredential{
			AccessToken: "openai-token",
			AccountID:   "acct-123",
		}, nil
	}

	for _, configuredModel := range []string{"openai/gpt-image-2", "openai-codex/gpt-image-2"} {
		t.Run(configuredModel, func(t *testing.T) {
			provider, model, err := CreateImageGenerationProviderFromModel(configuredModel)
			if err != nil {
				t.Fatalf("CreateImageGenerationProviderFromModel() error = %v", err)
			}
			if model != "gpt-image-2" {
				t.Fatalf("model = %q, want gpt-image-2", model)
			}
			if ImageCapabilities(provider).ProviderID != "openai-codex" {
				t.Fatalf("provider id = %q, want openai-codex", ImageCapabilities(provider).ProviderID)
			}
		})
	}
}

func TestCreateImageGenerationProviderFromModelRejectsKnownUnsupportedProvider(t *testing.T) {
	_, _, err := CreateImageGenerationProviderFromModel("anthropic/imagen")
	if err == nil || !strings.Contains(err.Error(), `provider "anthropic" does not support image generation`) {
		t.Fatalf("CreateImageGenerationProviderFromModel() error = %v, want unsupported-provider error", err)
	}
}

func TestSplitImageGenerationModelPreservesUnknownNamespacedModel(t *testing.T) {
	provider, model := splitImageGenerationModel("vendor/native/model")
	if provider != "openai" || model != "vendor/native/model" {
		t.Fatalf("splitImageGenerationModel() = (%q, %q), want (openai, vendor/native/model)", provider, model)
	}
}

func TestCreateProviderReturnsCodexProviderForOpenAIOAuth(t *testing.T) {
	// TODO: This test requires openai protocol to support auth_method: "oauth"
	// which is not yet implemented in the new factory_provider.go
	t.Skip("OpenAI OAuth via model_list not yet implemented")
}
