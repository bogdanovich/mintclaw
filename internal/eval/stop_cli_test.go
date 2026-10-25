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

func TestNativeStopUsesRealCLIAbortQueueAndReuse(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("prepared runtime admission currently supports Linux and macOS")
	}
	item := nativeStopDefinition(t)
	// A separately verified prebuilt CLI allows model-free controls on a
	// constrained Linux host. Ordinary CI still builds the current source.
	binary := os.Getenv("MINTCLAW_EVAL_NATIVE_TEST_CLI")
	if binary == "" {
		binary = filepath.Join(t.TempDir(), "mintclaw")
		repository, err := filepath.Abs("../..")
		if err != nil {
			t.Fatal(err)
		}
		buildSteeringTestCLI(t, binary, repository, nativeModelTestCommit)
	} else {
		info, err := os.Lstat(binary)
		if err != nil || !filepath.IsAbs(binary) || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			t.Fatal("invalid explicit native test CLI", err)
		}
	}
	fixture, parent := preparationFixture(t)
	enableTestActiveTool(fixture)
	fixture.preset.JudgeModel = "eval-model"
	fixture.preset.ActiveTool.MaxParallelTurns = 1
	fixture.cfg.Agents.Defaults.MaxParallelTurns, fixture.cfg.Agents.List[0].MaxParallelTurns = 1, 1
	fixture.cfg.Agents.Defaults.ContextManager = "seahorse"
	fixture.cfg.Agents.Defaults.ContextWindow, fixture.cfg.Agents.Defaults.MaxTokens = 16384, 512
	fixture.cfg.Agents.Defaults.MaxToolIterations, fixture.cfg.Agents.Defaults.MaxLLMRetries = 4, 1
	limits := targetBudgetTestLimits()
	limits.MaxCalls, limits.MaxInputBytes, limits.MaxOutputTokens, limits.MaxReportedTokens = 8, 131072, 512, 10000
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
			findings, _ := json.Marshal(JudgeFindings{Version: Version, Criteria: []JudgeFinding{{
				ID: "stop-and-reuse", Verdict: Pass, Reason: "Synthetic native control.",
				EvidenceIDs: []string{"event-000004"},
			}}})
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
			args, toolResult := activeToolArgs{}, false
			for _, entry := range body.Messages {
				toolResult = toolResult || entry.Role == "tool"
				var text string
				_ = json.Unmarshal(entry.Content, &text)
				for _, line := range strings.Split(text, "\n") {
					if data, ok := strings.CutPrefix(line, "Eval tool arguments: "); ok {
						_ = json.Unmarshal([]byte(data), &args)
					}
				}
			}
			if args.TrialID != "" && !toolResult {
				arguments, _ := json.Marshal(args)
				message = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
					"id": "read-" + args.TrialID, "type": "function", "function": map[string]any{
						"name": activeToolNativeName, "arguments": string(arguments),
					},
				}}}
				finish = "tool_calls"
			} else {
				answer := "unexpected continuation"
				last := body.Messages[len(body.Messages)-1]
				var text string
				_ = json.Unmarshal(last.Content, &text)
				if last.Role == "user" && strings.Contains(text, item.Case.NativeStop.ReuseMarker) {
					answer = item.Case.NativeStop.ReuseMarker
				}
				message = map[string]any{"role": "assistant", "content": answer}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "synthetic-native-stop", "model": "synthetic-model",
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
		t.Fatal("prepare native stop profile", err)
	}
	corpus := t.TempDir()
	writeCase(t, corpus, "stop.json", item.Case)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "eval", "run", "--root", corpus, "--preset", prepared.PresetPath,
		"--out", filepath.Join(prepared.Home, "reports"), "--start-target", "--json")
	command.Env = []string{
		"HOME=" + prepared.Home, config.EnvHome + "=" + prepared.Home, config.EnvConfig + "=" + prepared.ConfigPath,
		"PATH=" + os.Getenv("PATH"), "LANG=C", "MINTCLAW_LOG_LEVEL=info", "MINTCLAW_LOG_FORMAT=json",
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
		t.Fatalf("native stop report: %v\n%s\n%s", runErr, output, stderr.Bytes())
	}
	data, err := os.ReadFile(filepath.Join(report.ReportDirectory, report.Trials[0].File))
	var trial Trial
	if err != nil || json.Unmarshal(data, &trial) != nil {
		t.Fatal("native stop trial", err)
	}
	if runErr != nil || report.Verdict != Pass || trial.Verdict != Pass || targets.Load() != 2 || judges.Load() != 1 ||
		!validNativeStopEvidence(trial, item.Case, prepared.Workspace, report.ActiveTool.Profile) {
		shape, shapeOK := inspectNativeStopShape(trial)
		_, abortOK := inspectNativeStopAbort(trial.NativeStop, trial.AgentID, trial.SessionKey, trial.SessionID)
		if shapeOK && trial.NativeStop != nil {
			t.Logf(
				"stop oracle gates: shape=%t reply=%t route=%t abort=%t reuse=%t delivery=%t events=%+v",
				shapeOK,
				validNativeStopReply(trial, shape.stopped, prepared.Workspace),
				validTrialConversation(trial, trial.AgentID, prepared.Workspace),
				abortOK,
				validNativeStopReuseLog(trial.NativeStop, trial, shape.final),
				validCoreFinalDeliveries(
					trial.NativeStop.Outbox,
					trial.NativeStop.OutboxNames,
					trial,
					prepared.Workspace,
				),
				trial.Events,
			)
		}
		t.Fatalf("native stop: %v verdict=%s trial=%s/%s proof=%+v checks=%+v targets=%d judges=%d\n%s",
			runErr, report.Verdict, trial.Verdict, trial.Code, trial.NativeStop, trial.Checks,
			targets.Load(), judges.Load(), stderr.Bytes())
	}
	assertNativeStopFrozenControls(t, trial, item.Case, report.RunReport, prepared.Workspace)
	for _, name := range []string{"sessions", "state/outbox", "state/diagnostics/traces"} {
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
		targets.Load() != 2 || judges.Load() != 2 {
		t.Fatalf("offline native stop grade: %v\n%s", gradeErr, gradedOutput)
	}
	trial.NativeStop = nil
	invalid, _ := json.Marshal(trial)
	if os.WriteFile(filepath.Join(report.ReportDirectory, report.Trials[0].File), invalid, 0o600) != nil {
		t.Fatal("remove frozen native stop proof")
	}
	refused := exec.CommandContext(ctx, binary, grade.Args[1:]...)
	refused.Env = command.Env
	if output, err := refused.CombinedOutput(); err == nil || targets.Load() != 2 || judges.Load() != 2 {
		t.Fatalf("missing native stop proof reached judge: %v\n%s", err, output)
	}
}
