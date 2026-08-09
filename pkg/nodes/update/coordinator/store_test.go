//go:build linux || darwin

package coordinator

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestStoreInitializesCommitsAndRejectsStaleGeneration(t *testing.T) {
	root := privateRoot(t)
	store, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	state := testState(t)
	if err = store.Initialize(state); err != nil {
		t.Fatal(err)
	}
	if err = store.Initialize(state); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second Initialize() error = %v", err)
	}
	loaded, err := store.Load()
	if err != nil || loaded != state {
		t.Fatalf("Load() = %#v, %v", loaded, err)
	}
	next := loaded
	next.Generation++
	if err = store.Commit(loaded.Generation, next); err != nil {
		t.Fatal(err)
	}
	if err = store.Commit(loaded.Generation, next); err == nil || !strings.Contains(err.Error(), "generation") {
		t.Fatalf("stale Commit() error = %v", err)
	}
}

func TestStoreInitialPublicationIsRecoverableAfterDurabilityUncertainty(t *testing.T) {
	root := privateRoot(t)
	store, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	state := testState(t)
	store.fault = func(point string) error {
		if point == "state_after_publish" {
			return unix.EIO
		}
		return nil
	}
	if err = store.Initialize(state); !errors.Is(err, unix.EIO) {
		t.Fatalf("Initialize() error = %v", err)
	}
	store.fault = nil
	loaded, err := store.Load()
	if err != nil || loaded != state {
		t.Fatalf("Load() after uncertain publication = %#v, %v", loaded, err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	loaded, err = reopened.Load()
	if err != nil || loaded != state {
		t.Fatalf("reopened state = %#v, %v", loaded, err)
	}
}

func TestStorePowerLossBoundariesRecoverOldOrPublishedGeneration(t *testing.T) {
	root := privateRoot(t)
	store, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	state := testState(t)
	if err = store.Initialize(state); err != nil {
		t.Fatal(err)
	}
	next := state
	next.Generation++
	store.fault = func(point string) error {
		if point == "state_before_publish" {
			return unix.ENOSPC
		}
		return nil
	}
	if err = store.Commit(state.Generation, next); !errors.Is(err, unix.ENOSPC) {
		t.Fatalf("pre-publication Commit() error = %v", err)
	}
	loaded, err := store.Load()
	if err != nil || loaded.Generation != state.Generation {
		t.Fatalf("pre-publication state = %#v, %v", loaded, err)
	}
	store.fault = func(point string) error {
		if point == "state_after_publish" {
			return unix.EIO
		}
		return nil
	}
	if err = store.Commit(state.Generation, next); !errors.Is(err, unix.EIO) {
		t.Fatalf("post-publication Commit() error = %v", err)
	}
	store.fault = nil
	loaded, err = store.Load()
	if err != nil || loaded.Generation != next.Generation {
		t.Fatalf("post-publication state = %#v, %v", loaded, err)
	}
}

func TestStoreReconcilesOnlyExactPrivateTemporaryFiles(t *testing.T) {
	root := privateRoot(t)
	for name, mode := range map[string]os.FileMode{
		".state-0123456789abcdef0123456789abcdef.tmp":     0o600,
		".archive-1123456789abcdef0123456789abcdef.tmp":   0o600,
		".candidate-2123456789abcdef0123456789abcdef.tmp": 0o500,
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("interrupted"), mode); err != nil {
			t.Fatal(err)
		}
	}
	store, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != lockFileName {
		t.Fatalf("entries after reconciliation = %v", entries)
	}
}

func TestStoreFailsClosedForUnexpectedOrUnsafeTemporaryEntries(t *testing.T) {
	for name, configure := range map[string]func(string) error{
		"unexpected": func(root string) error {
			return os.WriteFile(filepath.Join(root, "operator-note"), []byte("keep"), 0o600)
		},
		"malformed name": func(root string) error {
			return os.WriteFile(filepath.Join(root, ".archive-not-random.tmp"), []byte("x"), 0o600)
		},
		"unsafe mode": func(root string) error {
			return os.WriteFile(
				filepath.Join(root, ".archive-0123456789abcdef0123456789abcdef.tmp"), []byte("x"), 0o644,
			)
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := privateRoot(t)
			if err := configure(root); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenStore(root); err == nil {
				t.Fatal("OpenStore() unexpectedly accepted an unsafe entry")
			}
		})
	}
}

func TestStoreRejectsConcurrentOwnerAndUnsafeFiles(t *testing.T) {
	root := privateRoot(t)
	store, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if _, err = OpenStore(root); err == nil || !strings.Contains(err.Error(), "already owned") {
		t.Fatalf("second OpenStore() error = %v", err)
	}
	state := testState(t)
	if err = store.Initialize(state); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(filepath.Join(root, stateFileName), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Load(); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestStoreRejectsMalformedDuplicateAndTrailingState(t *testing.T) {
	for name, data := range map[string]string{
		"duplicate": `{"schema_version":1,"schema_version":1}`,
		"trailing":  `{} {}`,
		"oversized": strings.Repeat("x", MaxStateBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			root := privateRoot(t)
			if err := os.WriteFile(filepath.Join(root, stateFileName), []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			store, err := OpenStore(root)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			if _, err = store.Load(); err == nil {
				t.Fatal("malformed state was accepted")
			}
		})
	}
}

func TestStoreRejectsHardlinkedState(t *testing.T) {
	root := privateRoot(t)
	store, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if err = store.Initialize(testState(t)); err != nil {
		t.Fatal(err)
	}
	if err = os.Link(filepath.Join(root, stateFileName), filepath.Join(root, "state-copy")); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Load(); err == nil || !strings.Contains(err.Error(), "regular") {
		t.Fatalf("Load() error = %v", err)
	}
}

func privateRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "coordinator")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}
