package thread

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestTrashThreadMovesOnlyRecognizedExternalState(t *testing.T) {
	root := t.TempDir()
	projectRoot := filepath.Join(root, "project")
	if err := os.Mkdir(projectRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	projectSentinel := filepath.Join(projectRoot, "keep.go")
	if err := os.WriteFile(projectSentinel, []byte("package keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	project, err := ResolveProject(t.Context(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(filepath.Join(root, "external", "coding"))
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := NewMetadata(NewThreadID(), project, "trash safely", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(metadata); err != nil {
		t.Fatal(err)
	}
	threadRoot, err := store.ThreadRoot(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	sessions := filepath.Join(threadRoot, "sessions")
	if err := os.Mkdir(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessions, "history.jsonl"), []byte("history\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := store.PlanDelete(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	wantPaths := []string{
		filepath.Join(threadRoot, "sessions"),
		filepath.Join(threadRoot, leaseFileName),
		filepath.Join(threadRoot, metadataFileName),
	}
	if !reflect.DeepEqual(plan.OwnedPaths, wantPaths) || plan.ThreadRoot != threadRoot ||
		plan.ProjectRoot != project.ProjectRoot {
		t.Fatalf("delete plan = %+v, want paths %v", plan, wantPaths)
	}
	if _, err := store.TrashThread(lease, "wrong", time.Now()); err == nil {
		t.Fatal("TrashThread() accepted the wrong confirmation")
	}
	trashedAt := time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC)
	result, err := store.TrashThread(lease, metadata.ThreadID, trashedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if result.ThreadID != metadata.ThreadID || result.TrashID == "" || result.Path == "" ||
		!result.At.Equal(trashedAt) {
		t.Fatalf("trash result = %+v", result)
	}
	if _, err := os.Stat(threadRoot); !os.IsNotExist(err) {
		t.Fatalf("active thread root still exists: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(result.Path, "sessions", "history.jsonl")); err != nil ||
		string(data) != "history\n" {
		t.Fatalf("recoverable history = %q, %v", data, err)
	}
	if _, err := os.Stat(projectSentinel); err != nil {
		t.Fatalf("project sentinel was touched: %v", err)
	}
	catalog, err := NewCatalog(store, CatalogOptions{})
	if err != nil {
		t.Fatal(err)
	}
	page, err := catalog.Query(t.Context(), CatalogQuery{ProjectKey: project.ProjectKey})
	if err != nil || len(page.Threads) != 0 {
		t.Fatalf("post-trash catalog = %+v, %v", page, err)
	}
	if _, err := store.AcquireLease(metadata.ThreadID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("post-trash AcquireLease() error = %v", err)
	}
}

func TestDeletePlanFailsClosedForUnknownOrLinkedState(t *testing.T) {
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
	unknown := filepath.Join(threadRoot, "project-copy")
	if err := os.WriteFile(unknown, []byte("unknown"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PlanDelete(
		metadata.ThreadID,
	); err == nil ||
		!strings.Contains(err.Error(), "cannot confirm ownership") {
		t.Fatalf("unknown entry error = %v", err)
	}
	if err := os.Remove(unknown); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(threadRoot, "media")
	if err := os.Symlink(project.ProjectRoot, linked); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := store.PlanDelete(metadata.ThreadID); err == nil || !strings.Contains(err.Error(), "symbolic-link") {
		t.Fatalf("linked entry error = %v", err)
	}
}

func TestDeletePlanRejectsSymlinkedThreadRoot(t *testing.T) {
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
	moved := threadRoot + "-moved"
	if err := os.Rename(threadRoot, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, threadRoot); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := store.PlanDelete(metadata.ThreadID); err == nil ||
		!strings.Contains(err.Error(), "direct thread root") {
		t.Fatalf("symlinked root error = %v", err)
	}
}

func TestDeletePlanRejectsProjectOrGitDirectoryNestedUnderThread(t *testing.T) {
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

	for _, test := range []struct {
		name   string
		mutate func(*Metadata, string)
		want   string
	}{
		{
			name: "project root",
			mutate: func(metadata *Metadata, nested string) {
				metadata.Project = ProjectIdentity{
					Kind: ProjectKindDirectory, ProjectRoot: nested, InvocationCWD: nested,
				}
				metadata.Project.ProjectKey = projectKey(ProjectKindDirectory, nested)
			},
			want: "project root",
		},
		{
			name: "Git directory",
			mutate: func(metadata *Metadata, nested string) {
				metadata.Project.Kind = ProjectKindGitWorktree
				metadata.Project.GitWorktreeRoot = metadata.Project.ProjectRoot
				metadata.Project.GitDir = nested
				metadata.Project.GitCommonDir = metadata.Project.ProjectRoot
				metadata.Project.ProjectKey = projectKey(ProjectKindGitWorktree, metadata.Project.ProjectRoot)
			},
			want: "Git directory",
		},
		{
			name: "git common directory",
			mutate: func(metadata *Metadata, nested string) {
				metadata.Project.Kind = ProjectKindGitWorktree
				metadata.Project.GitWorktreeRoot = metadata.Project.ProjectRoot
				metadata.Project.GitDir = metadata.Project.ProjectRoot
				metadata.Project.GitCommonDir = nested
				metadata.Project.ProjectKey = projectKey(ProjectKindGitWorktree, metadata.Project.ProjectRoot)
			},
			want: "Git common directory",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			metadata, metadataErr := NewMetadata(NewThreadID(), project, "safe", time.Now())
			if metadataErr != nil {
				t.Fatal(metadataErr)
			}
			if err := store.ProvisionThread(metadata.ThreadID); err != nil {
				t.Fatal(err)
			}
			threadRoot, _ := store.ThreadRoot(metadata.ThreadID)
			nested := filepath.Join(threadRoot, "sessions", "nested-project")
			if err := os.MkdirAll(nested, 0o700); err != nil {
				t.Fatal(err)
			}
			test.mutate(&metadata, nested)
			if err := store.Save(metadata); err != nil {
				t.Fatal(err)
			}
			if _, err := store.PlanDelete(metadata.ThreadID); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("PlanDelete() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestTrashThreadRejectsSymlinkedTrashHierarchy(t *testing.T) {
	root := t.TempDir()
	projectRoot := filepath.Join(root, "project")
	outside := filepath.Join(projectRoot, "injected-trash")
	if err := os.MkdirAll(outside, 0o755); err != nil {
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
	if err := os.Symlink(outside, filepath.Join(store.Root(), "trash")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Release() }()
	if result, err := store.TrashThread(lease, metadata.ThreadID, time.Now()); err == nil || result.Path != "" ||
		!strings.Contains(err.Error(), "not a direct directory") {
		t.Fatalf("TrashThread() result = %+v, error = %v", result, err)
	}
	if _, err := store.Load(metadata.ThreadID); err != nil {
		t.Fatalf("active thread changed: %v", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("outside trash changed: entries=%v err=%v", entries, err)
	}
}

func TestDeletePlanRejectsHardLinkedMetadata(t *testing.T) {
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
	if err := os.Link(
		filepath.Join(threadRoot, metadataFileName),
		filepath.Join(root, "metadata-hardlink"),
	); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if _, err := store.PlanDelete(metadata.ThreadID); err == nil || !strings.Contains(err.Error(), "singly linked") {
		t.Fatalf("PlanDelete() error = %v", err)
	}
}
