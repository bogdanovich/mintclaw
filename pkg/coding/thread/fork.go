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
		forkCreatedAt := options.At.UTC()
		for index := range prefix {
			if prefix[index].CreatedAt == nil {
				prefix[index].CreatedAt = &forkCreatedAt
			}
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
		snapshot, snapshotErr := memory.BuildJSONLSnapshot(child.SessionKey, prefix, forkCreatedAt)
		if snapshotErr != nil {
			return fmt.Errorf("coding thread fork: prepare child transcript: %w", snapshotErr)
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
		return s.publishFork(ctx, child, snapshot, result)
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
	scanner.Buffer(make([]byte, 0, 64*1024), memory.MaxJSONLRecordBytes)
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
	roots := make([]int, 0)
	for index, message := range history {
		if message.Role == "user" && message.ToolCallID == "" && message.RootTurnStart {
			roots = append(roots, index)
		}
	}
	return roots
}

func (s *Store) publishFork(
	ctx context.Context,
	child Metadata,
	snapshot memory.JSONLSnapshot,
	result ForkResult,
) error {
	metadataData, err := encodeForkMetadata(child)
	if err != nil {
		return err
	}
	threadsRoot, targetRoot, provisionErr := s.provisionForkTarget(child.ThreadID)
	if provisionErr != nil {
		return provisionErr
	}
	targetLease, err := s.acquireForkTargetLease(targetRoot, result.StateRoot, child.ThreadID)
	if err != nil {
		closeErr := errors.Join(targetRoot.Close(), threadsRoot.Close())
		return fmt.Errorf(
			"coding thread fork: acquire pinned target lease; reservation left in place: %w",
			errors.Join(err, closeErr),
		)
	}
	var sessionsRoot *os.Root
	abort := func(operationErr error) error {
		if sessionsRoot != nil {
			operationErr = errors.Join(operationErr, sessionsRoot.Close())
			sessionsRoot = nil
		}
		operationErr = errors.Join(operationErr, targetRoot.Close(), threadsRoot.Close())
		return s.quarantineUnpublishedFork(targetLease, child.ThreadID, operationErr)
	}
	if identityErr := validateExistingForkLeaseRoot(targetRoot, targetLease); identityErr != nil {
		return abort(fmt.Errorf("coding thread fork: pin target root: %w", identityErr))
	}
	if mkdirErr := targetRoot.Mkdir("sessions", 0o700); mkdirErr != nil {
		return abort(fmt.Errorf("coding thread fork: create pinned sessions directory: %w", mkdirErr))
	}
	if syncErr := s.syncRoot(targetRoot); syncErr != nil {
		return abort(&fileutil.CommittedWriteError{Err: fmt.Errorf("sync pinned target root: %w", syncErr)})
	}
	sessionsRoot, err = targetRoot.OpenRoot("sessions")
	if err != nil {
		return abort(fmt.Errorf("coding thread fork: pin sessions directory: %w", err))
	}
	if writeErr := s.writeRoot(sessionsRoot, snapshot.JSONLFile, snapshot.JSONL, 0o600); writeErr != nil {
		return abort(fmt.Errorf("coding thread fork: write pinned transcript: %w", writeErr))
	}
	if writeErr := s.writeRoot(sessionsRoot, snapshot.MetadataFile, snapshot.Metadata, 0o600); writeErr != nil {
		return abort(fmt.Errorf("coding thread fork: write pinned transcript metadata: %w", writeErr))
	}
	if contextErr := context.Cause(ctx); contextErr != nil {
		return abort(contextErr)
	}
	saveErr := s.writeRoot(targetRoot, metadataFileName, metadataData, 0o600)
	verifyErr := s.verifyPublishedFork(ctx, targetLease, child, snapshot, sessionsRoot)
	published := verifyErr == nil
	if !published {
		saveErr = errors.Join(saveErr, verifyErr)
	}
	closeErr := errors.Join(sessionsRoot.Close(), targetRoot.Close(), threadsRoot.Close())
	sessionsRoot = nil
	saveErr = errors.Join(saveErr, closeErr)
	if !published {
		return s.quarantineUnpublishedFork(targetLease, child.ThreadID, saveErr)
	}
	releaseErr := targetLease.Release()
	if err := errors.Join(saveErr, releaseErr); err != nil {
		return &CommittedForkError{Result: result, Err: err}
	}
	return nil
}

func encodeForkMetadata(child Metadata) ([]byte, error) {
	if err := child.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(child, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("coding thread fork: encode child metadata: %w", err)
	}
	data = append(data, '\n')
	if len(data) > MaxMetadataBytes {
		return nil, fmt.Errorf("coding thread fork: child metadata exceeds %d bytes", MaxMetadataBytes)
	}
	return data, nil
}

func writeRootFileAtomic(root *os.Root, name string, data []byte, mode os.FileMode) error {
	if root == nil || !filepath.IsLocal(name) {
		return fmt.Errorf("coding thread fork: pinned root and local file name are required")
	}
	temporary := ".tmp-" + NewThreadID()
	file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = file.Close()
			_ = root.Remove(temporary)
		}
	}()
	written, err := file.Write(data)
	if err == nil && written != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return err
	}
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := root.Rename(temporary, name); err != nil {
		return err
	}
	cleanup = false
	if err := syncRootDirectory(root); err != nil {
		return &fileutil.CommittedWriteError{Err: err}
	}
	return nil
}

