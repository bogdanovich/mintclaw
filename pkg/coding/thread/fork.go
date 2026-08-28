package thread

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

const (
	// MaxForkMessages bounds the canonical conversational prefix copied by one fork.
	MaxForkMessages = 4096
	// MaxForkTranscriptBytes bounds source JSONL scanned into memory by one fork.
	MaxForkTranscriptBytes int64 = 32 << 20
)

// ForkOptions selects one stable conversational boundary and a fresh project
// snapshot. AtTurn is one-based; zero selects the latest root turn.
type ForkOptions struct {
	TargetThreadID string
	Project        ProjectIdentity
	AtTurn         int
	At             time.Time
}

// ForkResult describes a newly published independent coding thread.
type ForkResult struct {
	SourceThreadID string `json:"source_thread_id"`
	ThreadID       string `json:"thread_id"`
	SessionKey     string `json:"session_key"`
	StateRoot      string `json:"state_root"`
	ProjectRoot    string `json:"project_root"`
	SourceTurn     int    `json:"source_turn"`
	CopiedMessages int    `json:"copied_messages"`
	LiveFilesystem bool   `json:"live_filesystem"`
}

// CommittedForkError reports a catalog-visible fork whose final durability or
// lease release could not be confirmed. Result identifies the resumable child.
type CommittedForkError struct {
	Result ForkResult
	Err    error
}

func (e *CommittedForkError) Error() string {
	return fmt.Sprintf("coding thread fork committed but finalization was not confirmed: %v", e.Err)
}

func (e *CommittedForkError) Unwrap() error { return e.Err }

// IsCommittedForkError distinguishes a published child from preparation errors.
func IsCommittedForkError(err error) bool {
	var committed *CommittedForkError
	return errors.As(err, &committed)
}

// ForkThread copies a bounded canonical conversational prefix while holding
// the source lease, publishes metadata last, and never copies workspace state.
func (s *Store) ForkThread(
	ctx context.Context,
	sourceLease *Lease,
	options ForkOptions,
) (Metadata, ForkResult, error) {
	if s == nil {
		return Metadata{}, ForkResult{}, fmt.Errorf("coding thread store is nil")
	}
	if ctx == nil {
		return Metadata{}, ForkResult{}, fmt.Errorf("coding thread fork: context is required")
	}
	if options.AtTurn < 0 {
		return Metadata{}, ForkResult{}, fmt.Errorf("coding thread fork: turn must be zero or positive")
	}
	if options.At.IsZero() {
		return Metadata{}, ForkResult{}, fmt.Errorf("coding thread fork: timestamp is required")
	}
	if err := options.Project.Validate(); err != nil {
		return Metadata{}, ForkResult{}, fmt.Errorf("coding thread fork: current project: %w", err)
	}
	if err := validateThreadID(options.TargetThreadID); err != nil {
		return Metadata{}, ForkResult{}, err
	}
	sourceThreadID := sourceLease.ThreadID()
	if sourceThreadID == options.TargetThreadID {
		return Metadata{}, ForkResult{}, fmt.Errorf("coding thread fork: target must differ from source")
	}

	var child Metadata
	var result ForkResult
	err := sourceLease.withActive(s.root, sourceThreadID, func() error {
		source, err := s.loadDirectMetadata(sourceThreadID)
		if err != nil {
			return err
		}
		if source.Project.ProjectKey != options.Project.ProjectKey {
			return fmt.Errorf("coding thread fork: source belongs to project %q", source.Project.ProjectRoot)
		}
		history, revision, err := s.readForkHistory(ctx, source)
		if err != nil {
			return err
		}
		prefix, point, err := selectForkPrefix(history, revision, options.AtTurn)
		if err != nil {
			return err
		}
		child, err = NewMetadata(
			options.TargetThreadID,
			options.Project,
			"Fork of "+source.Title,
			options.At,
		)
		if err != nil {
			return err
		}
		child.Model = source.Model
		child.Provider = source.Provider
		child.ParentThread = source.ThreadID
		child.Fork = &point
		if validationErr := child.Validate(); validationErr != nil {
			return validationErr
		}
		result = ForkResult{
			SourceThreadID: source.ThreadID,
			ThreadID:       child.ThreadID, SessionKey: child.SessionKey,
			ProjectRoot: child.Project.ProjectRoot, SourceTurn: point.SourceTurn,
			CopiedMessages: len(prefix), LiveFilesystem: true,
		}
		result.StateRoot, err = s.ThreadRoot(child.ThreadID)
		if err != nil {
			return err
		}
		return s.publishFork(ctx, child, prefix, result)
	})
	return child, result, err
}

