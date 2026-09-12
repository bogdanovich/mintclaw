// Package worktree owns isolated Git execution roots for asynchronous coding
// tasks. It deliberately does not own coding transcripts, worker processes,
// remote dispatch, or pull-request publication.
package worktree

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
)

const (
	SchemaVersion       = 1
	MaxRecordBytes      = 128 << 10
	MaxOwnerRecordBytes = 8 << 10
	DefaultBranchPrefix = "mintclaw"

	maxIDBytes     = 128
	maxPathBytes   = 32 << 10
	maxBranchBytes = 512
)

var (
	ErrAllocationConflict  = errors.New("coding worktree allocation identity conflict")
	ErrAllocationUncertain = errors.New("coding worktree allocation is uncertain")
	ErrOwnerBusy           = errors.New("coding worktree owner lease busy")

	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
)

// State is the durable lifecycle of one isolated execution root.
type State string

const (
	StateReserved       State = "reserved"
	StatePreparing      State = "preparing"
	StateReady          State = "ready"
	StateRetained       State = "retained"
	StateCleanupPending State = "cleanup_pending"
	StateReleased       State = "released"
	StateUncertain      State = "uncertain"
)

func (state State) valid() bool {
	switch state {
	case StateReserved, StatePreparing, StateReady, StateRetained,
		StateCleanupPending, StateReleased, StateUncertain:
		return true
	default:
		return false
	}
}

// Request contains only supervisor-selected allocation authority. The model
// never supplies a path, branch, repository URL, or moving ref.
type Request struct {
	TaskID           string
	TaskGenerationID string
	ThreadID         string
	Source           thread.ProjectIdentity
	BaseRevision     string
}

func (request Request) validate() error {
	if !validIdentifier(request.TaskID) || !validIdentifier(request.TaskGenerationID) {
		return fmt.Errorf("coding worktree: task identities are invalid")
	}
	parsedThreadID, err := uuid.Parse(request.ThreadID)
	if err != nil || parsedThreadID.String() != request.ThreadID {
		return fmt.Errorf("coding worktree: thread ID must be a canonical UUID")
	}
	if err := request.Source.Validate(); err != nil {
		return fmt.Errorf("coding worktree: source project: %w", err)
	}
	if request.Source.Kind != thread.ProjectKindGitWorktree || request.Source.GitHead == "" {
		return fmt.Errorf("coding worktree: source must be a Git worktree with a committed HEAD")
	}
	if !validObjectID(request.BaseRevision) {
		return fmt.Errorf("coding worktree: base revision must be a full object ID")
	}
	return nil
}

// Allocation is the bounded durable identity and latest reconciled state for
// one MintClaw-owned linked worktree.
type Allocation struct {
	SchemaVersion         int                     `json:"schema_version"`
	WorktreeID            string                  `json:"worktree_id"`
	TaskID                string                  `json:"task_id"`
	TaskGenerationID      string                  `json:"task_generation_id"`
	ThreadID              string                  `json:"thread_id"`
	Source                thread.ProjectIdentity  `json:"source"`
	BaseRevision          string                  `json:"base_revision"`
	WorktreeParent        string                  `json:"worktree_parent"`
	ExecutionRoot         string                  `json:"execution_root"`
	ExecutionRootIdentity string                  `json:"execution_root_identity"`
	Branch                string                  `json:"branch"`
	Execution             *thread.ProjectIdentity `json:"execution,omitempty"`
	SourceDirty           bool                    `json:"source_dirty,omitempty"`
	SourceStatusComplete  bool                    `json:"source_status_complete"`
	State                 State                   `json:"state"`
	CreatedAt             time.Time               `json:"created_at"`
	UpdatedAt             time.Time               `json:"updated_at"`
}

