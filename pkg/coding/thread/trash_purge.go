package thread

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/google/uuid"
)

const DefaultThreadTrashScanLimit = 10_000

// ThreadTrashPurgeOptions bounds one recoverable-trash scan. Delete=false is
// a side-effect-free plan; Delete=true performs the same validation again
// under the attachment maintenance lease before removing any candidates.
type ThreadTrashPurgeOptions struct {
	Before     time.Time
	EntryLimit int
	Delete     bool
}

// ThreadTrashCandidate contains only opaque identity and retention facts.
// It deliberately excludes project paths, titles, prompts, and content.
type ThreadTrashCandidate struct {
	TrashID   string    `json:"trash_id"`
	ThreadID  string    `json:"thread_id"`
	TrashedAt time.Time `json:"trashed_at"`
	identity  os.FileInfo
}

// ThreadTrashPurgeResult is the bounded, machine-readable outcome of one
// trash-retention pass.
type ThreadTrashPurgeResult struct {
	Before            time.Time              `json:"before"`
	Scanned           int                    `json:"scanned"`
	Candidates        []ThreadTrashCandidate `json:"candidates"`
	Deleted           int                    `json:"deleted"`
	DeletedTrashIDs   []string               `json:"deleted_trash_ids,omitempty"`
	AttemptedTrashIDs []string               `json:"attempted_trash_ids,omitempty"`
	IncompleteTrashID string                 `json:"incomplete_trash_id,omitempty"`
}

// CommittedThreadTrashPurgeError reports that recursive deletion started.
// A failed removal may already have deleted some files; IncompleteTrashID
// identifies that root even when Deleted is zero.
type CommittedThreadTrashPurgeError struct {
	Result ThreadTrashPurgeResult
	Err    error
}

func (e *CommittedThreadTrashPurgeError) Error() string {
	return fmt.Sprintf("coding thread trash purge committed partially: %v", e.Err)
}

func (e *CommittedThreadTrashPurgeError) Unwrap() error { return e.Err }

// IsCommittedThreadTrashPurgeError distinguishes partial committed deletion
// from a fail-closed planning error which removed nothing.
func IsCommittedThreadTrashPurgeError(err error) bool {
	var committed *CommittedThreadTrashPurgeError
	return errors.As(err, &committed)
}

// PurgeThreadTrash plans or permanently removes expired recoverable coding
// thread roots. It never enters project roots, Git directories, or worktrees.
func (s *Store) PurgeThreadTrash(
	ctx context.Context,
	options ThreadTrashPurgeOptions,
) (result ThreadTrashPurgeResult, resultErr error) {
	if s == nil {
		return ThreadTrashPurgeResult{}, fmt.Errorf("coding thread trash purge: store is nil")
	}
	if ctx == nil {
		return ThreadTrashPurgeResult{}, fmt.Errorf("coding thread trash purge: context is required")
	}
	resolved, err := resolveThreadTrashPurgeOptions(options)
	if err != nil {
		return ThreadTrashPurgeResult{}, err
	}
	empty := ThreadTrashPurgeResult{
		Before:     resolved.Before,
		Candidates: []ThreadTrashCandidate{},
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return ThreadTrashPurgeResult{}, contextErr
	}
	if _, statErr := os.Lstat(s.root); errors.Is(statErr, os.ErrNotExist) {
		return empty, nil
	} else if statErr != nil {
		return ThreadTrashPurgeResult{}, fmt.Errorf("coding thread trash purge: inspect store root: %w", statErr)
	}
	if !resolved.Delete {
		root, openErr := openPinnedCatalogRoot(s.root)
		if openErr != nil {
			return ThreadTrashPurgeResult{}, fmt.Errorf("coding thread trash purge: pin store root: %w", openErr)
		}
		result, err = s.planThreadTrashPurge(ctx, root, resolved)
		return result, errors.Join(err, root.Close())
	}

	maintenance, err := s.acquireAttachmentMaintenanceLease()
	if err != nil {
		return ThreadTrashPurgeResult{}, err
	}
	defer func() {
		releaseErr := maintenance.Release()
		if releaseErr == nil {
			return
		}
		resultErr = classifyThreadTrashPurge(result, errors.Join(resultErr, releaseErr))
	}()
	if maintenanceErr := maintenance.Validate(); maintenanceErr != nil {
		return ThreadTrashPurgeResult{}, maintenanceErr
	}
	result, err = s.planThreadTrashPurge(ctx, maintenance.root, resolved)
	if err != nil || len(result.Candidates) == 0 {
		return result, err
	}
	return s.deleteThreadTrashCandidates(ctx, maintenance, result)
}

