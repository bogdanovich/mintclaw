package eval

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/health"
)

func comparisonFixture(t *testing.T, verdicts ...Verdict) RunReport {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	report := RunReport{
		Version: Version, ID: uuid.NewString(), PresetID: "eval-test", Mode: "run", Repeat: len(verdicts),
		StartedAt: time.Now().UTC(), Complete: true, Verdict: combineVerdicts(verdicts...),
		Coverage: map[Verdict]int{}, ReportDir: directory,
		Runtime: health.RuntimeProbe{
			Version: 1, ConfigSHA256: strings.Repeat("1", 64), GitCommit: strings.Repeat("a", 40),
			GOOS: "linux", GOARCH: "amd64", GoVersion: "test-go", BuildVersion: "test-build",
		},
	}
	for ordinal, verdict := range verdicts {
		trial := Trial{
			Version: Version, ID: uuid.NewString(), SessionID: uuid.NewString(), AgentID: "main",
			CaseID: "general.compare", CaseSHA256: strings.Repeat("2", 64), Repetition: ordinal + 1,
			Verdict: verdict, ExecutionVerdict: verdict, Code: "synthetic_result",
			Events: []Event{{ID: "event-1", Turn: 1, Kind: "final", Content: "private-evidence-canary"}},
			Checks: []CheckResult{{ID: "outcome", Verdict: verdict, EvidenceIDs: []string{}}},
		}
		name := "trial-" + trial.ID + ".json"
		writeAdmissionJSON(t, filepath.Join(directory, name), trial)
		report.Trials = append(report.Trials, TrialSummary{
			ID: trial.ID, CaseID: trial.CaseID, CaseSHA256: trial.CaseSHA256, Repetition: trial.Repetition,
			Verdict: trial.Verdict, Code: trial.Code, File: name,
		})
		report.Coverage[verdict]++
	}
	writeAdmissionJSON(t, filepath.Join(directory, "run.json"), report)
	return report
}

func comparisonTrial(t *testing.T, report RunReport, ordinal int) Trial {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(report.ReportDir, report.Trials[ordinal].File))
	var trial Trial
	if err != nil || json.Unmarshal(data, &trial) != nil {
		t.Fatal("invalid test trial", err)
	}
	return trial
}

func TestCompareRetainsAllVerdictsAndSamplingCountsWithoutQualityClaim(t *testing.T) {
	baseline := comparisonFixture(t, Pass, Fail, Error, Inconclusive, Skipped)
	current := comparisonFixture(t, Pass, Pass)
	before, _ := os.ReadFile(filepath.Join(baseline.ReportDir, "run.json"))
	result, err := Compare(baseline.ReportDir, current.ReportDir)
	if err != nil || !result.CompatibleCases || result.Outcome != "compared" || len(result.Cases) != 1 ||
		result.Cases[0].Baseline.Trials != 5 || result.Cases[0].Current.Trials != 2 ||
		result.Cases[0].Current.Coverage[Pass] != 2 || !slices.Equal(result.EnvironmentChanges, []string{"repetitions"}) ||
		result.Attribution != "not_established" || result.EnvironmentCoverage != "recorded_runtime_and_roles_only" {
		t.Fatal("comparison dropped coverage or claimed causal quality", result, err)
	}
	for _, verdict := range []Verdict{Pass, Fail, Error, Inconclusive, Skipped} {
		if result.Cases[0].Baseline.Coverage[verdict] != 1 {
			t.Fatal("a nonpassing trial disappeared")
		}
	}
	encoded, err := json.Marshal(result)
	after, _ := os.ReadFile(filepath.Join(baseline.ReportDir, "run.json"))
	if err != nil || strings.Contains(string(encoded), "private-evidence-canary") || string(before) != string(after) {
		t.Fatal("comparison exposed transcripts or modified evidence")
	}
}

func TestCompareReportsCaseChangesAndDoesNotAttributeEnvironmentDrift(t *testing.T) {
	for _, change := range []string{"case_changed", "added_and_removed", "environment"} {
		t.Run(change, func(t *testing.T) {
			baseline, current := comparisonFixture(t, Pass), comparisonFixture(t, Pass)
			trial := comparisonTrial(t, current, 0)
			switch change {
			case "case_changed":
				trial.CaseSHA256, current.Trials[0].CaseSHA256 = strings.Repeat("3", 64), strings.Repeat("3", 64)
			case "added_and_removed":
				trial.CaseID, current.Trials[0].CaseID = "general.new", "general.new"
			case "environment":
				current.PresetID, current.Mode = "another-preset", "grade"
				current.Runtime.GitCommit, current.Runtime.ConfigSHA256 = strings.Repeat(
					"b",
					40,
				), strings.Repeat(
					"3",
					64,
				)
				current.Runtime.GOOS = "darwin"
				trial.DriverCalls = []RoleCall{
					{Identity: RoleIdentity{Role: "user", Model: "other"}, PromptVersion: "user-v2"},
				}
			}
			writeAdmissionJSON(t, filepath.Join(current.ReportDir, current.Trials[0].File), trial)
			writeAdmissionJSON(t, filepath.Join(current.ReportDir, "run.json"), current)
			result, err := Compare(baseline.ReportDir, current.ReportDir)
			if change == "environment" {
				if err != nil || !result.CompatibleCases || !slices.Equal(
					result.EnvironmentChanges,
					[]string{
						"preset",
						"runtime_revision",
						"configuration",
						"platform",
						"grading_mode",
						"model_roles_or_prompts",
					},
				) {
					t.Fatal("environment changes were hidden or confused with case incompatibility", result, err)
				}
			} else if !errors.Is(err, ErrNotComparable) || result.CompatibleCases || result.Outcome != "compared" ||
				change == "case_changed" && result.Cases[0].Status != "case_changed" ||
				change == "added_and_removed" && (len(result.Cases) != 2 || result.Cases[0].Status != "removed" ||
					result.Cases[1].Status != "added") {
				t.Fatal("incompatible cohort silently compared", result, err)
			}
		})
	}
}

