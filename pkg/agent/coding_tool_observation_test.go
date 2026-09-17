package agent

import (
	"strings"
	"testing"

	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
	agenttools "github.com/bogdanovich/mintclaw/pkg/tools"
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
	ts := &turnState{opts: freezeTurnInput(turnSpec{CodingContext: CodingPromptContext{SessionKey: "thread-1"}})}
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

func TestCodingToolObservationAdmitsRepositoryDiffOnlyForCodingTurns(t *testing.T) {
	observation := toolshared.NewRepositoryDiffObservation(codingworkspace.DiffResult{
		SchemaVersion: codingworkspace.RepositoryDiffSchemaV1,
		Target:        codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetCurrent},
		Files: []codingworkspace.DiffFile{{
			Path: "stable.go",
			Hunks: []codingworkspace.DiffHunk{{Lines: []codingworkspace.DiffLine{{
				Kind: "addition", NewLine: 1, Text: "stable",
			}}}},
		}},
	})
	if got := codingToolObservation(&turnState{}, observation); got != nil {
		t.Fatalf("personal turn repository diff observation = %#v", got)
	}
	ts := &turnState{opts: freezeTurnInput(turnSpec{CodingContext: CodingPromptContext{SessionKey: "thread-1"}})}
	got := codingToolObservation(ts, observation)
	if got == nil || got.RepositoryDiff == nil || got.RepositoryDiff.Diff.Files[0].Path != "stable.go" {
		t.Fatalf("coding repository diff observation = %#v", got)
	}
	observation.RepositoryDiff.Diff.Files[0].Path = "mutated.go"
	if got.RepositoryDiff.Diff.Files[0].Path != "stable.go" {
		t.Fatalf("coding repository diff aliases tool result = %#v", got)
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
	ts := &turnState{opts: freezeTurnInput(turnSpec{CodingContext: CodingPromptContext{SessionKey: "thread-1"}})}
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

func TestCodingToolStartObservationUsesNativeProviderOnlyForCodingTurns(t *testing.T) {
	execTool, err := agenttools.NewExecTool(t.TempDir(), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	registry := agenttools.NewToolRegistry()
	registry.Register(execTool)
	arguments := map[string]any{
		"action": "run", "command": "printf sk-123456789abcdef", "background": true,
	}
	if got := codingToolStartObservation(&turnState{}, registry, "exec", arguments); got != nil {
		t.Fatalf("personal turn start observation = %+v", got)
	}
	ts := &turnState{opts: freezeTurnInput(turnSpec{CodingContext: CodingPromptContext{SessionKey: "thread-1"}})}
	got := codingToolStartObservation(ts, registry, "exec", arguments)
	if got == nil || got.Command == nil || got.Command.Action != "run" || !got.Command.Background ||
		got.Command.OwnsProcess || got.Command.Status != "running" ||
		strings.Contains(got.Command.Command, "123456789abcdef") {
		t.Fatalf("coding start observation = %+v", got)
	}
	if observation := codingToolStartObservation(ts, registry, "missing", arguments); observation != nil {
		t.Fatalf("missing tool observation = %+v", observation)
	}
}
