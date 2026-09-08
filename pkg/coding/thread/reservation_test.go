package thread

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

func TestReserveThreadIsExclusiveAndDoesNotPublishMetadata(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "coding"))
	if err != nil {
		t.Fatal(err)
	}
	threadID := uuid.NewString()
	if err := store.ReserveThread(threadID); err != nil {
		t.Fatal(err)
	}
	root, err := store.ThreadRoot(threadID)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("reserved thread mode = %v", info.Mode())
	}
	if _, err := store.Load(threadID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reserved thread published metadata: %v", err)
	}
	marker := filepath.Join(root, "partial-state")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.ReserveThread(threadID); !errors.Is(err, ErrThreadExists) {
		t.Fatalf("second ReserveThread() error = %v, want %v", err, ErrThreadExists)
	}
	if content, err := os.ReadFile(marker); err != nil || string(content) != "keep" {
		t.Fatalf("second reservation changed partial state: %q, %v", content, err)
	}
}

func TestReserveThreadHasOneConcurrentWinner(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "coding"))
	if err != nil {
		t.Fatal(err)
	}
	threadID := uuid.NewString()
	start := make(chan struct{})
	var winners atomic.Int32
	var unexpected atomic.Int32
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			err := store.ReserveThread(threadID)
			switch {
			case err == nil:
				winners.Add(1)
			case errors.Is(err, ErrThreadExists):
			default:
				unexpected.Add(1)
			}
		}()
	}
	close(start)
	wait.Wait()
	if winners.Load() != 1 || unexpected.Load() != 0 {
		t.Fatalf("reservation outcomes = %d winners, %d unexpected", winners.Load(), unexpected.Load())
	}
}
