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

func TestSkillSelectionUsesRealCLIAndFrozenNativeRevision(t *testing.T) {
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
	} else {
		info, err := os.Lstat(binary)
		if err != nil || !filepath.IsAbs(binary) || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			t.Fatal("invalid explicit native test CLI", err)
		}
	}
	item := skillSelectionDefinition(t)
	fixture, parent := preparationFixture(t)
	enableTestSkillSelection(t, fixture, item.Case)
	fixture.cfg.Agents.Defaults.ContextWindow, fixture.cfg.Agents.Defaults.MaxTokens = 16384, 128
	fixture.cfg.Agents.Defaults.MaxLLMRetries = 1
	fixture.preset.TargetCallBudget.MaxCalls = 12
	var targets, judges, selected atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		raw, err := io.ReadAll(http.MaxBytesReader(w, request.Body, 1<<20))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		content := item.Case.SkillSelection.Value
		if bytes.Contains(raw, []byte("You are an independent conversation judge")) {
			judges.Add(1)
			finding, _ := json.Marshal(JudgeFindings{Version: Version, Criteria: []JudgeFinding{{
				ID: "selected-and-controls", Verdict: Pass, Reason: "Synthetic native control.",
				EvidenceIDs: []string{"event-000001"},
			}}})
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
			// Historical envelopes may retain the prior selected skill. Only
			// the actual current user's injected revision drives this control.
			if bytes.Contains(current, []byte("Revision: sha256:"+item.Case.Fixtures[0].SHA256)) {
				selected.Add(1)
				content = item.Case.SkillSelection.Output
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "synthetic-skill-selection", "model": "synthetic-model",
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
	seed, _, seedErr := inspectProfileFiles(fixture.preset, fixture.home)
	if seedErr != nil || validateConversationPolicy(seed.config, fixture.preset, fixture.workspace) != nil {
		t.Fatal("saved skill-selection policy differs from source", seedErr)
	}
	prepared, err := PrepareProfile(t.Context(), fixture.preset, fixture.home, parent)
	if err != nil {
		t.Fatalf("prepare skill-selection profile: %v outcome=%s home=%s", err, prepared.Outcome, prepared.Home)
	}
	corpus := t.TempDir()
	writeCase(t, corpus, "skill-selection.json", item.Case)
	source, err := os.ReadFile(filepath.Join("../../evals", item.Case.Fixtures[0].Path))
	if err != nil {
		t.Fatal(err)
	}
	fixturePath := filepath.Join(corpus, item.Case.Fixtures[0].Path)
	if os.MkdirAll(filepath.Dir(fixturePath), 0o700) != nil || os.WriteFile(fixturePath, source, 0o600) != nil {
		t.Fatal("private corpus fixture")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
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
			t.Fatalf("native trial: %v run=%v summary=%+v\n%s", err, runErr, summary, stderr.Bytes())
		}
		if runErr != nil || trial.Verdict != Pass ||
			skillSelectionEvidenceCode(trial, item.Case, prepared.Workspace) != "" {
			t.Fatalf("native skill control: run=%v trial=%s/%s checks=%+v oracle=%s\n%s",
				runErr, trial.Verdict, trial.Code, trial.Checks,
				skillSelectionEvidenceCode(trial, item.Case, prepared.Workspace), stderr.Bytes())
		}
		if index == 0 {
			first = trial
		} else if trial.SessionID == first.SessionID {
			t.Fatal("repetitions reused a native session")
		}
	}
	if report.Verdict != Pass || targets.Load() != 9 || selected.Load() != 3 || judges.Load() != 3 {
		t.Fatal("native selection did not follow actual current injected contexts",
			report.Verdict, targets.Load(), selected.Load(), judges.Load())
	}
	assertSkillSelectionFrozenControls(t, first, item.Case, report.RunReport, prepared.Workspace)
	for _, name := range []string{"sessions", "state/outbox", "state/diagnostics/traces", item.Case.SkillSelection.path()} {
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
			t.Fatal("remove own native log")
		}
	}
	grade := exec.CommandContext(ctx, binary, "eval", "grade", report.ReportDirectory, "--root", corpus,
		"--preset", prepared.PresetPath, "--out", filepath.Join(prepared.Home, "reports"), "--json")
	grade.Env = command.Env
	gradedOutput, gradeErr := grade.CombinedOutput()
	var graded RunReport
	if gradeErr != nil || json.Unmarshal(gradedOutput, &graded) != nil || graded.Verdict != Pass ||
		targets.Load() != 9 || selected.Load() != 3 || judges.Load() != 6 {
		t.Fatalf("offline skill grade: %v\n%s", gradeErr, gradedOutput)
	}
	first.SkillSelection = nil
	invalid, _ := json.Marshal(first)
	if os.WriteFile(filepath.Join(report.ReportDirectory, report.Trials[0].File), invalid, 0o600) != nil {
		t.Fatal("remove own frozen skill proof")
	}
	refused := exec.CommandContext(ctx, binary, grade.Args[1:]...)
	refused.Env = command.Env
	if _, err := refused.CombinedOutput(); err == nil || targets.Load() != 9 || judges.Load() != 6 {
		t.Fatal("missing frozen skill proof reached the judge or replayed the target", err)
	}
}
