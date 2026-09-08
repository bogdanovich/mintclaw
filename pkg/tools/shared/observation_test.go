package toolshared

import (
	"strings"
	"testing"
	"unicode/utf8"

	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
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
		Action: "run", Command: "printf sk-123456789abcdef", CWD: "/repo", Input: "Bearer abcdefghijklmnop",
		Source: "agent", Stdout: strings.Repeat("o", maxCommandOutputBytes+1),
		Output: "Bearer abcdefghijklmnop", Status: "failed", ExitCode: &exitCode,
		Transcript: []CommandTranscriptEntry{{Sequence: 1, Stream: "stdout", Text: "safe output"}},
	}
	got := SanitizeToolObservation(&ToolObservation{Command: command})
	if got == nil || got.Command == nil || !got.Command.Truncated ||
		len(got.Command.Stdout) > maxCommandOutputBytes || strings.Contains(got.Command.Output, "abcdefghijklmnop") ||
		strings.Contains(got.Command.Command, "123456789abcdef") ||
		strings.Contains(got.Command.Input, "abcdefghijklmnop") || got.Command.ExitCode == nil ||
		*got.Command.ExitCode != 7 || len(got.Command.Transcript) != 1 {
		t.Fatalf("safe command observation = %#v", got)
	}
	command.Stdout = "mutated"
	command.Transcript[0].Text = "mutated transcript"
	exitCode = 9
	if got.Command.Stdout == "mutated" || got.Command.Transcript[0].Text == "mutated transcript" ||
		*got.Command.ExitCode != 7 {
		t.Fatalf("safe command observation aliases input: %#v", got)
	}

	validPlan, err := NewPlanObservation("", []PlanStepObservation{{Step: "one", Status: PlanStepPending}})
	if err != nil {
		t.Fatal(err)
	}
	for name, observation := range map[string]*ToolObservation{
		"nil":               nil,
		"empty":             {},
		"ambiguous":         {Command: command, Plan: &validPlan},
		"ambiguous explore": {Exploration: &ExplorationObservation{Operation: ExplorationRead}, Plan: &validPlan},
		"bad exploration":   {Exploration: &ExplorationObservation{Operation: "execute"}},
		"bad plan":          {Plan: &PlanObservation{Steps: []PlanStepObservation{{Step: "one", Status: "blocked"}}}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := SanitizeToolObservation(observation); got != nil {
				t.Fatalf("invalid union admitted: %#v", got)
			}
		})
	}
}

func TestSanitizeToolObservationBoundsRedactsAndClonesExploration(t *testing.T) {
	original := &ExplorationObservation{
		Operation: ExplorationSearch,
		Path:      strings.Repeat("p", maxExplorationValueBytes+1),
		Pattern:   "sk-123456789abcdef",
		Workspace: "build",
	}
	got := SanitizeToolObservation(&ToolObservation{Exploration: original})
	if got == nil || got.Exploration == nil || got.Exploration.Operation != ExplorationSearch ||
		!got.Exploration.Truncated || len(got.Exploration.Path) > maxExplorationValueBytes ||
		strings.Contains(got.Exploration.Pattern, "123456789abcdef") || got.Exploration.Workspace != "build" {
		t.Fatalf("safe exploration observation = %#v", got)
	}
	original.Path = "mutated"
	original.Pattern = "mutated"
	if got.Exploration.Path == "mutated" || got.Exploration.Pattern == "mutated" {
		t.Fatalf("safe exploration aliases input: %#v", got)
	}
}

