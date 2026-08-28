package thread

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

const maxDeletePlanEntries = 32

// CommittedTrashError reports that the thread move committed, but directory
// durability could not be confirmed. Result always names the recovery path.
type CommittedTrashError struct {
	Result TrashResult
	Err    error
}

func (e *CommittedTrashError) Error() string {
	return fmt.Sprintf("coding thread delete committed but durability was not confirmed: %v", e.Err)
}

func (e *CommittedTrashError) Unwrap() error { return e.Err }

// IsCommittedTrashError distinguishes a completed rename from preparation
// errors which may also report committed directory creation.
func IsCommittedTrashError(err error) bool {
	var committed *CommittedTrashError
	return errors.As(err, &committed)
}

// DeletePlan is the bounded confirmation contract for one recoverable thread
// deletion. Every listed path is below the external MintClaw coding root.
type DeletePlan struct {
	ThreadID    string   `json:"thread_id"`
	Title       string   `json:"title"`
	ThreadRoot  string   `json:"thread_root"`
	OwnedPaths  []string `json:"owned_paths"`
	ProjectKey  string   `json:"project_key"`
	ProjectRoot string   `json:"project_root"`
}

// TrashResult identifies the recoverable destination after one atomic move.
type TrashResult struct {
	ThreadID string    `json:"thread_id"`
	TrashID  string    `json:"trash_id"`
	Path     string    `json:"path"`
	At       time.Time `json:"at"`
}

// PlanDelete enumerates only recognized MintClaw-owned top-level artifacts.
// Unknown entries fail closed instead of being treated as disposable.
func (s *Store) PlanDelete(threadID string) (DeletePlan, error) {
	return s.PlanDeleteContext(context.Background(), threadID)
}

// PlanDeleteContext builds a recoverable deletion plan while allowing legacy
// Git descriptors to resolve their worktree-private Git directory safely.
func (s *Store) PlanDeleteContext(ctx context.Context, threadID string) (DeletePlan, error) {
	if s == nil {
		return DeletePlan{}, fmt.Errorf("coding thread store is nil")
	}
	threadRoot, err := s.ThreadRoot(threadID)
	if err != nil {
		return DeletePlan{}, err
	}
	storeRoot, err := openCatalogRoot(s.root)
	if err != nil {
		return DeletePlan{}, fmt.Errorf("coding thread delete: open store root: %w", err)
	}
	defer func() { _ = storeRoot.Close() }()
	threadsRoot, err := openCatalogChildDirectory(storeRoot, "threads")
	if err != nil {
		return DeletePlan{}, fmt.Errorf("coding thread delete: open threads root: %w", err)
	}
	defer func() { _ = threadsRoot.Close() }()
	directThreadRoot, err := openCatalogChildDirectory(threadsRoot, threadID)
	if err != nil {
		return DeletePlan{}, fmt.Errorf("coding thread delete: open direct thread root: %w", err)
	}
	defer func() { _ = directThreadRoot.Close() }()
	metadata, err := loadCatalogMetadataFromDirectory(
		directThreadRoot,
		threadID,
		openCatalogMetadataFile,
	)
	if err != nil {
		return DeletePlan{}, err
	}
	project, err := resolveDeleteProjectBoundaries(ctx, metadata.Project)
	if err != nil {
		return DeletePlan{}, err
	}
	if boundaryErr := validateDeleteProjectBoundaries(threadRoot, project); boundaryErr != nil {
		return DeletePlan{}, boundaryErr
	}
	entries, err := directThreadRoot.readDir(maxDeletePlanEntries + 1)
	if err != nil {
		return DeletePlan{}, fmt.Errorf("coding thread delete: inspect owned state: %w", err)
	}
	if len(entries) > maxDeletePlanEntries {
		return DeletePlan{}, fmt.Errorf("coding thread delete: owned state has too many top-level entries")
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !ownedThreadEntry(entry.Name()) {
			return DeletePlan{}, fmt.Errorf(
				"coding thread delete: cannot confirm ownership of top-level entry %q",
				entry.Name(),
			)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return DeletePlan{}, fmt.Errorf("coding thread delete: symbolic-link entry %q is not allowed", entry.Name())
		}
		paths = append(paths, filepath.Join(threadRoot, entry.Name()))
	}
	sort.Strings(paths)
	return DeletePlan{
		ThreadID: threadID, Title: metadata.Title, ThreadRoot: threadRoot,
		OwnedPaths: paths, ProjectKey: metadata.Project.ProjectKey, ProjectRoot: metadata.Project.ProjectRoot,
	}, nil
}

func resolveDeleteProjectBoundaries(ctx context.Context, project ProjectIdentity) (ProjectIdentity, error) {
	if project.Kind != ProjectKindGitWorktree || project.GitDir != "" {
		return project, nil
	}
	current, err := ResolveProject(ctx, project.ProjectRoot)
	if err != nil {
		return ProjectIdentity{}, fmt.Errorf("coding thread delete: resolve legacy Git directory: %w", err)
	}
	if current.ProjectKey != project.ProjectKey || current.GitCommonDir != project.GitCommonDir {
		return ProjectIdentity{}, fmt.Errorf("coding thread delete: legacy Git identity no longer matches the project")
	}
	project.GitDir = current.GitDir
	return project, nil
}

