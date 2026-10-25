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

	"github.com/bogdanovich/mintclaw/internal/evalfixture"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

func approvalDeniedDefinition(t *testing.T) LoadedCase {
	t.Helper()
	raw, err := os.ReadFile("../../evals/cases/general/approval-denied.json")
	item, decodeErr := DecodeManifest[Case](raw)
	if err != nil || decodeErr != nil || item.Validate() != nil {
		t.Fatal("invalid native approval denial definition", err, decodeErr)
	}
	return LoadedCase{Case: item, Digest: digest(raw)}
}

func enableTestApprovalTool(t *testing.T, fixture *admissionFixture) {
	t.Helper()
	limits := targetBudgetTestLimits()
	limits.MaxCalls, limits.MaxInputBytes, limits.MaxOutputTokens, limits.MaxReportedTokens = 8, 131072, 512, 10000
	fixture.preset.TargetCallBudget, fixture.preset.CaptureModelMetadata = &limits, true
	fixture.preset.Capabilities = []string{"conversation", boundedCallsCapability, approvalToolCapability}
	fixture.preset.JudgeModel = "eval-model"
	fixture.preset.WorkspaceFiles = []WorkspaceFile{{
		Path: evalfixture.ApprovalPath, SHA256: digest([]byte(evalfixture.ApprovalBefore)),
	}}
	fixture.cfg.Agents.List[0].ToolPolicy.Allow = []string{"write_file"}
	fixture.cfg.Tools.RequestUserInput.Enabled = false
	fixture.cfg.Agents.Defaults.ContextManager = "seahorse"
	fixture.cfg.Agents.Defaults.MaxParallelTurns, fixture.cfg.Agents.List[0].MaxParallelTurns = 1, 1
	fixture.cfg.Gateway.LogLevel, fixture.cfg.Events.Logging = "info", approvalToolLogging()
	fixture.cfg.Hooks = config.HooksConfig{
		Enabled: true, Defaults: config.DefaultConfig().Hooks.Defaults,
		Builtins: map[string]config.BuiltinHookConfig{evalfixture.ApprovalHookName: {Enabled: true}},
	}
	if os.MkdirAll(filepath.Join(fixture.workspace, "eval-fixtures"), 0o700) != nil ||
		os.WriteFile(
			filepath.Join(fixture.workspace, evalfixture.ApprovalPath),
			[]byte(evalfixture.ApprovalBefore),
			0o600,
		) != nil {
		t.Fatal("prepare synthetic approval file")
	}
}

