package thread

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/coding/workspace"
)

func TestStorePublishesImmutableRepositoryBaselineUnderThreadLease(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	project, err := ResolveProject(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := NewMetadata(NewThreadID(), project, "baseline", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionThread(metadata.ThreadID); err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	baseline, err := workspace.NewRepository(root, root, workspace.Limits{}).CaptureBaseline(
		t.Context(),
		workspace.BaselineRequest{
			ProjectKey: project.ProjectKey,
			CapturedAt: time.Now().UTC(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PublishRepositoryBaseline(t.Context(), lease, metadata, baseline); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadRepositoryBaselineWithLease(t.Context(), lease, metadata)
	if err != nil || loaded.BaselineID != baseline.BaselineID {
		t.Fatalf("loaded baseline = %#v, %v", loaded, err)
	}
	if err := store.PublishRepositoryBaseline(
		t.Context(),
		lease,
		metadata,
		baseline,
	); !errors.Is(
		err,
		ErrRepositoryBaselineExists,
	) {
		t.Fatalf("second publication error = %v", err)
	}
	path := filepath.Join(root, "coding", "threads", metadata.ThreadID, "repository", "baseline.json")
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("baseline file = %#v, %v", info, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	withRemovedOrigin := bytes.Replace(
		data,
		[]byte("  \"captured_at\":"),
		[]byte("  \"origin\": \"new_thread\",\n  \"captured_at\":"),
		1,
	)
	if bytes.Equal(data, withRemovedOrigin) {
		t.Fatal("failed to add removed origin field to baseline fixture")
	}
	if err := os.WriteFile(path, withRemovedOrigin, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadRepositoryBaselineWithLease(t.Context(), lease, metadata); err == nil ||
		!strings.Contains(err.Error(), `unknown field "origin"`) {
		t.Fatalf("removed origin field load error = %v", err)
	}
}

func TestStoreRejectsRepositoryBaselineWithoutOwningLease(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	project, err := ResolveProject(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := NewMetadata(NewThreadID(), project, "baseline", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionThread(metadata.ThreadID); err != nil {
		t.Fatal(err)
	}
	baseline, err := workspace.NewRepository(root, root, workspace.Limits{}).CaptureBaseline(
		t.Context(),
		workspace.BaselineRequest{
			ProjectKey: project.ProjectKey,
			CapturedAt: time.Now().UTC(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PublishRepositoryBaseline(t.Context(), nil, metadata, baseline); err == nil {
		t.Fatal("baseline publication accepted a nil lease")
	}
}
