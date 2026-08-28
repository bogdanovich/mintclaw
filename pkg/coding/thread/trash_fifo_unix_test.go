//go:build unix

package thread

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestDeletePlanRejectsMetadataFIFOWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	projectRoot := filepath.Join(root, "project")
	if err := os.Mkdir(projectRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := ResolveProject(t.Context(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := NewMetadata(NewThreadID(), project, "safe", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(metadata); err != nil {
		t.Fatal(err)
	}
	threadRoot, _ := store.ThreadRoot(metadata.ThreadID)
	metadataPath := filepath.Join(threadRoot, metadataFileName)
	if err := os.Remove(metadataPath); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(metadataPath, 0o600); err != nil {
		t.Skipf("FIFO unavailable: %v", err)
	}
	if _, err := store.PlanDelete(metadata.ThreadID); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("PlanDelete() error = %v", err)
	}
}
