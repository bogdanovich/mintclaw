package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/seahorse"
)

type agentBudgetTestProvider struct {
	calls       atomic.Int32
	closes      atomic.Int32
	unsupported bool
}

func (p *agentBudgetTestProvider) Chat(
	context.Context, []providers.Message, []providers.ToolDefinition, string, map[string]any,
) (*providers.LLMResponse, error) {
	p.calls.Add(1)
	return &providers.LLMResponse{
		Content: "budget fixture answer", FinishReason: "stop",
		Usage: &providers.UsageInfo{PromptTokens: 5, CompletionTokens: 1, TotalTokens: 6},
	}, nil
}

func (p *agentBudgetTestProvider) GetDefaultModel() string { return "fixture-model" }
func (p *agentBudgetTestProvider) Close()                  { p.closes.Add(1) }
func (p *agentBudgetTestProvider) Capabilities() providers.ProviderCapabilities {
	return providers.ProviderCapabilities{CallerMediatedTools: !p.unsupported}
}

func newAgentCallBudget(t *testing.T, calls int) *providers.CallBudget {
	t.Helper()
	budget, err := providers.NewCallBudget(providers.CallBudgetLimits{
		MaxCalls: calls, MaxInputBytes: 512 << 10, MaxOutputTokens: 128,
		MaxReportedTokens: 10000, MaxInFlight: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	return budget
}

func agentBudgetConfig(t *testing.T) *config.Config {
	t.Helper()
	t.Setenv(config.EnvHome, t.TempDir())
	return &config.Config{Agents: config.AgentsConfig{
		Defaults: config.AgentDefaults{
			Workspace: t.TempDir(), ModelName: "fixture-model",
			MaxTokens: 128, ContextWindow: 8192, MaxToolIterations: 2, MaxLLMRetries: 1, ContextManager: "none",
		},
		List: []config.AgentConfig{{ID: "main", Default: true}},
	}}
}

func newBudgetAgentLoop(
	t *testing.T, cfg *config.Config, delegate providers.LLMProvider, budget *providers.CallBudget,
) *AgentLoop {
	t.Helper()
	messageBus := bus.NewMessageBus()
	t.Cleanup(messageBus.Close)
	loop, err := NewAgentLoopChecked(cfg, messageBus, delegate,
		WithIsolatedToolBootstrap(), WithIsolatedSkillBootstrap(), WithProviderCallBudget(budget))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(loop.Close)
	return loop
}

func agentBudgetChat(ctx context.Context, provider providers.LLMProvider) error {
	_, err := provider.Chat(ctx, []providers.Message{{Role: "user", Content: "synthetic"}}, nil,
		"fixture-model", map[string]any{"max_tokens": 128})
	return err
}

func TestAgentProviderBudgetBoundsNativeTurnsAndRefusesReload(t *testing.T) {
	budget := newAgentCallBudget(t, 2)
	delegate := &agentBudgetTestProvider{}
	cfg := agentBudgetConfig(t)
	loop := newBudgetAgentLoop(t, cfg, delegate, budget)
	for range 2 {
		response, err := loop.ProcessDirect(t.Context(), "hello", "budget-session")
		if err != nil || response != "budget fixture answer" {
			t.Fatalf("native turn = %q/%v", response, err)
		}
	}
	_, _ = loop.ProcessDirect(t.Context(), "one too many", "budget-session")
	if state := budget.Snapshot(); state.Calls != 2 || state.Completed != 2 ||
		state.StoppedReason != "calls" || delegate.calls.Load() != 2 {
		t.Fatalf("native call limit = %+v, delegate %d", state, delegate.calls.Load())
	}
	if err := loop.ValidateConfigReload(cfg); !errors.Is(err, providers.ErrCallBudget) {
		t.Fatalf("reload validation = %v", err)
	}
	if prepared, err := loop.PrepareConfigReload(
		t.Context(),
		delegate,
		cfg,
	); prepared != nil ||
		!errors.Is(err, providers.ErrCallBudget) {
		t.Fatalf("reload preparation = %+v/%v", prepared, err)
	}
	loop.Close()
	if delegate.closes.Load() != 0 {
		t.Fatal("injected provider acquired a second cleanup owner")
	}
	if state := budget.Seal(); !state.UsageKnown || state.InFlight != 0 || state.ReportedTokens != 12 {
		t.Fatalf("settled budget = %+v", state)
	}
}

func TestAgentProviderBudgetIncludesCapturedCompactorAndFinalRenderer(t *testing.T) {
	budget := newAgentCallBudget(t, 2)
	delegate := &agentBudgetTestProvider{}
	cfg := agentBudgetConfig(t)
	cfg.Agents.Defaults.ContextManager = "seahorse"
	loop := newBudgetAgentLoop(t, cfg, delegate, budget)
	instance := loop.registry.GetDefaultAgent()
	manager, ok := loop.contextManager.(*seahorseContextManager)
	if !ok {
		t.Fatal("native context manager was not constructed")
	}
	runtime, err := manager.runtimeFor(instance)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the captured native completion boundary, not a C3f compaction witness.
	if _, err = runtime.complete(
		t.Context(),
		"synthetic summary",
		seahorse.CompleteOptions{MaxTokens: 128},
	); err != nil {
		t.Fatal(err)
	}
	pipeline := newTestPipeline(loop)
	ts := newTurnState(instance, makeTestTurnSpec("budget-render"), turnEventScope{
		turnID: "budget-render-turn", context: newTurnContext(nil, nil, nil),
	})
	exec, err := pipeline.SetupTurn(t.Context(), ts)
	if err != nil {
		t.Fatal(err)
	}
	exec.sawSteering = true
	exec.messages = []providers.Message{{Role: "user", Content: "synthetic final render"}}
	content, rendered := tryRenderFinalTurnReply(t.Context(), true, false, ts, exec, terminalContent{})
	if !rendered || content.content != "budget fixture answer" {
		t.Fatalf("native final render = %+v/%v", content, rendered)
	}
	if err := agentBudgetChat(t.Context(), instance.Provider); !errors.Is(err, providers.ErrCallBudget) {
		t.Fatalf("primary bypass after compactor/render = %v", err)
	}
	if state := budget.Snapshot(); state.Calls != 2 || state.Completed != 2 || delegate.calls.Load() != 2 {
		t.Fatalf("shared native budget = %+v", state)
	}
}

func TestAgentProviderBudgetWrapsRegistryAndFutureModels(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"synthetic","object":"chat.completion","model":"fixture-model",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"synthetic"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`))
	}))
	defer server.Close()
	cfg := agentBudgetConfig(t)
	for _, alias := range []string{"fixture-model", "fallback", "light", "future"} {
		model := &config.ModelConfig{
			ModelName: alias, Provider: "openai", Model: "fixture-model",
			APIBase: server.URL, Enabled: true,
		}
		model.SetAPIKey("synthetic-fixture-key")
		cfg.ModelList = append(cfg.ModelList, model)
	}
	cfg.Agents.Defaults.ModelFallbacks = []string{"fallback"}
	cfg.Agents.Defaults.Routing = &config.RoutingConfig{Enabled: true, LightModel: "light"}
	budget := newAgentCallBudget(t, 6)
	loop := newBudgetAgentLoop(t, cfg, &agentBudgetTestProvider{}, budget)
	instance := loop.registry.GetDefaultAgent()
	if instance.LightProvider == nil || len(instance.CandidateProviders) != 3 {
		t.Fatalf("native routing providers absent: light %v, candidates %d",
			instance.LightProvider != nil, len(instance.CandidateProviders))
	}
	for _, provider := range []providers.LLMProvider{instance.Provider, instance.LightProvider} {
		if err := agentBudgetChat(t.Context(), provider); err != nil {
			t.Fatal(err)
		}
	}
	for _, provider := range instance.CandidateProviders {
		if err := agentBudgetChat(t.Context(), provider); err != nil {
			t.Fatal(err)
		}
	}
	created, _, err := loop.modelExecution.currentProviderFactory()(cfg.ModelList[3])
	if err != nil {
		t.Fatal(err)
	}
	defer closeProviderIfStateful(created)
	if err := agentBudgetChat(t.Context(), created); err != nil {
		t.Fatal(err)
	}
	if err := agentBudgetChat(t.Context(), instance.Provider); !errors.Is(err, providers.ErrCallBudget) {
		t.Fatalf("registry/factory bypass = %v", err)
	}
	if state := budget.Snapshot(); state.Calls != 6 || state.Completed != 6 || requests.Load() != 6 {
		t.Fatalf("registry/factory state = %+v, requests %d", state, requests.Load())
	}
}

func TestAgentProviderBudgetAdmissionAndFactoryCleanup(t *testing.T) {
	for _, test := range []struct{ nilBudget, unsupported, reload bool }{
		{nilBudget: true}, {unsupported: true}, {reload: true},
	} {
		cfg := agentBudgetConfig(t)
		cfg.Gateway.HotReload = test.reload
		budget := newAgentCallBudget(t, 1)
		if test.nilBudget {
			budget = nil
		}
		delegate := &agentBudgetTestProvider{unsupported: test.unsupported}
		messageBus := bus.NewMessageBus()
		loop, err := NewAgentLoopChecked(cfg, messageBus, delegate,
			WithIsolatedToolBootstrap(), WithIsolatedSkillBootstrap(), WithProviderCallBudget(budget))
		messageBus.Close()
		if loop != nil || !errors.Is(err, providers.ErrCallBudget) || delegate.calls.Load() != 0 ||
			delegate.closes.Load() != 0 {
			t.Fatalf("admission = %v/%v, calls %d closes %d", loop, err, delegate.calls.Load(), delegate.closes.Load())
		}
	}
	created := &agentBudgetTestProvider{unsupported: true}
	factory := budgetedModelProviderFactory(newAgentCallBudget(t, 1),
		func(*config.ModelConfig) (providers.LLMProvider, string, error) { return created, "test", nil })
	if provider, _, err := factory(
		&config.ModelConfig{},
	); provider != nil || !errors.Is(err, providers.ErrCallBudget) ||
		created.closes.Load() != 1 {
		t.Fatalf("failed factory ownership = %v/%v, closes %d", provider, err, created.closes.Load())
	}
	nilFactory := budgetedModelProviderFactory(newAgentCallBudget(t, 1),
		func(*config.ModelConfig) (providers.LLMProvider, string, error) {
			return (*agentBudgetTestProvider)(nil), "test", nil
		})
	if provider, _, err := nilFactory(
		&config.ModelConfig{},
	); provider != nil ||
		!errors.Is(err, providers.ErrCallBudget) {
		t.Fatalf("typed nil factory = %v/%v", provider, err)
	}
}
