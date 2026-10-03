package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/testharness/llmscenario"
)

func TestGatewayContextPrefixCorpusAcrossRestart(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "corpus.txt")
	if err := os.WriteFile(path, []byte(llmscenario.PrefixMarker), 0o600); err != nil {
		t.Fatal(err)
	}
	corpus := llmscenario.PrefixCorpus{Path: path}
	steps := corpus.Steps()
	first := llmscenario.NewScriptedProvider("test-model", steps[:2]...)
	configure := func(cfg *config.Config) {
		cfg.Agents.Defaults.ContextManager = "seahorse"
		cfg.Tools.ReadFile = config.DefaultConfig().Tools.ReadFile
		cfg.Agents.Defaults.Provider = "fixture"
		cfg.ModelList = config.SecureModelList{&config.ModelConfig{
			ModelName: "test-model", Provider: "fixture", Model: "test-model", Enabled: true,
		}}
	}
	initial := newAgentLoopTestFixtureWithWorkspace(t, workspace, first, configure)
	initial.Agent.ContextBuilder.now = func() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) }
	message := testInboundMessage(bus.InboundMessage{
		Context: bus.InboundContext{Channel: "telegram", ChatID: "corpus", SenderID: "sender-a"},
		Content: llmscenario.PrefixInitialPrompt,
	})
	if _, err := initial.Loop.processMessage(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	initial.Close()
	resumed := llmscenario.NewScriptedProvider("test-model", steps[2:]...)
	restart := newAgentLoopTestFixtureWithWorkspace(t, workspace, resumed, configure)
	restart.Agent.ContextBuilder.now = func() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) }
	message.Context.SenderID = "sender-b"
	message.Content = llmscenario.PrefixFollowupPrompt
	if _, err := restart.Loop.processMessage(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	var requests []llmscenario.RequestSnapshot
	for _, call := range append(first.Calls(), resumed.Calls()...) {
		snapshot, err := llmscenario.SnapshotCall(call)
		if err != nil {
			t.Fatal(err)
		}
		requests = append(requests, snapshot)
	}
	if err := corpus.Check(requests...); err != nil {
		t.Fatal(err)
	}
	for _, provider := range []*llmscenario.ScriptedProvider{first, resumed} {
		if err := provider.AssertExhausted(); err != nil {
			t.Fatal(err)
		}
	}
}
