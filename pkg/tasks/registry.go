package tasks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

type Runtime string

const (
	RuntimeSubagent Runtime = "subagent"
	RuntimeDelegate Runtime = "delegate"
	RuntimeTool     Runtime = "tool"
	RuntimeCron     Runtime = "cron"
)

var ErrTaskAlreadyExists = errors.New("task already exists")

type Status string

const (
	StatusQueued          Status = "queued"
	StatusRunning         Status = "running"
	StatusWaitingForInput Status = "waiting_for_input"
	StatusSucceeded       Status = "succeeded"
	StatusFailed          Status = "failed"
	StatusTimedOut        Status = "timed_out"
	//nolint:misspell // External task status value intentionally uses British spelling for compatibility.
	StatusCancelled Status = "cancelled"
	StatusLost      Status = "lost"
)

type DeliveryStatus string

const (
	DeliveryPending       DeliveryStatus = "pending"
	DeliveryDelivered     DeliveryStatus = "delivered"
	DeliverySessionQueued DeliveryStatus = "session_queued"
	DeliveryFailed        DeliveryStatus = "failed"
	DeliveryParentMissing DeliveryStatus = "parent_missing"
	DeliveryNotApplicable DeliveryStatus = "not_applicable"
)

type NotifyPolicy string

const (
	NotifyDoneOnly     NotifyPolicy = "done_only"
	NotifyStateChanges NotifyPolicy = "state_changes"
	NotifySilent       NotifyPolicy = "silent"
)

const (
	DefaultTerminalRetention = 7 * 24 * time.Hour
	DefaultMaxRecords        = 1000
	DefaultMaxEvents         = 5000
	DefaultMaxSnapshotBytes  = 2 * 1024 * 1024
	TaskEventSchemaVersion   = "task_event.v2"
	DeliverableReportV1      = "deliverable_report.v1"
)

type EventType string

const (
	EventTaskUpserted         EventType = "task.upserted"
	EventTaskStatusChanged    EventType = "task.status_changed"
	EventTaskDeliveryChanged  EventType = "task.delivery_changed"
	EventTaskDeliveryDecision EventType = "task.delivery_decision"
	EventTaskProgress         EventType = "task.progress"
	EventTaskUpdated          EventType = "task.updated"
	EventTaskReconciled       EventType = "task.reconciled"
)

type CompletionPayload struct {
	Text             string            `json:"text,omitempty"`
	Media            []CompletionMedia `json:"media,omitempty"`
	ObjectiveOutcome *ObjectiveOutcome `json:"objective_outcome,omitempty"`
}