func syncRootDirectory(root *os.Root) error {
	directory, err := root.OpenFile(".", os.O_RDWR, 0)
	if err != nil {
		directory, err = root.Open(".")
	}
	if err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return err
	}
	return directory.Close()
}

// quarantineUnpublishedFork removes a failed preparation from the active
// catalog without recursively deleting through a replaceable pathname. The
// moved entry is kept recoverable only when it still contains the held lease;
// a replacement is restored and never deleted.
func (s *Store) quarantineUnpublishedFork(lease *Lease, threadID string, operationErr error) error {
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return errors.Join(
			operationErr,
			fmt.Errorf("coding thread fork: anchor cleanup root: %w", err),
			lease.Release(),
		)
	}
	defer func() { _ = root.Close() }()
	if err := ensureDirectTrashDirectory(root, "trash"); err != nil {
		return errors.Join(operationErr, err, lease.Release())
	}
	quarantineDir := filepath.Join("trash", "fork-preparations")
	if err := ensureDirectTrashDirectory(root, quarantineDir); err != nil {
		return errors.Join(operationErr, err, lease.Release())
	}
	quarantineID := threadID + "-" + NewThreadID()
	activeName := filepath.Join("threads", threadID)
	quarantineName := filepath.Join(quarantineDir, quarantineID)
	if err := root.Rename(activeName, quarantineName); err != nil {
		return errors.Join(
			operationErr,
			fmt.Errorf("coding thread fork: quarantine unpublished target: %w", err),
			lease.Release(),
		)
	}
	identityErr := validateQuarantinedForkLease(root, quarantineName, lease)
	if identityErr != nil {
		restoreErr := root.Rename(quarantineName, activeName)
		syncErr := errors.Join(
			fileutil.SyncDirectory(filepath.Join(s.root, "threads")),
			fileutil.SyncDirectory(filepath.Join(s.root, quarantineDir)),
		)
		return errors.Join(
			operationErr,
			fmt.Errorf("coding thread fork: cleanup target identity changed: %w", identityErr),
			restoreErr,
			syncErr,
			lease.Release(),
		)
	}
	syncErr := errors.Join(
		fileutil.SyncDirectory(filepath.Join(s.root, "threads")),
		fileutil.SyncDirectory(filepath.Join(s.root, quarantineDir)),
	)
	return errors.Join(operationErr, syncErr, lease.Release())
}

func validateQuarantinedForkLease(root *os.Root, name string, lease *Lease) error {
	targetRoot, err := root.OpenRoot(name)
	if err != nil {
		return err
	}
	defer func() { _ = targetRoot.Close() }()
	return validateExistingForkLeaseRoot(targetRoot, lease)
}

