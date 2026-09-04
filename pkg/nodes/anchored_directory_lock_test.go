package nodes

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAnchoredDirectoryConcurrentLockCreation(t *testing.T) {
	directoryPath := t.TempDir()
	const workers = 16
	keyDirectory, err := openAnchoredDirectory(directoryPath)
	if err != nil {
		t.Fatal(err)
	}
	key := keyDirectory.processLockKey("state.db.init.lock")
	if err = keyDirectory.close(); err != nil {
		t.Fatal(err)
	}

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
	anchoredProcessLocks.Lock()
	_, retained := anchoredProcessLocks.entries[key]
	anchoredProcessLocks.Unlock()
	if retained {
		t.Fatal("released anchored process lock remains registered")
	}
}

func TestAnchoredDirectoryAliasPathsShareProcessLockKey(t *testing.T) {
	root := t.TempDir()
	realParent := filepath.Join(root, "real")
	directoryPath := filepath.Join(realParent, "state")
	if err := os.MkdirAll(directoryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	aliasParent := filepath.Join(root, "alias")
	if err := os.Symlink(realParent, aliasParent); err != nil {
		t.Skipf("create directory alias: %v", err)
	}

	direct, err := openAnchoredDirectory(directoryPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = direct.close() }()
	aliased, err := openAnchoredDirectory(filepath.Join(aliasParent, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = aliased.close() }()

	directKey := direct.processLockKey("state.db.init.lock")
	aliasedKey := aliased.processLockKey("state.db.init.lock")
	if directKey != aliasedKey {
		t.Fatalf("alias process lock key = %#v, want %#v", aliasedKey, directKey)
	}
}
