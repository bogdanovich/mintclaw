package providers

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/auth"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestCheckModelRouteReadinessForConfiguredAPIRoutes(t *testing.T) {
	missing := &config.ModelConfig{Provider: "gemini", Model: "gemini-3-flash"}
	readiness := CheckModelRouteReadiness(missing)
	if readiness.Available || readiness.Reason != "API key or endpoint required" ||
		!strings.Contains(readiness.SetupHint, "api_keys") {
		t.Fatalf("missing Gemini setup readiness = %+v", readiness)
	}

	withKey := &config.ModelConfig{Provider: "gemini", Model: "gemini-3-flash"}
	withKey.SetAPIKey("secret-not-exposed")
	if readiness = CheckModelRouteReadiness(withKey); !readiness.Available || readiness.Reason != "" ||
		readiness.SetupHint != "" {
		t.Fatalf("keyed Gemini readiness = %+v", readiness)
	}

	customEndpoint := &config.ModelConfig{
		Provider: "openai", Model: "local-model", APIBase: "http://127.0.0.1:8080/v1",
	}
	if readiness = CheckModelRouteReadiness(customEndpoint); !readiness.Available {
		t.Fatalf("custom endpoint readiness = %+v", readiness)
	}

	localDefault := &config.ModelConfig{Provider: "ollama", Model: "qwen3"}
	if readiness = CheckModelRouteReadiness(localDefault); !readiness.Available {
		t.Fatalf("local default readiness = %+v", readiness)
	}
}

func TestCheckModelRouteReadinessForOAuth(t *testing.T) {
	originalGetCredential := getCredential
	t.Cleanup(func() { getCredential = originalGetCredential })

	model := &config.ModelConfig{Provider: "openai", Model: "gpt-5.6-sol", AuthMethod: "oauth"}
	getCredential = func(provider string) (*auth.AuthCredential, error) {
		if provider != "openai" {
			t.Fatalf("credential provider = %q, want openai", provider)
		}
		return nil, nil
	}
	readiness := CheckModelRouteReadiness(model)
	if readiness.Available || readiness.Reason != "Login required" ||
		!strings.Contains(readiness.SetupHint, "auth login --provider openai") {
		t.Fatalf("missing OAuth readiness = %+v", readiness)
	}

	getCredential = func(string) (*auth.AuthCredential, error) {
		return &auth.AuthCredential{
			AccessToken: "expired", ExpiresAt: time.Now().Add(-time.Hour),
		}, nil
	}
	if readiness = CheckModelRouteReadiness(model); readiness.Available || readiness.Reason != "Login expired" {
		t.Fatalf("expired OAuth readiness = %+v", readiness)
	}

	getCredential = func(string) (*auth.AuthCredential, error) {
		return &auth.AuthCredential{
			AccessToken: "refreshable", RefreshToken: "refresh", ExpiresAt: time.Now().Add(-time.Hour),
		}, nil
	}
	if readiness = CheckModelRouteReadiness(model); !readiness.Available {
		t.Fatalf("refreshable OAuth readiness = %+v", readiness)
	}

	getCredential = func(string) (*auth.AuthCredential, error) {
		return nil, errors.New("unreadable auth store")
	}
	if readiness = CheckModelRouteReadiness(model); readiness.Available ||
		readiness.Reason != "Authentication status unavailable" || strings.Contains(readiness.SetupHint, "unreadable") {
		t.Fatalf("unreadable OAuth readiness = %+v", readiness)
	}
}

func TestCheckModelRouteReadinessForLocalCLI(t *testing.T) {
	originalLookPath := modelRouteLookPath
	t.Cleanup(func() { modelRouteLookPath = originalLookPath })

	modelRouteLookPath = func(command string) (string, error) {
		if command != "codex" {
			t.Fatalf("command = %q, want codex", command)
		}
		return "", errors.New("not found")
	}
	model := &config.ModelConfig{Provider: "codex-cli", Model: "gpt-5.6-sol"}
	readiness := CheckModelRouteReadiness(model)
	if readiness.Available || readiness.Reason != "Codex CLI not found" ||
		!strings.Contains(readiness.SetupHint, "Install") {
		t.Fatalf("missing Codex CLI readiness = %+v", readiness)
	}

	modelRouteLookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	if readiness = CheckModelRouteReadiness(model); !readiness.Available {
		t.Fatalf("installed Codex CLI readiness = %+v", readiness)
	}
}

func TestCheckModelRouteReadinessRejectsUnsupportedCodingProvider(t *testing.T) {
	readiness := CheckModelRouteReadiness(&config.ModelConfig{
		Provider: "elevenlabs", Model: "scribe-v2",
	})
	if readiness.Available || readiness.Reason != "Unsupported coding provider" {
		t.Fatalf("unsupported coding provider readiness = %+v", readiness)
	}
}

func TestCheckModelRouteReadinessCoversEveryDefaultModelProvider(t *testing.T) {
	for provider, option := range modelProviderOptionsByName {
		if !option.DefaultModelAllowed {
			continue
		}
		readiness := CheckModelRouteReadiness(&config.ModelConfig{Provider: provider, Model: "test-model"})
		if readiness.Reason == "Unsupported coding provider" {
			t.Errorf("default model provider %q has no readiness policy", provider)
		}
	}
}
