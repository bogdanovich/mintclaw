package agent

import (
	"testing"

	taskregistry "github.com/bogdanovich/mintclaw/pkg/tasks"
)

func TestTaskCoordinatorsDoNotShareMutableState(t *testing.T) {
	first := newTaskCoordinator(nil, nil, nil)
	second := newTaskCoordinator(nil, nil, nil)
	registry := taskregistry.NewRegistry(t.TempDir())
	first.registries.Store(normalizeRuntimeWorkspace("workspace"), registry)

	if got := second.registry("workspace"); got != nil {
		t.Fatalf("task coordinators share registry state: %p", got)
	}
	if !first.claimCompletion("completion") {
		t.Fatal("first coordinator rejected a fresh completion claim")
	}
	if first.claimCompletion("completion") {
		t.Fatal("first coordinator admitted a duplicate completion claim")
	}
	if !second.claimCompletion("completion") {
		t.Fatal("task coordinators share completion admission state")
	}
	first.releaseCompletion("completion")
	if !first.claimCompletion("completion") {
		t.Fatal("released completion claim remained reserved")
	}
}
