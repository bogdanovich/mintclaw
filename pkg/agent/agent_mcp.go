// MintClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/mcp"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	integrationtools "github.com/bogdanovich/mintclaw/pkg/tools/integration"
)

type mcpRuntime struct {
	mu          sync.Mutex
	initialized bool
	manager     *mcp.Manager
	initErr     error
}

func (r *mcpRuntime) initialize(load func() (*mcp.Manager, error)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.initialized {
		return r.initErr
	}
	r.initialized = true
	r.manager, r.initErr = load()
	return r.initErr
}

func (r *mcpRuntime) reset(closeManager func(*mcp.Manager) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var closeErr error
	if r.manager != nil && closeManager != nil {
		closeErr = closeManager(r.manager)
	}
	r.initialized = false
	r.manager = nil
	r.initErr = nil
	return closeErr
}

func (r *mcpRuntime) takeManager() *mcp.Manager {
	r.mu.Lock()
	defer r.mu.Unlock()
	manager := r.manager
	r.manager = nil
	return manager
}

func (r *mcpRuntime) restoreManager(manager *mcp.Manager) {
	if manager == nil {
		return
	}
	r.mu.Lock()
	if r.manager == nil {
		r.manager = manager
	}
	r.mu.Unlock()
}

func (r *mcpRuntime) hasManager() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.manager != nil
}

func (r *mcpRuntime) getManager() *mcp.Manager {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.manager
}

