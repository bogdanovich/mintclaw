//go:build !windows

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/tasks"
)

func TestTaskRegistryReadinessPrecedesPublication(t *testing.T) {
	workspace := normalizeRuntimeWorkspace(t.TempDir())
	store := tasks.WorkspaceStorePath(workspace)
	initial := tasks.NewRegistry(store)
	if err := initial.Create(tasks.Record{TaskID: "old-runtime-task", Status: tasks.StatusRunning}); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(interactions.WorkspaceStorePath(workspace)+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	var release sync.Once
	unlock := func() {
		release.Do(func() {
			if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
				t.Error(err)
			}
		})
	}
	defer unlock()
	owner := newInteractionCoordinator(t.TempDir())
	coordinator := newTaskCoordinator(nil, nil, &owner)
	done := make(chan *tasks.Registry, 2)
	go func() { done <- coordinator.configuredRegistry(workspace) }()
	// A ready registry must remain unavailable while the authoritative relation
	// is locked. This also catches a loader that publishes then restores.
	select {
	case <-done:
		t.Fatal("registry returned before restoration could acquire the interaction lock")
	case <-time.After(100 * time.Millisecond):
	}
	if published := coordinator.registry(workspace); published != nil {
		t.Fatal("unready registry was published")
	}
	go func() { done <- coordinator.configuredRegistry(filepath.Join(workspace, ".")) }()
	// Other workspaces must not wait behind a contended restoration.
	otherDone := make(chan *tasks.Registry, 1)
	go func() { otherDone <- coordinator.configuredRegistry(t.TempDir()) }()
	select {
	case <-otherDone:
	case <-time.After(3 * time.Second):
		t.Fatal("unrelated workspace initialization blocked")
	}
	unlock()
	var registry *tasks.Registry
	for i := range 2 {
		select {
		case ready := <-done:
			if registry != nil && ready != registry {
				t.Fatal("concurrent callers received different registries")
			}
			registry = ready
			old, ok := ready.Get("old-runtime-task")
			if !ok || old.Status != tasks.StatusLost {
				t.Fatalf("restoration incomplete: %#v", old)
			}
			if err := ready.Create(
				tasks.Record{TaskID: fmt.Sprintf("new-live-%d", i), Status: tasks.StatusRunning},
			); err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("restoration did not finish after lock release")
		}
	}
	if registry != coordinator.registry(workspace) {
		t.Fatal("ready registry not published")
	}
	for i := range 2 {
		rec, ok := registry.Get(fmt.Sprintf("new-live-%d", i))
		if !ok || rec.Status != tasks.StatusRunning {
			t.Fatalf("new live work was reconciled: %#v", rec)
		}
	}
}