func TestCompareRejectsIncompleteUnsafeOrContradictoryRetainedReports(t *testing.T) {
	for _, change := range []string{
		"incomplete", "missing_trial", "duplicate_trial", "missing_repetition", "ordinal", "trial_ordinal",
		"duplicate_session", "coverage", "verdict", "summary", "hard_failure", "private_judge_failure",
		"unknown_execution", "unknown_verdict", "file_path", "public_report", "public_trial", "public_directory",
		"symlink_trial", "unknown_json", "duplicate_json",
	} {
		t.Run(change, func(t *testing.T) {
			report := comparisonFixture(t, Pass, Pass)
			trial := comparisonTrial(t, report, 1)
			trialPath := filepath.Join(report.ReportDir, report.Trials[1].File)
			switch change {
			case "incomplete":
				report.Complete = false
			case "missing_trial":
				if err := os.Remove(trialPath); err != nil {
					t.Fatal(err)
				}
			case "duplicate_trial":
				report.Trials[1] = report.Trials[0]
			case "missing_repetition":
				report.Trials = report.Trials[:1]
				report.Coverage[Pass] = 1
			case "ordinal":
				report.Trials[1].Repetition = 1
			case "trial_ordinal":
				trial.Repetition = 1
			case "duplicate_session":
				trial.SessionID = comparisonTrial(t, report, 0).SessionID
			case "coverage":
				report.Coverage[Pass] = 1
			case "verdict":
				report.Verdict = Fail
			case "summary":
				trial.Code = "different_code"
			case "hard_failure":
				trial.Checks[0].Verdict = Fail
			case "private_judge_failure":
				trial.Judge = &Judgement{
					Version: Version, Code: "private_judge_output", Verdict: Fail,
					Findings: JudgeFindings{Version: Version, Criteria: []JudgeFinding{}},
				}
			case "unknown_execution":
				trial.ExecutionVerdict = "invented"
			case "unknown_verdict":
				report.Trials[1].Verdict = "invented"
			case "file_path":
				report.Trials[1].File = "../private-evidence-canary"
			}
			if change != "missing_trial" {
				writeAdmissionJSON(t, trialPath, trial)
			}
			writeAdmissionJSON(t, filepath.Join(report.ReportDir, "run.json"), report)
			switch change {
			case "public_report":
				if err := os.Chmod(filepath.Join(report.ReportDir, "run.json"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "public_trial":
				if err := os.Chmod(trialPath, 0o644); err != nil {
					t.Fatal(err)
				}
			case "public_directory":
				if err := os.Chmod(report.ReportDir, 0o755); err != nil {
					t.Fatal(err)
				}
			case "symlink_trial":
				if err := os.Remove(trialPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("run.json", trialPath); err != nil {
					t.Fatal(err)
				}
			case "unknown_json", "duplicate_json":
				data, _ := json.Marshal(report)
				member := `"private-evidence-canary":true,`
				if change == "duplicate_json" {
					member = `"complete":false,`
				}
				data = append([]byte("{"+member), data[1:]...)
				if err := os.WriteFile(filepath.Join(report.ReportDir, "run.json"), data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			result, err := Compare(report.ReportDir, report.ReportDir)
			if !errors.Is(err, ErrCompare) || result.Outcome != "" ||
				strings.Contains(err.Error(), "private-evidence-canary") {
				t.Fatal("unsafe report yielded a comparison or echoed evidence", result, err)
			}
		})
	}
}

func TestCompareAcceptsLegacySingleTrialOrdinals(t *testing.T) {
	report := comparisonFixture(t, Inconclusive)
	report.Repeat, report.Trials[0].Repetition = 0, 0
	trial := comparisonTrial(t, report, 0)
	trial.Repetition = 0
	writeAdmissionJSON(t, filepath.Join(report.ReportDir, report.Trials[0].File), trial)
	writeAdmissionJSON(t, filepath.Join(report.ReportDir, "run.json"), report)
	result, err := Compare(report.ReportDir, report.ReportDir)
	if err != nil || result.Cases[0].Baseline.Trials != 1 || result.Cases[0].Baseline.Coverage[Inconclusive] != 1 {
		t.Fatal("legacy singleton lost its nonpassing outcome", result, err)
	}
}

func TestCompareReadsActualRunnerReportsWithoutAdditionalTasksOrProbes(t *testing.T) {
	fixture := newAdmissionFixture(t)
	item := loadedTestCase(t, "general.compare-runner")
	report, err := Run(t.Context(), fixture.preset, []LoadedCase{item}, RunOptions{
		Home: fixture.home, OutputParent: fixture.output, SuiteTimeout: 10 * time.Second, Repeat: 2,
	})
	if err != nil || !report.Complete {
		t.Fatal("offline native-protocol fixture failed", err)
	}
	tasks, probes := fixture.tasks.Load(), fixture.probes.Load()
	result, err := Compare(report.ReportDir, report.ReportDir)
	if err != nil || !result.CompatibleCases || result.Cases[0].Baseline.Coverage[Pass] != 2 ||
		fixture.tasks.Load() != tasks || fixture.probes.Load() != probes {
		t.Fatal("comparison lost actual run repetitions or contacted the target", result, err)
	}
}