func resolveThreadTrashPurgeOptions(options ThreadTrashPurgeOptions) (ThreadTrashPurgeOptions, error) {
	if options.Before.IsZero() {
		return ThreadTrashPurgeOptions{}, fmt.Errorf("coding thread trash purge: retention cutoff is required")
	}
	options.Before = options.Before.UTC()
	if options.Before.After(time.Now().UTC()) {
		return ThreadTrashPurgeOptions{}, fmt.Errorf("coding thread trash purge: retention cutoff cannot be future")
	}
	if options.EntryLimit == 0 {
		options.EntryLimit = DefaultThreadTrashScanLimit
	}
	if options.EntryLimit < 1 || options.EntryLimit > DefaultThreadTrashScanLimit {
		return ThreadTrashPurgeOptions{}, fmt.Errorf("coding thread trash purge: invalid scan limit")
	}
	return options, nil
}

func (s *Store) planThreadTrashPurge(
	ctx context.Context,
	storeRoot *os.Root,
	options ThreadTrashPurgeOptions,
) (ThreadTrashPurgeResult, error) {
	result := ThreadTrashPurgeResult{
		Before:     options.Before,
		Candidates: []ThreadTrashCandidate{},
	}
	trashRoot, exists, err := openOptionalThreadTrashChild(storeRoot, "trash")
	if err != nil || !exists {
		return result, err
	}
	defer func() { _ = trashRoot.Close() }()
	threadsRoot, exists, err := openOptionalThreadTrashChild(trashRoot, "threads")
	if err != nil || !exists {
		return result, err
	}
	defer func() { _ = threadsRoot.Close() }()
	entries, err := readBoundedRootEntries(threadsRoot, options.EntryLimit)
	if err != nil {
		return result, fmt.Errorf("coding thread trash purge: scan trash: %w", err)
	}
	result.Scanned = len(entries)
	for _, entry := range entries {
		if contextErr := ctx.Err(); contextErr != nil {
			return result, contextErr
		}
		candidate, parseErr := parseThreadTrashID(entry.Name())
		if parseErr != nil {
			return result, parseErr
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			return result, fmt.Errorf(
				"coding thread trash purge: trash entry %q is not a direct directory",
				entry.Name(),
			)
		}
		identity, validateErr := s.validateThreadTrashCandidate(threadsRoot, candidate)
		if validateErr != nil {
			return result, validateErr
		}
		candidate.identity = identity
		if candidate.TrashedAt.Before(options.Before) {
			result.Candidates = append(result.Candidates, candidate)
		}
	}
	if err := validatePinnedAttachmentDirectory(trashRoot, "threads", threadsRoot); err != nil {
		return result, err
	}
	if err := validatePinnedAttachmentDirectory(storeRoot, "trash", trashRoot); err != nil {
		return result, err
	}
	if err := validatePinnedAttachmentRoot(s.root, storeRoot); err != nil {
		return result, err
	}
	sort.Slice(result.Candidates, func(left, right int) bool {
		if !result.Candidates[left].TrashedAt.Equal(result.Candidates[right].TrashedAt) {
			return result.Candidates[left].TrashedAt.Before(result.Candidates[right].TrashedAt)
		}
		return result.Candidates[left].TrashID < result.Candidates[right].TrashID
	})
	return result, nil
}

