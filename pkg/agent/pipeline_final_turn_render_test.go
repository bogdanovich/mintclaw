package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func TestPipelineShouldFinalizeAfterToolLoopUsesConfig(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.FinalTurnRenderMode = "llm"
	pipeline := &Pipeline{Cfg: cfg}
	exec := &turnExecution{
		sawSteering: true,
	}

	if !pipeline.shouldFinalizeAfterToolLoop(exec, newLLMIterationState(1)) {
		t.Fatal("shouldFinalizeAfterToolLoop() = false, want true")
	}
}

func TestFinalTurnRenderCarriesProtectedDiagnostics(t *testing.T) {
	const canary = "browser-diagnostics-final-render-canary-6b1f3a"
	provider := &sequenceProvider{responses: []*providers.LLMResponse{{
		Content: "rendered diagnostics " + canary,
	}}}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.FinalTurnRenderMode = "llm"
	pipeline := newTestPipeline(al)
	ts := newTurnState(agent, makeTestTurnSpec("protected-final-render-session"), turnEventScope{
		turnID: "protected-final-render-turn", context: newTurnContext(nil, nil, nil),
	})
	exec, err := pipeline.SetupTurn(t.Context(), ts)
	if err != nil {
		t.Fatalf("SetupTurn() error = %v", err)
	}
	exec.sawSteering = true
	exec.messages = []providers.Message{
		{Role: "assistant", ToolCalls: []providers.ToolCall{{
			ID: "diagnostics-call", Name: "browser_diagnostics", Arguments: map[string]any{},
		}}},
		{Role: "tool", ToolCallID: "diagnostics-call", Content: canary},
	}

	got, rendered := tryRenderFinalTurnReply(t.Context(), cfg, ts, exec, terminalContent{})
	if !rendered || got.content != "rendered diagnostics "+canary || !got.protected {
		t.Fatalf("protected final render = (%#v, %v)", got, rendered)
	}
}

func TestCodingFinalTurnRenderRefreshesWorkspaceSnapshot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCodingWorkspaceTestFile(t, filepath.Join(project, "tracked.txt"), "baseline\n")
	runCodingWorkspaceGit(t, project, "init", "-b", "main")
	runCodingWorkspaceGit(t, project, "config", "user.email", "mintclaw-tests@example.invalid")
	runCodingWorkspaceGit(t, project, "config", "user.name", "MintClaw Tests")
	runCodingWorkspaceGit(t, project, "add", "tracked.txt")
	runCodingWorkspaceGit(t, project, "commit", "-m", "initial")

	layout, err := NewCodingRuntimeLayout(
		"thread-final-reanchor",
		project,
		filepath.Join(root, "state"),
		[]string{project},
	)
	if err != nil {
		t.Fatal(err)
	}
	builder, err := newCodingContextBuilder(layout)
	if err != nil {
		t.Fatal(err)
	}
	provider := &promptCapturingProvider{}
	agent := &AgentInstance{
		ID:             "main",
		Model:          "coding-workspace-model",
		Workspace:      project,
		MaxTokens:      4096,
		Provider:       provider,
		ContextBuilder: builder,
	}
	opts := makeTestTurnSpec("coding:thread-final-reanchor")
	opts.CodingContext = builder.codingContext
	ts := newTurnState(agent, opts, turnEventScope{})
	execState := &turnExecution{
		messages: builder.BuildMessagesFromPrompt(PromptBuildRequest{
			CurrentMessage: "inspect",
			CodingContext:  opts.CodingContext,
		}),
		sawSteering: true,
		model: turnExecutionModel{
			activeProvider: provider,
			activeModel:    "coding-workspace-model",
			activeCandidates: []providers.FallbackCandidate{{
				Provider: "test-provider",
				Model:    "coding-workspace-model",
			}},
		},
	}
	if len(execState.messages) == 0 || !strings.Contains(execState.messages[0].Content, "Branch: main") {
		t.Fatalf("initial final-render messages = %#v", execState.messages)
	}

	runCodingWorkspaceGit(t, project, "switch", "-c", "changed-before-final-render")
	writeCodingWorkspaceTestFile(t, filepath.Join(project, "final.txt"), "external final-render change\n")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.FinalTurnRenderMode = "llm"
	result, rendered := tryRenderFinalTurnReply(t.Context(), cfg, ts, execState, terminalContent{})
	if !rendered || result.content != "captured response" {
		t.Fatalf("final render = (%#v, %v)", result, rendered)
	}
	messages := provider.Messages()
	if len(messages) == 0 {
		t.Fatal("final-render provider received no messages")
	}
	for _, want := range []string{
		"Branch: changed-before-final-render",
		"Status: dirty",
		`?? "final.txt"`,
	} {
		if !strings.Contains(messages[0].Content, want) {
			t.Fatalf("final-render provider prompt missing %q: %s", want, messages[0].Content)
		}
	}
}

func TestPipelineShouldFinalizeAfterToolLoop_DefaultsWhenConfigMissing(t *testing.T) {
	pipeline := &Pipeline{}
	exec := &turnExecution{
		sawSteering: true,
	}

	if pipeline.shouldFinalizeAfterToolLoop(exec, newLLMIterationState(1)) {
		t.Fatal("shouldFinalizeAfterToolLoop() = true, want false without config")
	}
}
