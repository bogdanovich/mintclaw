package providers

import (
	"slices"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/reasoning"
)

func TestReasoningProfileUsesConcreteCodexCatalog(t *testing.T) {
	want := []reasoning.Effort{
		reasoning.EffortLow,
		reasoning.EffortMedium,
		reasoning.EffortHigh,
		reasoning.EffortXHigh,
		reasoning.EffortMax,
	}
	for _, model := range []string{"gpt-5.6-sol", "gpt-6.2-sol"} {
		t.Run(model, func(t *testing.T) {
			profile := ReasoningProfile(&config.ModelConfig{
				Provider: "openai", Model: model, AuthMethod: "oauth",
			})
			got := make([]reasoning.Effort, 0, len(profile.Options))
			for _, option := range profile.Options {
				got = append(got, option.ID)
			}
			if !slices.Equal(got, want) || profile.Default != reasoning.EffortLow ||
				profile.Source != "bundled_codex_catalog" {
				t.Fatalf("ReasoningProfile() = %+v", profile)
			}
		})
	}
}

func TestReasoningProfileUsesGPT61SolCatalog(t *testing.T) {
	profile := ReasoningProfile(&config.ModelConfig{
		Provider: "openai", Model: "gpt-6.1-sol", AuthMethod: "oauth", ThinkingLevel: "medium",
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
	if !slices.Equal(got, want) || profile.Default != reasoning.EffortMedium ||
		profile.Source != "bundled_codex_catalog" {
		t.Fatalf("ReasoningProfile() = %+v", profile)
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
