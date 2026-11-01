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

	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestFinalDeliveryUsesRealCLIAndFrozenEvidence(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("prepared runtime admission currently supports Linux and macOS")
	}
	// Ordinary CI builds this checkout. A separately hash-verified prebuilt
	// CLI supports the same model-free control on a constrained Linux host.
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
	for _, streaming := range []bool{false, true} {
		name := "native-nonstreaming-control"
		if streaming {
			name = "native-streaming-unavailable"
		}
		t.Run(name, func(t *testing.T) {
			item := finalDeliveryDefinition(t)
			item.Case.FinalDelivery.Streaming = streaming
			repeat := 3
			if streaming {
				repeat = 1
			}
			fixture, parent := preparationFixture(t)
			enableTestFinalDelivery(t, fixture, streaming)
			fixture.cfg.Agents.Defaults.ContextWindow, fixture.cfg.Agents.Defaults.MaxTokens = 16384, 512
			fixture.cfg.Agents.Defaults.MaxLLMRetries = 1
			fixture.preset.TargetCallBudget.MaxOutputTokens = 512
			var targets, judges, streams atomic.Int32
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				raw, readErr := io.ReadAll(http.MaxBytesReader(w, request.Body, 1<<20))
				if readErr != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				content := "Synthetic colored blocks are sorted by color."
				if bytes.Contains(raw, []byte("You are an independent conversation judge")) {
					judges.Add(1)
					findings, _ := json.Marshal(JudgeFindings{Version: Version, Criteria: []JudgeFinding{{
						ID: "grounded-followup", Verdict: Pass, Reason: "Synthetic native control.",
						EvidenceIDs: []string{"event-000001"},
					}}})
					content = string(findings)
				} else {
					number := targets.Add(1)
					if number%2 == 0 {
						content = item.Case.FinalDelivery.Marker
					}
					var body struct {
						Stream bool `json:"stream"`
					}
					if json.Unmarshal(raw, &body) != nil {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					if body.Stream {
						streams.Add(1)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id": "synthetic-delivery", "model": "synthetic-model",
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
				t.Fatal("prepare native delivery profile", err)
			}
			corpus := filepath.Join(t.TempDir(), "corpus")
			if os.Mkdir(corpus, 0o700) != nil {
				t.Fatal("corpus")
			}
			writeCase(t, corpus, "delivery.json", item.Case)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
			defer cancel()
			command := exec.CommandContext(ctx, binary, "eval", "run", "--root", corpus,
				"--preset", prepared.PresetPath, "--out", filepath.Join(prepared.Home, "reports"),
				"--start-target", "--json")
			if !streaming {
				command.Args = append(command.Args, "--repeat", "3")
			}
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
			if json.Unmarshal(output, &report) != nil || len(report.Trials) != repeat {
				t.Fatalf("native report: %v\n%s\n%s", runErr, output, stderr.Bytes())
			}
			data, err := os.ReadFile(filepath.Join(report.ReportDirectory, report.Trials[0].File))
			var trial Trial
			if err != nil || json.Unmarshal(data, &trial) != nil {
				files, _ := filepath.Glob(filepath.Join(report.ReportDirectory, "scratch-*", trialCheckpointName))
				for _, file := range files {
					checkpoint, _ := os.ReadFile(file)
					var pending Trial
					if json.Unmarshal(checkpoint, &pending) == nil {
						t.Logf(
							"pending execution=%s/%s events=%d last=%s",
							pending.ExecutionVerdict,
							pending.ExecutionCode,
							len(pending.Events),
							lastOutcome(pending.Events).Kind,
						)
					}
				}
				t.Fatalf("native trial: %v run=%v verdict=%s summary=%+v private=%+v\n%s",
					err, runErr, report.Verdict, report.Trials[0], report.PrivateSurfaces, stderr.Bytes())
			}
			if targets.Load() != int32(2*repeat) || streams.Load() != 0 || trial.FinalDelivery == nil ||
				trial.FinalDelivery.Code != "observed" {
				t.Fatalf("native delivery observations: targets=%d streams=%d proof=%+v error=%v\n%s",
					targets.Load(), streams.Load(), trial.FinalDelivery, runErr, stderr.Bytes())
			}
			if streaming {
				if runErr == nil || trial.Verdict != Fail || judges.Load() != 0 || trial.EvidenceEligible ||
					finalDeliveryEvidenceCode(
						trial,
						item.Case,
						prepared.Workspace,
					) != "final_delivery_stream_unavailable" {
					t.Fatal("ordinary finals incorrectly qualified streaming", trial.Verdict, trial.Code, trial.Checks)
				}
				return
			}
			if runErr != nil || report.Verdict != Pass || judges.Load() != int32(repeat) ||
				finalDeliveryEvidenceCode(trial, item.Case, prepared.Workspace) != "" {
				t.Fatalf("native nonstreaming control: %v verdict=%s trial=%s/%s checks=%+v\n%s",
					runErr, report.Verdict, trial.Verdict, trial.Code, trial.Checks, stderr.Bytes())
			}
			assertFinalDeliveryFrozenControls(t, trial, item.Case, report.RunReport, prepared.Workspace)
			for _, summary := range report.Trials[1:] {
				data, err := os.ReadFile(filepath.Join(report.ReportDirectory, summary.File))
				var peer Trial
				if err != nil || json.Unmarshal(data, &peer) != nil || peer.Verdict != Pass ||
					peer.SessionID == trial.SessionID || finalDeliveryEvidenceCode(peer, item.Case, prepared.Workspace) != "" {
					t.Fatal("shared native outbox lost repeated-trial isolation", err)
				}
			}
			for _, name := range []string{"sessions", "state/outbox", "state/diagnostics/traces"} {
				if os.RemoveAll(filepath.Join(prepared.Workspace, name)) != nil {
					t.Fatal("remove own live observations")
				}
			}
			grade := exec.CommandContext(ctx, binary, "eval", "grade", report.ReportDirectory,
				"--root", corpus, "--preset", prepared.PresetPath,
				"--out", filepath.Join(prepared.Home, "reports"), "--json")
			grade.Env = command.Env
			gradedOutput, gradeErr := grade.CombinedOutput()
			var graded RunReport
			if gradeErr != nil || json.Unmarshal(gradedOutput, &graded) != nil || graded.Verdict != Pass ||
				targets.Load() != int32(2*repeat) || judges.Load() != int32(2*repeat) {
				t.Fatalf("offline delivery grade: %v\n%s", gradeErr, gradedOutput)
			}
			trial.FinalDelivery = nil
			invalid, _ := json.Marshal(trial)
			if os.WriteFile(filepath.Join(report.ReportDirectory, report.Trials[0].File), invalid, 0o600) != nil {
				t.Fatal("remove frozen proof")
			}
			refused := exec.CommandContext(ctx, binary, grade.Args[1:]...)
			refused.Env = command.Env
			if _, err := refused.CombinedOutput(); err == nil ||
				targets.Load() != int32(2*repeat) || judges.Load() != int32(2*repeat) {
				t.Fatal("missing proof reached offline judge", err)
			}
		})
	}
}
