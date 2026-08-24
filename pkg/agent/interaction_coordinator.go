package agent

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
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
