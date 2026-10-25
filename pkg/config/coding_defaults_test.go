package config

import (
	"strings"
	"testing"
)

func TestValidateCodingDefaults(t *testing.T) {
	tests := []struct {
		name     string
		defaults CodingDefaultsConfig
		wantErr  string
	}{
		{name: "empty"},
		{
			name: "complete",
			defaults: CodingDefaultsConfig{
				Model: "reasoner", Provider: "openai", ReasoningEffort: "xhigh",
			},
		},
		{
			name: "provider without model", defaults: CodingDefaultsConfig{Provider: "openai"},
			wantErr: "coding.defaults.model is required",
		},
		{
			name: "model without provider", defaults: CodingDefaultsConfig{Model: "reasoner"},
			wantErr: "coding.defaults.provider is required",
		},
		{
			name: "unknown reasoning",
			defaults: CodingDefaultsConfig{
				Model: "reasoner", Provider: "openai", ReasoningEffort: "impossible",
			},
			wantErr: "reasoning_effort \"impossible\" is unsupported",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Coding.Defaults = test.defaults
			err := cfg.ValidateCodingDefaults()
			if test.wantErr == "" && err != nil {
				t.Fatalf("ValidateCodingDefaults() error = %v", err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("ValidateCodingDefaults() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestValidateModelReferencesChecksCodingDefault(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Coding.Defaults = CodingDefaultsConfig{Model: "missing", Provider: "openai"}
	err := cfg.ValidateModelReferences()
	if err == nil || !strings.Contains(err.Error(),
		`coding.defaults.model references unknown or disabled model_name "missing"`) {
		t.Fatalf("ValidateModelReferences() error = %v", err)
	}
}
