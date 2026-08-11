package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type preparedRegistryResult struct {
	registry *AgentRegistry
	err      error
}

// PreparedConfigReload owns an unpublished registry and provider until Commit.
type PreparedConfigReload struct {
	mu        sync.Mutex
	loop      *AgentLoop
	config    *config.Config
	provider  providers.LLMProvider
	base      *AgentRegistry
	registry  *AgentRegistry
	committed bool
}

func (al *AgentLoop) PrepareConfigReload(
	ctx context.Context,
	provider providers.LLMProvider,
	cfg *config.Config,
) (*PreparedConfigReload, error) {
	if provider == nil {
		return nil, fmt.Errorf("provider cannot be nil")
	}
	if err := al.ValidateConfigReload(cfg); err != nil {
		return nil, err
	}
	baseRegistry := al.GetRegistry()

	resultCh := make(chan preparedRegistryResult, 1)
	go func() {
		result := preparedRegistryResult{}
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.RecoverPanicNoExit(recovered)
				result.err = fmt.Errorf("panic during registry creation: %v", recovered)
				logger.ErrorCF("agent", "Panic during registry creation", map[string]any{"panic": recovered})
			}
			resultCh <- result
		}()
		result.registry = NewAgentRegistry(cfg, provider)
	}()

	var result preparedRegistryResult
	select {
	case result = <-resultCh:
		if result.registry == nil {
			if result.err != nil {
				return nil, fmt.Errorf("registry creation failed: %w", result.err)
			}
			return nil, fmt.Errorf("registry creation failed (nil result)")
		}
	case <-ctx.Done():
		go func() {
			lateResult := <-resultCh
			if lateResult.registry != nil {
				lateResult.registry.Close()
			}
		}()
		return nil, fmt.Errorf("context canceled during registry creation: %w", ctx.Err())
	}
	if err := ctx.Err(); err != nil {
		result.registry.Close()
		return nil, fmt.Errorf("context canceled after registry creation: %w", err)
	}
	if al.isolatedSkillBootstrap {
		al.isolateSkillRegistry(result.registry)
	}
	if !al.isolatedToolBootstrap {
		registerSharedTools(al, cfg, al.bus, result.registry, provider)
	}
	if err := al.registerRuntimeToolsForRegistry(cfg, result.registry); err != nil {
		result.registry.Close()
		return nil, err
	}

	return &PreparedConfigReload{
		loop:     al,
		config:   cfg,
		provider: provider,
		base:     baseRegistry,
		registry: result.registry,
	}, nil
}

func (prepared *PreparedConfigReload) RegisterTool(tool toolshared.Tool) {
	if prepared == nil {
		return
	}
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	if prepared.committed || prepared.registry == nil {
		return
	}
	registerToolOnRegistry(prepared.registry, tool)
}

func (prepared *PreparedConfigReload) Commit(ctx context.Context) error {
	if prepared == nil {
		return fmt.Errorf("prepared config reload is nil")
	}
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	if prepared.committed || prepared.registry == nil {
		return fmt.Errorf("prepared config reload is no longer available")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	al := prepared.loop
	registry := prepared.registry
	cfg := prepared.config
	al.mu.Lock()
	if al.registry != prepared.base {
		al.mu.Unlock()
		return fmt.Errorf("active agent registry changed after reload preparation")
	}
	oldRegistry := al.registry
	al.cfg = cfg
	al.registry = registry
	al.agentTurnAdmissions.update(registry)
	al.fallback = fallbackForRegistry(registry)
	al.mu.Unlock()

	prepared.committed = true
	prepared.registry = nil
	al.refreshRuntimeEventLogger(cfg)
	if al.traceCapture != nil {
		al.traceCapture.updateConfig(cfg)
	}

	oldMCPManager := al.mcp.reset()
	al.hookRuntime.reset(al)
	configureHookManagerFromConfig(al.hooks, cfg)
	if err := al.ensureHooksInitialized(ctx); err != nil {
		logger.WarnCF(
			"agent",
			"Configured hooks failed to reinitialize after reload",
			map[string]any{"error": err.Error()},
		)
	}
	if oldMCPManager != nil {
		if err := oldMCPManager.Close(); err != nil {
			logger.WarnCF(
				"agent",
				"Failed to close previous MCP manager during reload",
				map[string]any{"error": err.Error()},
			)
		}
	}
	if err := al.ensureMCPInitialized(ctx); err != nil {
		logger.WarnCF("agent", "MCP failed to reinitialize after reload", map[string]any{"error": err.Error()})
	}

	closePreviousRegistry(ctx, al, oldRegistry)
	logger.InfoCF("agent", "Provider and config reloaded successfully", map[string]any{
		"model": cfg.Agents.Defaults.GetModelName(),
	})
	return nil
}

func (prepared *PreparedConfigReload) Abort() {
	if prepared == nil {
		return
	}
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	if prepared.committed {
		return
	}
	if prepared.registry != nil {
		prepared.registry.Close()
		prepared.registry = nil
	}
	if stateful, ok := prepared.provider.(providers.StatefulProvider); ok {
		stateful.Close()
	}
}

func fallbackForRegistry(registry *AgentRegistry) *providers.FallbackChain {
	rateLimiters := providers.NewRateLimiterRegistry()
	for _, agentID := range registry.ListAgentIDs() {
		if instance, ok := registry.GetAgent(agentID); ok {
			rateLimiters.RegisterCandidates(instance.Candidates)
			rateLimiters.RegisterCandidates(instance.LightCandidates)
		}
	}
	return providers.NewFallbackChain(providers.NewCooldownTracker(), rateLimiters)
}

func closePreviousRegistry(ctx context.Context, al *AgentLoop, registry *AgentRegistry) {
	oldProvider, ok := extractProvider(registry)
	stateful, statefulProvider := oldProvider.(providers.StatefulProvider)
	if statefulProvider && !al.waitForActiveRequests(ctx, 2*time.Second) {
		if ctx.Err() != nil {
			logger.WarnCF("agent", "Context canceled during provider cleanup, forcing close", map[string]any{
				"error": ctx.Err(),
			})
		} else {
			logger.WarnCF(
				"agent",
				"Timed out waiting for active requests during provider cleanup, forcing close",
				map[string]any{
					"timeout_ms": 2000,
				},
			)
		}
	}
	if registry != nil {
		registry.Close()
	}
	if ok && statefulProvider {
		stateful.Close()
	}
}
