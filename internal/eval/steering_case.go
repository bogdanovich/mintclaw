package eval

import (
	"slices"
	"strings"
)

// Modes are separate profile-selected variants. They never change mid-trial.
type SteeringCase struct {
	Scenario      string   `json:"scenario"`
	Modes         []string `json:"modes"`
	InitialMarker string   `json:"initial_marker"`
	Updates       []string `json:"updates"`
}

func (steering SteeringCase) validate(item Case) error {
	if item.Suite != "general" || item.PDF != nil || item.Browser != nil ||
		!containsAll(item.Requires, []string{"conversation", activeToolCapability, boundedCallsCapability}) ||
		len(item.Fixtures) != 0 || len(item.Driver.Facts) != 0 || item.Driver.Mode != "scripted" ||
		len(item.Driver.Steps) != 1 || item.Driver.Steps[0].WaitFor.Kind != "final" ||
		item.Driver.Steps[0].Action.Kind != "message" || !requiredRubric(item.Rubric) ||
		len(item.Task) > MaxInputBytes-1024 || !validID(steering.InitialMarker) ||
		!strings.Contains(item.Task, steering.InitialMarker) {
		return ErrInvalidCase
	}
	wantUpdates := 1
	switch steering.Scenario {
	case "boundary":
		if !slices.Equal(steering.Modes, []string{"one-at-a-time"}) {
			return ErrInvalidCase
		}
	case "order":
		wantUpdates = 2
		if !slices.Equal(steering.Modes, []string{"one-at-a-time", "all"}) {
			return ErrInvalidCase
		}
	default:
		return ErrInvalidCase
	}
	if len(steering.Updates) != wantUpdates || item.Limits.MaxUserTurns != wantUpdates+2 {
		return ErrInvalidCase
	}
	seen := map[string]bool{steering.InitialMarker: true}
	for _, marker := range steering.Updates {
		if !validID(marker) || seen[marker] || strings.Contains(item.Task, marker) {
			return ErrInvalidCase
		}
		for previous := range seen {
			if strings.Contains(previous, marker) || strings.Contains(marker, previous) {
				return ErrInvalidCase
			}
		}
		seen[marker] = true
	}
	checks := 0
	for _, check := range item.Checks {
		if check.Kind == "steering_boundary" {
			checks++
		}
	}
	if checks != 1 {
		return ErrInvalidCase
	}
	return nil
}
