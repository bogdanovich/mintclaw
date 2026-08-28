package thread

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/providers/messageutil"
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
		source, history, revision, err := s.readForkSource(ctx, sourceLease, options.Project.ProjectKey)
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

func (s *Store) readForkSource(
	ctx context.Context,
	lease *Lease,
	projectKey string,
) (Metadata, []providers.Message, memory.HistoryRevision, error) {
	threadID := lease.ThreadID()
	storeRoot, err := openCatalogRoot(s.root)
	if err != nil {
		return Metadata{}, nil, memory.HistoryRevision{}, fmt.Errorf("coding thread fork: open store root: %w", err)
	}
	defer func() { _ = storeRoot.Close() }()
	threadsRoot, err := openCatalogChildDirectory(storeRoot, "threads")
	if err != nil {
		return Metadata{}, nil, memory.HistoryRevision{}, fmt.Errorf("coding thread fork: open threads root: %w", err)
	}
	defer func() { _ = threadsRoot.Close() }()
	threadRoot, err := openCatalogChildDirectory(threadsRoot, threadID)
	if err != nil {
		return Metadata{}, nil, memory.HistoryRevision{}, fmt.Errorf("coding thread fork: open thread root: %w", err)
	}
	defer func() { _ = threadRoot.Close() }()
	if leaseErr := validateForkLeaseRoot(threadRoot, lease); leaseErr != nil {
		return Metadata{}, nil, memory.HistoryRevision{}, leaseErr
	}
	metadata, err := loadCatalogMetadataFromDirectory(threadRoot, threadID, openCatalogMetadataFile)
	if err != nil {
		return Metadata{}, nil, memory.HistoryRevision{}, err
	}
	if metadata.Project.ProjectKey != projectKey {
		return Metadata{}, nil, memory.HistoryRevision{}, fmt.Errorf(
			"coding thread fork: source belongs to project %q",
			metadata.Project.ProjectRoot,
		)
	}
	sessionsRoot, err := openCatalogChildDirectory(threadRoot, "sessions")
	if err != nil {
		return Metadata{}, nil, memory.HistoryRevision{}, fmt.Errorf("coding thread fork: open sessions: %w", err)
	}
	defer func() { _ = sessionsRoot.Close() }()
	sessionStem := "coding_" + metadata.ThreadID
	metaFile, err := openCatalogFile(sessionsRoot, sessionStem+".meta.json")
	if err != nil {
		return Metadata{}, nil, memory.HistoryRevision{}, fmt.Errorf(
			"coding thread fork: pin transcript metadata: %w",
			err,
		)
	}
	jsonlFile, err := openCatalogFile(sessionsRoot, sessionStem+".jsonl")
	if err != nil {
		_ = metaFile.Close()
		return Metadata{}, nil, memory.HistoryRevision{}, fmt.Errorf(
			"coding thread fork: pin transcript JSONL: %w",
			err,
		)
	}
	metaData, _, metaErr := readPinnedForkFile(ctx, metaFile)
	jsonlData, jsonlInfo, jsonlErr := readPinnedForkFile(ctx, jsonlFile)
	closeErr := errors.Join(jsonlFile.Close(), metaFile.Close())
	if readErr := errors.Join(metaErr, jsonlErr, closeErr); readErr != nil {
		return Metadata{}, nil, memory.HistoryRevision{}, readErr
	}
	var sessionMeta memory.SessionMeta
	if decodeErr := json.Unmarshal(metaData, &sessionMeta); decodeErr != nil {
		return Metadata{}, nil, memory.HistoryRevision{}, fmt.Errorf(
			"coding thread fork: decode transcript metadata: %w",
			decodeErr,
		)
	}
	if sessionMeta.Key != "" && sessionMeta.Key != metadata.SessionKey {
		return Metadata{}, nil, memory.HistoryRevision{}, fmt.Errorf("coding thread fork: source session key mismatch")
	}
	if sessionMeta.HistoryDirty {
		return Metadata{}, nil, memory.HistoryRevision{}, fmt.Errorf(
			"coding thread fork: source transcript has an unfinished history mutation",
		)
	}
	visible := sessionMeta.Count - sessionMeta.Skip
	if sessionMeta.Count < 0 || sessionMeta.Skip < 0 || visible < 0 || visible > MaxForkMessages {
		return Metadata{}, nil, memory.HistoryRevision{}, fmt.Errorf(
			"coding thread fork: source transcript exceeds %d visible messages",
			MaxForkMessages,
		)
	}
	history, rawCount, err := decodePinnedForkHistory(ctx, jsonlData, sessionMeta.Skip)
	if err != nil {
		return Metadata{}, nil, memory.HistoryRevision{}, err
	}
	if rawCount != sessionMeta.Count || len(history) != visible {
		return Metadata{}, nil, memory.HistoryRevision{}, fmt.Errorf(
			"coding thread fork: pinned transcript does not match its metadata",
		)
	}
	revision := memory.HistoryRevision{
		Revision:  sessionMeta.HistoryRevision,
		Count:     sessionMeta.Count,
		Skip:      sessionMeta.Skip,
		FileSize:  jsonlInfo.Size(),
		ModTimeNS: jsonlInfo.ModTime().UnixNano(),
	}
	return metadata, history, revision, nil
}

