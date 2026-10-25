package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/internal/evaltarget"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestTargetBudgetRunUsesRealCLIAndOneCeilingAcrossRepetitions(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("prepared runtime admission currently supports Linux and macOS")
	}
	binary := buildTargetBudgetTestCLI(t)
	for _, mode := range []string{"conversation", "active_tool"} {
		t.Run(mode, func(t *testing.T) {
			testTargetBudgetRunRealCLI(t, binary, mode == "active_tool")
		})
	}
}

func testTargetBudgetRunRealCLI(t *testing.T, binary string, active bool) {
	t.Helper()
	fixture, parent := preparationFixture(t)
	if active {
		enableTestActiveTool(fixture)
	}
	var requests atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if json.NewDecoder(http.MaxBytesReader(writer, request.Body, 1<<20)).Decode(&body) != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		if body["max_tokens"] != float64(128) && body["max_completion_tokens"] != float64(128) {
			t.Error("requested output limit did not reach the real provider request")
		}
		if active {
			var definitions []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			}
			raw, err := json.Marshal(body["tools"])
			if err != nil || json.Unmarshal(raw, &definitions) != nil {
				t.Error("native tool schema unavailable")
			}
			mcpTools := []string{}
			for _, definition := range definitions {
				if strings.HasPrefix(definition.Function.Name, "mcp_") {
					mcpTools = append(mcpTools, definition.Function.Name)
				}
			}
			if !slices.Equal(mcpTools, []string{activeToolNativeName}) {
				t.Error("native gateway did not discover only the owned MCP fixture", mcpTools)
			}
		}
		requests.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"synthetic","model":"synthetic-model",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"synthetic-marker"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}}`))
	}))
	defer model.Close()
	fixture.cfg.ModelList[0].APIBase = model.URL
	fixture.cfg.Agents.Defaults.ContextManager = "none"
	fixture.cfg.Agents.Defaults.ContextWindow, fixture.cfg.Agents.Defaults.MaxTokens = 8192, 128
	fixture.cfg.Agents.Defaults.MaxToolIterations, fixture.cfg.Agents.Defaults.MaxLLMRetries = 2, 1
	if _, err := config.NewRepository(fixture.preset.ConfigPath).Save(fixture.cfg); err != nil {
		t.Fatal(err)
	}
	limits := targetBudgetTestLimits()
	limits.MaxCalls = 6
	fixture.preset.TargetCallBudget = &limits
	if !slices.Contains(fixture.preset.Capabilities, boundedCallsCapability) {
		fixture.preset.Capabilities = append(fixture.preset.Capabilities, boundedCallsCapability)
	}
	prepared, err := PrepareProfile(t.Context(), fixture.preset, fixture.home, parent)
	if err != nil {
		t.Fatal("native isolated profile preparation", err)
	}
	outputParent := filepath.Join(prepared.Home, "reports")
	corpus := t.TempDir()
	item := testCase()
	item.Requires = append(item.Requires, boundedCallsCapability)
	item.Checks = append(item.Checks, Check{ID: "marker", Kind: "response_contains", Expected: "synthetic-marker"})
	writeCase(t, corpus, "bounded.json", item)
	runCtx, runCancel := context.WithTimeout(t.Context(), time.Minute)
	defer runCancel()
	command := exec.CommandContext(runCtx, binary, "eval", "run", "--root", corpus,
		"--preset", prepared.PresetPath, "--out", outputParent, "--start-target", "--repeat", "3", "--json")
	command.Env = []string{
		"HOME=" + prepared.Home, config.EnvHome + "=" + prepared.Home,
		config.EnvConfig + "=" + prepared.ConfigPath, "PATH=" + os.Getenv("PATH"), "LANG=C",
		"MINTCLAW_LOG_LEVEL=" + config.EffectiveGatewayLogLevel(fixture.cfg), "MINTCLAW_LOG_FORMAT=json",
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	command.Cancel = func() error { return command.Process.Signal(os.Interrupt) }
	command.WaitDelay = 40 * time.Second
	output, err := command.Output()
	if err != nil {
		t.Fatalf("real CLI eval: %v\n%s\n%s", err, output, stderr.Bytes())
	}
	var result struct {
		RunReport
		ReportDirectory string `json:"report_dir"`
	}
	if err = json.Unmarshal(output, &result); err != nil {
		t.Fatalf("real CLI report: %v\n%s", err, output)
	}
	if result.Verdict != Pass || !result.Complete || result.Repeat != 3 || result.Coverage[Pass] != 3 ||
		result.TargetBudget == nil || !result.TargetBudget.valid(result.RunReport) ||
		result.TargetBudget.verdict() != Pass || requests.Load() != 6 {
		t.Fatalf("native run budget did not cover all repetitions: %+v, requests %d", result.RunReport, requests.Load())
	}
	proof, err := evaltarget.DecodeProof(
		[]byte(result.TargetBudget.Proof),
		result.TargetBudget.Spec,
		result.Runtime.PID,
	)
	if err != nil || proof.Snapshot.Calls != 6 || proof.Snapshot.Completed != 6 || proof.Snapshot.ReportedTokens != 54 {
		t.Fatal("native receipt disagrees with independently observed requests", proof, err)
	}
	sessions := map[string]bool{}
	for _, summary := range result.Trials {
		data, err := os.ReadFile(filepath.Join(result.ReportDirectory, summary.File))
		var trial Trial
		if err != nil || json.Unmarshal(data, &trial) != nil || !trial.TargetBudgetRequired ||
			sessions[trial.SessionID] {
			t.Fatal("native trials lost mandatory budget or independent sessions", err)
		}
		sessions[trial.SessionID] = true
	}
	name, _ := result.TargetBudget.Spec.ReceiptName()
	if err := os.Remove(filepath.Join(prepared.Home, name)); err != nil {
		t.Fatal(err)
	}
	if _, err = Compare(result.ReportDirectory, result.ReportDirectory); err != nil {
		t.Fatal("comparison needed live child state instead of retained native proof", err)
	}
}

func buildTargetBudgetTestCLI(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "mintclaw")
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	buildCtx, buildCancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer buildCancel()
	build := exec.CommandContext(buildCtx, "go", "build", "-tags", "goolm,stdjson", "-o", binary, "./cmd/mintclaw")
	build.Dir, build.Env = root, append(os.Environ(), "CGO_ENABLED=0")
	output, err := build.CombinedOutput()
	if err != nil {
		t.Fatalf("real CLI build: %v\n%s", err, output)
	}
	return binary
}