func (s *Store) loadDirectMetadata(threadID string) (Metadata, error) {
	storeRoot, err := openCatalogRoot(s.root)
	if err != nil {
		return Metadata{}, fmt.Errorf("coding thread: open store root: %w", err)
	}
	defer func() { _ = storeRoot.Close() }()
	threadsRoot, err := openCatalogChildDirectory(storeRoot, "threads")
	if err != nil {
		return Metadata{}, fmt.Errorf("coding thread: open threads root: %w", err)
	}
	defer func() { _ = threadsRoot.Close() }()
	directThreadRoot, err := openCatalogChildDirectory(threadsRoot, threadID)
	if err != nil {
		return Metadata{}, fmt.Errorf("coding thread: open direct thread root: %w", err)
	}
	defer func() { _ = directThreadRoot.Close() }()
	return loadCatalogMetadataFromDirectory(
		directThreadRoot,
		threadID,
		openCatalogMetadataFile,
	)
}

func (s *Store) readForkHistory(
	ctx context.Context,
	source Metadata,
) ([]providers.Message, memory.HistoryRevision, error) {
	threadRoot, err := s.ThreadRoot(source.ThreadID)
	if err != nil {
		return nil, memory.HistoryRevision{}, err
	}
	sessionsRoot := filepath.Join(threadRoot, "sessions")
	info, err := os.Lstat(sessionsRoot)
	if err != nil {
		return nil, memory.HistoryRevision{}, fmt.Errorf("coding thread fork: inspect source sessions: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, memory.HistoryRevision{}, fmt.Errorf(
			"coding thread fork: source sessions is not a direct directory",
		)
	}
	sessionStem := "coding_" + source.ThreadID
	for _, name := range []string{sessionStem + ".jsonl", sessionStem + ".meta.json"} {
		if validationErr := validateForkTranscriptFile(filepath.Join(sessionsRoot, name)); validationErr != nil {
			return nil, memory.HistoryRevision{}, validationErr
		}
	}
	canonical, err := memory.NewJSONLStore(sessionsRoot)
	if err != nil {
		return nil, memory.HistoryRevision{}, fmt.Errorf("coding thread fork: open source transcript: %w", err)
	}
	backend := session.NewJSONLBackend(canonical)
	revision, revisionErr := backend.GetHistoryRevision(ctx, source.SessionKey)
	if revisionErr != nil {
		_ = backend.Close()
		return nil, memory.HistoryRevision{}, fmt.Errorf(
			"coding thread fork: inspect source transcript: %w",
			revisionErr,
		)
	}
	visible := revision.Count - revision.Skip
	if visible < 0 || visible > MaxForkMessages || revision.FileSize > MaxForkTranscriptBytes {
		_ = backend.Close()
		return nil, memory.HistoryRevision{}, fmt.Errorf(
			"coding thread fork: source transcript exceeds %d messages or %d bytes",
			MaxForkMessages,
			MaxForkTranscriptBytes,
		)
	}
	history, readErr := backend.ReadTurnHistory(ctx, source.SessionKey)
	after, afterErr := backend.GetHistoryRevision(ctx, source.SessionKey)
	closeErr := backend.Close()
	if err := errors.Join(readErr, afterErr, closeErr); err != nil {
		return nil, memory.HistoryRevision{}, fmt.Errorf("coding thread fork: read source transcript: %w", err)
	}
	if revision != after || len(history) != visible {
		return nil, memory.HistoryRevision{}, fmt.Errorf("coding thread fork: source transcript changed while reading")
	}
	return history, revision, nil
}

func validateForkTranscriptFile(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("coding thread fork: inspect source transcript file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("coding thread fork: source transcript contains a linked or non-regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("coding thread fork: open source transcript file: %w", err)
	}
	directInfo, statErr := file.Stat()
	if statErr != nil {
		return fmt.Errorf(
			"coding thread fork: inspect opened source transcript file: %w",
			errors.Join(statErr, file.Close()),
		)
	}
	validateErr := validateCatalogMetadataFile(file, directInfo)
	closeErr := file.Close()
	if err := errors.Join(statErr, validateErr, closeErr); err != nil {
		return fmt.Errorf("coding thread fork: source transcript file is unsafe: %w", err)
	}
	if directInfo.Size() > MaxForkTranscriptBytes {
		return fmt.Errorf(
			"coding thread fork: source transcript file exceeds %d bytes",
			MaxForkTranscriptBytes,
		)
	}
	return nil
}

