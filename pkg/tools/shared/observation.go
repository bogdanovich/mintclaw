package toolshared

import (
	"strings"

	codingplan "github.com/bogdanovich/mintclaw/pkg/coding/plan"
	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
)

const (
	// MaxPlanObservationSteps bounds both update_plan admission and frontend
	// observation production. Normal coding plans are intentionally concise.
	MaxPlanObservationSteps = codingplan.MaxSteps
	maxPlanExplanationBytes = codingplan.MaxExplanationBytes
	maxPlanStepBytes        = codingplan.MaxStepBytes
	maxPlanTextBytes        = codingplan.MaxTextBytes

	maxCommandOutputBytes   = 64 << 10
	maxCommandIdentityBytes = 1 << 10
)

// NewPlanObservation validates, redacts, bounds, and clones one plan before it
// crosses into presentation state.
func NewPlanObservation(explanation string, steps []PlanStepObservation) (PlanObservation, error) {
	return codingplan.New(explanation, steps)
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
