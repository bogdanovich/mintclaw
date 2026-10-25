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

func buildSteeringTestCLI(t *testing.T, binary, root, commit string) {
	t.Helper()
	buildCtx, buildCancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer buildCancel()
	flags := "-X github.com/bogdanovich/mintclaw/pkg/config.Version=steering-native-test " +
		"-X github.com/bogdanovich/mintclaw/pkg/config.GitCommit=" + commit
	// This model-free fixture uses synthetic attribution, not checkout attestation.
	build := exec.CommandContext(
		buildCtx, "go", "build", "-buildvcs=false", "-tags", "goolm,stdjson", "-ldflags", flags,
		"-o", binary, "./cmd/mintclaw",
	)
	build.Dir, build.Env = root, append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build native steering CLI: %v\n%s", err, output)
	}
}

func TestSteeringCasesUseRealCLIAndNativeInFlightBoundary(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("prepared runtime admission currently supports Linux and macOS")
	}
	binary := filepath.Join(t.TempDir(), "mintclaw")
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	buildSteeringTestCLI(t, binary, root, nativeModelTestCommit)
	buildSteeringTestCLI(t, binary+"-no-source", root, "")
	for _, variant := range []struct {
		name, file, mode string
		missingSource    bool
	}{
		{"boundary", "steering-boundary.json", "one-at-a-time", false},
		{"order-one", "steering-order.json", "one-at-a-time", false},
		{"order-all", "steering-order.json", "all", false},
		{"missing-source", "steering-boundary.json", "one-at-a-time", true},
	} {
		t.Run(variant.name, func(t *testing.T) {
			definition, err := os.ReadFile(filepath.Join("../../evals/cases/general", variant.file))
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := DecodeManifest[Case](definition)
			if err != nil || parsed.Validate() != nil {
				t.Fatal("invalid steering definition", err)
			}
			item := LoadedCase{Case: parsed, Digest: digest(definition)}
			fixture, parent := preparationFixture(t)
			enableTestActiveTool(fixture)
			fixture.preset.ActiveTool.SteeringMode, fixture.cfg.Agents.Defaults.SteeringMode = variant.mode, variant.mode
			fixture.preset.JudgeModel = "eval-model"
			fixture.cfg.Agents.Defaults.ContextManager = "seahorse"
			fixture.cfg.Agents.Defaults.ContextWindow, fixture.cfg.Agents.Defaults.MaxTokens = 16384, 512
			fixture.cfg.Agents.Defaults.MaxToolIterations, fixture.cfg.Agents.Defaults.MaxLLMRetries = 6, 1
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
					message = map[string]any{
						"role":    "assistant",
						"content": `{"version":1,"criteria":[{"id":"grounded-steering","verdict":"pass","reason":"Synthetic native control.","evidence_ids":["event-000001"]}]}`,
					}
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
					toolResult, args := false, activeToolArgs{}
					for _, entry := range body.Messages {
						toolResult = toolResult || entry.Role == "tool"
						var text string
						_ = json.Unmarshal(entry.Content, &text)
						for _, line := range strings.Split(text, "\n") {
							if encoded, ok := strings.CutPrefix(line, "Eval tool arguments: "); ok {
								_ = json.Unmarshal([]byte(encoded), &args)
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
						marker := item.Case.Steering.InitialMarker
						for _, update := range item.Case.Steering.Updates {
							if bytes.Contains(raw, []byte(update)) {
								marker = update
							}
						}
						message = map[string]any{"role": "assistant", "content": marker}
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id": "synthetic-steering", "model": "synthetic-model",
					"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}},
					"usage":   map[string]any{"prompt_tokens": 7, "completion_tokens": 2, "total_tokens": 9},
				})
			}))
			defer model.Close()
			fixture.cfg.ModelList[0].APIBase = model.URL
			if _, err := config.NewRepository(fixture.preset.ConfigPath).Save(fixture.cfg); err != nil {
				t.Fatal(err)
			}
			prepared, err := PrepareProfile(t.Context(), fixture.preset, fixture.home, parent)
			if err != nil {
				t.Fatal(err)
			}
			corpus := t.TempDir()
			writeCase(t, corpus, "case.json", item.Case)
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()
			targetBinary := binary
			if variant.missingSource {
				targetBinary += "-no-source"
			}
			command := exec.CommandContext(
				ctx,
				targetBinary,
				"eval",
				"run",
				"--root",
				corpus,
				"--preset",
				prepared.PresetPath,
				"--out",
				filepath.Join(prepared.Home, "reports"),
				"--start-target",
				"--json",
			)
			command.Env = []string{
				"HOME=" + prepared.Home, config.EnvHome + "=" + prepared.Home,
				config.EnvConfig + "=" + prepared.ConfigPath, "PATH=" + os.Getenv("PATH"), "LANG=C",
				"MINTCLAW_LOG_LEVEL=info", "MINTCLAW_LOG_FORMAT=json",
			}
			var stderr bytes.Buffer
			command.Stderr = &stderr
			command.Cancel = func() error { return command.Process.Signal(os.Interrupt) }
			command.WaitDelay = 40 * time.Second
			output, runErr := command.Output()
			var report struct {
				RunReport
				ReportDirectory string `json:"report_dir"`
			}
			if json.Unmarshal(output, &report) != nil || len(report.Trials) != 1 {
				t.Fatalf("native steering report: %v\n%s\n%s", runErr, output, stderr.Bytes())
			}
			data, err := os.ReadFile(filepath.Join(report.ReportDirectory, report.Trials[0].File))
			var trial Trial
			if err != nil || json.Unmarshal(data, &trial) != nil {
				checkpointPath := ""
				matches, _ := filepath.Glob(filepath.Join(report.ReportDirectory, "scratch-*", trialCheckpointName))
				if len(matches) == 1 {
					checkpointPath = matches[0]
				}
				checkpoint, checkpointErr := os.ReadFile(checkpointPath)
				staged, decodeErr := decodeDocument[Trial](checkpoint, 8*1024*1024)
				proofCode := "missing"
				if staged.Steering != nil {
					proofCode = staged.Steering.Code
				}
				t.Logf("checkpoint: read=%v decode=%v bytes=%d credential=%v proof=%s execution=%s/%s",
					checkpointErr, decodeErr, len(checkpoint),
					credentialJSONValue(staged, fixture.cfg.SensitiveDataReplacer()), proofCode,
					staged.ExecutionVerdict, staged.ExecutionCode)
				t.Fatalf("native trial: %v; run: %v\n%s\n%s", err, runErr, output, stderr.Bytes())
			}
			if variant.missingSource {
				if !report.Complete || report.Verdict != Inconclusive || trial.Verdict != Inconclusive ||
					trial.ExecutionVerdict != Pass || trial.Steering == nil ||
					trial.Steering.Code != "post_stop_evidence_unavailable" || judges.Load() != 0 ||
					targets.Load() != 3 || report.TargetBudget == nil || report.TargetBudget.verdict() != Pass {
					t.Fatalf("missing source did not remain a complete non-passing report: %v\n%s", runErr, output)
				}
				return
			}
			if runErr != nil || report.Verdict != Pass || trial.Verdict != Pass || judges.Load() != 1 ||
				report.TargetBudget == nil || report.TargetBudget.verdict() != Pass {
				proofCode := "missing"
				if trial.Steering != nil {
					proofCode = trial.Steering.Code
				}
				t.Fatalf(
					"native steering failed: %v report=%s trial=%s/%s proof=%s targets=%d judges=%d checks=%+v\n%s",
					runErr,
					report.Verdict,
					trial.Verdict,
					trial.Code,
					proofCode,
					targets.Load(),
					judges.Load(),
					trial.Checks,
					stderr.Bytes(),
				)
			}
			if report.ActiveTool == nil || !validSteeringEvidence(trial, item.Case, prepared.Workspace,
				report.ActiveTool.Profile, report.Runtime) {
				t.Fatal("native positive lost its independently replayable hard evidence")
			}
			assertSteeringFrozenControls(t, trial, item.Case, report.RunReport, prepared.Workspace)
			for _, path := range []string{"sessions", "state/outbox", "state/diagnostics/traces"} {
				if err := os.RemoveAll(filepath.Join(prepared.Workspace, path)); err != nil {
					t.Fatal("remove owned synthetic live evidence", err)
				}
			}
			if _, err := Compare(report.ReportDirectory, report.ReportDirectory); err != nil {
				t.Fatal("comparison reopened live native state", err)
			}
			targetsBeforeGrade := targets.Load()
			grade := exec.CommandContext(ctx, binary, "eval", "grade", report.ReportDirectory, "--root", corpus,
				"--preset", prepared.PresetPath, "--out", filepath.Join(prepared.Home, "reports"), "--json")
			grade.Env = command.Env
			gradeOutput, err := grade.CombinedOutput()
			var graded RunReport
			if err != nil || json.Unmarshal(gradeOutput, &graded) != nil || graded.Verdict != Pass ||
				targets.Load() != targetsBeforeGrade || judges.Load() != 2 {
				t.Fatalf("offline native regrade: %v\n%s", err, gradeOutput)
			}
			trial.Steering = nil
			invalid, err := json.Marshal(trial)
			if err != nil ||
				os.WriteFile(filepath.Join(report.ReportDirectory, report.Trials[0].File), invalid, 0o600) != nil {
				t.Fatal("remove synthetic frozen proof", err)
			}
			refused := exec.CommandContext(ctx, binary, grade.Args[1:]...)
			refused.Env = command.Env
			if output, err := refused.CombinedOutput(); err == nil || targets.Load() != targetsBeforeGrade ||
				judges.Load() != 2 {
				t.Fatalf("missing native proof reached judge or passed regrade: %v\n%s", err, output)
			}
		})
	}
}