func validateDeleteProjectBoundaries(threadRoot string, project ProjectIdentity) error {
	paths := []struct {
		name string
		path string
	}{
		{name: "project root", path: project.ProjectRoot},
		{name: "Git directory", path: project.GitDir},
		{name: "Git common directory", path: project.GitCommonDir},
	}
	for _, candidate := range paths {
		if candidate.path == "" {
			continue
		}
		overlaps, err := pathsOverlap(threadRoot, candidate.path)
		if err != nil {
			return fmt.Errorf("coding thread delete: validate %s boundary: %w", candidate.name, err)
		}
		if overlaps {
			return fmt.Errorf("coding thread delete: thread state overlaps the %s", candidate.name)
		}
	}
	return nil
}

func ownedThreadEntry(name string) bool {
	switch name {
	case metadataFileName, leaseFileName, "sessions", "context", "memory", "runtime", "diagnostics", "media":
		return true
	default:
		return false
	}
}

// TrashThread atomically removes a thread from the active catalog by moving
// its complete external state root into MintClaw's same-filesystem trash.
func (s *Store) TrashThread(
	ctx context.Context,
	lease *Lease,
	confirmation string,
	now time.Time,
) (TrashResult, error) {
	if s == nil {
		return TrashResult{}, fmt.Errorf("coding thread store is nil")
	}
	threadID := lease.ThreadID()
	if confirmation != threadID {
		return TrashResult{}, fmt.Errorf("coding thread delete: confirmation must exactly match thread ID")
	}
	if now.IsZero() {
		return TrashResult{}, fmt.Errorf("coding thread delete: timestamp is required")
	}
	var result TrashResult
	err := lease.withActive(s.root, threadID, func() error {
		if _, planErr := s.PlanDeleteContext(ctx, threadID); planErr != nil {
			return planErr
		}
		root, rootErr := os.OpenRoot(s.root)
		if rootErr != nil {
			return fmt.Errorf("coding thread delete: anchor store root: %w", rootErr)
		}
		defer func() { _ = root.Close() }()
		if err := ensureDirectTrashDirectory(root, "trash"); err != nil {
			return err
		}
		if err := ensureDirectTrashDirectory(root, filepath.Join("trash", "threads")); err != nil {
			return err
		}
		trashRoot := filepath.Join(s.root, "trash", "threads")
		if err := errors.Join(
			fileutil.SyncDirectory(s.root),
			fileutil.SyncDirectory(filepath.Join(s.root, "trash")),
		); err != nil {
			return fmt.Errorf("coding thread delete: sync trash hierarchy before move: %w", err)
		}
		at := now.UTC()
		trashID := fmt.Sprintf("%s-%s-%s", threadID, at.Format("20060102T150405.000000000Z"), uuid.NewString())
		destination := filepath.Join(trashRoot, trashID)
		if err := root.Rename(
			filepath.Join("threads", threadID),
			filepath.Join("trash", "threads", trashID),
		); err != nil {
			return fmt.Errorf("coding thread delete: move to trash: %w", err)
		}
		result = TrashResult{ThreadID: threadID, TrashID: trashID, Path: destination, At: at}
		if err := errors.Join(
			fileutil.SyncDirectory(filepath.Join(s.root, "threads")),
			fileutil.SyncDirectory(trashRoot),
		); err != nil {
			return &CommittedTrashError{Result: result, Err: fmt.Errorf("sync trash move: %w", err)}
		}
		return nil
	})
	return result, err
}

func ensureDirectTrashDirectory(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if os.IsNotExist(err) {
		if mkdirErr := root.Mkdir(name, 0o700); mkdirErr != nil {
			return fmt.Errorf("coding thread delete: create direct trash directory %q: %w", name, mkdirErr)
		}
		info, err = root.Lstat(name)
	}
	if err != nil {
		return fmt.Errorf("coding thread delete: inspect direct trash directory %q: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("coding thread delete: trash component %q is not a direct directory", name)
	}
	if err := root.Chmod(name, 0o700); err != nil {
		return fmt.Errorf("coding thread delete: secure trash component %q: %w", name, err)
	}
	return nil
}

func pathWithin(candidate, root string) (bool, error) {
	candidate = filepath.Clean(candidate)
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" {
		return false, fmt.Errorf("root is required")
	}
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false, err
	}
	return relative == "." || filepath.IsLocal(relative), nil
}

func pathsOverlap(left, right string) (bool, error) {
	leftInsideRight, err := pathWithin(left, right)
	if err != nil {
		return false, err
	}
	rightInsideLeft, err := pathWithin(right, left)
	if err != nil {
		return false, err
	}
	return leftInsideRight || rightInsideLeft, nil
}
