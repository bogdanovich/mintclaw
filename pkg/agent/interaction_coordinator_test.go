package agent

import (
	"context"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

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