func validateExistingForkLeaseRoot(root *os.Root, lease *Lease) error {
	pinnedLease, err := root.OpenFile(leaseFileName, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer func() { _ = pinnedLease.Close() }()
	pinnedInfo, err := pinnedLease.Stat()
	if err != nil {
		return err
	}
	if validationErr := validateCatalogMetadataFile(pinnedLease, pinnedInfo); validationErr != nil {
		return validationErr
	}
	leaseInfo, err := lease.file.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(pinnedInfo, leaseInfo) {
		return fmt.Errorf("held lease no longer identifies the quarantined target")
	}
	return nil
}

func (s *Store) verifyPublishedFork(
	ctx context.Context,
	targetLease *Lease,
	child Metadata,
	snapshot memory.JSONLSnapshot,
	pinnedSessions *os.Root,
) error {
	return targetLease.withActive(s.root, child.ThreadID, func() error {
		storeRoot, err := openCatalogRoot(s.root)
		if err != nil {
			return fmt.Errorf("coding thread fork: verify store root: %w", err)
		}
		defer func() { _ = storeRoot.Close() }()
		threadsRoot, err := openCatalogChildDirectory(storeRoot, "threads")
		if err != nil {
			return fmt.Errorf("coding thread fork: verify threads root: %w", err)
		}
		defer func() { _ = threadsRoot.Close() }()
		threadRoot, err := openCatalogChildDirectory(threadsRoot, child.ThreadID)
		if err != nil {
			return fmt.Errorf("coding thread fork: verify target root: %w", err)
		}
		defer func() { _ = threadRoot.Close() }()
		if validationErr := validateForkLeaseRoot(threadRoot, targetLease); validationErr != nil {
			return validationErr
		}
		loaded, err := loadCatalogMetadataFromDirectory(threadRoot, child.ThreadID, openCatalogMetadataFile)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(loaded, child) {
			return fmt.Errorf("coding thread fork: published metadata does not match the child")
		}
		activeSessions, err := openCatalogChildDirectory(threadRoot, "sessions")
		if err != nil {
			return fmt.Errorf("coding thread fork: verify active sessions root: %w", err)
		}
		defer func() { _ = activeSessions.Close() }()
		activeInfo, err := activeSessions.stat()
		if err != nil {
			return fmt.Errorf("coding thread fork: stat active sessions root: %w", err)
		}
		pinnedDirectory, err := pinnedSessions.Open(".")
		if err != nil {
			return fmt.Errorf("coding thread fork: stat pinned sessions root: %w", err)
		}
		pinnedInfo, statErr := pinnedDirectory.Stat()
		closeErr := pinnedDirectory.Close()
		if err := errors.Join(statErr, closeErr); err != nil {
			return fmt.Errorf("coding thread fork: stat pinned sessions root: %w", err)
		}
		if !os.SameFile(activeInfo, pinnedInfo) {
			return fmt.Errorf("coding thread fork: active sessions root was replaced")
		}
		if err := verifyForkSnapshotFile(ctx, activeSessions, snapshot.JSONLFile, snapshot.JSONL); err != nil {
			return err
		}
		return verifyForkSnapshotFile(ctx, activeSessions, snapshot.MetadataFile, snapshot.Metadata)
	})
}

func verifyForkSnapshotFile(ctx context.Context, root *catalogDirectory, name string, expected []byte) error {
	file, err := openCatalogFile(root, name)
	if err != nil {
		return fmt.Errorf("coding thread fork: verify snapshot file %q: %w", name, err)
	}
	data, _, readErr := readPinnedForkFile(ctx, file)
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return fmt.Errorf("coding thread fork: verify snapshot file %q: %w", name, err)
	}
	if !bytes.Equal(data, expected) {
		return fmt.Errorf("coding thread fork: published snapshot file %q does not match", name)
	}
	return nil
}

