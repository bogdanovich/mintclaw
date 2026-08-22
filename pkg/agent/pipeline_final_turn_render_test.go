package agent

import (
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
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

func TestPipelineShouldFinalizeAfterToolLoop_DefaultsWhenConfigMissing(t *testing.T) {
	pipeline := &Pipeline{}
	exec := &turnExecution{
		sawSteering: true,
	}

	if pipeline.shouldFinalizeAfterToolLoop(exec, newLLMIterationState(1)) {
		t.Fatal("shouldFinalizeAfterToolLoop() = true, want false without config")
	}
}
