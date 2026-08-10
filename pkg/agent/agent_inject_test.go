package agent

import (
	"context"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type decoratorTestTool struct{ id string }

type decoratorTestWrapper struct {
	toolshared.Tool
	id string
}

func (*decoratorTestTool) Name() string               { return "read_file" }
func (*decoratorTestTool) Description() string        { return "read" }
func (*decoratorTestTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (tool *decoratorTestTool) Execute(context.Context, map[string]any) *toolshared.ToolResult {
	return toolshared.NewToolResult(tool.id)
}

func TestDecorateRuntimeToolOnRegistryPreservesPerAgentLocalTool(t *testing.T) {
	mainTools := tools.NewToolRegistry()
	opsTools := tools.NewToolRegistry()
	mainLocal := &decoratorTestTool{id: "main-local"}
	opsLocal := &decoratorTestTool{id: "ops-local"}
	mainTools.Register(mainLocal)
	opsTools.Register(opsLocal)
	registry := &AgentRegistry{agents: map[string]*AgentInstance{
		"main": {ID: "main", Tools: mainTools},
		"ops":  {ID: "ops", Tools: opsTools},
	}}
	seen := make(map[string]toolshared.Tool)
	err := decorateRuntimeToolOnRegistry(
		config.DefaultConfig(),
		registry,
		"read_file",
		func(_ *config.Config, agentID string, local toolshared.Tool) (toolshared.Tool, error) {
			seen[agentID] = local
			return local, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if seen["main"] != mainLocal || seen["ops"] != opsLocal {
		t.Fatalf("decorator locals = %#v", seen)
	}
}

func TestRegisterRuntimeToolDecoratorDoesNotStackAcrossReloadRecovery(t *testing.T) {
	newRegistry := func(id string) *AgentRegistry {
		registry := tools.NewToolRegistry()
		registry.Register(&decoratorTestTool{id: id})
		return &AgentRegistry{agents: map[string]*AgentInstance{
			"main": {ID: "main", Tools: registry},
		}}
	}
	wrap := func(id string, calls *int) RuntimeToolDecoratorFactory {
		return func(_ *config.Config, _ string, local toolshared.Tool) (toolshared.Tool, error) {
			*calls++
			return &decoratorTestWrapper{Tool: local, id: id}, nil
		}
	}
	assertWrapper := func(t *testing.T, registry *AgentRegistry, wantID string, wantDepth int) {
		t.Helper()
		agent, ok := registry.GetAgent("main")
		if !ok {
			t.Fatal("main agent is unavailable")
		}
		tool, ok := agent.Tools.Get("read_file")
		if !ok {
			t.Fatal("read_file is unavailable")
		}
		depth := 0
		for {
			wrapper, wrapped := tool.(*decoratorTestWrapper)
			if !wrapped {
				break
			}
			depth++
			if depth == 1 && wrapper.id != wantID {
				t.Fatalf("wrapper id = %q, want %q", wrapper.id, wantID)
			}
			tool = wrapper.Tool
		}
		if depth != wantDepth {
			t.Fatalf("wrapper depth = %d, want %d", depth, wantDepth)
		}
	}

	cfg := config.DefaultConfig()
	current := newRegistry("current")
	loop := &AgentLoop{cfg: cfg, registry: current}
	firstCalls := 0
	if err := loop.RegisterRuntimeToolDecorator("read_file", wrap("first", &firstCalls)); err != nil {
		t.Fatal(err)
	}
	assertWrapper(t, current, "first", 1)

	// Failed reload recovery retains the current registry and reruns service
	// registration. The factory changes, but the retained tool stays one layer.
	secondCalls := 0
	if err := loop.RegisterRuntimeToolDecorator("read_file", wrap("second", &secondCalls)); err != nil {
		t.Fatal(err)
	}
	assertWrapper(t, current, "first", 1)
	if secondCalls != 0 {
		t.Fatalf("second factory calls on retained registry = %d, want 0", secondCalls)
	}

	// A successful reload decorates its fresh registry from the retained
	// factory. The subsequent service registration must not stack it.
	fresh := newRegistry("fresh")
	if err := loop.registerRuntimeToolsForRegistry(cfg, fresh); err != nil {
		t.Fatal(err)
	}
	loop.mu.Lock()
	loop.registry = fresh
	loop.mu.Unlock()
	if err := loop.RegisterRuntimeToolDecorator("read_file", wrap("third", new(int))); err != nil {
		t.Fatal(err)
	}
	assertWrapper(t, fresh, "second", 1)
	if firstCalls != 1 || secondCalls != 1 {
		t.Fatalf("decorator factory calls = first:%d second:%d, want 1 each", firstCalls, secondCalls)
	}
}

func TestRegisterRuntimeAgentToolProjectsEachAgentSeparately(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.List = []config.AgentConfig{{ID: "alpha"}, {ID: "beta"}}
	loop := NewAgentLoop(cfg, nil, nil, nil)
	if err := loop.RegisterRuntimeAgentTool(
		"scoped",
		func(_ *config.Config, agentID string) (toolshared.Tool, error) {
			return &runtimeAgentTestTool{agentID: agentID}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	for _, agentID := range []string{"alpha", "beta"} {
		instance, ok := loop.GetRegistry().GetAgent(agentID)
		if !ok {
			t.Fatalf("agent %s is missing", agentID)
		}
		registered, ok := instance.Tools.Get("scoped")
		if !ok || registered.(*runtimeAgentTestTool).agentID != agentID {
			t.Fatalf("agent %s received %#v", agentID, registered)
		}
	}
}

type runtimeAgentTestTool struct{ agentID string }

func (*runtimeAgentTestTool) Name() string        { return "scoped" }
func (*runtimeAgentTestTool) Description() string { return "agent-scoped runtime test tool" }
func (tool *runtimeAgentTestTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "agent": tool.agentID}
}

func (*runtimeAgentTestTool) Execute(context.Context, map[string]any) *toolshared.ToolResult {
	return toolshared.NewToolResult("ok")
}

func TestPreparedConfigReloadRefreshesGenerationBoundRuntimeTool(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ContextManager = "none"
	loop := NewAgentLoop(cfg, nil, &mockProvider{})
	generation := "before-reconcile"
	if err := loop.RegisterRuntimeTool(
		"generation_test",
		func(*config.Config) (toolshared.Tool, error) {
			return &refreshRuntimeTestTool{generation: generation}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	prepared, err := loop.PrepareConfigReload(t.Context(), &mockProvider{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(prepared.Abort)
	generation = "after-reconcile"
	if err := prepared.RefreshRuntimeTools("generation_test"); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	tool, ok := loop.GetRegistry().GetDefaultAgent().Tools.Get("generation_test")
	if !ok || tool.(*refreshRuntimeTestTool).generation != "after-reconcile" {
		t.Fatalf("refreshed runtime tool = %#v", tool)
	}
}

type refreshRuntimeTestTool struct{ generation string }

func (*refreshRuntimeTestTool) Name() string               { return "generation_test" }
func (*refreshRuntimeTestTool) Description() string        { return "generation-bound test tool" }
func (*refreshRuntimeTestTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (*refreshRuntimeTestTool) Execute(context.Context, map[string]any) *toolshared.ToolResult {
	return toolshared.NewToolResult("ok")
}