// ensureMCPInitialized loads MCP servers/tools once so both Run() and direct
// agent mode share the same initialization path.
func (al *AgentLoop) ensureMCPInitialized(ctx context.Context) error {
	al.mu.RLock()
	isolatedToolBootstrap := al.isolatedToolBootstrap
	cfg := al.cfg
	registry := al.registry
	runtimeEvents := al.runtimeEvents
	al.mu.RUnlock()

	if isolatedToolBootstrap || cfg == nil || registry == nil {
		return nil
	}
	if !cfg.Tools.IsToolEnabled("mcp") {
		return nil
	}

	if len(cfg.Tools.MCP.Servers) == 0 {
		logger.WarnCF("agent", "MCP is enabled but no servers are configured, skipping MCP initialization", nil)
		return nil
	}

	mcpCfg := filterMCPConfigServers(cfg.Tools.MCP, registry.allowedMCPServers())
	if len(mcpCfg.Servers) == 0 {
		logger.InfoCF(
			"agent",
			"No MCP servers selected after applying per-agent mcpServers allowlists",
			nil,
		)
		return nil
	}

	findValidServer := false
	for _, serverCfg := range mcpCfg.Servers {
		if serverCfg.Enabled {
			findValidServer = true
		}
	}
	if !findValidServer {
		logger.WarnCF("agent", "MCP is enabled but no valid servers are configured, skipping MCP initialization", nil)
		return nil
	}

	return al.mcp.initialize(func() (*mcp.Manager, error) {
		mcpManager := mcp.NewManager(mcp.WithRuntimeEvents(runtimeEvents))

		defaultAgent := registry.GetDefaultAgent()
		workspacePath := cfg.WorkspacePath()
		if defaultAgent != nil && defaultAgent.Workspace != "" {
			workspacePath = defaultAgent.Workspace
		}

		if err := mcpManager.LoadFromMCPConfig(ctx, mcpCfg, workspacePath); err != nil {
			initErr := fmt.Errorf("failed to load MCP servers: %w", err)
			logger.WarnCF("agent", "Failed to load MCP servers, MCP tools will not be available",
				map[string]any{
					"error": err.Error(),
				})
			if closeErr := mcpManager.Close(); closeErr != nil {
				logger.ErrorCF("agent", "Failed to close MCP manager",
					map[string]any{
						"error": closeErr.Error(),
					})
			}
			return nil, initErr
		}

		// Register MCP tools for all agents
		servers := mcpManager.GetServers()
		uniqueTools := 0
		totalRegistrations := 0
		agentIDs := registry.ListAgentIDs()
		agentCount := len(agentIDs)

		hiddenToolsByAgent := make(map[string]int, len(agentIDs))

		for serverName, conn := range servers {
			uniqueTools += len(conn.Tools)

			// Determine whether this server's tools should be deferred (hidden).
			// Per-server "deferred" field takes precedence over the global Discovery.Enabled.
			serverCfg := mcpCfg.Servers[serverName]
			registerAsHidden := serverIsDeferred(cfg.Tools.MCP.Discovery.Enabled, serverCfg)
			visibleTools := configuredVisibleMCPTools(serverCfg)
			registeredToolsByAgent := make(map[string]mcpToolVisibilityCounts, len(agentIDs))

			for _, tool := range conn.Tools {
				for _, agentID := range agentIDs {
					agent, ok := registry.GetAgent(agentID)
					if !ok {
						continue
					}
					if !agent.AllowsMCPServer(serverName) {
						logger.DebugCF("agent", "Skipped MCP tool registration by agent mcpServers allowlist",
							map[string]any{
								"agent_id": agentID,
								"server":   serverName,
								"tool":     tool.Name,
							})
						continue
					}

					mcpTool := integrationtools.NewMCPTool(mcpManager, serverName, tool)
					toolName := mcpTool.Name()
					mcpTool.SetWorkspace(agent.Workspace)
					mcpTool.SetMaxInlineTextRunes(cfg.Tools.MCP.GetMaxInlineTextChars())
					mcpTool.SetEventPublisher(runtimeEvents)

					var registered bool
					registerVisible := !registerAsHidden || toolVisibleFromDeferredSet(toolName, visibleTools)
					if registerVisible {
						registered = registerToolIfAllowed(agent, mcpTool)
					} else {
						registered = registerHiddenToolIfAllowed(agent, mcpTool)
					}
					if !registered {
						continue
					}
					if !toolRegistryIncludes(agent.Tools, toolName) {
						continue
					}

					recordRegisteredMCPTool(registeredToolsByAgent, agentID, registerVisible)
					if !registerVisible {
						hiddenToolsByAgent[agentID]++
					}
					totalRegistrations++
					logger.DebugCF("agent", "Registered MCP tool",
						map[string]any{
							"agent_id": agentID,
							"server":   serverName,
							"tool":     tool.Name,
							"name":     toolName,
							"deferred": registerAsHidden,
							"visible":  registerVisible,
						})
				}
			}

			for _, agentID := range agentIDs {
				agent, ok := registry.GetAgent(agentID)
				if !ok {
					continue
				}
				registerMCPServerPromptContributor(
					agentID,
					agent,
					serverName,
					registeredToolsByAgent[agentID],
				)
			}
		}
		logger.InfoCF("agent", "MCP tools registered successfully",
			map[string]any{
				"server_count":        len(servers),
				"unique_tools":        uniqueTools,
				"total_registrations": totalRegistrations,
				"agent_count":         agentCount,
			})

		// Initializes Discovery Tools only if enabled by configuration
		if cfg.Tools.MCP.Enabled && cfg.Tools.MCP.Discovery.Enabled {
			useBM25 := cfg.Tools.MCP.Discovery.UseBM25
			useRegex := cfg.Tools.MCP.Discovery.UseRegex

			// Fail fast: If discovery is enabled but no search method is turned on
			if !useBM25 && !useRegex {
				initErr := fmt.Errorf(
					"tool discovery is enabled but neither 'use_bm25' nor 'use_regex' is set to true in the configuration",
				)
				if closeErr := mcpManager.Close(); closeErr != nil {
					logger.ErrorCF("agent", "Failed to close MCP manager",
						map[string]any{
							"error": closeErr.Error(),
						})
				}
				return nil, initErr
			}

			ttl := cfg.Tools.MCP.Discovery.TTL
			if ttl <= 0 {
				ttl = 5 // Default value
			}

			maxSearchResults := cfg.Tools.MCP.Discovery.MaxSearchResults
			if maxSearchResults <= 0 {
				maxSearchResults = 5 // Default value
			}

			logger.InfoCF("agent", "Initializing tool discovery", map[string]any{
				"bm25": useBM25, "regex": useRegex, "ttl": ttl, "max_results": maxSearchResults,
			})

			for _, agentID := range agentIDs {
				agent, ok := registry.GetAgent(agentID)
				if !ok {
					continue
				}
				hiddenCount := hiddenToolsByAgent[agentID]
				if agent.ContextBuilder != nil {
					if err := agent.ContextBuilder.RegisterPromptContributor(toolDiscoveryPromptContributor{
						useBM25:  useBM25 && hiddenCount > 0,
						useRegex: useRegex && hiddenCount > 0,
					}); err != nil {
						logger.WarnCF("agent", "Failed to register tool discovery prompt contributor",
							map[string]any{
								"agent_id": agentID,
								"error":    err.Error(),
							},
						)
					}
				}
				if hiddenCount <= 0 {
					continue
				}

				if useRegex {
					registerToolIfAllowed(agent, tools.NewRegexSearchTool(agent.Tools, ttl, maxSearchResults))
				}
				if useBM25 {
					registerToolIfAllowed(agent, tools.NewBM25SearchTool(agent.Tools, ttl, maxSearchResults))
				}
			}
		}

		return mcpManager, nil
	})
}