func (s *Store) provisionForkTarget(threadID string) (*os.Root, *os.Root, error) {
	threadsRoot := filepath.Join(s.root, "threads")
	relativeThreads, resolveErr := filepath.Rel(s.durableRoot, threadsRoot)
	if resolveErr != nil {
		return nil, nil, fmt.Errorf("coding thread fork: resolve threads root: %w", resolveErr)
	}
	if !filepath.IsLocal(relativeThreads) {
		return nil, nil, fmt.Errorf("coding thread fork: threads root escapes durable store")
	}
	if err := s.mkdirDurable(s.durableRoot, relativeThreads, 0o700); err != nil {
		return nil, nil, fmt.Errorf("coding thread fork: create threads root: %w", err)
	}
	pinnedThreads, pinErr := openPinnedCatalogRoot(threadsRoot)
	if pinErr != nil {
		return nil, nil, fmt.Errorf("coding thread fork: pin threads root: %w", pinErr)
	}
	if err := pinnedThreads.Mkdir(threadID, 0o700); err != nil {
		_ = pinnedThreads.Close()
		if os.IsExist(err) {
			return nil, nil, fmt.Errorf("coding thread fork: target thread already exists")
		}
		return nil, nil, fmt.Errorf("coding thread fork: reserve target thread: %w", err)
	}
	targetRoot, err := pinnedThreads.OpenRoot(threadID)
	if err != nil {
		_ = pinnedThreads.Close()
		return nil, nil, fmt.Errorf("coding thread fork: pin target reservation: %w", err)
	}
	if err := s.syncRoot(pinnedThreads); err != nil {
		cleanupErr := cleanupForkReservation(pinnedThreads, targetRoot, threadID)
		return nil, nil, errors.Join(
			fmt.Errorf("coding thread fork: sync target reservation: %w", err),
			cleanupErr,
			targetRoot.Close(),
			pinnedThreads.Close(),
		)
	}
	return pinnedThreads, targetRoot, nil
}

func openPinnedCatalogRoot(path string) (*os.Root, error) {
	catalogRoot, err := openCatalogRoot(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = catalogRoot.Close() }()
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	pinned, err := root.Open(".")
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	pinnedInfo, pinnedErr := pinned.Stat()
	catalogInfo, catalogErr := catalogRoot.stat()
	closeErr := pinned.Close()
	if err := errors.Join(pinnedErr, catalogErr, closeErr); err != nil {
		_ = root.Close()
		return nil, err
	}
	if !os.SameFile(pinnedInfo, catalogInfo) {
		_ = root.Close()
		return nil, fmt.Errorf("active catalog root changed while pinning")
	}
	return root, nil
}

func cleanupForkReservation(threadsRoot, targetRoot *os.Root, threadID string) error {
	quarantineName := ".fork-reservation-" + NewThreadID()
	if err := threadsRoot.Rename(threadID, quarantineName); err != nil {
		return err
	}
	restore := true
	defer func() {
		if restore {
			_ = threadsRoot.Rename(quarantineName, threadID)
		}
	}()
	active, err := threadsRoot.Lstat(quarantineName)
	if err != nil {
		return err
	}
	pinned, err := targetRoot.Open(".")
	if err != nil {
		return err
	}
	pinnedInfo, statErr := pinned.Stat()
	closeErr := pinned.Close()
	if err := errors.Join(statErr, closeErr); err != nil {
		return err
	}
	if active.Mode()&os.ModeSymlink != 0 || !os.SameFile(active, pinnedInfo) {
		return fmt.Errorf("coding thread fork: target reservation identity changed")
	}
	if err := threadsRoot.Remove(quarantineName); err != nil {
		return err
	}
	restore = false
	return syncRootDirectory(threadsRoot)
}

func (s *Store) acquireForkTargetLease(root *os.Root, targetPath, threadID string) (*Lease, error) {
	owner := newLeaseOwner()
	if err := owner.validate(); err != nil {
		return nil, err
	}
	file, err := openPinnedThreadLeaseFile(root, targetPath)
	if err != nil {
		return nil, err
	}
	if err := tryAcquireThreadLeaseFile(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := writeLeaseOwner(file, owner); err != nil {
		_ = releaseThreadLeaseFile(file)
		_ = file.Close()
		return nil, err
	}
	return &Lease{storeRoot: s.root, threadID: threadID, owner: owner, file: file}, nil
}
