package agent

import (
	"context"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type decoratorTestTool struct{ id string }

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