func selectForkPrefix(
	history []providers.Message,
	revision memory.HistoryRevision,
	atTurn int,
) ([]providers.Message, ForkPoint, error) {
	rootIndexes := forkRootIndexes(history)
	if len(rootIndexes) == 0 {
		return nil, ForkPoint{}, fmt.Errorf("coding thread fork: source has no stable user turns")
	}
	selected := atTurn
	if selected == 0 {
		selected = len(rootIndexes)
	}
	if selected > len(rootIndexes) {
		return nil, ForkPoint{}, fmt.Errorf(
			"coding thread fork: turn %d exceeds %d available turns",
			selected,
			len(rootIndexes),
		)
	}
	rootIndex := rootIndexes[selected-1]
	end := len(history)
	if selected < len(rootIndexes) {
		end = rootIndexes[selected]
	}
	identity, err := memory.HistoryCursorForMessages(history, rootIndex+1)
	if err != nil {
		return nil, ForkPoint{}, err
	}
	prefix := append([]providers.Message(nil), history[:end]...)
	return prefix, ForkPoint{
		SourceRevision: revision.Revision, SourceMessageID: identity.Digest,
		SourceMessageIndex: rootIndex, SourceTurn: selected, CopiedMessages: len(prefix),
	}, nil
}

func forkRootIndexes(history []providers.Message) []int {
	marked := make([]int, 0)
	legacy := make([]int, 0)
	for index, message := range history {
		if message.Role != "user" || message.ToolCallID != "" {
			continue
		}
		legacy = append(legacy, index)
		if message.RootTurnStart {
			marked = append(marked, index)
		}
	}
	if len(marked) == 0 {
		return legacy
	}
	transition := marked[0]
	roots := make([]int, 0, len(legacy)+len(marked))
	for _, index := range legacy {
		if index >= transition {
			break
		}
		roots = append(roots, index)
	}
	return append(roots, marked...)
}

func (s *Store) publishFork(
	ctx context.Context,
	child Metadata,
	history []providers.Message,
	result ForkResult,
) error {
	if err := s.provisionForkTarget(result.StateRoot); err != nil {
		return err
	}
	cleanup := func(operationErr error) error {
		removeErr := os.RemoveAll(result.StateRoot)
		syncErr := fileutil.SyncDirectory(filepath.Join(s.root, "threads"))
		return errors.Join(operationErr, removeErr, syncErr)
	}
	targetLease, err := s.AcquireLease(child.ThreadID)
	if err != nil {
		return cleanup(err)
	}
	sessionsRoot := filepath.Join(result.StateRoot, "sessions")
	if mkdirErr := s.mkdirDurable(result.StateRoot, "sessions", 0o700); mkdirErr != nil {
		return cleanup(errors.Join(mkdirErr, targetLease.Release()))
	}
	canonical, err := memory.NewJSONLStore(sessionsRoot)
	if err != nil {
		return cleanup(errors.Join(err, targetLease.Release()))
	}
	backend := session.NewJSONLBackend(canonical)
	writeErr := backend.ReplaceTurnHistory(ctx, child.SessionKey, history)
	closeErr := backend.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return cleanup(errors.Join(err, targetLease.Release()))
	}
	saveErr := s.Save(child)
	published := saveErr == nil || fileutil.IsCommittedWriteError(saveErr)
	releaseErr := targetLease.Release()
	if !published {
		return cleanup(errors.Join(saveErr, releaseErr))
	}
	if err := errors.Join(saveErr, releaseErr); err != nil {
		return &CommittedForkError{Result: result, Err: err}
	}
	return nil
}

func (s *Store) provisionForkTarget(targetRoot string) error {
	threadsRoot := filepath.Join(s.root, "threads")
	relativeThreads, err := filepath.Rel(s.durableRoot, threadsRoot)
	if err != nil {
		return fmt.Errorf("coding thread fork: resolve threads root: %w", err)
	}
	if !filepath.IsLocal(relativeThreads) {
		return fmt.Errorf("coding thread fork: threads root escapes durable store")
	}
	if err := s.mkdirDurable(s.durableRoot, relativeThreads, 0o700); err != nil {
		return fmt.Errorf("coding thread fork: create threads root: %w", err)
	}
	if err := os.Mkdir(targetRoot, 0o700); err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("coding thread fork: target thread already exists")
		}
		return fmt.Errorf("coding thread fork: reserve target thread: %w", err)
	}
	if err := fileutil.SyncDirectory(threadsRoot); err != nil {
		return errors.Join(
			fmt.Errorf("coding thread fork: sync target reservation: %w", err),
			os.Remove(targetRoot),
			fileutil.SyncDirectory(threadsRoot),
		)
	}
	return nil
}
