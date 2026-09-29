package agent

import (
	"path"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func agentAllowsTool(agent *AgentInstance, toolName string) bool {
	if agent == nil {
		return true
	}
	return toolAllowedByPolicy(agent.ToolPolicy, toolName)
}

func toolAllowedByPolicy(policy *config.AgentCapabilityPolicy, toolName string) bool {
	if policy == nil {
		return true
	}

	allowed := policy.Default == config.AgentCapabilityDefaultAllow
	if policy.Default == config.AgentCapabilityDefaultDeny {
		allowed = matchesAnyGlob(toolName, policy.Allow)
	}
	if !allowed {
		return false
	}
	if len(policy.Deny) > 0 && matchesAnyGlob(toolName, policy.Deny) {
		return false
	}
	return true
}

func matchesAnyGlob(name string, patterns []string) bool {
	for _, pattern := range patterns {
		if pattern == "" {
			continue
		}
		if ok, err := path.Match(pattern, name); err == nil && ok {
			return true
		}
	}
	return false
}

func registerToolIfAllowed(agent *AgentInstance, tool toolshared.Tool) bool {
	registered, err := putRuntimeToolIfAllowed(agent, tool, false)
	if err != nil {
		logger.ErrorCF("agent", "Failed to compose runtime tool", map[string]any{
			"agent_id": agent.ID,
			"tool":     tool.Name(),
			"error":    err.Error(),
		})
	}
	return registered && err == nil
}

func registerHiddenToolIfAllowed(agent *AgentInstance, tool toolshared.Tool) bool {
	registered, err := putRuntimeToolIfAllowed(agent, tool, true)
	if err != nil {
		logger.ErrorCF("agent", "Failed to compose hidden runtime tool", map[string]any{
			"agent_id": agent.ID,
			"tool":     tool.Name(),
			"error":    err.Error(),
		})
	}
	return registered && err == nil
}

func putRuntimeToolIfAllowed(
	agent *AgentInstance,
	tool toolshared.Tool,
	hidden bool,
) (bool, error) {
	if agent == nil || agent.Tools == nil || tool == nil {
		return false, nil
	}
	allowed := agentAllowsTool(agent, tool.Name())
	if !allowed {
		logger.DebugCF("agent", "Skipped tool by agent filter", map[string]any{
			"agent_id": agent.ID,
			"tool":     tool.Name(),
		})
	}
	if agent.toolComposer != nil {
		if err := agent.toolComposer.PutTool(runtimeToolContributorName(tool.Name()), tool, hidden); err != nil {
			return false, err
		}
		return allowed, nil
	}
	if !allowed {
		return false, nil
	}
	if hidden {
		agent.Tools.RegisterHidden(tool)
	} else {
		agent.Tools.Register(tool)
	}
	return true, nil
}

func runtimeToolContributorName(toolName string) string {
	return runtimeOwnedToolContributorName("builtin", toolName)
}

func runtimeInjectedToolContributorName(toolName string) string {
	return runtimeOwnedToolContributorName("injected", toolName)
}

func runtimeFactoryToolContributorName(toolName string) string {
	return runtimeOwnedToolContributorName("factory", toolName)
}

func runtimeAgentFactoryToolContributorName(toolName string) string {
	return runtimeOwnedToolContributorName("agent-factory", toolName)
}

func runtimeOwnedToolContributorName(owner, toolName string) string {
	return "runtime." + owner + ".tool." + strings.TrimSpace(toolName)
}
