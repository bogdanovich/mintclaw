package config

import (
	"fmt"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/reasoning"
)

// ValidateCodingDefaults checks the provider-neutral shape of coding defaults.
// Runtime route selection additionally verifies the exact provider/model pair
// and its supported reasoning efforts.
func (c *Config) ValidateCodingDefaults() error {
	if c == nil {
		return nil
	}
	defaults := c.Coding.Defaults
	if defaults.Model == "" {
		if defaults.Provider != "" || defaults.ReasoningEffort != "" {
			return fmt.Errorf("coding.defaults.model is required when provider or reasoning_effort is configured")
		}
		return nil
	}
	if defaults.Model != strings.TrimSpace(defaults.Model) {
		return fmt.Errorf("coding.defaults.model must not have surrounding whitespace")
	}
	if defaults.Provider == "" {
		return fmt.Errorf("coding.defaults.provider is required when model is configured")
	}
	if defaults.Provider != strings.TrimSpace(defaults.Provider) {
		return fmt.Errorf("coding.defaults.provider must not have surrounding whitespace")
	}
	if defaults.ReasoningEffort == "" {
		return nil
	}
	if defaults.ReasoningEffort != strings.TrimSpace(defaults.ReasoningEffort) {
		return fmt.Errorf("coding.defaults.reasoning_effort must not have surrounding whitespace")
	}
	effort, ok := reasoning.Parse(defaults.ReasoningEffort)
	if !ok || string(effort) != defaults.ReasoningEffort {
		return fmt.Errorf("coding.defaults.reasoning_effort %q is unsupported", defaults.ReasoningEffort)
	}
	return nil
}
