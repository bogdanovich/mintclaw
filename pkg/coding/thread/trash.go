package thread

import (
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

// DeletePlan is the bounded confirmation contract for one recoverable thread
// deletion. Every listed path is below the external MintClaw coding root.
type DeletePlan struct {
	ThreadID    string   `json:"thread_id"`
	Title       string   `json:"title"`
	ThreadRoot  string   `json:"thread_root"`
	OwnedPaths  []string `json:"owned_paths"`
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
	metadata, err := s.Load(threadID)
	if err != nil {
		return DeletePlan{}, err
	}
	threadRoot, err := s.ThreadRoot(threadID)
	if err != nil {
		return DeletePlan{}, err
	}
	rootInfo, err := os.Lstat(threadRoot)
	if err != nil {
		return DeletePlan{}, fmt.Errorf("coding thread delete: inspect thread root: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return DeletePlan{}, fmt.Errorf("coding thread delete: thread root is not a direct directory")
	}
	if inside, checkErr := pathWithin(threadRoot, metadata.Project.ProjectRoot); checkErr != nil {
		return DeletePlan{}, fmt.Errorf("coding thread delete: validate project boundary: %w", checkErr)
	} else if inside {
		return DeletePlan{}, fmt.Errorf("coding thread delete: thread state overlaps the project root")
	}
	entries, err := os.ReadDir(threadRoot)
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
		OwnedPaths: paths, ProjectRoot: metadata.Project.ProjectRoot,
	}, nil
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
func (s *Store) TrashThread(lease *Lease, confirmation string, now time.Time) (TrashResult, error) {
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
		if _, planErr := s.PlanDelete(threadID); planErr != nil {
			return planErr
		}
		trashRoot := filepath.Join(s.root, "trash", "threads")
		relative, relErr := filepath.Rel(s.durableRoot, trashRoot)
		if relErr != nil || !filepath.IsLocal(relative) {
			return fmt.Errorf("coding thread delete: trash root escapes store")
		}
		if err := s.mkdirDurable(s.durableRoot, relative, 0o700); err != nil {
			return fmt.Errorf("coding thread delete: create trash: %w", err)
		}
		at := now.UTC()
		trashID := fmt.Sprintf("%s-%s-%s", threadID, at.Format("20060102T150405.000000000Z"), uuid.NewString())
		destination := filepath.Join(trashRoot, trashID)
		source, sourceErr := s.ThreadRoot(threadID)
		if sourceErr != nil {
			return sourceErr
		}
		if err := os.Rename(source, destination); err != nil {
			return fmt.Errorf("coding thread delete: move to trash: %w", err)
		}
		result = TrashResult{ThreadID: threadID, TrashID: trashID, Path: destination, At: at}
		if err := errors.Join(
			fileutil.SyncDirectory(filepath.Dir(source)),
			fileutil.SyncDirectory(trashRoot),
		); err != nil {
			return &fileutil.CommittedWriteError{Err: fmt.Errorf("coding thread delete: sync trash move: %w", err)}
		}
		return nil
	})
	return result, err
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
