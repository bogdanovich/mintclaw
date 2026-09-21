package agent

import (
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestParseThinkingLevel(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  ThinkingLevel
	}{
		{"off", "off", ThinkingOff},
		{"empty", "", ThinkingOff},
		{"minimal", "minimal", ThinkingMinimal},
		{"low", "low", ThinkingLow},
		{"medium", "medium", ThinkingMedium},
		{"high", "high", ThinkingHigh},
		{"xhigh", "xhigh", ThinkingXHigh},
		{"max", "max", ThinkingMax},
		{"adaptive", "adaptive", ThinkingAdaptive},
		{"unknown", "unknown", ThinkingOff},
		// Case-insensitive and whitespace-tolerant
		{"upper_Medium", "Medium", ThinkingMedium},
		{"upper_HIGH", "HIGH", ThinkingHigh},
		{"mixed_Adaptive", "Adaptive", ThinkingAdaptive},
		{"leading_space", " high", ThinkingHigh},
		{"trailing_space", "low ", ThinkingLow},
		{"both_spaces", " medium ", ThinkingMedium},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseThinkingLevel(tt.input); got != tt.want {
				t.Errorf("parseThinkingLevel(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestActiveThinkingSettingsExactOverrideWinsOverModelDefault(t *testing.T) {
	settings := activeThinkingSettings(
		&config.ModelConfig{ThinkingLevel: "low"},
		ThinkingXHigh,
		true,
		true,
	)
	if settings.level != ThinkingXHigh || !settings.configured {
		t.Fatalf("exact thinking override = %+v, want configured xhigh", settings)
	}

	settings = activeThinkingSettings(
		&config.ModelConfig{ThinkingLevel: "low"},
		ThinkingXHigh,
		true,
		false,
	)
	if settings.level != ThinkingLow || !settings.configured {
		t.Fatalf("model thinking default = %+v, want configured low", settings)
	}
}
