// Package diagnostictrace defines MintClaw's bounded diagnostic trace format.
package diagnostictrace

import (
	"encoding/json"
	"time"
)

const SchemaVersionV1 = "mintclaw.diagnostic_trace.v1"

type ContentMode string

const (
	ContentMetadataOnly ContentMode = "metadata_only"
	ContentRedacted     ContentMode = "redacted_content"
)

type RecordKind string

const (
	RecordTurnStart            RecordKind = "turn.start"
	RecordTurnEnd              RecordKind = "turn.end"
	RecordModelRequest         RecordKind = "model.request"
	RecordModelResponse        RecordKind = "model.response"
	RecordModelRetry           RecordKind = "model.retry"
	RecordModelFallbackAttempt RecordKind = "model.fallback_attempt"
	RecordToolCall             RecordKind = "tool.call"
	RecordToolResult           RecordKind = "tool.result"
	RecordToolSkipped          RecordKind = "tool.skipped"
	RecordToolLoopDecision     RecordKind = "tool.loop_decision"
	RecordSteeringInjected     RecordKind = "steering.injected"
	RecordInterrupt            RecordKind = "steering.interrupt"
	RecordDeliveryDecision     RecordKind = "delivery.decision"
	RecordDeliveryAttempt      RecordKind = "delivery.attempt"
	RecordDeliveryOutcome      RecordKind = "delivery.outcome"
	RecordInteractionLifecycle RecordKind = "interaction.lifecycle"
	RecordContextCompaction    RecordKind = "context.compaction"
	RecordContextSnapshot      RecordKind = "context.snapshot"
	RecordSubTurnAdmission     RecordKind = "subturn.admission"
	RecordRuntimeError         RecordKind = "runtime.error"
)

type Trace struct {
	SchemaVersion string        `json:"schema_version"`
	TraceID       string        `json:"trace_id"`
	CreatedAt     time.Time     `json:"created_at"`
	Source        Source        `json:"source"`
	Policy        CapturePolicy `json:"policy"`
	Limits        AppliedLimits `json:"limits"`
	Metadata      Metadata      `json:"metadata,omitempty"`
	Records       []Record      `json:"records"`
	Outcome       *Outcome      `json:"outcome,omitempty"`
	Truncation    Truncation    `json:"truncation,omitempty"`
}

type Source struct {
	MintClawVersion string `json:"mintclaw_version,omitempty"`
	Commit          string `json:"commit,omitempty"`
}

type CapturePolicy struct {
	ContentMode ContentMode `json:"content_mode"`
	Redactor    string      `json:"redactor,omitempty"`
}

type AppliedLimits struct {
	MaxTraceBytes  int `json:"max_trace_bytes"`
	MaxRecords     int `json:"max_records"`
	MaxRecordBytes int `json:"max_record_bytes"`
}

type Metadata struct {
	RootTurnID   string `json:"root_turn_id,omitempty"`
	ParentTurnID string `json:"parent_turn_id,omitempty"`
	ChildTurnID  string `json:"child_turn_id,omitempty"`
	SessionHash  string `json:"session_hash,omitempty"`
	AgentID      string `json:"agent_id,omitempty"`
	RuntimeID    string `json:"runtime_id,omitempty"`
	ProjectHash  string `json:"project_hash,omitempty"`
}

type Record struct {
	Sequence    uint64          `json:"sequence"`
	OffsetNanos int64           `json:"offset_nanos"`
	Kind        RecordKind      `json:"kind"`
	Origin      Origin          `json:"origin"`
	Scope       Scope           `json:"scope,omitempty"`
	Correlation Correlation     `json:"correlation,omitempty"`
	Digest      string          `json:"digest"`
	Data        json.RawMessage `json:"data,omitempty"`
}

type Origin struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
}

type Scope struct {
	AgentID      string `json:"agent_id,omitempty"`
	SessionHash  string `json:"session_hash,omitempty"`
	TurnID       string `json:"turn_id,omitempty"`
	TaskID       string `json:"task_id,omitempty"`
	Channel      string `json:"channel,omitempty"`
	TargetHash   string `json:"target_hash,omitempty"`
	TargetAlias  string `json:"target_alias,omitempty"`
	ScopeAlias   string `json:"scope_alias,omitempty"`
	ProfileAlias string `json:"profile_alias,omitempty"`
}

type Correlation struct {
	ParentTurnID           string `json:"parent_turn_id,omitempty"`
	ModelAttemptID         string `json:"model_attempt_id,omitempty"`
	RequestID              string `json:"request_id,omitempty"`
	ToolCallID             string `json:"tool_call_id,omitempty"`
	ToolCallGeneration     string `json:"tool_call_generation,omitempty"`
	TaskGenerationID       string `json:"task_generation_id,omitempty"`
	CompletionID           string `json:"completion_id,omitempty"`
	WorkerGenerationID     string `json:"worker_generation_id,omitempty"`
	RemoteThreadID         string `json:"remote_thread_id,omitempty"`
	InvocationID           string `json:"invocation_id,omitempty"`
	PolicyRevision         string `json:"policy_revision,omitempty"`
	InteractionID          string `json:"interaction_id,omitempty"`
	InteractionRevision    int64  `json:"interaction_revision,omitempty"`
	InteractionEventID     string `json:"interaction_event_id,omitempty"`
	CompactionAttemptID    string `json:"compaction_attempt_id,omitempty"`
	RepositorySnapshotHash string `json:"repository_snapshot_hash,omitempty"`
	RepositoryCommit       string `json:"repository_commit,omitempty"`
	DeliveryID             string `json:"delivery_id,omitempty"`
	EventID                string `json:"event_id,omitempty"`
}

type Outcome struct {
	Status      string `json:"status"`
	ContentHash string `json:"content_hash,omitempty"`
	ContentLen  int    `json:"content_len,omitempty"`
	ErrorCode   string `json:"error_code,omitempty"`
}

type Truncation struct {
	Incomplete     bool               `json:"incomplete,omitempty"`
	DroppedRecords int                `json:"dropped_records,omitempty"`
	Reasons        []string           `json:"reasons,omitempty"`
	DroppedByKind  map[RecordKind]int `json:"dropped_by_kind,omitempty"`
}
