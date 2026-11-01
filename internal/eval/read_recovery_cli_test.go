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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestReadRecoveryUsesRealCLIAndFrozenPostStopEvidence(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("prepared runtime admission currently supports Linux and macOS")
	}
	item := readRecoveryDefinition(t)
	binary := filepath.Join(t.TempDir(), "mintclaw")
	repository, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	buildSteeringTestCLI(t, binary, repository, nativeModelTestCommit)
	fixture, parent := preparationFixture(t)
	enableTestReadTool(t, fixture)
	marker := item.Case.ReadRecovery.Marker
	fixture.preset.WorkspaceFiles[0].SHA256 = item.Case.Fixtures[0].SHA256
	if os.WriteFile(filepath.Join(fixture.workspace, readFixturePath), []byte(marker+"\n"), 0o600) != nil {
		t.Fatal("pin public synthetic fixture")
	}
	fixture.cfg.Agents.Defaults.ContextManager = "seahorse"
	fixture.cfg.Agents.Defaults.ContextWindow, fixture.cfg.Agents.Defaults.MaxTokens = 16384, 512
	fixture.cfg.Agents.Defaults.MaxToolIterations, fixture.cfg.Agents.Defaults.MaxLLMRetries = 4, 1
	limits := targetBudgetTestLimits()
	limits.MaxCalls, limits.MaxInputBytes, limits.MaxOutputTokens, limits.MaxReportedTokens = 16, 131072, 512, 10000
	fixture.preset.TargetCallBudget = &limits
	var targets, judges atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		raw, err := io.ReadAll(http.MaxBytesReader(w, request.Body, 1<<20))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var message map[string]any
		finish := "stop"
		if bytes.Contains(raw, []byte("You are an independent conversation judge")) {
			judges.Add(1)
			findings, _ := json.Marshal(JudgeFindings{Version: Version, Criteria: []JudgeFinding{
				{
					ID:          "grounded-recovery",
					Verdict:     Pass,
					Reason:      "Synthetic native control.",
					EvidenceIDs: []string{"event-000001"},
				},
			}})
			message = map[string]any{"role": "assistant", "content": string(findings)}
		} else {
			targets.Add(1)
			var body struct {
				Messages []struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				} `json:"messages"`
			}
			if json.Unmarshal(raw, &body) != nil || len(body.Messages) == 0 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			last := body.Messages[len(body.Messages)-1]
			var content string
			_ = json.Unmarshal(last.Content, &content)
			path := ""
			if last.Role == "user" {
				if strings.Contains(content, readMissingPath) {
					path = readMissingPath
				}
				if strings.Contains(content, readFixturePath) {
					path = readFixturePath
				}
			}
			if path != "" {
				arguments, _ := json.Marshal(map[string]string{"path": path})
				message = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
					"id": "read-" + strings.TrimSuffix(filepath.Base(path), ".txt"), "type": "function",
					"function": map[string]any{"name": "read_file", "arguments": string(arguments)},
				}}}
				finish = "tool_calls"
			} else {
				answer := "The file could not be read. Please provide the corrected path."
				if bytes.Contains(raw, []byte(marker)) {
					answer = marker
				}
				message = map[string]any{"role": "assistant", "content": answer}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "synthetic-read-recovery", "model": "synthetic-model",
			"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}},
			"usage":   map[string]any{"prompt_tokens": 7, "completion_tokens": 2, "total_tokens": 9},
		})
	}))
	defer model.Close()
	fixture.cfg.ModelList[0].APIBase = model.URL + "/v1"
	if _, err := config.NewRepository(fixture.preset.ConfigPath).Save(fixture.cfg); err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareProfile(t.Context(), fixture.preset, fixture.home, parent)
	if err != nil {
		t.Fatal("prepare native recovery profile", err)
	}
	corpus := filepath.Join(t.TempDir(), "corpus")
	if os.Mkdir(corpus, 0o700) != nil || os.Mkdir(filepath.Join(corpus, "fixtures"), 0o700) != nil {
		t.Fatal("corpus")
	}
	data := []byte(marker + "\n")
	localItem := item
	localItem.Case.Fixtures = []Fixture{
		{ID: "recovery", Path: "fixtures/recovery.txt", SHA256: digest(data), Kind: "file"},
	}
	if os.WriteFile(filepath.Join(corpus, "fixtures/recovery.txt"), data, 0o600) != nil {
		t.Fatal("corpus fixture")
	}
	writeCase(t, corpus, "recovery.json", localItem.Case)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "eval", "run", "--root", corpus, "--preset", prepared.PresetPath,
		"--out", filepath.Join(prepared.Home, "reports"), "--start-target", "--json")
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
	if json.Unmarshal(output, &report) != nil || len(report.Trials) != 1 {
		t.Fatalf("native report: %v\n%s\n%s", runErr, output, stderr.Bytes())
	}
	trialData, err := os.ReadFile(filepath.Join(report.ReportDirectory, report.Trials[0].File))
	var trial Trial
	if err != nil || json.Unmarshal(trialData, &trial) != nil {
		t.Fatal("native trial", err)
	}
	if runErr != nil || report.Verdict != Pass || trial.Verdict != Pass || targets.Load() != 5 || judges.Load() != 1 ||
		!validReadRecoveryEvidence(trial, localItem.Case, prepared.Workspace) {
		t.Fatalf(
			"native read recovery: %v verdict=%s trial=%s/%s proof=%+v checks=%+v targets=%d judges=%d\n%s",
			runErr,
			report.Verdict,
			trial.Verdict,
			trial.Code,
			trial.ReadRecovery,
			trial.Checks,
			targets.Load(),
			judges.Load(),
			stderr.Bytes(),
		)
	}
	assertReadRecoveryFrozenControls(t, trial, localItem.Case, report.RunReport, prepared.Workspace)
	for _, name := range []string{"sessions", readFixtureDirectory, "state/diagnostics/traces"} {
		if os.RemoveAll(filepath.Join(prepared.Workspace, name)) != nil {
			t.Fatal("remove owned live observations")
		}
	}
	if _, err := Compare(report.ReportDirectory, report.ReportDirectory); err != nil {
		t.Fatal("offline compare", err)
	}
	grade := exec.CommandContext(ctx, binary, "eval", "grade", report.ReportDirectory, "--root", corpus,
		"--preset", prepared.PresetPath, "--out", filepath.Join(prepared.Home, "reports"), "--json")
	grade.Env = command.Env
	gradedOutput, gradeErr := grade.CombinedOutput()
	var graded RunReport
	if gradeErr != nil || json.Unmarshal(gradedOutput, &graded) != nil || graded.Verdict != Pass ||
		targets.Load() != 5 || judges.Load() != 2 {
		t.Fatalf("offline recovery grade: %v\n%s", gradeErr, gradedOutput)
	}
	trial.ReadRecovery = nil
	invalid, _ := json.Marshal(trial)
	if os.WriteFile(filepath.Join(report.ReportDirectory, report.Trials[0].File), invalid, 0o600) != nil {
		t.Fatal("remove proof")
	}
	refused := exec.CommandContext(ctx, binary, grade.Args[1:]...)
	refused.Env = command.Env
	if output, err := refused.CombinedOutput(); err == nil || targets.Load() != 5 || judges.Load() != 2 {
		t.Fatalf("missing recovery proof reached judge: %v\n%s", err, output)
	}
}