func (s *Store) deleteThreadTrashCandidates(
	ctx context.Context,
	maintenance *attachmentMaintenanceLease,
	result ThreadTrashPurgeResult,
) (ThreadTrashPurgeResult, error) {
	trashRoot, exists, err := openOptionalThreadTrashChild(maintenance.root, "trash")
	if err != nil {
		return result, classifyThreadTrashPurge(result, err)
	}
	if !exists {
		return result, fmt.Errorf("coding thread trash purge: trash root disappeared before deletion")
	}
	defer func() { _ = trashRoot.Close() }()
	threadsRoot, exists, err := openOptionalThreadTrashChild(trashRoot, "threads")
	if err != nil {
		return result, classifyThreadTrashPurge(result, err)
	}
	if !exists {
		return result, fmt.Errorf("coding thread trash purge: thread trash disappeared before deletion")
	}
	defer func() { _ = threadsRoot.Close() }()
	for _, candidate := range result.Candidates {
		if contextErr := ctx.Err(); contextErr != nil {
			return result, classifyThreadTrashPurge(result, contextErr)
		}
		if err := maintenance.Validate(); err != nil {
			return result, classifyThreadTrashPurge(result, err)
		}
		if err := validatePinnedAttachmentDirectory(maintenance.root, "trash", trashRoot); err != nil {
			return result, classifyThreadTrashPurge(result, err)
		}
		if err := validatePinnedAttachmentDirectory(trashRoot, "threads", threadsRoot); err != nil {
			return result, classifyThreadTrashPurge(result, err)
		}
		if _, err := s.validateThreadTrashCandidate(threadsRoot, candidate); err != nil {
			return result, classifyThreadTrashPurge(result, err)
		}
		result.AttemptedTrashIDs = append(result.AttemptedTrashIDs, candidate.TrashID)
		result.IncompleteTrashID = candidate.TrashID
		if err := s.removeThreadTrash(threadsRoot, candidate.TrashID); err != nil {
			return result, classifyThreadTrashPurge(
				result,
				fmt.Errorf("coding thread trash purge: remove %q: %w", candidate.TrashID, err),
			)
		}
		if _, err := threadsRoot.Lstat(candidate.TrashID); !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				err = fmt.Errorf("trash entry still exists")
			}
			return result, classifyThreadTrashPurge(
				result,
				fmt.Errorf("coding thread trash purge: verify removal of %q: %w", candidate.TrashID, err),
			)
		}
		result.Deleted++
		result.DeletedTrashIDs = append(result.DeletedTrashIDs, candidate.TrashID)
		result.IncompleteTrashID = ""
		if err := s.syncRoot(threadsRoot); err != nil {
			return result, classifyThreadTrashPurge(
				result,
				fmt.Errorf("coding thread trash purge: sync deletion: %w", err),
			)
		}
	}
	return result, nil
}

func (s *Store) validateThreadTrashCandidate(
	threadsRoot *os.Root,
	candidate ThreadTrashCandidate,
) (os.FileInfo, error) {
	threadRoot, err := openPinnedAttachmentChild(threadsRoot, candidate.TrashID)
	if err != nil {
		return nil, fmt.Errorf("coding thread trash purge: pin %q: %w", candidate.TrashID, err)
	}
	defer func() { _ = threadRoot.Close() }()
	identity, err := threadRoot.Stat(".")
	if err != nil {
		return nil, err
	}
	if candidate.identity != nil && !os.SameFile(candidate.identity, identity) {
		return nil, fmt.Errorf("coding thread trash purge: candidate changed after planning")
	}
	entries, err := readBoundedRootEntries(threadRoot, maxDeletePlanEntries)
	if err != nil {
		return nil, fmt.Errorf("coding thread trash purge: inspect %q: %w", candidate.TrashID, err)
	}
	for _, entry := range entries {
		if !ownedThreadEntry(entry.Name()) {
			return nil, fmt.Errorf(
				"coding thread trash purge: cannot confirm ownership of %q in %q",
				entry.Name(),
				candidate.TrashID,
			)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf(
				"coding thread trash purge: symbolic-link entry %q in %q is not allowed",
				entry.Name(),
				candidate.TrashID,
			)
		}
	}
	metadata, err := loadThreadTrashMetadata(threadRoot, candidate.ThreadID)
	if err != nil {
		return nil, fmt.Errorf("coding thread trash purge: validate %q metadata: %w", candidate.TrashID, err)
	}
	trashPath := filepath.Join(s.root, "trash", "threads", candidate.TrashID)
	if err := validateDeleteProjectBoundaries(trashPath, metadata.Project); err != nil {
		return nil, err
	}
	if err := validatePinnedAttachmentDirectory(threadsRoot, candidate.TrashID, threadRoot); err != nil {
		return nil, fmt.Errorf("coding thread trash purge: revalidate %q: %w", candidate.TrashID, err)
	}
	return identity, nil
}