func validateForkLeaseRoot(root *catalogDirectory, lease *Lease) error {
	pinnedLease, err := openThreadLeaseFile(root)
	if err != nil {
		return fmt.Errorf("coding thread fork: pin source lease path: %w", err)
	}
	defer func() { _ = pinnedLease.Close() }()
	pinnedInfo, err := pinnedLease.Stat()
	if err != nil {
		return err
	}
	leaseInfo, err := lease.file.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(pinnedInfo, leaseInfo) {
		return fmt.Errorf("coding thread fork: source lease no longer identifies the active thread root")
	}
	return nil
}

func readPinnedForkFile(ctx context.Context, file *os.File) ([]byte, os.FileInfo, error) {
	before, err := file.Stat()
	if err != nil {
		return nil, nil, fmt.Errorf("coding thread fork: stat pinned source file: %w", err)
	}
	if validationErr := validateCatalogMetadataFile(file, before); validationErr != nil {
		return nil, nil, fmt.Errorf("coding thread fork: pinned source file is unsafe: %w", validationErr)
	}
	if before.Size() > MaxForkTranscriptBytes {
		return nil, nil, fmt.Errorf("coding thread fork: pinned source file exceeds %d bytes", MaxForkTranscriptBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxForkTranscriptBytes+1))
	if err != nil {
		return nil, nil, fmt.Errorf("coding thread fork: read pinned source file: %w", err)
	}
	if len(data) > int(MaxForkTranscriptBytes) {
		return nil, nil, fmt.Errorf("coding thread fork: pinned source file exceeds %d bytes", MaxForkTranscriptBytes)
	}
	if contextErr := context.Cause(ctx); contextErr != nil {
		return nil, nil, contextErr
	}
	after, err := file.Stat()
	if err != nil {
		return nil, nil, fmt.Errorf("coding thread fork: restat pinned source file: %w", err)
	}
	if before.Size() != after.Size() || before.ModTime() != after.ModTime() {
		return nil, nil, fmt.Errorf("coding thread fork: pinned source file changed while reading")
	}
	return data, before, nil
}

func decodePinnedForkHistory(
	ctx context.Context,
	data []byte,
	skip int,
) ([]providers.Message, int, error) {
	var history []providers.Message
	rawCount := 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), int(MaxForkTranscriptBytes))
	for scanner.Scan() {
		if err := context.Cause(ctx); err != nil {
			return nil, 0, err
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		rawCount++
		if rawCount <= skip {
			continue
		}
		var message providers.Message
		if err := json.Unmarshal(line, &message); err != nil {
			return nil, 0, fmt.Errorf("coding thread fork: decode JSONL line %d: %w", rawCount, err)
		}
		if !messageutil.IsTransientAssistantThoughtMessage(message) {
			history = append(history, message)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, fmt.Errorf("coding thread fork: scan pinned JSONL: %w", err)
	}
	return history, rawCount, nil
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
	published := saveErr == nil
	if saveErr != nil {
		loaded, verifyErr := s.Load(child.ThreadID)
		published = verifyErr == nil && reflect.DeepEqual(loaded, child)
		if !published {
			if verifyErr == nil {
				verifyErr = fmt.Errorf("published metadata does not match the fork child")
			}
			saveErr = errors.Join(saveErr, verifyErr)
		}
	}
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
