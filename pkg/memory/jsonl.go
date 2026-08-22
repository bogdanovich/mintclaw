package memory

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/providers/messageutil"
)

const (
	// numLockShards is the fixed number of mutexes used to serialize
	// per-session access. Using a sharded array instead of a map keeps
	// memory bounded regardless of how many sessions are created over
	// the lifetime of the process — important for a long-running daemon.
	numLockShards = 64

	// maxLineSize is the maximum size of a single JSON line in a .jsonl
	// file. Tool results (read_file, web search, etc.) can be large, so
	// we set a generous limit. The scanner starts at 64 KB and grows
	// only as needed up to this cap.
	maxLineSize = 10 * 1024 * 1024 // 10 MB

	maxHistoryPageMessages = 256
)

// SessionMeta holds per-session metadata stored in a .meta.json file.
//
// Scope is stored as raw JSON so pkg/memory can stay decoupled from the
// higher-level session package while still preserving structured scope data.
type SessionMeta struct {
	Key       string          `json:"key"`
	Summary   string          `json:"summary"`
	Skip      int             `json:"skip"`
	Count     int             `json:"count"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	Scope     json.RawMessage `json:"scope,omitempty"`
	// HistoryRevision changes whenever the visible canonical history changes.
	// HistoryDirty remains set across crashes during multi-file mutations.
	HistoryRevision      uint64 `json:"history_revision,omitempty"`
	HistoryDirty         bool   `json:"history_dirty,omitempty"`
	HistoryHasPrevious   bool   `json:"history_has_previous,omitempty"`
	HistoryPreviousCount int    `json:"history_previous_count,omitempty"`
	HistoryPreviousSkip  int    `json:"history_previous_skip,omitempty"`
	HistoryTargetDigest  string `json:"history_target_digest,omitempty"`
}

// JSONLStore implements Store using append-only JSONL files.
//
// Each session is stored as two files:
//
//	{sanitized_key}.jsonl      — one JSON-encoded message per line, append-only
//	{sanitized_key}.meta.json  — session metadata (summary, logical truncation offset)
//
// Messages are never physically deleted from the JSONL file. Instead,
// TruncateHistory records a "skip" offset in the metadata file and
// GetHistory ignores lines before that offset. This keeps all writes
// append-only, which is both fast and crash-safe.
type JSONLStore struct {
	dir          string
	locks        [numLockShards]sync.Mutex
	journalFault func(jsonlJournalWriteStage) error
	appendWrite  func(*os.File, []byte) (int, error)
}

// CommittedAppendError reports that the JSONL record was durably appended,
// but a later close or metadata-finalization step failed. Callers must recover
// from the canonical history instead of blindly retrying the append.
type CommittedAppendError struct {
	Err error
}

func (e *CommittedAppendError) Error() string {
	return fmt.Sprintf("memory: append committed but finalization failed: %v", e.Err)
}

func (e *CommittedAppendError) Unwrap() error {
	return e.Err
}

// IsCommittedAppendError reports whether err preserves a committed append.
func IsCommittedAppendError(err error) bool {
	var committed *CommittedAppendError
	return errors.As(err, &committed)
}

// IndeterminateAppendError reports that writing began, but file or directory
// durability could not be confirmed. The append must not be published as
// durable, and retrying it may duplicate a record that survived the failure.
type IndeterminateAppendError struct {
	Err error
}

func (e *IndeterminateAppendError) Error() string {
	return fmt.Sprintf("memory: append outcome is indeterminate; do not blindly retry: %v", e.Err)
}

func (e *IndeterminateAppendError) Unwrap() error {
	return e.Err
}

// IsIndeterminateAppendError reports whether err is unsafe to retry without
// first recovering canonical history.
func IsIndeterminateAppendError(err error) bool {
	var indeterminate *IndeterminateAppendError
	return errors.As(err, &indeterminate)
}

func contextCause(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return context.Cause(ctx)
}

type jsonlJournalWriteStage string

const (
	jsonlJournalStageFlush  jsonlJournalWriteStage = "flush"
	jsonlJournalStageAppend jsonlJournalWriteStage = "append"
	jsonlJournalStageFsync  jsonlJournalWriteStage = "fsync"
	jsonlJournalStageDir    jsonlJournalWriteStage = "directory"
	jsonlJournalStageRename jsonlJournalWriteStage = "rename"
)

func (s *JSONLStore) injectJournalFault(stage jsonlJournalWriteStage) error {
	if s.journalFault == nil {
		return nil
	}
	return s.journalFault(stage)
}

// NewJSONLStore creates a new JSONL-backed store rooted at dir.
func NewJSONLStore(dir string) (*JSONLStore, error) {
	err := os.MkdirAll(dir, 0o755)
	if err != nil {
		return nil, fmt.Errorf("memory: create directory: %w", err)
	}
	return &JSONLStore{dir: dir, appendWrite: func(file *os.File, data []byte) (int, error) {
		return file.Write(data)
	}}, nil
}

// sessionLock returns a mutex for the given session key.
// Keys are mapped to a fixed pool of shards via FNV hash, so
// memory usage is O(1) regardless of total session count.
func (s *JSONLStore) sessionLock(key string) *sync.Mutex {
	h := fnv.New32a()
	h.Write([]byte(key))
	return &s.locks[h.Sum32()%numLockShards]
}

func (s *JSONLStore) jsonlPath(key string) string {
	return filepath.Join(s.dir, sanitizeKey(key)+".jsonl")
}

func (s *JSONLStore) metaPath(key string) string {
	return filepath.Join(s.dir, sanitizeKey(key)+".meta.json")
}

// sanitizeKey converts a session key to a safe filename component.
// Mirrors pkg/session.sanitizeFilename so that migration paths match.
// Replaces ':' with '_' (session key separator) and '/' and '\' with '_'
// so composite IDs (e.g. Telegram forum "chatID/threadID", Slack "channel/thread_ts")
// do not create subdirectories or break on Windows.
func sanitizeKey(key string) string {
	s := strings.ReplaceAll(key, ":", "_")
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	return s
}

// readMeta loads the metadata file for a session.
// Returns a zero-value sessionMeta if the file does not exist.
func (s *JSONLStore) readMeta(key string) (SessionMeta, error) {
	data, err := os.ReadFile(s.metaPath(key))
	if os.IsNotExist(err) {
		return SessionMeta{Key: key}, nil
	}
	if err != nil {
		return SessionMeta{}, fmt.Errorf("memory: read meta: %w", err)
	}
	var meta SessionMeta
	err = json.Unmarshal(data, &meta)
	if err != nil {
		return SessionMeta{}, fmt.Errorf("memory: decode meta: %w", err)
	}
	if meta.Key == "" {
		meta.Key = key
	}
	return meta, nil
}

// writeMeta atomically writes the metadata file using the project's
// standard WriteFileAtomic (temp + fsync + rename).
func (s *JSONLStore) writeMeta(key string, meta SessionMeta) error {
	if strings.TrimSpace(meta.Key) == "" {
		meta.Key = key
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("memory: encode meta: %w", err)
	}
	return fileutil.WriteFileAtomic(s.metaPath(key), data, 0o644)
}

func bumpHistoryRevision(meta *SessionMeta) {
	meta.HistoryRevision++
	if meta.HistoryRevision == 0 {
		meta.HistoryRevision = 1
	}
}

func (s *JSONLStore) beginHistoryMutation(key string, meta *SessionMeta, bump bool) error {
	if bump {
		bumpHistoryRevision(meta)
	}
	meta.HistoryDirty = true
	return s.writeMeta(key, *meta)
}

func (s *JSONLStore) finishHistoryMutation(key string, meta *SessionMeta) error {
	meta.HistoryDirty = false
	meta.HistoryHasPrevious = false
	meta.HistoryPreviousCount = 0
	meta.HistoryPreviousSkip = 0
	meta.HistoryTargetDigest = ""
	return s.writeMeta(key, *meta)
}

func (s *JSONLStore) reconcileDirtyHistory(key string, meta *SessionMeta) error {
	if !meta.HistoryDirty {
		return nil
	}
	jsonlExists, err := repairDirtyJSONL(s.jsonlPath(key))
	if err != nil {
		return err
	}
	if jsonlExists {
		if syncErr := fileutil.SyncDirectory(s.dir); syncErr != nil {
			return fmt.Errorf("memory: sync recovered jsonl directory: %w", syncErr)
		}
	}
	rawCount, _, err := scanRetainedMessageLines(s.jsonlPath(key))
	if err != nil {
		return err
	}
	targetReached := false
	if meta.HistoryTargetDigest != "" && jsonlExists {
		digest, digestErr := digestFile(s.jsonlPath(key))
		if digestErr != nil {
			return digestErr
		}
		targetReached = digest == meta.HistoryTargetDigest
	}
	if meta.HistoryHasPrevious && meta.HistoryTargetDigest != "" && !targetReached {
		meta.Count = meta.HistoryPreviousCount
		meta.Skip = meta.HistoryPreviousSkip
	} else if meta.HistoryHasPrevious && meta.HistoryTargetDigest == "" && rawCount != meta.Count {
		// Legacy dirty metadata did not record a replacement identity. Keep
		// the historical count-based fallback for stores created before the
		// digest field existed.
		meta.Count = meta.HistoryPreviousCount
		meta.Skip = meta.HistoryPreviousSkip
	} else {
		meta.Count = rawCount
		if meta.Skip > rawCount {
			meta.Skip = rawCount
		}
	}
	if err := s.finishHistoryMutation(key, meta); err != nil {
		return fmt.Errorf("memory: finish dirty history recovery: %w", err)
	}
	return nil
}

func digestFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("memory: open jsonl for digest: %w", err)
	}
	defer func() { _ = file.Close() }()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("memory: digest jsonl: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func repairDirtyJSONL(path string) (bool, error) {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("memory: open jsonl for tail recovery: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return true, fmt.Errorf("memory: stat jsonl for tail recovery: %w", err)
	}
	size := info.Size()
	if size == 0 {
		return true, syncAndCloseRecoveredJSONL(file)
	}
	var last [1]byte
	if _, err := file.ReadAt(last[:], size-1); err != nil {
		_ = file.Close()
		return true, fmt.Errorf("memory: inspect jsonl tail: %w", err)
	}
	if last[0] == '\n' {
		return true, syncAndCloseRecoveredJSONL(file)
	}

	const recoveryChunkSize = 64 * 1024
	buffer := make([]byte, recoveryChunkSize)
	truncateAt := int64(0)
	for end := size; end > 0; {
		start := max(int64(0), end-int64(len(buffer)))
		chunk := buffer[:end-start]
		read, readErr := file.ReadAt(chunk, start)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			_ = file.Close()
			return true, fmt.Errorf("memory: scan jsonl tail: %w", readErr)
		}
		if index := bytes.LastIndexByte(chunk[:read], '\n'); index >= 0 {
			truncateAt = start + int64(index) + 1
			break
		}
		end = start
	}
	if err := file.Truncate(truncateAt); err != nil {
		_ = file.Close()
		return true, fmt.Errorf("memory: truncate incomplete jsonl tail: %w", err)
	}
	return true, syncAndCloseRecoveredJSONL(file)
}

func syncAndCloseRecoveredJSONL(file *os.File) error {
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("memory: sync recovered jsonl tail: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("memory: close recovered jsonl tail: %w", err)
	}
	return nil
}

func cloneRawJSON(data json.RawMessage) json.RawMessage {
	if len(data) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), data...)
}

// GetSessionMeta returns the current metadata snapshot for sessionKey.
func (s *JSONLStore) GetSessionMeta(_ context.Context, sessionKey string) (SessionMeta, error) {
	l := s.sessionLock(sessionKey)
	l.Lock()
	defer l.Unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return SessionMeta{}, err
	}
	meta.Scope = cloneRawJSON(meta.Scope)
	return meta, nil
}

// GetHistoryRevision returns a cheap durable identity while holding the same
// per-session lock used by writers, so callers never observe an in-process
// mutation halfway through.
func (s *JSONLStore) GetHistoryRevision(
	_ context.Context,
	sessionKey string,
) (HistoryRevision, error) {
	l := s.sessionLock(sessionKey)
	l.Lock()
	defer l.Unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return HistoryRevision{}, err
	}
	if recoveryErr := s.reconcileDirtyHistory(sessionKey, &meta); recoveryErr != nil {
		return HistoryRevision{}, recoveryErr
	}
	var size, modTimeNS int64
	info, err := os.Stat(s.jsonlPath(sessionKey))
	if err == nil {
		size = info.Size()
		modTimeNS = info.ModTime().UnixNano()
	} else if !os.IsNotExist(err) {
		return HistoryRevision{}, err
	}
	return HistoryRevision{
		Revision:  meta.HistoryRevision,
		Count:     meta.Count,
		Skip:      meta.Skip,
		Dirty:     meta.HistoryDirty,
		FileSize:  size,
		ModTimeNS: modTimeNS,
	}, nil
}

// UpsertSessionMeta stores structured session metadata while preserving
// summary/count/skip timestamps maintained by the core JSONL store.
func (s *JSONLStore) UpsertSessionMeta(
	_ context.Context,
	sessionKey string,
	scope json.RawMessage,
) error {
	l := s.sessionLock(sessionKey)
	l.Lock()
	defer l.Unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return err
	}
	meta.Scope = cloneRawJSON(scope)
	now := time.Now()
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = now
	}
	meta.UpdatedAt = now

	return s.writeMeta(sessionKey, meta)
}

// readMessages reads valid JSON lines from a .jsonl file, skipping
// the first `skip` lines without unmarshaling them. This avoids the
// cost of json.Unmarshal on logically truncated messages.
// Malformed trailing lines (e.g. from a crash) are silently skipped.
func readMessages(path string, skip int) ([]providers.Message, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return []providers.Message{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("memory: open jsonl: %w", err)
	}
	defer func() { _ = f.Close() }()

	var msgs []providers.Message
	scanner := bufio.NewScanner(f)
	// Allow large lines for tool results (read_file, web search, etc.).
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)

	lineNum := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		lineNum++
		if lineNum <= skip {
			continue
		}
		var msg providers.Message
		if err := json.Unmarshal(line, &msg); err != nil {
			// Corrupt line — likely a partial write from a crash.
			// Log so operators know data was skipped, but don't
			// fail the entire read; this is the standard JSONL
			// recovery pattern.
			log.Printf("memory: skipping corrupt line %d in %s: %v",
				lineNum, filepath.Base(path), err)
			continue
		}
		if messageutil.IsTransientAssistantThoughtMessage(msg) {
			continue
		}
		msgs = append(msgs, msg)
	}
	if scanner.Err() != nil {
		return nil, fmt.Errorf("memory: scan jsonl: %w", scanner.Err())
	}

	if msgs == nil {
		msgs = []providers.Message{}
	}
	return msgs, nil
}

// scanRetainedMessageLines returns the total number of non-empty raw JSONL
// lines plus the raw line numbers that survive readMessages filtering.
// TruncateHistory uses this to compute keepLast against retained messages
// while preserving the raw-line skip offset stored in metadata.
func scanRetainedMessageLines(path string) (int, []int, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return 0, []int{}, nil
	}
	if err != nil {
		return 0, nil, fmt.Errorf("memory: open jsonl: %w", err)
	}
	defer func() { _ = f.Close() }()

	rawCount := 0
	retained := make([]int, 0)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		rawCount++

		var msg providers.Message
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if messageutil.IsTransientAssistantThoughtMessage(msg) {
			continue
		}
		retained = append(retained, rawCount)
	}
	if err := scanner.Err(); err != nil {
		return 0, nil, err
	}
	return rawCount, retained, nil
}

func (s *JSONLStore) AddMessage(
	ctx context.Context, sessionKey, role, content string,
) error {
	return s.addMsg(ctx, sessionKey, providers.Message{
		Role:    role,
		Content: content,
	})
}

func (s *JSONLStore) AddFullMessage(
	ctx context.Context, sessionKey string, msg providers.Message,
) error {
	return s.addMsg(ctx, sessionKey, msg)
}

// addMsg is the shared implementation for AddMessage and AddFullMessage.
func (s *JSONLStore) addMsg(ctx context.Context, sessionKey string, msg providers.Message) error {
	if err := contextCause(ctx); err != nil {
		return err
	}
	if messageutil.IsTransientAssistantThoughtMessage(msg) {
		return nil
	}

	l := s.sessionLock(sessionKey)
	l.Lock()
	defer l.Unlock()
	if err := contextCause(ctx); err != nil {
		return err
	}

	now := time.Now()
	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return err
	}
	if recoveryErr := s.reconcileDirtyHistory(sessionKey, &meta); recoveryErr != nil {
		return recoveryErr
	}
	if meta.Count == 0 && meta.CreatedAt.IsZero() {
		meta.CreatedAt = now
	}
	meta.UpdatedAt = now
	if faultErr := s.injectJournalFault(jsonlJournalStageFlush); faultErr != nil {
		return fmt.Errorf("memory: flush journal metadata: %w", faultErr)
	}
	if mutationErr := s.beginHistoryMutation(sessionKey, &meta, true); mutationErr != nil {
		return mutationErr
	}

	if msg.CreatedAt == nil {
		msg.CreatedAt = &now
	}

	// Append the message as a single JSON line.
	line, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("memory: marshal message: %w", err)
	}
	line = append(line, '\n')

	jsonlPath := s.jsonlPath(sessionKey)
	f, err := os.OpenFile(jsonlPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0o644)
	created := err == nil
	if errors.Is(err, os.ErrExist) {
		f, err = os.OpenFile(jsonlPath, os.O_WRONLY|os.O_APPEND, 0o644)
	}
	if err != nil {
		return fmt.Errorf("memory: open jsonl for append: %w", err)
	}
	if faultErr := s.injectJournalFault(jsonlJournalStageAppend); faultErr != nil {
		_ = f.Close()
		return fmt.Errorf("memory: append message: %w", faultErr)
	}
	writeAppend := s.appendWrite
	if writeAppend == nil {
		writeAppend = func(file *os.File, data []byte) (int, error) { return file.Write(data) }
	}
	written, writeErr := writeAppend(f, line)
	if writeErr == nil && written != len(line) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		_ = f.Close()
		if written > 0 {
			return &IndeterminateAppendError{Err: fmt.Errorf("memory: append message: %w", writeErr)}
		}
		return fmt.Errorf("memory: append message: %w", writeErr)
	}
	// Flush to physical storage before closing. This matches the
	// durability guarantee of writeMeta and rewriteJSONL (which use
	// WriteFileAtomic with fsync). Without Sync, a power loss could
	// leave the append in the kernel page cache only — lost on reboot.
	if faultErr := s.injectJournalFault(jsonlJournalStageFsync); faultErr != nil {
		_ = f.Close()
		return &IndeterminateAppendError{Err: fmt.Errorf("memory: sync jsonl: %w", faultErr)}
	}
	if syncErr := f.Sync(); syncErr != nil {
		_ = f.Close()
		return &IndeterminateAppendError{Err: fmt.Errorf("memory: sync jsonl: %w", syncErr)}
	}
	if created {
		if faultErr := s.injectJournalFault(jsonlJournalStageDir); faultErr != nil {
			_ = f.Close()
			return &IndeterminateAppendError{
				Err: fmt.Errorf("memory: sync jsonl directory: %w", faultErr),
			}
		}
		if syncErr := fileutil.SyncDirectory(s.dir); syncErr != nil {
			_ = f.Close()
			return &IndeterminateAppendError{
				Err: fmt.Errorf("memory: sync jsonl directory: %w", syncErr),
			}
		}
	}
	if closeErr := f.Close(); closeErr != nil {
		return &CommittedAppendError{Err: fmt.Errorf("memory: close jsonl: %w", closeErr)}
	}

	meta.Count++
	meta.UpdatedAt = now
	if faultErr := s.injectJournalFault(jsonlJournalStageRename); faultErr != nil {
		return &CommittedAppendError{Err: fmt.Errorf("memory: commit journal metadata: %w", faultErr)}
	}

	if finishErr := s.finishHistoryMutation(sessionKey, &meta); finishErr != nil {
		return &CommittedAppendError{Err: finishErr}
	}
	return nil
}

func (s *JSONLStore) GetHistory(
	_ context.Context, sessionKey string,
) ([]providers.Message, error) {
	l := s.sessionLock(sessionKey)
	l.Lock()
	defer l.Unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return nil, err
	}

	// Pass meta.Skip so readMessages skips those lines without
	// unmarshaling them — avoids wasted CPU on truncated messages.
	msgs, err := readMessages(s.jsonlPath(sessionKey), meta.Skip)
	if err != nil {
		return nil, err
	}

	return msgs, nil
}

// GetHistoryPage scans canonical JSONL under the session lock but retains and
// returns only the requested visible-message window.
func (s *JSONLStore) GetHistoryPage(
	ctx context.Context,
	sessionKey string,
	request HistoryPageRequest,
) (HistoryPage, error) {
	if request.Limit <= 0 || request.Limit > maxHistoryPageMessages {
		return HistoryPage{}, fmt.Errorf(
			"memory: history page limit must be within 1..%d",
			maxHistoryPageMessages,
		)
	}
	if err := contextCause(ctx); err != nil {
		return HistoryPage{}, err
	}
	l := s.sessionLock(sessionKey)
	l.Lock()
	defer l.Unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return HistoryPage{}, err
	}
	if recoveryErr := s.reconcileDirtyHistory(sessionKey, &meta); recoveryErr != nil {
		return HistoryPage{}, recoveryErr
	}
	path := s.jsonlPath(sessionKey)
	total, _, err := scanVisibleHistory(ctx, path, meta.Skip, 0, 0, nil, 0)
	if err != nil {
		return HistoryPage{}, err
	}
	cursorTotal := total
	if request.Cursor != nil {
		cursorTotal = request.Cursor.Total
		if cursorTotal < 0 || cursorTotal > total {
			return HistoryPage{}, fmt.Errorf("%w: canonical prefix length changed", ErrHistoryCursorStale)
		}
	}
	end := request.Before
	if end < 0 || end > cursorTotal {
		end = cursorTotal
	}
	start := max(0, end-request.Limit)
	messages := make([]providers.Message, 0, end-start)
	_, cursor, err := scanVisibleHistory(ctx, path, meta.Skip, start, end, &messages, cursorTotal)
	if err != nil {
		return HistoryPage{}, err
	}
	if err := validateHistoryCursor(request.Cursor, cursor); err != nil {
		return HistoryPage{}, err
	}
	var size, modTimeNS int64
	if info, statErr := os.Stat(path); statErr == nil {
		size = info.Size()
		modTimeNS = info.ModTime().UnixNano()
	} else if !os.IsNotExist(statErr) {
		return HistoryPage{}, statErr
	}
	return HistoryPage{
		Messages: messages,
		Revision: HistoryRevision{
			Revision:  meta.HistoryRevision,
			Count:     meta.Count,
			Skip:      meta.Skip,
			Dirty:     meta.HistoryDirty,
			FileSize:  size,
			ModTimeNS: modTimeNS,
		},
		Cursor:   cursor,
		Start:    start,
		End:      end,
		Total:    cursorTotal,
		HasOlder: start > 0,
		HasNewer: end < cursorTotal,
	}, nil
}

func scanVisibleHistory(
	ctx context.Context,
	path string,
	skip int,
	start int,
	end int,
	destination *[]providers.Message,
	cursorTotal int,
) (int, HistoryCursor, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return 0, newHistoryCursorDigest().cursor(), nil
	}
	if err != nil {
		return 0, HistoryCursor{}, fmt.Errorf("memory: open history page: %w", err)
	}
	defer func() { _ = file.Close() }()

	rawIndex := 0
	visibleIndex := 0
	digest := newHistoryCursorDigest()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)
	for scanner.Scan() {
		if err := contextCause(ctx); err != nil {
			return 0, HistoryCursor{}, err
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		rawIndex++
		if rawIndex <= skip {
			continue
		}
		var message providers.Message
		if err := json.Unmarshal(
			line,
			&message,
		); err != nil ||
			messageutil.IsTransientAssistantThoughtMessage(message) {
			continue
		}
		if visibleIndex < cursorTotal {
			if err := digest.add(message); err != nil {
				return 0, HistoryCursor{}, err
			}
		}
		if destination != nil && visibleIndex >= start && visibleIndex < end {
			*destination = append(*destination, message)
		}
		visibleIndex++
		if destination != nil && visibleIndex >= max(end, cursorTotal) {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, HistoryCursor{}, fmt.Errorf("memory: scan history page: %w", err)
	}
	return visibleIndex, digest.cursor(), nil
}

func (s *JSONLStore) GetSummary(
	_ context.Context, sessionKey string,
) (string, error) {
	l := s.sessionLock(sessionKey)
	l.Lock()
	defer l.Unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return "", err
	}
	return meta.Summary, nil
}

func (s *JSONLStore) SetSummary(
	_ context.Context, sessionKey, summary string,
) error {
	l := s.sessionLock(sessionKey)
	l.Lock()
	defer l.Unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return err
	}
	now := time.Now()
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = now
	}
	meta.Summary = summary
	meta.UpdatedAt = now

	return s.writeMeta(sessionKey, meta)
}

func (s *JSONLStore) TruncateHistory(
	_ context.Context, sessionKey string, keepLast int,
) error {
	l := s.sessionLock(sessionKey)
	l.Lock()
	defer l.Unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return err
	}
	if recoveryErr := s.reconcileDirtyHistory(sessionKey, &meta); recoveryErr != nil {
		return recoveryErr
	}

	rawCount, retainedRawLines, scanErr := scanRetainedMessageLines(s.jsonlPath(sessionKey))
	if scanErr != nil {
		return scanErr
	}
	meta.Count = rawCount
	if meta.Skip > meta.Count {
		meta.Skip = meta.Count
	}

	activeStart := sort.Search(len(retainedRawLines), func(i int) bool {
		return retainedRawLines[i] > meta.Skip
	})
	activeRetainedCount := len(retainedRawLines) - activeStart

	switch {
	case keepLast <= 0 || activeRetainedCount == 0:
		meta.Skip = meta.Count
	case keepLast < activeRetainedCount:
		activeRawLines := retainedRawLines[activeStart:]
		meta.Skip = activeRawLines[activeRetainedCount-keepLast-1]
	}
	meta.UpdatedAt = time.Now()
	bumpHistoryRevision(&meta)
	meta.HistoryDirty = false

	return s.writeMeta(sessionKey, meta)
}

func (s *JSONLStore) SetHistory(
	ctx context.Context,
	sessionKey string,
	history []providers.Message,
) error {
	if err := contextCause(ctx); err != nil {
		return err
	}
	history = messageutil.FilterInvalidHistoryMessages(history)

	l := s.sessionLock(sessionKey)
	l.Lock()
	defer l.Unlock()
	if err := contextCause(ctx); err != nil {
		return err
	}

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return err
	}
	if recoveryErr := s.reconcileDirtyHistory(sessionKey, &meta); recoveryErr != nil {
		return recoveryErr
	}
	return s.setHistoryLocked(sessionKey, history, &meta)
}

// MutateHistory applies mutate to the latest visible history while holding the
// same per-session lock used by appends, truncation, replacement, and compaction.
func (s *JSONLStore) MutateHistory(
	ctx context.Context,
	sessionKey string,
	mutate func([]providers.Message) ([]providers.Message, bool, error),
) (bool, error) {
	if err := contextCause(ctx); err != nil {
		return false, err
	}
	if mutate == nil {
		return false, fmt.Errorf("memory: history mutation callback is required")
	}
	l := s.sessionLock(sessionKey)
	l.Lock()
	defer l.Unlock()
	if err := contextCause(ctx); err != nil {
		return false, err
	}
	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return false, err
	}
	if recoveryErr := s.reconcileDirtyHistory(sessionKey, &meta); recoveryErr != nil {
		return false, recoveryErr
	}
	history, err := readMessages(s.jsonlPath(sessionKey), meta.Skip)
	if err != nil {
		return false, err
	}
	next, changed, err := mutate(append([]providers.Message(nil), history...))
	if err != nil || !changed {
		return false, err
	}
	if err := contextCause(ctx); err != nil {
		return false, err
	}
	next = messageutil.FilterInvalidHistoryMessages(next)
	if err := s.setHistoryLocked(sessionKey, next, &meta); err != nil {
		return false, err
	}
	return true, nil
}

func (s *JSONLStore) setHistoryLocked(
	sessionKey string,
	history []providers.Message,
	meta *SessionMeta,
) error {
	now := time.Now()
	for i := range history {
		if history[i].CreatedAt == nil {
			history[i].CreatedAt = &now
		}
	}
	encodedHistory, encodeErr := encodeJSONL(history)
	if encodeErr != nil {
		return encodeErr
	}
	previousCount, previousSkip := meta.Count, meta.Skip
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = now
	}
	meta.Skip = 0
	meta.Count = len(history)
	meta.UpdatedAt = now
	meta.HistoryHasPrevious = true
	meta.HistoryPreviousCount = previousCount
	meta.HistoryPreviousSkip = previousSkip
	meta.HistoryTargetDigest = digestJSONL(encodedHistory)
	if err := s.beginHistoryMutation(sessionKey, meta, true); err != nil {
		return err
	}

	// A dirty marker written before replacement forces derived stores to
	// rebuild if the process exits between the metadata and JSONL writes.
	if err := s.rewriteJSONLBytes(sessionKey, encodedHistory); err != nil {
		return err
	}
	return s.finishHistoryMutation(sessionKey, meta)
}

// Compact physically rewrites the JSONL file, dropping all logically
// skipped lines. This reclaims disk space that accumulates after
// repeated TruncateHistory calls.
//
// It is safe to call at any time; if there is nothing to compact
// (skip == 0) the method returns immediately.
func (s *JSONLStore) Compact(
	_ context.Context, sessionKey string,
) error {
	l := s.sessionLock(sessionKey)
	l.Lock()
	defer l.Unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return err
	}
	if recoveryErr := s.reconcileDirtyHistory(sessionKey, &meta); recoveryErr != nil {
		return recoveryErr
	}
	if meta.Skip == 0 {
		return nil
	}

	// Read only the active messages, skipping truncated lines
	// without unmarshaling them.
	active, err := readMessages(s.jsonlPath(sessionKey), meta.Skip)
	if err != nil {
		return err
	}

	previousCount, previousSkip := meta.Count, meta.Skip
	meta.Skip = 0
	meta.Count = len(active)
	meta.UpdatedAt = time.Now()
	meta.HistoryHasPrevious = true
	meta.HistoryPreviousCount = previousCount
	meta.HistoryPreviousSkip = previousSkip
	encodedActive, encodeErr := encodeJSONL(active)
	if encodeErr != nil {
		return encodeErr
	}
	meta.HistoryTargetDigest = digestJSONL(encodedActive)
	// Compact preserves the visible history, so it uses a dirty marker but
	// does not advance the logical history revision.
	if err := s.beginHistoryMutation(sessionKey, &meta, false); err != nil {
		return err
	}

	if err := s.rewriteJSONLBytes(sessionKey, encodedActive); err != nil {
		return err
	}
	return s.finishHistoryMutation(sessionKey, &meta)
}

func encodeJSONL(msgs []providers.Message) ([]byte, error) {
	msgs = messageutil.FilterInvalidHistoryMessages(msgs)

	var buf bytes.Buffer
	for i, msg := range msgs {
		line, err := json.Marshal(msg)
		if err != nil {
			return nil, fmt.Errorf("memory: marshal message %d: %w", i, err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

func digestJSONL(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// rewriteJSONLBytes atomically replaces the JSONL file using the project's
// standard WriteFileAtomic (temp + fsync + rename).
func (s *JSONLStore) rewriteJSONLBytes(sessionKey string, data []byte) error {
	return fileutil.WriteFileAtomic(s.jsonlPath(sessionKey), data, 0o644)
}

// ListSessions returns all known session keys by reading .meta.json files.
func (s *JSONLStore) ListSessions() []string {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	var keys []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".meta.json") {
			continue
		}
		// Read the meta file to get the original key
		data, err := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			continue
		}
		var meta SessionMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}
		if meta.Key != "" {
			keys = append(keys, meta.Key)
		}
	}
	return keys
}

func (s *JSONLStore) Close() error {
	return nil
}
