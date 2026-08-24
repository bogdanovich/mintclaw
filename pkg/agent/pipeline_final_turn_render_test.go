package agent

import (
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
			ID: "diagnostics-call", Name: "browser_diagnostics",
			Function: &providers.FunctionCall{Name: "browser_diagnostics"},
		}}},
		{Role: "tool", ToolCallID: "diagnostics-call", Content: canary},
	}

	got, rendered := tryRenderFinalTurnReply(t.Context(), cfg, ts, exec, terminalContent{})
	if !rendered || got.content != "rendered diagnostics "+canary || !got.protected {
		t.Fatalf("protected final render = (%#v, %v)", got, rendered)
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
