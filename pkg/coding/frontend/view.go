// Package frontend defines the bounded in-process presentation state consumed
// by coding frontends. It is deliberately separate from both the agent runtime
// and any terminal framework.
package frontend

import (
	"context"
	"errors"
	"time"

	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
)

var (
	ErrTranscriptPagingUnsupported = errors.New("coding transcript paging is unsupported")
	ErrTranscriptHistoryChanged    = errors.New("coding transcript history changed after opening")
	ErrWorkspaceRefreshUnsupported = errors.New("coding workspace refresh is unsupported")
	ErrCommandUnsupported          = errors.New("coding controller command is not supported")
)

type Activity string

const (
	ActivityIdle         Activity = "idle"
	ActivityRunning      Activity = "running"
	ActivityInterrupting Activity = "interrupting"
	ActivityCompacting   Activity = "compacting"
	ActivityWaitingInput Activity = "waiting_for_input"
	ActivityFailed       Activity = "failed"
)

type TurnOutcome string

const (
	TurnOutcomeCompleted   TurnOutcome = "completed"
	TurnOutcomeSuspended   TurnOutcome = "suspended"
	TurnOutcomeFailed      TurnOutcome = "failed"
	TurnOutcomeInterrupted TurnOutcome = "interrupted"
)

// LastTurnOutcome is the bounded typed terminal state retained in the current
// presentation view.
type LastTurnOutcome struct {
	TurnID  string      `json:"turn_id"`
	Outcome TurnOutcome `json:"outcome"`
}

type EntryKind string

const (
	EntryUser      EntryKind = "user"
	EntryAssistant EntryKind = "assistant"
	EntryReasoning EntryKind = "reasoning"
	EntryTool      EntryKind = "tool"
	EntryWarning   EntryKind = "warning"
	EntryError     EntryKind = "error"
)

type TranscriptEntry struct {
	ID        string    `json:"id"`
	TurnID    string    `json:"turn_id"`
	Kind      EntryKind `json:"kind"`
	Text      string    `json:"text"`
	Complete  bool      `json:"complete"`
	Truncated bool      `json:"truncated,omitempty"`
}

