package eval

import (
	"slices"
	"strings"
)

type ReadRecoveryCase struct {
	Marker string `json:"marker"`
}

func (recovery ReadRecoveryCase) validate(item Case) error {
	if item.Suite != "general" || item.PDF != nil || item.Browser != nil || item.Steering != nil ||
		!validID(recovery.Marker) || strings.Contains(item.Task, recovery.Marker) ||
		!strings.Contains(item.Task, readMissingPath) || len(item.Fixtures) != 1 ||
		item.Fixtures[0].Kind != "file" || item.Fixtures[0].SHA256 != digest([]byte(recovery.Marker+"\n")) ||
		!slices.Contains(item.Requires, "conversation") || !slices.Contains(item.Requires, readToolCapability) ||
		!slices.Contains(item.Requires, boundedCallsCapability) || item.Driver.Mode != "scripted" ||
		slices.Contains(item.Requires, "pdf") || slices.Contains(item.Requires, "browser") ||
		slices.Contains(item.Requires, activeToolCapability) ||
		len(item.Driver.Facts) != 0 || len(item.Driver.Steps) != 2 || item.Limits.MaxUserTurns != 3 ||
		item.Limits.MaxDriverCalls != 1 || item.Limits.TurnTimeout != "2m" || item.Limits.CaseTimeout != "5m" ||
		!requiredRubric(item.Rubric) {
		return ErrInvalidCase
	}
	for _, step := range item.Driver.Steps {
		if step.Action.Kind != "message" || step.WaitFor.Kind != "final" || step.WaitFor.Contains != "" ||
			strings.Contains(step.Action.Text, recovery.Marker) {
			return ErrInvalidCase
		}
	}
	if !strings.Contains(item.Driver.Steps[0].Action.Text, readFixturePath) {
		return ErrInvalidCase
	}
	native, marker, session, final := false, false, false, false
	for _, check := range item.Checks {
		switch check.Kind {
		case "read_recovery":
			if native || check.Expected != "" {
				return ErrInvalidCase
			}
			native = true
		case "response_contains":
			marker = marker || check.Expected == recovery.Marker
		case "same_session":
			session = true
		case "last_outcome":
			final = final || check.Expected == "final"
		}
	}
	if !native || !marker || !session || !final {
		return ErrInvalidCase
	}
	return nil
}
