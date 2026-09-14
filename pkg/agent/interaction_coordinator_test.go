package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

func TestInteractionCoordinatorCanonicalizesWorkspaceAliases(t *testing.T) {
	coordinator := newInteractionCoordinator(t.TempDir())
	workspace := t.TempDir()
	alias := filepath.Join(t.TempDir(), "workspace-alias")
	if err := os.Symlink(workspace, alias); err != nil {
		t.Skipf("create workspace symlink: %v", err)
	}
	canonical := coordinator.registryForWorkspace(workspace)
	throughAlias := coordinator.registryForWorkspace(alias)
	if canonical == nil || canonical != throughAlias {
		t.Fatal("workspace aliases created distinct interaction registries")
	}
}

func TestInteractionCoordinatorsDoNotShareMutableState(t *testing.T) {
	first := newInteractionCoordinator(t.TempDir())
	second := newInteractionCoordinator(t.TempDir())
	registry := interactions.NewRegistry(t.TempDir())
	first.registries.Store("workspace", registry)
	first.resolutions.Store("interaction", func(context.Context, interactions.Outcome) error { return nil })
	first.resumeFlights.Store("flight", &interactionResumeFlight{})
	first.recoveryRunning.Store(true)

	if _, ok := second.registries.Load("workspace"); ok {
		t.Fatal("interaction coordinators share registry state")
	}
	if _, ok := second.resolutions.Load("interaction"); ok {
		t.Fatal("interaction coordinators share resolution state")
	}
	if _, ok := second.resumeFlights.Load("flight"); ok {
		t.Fatal("interaction coordinators share resume-flight state")
	}
	if second.recoveryRunning.Load() {
		t.Fatal("interaction coordinators share recovery admission")
	}
}
