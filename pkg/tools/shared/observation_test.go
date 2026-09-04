package toolshared

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNewPlanObservationPreservesValidatedOrderAndRedactsSecrets(t *testing.T) {
	steps := []PlanStepObservation{
		{Step: "Inspect the current state", Status: PlanStepCompleted},
		{Step: "Use sk-123456789abcdef while implementing", Status: PlanStepInProgress},
		{Step: "Run tests", Status: PlanStepPending},
	}
	plan, err := NewPlanObservation("Authorization: Bearer abcdefghijklmnop", steps)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 3 || plan.Steps[0].Step != "Inspect the current state" ||
		plan.Steps[1].Status != PlanStepInProgress || plan.Steps[2].Status != PlanStepPending {
		t.Fatalf("plan observation = %+v", plan)
	}
	joined := plan.Explanation + " " + plan.Steps[1].Step
	if strings.Contains(joined, "abcdefghijklmnop") || strings.Contains(joined, "123456789abcdef") ||
		!strings.Contains(joined, "[REDACTED]") {
		t.Fatalf("plan observation was not redacted: %+v", plan)
	}
	steps[0].Step = "mutated"
	if plan.Steps[0].Step != "Inspect the current state" {
		t.Fatalf("plan observation aliases input: %+v", plan)
	}
}

func TestNewPlanObservationIsByteAndItemBounded(t *testing.T) {
	steps := make([]PlanStepObservation, MaxPlanObservationSteps)
	for index := range steps {
		steps[index] = PlanStepObservation{Step: strings.Repeat("界", maxPlanStepBytes), Status: PlanStepPending}
	}
	plan, err := NewPlanObservation(strings.Repeat("e", maxPlanExplanationBytes+1), steps)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Truncated || len(plan.Explanation) > maxPlanExplanationBytes || !utf8.ValidString(plan.Explanation) {
		t.Fatalf("explanation bound = %d bytes, truncated=%v", len(plan.Explanation), plan.Truncated)
	}
	total := len(plan.Explanation)
	for _, step := range plan.Steps {
		total += len(step.Step)
		if len(step.Step) > maxPlanStepBytes || !utf8.ValidString(step.Step) {
			t.Fatalf("step is not bounded valid UTF-8: %q", step.Step)
		}
	}
	if total > maxPlanTextBytes {
		t.Fatalf("plan text = %d bytes, maximum %d", total, maxPlanTextBytes)
	}

	tooMany := make([]PlanStepObservation, len(steps)+1)
	copy(tooMany, steps)
	tooMany[len(steps)] = PlanStepObservation{Step: "overflow", Status: PlanStepPending}
	if _, err := NewPlanObservation("", tooMany); err == nil {
		t.Fatal("oversized plan observation was accepted")
	}
}

func TestNewPlanObservationRejectsInvalidPlans(t *testing.T) {
	tests := []struct {
		name  string
		steps []PlanStepObservation
	}{
		{name: "empty"},
		{name: "empty step", steps: []PlanStepObservation{{Status: PlanStepPending}}},
		{name: "invalid status", steps: []PlanStepObservation{{Step: "one", Status: "blocked"}}},
		{name: "multiple current", steps: []PlanStepObservation{
			{Step: "one", Status: PlanStepInProgress},
			{Step: "two", Status: PlanStepInProgress},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewPlanObservation("", test.steps); err == nil {
				t.Fatalf("invalid plan was accepted: %+v", test.steps)
			}
		})
	}
}

func TestSanitizeToolObservationFailsClosedAndClonesCommand(t *testing.T) {
	exitCode := 7
	command := &CommandObservation{
		Stdout:   strings.Repeat("o", maxCommandOutputBytes+1),
		Output:   "Bearer abcdefghijklmnop",
		Status:   "failed",
		ExitCode: &exitCode,
	}
	got := SanitizeToolObservation(&ToolObservation{Command: command})
	if got == nil || got.Command == nil || !got.Command.Truncated ||
		len(got.Command.Stdout) > maxCommandOutputBytes || strings.Contains(got.Command.Output, "abcdefghijklmnop") ||
		got.Command.ExitCode == nil || *got.Command.ExitCode != 7 {
		t.Fatalf("safe command observation = %#v", got)
	}
	command.Stdout = "mutated"
	exitCode = 9
	if got.Command.Stdout == "mutated" || *got.Command.ExitCode != 7 {
		t.Fatalf("safe command observation aliases input: %#v", got)
	}

	validPlan, err := NewPlanObservation("", []PlanStepObservation{{Step: "one", Status: PlanStepPending}})
	if err != nil {
		t.Fatal(err)
	}
	for name, observation := range map[string]*ToolObservation{
		"nil":       nil,
		"empty":     {},
		"ambiguous": {Command: command, Plan: &validPlan},
		"bad plan":  {Plan: &PlanObservation{Steps: []PlanStepObservation{{Step: "one", Status: "blocked"}}}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := SanitizeToolObservation(observation); got != nil {
				t.Fatalf("invalid union admitted: %#v", got)
			}
		})
	}
}