func assertReadRecoveryFrozenControls(t *testing.T, original Trial, item Case, report RunReport, workspace string) {
	t.Helper()
	for _, kind := range []string{"missing", "before", "after", "missing_file_exists", "history_digest", "foreign_scope"} {
		t.Run(kind, func(t *testing.T) {
			data, _ := json.Marshal(original)
			var trial Trial
			if json.Unmarshal(data, &trial) != nil {
				t.Fatal("clone")
			}
			switch kind {
			case "missing":
				trial.ReadRecovery = nil
			case "before":
				trial.ReadRecovery.Before.SHA256 = digest([]byte("wrong-before"))
			case "after":
				trial.ReadRecovery.After.SHA256 = digest([]byte("changed-after"))
			case "missing_file_exists":
				trial.ReadRecovery.After.Entries = append(trial.ReadRecovery.After.Entries, "missing.txt")
			case "history_digest":
				trial.ReadRecovery.HistorySHA256 = digest([]byte("different history"))
			case "foreign_scope":
				trial.SessionKey = "foreign-scope"
			}
			if validReadRecoveryEvidence(trial, item, workspace) {
				t.Fatal("contradictory or missing read recovery passed")
			}
			for index, check := range item.Checks {
				if check.Kind == "read_recovery" &&
					gradeReadRecoveryChecks(item, trial, report, workspace)[index].Verdict == Pass {
					t.Fatal("passing stored judge overrode native recovery failure")
				}
			}
		})
	}
	for _, kind := range []string{"missing_budget", "unknown_budget", "missing_lifecycle", "truncated_log", "private_unknown"} {
		t.Run(kind, func(t *testing.T) {
			data, _ := json.Marshal(report)
			var broken RunReport
			if json.Unmarshal(data, &broken) != nil {
				t.Fatal("clone owned observation")
			}
			switch kind {
			case "missing_budget":
				broken.TargetBudget = nil
			case "unknown_budget":
				broken.TargetBudget.Code = "unavailable"
			case "missing_lifecycle":
				broken.TargetLifecycle = nil
			case "truncated_log":
				broken.TargetLifecycle.LogTruncated = true
			case "private_unknown":
				broken.PrivateSurfaces.Code = "unavailable"
			}
			trial := original
			trial.Judge = nil
			assessCoreConversation(t.Context(), nil, item, &trial, broken, workspace)
			if trial.Verdict == Pass || trial.EvidenceEligible || trial.Judge != nil {
				t.Fatal("missing global observation reached judging or passed")
			}
		})
	}
}
