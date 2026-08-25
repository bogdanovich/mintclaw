package agent

import (
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func TestModelAliasFromCandidateIdentityKey(t *testing.T) {
	if got := modelAliasFromCandidateIdentityKey("model_name:primary"); got != "primary" {
		t.Fatalf("modelAliasFromCandidateIdentityKey() = %q, want %q", got, "primary")
	}
	if got := modelAliasFromCandidateIdentityKey("openai/gpt-5.4"); got != "" {
		t.Fatalf("modelAliasFromCandidateIdentityKey() = %q, want empty", got)
	}
}

func TestResolveModelCandidateRequiresExactModelName(t *testing.T) {
	cfg := &config.Config{ModelList: []*config.ModelConfig{{
		ModelName: "primary",
		Provider:  "openai",
		Model:     "gpt-5.4",
		Enabled:   true,
	}}}

	candidate, ok := resolveModelCandidate(cfg, "primary")
	if !ok {
		t.Fatal("resolveModelCandidate() did not resolve exact model_name")
	}
	if candidate.IdentityKey != "model_name:primary" {
		t.Fatalf("identity key = %q, want %q", candidate.IdentityKey, "model_name:primary")
	}

	for _, ref := range []string{"gpt-5.4", "openai/gpt-5.4"} {
		if candidate, ok = resolveModelCandidate(cfg, ref); ok {
			t.Fatalf("resolveModelCandidate(%q) = %#v, want no match", ref, candidate)
		}
	}
}

func TestResolvedCandidateModelName_PrefersIdentityAlias(t *testing.T) {
	got := resolvedCandidateModelName([]providers.FallbackCandidate{
		{Provider: "openai", Model: "gpt-5.4", IdentityKey: "model_name:primary"},
	}, "fallback-model")
	if got != "primary" {
		t.Fatalf("resolvedCandidateModelName() = %q, want %q", got, "primary")
	}
}

func TestResolvedCandidateModelName_DoesNotScanFallbackAliases(t *testing.T) {
	got := resolvedCandidateModelName([]providers.FallbackCandidate{
		{Provider: "openai", Model: "gpt-5.4"},
		{Provider: "openai", Model: "gpt-5.4-mini", IdentityKey: "model_name:fallback"},
	}, "primary-model")
	if got != "primary-model" {
		t.Fatalf("resolvedCandidateModelName() = %q, want %q", got, "primary-model")
	}
}

func TestResolvedCandidateModelName_UsesCandidateDisplayName(t *testing.T) {
	got := resolvedCandidateModelName([]providers.FallbackCandidate{
		{Provider: "openai", Model: "gpt-5.4", DisplayName: "gpt-5.4-display"},
	}, "fallback-model")
	if got != "gpt-5.4-display" {
		t.Fatalf("resolvedCandidateModelName() = %q, want %q", got, "gpt-5.4-display")
	}
}

func TestResolveActiveModelConfig_PrefersCandidateIdentityKey(t *testing.T) {
	cfg := &config.Config{
		ModelList: []*config.ModelConfig{
			{
				ModelName: "glm-4.7",
				Provider:  "zhipu",
				Model:     "glm-4.7",
				Enabled:   true,
				Streaming: config.ModelStreamingConfig{Enabled: false},
			},
			{
				ModelName: "suanneng-glm-4.7",
				Provider:  "zhipu",
				Model:     "glm-4.7",
				Enabled:   true,
				Streaming: config.ModelStreamingConfig{Enabled: true},
			},
		},
	}

	got := resolveActiveModelConfig(
		cfg,
		"/workspace",
		[]providers.FallbackCandidate{{
			Provider:    "zhipu",
			Model:       "glm-4.7",
			IdentityKey: "model_name:suanneng-glm-4.7",
		}},
		"glm-4.7",
	)

	if got == nil {
		t.Fatal("resolveActiveModelConfig() = nil, want model config")
	}
	if got.ModelName != "suanneng-glm-4.7" {
		t.Fatalf("model_name = %q, want %q", got.ModelName, "suanneng-glm-4.7")
	}
	if !got.Streaming.Enabled {
		t.Fatal("streaming.enabled = false, want true from identity-matched model config")
	}
}

func TestResolveActiveModelConfig_LoadBalancedAliasUsesSelectedCandidate(t *testing.T) {
	cfg := &config.Config{
		ModelList: []*config.ModelConfig{
			{
				ModelName: "lb-model", Provider: "openai", Model: "primary",
				Enabled:   true,
				Streaming: config.ModelStreamingConfig{Enabled: false},
			},
			{
				ModelName: "lb-model", Provider: "openai", Model: "secondary",
				Enabled:   true,
				Streaming: config.ModelStreamingConfig{Enabled: true},
			},
		},
	}

	got := resolveActiveModelConfig(
		cfg,
		"/workspace",
		[]providers.FallbackCandidate{{
			Provider:    "openai",
			Model:       "secondary",
			IdentityKey: "model_name:lb-model",
		}},
		"lb-model",
	)

	if got == nil {
		t.Fatal("resolveActiveModelConfig() = nil, want model config")
	}
	if got.Model != "secondary" {
		t.Fatalf("model = %q, want secondary", got.Model)
	}
	if !got.Streaming.Enabled {
		t.Fatal("streaming.enabled = false, want true from selected load-balanced entry")
	}
}

func TestResolveActiveModelConfig_UsesExactDuplicateEntryOrdinal(t *testing.T) {
	cfg := &config.Config{ModelList: []*config.ModelConfig{
		{
			ModelName: "lb-model", Provider: "openai", Model: "same-model", Enabled: true,
			Streaming: config.ModelStreamingConfig{Enabled: false},
		},
		{
			ModelName: "lb-model", Provider: "openai", Model: "same-model", Enabled: true,
			Streaming: config.ModelStreamingConfig{Enabled: true},
		},
	}}

	got := resolveActiveModelConfig(
		cfg,
		"/workspace",
		[]providers.FallbackCandidate{{
			Provider: "openai", Model: "same-model", IdentityKey: "model_name:lb-model", ConfigOrdinal: 2,
		}},
		"lb-model",
	)

	if got == nil || !got.Streaming.Enabled {
		t.Fatalf("resolveActiveModelConfig() = %+v, want exact second entry", got)
	}
}

func TestProviderForFallbackCandidate_FailsClosedForMissingExactProvider(t *testing.T) {
	activeProvider := &mockProvider{}
	candidate := providers.FallbackCandidate{
		Provider: "openai", Model: "fallback-model", ProviderConfigOrdinal: 2,
	}

	got, err := providerForFallbackCandidate(nil, activeProvider, candidate)
	if err == nil {
		t.Fatalf("providerForFallbackCandidate() = %#v, want exact-provider error", got)
	}
	if !strings.Contains(err.Error(), "row 2") {
		t.Fatalf("providerForFallbackCandidate() error = %q, want exact row", err)
	}
}

func TestProviderForFallbackCandidate_LegacyCandidateUsesActiveProvider(t *testing.T) {
	activeProvider := &mockProvider{}
	candidate := providers.FallbackCandidate{Provider: "openai", Model: "fallback-model"}

	got, err := providerForFallbackCandidate(nil, activeProvider, candidate)
	if err != nil {
		t.Fatalf("providerForFallbackCandidate() error = %v", err)
	}
	if got != activeProvider {
		t.Fatalf("providerForFallbackCandidate() = %#v, want active provider", got)
	}
}

func TestResolveActiveModelConfig_RequiresCandidateIdentity(t *testing.T) {
	cfg := &config.Config{
		ModelList: []*config.ModelConfig{
			{
				ModelName: "openai-gpt",
				Provider:  "openai",
				Model:     "gpt-4o",
				Enabled:   true,
				Streaming: config.ModelStreamingConfig{Enabled: true},
			},
		},
	}

	got := resolveActiveModelConfig(
		cfg,
		"/workspace",
		[]providers.FallbackCandidate{{
			Provider: "nvidia",
			Model:    "gpt-4o",
		}},
		"gpt-4o",
	)

	if got != nil {
		t.Fatalf("resolveActiveModelConfig() = %#v, want nil for non-active provider config", got)
	}
}

func TestResolveActiveModelConfig_IgnoresDisabledIdentityMatch(t *testing.T) {
	cfg := &config.Config{ModelList: []*config.ModelConfig{{
		ModelName: "primary",
		Provider:  "openai",
		Model:     "gpt-5.4",
		Enabled:   false,
	}}}

	got := resolveActiveModelConfig(
		cfg,
		"/workspace",
		[]providers.FallbackCandidate{{
			Provider:    "openai",
			Model:       "gpt-5.4",
			IdentityKey: "model_name:primary",
		}},
		"primary",
	)

	if got != nil {
		t.Fatalf("resolveActiveModelConfig() = %#v, want nil for disabled config", got)
	}
}
