package agent

import (
	"context"
	"errors"
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

func TestRegisterRuntimeToolRejectsCrossAgentCollisionWithoutPartialPublish(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.List = []config.AgentConfig{{ID: "alpha"}, {ID: "beta"}}
	loop := NewAgentLoop(cfg, nil, nil)
	t.Cleanup(loop.Close)

	beta, ok := loop.GetRegistry().GetAgent("beta")
	if !ok || beta.toolComposer == nil {
		t.Fatal("beta agent composer is unavailable")
	}
	original := &runtimeComposerTestTool{name: "atomic_runtime", value: "beta-owner"}
	if err := beta.toolComposer.PutTool("test.beta-owner", original, false); err != nil {
		t.Fatal(err)
	}

	err := loop.RegisterRuntimeTool("atomic_runtime", func(*config.Config) (toolshared.Tool, error) {
		return &runtimeComposerTestTool{name: "atomic_runtime", value: "runtime-owner"}, nil
	})
	if err == nil {
		t.Fatal("RegisterRuntimeTool() collision error = nil")
	}
	alpha, ok := loop.GetRegistry().GetAgent("alpha")
	if !ok {
		t.Fatal("alpha agent is unavailable")
	}
	if alpha.Tools.HasRegistered("atomic_runtime") {
		t.Fatal("alpha published a runtime tool after beta rejected the transaction")
	}
	registered, ok := beta.Tools.Get("atomic_runtime")
	if !ok || registered != original {
		t.Fatalf("beta collision owner = %#v, want original", registered)
	}
	if _, retained := loop.runtimeToolFactories()["atomic_runtime"]; retained {
		t.Fatal("failed runtime tool factory remained registered")
	}
}

func TestRuntimeToolFactoryClassesCannotReplaceEachOtherByName(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ContextManager = "none"
	loop := NewAgentLoop(cfg, nil, nil)
	t.Cleanup(loop.Close)

	shared := &runtimeComposerTestTool{name: "owned_runtime", value: "shared-owner"}
	if err := loop.RegisterRuntimeTool("owned_runtime", func(*config.Config) (toolshared.Tool, error) {
		return shared, nil
	}); err != nil {
		t.Fatal(err)
	}
	err := loop.RegisterRuntimeAgentTool(
		"owned_runtime",
		func(*config.Config, string) (toolshared.Tool, error) {
			return &runtimeComposerTestTool{name: "owned_runtime", value: "agent-owner"}, nil
		},
	)
	if err == nil {
		t.Fatal("RegisterRuntimeAgentTool() collision error = nil")
	}
	registered, ok := loop.GetRegistry().GetDefaultAgent().Tools.Get("owned_runtime")
	if !ok || registered != shared {
		t.Fatalf("registered runtime owner = %#v, want shared owner", registered)
	}
	if _, retained := loop.runtimeAgentToolFactories()["owned_runtime"]; retained {
		t.Fatal("rejected agent runtime factory remained registered")
	}
}

func TestInjectedRuntimeToolCannotReplaceFactoryOwnerByName(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ContextManager = "none"
	loop := NewAgentLoop(cfg, nil, nil)
	t.Cleanup(loop.Close)

	owned := &runtimeComposerTestTool{name: "owned_runtime", value: "factory-owner"}
	if err := loop.RegisterRuntimeTool("owned_runtime", func(*config.Config) (toolshared.Tool, error) {
		return owned, nil
	}); err != nil {
		t.Fatal(err)
	}
	loop.RegisterTool(&runtimeComposerTestTool{name: "owned_runtime", value: "injected-owner"})

	registered, ok := loop.GetRegistry().GetDefaultAgent().Tools.Get("owned_runtime")
	if !ok || registered != owned {
		t.Fatalf("registered runtime owner = %#v, want factory owner", registered)
	}
}

func TestRefreshRuntimeToolsPreservesLiveToolWhenFactoryFails(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.List = []config.AgentConfig{{ID: "alpha"}, {ID: "beta"}}
	loop := NewAgentLoop(cfg, nil, nil)
	t.Cleanup(loop.Close)

	fail := false
	if err := loop.RegisterRuntimeTool("refresh_atomic", func(*config.Config) (toolshared.Tool, error) {
		if fail {
			return nil, errors.New("refresh failed")
		}
		return &runtimeComposerTestTool{name: "refresh_atomic", value: "stable"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	before := make(map[string]toolshared.Tool)
	for _, agentID := range []string{"alpha", "beta"} {
		agent, _ := loop.GetRegistry().GetAgent(agentID)
		before[agentID], _ = agent.Tools.Get("refresh_atomic")
	}

	fail = true
	if err := loop.RefreshRuntimeTools("refresh_atomic"); err == nil {
		t.Fatal("RefreshRuntimeTools() error = nil")
	}
	for _, agentID := range []string{"alpha", "beta"} {
		agent, _ := loop.GetRegistry().GetAgent(agentID)
		after, ok := agent.Tools.Get("refresh_atomic")
		if !ok || after != before[agentID] {
			t.Fatalf("agent %s refresh tool = %#v, want retained %#v", agentID, after, before[agentID])
		}
	}
}

func TestRefreshRuntimeToolsValidatesEveryNameBeforePublishing(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ContextManager = "none"
	loop := NewAgentLoop(cfg, nil, nil)
	t.Cleanup(loop.Close)

	firstValue := "old"
	if err := loop.RegisterRuntimeTool("refresh_first", func(*config.Config) (toolshared.Tool, error) {
		return &runtimeComposerTestTool{name: "refresh_first", value: firstValue}, nil
	}); err != nil {
		t.Fatal(err)
	}
	secondFails := false
	if err := loop.RegisterRuntimeTool("refresh_second", func(*config.Config) (toolshared.Tool, error) {
		if secondFails {
			return nil, errors.New("second refresh failed")
		}
		return &runtimeComposerTestTool{name: "refresh_second", value: "stable"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	agent := loop.GetRegistry().GetDefaultAgent()
	firstBefore, _ := agent.Tools.Get("refresh_first")
	secondBefore, _ := agent.Tools.Get("refresh_second")

	firstValue = "new"
	secondFails = true
	if err := loop.RefreshRuntimeTools("refresh_first", "refresh_second"); err == nil {
		t.Fatal("RefreshRuntimeTools() error = nil")
	}
	firstAfter, _ := agent.Tools.Get("refresh_first")
	secondAfter, _ := agent.Tools.Get("refresh_second")
	if firstAfter != firstBefore || secondAfter != secondBefore {
		t.Fatalf(
			"failed multi-tool refresh published a partial generation: first=%#v second=%#v",
			firstAfter,
			secondAfter,
		)
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
