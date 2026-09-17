package plan

import (
	"strings"
	"testing"
)

func TestNewProducesSafeBoundedPlan(t *testing.T) {
	state, err := New(" Authorization: Bearer abcdefghijklmnop ", []Step{
		{Step: " Inspect ", Status: StepCompleted},
		{Step: strings.Repeat("界", MaxStepBytes), Status: StepInProgress},
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Explanation == "Authorization: Bearer abcdefghijklmnop" ||
		strings.Contains(state.Explanation, "abcdefghijklmnop") {
		t.Fatalf("explanation was not redacted: %q", state.Explanation)
	}
	if state.Steps[0].Step != "Inspect" || !state.Truncated || len(state.Steps[1].Step) > MaxStepBytes {
		t.Fatalf("bounded plan = %+v", state)
	}
	if err := ValidateSafe(state); err != nil {
		t.Fatalf("safe plan did not validate: %v", err)
	}
}

func TestNewRejectsInvalidPlanLifecycle(t *testing.T) {
	for name, steps := range map[string][]Step{
		"empty":                nil,
		"blank step":           {{Step: " ", Status: StepPending}},
		"unknown status":       {{Step: "one", Status: "blocked"}},
		"multiple in progress": {{Step: "one", Status: StepInProgress}, {Step: "two", Status: StepInProgress}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New("", steps); err == nil {
				t.Fatalf("New accepted %+v", steps)
			}
		})
	}
}

func TestValidateSafeRejectsUnnormalizedOrEphemeralState(t *testing.T) {
	valid, err := New("safe", []Step{{Step: "one", Status: StepPending}})
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*State){
		"call ID":     func(state *State) { state.CallID = "call-1" },
		"whitespace":  func(state *State) { state.Steps[0].Step = " one " },
		"unsafe text": func(state *State) { state.Explanation = "token sk-123456789abcdef" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := Clone(valid)
			mutate(&candidate)
			if err := ValidateSafe(candidate); err == nil {
				t.Fatalf("ValidateSafe accepted %+v", candidate)
			}
		})
	}
}

func TestContentEqualIgnoresCallCorrelationOnly(t *testing.T) {
	left := State{
		CallID: "one", Explanation: "note", Steps: []Step{{Step: "work", Status: StepPending}},
	}
	right := Clone(left)
	right.CallID = "two"
	if !ContentEqual(left, right) {
		t.Fatal("call correlation changed visible plan equality")
	}
	right.Steps[0].Status = StepCompleted
	if ContentEqual(left, right) {
		t.Fatal("progress change was treated as identical")
	}
}