// Validate checks a record without consulting mutable filesystem state.
func (allocation Allocation) Validate() error {
	if allocation.SchemaVersion != SchemaVersion || !allocation.State.valid() {
		return fmt.Errorf("coding worktree: unsupported allocation schema or state")
	}
	request := Request{
		TaskID:           allocation.TaskID,
		TaskGenerationID: allocation.TaskGenerationID,
		ThreadID:         allocation.ThreadID,
		Source:           allocation.Source,
		BaseRevision:     allocation.BaseRevision,
	}
	if err := request.validate(); err != nil {
		return err
	}
	if allocation.WorktreeID != IDForThread(allocation.ThreadID) {
		return fmt.Errorf("coding worktree: worktree ID does not match thread ID")
	}
	if !validPath(allocation.WorktreeParent) || !validPath(allocation.ExecutionRoot) {
		return fmt.Errorf("coding worktree: invalid durable path")
	}
	if filepath.Dir(allocation.ExecutionRoot) != allocation.WorktreeParent ||
		filepath.Base(allocation.ExecutionRoot) != directoryName(allocation.WorktreeID) {
		return fmt.Errorf("coding worktree: execution root is not the derived parent child")
	}
	if allocation.ExecutionRootIdentity != RootIdentity(allocation.ExecutionRoot) {
		return fmt.Errorf("coding worktree: execution root identity does not match path")
	}
	if allocation.Branch != branchName(DefaultBranchPrefix, allocation.WorktreeID) ||
		len(allocation.Branch) > maxBranchBytes {
		return fmt.Errorf("coding worktree: branch is not the derived branch")
	}
	if allocation.CreatedAt.IsZero() || allocation.UpdatedAt.IsZero() ||
		allocation.UpdatedAt.Before(allocation.CreatedAt) {
		return fmt.Errorf("coding worktree: invalid lifecycle timestamps")
	}
	if allocation.Execution != nil {
		if err := allocation.Execution.Validate(); err != nil {
			return fmt.Errorf("coding worktree: execution project: %w", err)
		}
		relativeCWD, err := filepath.Rel(allocation.Source.ProjectRoot, allocation.Source.InvocationCWD)
		if err != nil || (relativeCWD != "." && !filepath.IsLocal(relativeCWD)) {
			return fmt.Errorf("coding worktree: source invocation cwd is not local to its project")
		}
		executionCWD := filepath.Join(allocation.ExecutionRoot, relativeCWD)
		if allocation.Execution.ProjectRoot != allocation.ExecutionRoot ||
			allocation.Execution.InvocationCWD != executionCWD ||
			allocation.Execution.GitCommonDir != allocation.Source.GitCommonDir ||
			allocation.Execution.GitOrigin != allocation.Source.GitOrigin ||
			allocation.Execution.GitDir == allocation.Source.GitDir ||
			allocation.Execution.GitBranch != allocation.Branch {
			return fmt.Errorf("coding worktree: execution project does not match allocation")
		}
	}
	if allocation.State == StateReady && allocation.Execution == nil {
		return fmt.Errorf("coding worktree: ready allocation requires execution identity")
	}
	return nil
}

func (allocation Allocation) matches(request Request, parent string) bool {
	return allocation.WorktreeID == IDForThread(request.ThreadID) &&
		allocation.TaskID == request.TaskID &&
		allocation.TaskGenerationID == request.TaskGenerationID &&
		allocation.ThreadID == request.ThreadID &&
		allocation.Source == request.Source &&
		allocation.BaseRevision == strings.ToLower(request.BaseRevision) &&
		allocation.WorktreeParent == parent
}

// IDForThread derives a filesystem-safe opaque allocation identity from the
// already opaque native coding thread identity.
func IDForThread(threadID string) string {
	return "wt-" + strings.ReplaceAll(strings.ToLower(threadID), "-", "")
}

// RootIdentity is a diagnostic/path-binding digest, not an authorization
// token. Authorization comes from the held owner lease.
func RootIdentity(root string) string {
	digest := sha256.Sum256([]byte(filepath.Clean(root)))
	return hex.EncodeToString(digest[:])
}

func directoryName(worktreeID string) string {
	return "mintclaw-" + strings.TrimPrefix(worktreeID, "wt-")
}

func branchName(prefix, worktreeID string) string {
	return prefix + "/" + strings.TrimPrefix(worktreeID, "wt-")
}

func validIdentifier(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && utf8.ValidString(value) &&
		len(value) <= maxIDBytes && identifierPattern.MatchString(value)
}

func validObjectID(value string) bool {
	if value != strings.ToLower(value) || (len(value) != 40 && len(value) != 64) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validPath(path string) bool {
	return path != "" && path == strings.TrimSpace(path) && utf8.ValidString(path) &&
		len(path) <= maxPathBytes && !strings.ContainsFunc(path, unicode.IsControl) &&
		filepath.IsAbs(path) && filepath.Clean(path) == path
}
