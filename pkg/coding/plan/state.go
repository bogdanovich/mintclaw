// Package plan defines the bounded, renderer-neutral coding plan contract.
package plan

import (
	"fmt"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
)

const (
	MaxSteps            = 32
	MaxExplanationBytes = 4 << 10
	MaxStepBytes        = 768
	MaxTextBytes        = 32 << 10
)

// StepStatus is one validated update_plan lifecycle state.
type StepStatus string

const (
	StepPending    StepStatus = "pending"
	StepInProgress StepStatus = "in_progress"
	StepCompleted  StepStatus = "completed"
)

// Step is one bounded, ordered plan item.
type Step struct {
	Step   string     `json:"step"`
	Status StepStatus `json:"status"`
}

// State is the safe current plan shared by runtime observation, persistence,
// and presentation. CallID is ephemeral presentation correlation and is
// intentionally omitted by durable checkpoints.
type State struct {
	CallID      string `json:"call_id,omitempty"`
	Explanation string `json:"explanation,omitempty"`
	Steps       []Step `json:"steps"`
	Truncated   bool   `json:"truncated,omitempty"`
}

// New validates, redacts, bounds, and clones one plan before it crosses into
// presentation or durable current-plan state.
func New(explanation string, steps []Step) (State, error) {
	if len(steps) == 0 {
		return State{}, fmt.Errorf("plan requires at least one step")
	}
	if len(steps) > MaxSteps {
		return State{}, fmt.Errorf("plan has %d steps; maximum is %d", len(steps), MaxSteps)
	}

	result := State{Steps: make([]Step, 0, len(steps))}
	var truncated bool
	result.Explanation, truncated = sanitizeText(strings.TrimSpace(explanation), MaxExplanationBytes)
	result.Truncated = truncated

	inProgress := 0
	for index, step := range steps {
		text := strings.TrimSpace(step.Step)
		if text == "" {
			return State{}, fmt.Errorf("plan step %d is empty", index)
		}
		switch step.Status {
		case StepPending, StepInProgress, StepCompleted:
		default:
			return State{}, fmt.Errorf("plan step %d has invalid status", index)
		}
		if step.Status == StepInProgress {
			inProgress++
		}
		text, stepTruncated := sanitizeText(text, MaxStepBytes)
		result.Truncated = result.Truncated || stepTruncated
		result.Steps = append(result.Steps, Step{Step: text, Status: step.Status})
	}
	if inProgress > 1 {
		return State{}, fmt.Errorf("plan can contain at most one in_progress step")
	}
	textBytes := len(result.Explanation)
	for _, step := range result.Steps {
		textBytes += len(step.Step)
	}
	if textBytes > MaxTextBytes {
		return State{}, fmt.Errorf("plan text exceeds %d bytes", MaxTextBytes)
	}
	return result, nil
}

// ValidateSafe requires already-normalized, redacted state. It is used when
// reading a checkpoint so disk contents cannot bypass the production boundary.
func ValidateSafe(state State) error {
	normalized, err := New(state.Explanation, state.Steps)
	if err != nil {
		return err
	}
	if state.CallID != "" {
		return fmt.Errorf("durable plan must not contain an ephemeral call ID")
	}
	if state.Explanation != normalized.Explanation || !slices.Equal(state.Steps, normalized.Steps) {
		return fmt.Errorf("plan is not normalized and redacted")
	}
	return nil
}

// Clone returns caller-owned plan state.
func Clone(state State) State {
	state.Steps = slices.Clone(state.Steps)
	return state
}

// ContentEqual compares renderer-visible plan content while ignoring the
// ephemeral tool call which delivered it.
func ContentEqual(left, right State) bool {
	return left.Explanation == right.Explanation && left.Truncated == right.Truncated &&
		slices.Equal(left.Steps, right.Steps)
}

func sanitizeText(value string, maximum int) (string, bool) {
	value = strings.ToValidUTF8(value, "�")
	truncated := len(value) > maximum
	value = (diagnostictrace.Redactor{}).RedactText(value, maximum)
	return value, truncated
}
