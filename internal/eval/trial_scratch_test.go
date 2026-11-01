package eval

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestGradePreservesFrozenTrialScratchWithoutAllocatingOrReplaying(t *testing.T) {
	fixture, item, source, calls := gradeRepeatedFixture(t, false, 2)
	tasks, probes := fixture.tasks.Load(), fixture.probes.Load()
	before, err := os.ReadFile(filepath.Join(source.ReportDir, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := Grade(t.Context(), fixture.preset, []LoadedCase{item}, source.ReportDir, RunOptions{
		Home: fixture.home, OutputParent: fixture.output,
	})
	if err != nil || result.Verdict != Pass || !reflect.DeepEqual(result.TrialScratch, source.TrialScratch) ||
		calls.Load() != 2 || fixture.tasks.Load() != tasks || fixture.probes.Load() != probes {
		t.Fatal("regrade replaced resource receipts or replayed target work", err)
	}
	after, err := os.ReadFile(filepath.Join(source.ReportDir, "run.json"))
	if err != nil || string(before) != string(after) {
		t.Fatal("regrade modified historical resource evidence")
	}
	assertPrivateReport(t, result)
	comparison, err := Compare(source.ReportDir, result.ReportDir)
	if err != nil || slices.Contains(comparison.EnvironmentChanges, "trial_scratch_scope") ||
		slices.Contains(comparison.EnvironmentUnknowns, "current_trial_scratch") {
		t.Fatal("frozen staging receipts became a new or unknown resource scope", err)
	}
}

func TestGradeRejectsInvalidOrUnsettledTrialScratchBeforeJudge(t *testing.T) {
	for _, change := range []string{
		"retained", "unavailable", "not_attempted", "session", "case", "hash", "time", "scope", "missing", "duplicate",
	} {
		t.Run(change, func(t *testing.T) {
			fixture, item, source, calls := gradeFixture(t, false)
			receipt := &source.TrialScratch.Trials[0]
			switch change {
			case "retained", "unavailable", "not_attempted":
				receipt.Code = change
				if change != "retained" {
					receipt.CreatedAt, receipt.CheckpointSHA256 = 0, ""
				}
			case "session":
				receipt.SessionID = uuid.NewString()
			case "case":
				receipt.CaseSHA256 = strings.Repeat("b", 64)
			case "hash":
				receipt.CheckpointSHA256 = "unknown"
			case "time":
				receipt.CreatedAt = 0
			case "scope":
				source.TrialScratch.Scope = "whole_host_cleanup"
			case "missing":
				source.TrialScratch.Trials = nil
			case "duplicate":
				source.TrialScratch.Trials = append(source.TrialScratch.Trials, *receipt)
			}
			writeAdmissionJSON(t, filepath.Join(source.ReportDir, "run.json"), source)
			tasks, probes := fixture.tasks.Load(), fixture.probes.Load()
			result, err := Grade(t.Context(), fixture.preset, []LoadedCase{item}, source.ReportDir, RunOptions{
				Home: fixture.home, OutputParent: fixture.output,
			})
			if !errors.Is(err, ErrGrade) || result.ID != "" || calls.Load() != 0 ||
				fixture.tasks.Load() != tasks || fixture.probes.Load() != probes {
				t.Fatal("ineligible staging evidence reached a judge or target", err)
			}
		})
	}
}

func TestCompareKeepsLegacyScratchUnknownAndRejectsMismatchedReceipts(t *testing.T) {
	for _, change := range []string{"removed", "retained", "session", "false-pass"} {
		t.Run(change, func(t *testing.T) {
			baseline, current := comparisonFixture(t, Pass), comparisonFixture(t, Pass)
			trial := comparisonTrial(t, current, 0)
			current.TrialScratch = &TrialScratchObservation{Scope: trialScratchScope, Trials: []TrialScratchEvidence{{
				TrialID: trial.ID, SessionID: trial.SessionID, CaseSHA256: trial.CaseSHA256,
				Code: "removed", CreatedAt: 1, CheckpointSHA256: strings.Repeat("a", 64),
			}}}
			switch change {
			case "retained", "false-pass":
				current.TrialScratch.Trials[0].Code = "retained"
				if change == "retained" {
					current.Verdict = Inconclusive
				}
			case "session":
				current.TrialScratch.Trials[0].SessionID = uuid.NewString()
			}
			writeAdmissionJSON(t, filepath.Join(current.ReportDir, "run.json"), current)
			result, err := Compare(baseline.ReportDir, current.ReportDir)
			if change == "session" || change == "false-pass" {
				if !errors.Is(err, ErrCompare) {
					t.Fatal("contradictory staging receipts were accepted", err)
				}
				return
			}
			if err != nil || !result.CompatibleCases || result.CurrentVerdict != current.Verdict ||
				!slices.Contains(result.EnvironmentChanges, "trial_scratch_scope") ||
				!slices.Contains(result.EnvironmentUnknowns, "baseline_trial_scratch") ||
				slices.Contains(result.EnvironmentUnknowns, "current_trial_scratch") != (change == "retained") {
				t.Fatal("comparison invented legacy cleanup or hid uncertainty", err, result)
			}
		})
	}
}
