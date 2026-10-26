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

func TestRestartUsesRealCLIAndFrozenGenerationChain(t *testing.T) {
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
	item := restartDefinition(t)
	fixture, parent := preparationFixture(t)
	enableTestRestart(t, fixture)
	fixture.cfg.Agents.Defaults.ContextWindow, fixture.cfg.Agents.Defaults.MaxTokens = 16384, 128
	fixture.cfg.Agents.Defaults.MaxLLMRetries = 1
	fixture.preset.TargetCallBudget.MaxCalls = 12
	var targets, recalls, judges atomic.Int32
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
					ID:          "restart-recall",
					Verdict:     Pass,
					Reason:      "Synthetic native control.",
					EvidenceIDs: []string{"event-000004"},
				},
			}})
			content = string(finding)
		} else {
			targets.Add(1)
			var body struct {
				Messages []struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				} `json:"messages"`
			}
			if json.Unmarshal(raw, &body) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			var current json.RawMessage
			for _, message := range body.Messages {
				if message.Role == "user" {
					current = message.Content
				}
			}
			if bytes.Contains(current, []byte(item.Case.Restart.Recall)) {
				content = "Missing context."
				if bytes.Contains(raw, []byte(item.Case.Restart.Marker)) {
					recalls.Add(1)
					content = item.Case.Restart.Marker
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "synthetic-restart", "model": "synthetic-model",
			"choices": []any{map[string]any{
				"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": content},
			}},
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
	writeCase(t, corpus, "restart.json", item.Case)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "eval", "run", "--root", corpus, "--preset", prepared.PresetPath,
		"--out", filepath.Join(prepared.Home, "reports"), "--start-target", "--repeat", "3", "--json")
	command.Env = []string{
		"HOME=" + prepared.Home, config.EnvHome + "=" + prepared.Home,
		config.EnvConfig + "=" + prepared.ConfigPath, "PATH=" + os.Getenv("PATH"), "LANG=C",
		"MINTCLAW_LOG_LEVEL=warn", "MINTCLAW_LOG_FORMAT=json",
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
		t.Fatalf("native restart report: %v\n%s\n%s", runErr, output, stderr.Bytes())
	}
	var first Trial
	for index, summary := range report.Trials {
		data, err := os.ReadFile(filepath.Join(report.ReportDirectory, summary.File))
		var trial Trial
		if err != nil || json.Unmarshal(data, &trial) != nil {
			t.Fatal("trial", err)
		}
		code := restartEvidenceCode(trial, item.Case, report.RunReport, prepared.Workspace)
		if runErr != nil || trial.Verdict != Pass || code != "" {
			t.Fatalf(
				"native restart: run=%v trial=%s/%s checks=%+v oracle=%s generations=%d validity=%v verdict=%s\n%s",
				runErr,
				trial.Verdict,
				trial.Code,
				trial.Checks,
				code,
				len(report.TargetGenerations),
				validTargetGenerations(report.RunReport),
				targetGenerationsVerdict(report.RunReport),
				stderr.Bytes(),
			)
		}
		if index == 0 {
			first = trial
		} else if trial.SessionID == first.SessionID {
			t.Fatal("reused trial session")
		}
	}
	if report.Verdict != Pass || len(report.TargetGenerations) != 4 || targets.Load() != 6 || recalls.Load() != 3 ||
		judges.Load() != 3 {
		t.Fatal(
			"restart was not actual four-process bounded work",
			report.Verdict,
			len(report.TargetGenerations),
			targets.Load(),
			recalls.Load(),
			judges.Load(),
		)
	}
	assertRestartFrozenControls(t, first, item.Case, report.RunReport, prepared.Workspace)
	for _, name := range []string{"sessions", "state/outbox", "state/diagnostics/traces"} {
		if os.RemoveAll(filepath.Join(prepared.Workspace, name)) != nil {
			t.Fatal("remove own live evidence")
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
	gradedOutput, gradeErr := grade.CombinedOutput()
	var graded RunReport
	if gradeErr != nil || json.Unmarshal(gradedOutput, &graded) != nil || graded.Verdict != Pass ||
		targets.Load() != 6 || judges.Load() != 6 || !validTargetGenerations(graded) {
		t.Fatalf("offline restart grade: %v\n%s", gradeErr, gradedOutput)
	}
	first.Restart = nil
	invalid, _ := json.Marshal(first)
	if os.WriteFile(filepath.Join(report.ReportDirectory, report.Trials[0].File), invalid, 0o600) != nil {
		t.Fatal("remove own frozen proof")
	}
	refused := exec.CommandContext(ctx, binary, grade.Args[1:]...)
	refused.Env = command.Env
	if _, err := refused.CombinedOutput(); err == nil || targets.Load() != 6 || judges.Load() != 6 {
		t.Fatal("missing frozen restart reached judge", err)
	}
}

func assertRestartFrozenControls(t *testing.T, original Trial, item Case, report RunReport, workspace string) {
	t.Helper()
	for _, kind := range []string{
		"missing", "no_restart", "history_hash", "history_replay", "scope", "budget_reset",
		"same_pid", "same_instance", "same_nonce", "unclean", "unknown_budget", "missing_models", "wrong_generation", "missing_receipt", "old_trace",
	} {
		t.Run(kind, func(t *testing.T) {
			data, _ := json.Marshal(original)
			var trial Trial
			if json.Unmarshal(data, &trial) != nil {
				t.Fatal("clone trial")
			}
			data, _ = json.Marshal(report)
			var candidate RunReport
			if json.Unmarshal(data, &candidate) != nil {
				t.Fatal("clone report")
			}
			switch kind {
			case "missing":
				trial.Restart = nil
			case "no_restart":
				trial.Restart.AfterGeneration = trial.Restart.BeforeGeneration
			case "history_hash":
				trial.Restart.Before.HistorySHA256 = digest(nil)
			case "history_replay":
				trial.Restart.After.History = append(trial.Restart.After.History, trial.Restart.Before.History...)
			case "scope":
				trial.Events[3].TraceScopes[0].Workspace = "/foreign/workspace"
			case "budget_reset":
				candidate.TargetGenerations[1].Budget.Spec.Limits = candidate.TargetGenerations[0].Budget.Spec.Limits
				budget := candidate.TargetGenerations[1].Budget
				var proof evaltarget.Proof
				if json.Unmarshal([]byte(budget.Proof), &proof) != nil {
					t.Fatal("native receipt clone")
				}
				proof.Snapshot.Limits = budget.Spec.Limits
				raw, err := json.Marshal(proof)
				if err != nil {
					t.Fatal(err)
				}
				budget.Proof, budget.ProofSHA256 = string(raw), digest(raw)
			case "same_pid":
				candidate.TargetGenerations[1].Runtime.PID = candidate.TargetGenerations[0].Runtime.PID
			case "same_instance":
				candidate.TargetGenerations[1].Runtime.InstanceSHA256 = candidate.TargetGenerations[0].Runtime.InstanceSHA256
			case "same_nonce":
				candidate.TargetGenerations[1].Budget.Spec.Nonce = candidate.TargetGenerations[0].Budget.Spec.Nonce
			case "unclean":
				candidate.TargetGenerations[0].Lifecycle.Forced = true
			case "unknown_budget":
				candidate.TargetGenerations[0].Budget.Code = "receipt_unavailable"
			case "missing_models":
				candidate.TargetGenerations[0].Models = nil
			case "wrong_generation":
				trial.Events[3].Generation = trial.Events[1].Generation
			case "missing_receipt":
				candidate.TargetGenerations = nil
			case "old_trace":
				candidate.TargetGenerations[1].Models = candidate.TargetGenerations[0].Models
			}
			for _, check := range gradeRestartChecks(item, trial, candidate, workspace) {
				if check.ID == "native-restart" && check.Verdict == Pass {
					t.Fatal("invalid restart proof accepted", kind)
				}
			}
		})
	}
}
