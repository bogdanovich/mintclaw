// Package reasoning defines provider-neutral reasoning effort capabilities.
// Provider adapters remain responsible for mapping an effort onto their wire
// protocol; callers use Profile to validate user-visible choices first.
package reasoning

import (
	"fmt"
	"slices"
	"strings"
)

// Effort is a provider-neutral reasoning control value.
type Effort string

const (
	EffortOff      Effort = "off"
	EffortMinimal  Effort = "minimal"
	EffortLow      Effort = "low"
	EffortMedium   Effort = "medium"
	EffortHigh     Effort = "high"
	EffortXHigh    Effort = "xhigh"
	EffortMax      Effort = "max"
	EffortAdaptive Effort = "adaptive"
)

var knownEfforts = []Effort{
	EffortOff,
	EffortMinimal,
	EffortLow,
	EffortMedium,
	EffortHigh,
	EffortXHigh,
	EffortMax,
	EffortAdaptive,
}

// Parse normalizes a user-facing effort value.
func Parse(value string) (Effort, bool) {
	effort := Effort(strings.ToLower(strings.TrimSpace(value)))
	return effort, slices.Contains(knownEfforts, effort)
}

// Label returns a compact user-facing label.
func Label(effort Effort) string {
	switch effort {
	case EffortOff:
		return "Off"
	case EffortMinimal:
		return "Minimal"
	case EffortLow:
		return "Low"
	case EffortMedium:
		return "Medium"
	case EffortHigh:
		return "High"
	case EffortXHigh:
		return "Extra high"
	case EffortMax:
		return "Max"
	case EffortAdaptive:
		return "Adaptive"
	default:
		return string(effort)
	}
}

// Option describes one exact effort accepted by a model route.
type Option struct {
	ID          Effort `json:"id"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
}

// Profile is the controllable reasoning surface for one concrete model route.
// An empty Options list means MintClaw has no verified control surface; it does
// not imply that the model itself never reasons.
type Profile struct {
	Options  []Option `json:"options,omitempty"`
	Default  Effort   `json:"default,omitempty"`
	Required bool     `json:"required,omitempty"`
	Source   string   `json:"source,omitempty"`
}

// NewProfile constructs and validates a detached profile.
func NewProfile(efforts []Effort, defaultEffort Effort, required bool, source string) (Profile, error) {
	profile := Profile{
		Options:  make([]Option, 0, len(efforts)),
		Default:  defaultEffort,
		Required: required,
		Source:   strings.TrimSpace(source),
	}
	for _, effort := range efforts {
		profile.Options = append(profile.Options, Option{ID: effort, Label: Label(effort)})
	}
	if err := profile.Validate(); err != nil {
		return Profile{}, err
	}
	return profile, nil
}

// Validate rejects ambiguous or internally inconsistent capability metadata.
func (p Profile) Validate() error {
	seen := make(map[Effort]struct{}, len(p.Options))
	for index, option := range p.Options {
		effort, ok := Parse(string(option.ID))
		if !ok || effort != option.ID {
			return fmt.Errorf("reasoning profile option %d has unknown effort %q", index, option.ID)
		}
		if _, duplicate := seen[effort]; duplicate {
			return fmt.Errorf("reasoning profile repeats effort %q", effort)
		}
		seen[effort] = struct{}{}
	}
	if p.Default != "" {
		if _, ok := seen[p.Default]; !ok {
			return fmt.Errorf("reasoning profile default %q is not supported", p.Default)
		}
	}
	if p.Required {
		if len(p.Options) == 0 {
			return fmt.Errorf("required reasoning profile has no supported efforts")
		}
		if _, hasOff := seen[EffortOff]; hasOff {
			return fmt.Errorf("required reasoning profile cannot support off")
		}
	}
	return nil
}

// Supports reports whether the profile explicitly accepts an effort.
func (p Profile) Supports(effort Effort) bool {
	return slices.ContainsFunc(p.Options, func(option Option) bool {
		return option.ID == effort
	})
}

// OptionFor returns a detached option for an effort.
func (p Profile) OptionFor(effort Effort) (Option, bool) {
	for _, option := range p.Options {
		if option.ID == effort {
			return option, true
		}
	}
	return Option{}, false
}

// Intersect returns only choices supported by both profiles. The left-hand
// order and descriptions win because model aliases resolve their first route
// first. Empty/unknown is conservative and therefore intersects to empty.
func Intersect(left, right Profile) Profile {
	result := Profile{
		Options:  make([]Option, 0, min(len(left.Options), len(right.Options))),
		Required: left.Required || right.Required,
		Source:   "intersection",
	}
	for _, option := range left.Options {
		if right.Supports(option.ID) {
			result.Options = append(result.Options, option)
		}
	}
	if left.Default != "" && result.Supports(left.Default) {
		result.Default = left.Default
	} else if right.Default != "" && result.Supports(right.Default) {
		result.Default = right.Default
	}
	if len(result.Options) == 0 {
		result.Required = false
	}
	return result
}

// Clone returns a profile whose option storage is independent from the source.
func Clone(profile Profile) Profile {
	profile.Options = slices.Clone(profile.Options)
	return profile
}
