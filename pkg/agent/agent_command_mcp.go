package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/commands"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

func (al *AgentLoop) bindMCPCommandCapabilities(rt *commands.Runtime, cfg *config.Config) {
	rt.MCPIntegrationEnabled = cfg != nil && cfg.Tools.IsToolEnabled("mcp")
	rt.ListMCPServers = func(ctx context.Context) []commands.MCPServerInfo {
		if cfg == nil || len(cfg.Tools.MCP.Servers) == 0 {
			return nil
		}

		if err := al.ensureMCPInitialized(ctx); err != nil {
			logger.WarnCF("agent", "Failed to refresh MCP status for command", map[string]any{
				"error": err.Error(),
			})
		}

		connected := make(map[string]int)
		if manager := al.mcp.getManager(); manager != nil {
			for serverName, conn := range manager.GetServers() {
				connected[serverName] = len(conn.Tools)
			}
		}

		servers := make([]commands.MCPServerInfo, 0, len(cfg.Tools.MCP.Servers))
		for serverName, serverCfg := range cfg.Tools.MCP.Servers {
			toolCount, isConnected := connected[serverName]
			servers = append(servers, commands.MCPServerInfo{
				Name:      serverName,
				Enabled:   serverCfg.Enabled,
				Deferred:  serverIsDeferred(cfg.Tools.MCP.Discovery.Enabled, serverCfg),
				Connected: isConnected,
				ToolCount: toolCount,
			})
		}

		slices.SortFunc(servers, func(a, b commands.MCPServerInfo) int {
			return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		})
		return servers
	}

	rt.ListMCPTools = func(ctx context.Context, serverName string) ([]commands.MCPToolInfo, error) {
		if cfg == nil {
			return nil, fmt.Errorf("command unavailable: config not loaded")
		}

		serverName = strings.TrimSpace(serverName)
		if serverName == "" {
			return nil, fmt.Errorf("server name is required")
		}

		resolvedName := ""
		var serverCfg config.MCPServerConfig
		for name, candidate := range cfg.Tools.MCP.Servers {
			if strings.EqualFold(name, serverName) {
				resolvedName = name
				serverCfg = candidate
				break
			}
		}
		if resolvedName == "" {
			return nil, fmt.Errorf("MCP server '%s' is not configured", serverName)
		}
		if !serverCfg.Enabled {
			return nil, fmt.Errorf("MCP server '%s' is configured but disabled", resolvedName)
		}
		if !cfg.Tools.IsToolEnabled("mcp") {
			return nil, fmt.Errorf("MCP integration is disabled")
		}

		if err := al.ensureMCPInitialized(ctx); err != nil {
			logger.WarnCF("agent", "Failed to initialize MCP runtime for command", map[string]any{
				"server": resolvedName,
				"error":  err.Error(),
			})
		}

		manager := al.mcp.getManager()
		if manager == nil {
			return nil, fmt.Errorf("MCP server '%s' is configured but not connected", resolvedName)
		}

		conn, ok := manager.GetServer(resolvedName)
		if !ok {
			return nil, fmt.Errorf("MCP server '%s' is configured but not connected", resolvedName)
		}

		toolInfos := make([]commands.MCPToolInfo, 0, len(conn.Tools))
		for _, tool := range conn.Tools {
			if tool == nil {
				continue
			}
			name := strings.TrimSpace(tool.Name)
			if name == "" {
				continue
			}

			description := strings.TrimSpace(tool.Description)
			if description == "" {
				description = fmt.Sprintf("MCP tool from %s server", resolvedName)
			}
			toolInfos = append(toolInfos, commands.MCPToolInfo{
				Name:        name,
				Description: description,
				Parameters:  summarizeMCPToolParameters(tool.InputSchema),
			})
		}
		slices.SortFunc(toolInfos, func(a, b commands.MCPToolInfo) int {
			return cmp.Compare(a.Name, b.Name)
		})
		return toolInfos, nil
	}
}

func summarizeMCPToolParameters(schema any) []commands.MCPToolParameterInfo {
	schemaMap := normalizeMCPSchema(schema)
	properties, ok := schemaMap["properties"].(map[string]any)
	if !ok || len(properties) == 0 {
		return nil
	}

	required := make(map[string]struct{})
	switch raw := schemaMap["required"].(type) {
	case []string:
		for _, name := range raw {
			required[name] = struct{}{}
		}
	case []any:
		for _, value := range raw {
			name, ok := value.(string)
			if ok {
				required[name] = struct{}{}
			}
		}
	}

	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)

	params := make([]commands.MCPToolParameterInfo, 0, len(names))
	for _, name := range names {
		param := commands.MCPToolParameterInfo{Name: name}
		if propMap, ok := properties[name].(map[string]any); ok {
			if typeName, ok := propMap["type"].(string); ok {
				param.Type = strings.TrimSpace(typeName)
			}
			if desc, ok := propMap["description"].(string); ok {
				param.Description = strings.TrimSpace(desc)
			}
		}
		_, param.Required = required[name]
		params = append(params, param)
	}
	return params
}

func normalizeMCPSchema(schema any) map[string]any {
	if schema == nil {
		return emptyMCPSchema()
	}
	if schemaMap, ok := schema.(map[string]any); ok {
		return schemaMap
	}

	var jsonData []byte
	switch raw := schema.(type) {
	case json.RawMessage:
		jsonData = raw
	case []byte:
		jsonData = raw
	}
	if jsonData == nil {
		var err error
		jsonData, err = json.Marshal(schema)
		if err != nil {
			return emptyMCPSchema()
		}
	}

	var result map[string]any
	if err := json.Unmarshal(jsonData, &result); err != nil {
		return emptyMCPSchema()
	}
	return result
}

func emptyMCPSchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
		"required":   []string{},
	}
}