type CompletionMedia struct {
	Ref         string `json:"ref"`
	Type        string `json:"type,omitempty"`
	Filename    string `json:"filename,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

type DeliverablePayload struct {
	Text             string             `json:"text,omitempty"`
	Artifacts        []DeliverableItem  `json:"artifacts,omitempty"`
	Metadata         map[string]string  `json:"metadata,omitempty"`
	Report           *DeliverableReport `json:"report,omitempty"`
	ObjectiveOutcome *ObjectiveOutcome  `json:"objective_outcome,omitempty"`
}

type ObjectiveOutcome struct {
	Status         string          `json:"status"`
	CompletedItems []ObjectiveItem `json:"completed_items,omitempty"`
	MissingItems   []string        `json:"missing_items,omitempty"`
}

type ObjectiveItem struct {
	Item     string             `json:"item"`
	Kind     string             `json:"kind,omitempty"`
	Receipts []ObjectiveReceipt `json:"receipts,omitempty"`
}

type ObjectiveReceipt struct {
	ID       string            `json:"id"`
	Kind     string            `json:"kind,omitempty"`
	Target   string            `json:"target,omitempty"`
	Action   string            `json:"action,omitempty"`
	Tool     string            `json:"tool,omitempty"`
	Summary  string            `json:"summary,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type DeliverableItem struct {
	Ref         string `json:"ref"`
	Kind        string `json:"kind,omitempty"`
	Filename    string `json:"filename,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Delivered   bool   `json:"delivered,omitempty"`
}

// DeliverableReport is a versioned canonical report for durable outputs. The
// surrounding DeliverablePayload remains the compatibility projection for older
// tools; Report is the schemaed contract new consumers should prefer.
type DeliverableReport struct {
	SchemaVersion string             `json:"schema_version"`
	ReportID      string             `json:"report_id"`
	ContentHash   string             `json:"content_hash"`
	GeneratedAt   int64              `json:"generated_at"`
	Summary       string             `json:"summary,omitempty"`
	Claims        []ReportClaim      `json:"claims,omitempty"`
	FieldDeltas   []ReportFieldDelta `json:"field_deltas,omitempty"`
	Provenance    map[string]string  `json:"provenance,omitempty"`
	Metadata      map[string]string  `json:"metadata,omitempty"`
	Extra         map[string]any     `json:"extra,omitempty"`
}

type ReportClaim struct {
	Kind       string            `json:"kind"`
	Text       string            `json:"text"`
	Confidence string            `json:"confidence,omitempty"`
	SourceRefs []string          `json:"source_refs,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

type ReportFieldDelta struct {
	Field string `json:"field"`
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
}

// TaskEvent is the append-only canonical event stream for task state. Records
// remain the current-state projection; chat, terminal, and status tools should
// render from records or reports, not treat prose output as source of truth.
type TaskEvent struct {
	SchemaVersion  string            `json:"schema_version"`
	EventID        string            `json:"event_id"`
	TaskID         string            `json:"task_id"`
	GenerationID   string            `json:"generation_id"`
	Runtime        Runtime           `json:"runtime,omitempty"`
	ParentTaskID   string            `json:"parent_task_id,omitempty"`
	Type           EventType         `json:"type"`
	Status         Status            `json:"status,omitempty"`
	DeliveryStatus DeliveryStatus    `json:"delivery_status,omitempty"`
	Seq            int64             `json:"seq"`
	EmittedAt      int64             `json:"emitted_at"`
	Source         string            `json:"source,omitempty"`
	Producer       string            `json:"producer,omitempty"`
	Fingerprint    string            `json:"fingerprint,omitempty"`
	Payload        map[string]string `json:"payload,omitempty"`
}

type Record struct {
	TaskID              string              `json:"task_id"`
	GenerationID        string              `json:"generation_id"`
	LastEventSeq        int64               `json:"last_event_sequence"`
	Runtime             Runtime             `json:"runtime"`
	TaskKind            string              `json:"task_kind,omitempty"`
	ParentTaskID        string              `json:"parent_task_id,omitempty"`
	RequesterSessionKey string              `json:"requester_session_key,omitempty"`
	OwnerKey            string              `json:"owner_key,omitempty"`
	ScopeKind           string              `json:"scope_kind,omitempty"`
	Channel             string              `json:"channel,omitempty"`
	ChatID              string              `json:"chat_id,omitempty"`
	TopicID             string              `json:"topic_id,omitempty"`
	AgentID             string              `json:"agent_id,omitempty"`
	Label               string              `json:"label,omitempty"`
	Task                string              `json:"task"`
	Status              Status              `json:"status"`
	DeliveryStatus      DeliveryStatus      `json:"delivery_status"`
	NotifyPolicy        NotifyPolicy        `json:"notify_policy"`
	DeliveryMode        string              `json:"delivery_mode,omitempty"`
	TimeoutSeconds      int                 `json:"timeout_seconds,omitempty"`
	LastCompletionID    string              `json:"last_completion_id,omitempty"`
	DeliveredAt         int64               `json:"delivered_at,omitempty"`
	DeliveryError       string              `json:"delivery_error,omitempty"`
	CreatedAt           int64               `json:"created_at"`
	StartedAt           int64               `json:"started_at,omitempty"`
	EndedAt             int64               `json:"ended_at,omitempty"`
	LastEventAt         int64               `json:"last_event_at,omitempty"`
	CleanupAfter        int64               `json:"cleanup_after,omitempty"`
	Error               string              `json:"error,omitempty"`
	ProgressSummary     string              `json:"progress_summary,omitempty"`
	TerminalSummary     string              `json:"terminal_summary,omitempty"`
	InteractionID       string              `json:"interaction_id,omitempty"`
	InteractionShortID  string              `json:"interaction_short_id,omitempty"`
	InteractionSummary  string              `json:"interaction_summary,omitempty"`
	Completion          *CompletionPayload  `json:"completion,omitempty"`
	Deliverable         *DeliverablePayload `json:"deliverable,omitempty"`
}

type Options struct {
	TerminalRetention time.Duration
	MaxRecords        int
	MaxEvents         int
	MaxSnapshotBytes  int
}

type Registry struct {
	mu          sync.RWMutex
	store       string
	options     Options
	records     map[string]Record
	events      []TaskEvent
	lastLoad    error
	writeAtomic func(string, []byte, os.FileMode) error

	unsyncedWrite bool
}

type Snapshot struct {
	Tasks  []Record    `json:"tasks"`
	Events []TaskEvent `json:"events,omitempty"`
}

type registryState struct {
	records map[string]Record
	events  []TaskEvent
}

// Stats describes the current durable registry state and the retention limits
// that apply to it. Protected records are active, non-terminal, or have not
// reached a final delivery state, so retention never removes them.
type Stats struct {
	TaskCount          int
	EventCount         int
	ProtectedTaskCount int
	SnapshotBytes      int
	TerminalRetention  time.Duration
	MaxRecords         int
	MaxEvents          int
	MaxSnapshotBytes   int
	OverSnapshotBudget bool
}

func NewRegistry(storePath string) *Registry {
	return NewRegistryWithOptions(storePath, Options{})
}

func NewRegistryWithOptions(storePath string, opts Options) *Registry {
	if opts.TerminalRetention <= 0 {
		opts.TerminalRetention = DefaultTerminalRetention
	}
	if opts.MaxRecords <= 0 {
		opts.MaxRecords = DefaultMaxRecords
	}
	if opts.MaxEvents <= 0 {
		opts.MaxEvents = DefaultMaxEvents
	}
	if opts.MaxSnapshotBytes <= 0 {
		opts.MaxSnapshotBytes = DefaultMaxSnapshotBytes
	}
	r := &Registry{
		store:       strings.TrimSpace(storePath),
		options:     opts,
		records:     make(map[string]Record),
		events:      make([]TaskEvent, 0),
		writeAtomic: fileutil.WriteFileAtomic,
	}
	if r.store != "" {
		r.lastLoad = r.load()
		if r.lastLoad == nil {
			r.pruneLoadedState(time.Now().UnixMilli())
		}
	}
	return r
}

func WorkspaceStorePath(workspace string) string {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return ""
	}
	return filepath.Join(workspace, "state", "task_registry.json")
}