func TestSanitizeCommandTranscriptIsByteAndItemBounded(t *testing.T) {
	entries := make([]CommandTranscriptEntry, maxCommandTranscriptEntries+20)
	for index := range entries {
		entries[index] = CommandTranscriptEntry{
			Sequence: uint64(index + 1), Stream: "stdout", Text: strings.Repeat("界", 1024),
		}
	}
	got := SanitizeToolObservation(&ToolObservation{Command: &CommandObservation{Transcript: entries}})
	if got == nil || got.Command == nil || !got.Command.Truncated ||
		len(got.Command.Transcript) > maxCommandTranscriptEntries {
		t.Fatalf("bounded command transcript = %#v", got)
	}
	total := 0
	for _, entry := range got.Command.Transcript {
		total += len(entry.Text)
		if !utf8.ValidString(entry.Text) {
			t.Fatalf("invalid UTF-8 transcript entry: %q", entry.Text)
		}
	}
	if total > maxCommandOutputBytes {
		t.Fatalf("transcript bytes = %d, want <= %d", total, maxCommandOutputBytes)
	}
}

func TestSanitizeCommandTranscriptRedactsAcrossAdjacentFragments(t *testing.T) {
	got := SanitizeToolObservation(&ToolObservation{Command: &CommandObservation{Transcript: []CommandTranscriptEntry{
		{Sequence: 1, Stream: "stdout", Text: "sk-1234"},
		{Sequence: 2, Stream: "stdout", Text: "56789abcdef"},
		{Sequence: 3, Stream: "stderr", Text: "safe"},
	}}})
	if got == nil || got.Command == nil || len(got.Command.Transcript) != 2 ||
		got.Command.Transcript[0].Sequence != 1 || got.Command.Transcript[0].Text != "[REDACTED]" ||
		got.Command.Transcript[1].Text != "safe" {
		t.Fatalf("sanitized fragmented transcript = %#v", got)
	}
}

func TestCommandObservationSinkIsOptionalAndReceivesIndependentSafeValues(t *testing.T) {
	PublishCommandObservation(t.Context(), CommandObservation{Output: "ignored"})

	var received []CommandObservation
	ctx := WithCommandObservationSink(t.Context(), func(observation CommandObservation) {
		received = append(received, observation)
	})
	original := CommandObservation{
		Command: "echo safe", Status: "running",
		Transcript: []CommandTranscriptEntry{{Sequence: 1, Stream: "stdout", Text: "one"}},
	}
	PublishCommandObservation(ctx, original)
	original.Transcript[0].Text = "mutated"
	if len(received) != 1 || received[0].Transcript[0].Text != "one" {
		t.Fatalf("command observation sink = %+v", received)
	}
}

func TestRepositoryDiffObservationIsBoundedRedactedAndIndependent(t *testing.T) {
	diff := codingworkspace.DiffResult{
		SchemaVersion: codingworkspace.RepositoryDiffSchemaV1,
		Target:        codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetCurrent},
		Files: []codingworkspace.DiffFile{{
			Path:       "pkg/sk-123456789abcdef.go",
			Status:     " M",
			Additions:  1,
			Provenance: codingworkspace.ProvenanceFirstObservedDuringThread,
			Hunks: []codingworkspace.DiffHunk{{
				OldStart: 1, OldLines: 1, NewStart: 1, NewLines: 1,
				Lines: []codingworkspace.DiffLine{{
					Kind: "addition", NewLine: 1, Text: "Authorization: Bearer abcdefghijklmnop",
				}},
			}},
		}},
		Additions: 1,
		Provenance: &codingworkspace.ProvenanceResult{
			BaselineID: "baseline",
			Paths: []codingworkspace.ProvenancePath{{
				Path: "pkg/sk-123456789abcdef.go", Status: " M",
				Provenance: codingworkspace.ProvenanceFirstObservedDuringThread,
			}},
		},
	}
	got := NewRepositoryDiffObservation(diff)
	if got == nil || got.RepositoryDiff == nil || len(got.RepositoryDiff.Diff.Files) != 1 {
		t.Fatalf("repository diff observation = %#v", got)
	}
	observed := got.RepositoryDiff.Diff
	joined := observed.Files[0].Path + observed.Files[0].Hunks[0].Lines[0].Text +
		observed.Provenance.Paths[0].Path
	if strings.Contains(joined, "123456789abcdef") || strings.Contains(joined, "abcdefghijklmnop") ||
		!strings.Contains(joined, "[REDACTED]") {
		t.Fatalf("repository diff was not redacted: %#v", observed)
	}
	diff.Files[0].Path = "mutated.go"
	diff.Files[0].Hunks[0].Lines[0].Text = "mutated"
	diff.Provenance.Paths[0].Path = "mutated.go"
	if observed.Files[0].Path == "mutated.go" || observed.Files[0].Hunks[0].Lines[0].Text == "mutated" ||
		observed.Provenance.Paths[0].Path == "mutated.go" {
		t.Fatalf("repository diff observation aliases input: %#v", observed)
	}
}

