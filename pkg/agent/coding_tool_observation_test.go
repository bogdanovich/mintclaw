package agent

import (
	"testing"

	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestCodingToolObservationIsCodingOnlyAndCloned(t *testing.T) {
	exitCode := 7
	observation := &toolshared.ToolObservation{Command: &toolshared.CommandObservation{
		Output: "bounded", Status: "failed", ExitCode: &exitCode,
	}}
	if got := codingToolObservation(&turnState{}, observation); got != nil {
		t.Fatalf("personal turn observation = %#v", got)
	}
	ts := &turnState{opts: turnSpec{CodingContext: CodingPromptContext{SessionKey: "thread-1"}}}
	got := codingToolObservation(ts, observation)
	if got == nil || got.Command == nil || got.Command.Output != "bounded" || got.Command.ExitCode == nil ||
		*got.Command.ExitCode != 7 {
		t.Fatalf("coding turn observation = %#v", got)
	}
	observation.Command.Output = "mutated"
	exitCode = 9
	if got.Command.Output != "bounded" || *got.Command.ExitCode != 7 {
		t.Fatalf("coding observation aliases tool result = %#v", got)
	}
}

func TestCodingToolObservationAdmitsOnlySafePlanUnion(t *testing.T) {
	observation := &toolshared.ToolObservation{Plan: &toolshared.PlanObservation{
		Explanation: "Implement the fix",
		Steps: []toolshared.PlanStepObservation{
			{Step: "Inspect", Status: toolshared.PlanStepCompleted},
			{Step: "Patch", Status: toolshared.PlanStepInProgress},
		},
	}}
	if got := codingToolObservation(&turnState{}, observation); got != nil {
		t.Fatalf("personal turn plan observation = %#v", got)
	}
	ts := &turnState{opts: turnSpec{CodingContext: CodingPromptContext{SessionKey: "thread-1"}}}
	got := codingToolObservation(ts, observation)
	if got == nil || got.Plan == nil || got.Command != nil || len(got.Plan.Steps) != 2 ||
		got.Plan.Steps[1].Step != "Patch" {
		t.Fatalf("coding plan observation = %#v", got)
	}
	observation.Plan.Steps[1].Step = "mutated"
	if got.Plan.Steps[1].Step != "Patch" {
		t.Fatalf("coding plan observation aliases tool result = %#v", got)
	}

	exitCode := 0
	observation.Command = &toolshared.CommandObservation{ExitCode: &exitCode}
	if ambiguous := codingToolObservation(ts, observation); ambiguous != nil {
		t.Fatalf("ambiguous observation union admitted = %#v", ambiguous)
	}
	invalid := &toolshared.ToolObservation{Plan: &toolshared.PlanObservation{
		Steps: []toolshared.PlanStepObservation{{Step: "Blocked", Status: "blocked"}},
	}}
	if got := codingToolObservation(ts, invalid); got != nil {
		t.Fatalf("invalid plan observation admitted = %#v", got)
	}
}
