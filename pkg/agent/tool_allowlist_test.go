package agent

import (
	"context"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
	agenttools "github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type allowlistTestTool struct {
	name string
}

type scopedAllowlistTestTool struct {
	allowlistTestTool
	agents map[string]bool
}

func (tool *scopedAllowlistTestTool) ToolEnabledForAgent(agentID string) bool {
	return tool.agents[agentID]
}

func (t *allowlistTestTool) Name() string { return t.name }

func (t *allowlistTestTool) Description() string { return "test tool" }

func (t *allowlistTestTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (t *allowlistTestTool) Execute(
	_ context.Context,
	_ map[string]any,
) *toolshared.ToolResult {
	return toolshared.NewToolResult("ok")
}

func TestUnknownAgentToolNamesUsesConfigPolicy(t *testing.T) {
	registry := agenttools.NewToolRegistry()
	registry.Register(&allowlistTestTool{name: "read_file"})
	registry.Register(&allowlistTestTool{name: "web_search"})
	policy := &config.AgentCapabilityPolicy{
		Default: config.AgentCapabilityDefaultDeny,
		Allow:   []string{"read_file", "web_serach", "mcp_github_search", "web_*"},
		Deny:    []string{"missing_deny", "mcp_old_*"},
	}

	unknown := unknownAgentToolNames(registry, policy)
	if len(unknown) != 2 || unknown[0] != "missing_deny" || unknown[1] != "web_serach" {
		t.Fatalf("unknownAgentToolNames() = %v, want [missing_deny web_serach]", unknown)
	}
}

func TestUnknownAgentMCPServerNamesUsesConfigPolicy(t *testing.T) {
	cfg := &config.Config{
		Tools: config.ToolsConfig{
			MCP: config.MCPConfig{
				Servers: map[string]config.MCPServerConfig{
					"GitHub":     {Enabled: true},
					"filesystem": {Enabled: true},
				},
			},
		},
	}
	policy := &config.AgentCapabilityPolicy{
		Default: config.AgentCapabilityDefaultDeny,
		Allow:   []string{"github", "FileSystem", "slak", "git*"},
		Deny:    []string{"missing-deny", "legacy-*"},
	}

	unknown := unknownAgentMCPServerNames(cfg, policy)
	if len(unknown) != 2 || unknown[0] != "missing-deny" || unknown[1] != "slak" {
		t.Fatalf("unknownAgentMCPServerNames() = %v, want [missing-deny slak]", unknown)
	}
}

func TestRegisterToolOnRegistryHonorsAgentScope(t *testing.T) {
	workspace := t.TempDir()
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{Workspace: workspace},
			List: []config.AgentConfig{
				{ID: "main", Default: true, Workspace: workspace},
				{ID: "worker", Workspace: workspace},
			},
		},
	}
	registry := NewAgentRegistry(cfg, nil)
	registerToolOnRegistry(registry, &scopedAllowlistTestTool{
		allowlistTestTool: allowlistTestTool{name: "scoped"},
		agents:            map[string]bool{"main": true},
	})
	mainAgent, _ := registry.GetAgent("main")
	workerAgent, _ := registry.GetAgent("worker")
	if !mainAgent.Tools.HasRegistered("scoped") {
		t.Fatal("scoped tool was not registered for the explicitly permitted agent")
	}
	if workerAgent.Tools.HasRegistered("scoped") {
		t.Fatal("scoped tool leaked into an unpermitted agent registry")
	}
}
