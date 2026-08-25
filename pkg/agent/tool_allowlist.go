package agent

import (
	"sort"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

const dynamicMCPToolPrefix = "mcp_"

func normalizeMCPServerName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func normalizedMCPServerNameSet(
	servers map[string]config.MCPServerConfig,
) map[string]struct{} {
	normalized := make(map[string]struct{}, len(servers))
	for serverName := range servers {
		name := normalizeMCPServerName(serverName)
		if name == "" {
			continue
		}
		normalized[name] = struct{}{}
	}
	return normalized
}

func warnOnUnknownAgentToolDeclarations(
	agentID, workspace string,
	policy *config.AgentCapabilityPolicy,
	registry *tools.ToolRegistry,
) {
	if registry == nil {
		return
	}

	if unknownTools := unknownAgentToolNames(registry, policy); len(unknownTools) > 0 {
		logger.WarnCF("agent", "Agent config declares unregistered tool names",
			map[string]any{
				"agent_id":  agentID,
				"workspace": workspace,
				"tools":     unknownTools,
			})
	}
}

func warnOnUnknownAgentMCPServerDeclarations(
	agentID, workspace string,
	cfg *config.Config,
	policy *config.AgentCapabilityPolicy,
) {
	if cfg == nil {
		return
	}

	if unknownServers := unknownAgentMCPServerNames(cfg, policy); len(unknownServers) > 0 {
		logger.WarnCF("agent", "Agent config declares unknown MCP server names",
			map[string]any{
				"agent_id":    agentID,
				"workspace":   workspace,
				"mcp_servers": unknownServers,
			})
	}
}

func unknownAgentToolNames(
	registry *tools.ToolRegistry,
	policy *config.AgentCapabilityPolicy,
) []string {
	if policy == nil {
		return nil
	}

	known := registeredRuntimeToolNames(registry)
	unknown := make(map[string]struct{})
	patterns := append(append([]string(nil), policy.Allow...), policy.Deny...)
	for _, raw := range patterns {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" || strings.HasPrefix(name, dynamicMCPToolPrefix) || containsGlobMeta(name) {
			continue
		}
		if _, ok := known[name]; ok {
			continue
		}
		unknown[name] = struct{}{}
	}

	return sortedKeys(unknown)
}

func registeredRuntimeToolNames(registry *tools.ToolRegistry) map[string]struct{} {
	known := make(map[string]struct{})
	if registry == nil {
		return known
	}
	for _, raw := range registry.List() {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		known[name] = struct{}{}
	}
	return known
}

func unknownAgentMCPServerNames(cfg *config.Config, policy *config.AgentCapabilityPolicy) []string {
	if cfg == nil || policy == nil {
		return nil
	}

	knownServers := normalizedMCPServerNameSet(cfg.Tools.MCP.Servers)
	unknown := make(map[string]struct{})
	patterns := append(append([]string(nil), policy.Allow...), policy.Deny...)
	for _, raw := range patterns {
		name := normalizeMCPServerName(raw)
		if name == "" || containsGlobMeta(name) {
			continue
		}
		if _, ok := knownServers[name]; ok {
			continue
		}
		unknown[name] = struct{}{}
	}

	return sortedKeys(unknown)
}

func sortedKeys(values map[string]struct{}) []string {
	if len(values) == 0 {
		return nil
	}

	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func containsGlobMeta(value string) bool {
	return strings.ContainsAny(value, "*?[")
}
