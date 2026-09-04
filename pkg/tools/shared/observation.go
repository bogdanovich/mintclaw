package toolshared

import (
	"fmt"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
)

const (
	// MaxPlanObservationSteps bounds both update_plan admission and frontend
	// observation production. Normal coding plans are intentionally concise.
	MaxPlanObservationSteps = 32

	maxPlanExplanationBytes = 4 << 10
	maxPlanStepBytes        = 768
	maxPlanTextBytes        = 32 << 10

	maxCommandOutputBytes   = 64 << 10
	maxCommandIdentityBytes = 1 << 10
)

// NewPlanObservation validates, redacts, bounds, and clones one plan before it
// crosses into presentation state.
func NewPlanObservation(explanation string, steps []PlanStepObservation) (PlanObservation, error) {
	if len(steps) == 0 {
		return PlanObservation{}, fmt.Errorf("plan observation requires at least one step")
	}
	if len(steps) > MaxPlanObservationSteps {
		return PlanObservation{}, fmt.Errorf(
			"plan observation has %d steps; maximum is %d",
			len(steps),
			MaxPlanObservationSteps,
		)
	}

	result := PlanObservation{Steps: make([]PlanStepObservation, 0, len(steps))}
	var truncated bool
	result.Explanation, truncated = sanitizeObservationText(
		strings.TrimSpace(explanation),
		maxPlanExplanationBytes,
	)
	result.Truncated = truncated

	inProgress := 0
	for index, step := range steps {
		text := strings.TrimSpace(step.Step)
		if text == "" {
			return PlanObservation{}, fmt.Errorf("plan observation step %d is empty", index)
		}
		switch step.Status {
		case PlanStepPending, PlanStepInProgress, PlanStepCompleted:
		default:
			return PlanObservation{}, fmt.Errorf("plan observation step %d has invalid status", index)
		}
		if step.Status == PlanStepInProgress {
			inProgress++
		}

		text, stepTruncated := sanitizeObservationText(text, maxPlanStepBytes)
		result.Truncated = result.Truncated || stepTruncated
		result.Steps = append(result.Steps, PlanStepObservation{Step: text, Status: step.Status})
	}
	if inProgress > 1 {
		return PlanObservation{}, fmt.Errorf("plan observation can contain at most one in_progress step")
	}
	textBytes := len(result.Explanation)
	for _, step := range result.Steps {
		textBytes += len(step.Step)
	}
	if textBytes > maxPlanTextBytes {
		return PlanObservation{}, fmt.Errorf("plan observation text exceeds %d bytes", maxPlanTextBytes)
	}
	return result, nil
}

// SanitizeToolObservation returns an independent safe observation or nil when
// the value is empty, ambiguous, or invalid. This is the fail-closed admission
// boundary used after hooks and again by coding frontends.
func SanitizeToolObservation(observation *ToolObservation) *ToolObservation {
	if observation == nil || (observation.Command == nil) == (observation.Plan == nil) {
		return nil
	}
	if observation.Command != nil {
		command := sanitizeCommandObservation(*observation.Command)
		return &ToolObservation{Command: &command}
	}
	plan, err := NewPlanObservation(observation.Plan.Explanation, observation.Plan.Steps)
	if err != nil {
		return nil
	}
	plan.Truncated = plan.Truncated || observation.Plan.Truncated
	return &ToolObservation{Plan: &plan}
}

func sanitizeCommandObservation(command CommandObservation) CommandObservation {
	var truncated bool
	command.Stdout, truncated = sanitizeObservationText(command.Stdout, maxCommandOutputBytes)
	command.Truncated = command.Truncated || truncated
	command.Stderr, truncated = sanitizeObservationText(command.Stderr, maxCommandOutputBytes)
	command.Truncated = command.Truncated || truncated
	command.Output, truncated = sanitizeObservationText(command.Output, maxCommandOutputBytes)
	command.Truncated = command.Truncated || truncated
	command.SessionID, truncated = sanitizeObservationText(command.SessionID, maxCommandIdentityBytes)
	command.Truncated = command.Truncated || truncated
	command.Status, truncated = sanitizeObservationText(strings.TrimSpace(command.Status), maxCommandIdentityBytes)
	command.Truncated = command.Truncated || truncated
	if command.ExitCode != nil {
		exitCode := *command.ExitCode
		command.ExitCode = &exitCode
	}
	return command
}

func sanitizeObservationText(value string, maximum int) (string, bool) {
	value = strings.ToValidUTF8(value, "�")
	truncated := len(value) > maximum
	value = (diagnostictrace.Redactor{}).RedactText(value, maximum)
	return value, truncated
}