// ValidateSnapshot reads and validates a registry snapshot without pruning or writing it.
func ValidateSnapshot(storePath string) error {
	r := &Registry{
		store: strings.TrimSpace(storePath),
		options: Options{
			TerminalRetention: DefaultTerminalRetention,
			MaxRecords:        DefaultMaxRecords,
			MaxEvents:         DefaultMaxEvents,
			MaxSnapshotBytes:  DefaultMaxSnapshotBytes,
		},
		records: make(map[string]Record),
		events:  make([]TaskEvent, 0),
	}
	return r.load()
}

func (r *Registry) LastLoadError() error {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.lastLoad
}

// Stats returns an exact serialized snapshot size and retention state.
func (r *Registry) Stats() Stats {
	if r == nil {
		return Stats{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	stats := Stats{
		TaskCount:         len(r.records),
		EventCount:        len(r.events),
		SnapshotBytes:     r.snapshotSizeLocked(),
		TerminalRetention: r.options.TerminalRetention,
		MaxRecords:        r.options.MaxRecords,
		MaxEvents:         r.options.MaxEvents,
		MaxSnapshotBytes:  r.options.MaxSnapshotBytes,
	}
	for _, rec := range r.records {
		if !canPruneRecord(rec) {
			stats.ProtectedTaskCount++
		}
	}
	stats.OverSnapshotBudget = stats.MaxSnapshotBytes > 0 && stats.SnapshotBytes > stats.MaxSnapshotBytes
	return stats
}