func TestRepositoryDiffObservationFailsClosedAndBoundsEvidence(t *testing.T) {
	files := make([]codingworkspace.DiffFile, maxRepositoryDiffFiles+1)
	for index := range files {
		files[index] = codingworkspace.DiffFile{
			Path: "file.go", Status: "M", Additions: maxRepositoryDiffLines + 1,
			Hunks: []codingworkspace.DiffHunk{{
				OldStart: 1, OldLines: 1, NewStart: 1, NewLines: maxRepositoryDiffLines + 1,
				Lines: []codingworkspace.DiffLine{{
					Kind: "addition", NewLine: 1, Text: strings.Repeat("x", maxRepositoryDiffLineBytes+1),
				}},
			}},
		}
	}
	got := NewRepositoryDiffObservation(codingworkspace.DiffResult{
		SchemaVersion: codingworkspace.RepositoryDiffSchemaV1,
		Target:        codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetCurrent},
		Files:         files,
		Additions:     maxRepositoryDiffLines + 1,
	})
	if got == nil || got.RepositoryDiff == nil || !got.RepositoryDiff.Diff.Truncated ||
		len(got.RepositoryDiff.Diff.Files) > maxRepositoryDiffFiles ||
		len(got.RepositoryDiff.Diff.Files[0].Hunks[0].Lines[0].Text) > maxRepositoryDiffLineBytes {
		t.Fatalf("bounded repository diff observation = %#v", got)
	}

	validDiff := codingworkspace.DiffResult{
		SchemaVersion: codingworkspace.RepositoryDiffSchemaV1,
		Target:        codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetCurrent},
	}
	for name, observation := range map[string]*ToolObservation{
		"ambiguous": {
			Command: &CommandObservation{}, RepositoryDiff: &RepositoryDiffObservation{Diff: validDiff},
		},
		"schema": {RepositoryDiff: &RepositoryDiffObservation{Diff: codingworkspace.DiffResult{
			SchemaVersion: "future", Target: validDiff.Target,
		}}},
		"target": {RepositoryDiff: &RepositoryDiffObservation{Diff: codingworkspace.DiffResult{
			SchemaVersion: codingworkspace.RepositoryDiffSchemaV1,
			Target:        codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetBase},
		}}},
		"negative": {RepositoryDiff: &RepositoryDiffObservation{Diff: codingworkspace.DiffResult{
			SchemaVersion: codingworkspace.RepositoryDiffSchemaV1, Target: validDiff.Target, Additions: -1,
		}}},
		"line kind": {RepositoryDiff: &RepositoryDiffObservation{Diff: codingworkspace.DiffResult{
			SchemaVersion: codingworkspace.RepositoryDiffSchemaV1, Target: validDiff.Target,
			Files: []codingworkspace.DiffFile{{
				Path: "bad.go", Hunks: []codingworkspace.DiffHunk{{
					Lines: []codingworkspace.DiffLine{{Kind: "execute"}},
				}},
			}},
		}}},
	} {
		t.Run(name, func(t *testing.T) {
			if sanitized := SanitizeToolObservation(observation); sanitized != nil {
				t.Fatalf("invalid repository diff admitted: %#v", sanitized)
			}
		})
	}
}