func registerMCPServerPromptContributor(
	agentID string,
	agent *AgentInstance,
	serverName string,
	counts mcpToolVisibilityCounts,
) {
	if agent == nil || agent.ContextBuilder == nil || counts.total() <= 0 {
		return
	}
	if err := agent.ContextBuilder.RegisterPromptContributor(mcpServerPromptContributor{
		serverName:   serverName,
		visibleCount: counts.visible,
		hiddenCount:  counts.hidden,
	}); err != nil {
		logger.WarnCF("agent", "Failed to register MCP prompt contributor",
			map[string]any{
				"agent_id": agentID,
				"server":   serverName,
				"error":    err.Error(),
			})
	}
}

type mcpToolVisibilityCounts struct {
	visible int
	hidden  int
}

func (c mcpToolVisibilityCounts) total() int {
	return c.visible + c.hidden
}

func recordRegisteredMCPTool(
	registeredToolsByAgent map[string]mcpToolVisibilityCounts,
	agentID string,
	registerVisible bool,
) {
	counts := registeredToolsByAgent[agentID]
	if registerVisible {
		counts.visible++
	} else {
		counts.hidden++
	}
	registeredToolsByAgent[agentID] = counts
}

func toolRegistryIncludes(registry *tools.ToolRegistry, name string) bool {
	if registry == nil {
		return false
	}
	return registry.HasRegistered(name)
}

func configuredVisibleMCPTools(serverCfg config.MCPServerConfig) map[string]struct{} {
	if len(serverCfg.VisibleTools) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(serverCfg.VisibleTools))
	for _, toolName := range serverCfg.VisibleTools {
		toolName = strings.TrimSpace(toolName)
		if toolName == "" {
			continue
		}
		out[toolName] = struct{}{}
	}
	return out
}

func toolVisibleFromDeferredSet(toolName string, visible map[string]struct{}) bool {
	if len(visible) == 0 {
		return false
	}
	_, ok := visible[toolName]
	return ok
}

func filterMCPConfigServers(
	mcpCfg config.MCPConfig,
	allowed map[string]struct{},
) config.MCPConfig {
	if allowed == nil {
		return mcpCfg
	}

	filtered := mcpCfg
	filtered.Servers = make(map[string]config.MCPServerConfig)
	normalizedAllowed := make(map[string]struct{}, len(allowed))
	for serverName := range allowed {
		name := normalizeMCPServerName(serverName)
		if name == "" {
			continue
		}
		normalizedAllowed[name] = struct{}{}
	}
	for serverName, serverCfg := range mcpCfg.Servers {
		if _, ok := normalizedAllowed[normalizeMCPServerName(serverName)]; ok {
			filtered.Servers[serverName] = serverCfg
		}
	}

	return filtered
}

// serverIsDeferred reports whether an MCP server's tools should be registered
// as hidden (deferred/discovery mode).
//
// The per-server Deferred field takes precedence over the global discoveryEnabled
// default. When Deferred is nil, discoveryEnabled is used as the fallback.
func serverIsDeferred(discoveryEnabled bool, serverCfg config.MCPServerConfig) bool {
	if !discoveryEnabled {
		return false
	}
	if serverCfg.Deferred != nil {
		return *serverCfg.Deferred
	}
	return true
}
