//go:build unix

package thread

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	codingreview "github.com/bogdanovich/mintclaw/pkg/coding/review"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
)

func TestLatestReviewRestoreRejectsFIFOWithoutBlocking(t *testing.T) {
	projectRoot := t.TempDir()
	store, err := NewStore(filepath.Join(t.TempDir(), "coding"))
	if err != nil {
		t.Fatal(err)
	}
	project, err := ResolveProject(t.Context(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := NewMetadata(NewThreadID(), project, "FIFO review", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(metadata); err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	result := codingreview.Result{
		SchemaVersion: codingreview.SchemaVersion, ReviewID: codingreview.NewID(),
		Target: codingreview.Target{Kind: codingreview.TargetCurrent}, EvidenceGeneration: "generation-1",
		Summary: "Completed.", CompletedAt: time.Now().UTC(),
	}
	diff := codingworkspace.DiffResult{
		SchemaVersion: codingworkspace.RepositoryDiffSchemaV1, RepositoryAvailable: true,
		Target:             codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetCurrent},
		EvidenceGeneration: "generation-1",
	}
	if err := store.PublishReviewResult(t.Context(), lease, metadata, result, diff); err != nil {
		t.Fatal(err)
	}
	reviewRoot := filepath.Join(
		store.Root(), "threads", metadata.ThreadID, repositoryDirectory, reviewDirectory,
	)
	pointerPath := filepath.Join(reviewRoot, reviewLatestFileName)
	resultPath := filepath.Join(reviewRoot, result.ReviewID+".json")
	pointerData, err := os.ReadFile(pointerPath)
	if err != nil {
		t.Fatal(err)
	}
	resultData, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name string
		path string
		data []byte
	}{
		{name: "latest pointer", path: pointerPath, data: pointerData},
		{name: "immutable result", path: resultPath, data: resultData},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.Remove(test.path); err != nil {
				t.Fatal(err)
			}
			if err := unix.Mkfifo(test.path, 0o600); err != nil {
				t.Fatal(err)
			}
			assertLatestReviewFIFORejectedPromptly(t, store, lease, metadata, test.path)
			if err := os.Remove(test.path); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(test.path, test.data, 0o600); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func assertLatestReviewFIFORejectedPromptly(
	t *testing.T,
	store *Store,
	lease *Lease,
	metadata Metadata,
	fifoPath string,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, _, err := store.LoadLatestReviewResultWithLease(ctx, lease, metadata)
		result <- err
	}()
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "unsafe") {
			t.Fatalf("FIFO review restore error = %v", err)
		}
	case <-time.After(time.Second):
		writer, _ := os.OpenFile(fifoPath, os.O_WRONLY|unix.O_NONBLOCK, 0)
		if writer != nil {
			_ = writer.Close()
		}
		<-result
		t.Fatal("FIFO blocked latest review restore")
	}
}
