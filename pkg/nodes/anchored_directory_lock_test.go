package nodes

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAnchoredDirectoryConcurrentLockCreation(t *testing.T) {
	directoryPath := t.TempDir()
	const workers = 16

	results := make(chan error, workers)
	var active atomic.Int32
	var maximum atomic.Int32
	var start sync.WaitGroup
	start.Add(1)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			start.Wait()
			directory, err := openAnchoredDirectory(directoryPath)
			if err != nil {
				results <- err
				return
			}
			release, err := directory.acquireLock("state.db.init.lock")
			if err == nil {
				current := active.Add(1)
				for observed := maximum.Load(); current > observed; observed = maximum.Load() {
					if maximum.CompareAndSwap(observed, current) {
						break
					}
				}
				time.Sleep(time.Millisecond)
				active.Add(-1)
				release()
			}
			closeErr := directory.close()
			if err != nil {
				results <- err
				return
			}
			results <- closeErr
		}()
	}
	start.Done()
	group.Wait()
	close(results)

	for err := range results {
		if err != nil {
			t.Errorf("acquire concurrent anchored lock: %v", err)
		}
	}
	if maximum.Load() != 1 {
		t.Fatalf("maximum concurrent anchored lock holders = %d, want 1", maximum.Load())
	}
	key, err := filepath.Abs(filepath.Join(directoryPath, "state.db.init.lock"))
	if err != nil {
		t.Fatal(err)
	}
	anchoredProcessLocks.Lock()
	_, retained := anchoredProcessLocks.entries[filepath.Clean(key)]
	anchoredProcessLocks.Unlock()
	if retained {
		t.Fatal("released anchored process lock remains registered")
	}
}
