package eval

import "strings"

// NativeStopCase is an active runtime abort, not the local driver stop action.
type NativeStopCase struct {
	QueuedMarker string `json:"queued_marker"`
	ReuseMarker  string `json:"reuse_marker"`
}

func (stop NativeStopCase) validate(item Case) error {
	if item.Suite != "general" || item.PDF != nil || item.Browser != nil || item.Steering != nil ||
		item.ReadRecovery != nil || len(item.Requires) != 3 ||
		!containsAll(item.Requires, []string{"conversation", activeToolCapability, boundedCallsCapability}) ||
		len(item.Fixtures) != 0 || len(item.Driver.Facts) != 0 || item.Driver.Mode != "scripted" ||
		len(item.Driver.Steps) != 1 || item.Driver.Steps[0].WaitFor.Kind != "stopped" ||
		item.Driver.Steps[0].Action.Kind != "message" || item.Driver.Steps[0].WaitFor.Contains != "" ||
		item.Limits.MaxUserTurns != 4 || item.Limits.MaxDriverCalls != 1 ||
		item.Limits.TurnTimeout != "2m" || item.Limits.CaseTimeout != "5m" ||
		len(item.Rubric) != 1 || !requiredRubric(item.Rubric) || len(item.Task) > MaxInputBytes-1024 ||
		!validID(stop.QueuedMarker) || !validID(stop.ReuseMarker) ||
		strings.Contains(
			stop.QueuedMarker,
			stop.ReuseMarker,
		) || strings.Contains(stop.ReuseMarker, stop.QueuedMarker) ||
		strings.Contains(item.Task, stop.QueuedMarker) || strings.Contains(item.Task, stop.ReuseMarker) ||
		!strings.Contains(item.Driver.Steps[0].Action.Text, stop.ReuseMarker) ||
		strings.Contains(item.Driver.Steps[0].Action.Text, stop.QueuedMarker) {
		return ErrInvalidCase
	}
	checks := map[string]bool{}
	for _, check := range item.Checks {
		switch check.Kind {
		case "native_stop", "same_session":
			if checks[check.Kind] || check.Expected != "" {
				return ErrInvalidCase
			}
		case "response_contains":
			if checks[check.Kind] || check.Expected != stop.ReuseMarker {
				return ErrInvalidCase
			}
		case "last_outcome":
			if checks[check.Kind] || check.Expected != "final" {
				return ErrInvalidCase
			}
		default:
			return ErrInvalidCase
		}
		checks[check.Kind] = true
	}
	if len(checks) != 4 {
		return ErrInvalidCase
	}
	return nil
}

func nativeStopGuidance(marker, trial string) string {
	return "After the admitted read, reply with " + marker + ". [Eval scope " + trial + "]"
}

func validateNativeStopBinding(preset Preset, item Case) error {
	if item.NativeStop == nil {
		return nil
	}
	if preset.ActiveTool == nil || preset.ActiveTool.SteeringMode != "one-at-a-time" ||
		preset.ActiveTool.MaxParallelTurns != 1 {
		return ErrUnsafeTarget
	}
	return nil
}
