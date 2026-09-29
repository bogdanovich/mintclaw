package agent

import (
	"path/filepath"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	"github.com/bogdanovich/mintclaw/pkg/state"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

func TestWithStateManagerRetainsInjectedManager(t *testing.T) {
	cfg := &config.Config{Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
		Workspace: t.TempDir(), ModelName: "test-model", MaxTokens: 100, MaxToolIterations: 2,
		ContextManager: "none",
	}, List: []config.AgentConfig{{ID: "main", Default: true}}}}
	manager := state.NewManager(t.TempDir())
	loop := NewAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{}, WithStateManager(manager))
	t.Cleanup(loop.Close)

	if loop.StateManager() != manager {
		t.Fatal("agent loop replaced the injected state owner")
	}
}

func TestWithIsolatedToolBootstrapSkipsSharedProductionStateAndTools(t *testing.T) {
	cfg := &config.Config{Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
		Workspace: t.TempDir(), ModelName: "test-model", MaxTokens: 100, MaxToolIterations: 2,
		ContextManager: "none",
	}, List: []config.AgentConfig{{ID: "main", Default: true}}}}
	loop := NewAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{}, WithIsolatedToolBootstrap())
	t.Cleanup(loop.Close)

	if loop.state != nil {
		t.Fatal("isolated bootstrap constructed production state manager")
	}
	instance := loop.registry.GetDefaultAgent()
	if instance == nil {
		t.Fatal("isolated bootstrap has no default agent")
	}
	if got := instance.Tools.List(); len(got) != 0 {
		t.Fatalf("isolated bootstrap registered production tools: %v", got)
	}
}

func TestWithIsolatedSkillBootstrapUsesOnlyWorkspaceSkillRoot(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv(config.EnvHome, t.TempDir())
	cfg := &config.Config{Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
		Workspace: workspace, ModelName: "test-model", MaxTokens: 100, MaxToolIterations: 2,
		ContextManager: "none",
	}, List: []config.AgentConfig{{ID: "main", Default: true}}}}
	loop := NewAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{}, WithIsolatedSkillBootstrap())
	t.Cleanup(loop.Close)

	instance := loop.registry.GetDefaultAgent()
	if instance == nil || instance.ContextBuilder == nil {
		t.Fatal("isolated skill bootstrap has no context builder")
	}
	roots := instance.ContextBuilder.skillsLoader.SkillRoots()
	want := filepath.Join(normalizeRuntimeWorkspace(workspace), "skills")
	if len(roots) != 1 || roots[0] != want {
		t.Fatalf("isolated skill roots = %v, want [%s]", roots, want)
	}
}

func TestGatewayDocumentCapabilitySurfaceBaseline(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	msgBus := bus.NewMessageBus()
	t.Cleanup(msgBus.Close)
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ContextManager = "none"
	loop := NewAgentLoop(cfg, msgBus, &mockProvider{}, WithIsolatedSkillBootstrap())
	t.Cleanup(loop.Close)

	registry := loop.GetRegistry().GetDefaultAgent().Tools
	if !documentToolAvailable() {
		if registry.HasRegistered("document") {
			t.Fatal("gateway registered document without the required inspect/extract/render backends")
		}
		return
	}
	if !registry.HasRegistered("document") {
		t.Fatal("gateway omitted the configured document capability")
	}
	if !registry.HasRegistered(tools.BM25SearchToolName) {
		t.Fatal("gateway document capability omitted its deferred-discovery tool")
	}
	for _, capability := range []runtimecap.CapabilityID{
		runtimecap.CapabilityDocumentInspect,
		runtimecap.CapabilityDocumentExtract,
		runtimecap.CapabilityDocumentRender,
	} {
		availability, found := loop.CapabilityReport().Lookup(capability)
		if !found || !availability.Available {
			t.Fatalf("gateway document capability %s = %#v, found=%t", capability, availability, found)
		}
	}
	for _, definition := range registry.ToProviderDefs() {
		if definition.Function.Name == "document" {
			t.Fatal("gateway exposed the hidden document tool before deferred discovery")
		}
	}
}
