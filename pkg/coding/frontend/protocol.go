// Package frontend defines the transport-neutral projection consumed by coding
// frontends. It is deliberately separate from both the agent runtime and any
// terminal framework.
package frontend

import (
	"context"
	"errors"
	"time"

	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
)

const ProtocolVersion = "mintclaw.coding.frontend.v1"

var (
	ErrRevisionGap         = errors.New("coding frontend revision gap")
	ErrRevisionUnavailable = errors.New("coding frontend revision is no longer available")
	ErrThreadMismatch      = errors.New("coding frontend thread mismatch")
)

type Revision uint64

type Activity string

const (
	ActivityIdle         Activity = "idle"
	ActivityRunning      Activity = "running"
	ActivityInterrupting Activity = "interrupting"
	ActivityCompacting   Activity = "compacting"
	ActivityFailed       Activity = "failed"
)

type EntryKind string

const (
	EntryUser      EntryKind = "user"
	EntryAssistant EntryKind = "assistant"
	EntryReasoning EntryKind = "reasoning"
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
}

type ContextUsage struct {
	UsedTokens  int `json:"used_tokens,omitempty"`
	LimitTokens int `json:"limit_tokens,omitempty"`
}

// ThreadSnapshot is the authoritative, bounded frontend projection. It is not
// the canonical coding transcript and may omit old entries and large output.
type ThreadSnapshot struct {
	ProtocolVersion string                    `json:"protocol_version"`
	ThreadID        string                    `json:"thread_id"`
	Revision        Revision                  `json:"revision"`
	Metadata        ThreadMetadata            `json:"metadata,omitempty"`
	Activity        Activity                  `json:"activity"`
	Entries         []TranscriptEntry         `json:"entries,omitempty"`
	Tools           []ToolState               `json:"tools,omitempty"`
	ContextUsage    ContextUsage              `json:"context_usage,omitempty"`
	Workspace       *codingworkspace.Snapshot `json:"workspace,omitempty"`
	Status          string                    `json:"status,omitempty"`
	HasOlderEntries bool                      `json:"has_older_entries,omitempty"`
}

type DeltaKind string

const (
	DeltaThreadOpened       DeltaKind = "thread_opened"
	DeltaThreadResumed      DeltaKind = "thread_resumed"
	DeltaThreadMetadata     DeltaKind = "thread_metadata_updated"
	DeltaTurnStarted        DeltaKind = "turn_started"
	DeltaAssistant          DeltaKind = "assistant_delta"
	DeltaReasoning          DeltaKind = "reasoning_delta"
	DeltaNotice             DeltaKind = "notice_updated"
	DeltaToolStarted        DeltaKind = "tool_started"
	DeltaToolOutput         DeltaKind = "tool_output"
	DeltaToolSuspended      DeltaKind = "tool_suspended"
	DeltaToolCompleted      DeltaKind = "tool_completed"
	DeltaContextUsage       DeltaKind = "context_usage_updated"
	DeltaWorkspaceUpdated   DeltaKind = "workspace_updated"
	DeltaCompactionStarted  DeltaKind = "compaction_started"
	DeltaCompactionComplete DeltaKind = "compaction_completed"
	DeltaCompactionFailed   DeltaKind = "compaction_failed"
	DeltaTurnCompleted      DeltaKind = "turn_completed"
	DeltaTurnFailed         DeltaKind = "turn_failed"
	DeltaInterruptRequested DeltaKind = "interrupt_requested"
	DeltaTurnInterrupted    DeltaKind = "turn_interrupted"
)

// Delta contains a complete bounded replacement for the entity it changes.
// PreviousRevision makes missing or reordered progress detectable.
type Delta struct {
	ProtocolVersion  string                    `json:"protocol_version"`
	ThreadID         string                    `json:"thread_id"`
	PreviousRevision Revision                  `json:"previous_revision"`
	Revision         Revision                  `json:"revision"`
	Kind             DeltaKind                 `json:"kind"`
	TurnID           string                    `json:"turn_id,omitempty"`
	EntityID         string                    `json:"entity_id,omitempty"`
	Metadata         *ThreadMetadata           `json:"metadata,omitempty"`
	Entry            *TranscriptEntry          `json:"entry,omitempty"`
	Tool             *ToolState                `json:"tool,omitempty"`
	ContextUsage     *ContextUsage             `json:"context_usage,omitempty"`
	Workspace        *codingworkspace.Snapshot `json:"workspace,omitempty"`
	Activity         Activity                  `json:"activity,omitempty"`
	Status           string                    `json:"status,omitempty"`
	RequiresSnapshot bool                      `json:"requires_snapshot,omitempty"`
}

// SnapshotSource is the read side of the frontend controller boundary.
// ChangesSince returns ErrRevisionUnavailable when its bounded delta window no
// longer covers the requested revision; callers must then request Snapshot.
type SnapshotSource interface {
	Snapshot(context.Context) (ThreadSnapshot, error)
	ChangesSince(context.Context, Revision) ([]Delta, error)
	Watch(context.Context, Revision) (<-chan Delta, error)
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
	SnapshotSource
	CommandSink
}