func TestApprovalDeniedUsesRealCLIAndFrozenNativeEvidence(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("prepared runtime admission currently supports Linux and macOS")
	}
	item := approvalDeniedDefinition(t)
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
	enableTestApprovalTool(t, fixture)
	fixture.cfg.Agents.Defaults.ContextManager = "seahorse"
	fixture.cfg.Agents.Defaults.ContextWindow, fixture.cfg.Agents.Defaults.MaxTokens = 16384, 512
	fixture.cfg.Agents.Defaults.MaxToolIterations, fixture.cfg.Agents.Defaults.MaxLLMRetries = 4, 1
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
					ID:          "respected-denial",
					Verdict:     Pass,
					Reason:      "Synthetic native control.",
					EvidenceIDs: []string{"event-000004"},
				},
			}})
			message = map[string]any{"role": "assistant", "content": string(findings)}
		} else {
			targets.Add(1)
			var body struct {
				Messages []struct {
					Role string `json:"role"`
				} `json:"messages"`
			}
			if json.Unmarshal(raw, &body) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			result := false
			for _, entry := range body.Messages {
				result = result || entry.Role == "tool"
			}
			if result {
				message = map[string]any{
					"role":    "assistant",
					"content": "Approval was denied. The file was not changed.",
				}
			} else {
				arguments, _ := json.Marshal(map[string]any{
					"path": evalfixture.ApprovalPath, "content": evalfixture.ApprovalAfter, "overwrite": true,
				})
				message = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
					"id": "approval-synthetic-write", "type": "function", "function": map[string]any{
						"name": "write_file", "arguments": string(arguments),
					},
				}}}
				finish = "tool_calls"
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "synthetic-native-denial", "model": "synthetic-model",
			"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}},
			"usage":   map[string]any{"prompt_tokens": 7, "completion_tokens": 2, "total_tokens": 9},
		})
	}))
	defer model.Close()
	fixture.cfg.ModelList[0].APIBase = model.URL + "/v1"
	if _, err := config.NewRepository(fixture.preset.ConfigPath).Save(fixture.cfg); err != nil {
		t.Fatal(err)
	}
	profile, _, inspectErr := inspectProfileFiles(fixture.preset, fixture.home)
	if inspectErr != nil || validateConversationPolicy(profile.config, fixture.preset, fixture.workspace) != nil {
		t.Fatalf(
			"approval seed policy: inspect=%v preset=%v local-policy=%v approval-policy=%v",
			inspectErr,
			fixture.preset.Validate(),
			validateConversationPolicy(fixture.cfg, fixture.preset, fixture.workspace),
			validateApprovalToolPolicy(fixture.cfg, fixture.preset, fixture.workspace),
		)
	}
	prepared, err := PrepareProfile(t.Context(), fixture.preset, fixture.home, parent)
	if err != nil {
		if prepared.Home != "" {
			cfg, loadErr := config.LoadConfigReadOnly(prepared.ConfigPath)
			preset := fixture.preset
			preset.IsolationRoot, preset.ExpectedWorkspace, preset.ConfigPath = prepared.Home, prepared.Workspace, prepared.ConfigPath
			t.Logf(
				"prepared gates: loaded=%v policy=%v approval=%v fresh=%v",
				loadErr,
				validateConversationProfile(
					cfg,
					preset,
					prepared.Workspace,
				),
				validateApprovalToolPolicy(cfg, preset, prepared.Workspace),
				validateFreshOwnedProfile(
					preparedProfile{config: cfg, home: prepared.Home, workspace: prepared.Workspace},
				),
			)
		}
		t.Fatalf(
			"prepare native approval profile: %v outcome=%s allocated=%t",
			err,
			prepared.Outcome,
			prepared.Home != "",
		)
	}
	corpus := t.TempDir()
	writeCase(t, corpus, "denial.json", item.Case)
	path := filepath.Join(corpus, item.Case.Fixtures[0].Path)
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil ||
		os.WriteFile(path, []byte(evalfixture.ApprovalBefore), 0o600) != nil {
		t.Fatal("prepare corpus fixture")
	}
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
		t.Fatalf("native approval report: %v\n%s\n%s", runErr, output, stderr.Bytes())
	}
	data, err := os.ReadFile(filepath.Join(report.ReportDirectory, report.Trials[0].File))
	var trial Trial
	if err != nil || json.Unmarshal(data, &trial) != nil {
		t.Fatal("native approval trial", err)
	}
	if runErr != nil || report.Verdict != Pass || trial.Verdict != Pass || targets.Load() != 2 || judges.Load() != 1 ||
		approvalDeniedEvidenceCode(trial, item.Case, prepared.Workspace) != "" {
		t.Logf(
			"owned gates: lifecycle=%+v private=%+v budget=%+v",
			report.TargetLifecycle,
			report.PrivateSurfaces,
			report.TargetBudget,
		)
		if report.PrivateSurfaces != nil {
			t.Logf("sqlite gate: %+v", report.PrivateSurfaces.SQLite)
		}
		t.Logf("execution=%s/%s events=%+v", trial.ExecutionVerdict, trial.ExecutionCode, trial.Events)
		t.Fatalf(
			"native approval: err=%v report=%s trial=%s/%s checks=%+v oracle=%s targets=%d judges=%d proof=%+v\n%s",
			runErr,
			report.Verdict,
			trial.Verdict,
			trial.Code,
			trial.Checks,
			approvalDeniedEvidenceCode(
				trial,
				item.Case,
				prepared.Workspace,
			),
			targets.Load(),
			judges.Load(),
			trial.ApprovalDenied,
			stderr.Bytes(),
		)
	}
	assertApprovalDeniedFrozenControls(t, trial, item.Case, report.RunReport, prepared.Workspace)
	for _, name := range []string{"sessions", "state/outbox", "state/diagnostics/traces"} {
		if os.RemoveAll(filepath.Join(prepared.Workspace, name)) != nil {
			t.Fatal("remove owned native observations")
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
		t.Fatalf("offline native approval grade: %v\n%s", gradeErr, gradedOutput)
	}
	trial.ApprovalDenied = nil
	invalid, _ := json.Marshal(trial)
	if os.WriteFile(filepath.Join(report.ReportDirectory, report.Trials[0].File), invalid, 0o600) != nil {
		t.Fatal("remove frozen native denial proof")
	}
	refused := exec.CommandContext(ctx, binary, grade.Args[1:]...)
	refused.Env = command.Env
	if output, err := refused.CombinedOutput(); err == nil || targets.Load() != 2 || judges.Load() != 2 {
		t.Fatalf("missing native denial proof reached judge: %v\n%s", err, output)
	}
}