// ThreadMetadata is the bounded display metadata needed by a frontend. It
// deliberately excludes catalog and storage implementation details.
type ThreadMetadata struct {
	Title       string    `json:"title,omitempty"`
	Preview     string    `json:"preview,omitempty"`
	ProjectRoot string    `json:"project_root,omitempty"`
	CWD         string    `json:"cwd,omitempty"`
	Model       string    `json:"model,omitempty"`
	Provider    string    `json:"provider,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
}

// WriteAudit is a verified write-side effect reported by a tool. Descriptive
// model output is never promoted into this structure.
type WriteAudit struct {
	Kind    string `json:"kind"`
	Target  string `json:"target"`
	Action  string `json:"action"`
	Success bool   `json:"success"`
	Tool    string `json:"tool,omitempty"`
}

type ToolStatus string

const (
	ToolRunning     ToolStatus = "running"
	ToolSuspended   ToolStatus = "suspended"
	ToolSucceeded   ToolStatus = "succeeded"
	ToolFailed      ToolStatus = "failed"
	ToolInterrupted ToolStatus = "interrupted"
	ToolUnknown     ToolStatus = "unknown"
)

type ToolState struct {
	TurnID          string        `json:"turn_id"`
	CallID          string        `json:"call_id"`
	Name            string        `json:"name"`
	Arguments       string        `json:"arguments,omitempty"`
	Output          string        `json:"output,omitempty"`
	Status          ToolStatus    `json:"status"`
	Duration        time.Duration `json:"duration,omitempty"`
	OutputTruncated bool          `json:"output_truncated,omitempty"`
	WriteAudit      []WriteAudit  `json:"write_audit,omitempty"`
	Command         *CommandState `json:"command,omitempty"`
}

type CommandStatus string

const (
	CommandRunning   CommandStatus = "running"
	CommandSucceeded CommandStatus = "succeeded"
	CommandFailed    CommandStatus = "failed"
	CommandCanceled  CommandStatus = "canceled"
	CommandTimedOut  CommandStatus = "timed_out"
)

// CommandState is bounded tool-owned process output and lifecycle state.
type CommandState struct {
	Stdout     string        `json:"stdout,omitempty"`
	Stderr     string        `json:"stderr,omitempty"`
	Output     string        `json:"output,omitempty"`
	Status     CommandStatus `json:"status,omitempty"`
	SessionID  string        `json:"session_id,omitempty"`
	ExitCode   *int          `json:"exit_code,omitempty"`
	Truncated  bool          `json:"truncated,omitempty"`
	Background bool          `json:"background,omitempty"`
	Canceled   bool          `json:"canceled,omitempty"`
	TimedOut   bool          `json:"timed_out,omitempty"`
}

// ChangedFile is derived only from a successful file-kind WriteAudit.
type ChangedFile struct {
	Path   string `json:"path"`
	Action string `json:"action"`
	Tool   string `json:"tool,omitempty"`
	TurnID string `json:"turn_id"`
	CallID string `json:"call_id"`
}

type ContextUsage struct {
	UsedTokens  int `json:"used_tokens,omitempty"`
	LimitTokens int `json:"limit_tokens,omitempty"`
}

type CompactionStatus string

const (
	CompactionRunning     CompactionStatus = "running"
	CompactionProgress    CompactionStatus = "progress"
	CompactionCompleted   CompactionStatus = "completed"
	CompactionNoProgress  CompactionStatus = "no_progress"
	CompactionInterrupted CompactionStatus = "interrupted"
	CompactionFailed      CompactionStatus = "failed"
)

type CompactionState struct {
	TurnID              string           `json:"turn_id,omitempty"`
	AttemptID           string           `json:"attempt_id,omitempty"`
	ThreadID            string           `json:"thread_id,omitempty"`
	TranscriptRevision  uint64           `json:"transcript_revision,omitempty"`
	TranscriptCount     int              `json:"transcript_count,omitempty"`
	Reason              string           `json:"reason,omitempty"`
	Status              CompactionStatus `json:"status"`
	TokensSaved         int              `json:"tokens_saved,omitempty"`
	TokensBefore        int              `json:"tokens_before,omitempty"`
	TokensAfter         int              `json:"tokens_after,omitempty"`
	TokenCountsObserved bool             `json:"token_counts_observed,omitempty"`
	SummariesCreated    int              `json:"summaries_created,omitempty"`
	LeafSummaries       int              `json:"leaf_summaries,omitempty"`
	CondensedSummaries  int              `json:"condensed_summaries,omitempty"`
	Duration            time.Duration    `json:"duration,omitempty"`
	Background          bool             `json:"background,omitempty"`
}

// ThreadSnapshot is the authoritative, bounded in-process presentation view.
// It is not the canonical coding transcript and may omit old entries and large
// output.
type ThreadSnapshot struct {
	ThreadID        string                    `json:"thread_id"`
	Metadata        ThreadMetadata            `json:"metadata,omitempty"`
	Activity        Activity                  `json:"activity"`
	LastTurn        *LastTurnOutcome          `json:"last_turn,omitempty"`
	Entries         []TranscriptEntry         `json:"entries,omitempty"`
	Tools           []ToolState               `json:"tools,omitempty"`
	ChangedFiles    []ChangedFile             `json:"changed_files,omitempty"`
	ContextUsage    ContextUsage              `json:"context_usage,omitempty"`
	LastCompaction  *CompactionState          `json:"last_compaction,omitempty"`
	Workspace       *codingworkspace.Snapshot `json:"workspace,omitempty"`
	Status          string                    `json:"status,omitempty"`
	HasOlderEntries bool                      `json:"has_older_entries,omitempty"`
}

// ViewSource is the in-process read side of the frontend controller boundary.
// Subscribe atomically returns the current view and a bounded stream of later
// views. A slow subscriber receives the newest view instead of replaying every
// intermediate mutation.
type ViewSource interface {
	Snapshot(context.Context) (ThreadSnapshot, error)
	Subscribe(context.Context) (ThreadSnapshot, <-chan ThreadSnapshot, error)
}

// CommandSink is the write side of the frontend controller boundary. Runtime
// implementations own turn and persistence semantics; a TUI never calls agent
// internals directly.
type CommandSink interface {
	Submit(context.Context, string) error
	Interrupt(context.Context) error
	HardCancel(context.Context) error
	Compact(context.Context) error
	Rename(context.Context, string) error
	NewThread(context.Context) error
	Close(context.Context) error
}

type Controller interface {
	ViewSource
	CommandSink
}

// TranscriptPageRequest selects a bounded canonical transcript window. Before
// is an exclusive message index; a negative value selects the current end.
type TranscriptPageRequest struct {
	Before int
	Limit  int
}

// TranscriptPage is optional historical state. The live ThreadSnapshot remains
// authoritative for activity after the controller opens.
type TranscriptPage struct {
	Entries  []TranscriptEntry
	Start    int
	End      int
	Total    int
	HasOlder bool
	HasNewer bool
}

// TranscriptPager is an optional controller extension used by interactive
// frontends for lazy history hydration.
type TranscriptPager interface {
	TranscriptPage(context.Context, TranscriptPageRequest) (TranscriptPage, error)
}

// WorkspaceRefresher is an optional controller extension used by frontends to
// explicitly observe branch and worktree changes made outside the active turn.
type WorkspaceRefresher interface {
	RefreshWorkspace(context.Context) error
}
