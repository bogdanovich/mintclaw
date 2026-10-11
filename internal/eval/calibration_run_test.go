package eval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func calibrationFixture(t *testing.T, fixture *admissionFixture) string {
	t.Helper()
	item := rolePromptCase()
	corpus := Calibration{
		Version: Version, PromptVersion: JudgePromptVersion,
		Examples: []CalibrationExample{{
			ID: "coherent", Task: item.Task, Rubric: item.Rubric, Evidence: rolePromptEvents(), Expected: Pass,
		}},
	}
	name := filepath.Join(fixture.home, "calibration.json")
	writeAdmissionJSON(t, name, corpus)
	return name
}

func TestCalibratePresetPersistsPrivateReportWithoutTargetWork(t *testing.T) {
	fixture, _, _, count := gradeFixture(t, false)
	source := calibrationFixture(t, fixture)
	tasks, probes := fixture.tasks.Load(), fixture.probes.Load()
	result, err := CalibratePreset(t.Context(), fixture.preset, source, RunOptions{
		Home: fixture.home, OutputParent: fixture.output, SuiteTimeout: 5 * time.Second,
	})
	if err != nil || result.ID == "" || !result.Complete || result.Verdict != Pass || count.Load() != 1 ||
		len(result.Examples) != 1 || result.Examples[0].Judge.Call == nil ||
		result.Examples[0].Judge.Call.Usage == nil || result.Examples[0].Judge.Call.Usage.PromptTokens != 10252 ||
		len(result.RoleUsage) != 1 || result.RoleUsage[0].InputTokens != 10252 ||
		fixture.tasks.Load() != tasks || fixture.probes.Load() != probes {
		t.Fatal("calibration changed target state or lost its assessment/usage", err)
	}
	for _, name := range []string{"summary.md", "calibration.json"} {
		info, statErr := os.Stat(filepath.Join(result.ReportDir, name))
		data, readErr := os.ReadFile(filepath.Join(result.ReportDir, name))
		if statErr != nil || info.Mode().Perm() != 0o600 || readErr != nil ||
			strings.Contains(string(data), "synthetic-grading-credential") {
			t.Fatal("calibration report was public or contained credentials")
		}
	}
}

func TestCalibratePresetRefusesBadBudgetsAndPrivateCorpusBeforeCalls(t *testing.T) {
	fixture, _, _, count := gradeFixture(t, false)
	source := calibrationFixture(t, fixture)
	for _, timeout := range []time.Duration{-1, 5 * time.Hour} {
		result, err := CalibratePreset(t.Context(), fixture.preset, source, RunOptions{
			Home: fixture.home, OutputParent: fixture.output, SuiteTimeout: timeout,
		})
		if !errors.Is(err, ErrRunOptions) || result.ID != "" || count.Load() != 0 {
			t.Fatal("invalid calibration budget reached provider")
		}
	}
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.ReplaceAll(string(data), "Help select a test category.", "synthetic-grading-credential"))
	if err = os.WriteFile(source, data, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := CalibratePreset(t.Context(), fixture.preset, source, RunOptions{
		Home: fixture.home, OutputParent: fixture.output,
	})
	if !errors.Is(err, ErrGrade) || result.ID != "" || count.Load() != 0 {
		t.Fatal("credential-bearing calibration reached provider")
	}
}

func TestCanceledCalibrationRetainsNonPassingCoverageWithoutProviderCall(t *testing.T) {
	fixture, _, _, count := gradeFixture(t, false)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := CalibratePreset(ctx, fixture.preset, calibrationFixture(t, fixture), RunOptions{
		Home: fixture.home, OutputParent: fixture.output,
	})
	if !errors.Is(err, ErrNotPassed) || !result.Complete || result.Verdict == Pass || count.Load() != 0 ||
		len(result.Examples) != 1 || result.Examples[0].Judge.Call == nil ||
		result.Examples[0].Judge.Call.Attempted {
		t.Fatal("canceled calibration passed, lost coverage, or reached provider")
	}
}
