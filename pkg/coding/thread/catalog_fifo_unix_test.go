//go:build unix

package thread

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCatalogSkipsMetadataFIFOWithoutBlockingHealthyThreads(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	project := catalogFixtureProject(t, filepath.Join(root, "project"))
	healthy := catalogFixtureMetadata(t, project, "healthy", time.Now())
	if err := store.Save(healthy); err != nil {
		t.Fatalf("Save(healthy) error = %v", err)
	}

	corruptID := uuid.NewString()
	corruptRoot, err := store.ThreadRoot(corruptID)
	if err != nil {
		t.Fatalf("ThreadRoot(corrupt) error = %v", err)
	}
	if err := os.MkdirAll(corruptRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll(corrupt) error = %v", err)
	}
	if err := syscall.Mkfifo(filepath.Join(corruptRoot, metadataFileName), 0o600); err != nil {
		t.Fatalf("Mkfifo(metadata) error = %v", err)
	}

	catalog, err := NewCatalog(store, CatalogOptions{})
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	type queryResult struct {
		page CatalogPage
		err  error
	}
	result := make(chan queryResult, 1)
	go func() {
		page, queryErr := catalog.Query(t.Context(), CatalogQuery{All: true})
		result <- queryResult{page: page, err: queryErr}
	}()

	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("Query() error = %v", got.err)
		}
		if len(got.page.Threads) != 1 || got.page.Threads[0].ThreadID != healthy.ThreadID ||
			got.page.SkippedTotal != 1 {
			t.Fatalf("FIFO isolation page = %#v", got.page)
		}
	case <-time.After(time.Second):
		t.Fatal("Query() blocked while opening a metadata FIFO")
	}
}

func TestCatalogRejectsFIFOReplacedAfterDescriptorValidation(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	project := catalogFixtureProject(t, filepath.Join(root, "project"))
	metadata := catalogFixtureMetadata(t, project, "replace-after-lstat", time.Now())
	if err := store.Save(metadata); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	catalog, err := NewCatalog(store, CatalogOptions{})
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	threadsRoot, err := catalog.openThreadsRoot()
	if err != nil {
		t.Fatalf("openThreadsRoot() error = %v", err)
	}
	t.Cleanup(func() { _ = threadsRoot.Close() })
	threadRoot, err := store.ThreadRoot(metadata.ThreadID)
	if err != nil {
		t.Fatalf("ThreadRoot() error = %v", err)
	}
	metadataPath := filepath.Join(threadRoot, metadataFileName)

	result := make(chan error, 1)
	go func() {
		_, loadErr := loadCatalogMetadataWithOpener(
			threadsRoot,
			metadata.ThreadID,
			func(root *catalogDirectory) (*os.File, error) {
				if removeErr := os.Remove(metadataPath); removeErr != nil {
					return nil, removeErr
				}
				if fifoErr := syscall.Mkfifo(metadataPath, 0o600); fifoErr != nil {
					return nil, fifoErr
				}
				return openCatalogMetadataFile(root)
			},
		)
		result <- loadErr
	}()

	select {
	case loadErr := <-result:
		if loadErr == nil {
			t.Fatal("loadCatalogMetadataWithOpener() accepted the replacement FIFO")
		}
	case <-time.After(time.Second):
		t.Fatal("descriptor open blocked after replacement with a FIFO")
	}
}

func TestCatalogRejectsThreadsRootReplacedWithFIFOBeforeAtomicOpen(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	threadsPath := filepath.Join(store.Root(), "threads")
	if err := os.MkdirAll(threadsPath, 0o700); err != nil {
		t.Fatalf("MkdirAll(threads) error = %v", err)
	}
	catalog, err := NewCatalog(store, CatalogOptions{})
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}

	result := make(chan error, 1)
	go func() {
		opened, openErr := catalog.openThreadsRootWithOpener(
			func(parent *catalogDirectory, name string) (*catalogDirectory, error) {
				if removeErr := os.Remove(threadsPath); removeErr != nil {
					return nil, removeErr
				}
				if fifoErr := syscall.Mkfifo(threadsPath, 0o600); fifoErr != nil {
					return nil, fifoErr
				}
				return openCatalogChildDirectory(parent, name)
			},
		)
		if opened != nil {
			_ = opened.Close()
		}
		result <- openErr
	}()
	assertCatalogOpenCompletesWithError(t, result, "threads root")
}

func TestCatalogRejectsThreadDirectoryReplacedWithFIFOBeforeAtomicOpen(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	threadID := uuid.NewString()
	threadPath, err := store.ThreadRoot(threadID)
	if err != nil {
		t.Fatalf("ThreadRoot() error = %v", err)
	}
	if err := os.MkdirAll(threadPath, 0o700); err != nil {
		t.Fatalf("MkdirAll(thread) error = %v", err)
	}
	catalog, err := NewCatalog(store, CatalogOptions{})
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	threadsRoot, err := catalog.openThreadsRoot()
	if err != nil {
		t.Fatalf("openThreadsRoot() error = %v", err)
	}
	t.Cleanup(func() { _ = threadsRoot.Close() })

	result := make(chan error, 1)
	go func() {
		_, loadErr := loadCatalogMetadataWithDirectoryOpener(
			threadsRoot,
			threadID,
			func(parent *catalogDirectory, name string) (*catalogDirectory, error) {
				if removeErr := os.Remove(threadPath); removeErr != nil {
					return nil, removeErr
				}
				if fifoErr := syscall.Mkfifo(threadPath, 0o600); fifoErr != nil {
					return nil, fifoErr
				}
				return openCatalogChildDirectory(parent, name)
			},
		)
		result <- loadErr
	}()
	assertCatalogOpenCompletesWithError(t, result, "thread directory")
}

func assertCatalogOpenCompletesWithError(t *testing.T, result <-chan error, component string) {
	t.Helper()
	select {
	case openErr := <-result:
		if openErr == nil {
			t.Fatalf("catalog accepted the replacement FIFO for %s", component)
		}
	case <-time.After(time.Second):
		t.Fatalf("catalog blocked opening replacement FIFO for %s", component)
	}
}
