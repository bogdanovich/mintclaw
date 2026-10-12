package eval

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

func gradeFixture(t *testing.T, hardFailure bool) (*admissionFixture, LoadedCase, RunReport, *atomic.Int32) {
	t.Helper()
	return gradeRepeatedFixture(t, hardFailure, 1)
}

func gradeRepeatedFixture(
	t *testing.T, hardFailure bool, repeat int,
) (*admissionFixture, LoadedCase, RunReport, *atomic.Int32) {
	t.Helper()
	fixture := newAdmissionFixture(t)
	item := loadedTestCase(t, "general.grade-source")
	item.Case.Rubric = []Criterion{
		{ID: "coherent", Description: "Visible assistant replies are coherent.", Required: true},
	}
	if hardFailure {
		item.Case.Checks = append(
			item.Case.Checks,
			Check{ID: "missing", Kind: "response_contains", Expected: "not-present"},
		)
	}
	data, _ := json.Marshal(item.Case)
	item.Digest = digest(data)
	result, err := Run(t.Context(), fixture.preset, []LoadedCase{item}, RunOptions{
		Home: fixture.home, OutputParent: fixture.output, SuiteTimeout: 10 * time.Second, Repeat: repeat,
	})
	if !errors.Is(err, ErrNotPassed) || !result.Complete || len(result.Trials) != repeat {
		t.Fatal("unjudged source run was not retained", err)
	}
	count := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			w.WriteHeader(400)
			return
		}
		if _, tools := body["tools"]; tools {
			w.WriteHeader(400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{
				map[string]any{
					"message":       map[string]any{"role": "assistant", "content": adaptiveJudgePass},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]any{"prompt_tokens": 10252, "completion_tokens": 20, "total_tokens": 10272},
		})
	}))
	t.Cleanup(server.Close)
	fixture.cfg.ModelList[0].APIBase = server.URL
	fixture.cfg.ModelList[0].APIKeys = config.SimpleSecureStrings("synthetic-grading-credential")
	fixture.preset.JudgeModel = "eval-model"
	fixture.writeConfig(t)
	if err = os.WriteFile(filepath.Join(fixture.home, "eval-grading-key"),
		[]byte("synthetic-grading-credential"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(fixture.home, ".security.yml"),
		[]byte("model_list:\n  eval-model:\n    api_keys:\n      - file://eval-grading-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(fixture.home, ".mintclaw.pid")); err != nil {
		t.Fatal(err)
	}
	return fixture, item, result, count
}

func TestGradeRetainsAndRejudgesEveryIndependentRepetition(t *testing.T) {
	for _, hardFailure := range []bool{false, true} {
		fixture, item, source, count := gradeRepeatedFixture(t, hardFailure, 3)
		tasks, probes := fixture.tasks.Load(), fixture.probes.Load()
		result, err := Grade(t.Context(), fixture.preset, []LoadedCase{item}, source.ReportDir, RunOptions{
			Home: fixture.home, OutputParent: fixture.output, SuiteTimeout: 10 * time.Second,
		})
		want := Pass
		if hardFailure {
			want = Fail
		}
		if !hardFailure && err != nil || hardFailure && !errors.Is(err, ErrNotPassed) ||
			result.Verdict != want || !result.Complete || result.Repeat != 3 || len(result.Trials) != 3 ||
			count.Load() != 3 || result.Coverage[want] != 3 || fixture.tasks.Load() != tasks ||
			fixture.probes.Load() != probes || result.RoleUsage[0].InputTokens != 3*10252 {
			t.Fatal("regrade lost a repetition, duplicated work or hid a failure", err)
		}
		for ordinal, summary := range result.Trials {
			if summary.ID != source.Trials[ordinal].ID || summary.Repetition != ordinal+1 {
				t.Fatal("regrade replaced source trial identity")
			}
		}
	}
}

func TestGradeRetainsOwnedGatewayCleanupUncertaintyWithoutTargetWork(t *testing.T) {
	fixture, item, source, count := gradeFixture(t, false)
	exitCode := 9
	source.TargetLifecycle = &TargetLifecycle{
		Mode: "owned_gateway", PID: 1234, Admitted: true, StopRequested: true,
		ExitObserved: true, ExitCode: &exitCode, Code: "abnormal_exit",
	}
	writeAdmissionJSON(t, filepath.Join(source.ReportDir, "run.json"), source)
	tasks, probes := fixture.tasks.Load(), fixture.probes.Load()
	result, err := Grade(t.Context(), fixture.preset, []LoadedCase{item}, source.ReportDir, RunOptions{
		Home: fixture.home, OutputParent: fixture.output, StartTarget: true,
	})
	if !errors.Is(err, ErrNotPassed) || result.Verdict != Inconclusive || !result.Complete ||
		result.TargetLifecycle == nil || result.TargetLifecycle.Code != "abnormal_exit" ||
		result.Coverage[Pass] != 1 || count.Load() != 1 || fixture.tasks.Load() != tasks || fixture.probes.Load() != probes {
		t.Fatal("regrade erased source cleanup uncertainty or started target work", err)
	}
	if _, err = Compare(result.ReportDir, result.ReportDir); err != nil {
		t.Fatal("retained regrade lifecycle could not be compared", err)
	}
}

func TestGradeRejectsMissingDuplicatedOrMismatchedRepetitionsBeforeAnyCall(t *testing.T) {
	for _, change := range []string{
		"missing", "duplicate-id", "duplicate-session", "duplicate-ordinal", "ordinal", "trial-ordinal", "budget",
	} {
		t.Run(change, func(t *testing.T) {
			fixture, item, source, count := gradeRepeatedFixture(t, false, 2)
			switch change {
			case "missing":
				source.Trials = source.Trials[:1]
			case "duplicate-id":
				source.Trials[1].ID, source.Trials[1].File = source.Trials[0].ID, source.Trials[0].File
			case "duplicate-ordinal":
				source.Trials[1].Repetition = 1
			case "ordinal":
				source.Trials[1].Repetition = 3
			case "budget":
				source.Repeat = MaxRepeat + 1
			default:
				path := filepath.Join(source.ReportDir, source.Trials[1].File)
				data, err := os.ReadFile(path)
				var trial Trial
				if err != nil || json.Unmarshal(data, &trial) != nil {
					t.Fatal("invalid test source")
				}
				if change == "trial-ordinal" {
					trial.Repetition = 1
				} else {
					firstData, err := os.ReadFile(filepath.Join(source.ReportDir, source.Trials[0].File))
					var first Trial
					if err != nil || json.Unmarshal(firstData, &first) != nil {
						t.Fatal("invalid first source trial")
					}
					trial.SessionID = first.SessionID
				}
				writeAdmissionJSON(t, path, trial)
			}
			writeAdmissionJSON(t, filepath.Join(source.ReportDir, "run.json"), source)
			result, err := Grade(t.Context(), fixture.preset, []LoadedCase{item}, source.ReportDir, RunOptions{
				Home: fixture.home, OutputParent: fixture.output,
			})
			if !errors.Is(err, ErrGrade) || result.ID != "" || count.Load() != 0 {
				t.Fatal("invalid repetition source reached a judge", change)
			}
		})
	}
}

func TestGradePreservesLegacySingleTrialSourceWithoutRepeatFields(t *testing.T) {
	fixture, item, source, count := gradeFixture(t, false)
	source.Repeat, source.Trials[0].Repetition = 0, 0
	path := filepath.Join(source.ReportDir, source.Trials[0].File)
	data, err := os.ReadFile(path)
	var trial Trial
	if err != nil || json.Unmarshal(data, &trial) != nil {
		t.Fatal("invalid test source")
	}
	trial.Repetition = 0
	writeAdmissionJSON(t, path, trial)
	writeAdmissionJSON(t, filepath.Join(source.ReportDir, "run.json"), source)
	result, err := Grade(t.Context(), fixture.preset, []LoadedCase{item}, source.ReportDir, RunOptions{
		Home: fixture.home, OutputParent: fixture.output,
	})
	if err != nil || result.Verdict != Pass || count.Load() != 1 || len(result.Trials) != 1 {
		t.Fatal("legacy single-trial evidence became incompatible", err)
	}
}

func TestGradeUsesRetainedEvidenceAndNeverConnectsToGateway(t *testing.T) {
	for _, hardFailure := range []bool{false, true} {
		fixture, item, source, count := gradeFixture(t, hardFailure)
		tasks, probes := fixture.tasks.Load(), fixture.probes.Load()
		result, err := Grade(t.Context(), fixture.preset, []LoadedCase{item}, source.ReportDir, RunOptions{
			Home: fixture.home, OutputParent: fixture.output, SuiteTimeout: 10 * time.Second,
		})
		want := Pass
		if hardFailure {
			want = Fail
		}
		if !hardFailure && err != nil || hardFailure && !errors.Is(err, ErrNotPassed) || result.Verdict != want ||
			!result.Complete || result.Mode != "grade" || result.SourceRun != source.ID || count.Load() != 1 ||
			fixture.tasks.Load() != tasks || fixture.probes.Load() != probes || len(result.RoleUsage) != 1 ||
			result.RoleUsage[0].InputTokens != 10252 {
			t.Fatal("grade reran a task, hid a hard failure, lost usage, or failed", result.Verdict, err)
		}
		summary, readErr := os.ReadFile(filepath.Join(result.ReportDir, "summary.md"))
		if readErr != nil || !strings.Contains(string(summary), "10.2k") ||
			!strings.Contains(string(summary), "no target or tool work") {
			t.Fatal("compact summary missing")
		}
		assertPrivateReport(t, result)
	}
}

func TestGradeRejectsIneligibleOrChangedSourceBeforeJudgeCall(t *testing.T) {
	for _, change := range []string{
		"digest", "permissions", "eligibility", "credential", "escaped-credential", "privacy-failure", "incomplete",
	} {
		t.Run(change, func(t *testing.T) {
			fixture, item, source, count := gradeFixture(t, false)
			file := filepath.Join(source.ReportDir, source.Trials[0].File)
			switch change {
			case "digest":
				item.Digest = strings.Repeat("0", 64)
			case "permissions":
				if err := os.Chmod(file, 0o644); err != nil {
					t.Fatal(err)
				}
			case "incomplete":
				data, err := os.ReadFile(filepath.Join(source.ReportDir, "run.json"))
				if err != nil {
					t.Fatal(err)
				}
				var report RunReport
				if json.Unmarshal(data, &report) != nil {
					t.Fatal("decode")
				}
				report.Complete = false
				writeAdmissionJSON(t, filepath.Join(source.ReportDir, "run.json"), report)
			default:
				data, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				var trial Trial
				if json.Unmarshal(data, &trial) != nil {
					t.Fatal("decode")
				}
				switch change {
				case "eligibility":
					trial.EvidenceEligible = false
				case "escaped-credential":
					trial.Events[1].Content = "Key: synthetic\\u002dgrading-credential\nOrdinary prose follows."
				case "privacy-failure":
					trial.Judge = &Judgement{
						Version: Version, Verdict: Fail, Code: "private_judge_output",
						Findings: JudgeFindings{Version: Version, Criteria: []JudgeFinding{}},
					}
				default:
					trial.Events[1].Content = "synthetic-grading-credential"
				}
				writeAdmissionJSON(t, file, trial)
			}
			result, err := Grade(t.Context(), fixture.preset, []LoadedCase{item}, source.ReportDir, RunOptions{
				Home: fixture.home, OutputParent: fixture.output,
			})
			if !errors.Is(err, ErrGrade) || result.ID != "" || count.Load() != 0 {
				t.Fatal("ineligible source reached a judge", change, err)
			}
		})
	}
}
