// Package thread defines durable local-coding thread identity and metadata.
package thread

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

const (
	// SchemaVersion is the current on-disk CodingThread metadata schema.
	SchemaVersion = 1
	// MaxMetadataBytes bounds one descriptor independently of transcript size.
	MaxMetadataBytes = 32 * 1024

	metadataFileName  = "thread.meta.json"
	titleMaxBytes     = 80
	previewMaxBytes   = 240
	selectionMaxBytes = 256
)

// Status is the durable lifecycle state of a coding thread.
type Status string

const (
	StatusActive   Status = "active"
	StatusArchived Status = "archived"
)

// Compaction records the latest durable compaction checkpoint, when present.
type Compaction struct {
	At       time.Time `json:"at"`
	Revision uint64    `json:"revision"`
}

// Metadata is the versioned, transcript-independent coding-thread descriptor.
// It is intentionally small enough to support catalog reads without loading
// canonical JSONL history.
type Metadata struct {
	SchemaVersion int             `json:"schema_version"`
	ThreadID      string          `json:"thread_id"`
	SessionKey    string          `json:"session_key"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	Title         string          `json:"title"`
	Preview       string          `json:"preview"`
	Status        Status          `json:"status"`
	Project       ProjectIdentity `json:"project"`
	Model         string          `json:"model,omitempty"`
	Provider      string          `json:"provider,omitempty"`
	ParentThread  string          `json:"parent_thread_id,omitempty"`
	Compaction    *Compaction     `json:"last_compaction,omitempty"`
}

// NewMetadata creates a validated descriptor from the first accepted user
// request. Display text is derived locally without an additional model call.
func NewMetadata(
	threadID string,
	project ProjectIdentity,
	firstRequest string,
	now time.Time,
) (Metadata, error) {
	title, preview, err := DisplayFromRequest(firstRequest)
	if err != nil {
		return Metadata{}, err
	}
	metadata := Metadata{
		SchemaVersion: SchemaVersion,
		ThreadID:      threadID,
		SessionKey:    SessionKey(threadID),
		CreatedAt:     now.UTC(),
		UpdatedAt:     now.UTC(),
		Title:         title,
		Preview:       preview,
		Status:        StatusActive,
		Project:       project,
	}
	if err := metadata.Validate(); err != nil {
		return Metadata{}, err
	}
	return metadata, nil
}

// NewThreadID returns a new opaque coding-thread UUID.
func NewThreadID() string {
	return uuid.NewString()
}

// SessionKey returns the canonical transcript session key for a thread.
func SessionKey(threadID string) string {
	return "coding:" + threadID
}

// DisplayFromRequest derives bounded picker text from an accepted request.
func DisplayFromRequest(request string) (string, string, error) {
	normalized := strings.Join(strings.Fields(request), " ")
	if normalized == "" {
		return "", "", fmt.Errorf("coding thread: first request is required")
	}
	return truncateUTF8(normalized, titleMaxBytes), truncateUTF8(normalized, previewMaxBytes), nil
}

// Validate checks the complete persisted metadata contract.
func (m Metadata) Validate() error {
	if m.SchemaVersion != SchemaVersion {
		return fmt.Errorf("coding thread: unsupported metadata schema %d", m.SchemaVersion)
	}
	parsedID, err := uuid.Parse(m.ThreadID)
	if err != nil || parsedID.String() != m.ThreadID {
		return fmt.Errorf("coding thread: thread ID must be a canonical UUID")
	}
	if m.SessionKey != SessionKey(m.ThreadID) {
		return fmt.Errorf("coding thread: session key does not match thread ID")
	}
	if m.CreatedAt.IsZero() || m.UpdatedAt.IsZero() {
		return fmt.Errorf("coding thread: created and updated timestamps are required")
	}
	if m.UpdatedAt.Before(m.CreatedAt) {
		return fmt.Errorf("coding thread: updated timestamp precedes creation")
	}
	if err := validateDisplay("title", m.Title, titleMaxBytes); err != nil {
		return err
	}
	if err := validateDisplay("preview", m.Preview, previewMaxBytes); err != nil {
		return err
	}
	switch m.Status {
	case StatusActive, StatusArchived:
	default:
		return fmt.Errorf("coding thread: unsupported status %q", m.Status)
	}
	if err := m.Project.Validate(); err != nil {
		return fmt.Errorf("coding thread: project identity: %w", err)
	}
	if err := validateOptionalText("model", m.Model, selectionMaxBytes); err != nil {
		return err
	}
	if err := validateOptionalText("provider", m.Provider, selectionMaxBytes); err != nil {
		return err
	}
	if m.ParentThread != "" {
		parentID, parentErr := uuid.Parse(m.ParentThread)
		if parentErr != nil || parentID.String() != m.ParentThread || m.ParentThread == m.ThreadID {
			return fmt.Errorf("coding thread: parent thread ID must be a distinct canonical UUID")
		}
	}
	if m.Compaction != nil {
		if m.Compaction.At.IsZero() || m.Compaction.Revision == 0 {
			return fmt.Errorf("coding thread: compaction requires timestamp and revision")
		}
		if m.Compaction.At.Before(m.CreatedAt) || m.Compaction.At.After(m.UpdatedAt) {
			return fmt.Errorf("coding thread: compaction timestamp is outside thread lifetime")
		}
	}
	return nil
}

func validateDisplay(name, value string, maxBytes int) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("coding thread: %s is required", name)
	}
	if !utf8.ValidString(value) || len(value) > maxBytes {
		return fmt.Errorf("coding thread: %s must be valid UTF-8 within %d bytes", name, maxBytes)
	}
	return nil
}

func validateOptionalText(name, value string, maxBytes int) error {
	if value == "" {
		return nil
	}
	if value != strings.TrimSpace(value) || !utf8.ValidString(value) || len(value) > maxBytes {
		return fmt.Errorf("coding thread: %s must be trimmed valid UTF-8 within %d bytes", name, maxBytes)
	}
	return nil
}

func truncateUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	const suffix = "…"
	limit := maxBytes - len(suffix)
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return strings.TrimSpace(value[:limit]) + suffix
}

// Store atomically persists direct-addressable thread metadata below one
// external coding state root.
type Store struct {
	root         string
	durableRoot  string
	mkdirDurable func(string, string, os.FileMode) error
	writeAtomic  func(string, []byte, os.FileMode) error
}

// NewStore creates a side-effect-free metadata store descriptor.
func NewStore(root string) (*Store, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("coding thread store: root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("coding thread store: resolve root: %w", err)
	}
	resolved, durableRoot, err := resolveStoreRoot(absolute)
	if err != nil {
		return nil, fmt.Errorf("coding thread store: resolve root: %w", err)
	}
	return &Store{
		root:         resolved,
		durableRoot:  durableRoot,
		mkdirDurable: fileutil.MkdirAllDurable,
		writeAtomic:  fileutil.WriteFileAtomic,
	}, nil
}

// Root returns the external coding state root.
func (s *Store) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

// ThreadRoot returns the owner-scoped state root consumed by RuntimeLayout.
func (s *Store) ThreadRoot(threadID string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("coding thread store is nil")
	}
	if err := validateThreadID(threadID); err != nil {
		return "", err
	}
	return filepath.Join(s.root, "threads", threadID), nil
}

// ProvisionThread durably creates a private thread directory without
// publishing catalog-visible metadata.
func (s *Store) ProvisionThread(threadID string) error {
	if s == nil {
		return fmt.Errorf("coding thread store is nil")
	}
	threadRoot, err := s.ThreadRoot(threadID)
	if err != nil {
		return err
	}
	relativeRoot, err := filepath.Rel(s.durableRoot, threadRoot)
	if err != nil {
		return fmt.Errorf("coding thread store: resolve durable thread directory: %w", err)
	}
	if !filepath.IsLocal(relativeRoot) {
		return fmt.Errorf("coding thread store: durable thread directory escapes store root")
	}
	if err := s.mkdirDurable(s.durableRoot, relativeRoot, 0o700); err != nil {
		return fmt.Errorf("coding thread store: create durable thread directory: %w", err)
	}
	return nil
}

// Save atomically replaces one descriptor. A post-rename durability error is
// returned as fileutil.CommittedWriteError and must not be blindly retried.
func (s *Store) Save(metadata Metadata) error {
	if s == nil {
		return fmt.Errorf("coding thread store is nil")
	}
	if err := metadata.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("coding thread store: encode metadata: %w", err)
	}
	if len(data)+1 > MaxMetadataBytes {
		return fmt.Errorf("coding thread store: encoded metadata exceeds %d bytes", MaxMetadataBytes)
	}
	path, err := s.metadataPath(metadata.ThreadID)
	if err != nil {
		return err
	}
	if err := s.ProvisionThread(metadata.ThreadID); err != nil {
		return err
	}
	if err := s.writeAtomic(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("coding thread store: save %q: %w", metadata.ThreadID, err)
	}
	return nil
}

// Load reads and validates one descriptor without opening its transcript.
func (s *Store) Load(threadID string) (Metadata, error) {
	path, err := s.metadataPath(threadID)
	if err != nil {
		return Metadata{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return Metadata{}, fmt.Errorf("coding thread store: read %q: %w", threadID, err)
	}
	return loadMetadataFile(threadID, file)
}

func loadMetadataFile(threadID string, file *os.File) (Metadata, error) {
	data, readErr := io.ReadAll(io.LimitReader(file, MaxMetadataBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return Metadata{}, fmt.Errorf("coding thread store: read %q: %w", threadID, readErr)
	}
	if closeErr != nil {
		return Metadata{}, fmt.Errorf("coding thread store: close %q: %w", threadID, closeErr)
	}
	if len(data) > MaxMetadataBytes {
		return Metadata{}, fmt.Errorf(
			"coding thread store: descriptor %q exceeds %d bytes",
			threadID,
			MaxMetadataBytes,
		)
	}
	var metadata Metadata
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		return Metadata{}, fmt.Errorf("coding thread store: decode %q: %w", threadID, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Metadata{}, fmt.Errorf("coding thread store: decode %q: trailing JSON content", threadID)
	}
	if metadata.ThreadID != threadID {
		return Metadata{}, fmt.Errorf(
			"coding thread store: descriptor ID %q does not match path %q",
			metadata.ThreadID,
			threadID,
		)
	}
	if err := metadata.Validate(); err != nil {
		return Metadata{}, fmt.Errorf("coding thread store: validate %q: %w", threadID, err)
	}
	return metadata, nil
}

func (s *Store) metadataPath(threadID string) (string, error) {
	root, err := s.ThreadRoot(threadID)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, metadataFileName), nil
}

func validateThreadID(threadID string) error {
	parsed, err := uuid.Parse(threadID)
	if err != nil || parsed.String() != threadID {
		return errors.New("coding thread store: thread ID must be a canonical UUID")
	}
	return nil
}

func resolveStoreRoot(root string) (string, string, error) {
	cleaned := filepath.Clean(root)
	for current := cleaned; ; current = filepath.Dir(current) {
		_, err := os.Lstat(current)
		if err == nil {
			resolved, resolveErr := filepath.EvalSymlinks(current)
			if resolveErr != nil {
				return "", "", resolveErr
			}
			resolvedInfo, statErr := os.Stat(resolved)
			if statErr != nil {
				return "", "", statErr
			}
			if !resolvedInfo.IsDir() {
				return "", "", fmt.Errorf("root ancestor is not a directory")
			}
			relative, relErr := filepath.Rel(current, cleaned)
			if relErr != nil {
				return "", "", relErr
			}
			return filepath.Clean(filepath.Join(resolved, relative)), filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(err) {
			return "", "", err
		}
		if filepath.Dir(current) == current {
			return "", "", fmt.Errorf("no existing root ancestor")
		}
	}
}