func loadThreadTrashMetadata(root *os.Root, threadID string) (Metadata, error) {
	entry, err := root.Lstat(metadataFileName)
	if err != nil {
		return Metadata{}, err
	}
	if entry.Mode()&os.ModeSymlink != 0 || !entry.Mode().IsRegular() {
		return Metadata{}, fmt.Errorf("metadata is not a direct regular file")
	}
	catalogRoot, err := openCatalogRoot(root.Name())
	if err != nil {
		return Metadata{}, err
	}
	defer func() { _ = catalogRoot.Close() }()
	catalogIdentity, err := catalogRoot.stat()
	if err != nil {
		return Metadata{}, err
	}
	pinnedIdentity, err := root.Stat(".")
	if err != nil {
		return Metadata{}, err
	}
	if !os.SameFile(catalogIdentity, pinnedIdentity) {
		return Metadata{}, fmt.Errorf("metadata directory changed while opening")
	}
	file, err := openCatalogMetadataFile(catalogRoot)
	if err != nil {
		return Metadata{}, err
	}
	opened, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return Metadata{}, err
	}
	if !os.SameFile(entry, opened) {
		_ = file.Close()
		return Metadata{}, fmt.Errorf("metadata changed while opening")
	}
	if err := validateCatalogMetadataFile(file, opened); err != nil {
		_ = file.Close()
		return Metadata{}, err
	}
	return loadMetadataFile(threadID, file)
}

func openOptionalThreadTrashChild(parent *os.Root, name string) (*os.Root, bool, error) {
	child, err := openPinnedAttachmentChild(parent, name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("coding thread trash purge: open %q: %w", name, err)
	}
	return child, true, nil
}

func parseThreadTrashID(value string) (ThreadTrashCandidate, error) {
	const (
		threadIDBytes  = 36
		timestampBytes = 26
		trashIDBytes   = threadIDBytes + 1 + timestampBytes + 1 + threadIDBytes
	)
	if len(value) != trashIDBytes || value[threadIDBytes] != '-' ||
		value[threadIDBytes+1+timestampBytes] != '-' {
		return ThreadTrashCandidate{}, fmt.Errorf("coding thread trash purge: invalid trash ID %q", value)
	}
	threadID := value[:threadIDBytes]
	if err := validateThreadID(threadID); err != nil {
		return ThreadTrashCandidate{}, fmt.Errorf("coding thread trash purge: invalid trash ID %q", value)
	}
	timestampStart := threadIDBytes + 1
	timestampEnd := timestampStart + timestampBytes
	at, err := time.Parse(threadTrashTimeLayout, value[timestampStart:timestampEnd])
	if err != nil || at.Format(threadTrashTimeLayout) != value[timestampStart:timestampEnd] {
		return ThreadTrashCandidate{}, fmt.Errorf("coding thread trash purge: invalid trash ID %q", value)
	}
	suffix := value[timestampEnd+1:]
	parsed, err := uuid.Parse(suffix)
	if err != nil || parsed.String() != suffix {
		return ThreadTrashCandidate{}, fmt.Errorf("coding thread trash purge: invalid trash ID %q", value)
	}
	return ThreadTrashCandidate{TrashID: value, ThreadID: threadID, TrashedAt: at.UTC()}, nil
}

func classifyThreadTrashPurge(result ThreadTrashPurgeResult, err error) error {
	if err == nil || len(result.AttemptedTrashIDs) == 0 {
		return err
	}
	if IsCommittedThreadTrashPurgeError(err) {
		return err
	}
	return &CommittedThreadTrashPurgeError{Result: result, Err: err}
}
