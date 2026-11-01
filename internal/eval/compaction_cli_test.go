package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/internal/evaltarget"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestCompactionUsesRealCLIAndNativeCompletionBarrier(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("prepared runtime admission currently supports Linux and macOS")
	}
	binary := os.Getenv("MINTCLAW_EVAL_NATIVE_TEST_CLI")
	if binary == "" {
		binary = filepath.Join(t.TempDir(), "mintclaw")
		repository, err := filepath.Abs("../..")
		if err != nil {
			t.Fatal(err)
		}
		buildSteeringTestCLI(t, binary, repository, nativeModelTestCommit)
	}
	item := compactionDefinition(t)
	fixture, parent := preparationFixture(t)
	enableTestCompaction(t, fixture)
	var targets, summaries, judges, recallsFromCheckpoint atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		raw, err := io.ReadAll(http.MaxBytesReader(w, request.Body, 1<<20))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		content := "Noted."
		if bytes.Contains(raw, []byte("You are an independent conversation judge")) {
			judges.Add(1)
			finding, _ := json.Marshal(JudgeFindings{Version: Version, Criteria: []JudgeFinding{
				{
					ID:          "corrected-recall",
					Verdict:     Pass,
					Reason:      "Synthetic native control.",
					EvidenceIDs: []string{"event-000010"},
				},
			}})
			content = string(finding)
		} else {
			var body struct {
				MaxTokens     int `json:"max_tokens"`
				MaxCompletion int `json:"max_completion_tokens"`
				Messages      []struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				} `json:"messages"`
			}
			if json.Unmarshal(raw, &body) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if body.MaxTokens > 512 || body.MaxCompletion > 512 {
				summaries.Add(1)
				content = "Current code: " + item.Case.Compaction.OldMarker + "."
				if bytes.Contains(raw, []byte(item.Case.Compaction.Marker)) {
					content = "Current code: " + item.Case.Compaction.Marker + "."
				}
			} else {
				targets.Add(1)
				var current json.RawMessage
				for _, message := range body.Messages {
					if message.Role == "user" {
						current = message.Content
					}
				}
				if bytes.Contains(current, []byte(item.Case.Compaction.Recall)) {
					content = "Missing current code."
					if bytes.Contains(raw, []byte(item.Case.Compaction.Marker)) {
						content = item.Case.Compaction.Marker
						if !bytes.Contains(raw, []byte(item.Case.Compaction.Correction)) {
							recallsFromCheckpoint.Add(1)
						}
					}
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "synthetic-compaction",
			"model": "synthetic-model",
			"choices": []any{
				map[string]any{
					"index":         0,
					"finish_reason": "stop",
					"message":       map[string]any{"role": "assistant", "content": content},
				},
			},
			"usage": map[string]any{"prompt_tokens": 7, "completion_tokens": 2, "total_tokens": 9},
		})
	}))
	defer model.Close()
	fixture.cfg.ModelList[0].APIBase = model.URL + "/v1"
	if _, err := config.NewRepository(fixture.preset.ConfigPath).Save(fixture.cfg); err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareProfile(t.Context(), fixture.preset, fixture.home, parent)
	if err != nil {
		t.Fatal("prepare", err)
	}
	corpus := t.TempDir()
	writeCase(t, corpus, "compaction.json", item.Case)
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "eval", "run", "--root", corpus, "--preset", prepared.PresetPath,
		"--out", filepath.Join(prepared.Home, "reports"), "--start-target", "--repeat", "3", "--json")
	command.Env = []string{
		"HOME=" + prepared.Home, config.EnvHome + "=" + prepared.Home,
		config.EnvConfig + "=" + prepared.ConfigPath, "PATH=" + os.Getenv("PATH"), "LANG=C",
		"MINTCLAW_LOG_LEVEL=info", "MINTCLAW_LOG_FORMAT=json",
	}
	command.Cancel = func() error { return command.Process.Signal(os.Interrupt) }
	command.WaitDelay = 40 * time.Second
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, runErr := command.Output()
	var report struct {
		RunReport
		ReportDirectory string `json:"report_dir"`
	}
	if json.Unmarshal(output, &report) != nil || len(report.Trials) != 3 {
		t.Fatalf("native report: %v\n%s\n%s", runErr, output, stderr.Bytes())
	}
	var first Trial
	for index, summary := range report.Trials {
		data, err := os.ReadFile(filepath.Join(report.ReportDirectory, summary.File))
		var trial Trial
		if err != nil || json.Unmarshal(data, &trial) != nil {
			t.Fatal("trial", err)
		}
		code := compactionEvidenceCode(trial, item.Case, prepared.Workspace)
		if runErr != nil || trial.Verdict != Pass || code != "" {
			t.Fatalf(
				"native compaction: run=%v verdict=%s/%s execution=%s/%s checks=%+v oracle=%s\n%s",
				runErr,
				trial.Verdict,
				trial.Code,
				trial.ExecutionVerdict,
				trial.ExecutionCode,
				trial.Checks,
				code,
				stderr.Bytes(),
			)
		}
		if index == 0 {
			first = trial
		} else if trial.SessionID == first.SessionID {
			t.Fatal("reused native session")
		}
	}
	if report.Verdict != Pass || targets.Load() != 15 || summaries.Load() < 3 || judges.Load() != 3 ||
		recallsFromCheckpoint.Load() != 3 {
		t.Fatal(
			"native pressure/compaction not observed",
			report.Verdict,
			targets.Load(),
			summaries.Load(),
			judges.Load(),
			recallsFromCheckpoint.Load(),
		)
	}
	if report.TargetBudget == nil {
		t.Fatal("native budget unavailable")
	}
	budget, err := evaltarget.DecodeProof(
		[]byte(report.TargetBudget.Proof),
		report.TargetBudget.Spec,
		report.Runtime.PID,
	)
	actual := int(targets.Load() + summaries.Load())
	if err != nil || budget.Snapshot.Calls != actual || budget.Snapshot.ReportedTokens != int64(actual*9) {
		t.Fatal("auxiliary summary calls escaped the native budget", err, budget.Snapshot.Calls, actual)
	}
	if report.NativeModels == nil {
		t.Fatal("native target metadata missing")
	}
	if report.NativeModels.Code != "observed" || report.NativeModels.Totals.Requests != 15 {
		for _, turn := range report.NativeModels.Turns {
			if turn.Code != "observed" {
				t.Logf(
					"native model projection: code=%s turn=%s calls=%d",
					turn.Code,
					turn.TraceScope.TurnID,
					len(turn.Calls),
				)
			}
		}
		t.Fatalf(
			"native target metadata: code=%s reason=%s roots=%d totals=%+v",
			report.NativeModels.Code,
			report.NativeModels.Reason,
			len(report.NativeModels.Turns),
			report.NativeModels.Totals,
		)
	}
	assertCompactionFrozenControls(t, first, item.Case, report.RunReport, prepared.Workspace)
	for _, name := range []string{"sessions", "state/outbox", "state/diagnostics/traces"} {
		if os.RemoveAll(filepath.Join(prepared.Workspace, name)) != nil {
			t.Fatal("remove own stores")
		}
	}
	logs, err := filepath.Glob(filepath.Join(prepared.Home, "eval-gateway-*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, log := range logs {
		if os.Remove(log) != nil {
			t.Fatal("remove own log")
		}
	}
	grade := exec.CommandContext(ctx, binary, "eval", "grade", report.ReportDirectory, "--root", corpus,
		"--preset", prepared.PresetPath, "--out", filepath.Join(prepared.Home, "reports"), "--json")
	grade.Env = command.Env
	beforeSummaries := summaries.Load()
	gradedOutput, gradeErr := grade.CombinedOutput()
	var graded RunReport
	if gradeErr != nil || json.Unmarshal(gradedOutput, &graded) != nil || graded.Verdict != Pass ||
		targets.Load() != 15 || summaries.Load() != beforeSummaries || judges.Load() != 6 {
		t.Fatalf("offline grade: %v\n%s", gradeErr, gradedOutput)
	}
	first.Compaction = nil
	invalid, _ := json.Marshal(first)
	if os.WriteFile(filepath.Join(report.ReportDirectory, report.Trials[0].File), invalid, 0o600) != nil {
		t.Fatal("remove own proof")
	}
	refused := exec.CommandContext(ctx, binary, grade.Args[1:]...)
	refused.Env = command.Env
	if _, err := refused.CombinedOutput(); err == nil || targets.Load() != 15 || summaries.Load() != beforeSummaries ||
		judges.Load() != 6 {
		t.Fatal("missing compaction proof reached model", err)
	}
}
