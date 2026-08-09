// Package frontend defines the transport-neutral projection consumed by coding
// frontends. It is deliberately separate from both the agent runtime and any
// terminal framework.
package frontend

import (
	"context"
	"errors"
	"time"
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

type ToolStatus string

const (
	ToolRunning     ToolStatus = "running"
	ToolSucceeded   ToolStatus = "succeeded"
	ToolFailed      ToolStatus = "failed"
	ToolInterrupted ToolStatus = "interrupted"
	ToolUnknown     ToolStatus = "unknown"
)

type ToolState struct {
	CallID          string        `json:"call_id"`
	Name            string        `json:"name"`
	Arguments       string        `json:"arguments,omitempty"`
	Output          string        `json:"output,omitempty"`
	Status          ToolStatus    `json:"status"`
	Duration        time.Duration `json:"duration,omitempty"`
	OutputTruncated bool          `json:"output_truncated,omitempty"`
}

type ContextUsage struct {
	UsedTokens  int `json:"used_tokens,omitempty"`
	LimitTokens int `json:"limit_tokens,omitempty"`
}

// ThreadSnapshot is the authoritative, bounded frontend projection. It is not
// the canonical coding transcript and may omit old entries and large output.
type ThreadSnapshot struct {
	ProtocolVersion string            `json:"protocol_version"`
	ThreadID        string            `json:"thread_id"`
	Revision        Revision          `json:"revision"`
	Activity        Activity          `json:"activity"`
	Entries         []TranscriptEntry `json:"entries,omitempty"`
	Tools           []ToolState       `json:"tools,omitempty"`
	ContextUsage    ContextUsage      `json:"context_usage,omitempty"`
	Status          string            `json:"status,omitempty"`
	HasOlderEntries bool              `json:"has_older_entries,omitempty"`
}

type DeltaKind string

const (
	DeltaThreadOpened       DeltaKind = "thread_opened"
	DeltaThreadResumed      DeltaKind = "thread_resumed"
	DeltaTurnStarted        DeltaKind = "turn_started"
	DeltaAssistant          DeltaKind = "assistant_delta"
	DeltaReasoning          DeltaKind = "reasoning_delta"
	DeltaToolStarted        DeltaKind = "tool_started"
	DeltaToolOutput         DeltaKind = "tool_output"
	DeltaToolCompleted      DeltaKind = "tool_completed"
	DeltaContextUsage       DeltaKind = "context_usage_updated"
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
	ProtocolVersion  string           `json:"protocol_version"`
	ThreadID         string           `json:"thread_id"`
	PreviousRevision Revision         `json:"previous_revision"`
	Revision         Revision         `json:"revision"`
	Kind             DeltaKind        `json:"kind"`
	Entry            *TranscriptEntry `json:"entry,omitempty"`
	Tool             *ToolState       `json:"tool,omitempty"`
	ContextUsage     *ContextUsage    `json:"context_usage,omitempty"`
	Activity         Activity         `json:"activity,omitempty"`
	Status           string           `json:"status,omitempty"`
	RequiresSnapshot bool             `json:"requires_snapshot,omitempty"`
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
