package agent

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

// interactionCoordinator owns process-wide human-interaction state. Runner
// generations may replace delivery wiring, but durable registries, callbacks,
// resume flights, and recovery admission remain stable for the loop lifetime.
type interactionCoordinator struct {
	registries      sync.Map
	resolutions     sync.Map
	resumeFlights   sync.Map
	catalog         *interactions.WorkspaceCatalog
	catalogMu       sync.Mutex
	recoveryRunning atomic.Bool
	currentConfig   func() *config.Config
	codingProfile   *CodingRuntimeProfile
	observe         func(string, interactions.EventObservation)
}

func (c *interactionCoordinator) configure(
	currentConfig func() *config.Config,
	codingProfile *CodingRuntimeProfile,
	observe func(string, interactions.EventObservation),
) {
	if c == nil {
		return
	}
	c.currentConfig = currentConfig
	c.codingProfile = codingProfile
	c.observe = observe
}

func (c *interactionCoordinator) registryForWorkspace(workspace string) *interactions.Registry {
	if c == nil {
		return nil
	}
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return nil
	}
	if existing, ok := c.registries.Load(workspace); ok {
		registry, _ := existing.(*interactions.Registry)
		return registry
	}
	options := interactions.Options{}
	if c.currentConfig != nil {
		if cfg := c.currentConfig(); cfg != nil {
			options.TerminalRetention = cfg.Tools.RequestUserInput.Retention()
		}
	}
	storePath := interactions.WorkspaceStorePath(workspace)
	if layout, ok := codingLayoutForWorkspace(c.codingProfile, workspace); ok {
		storePath = layout.StatePaths().InteractionFile
	}
	registry := interactions.NewRegistryWithOptions(storePath, options)
	actual, loaded := c.registries.LoadOrStore(workspace, registry)
	stored, _ := actual.(*interactions.Registry)
	if stored == nil {
		stored = registry
	}
	if !loaded {
		if c.observe != nil {
			stored.Subscribe(func(observation interactions.EventObservation) {
				c.observe(workspace, observation)
			})
		}
		stats := stored.Stats()
		logger.InfoCF("agent", "Loaded human interaction registry", map[string]any{
			"workspace":       workspace,
			"records":         stats.RecordCount,
			"nonterminal":     stats.NonterminalCount,
			"retention_hours": int(stats.Retention / time.Hour),
			"load_error":      errString(stored.LastLoadError()),
		})
	}
	return stored
}

func newInteractionCoordinator(home string) interactionCoordinator {
	return interactionCoordinator{
		catalog: interactions.NewWorkspaceCatalog(home),
	}
}

func (c *interactionCoordinator) create(
	workspace string,
	registry *interactions.Registry,
	request interactions.CreateRequest,
) (interactions.Record, error) {
	c.catalogMu.Lock()
	defer c.catalogMu.Unlock()
	if c.catalog != nil {
		if err := c.catalog.Register(workspace); err != nil {
			return interactions.Record{}, fmt.Errorf("register interaction workspace: %w", err)
		}
	}
	return registry.Create(request)
}

func (c *interactionCoordinator) prune(
	workspace string,
	registry *interactions.Registry,
	now time.Time,
) error {
	c.catalogMu.Lock()
	defer c.catalogMu.Unlock()
	if err := registry.Prune(now); err != nil {
		return fmt.Errorf("prune registry: %w", err)
	}
	if registry.LastLoadError() != nil || registry.Stats().RecordCount != 0 || c.catalog == nil {
		return nil
	}
	if err := c.catalog.Remove(workspace); err != nil {
		return fmt.Errorf("remove empty workspace: %w", err)
	}
	return nil
}

func (c *interactionCoordinator) catalogedWorkspaces() ([]string, error) {
	c.catalogMu.Lock()
	defer c.catalogMu.Unlock()
	if c.catalog == nil {
		return nil, nil
	}
	return c.catalog.List()
}

func (c *interactionCoordinator) activeTaskIDs(workspace string) (map[string]struct{}, error) {
	if c == nil {
		return nil, fmt.Errorf("interaction coordinator is unavailable")
	}
	registry := c.registryForWorkspace(workspace)
	if registry == nil {
		return nil, fmt.Errorf("interaction registry is unavailable")
	}
	if err := registry.LastLoadError(); err != nil {
		return nil, fmt.Errorf("load interaction registry: %w", err)
	}
	return registry.NonterminalTaskIDs(), nil
}
