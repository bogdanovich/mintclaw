package providers

import (
	"slices"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/reasoning"
)

func TestReasoningProfileUsesConcreteCodexCatalog(t *testing.T) {
	profile := ReasoningProfile(&config.ModelConfig{
		Provider: "openai", Model: "gpt-5.6-sol", AuthMethod: "oauth",
	})
	got := make([]reasoning.Effort, 0, len(profile.Options))
	for _, option := range profile.Options {
		got = append(got, option.ID)
	}
	want := []reasoning.Effort{
		reasoning.EffortLow,
		reasoning.EffortMedium,
		reasoning.EffortHigh,
		reasoning.EffortXHigh,
		reasoning.EffortMax,
	}
	if !slices.Equal(got, want) || profile.Default != reasoning.EffortLow ||
		profile.Source != "bundled_codex_catalog" {
		t.Fatalf("ReasoningProfile() = %+v", profile)
	}
	if profile.Supports(reasoning.EffortAdaptive) {
		t.Fatal("Codex profile advertised unsupported adaptive reasoning")
	}
}

func TestReasoningProfileUnknownRouteIsConservative(t *testing.T) {
	profile := ReasoningProfile(&config.ModelConfig{Provider: "openrouter", Model: "unknown", Enabled: true})
	if len(profile.Options) != 0 || profile.Default != "" {
		t.Fatalf("ReasoningProfile() = %+v, want unknown capability surface", profile)
	}
}

func TestReasoningProfileHonorsExplicitConfig(t *testing.T) {
	profile := ReasoningProfile(&config.ModelConfig{
		Provider: "custom", Model: "model", ThinkingLevel: "high",
		Reasoning: &config.ModelReasoningConfig{
			SupportedEfforts: []reasoning.Effort{reasoning.EffortLow, reasoning.EffortHigh},
			DefaultEffort:    reasoning.EffortLow,
		},
	})
	if !profile.Supports(reasoning.EffortHigh) || profile.Default != reasoning.EffortHigh {
		t.Fatalf("ReasoningProfile() = %+v", profile)
	}
}
