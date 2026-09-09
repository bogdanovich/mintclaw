// Package frontend defines the bounded in-process presentation state consumed
// by coding frontends. It is deliberately separate from both the agent runtime
// and any terminal framework.
package frontend

import (
	"context"
	"errors"
	"time"

	codingplan "github.com/bogdanovich/mintclaw/pkg/coding/plan"
	codingreview "github.com/bogdanovich/mintclaw/pkg/coding/review"
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
	ActivityReviewing    Activity = "reviewing"
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

// AssistantPhase identifies whether provider-produced assistant text explains
// ongoing work or completes the turn. Non-assistant entries leave it empty.
type AssistantPhase string

const (
	AssistantPhaseCommentary AssistantPhase = "commentary"
	AssistantPhaseFinal      AssistantPhase = "final"
)

type TranscriptEntry struct {
	ID        string         `json:"id"`
	TurnID    string         `json:"turn_id"`
	Kind      EntryKind      `json:"kind"`
	Phase     AssistantPhase `json:"phase,omitempty"`
	Text      string         `json:"text"`
	Complete  bool           `json:"complete"`
	Truncated bool           `json:"truncated,omitempty"`
	// The remaining fields are durable hydration evidence. EvidenceOnly
	// entries participate in turn reconstruction but never become cells.
	OccurredAt    time.Time `json:"occurred_at,omitempty"`
	RootTurnStart bool      `json:"root_turn_start,omitempty"`
	ConcreteWork  bool      `json:"concrete_work,omitempty"`
	EvidenceOnly  bool      `json:"evidence_only,omitempty"`
}

// PresentationKind identifies the semantic renderer selected for one ordered
// frontend item. It deliberately describes content rather than terminal
// styling.
type PresentationKind string

const (
	PresentationUserMessage      PresentationKind = "user_message"
	PresentationAssistantMessage PresentationKind = "assistant_message"
	PresentationFinalAnswer      PresentationKind = "final_answer"
	PresentationReasoning        PresentationKind = "reasoning"
	PresentationToolMessage      PresentationKind = "tool_message"
	PresentationToolCall         PresentationKind = "tool_call"
	PresentationPlanUpdate       PresentationKind = "plan_update"
	PresentationCompaction       PresentationKind = "compaction"
	PresentationTurnSeparator    PresentationKind = "turn_separator"
	PresentationWarning          PresentationKind = "warning"
	PresentationError            PresentationKind = "error"
)

// PresentationLifecycle is the renderer-neutral lifecycle of one item.
type PresentationLifecycle string

const (
	PresentationActive      PresentationLifecycle = "active"
	PresentationCompleted   PresentationLifecycle = "completed"
	PresentationFailed      PresentationLifecycle = "failed"
	PresentationInterrupted PresentationLifecycle = "interrupted"
	PresentationSuspended   PresentationLifecycle = "suspended"
	PresentationUnknown     PresentationLifecycle = "unknown"
)

// ThreadMetadata is the bounded display metadata needed by a frontend. It
// deliberately excludes catalog and storage implementation details.
type ThreadMetadata struct {
	Title       string    `json:"title,omitempty"`
	Preview     string    `json:"preview,omitempty"`
	ProjectRoot string    `json:"project_root,omitempty"`
	CWD         string    `json:"cwd,omitempty"`
	Model       string    `json:"model,omitempty"`
	Provider    string    `json:"provider,omitempty"`
	Archived    bool      `json:"archived,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
}

// PermissionMode is the effective filesystem/tool access granted to the
// coding runtime. It describes actual runtime construction, not a user-facing
// claim inferred from model output.
type PermissionMode string

const (
	PermissionFullAccess PermissionMode = "full_access"
	PermissionReadOnly   PermissionMode = "read_only"
)

// AutonomyMode describes whether tool execution pauses for approvals.
type AutonomyMode string

const (
	AutonomyYolo AutonomyMode = "yolo"
)

// ProviderAccountState is a redacted provider credential summary. It never
// carries account IDs, email addresses, tokens, or API key material.
type ProviderAccountState string

const (
	ProviderAccountAuthenticated ProviderAccountState = "authenticated"
	ProviderAccountConfigured    ProviderAccountState = "configured"
	ProviderAccountNeedsRefresh  ProviderAccountState = "needs_refresh"
	ProviderAccountExpired       ProviderAccountState = "expired"
)

type ProviderAccount struct {
	Provider   string               `json:"provider,omitempty"`
	AuthMethod string               `json:"auth_method,omitempty"`
	State      ProviderAccountState `json:"state,omitempty"`
}

// InstructionSource is a content-free description of one project instruction
// file that was admitted by the coding instruction loader.
type InstructionSource struct {
	Path      string `json:"path"`
	Scope     string `json:"scope,omitempty"`
	Label     string `json:"label,omitempty"`
	Global    bool   `json:"global,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// RuntimeStatus contains bounded, renderer-neutral operational facts that are
// known only after constructing a coding runtime. Durable thread metadata stays
// separate because these values are recomputed on every new or resumed run.
type RuntimeStatus struct {
	Version                     string              `json:"version,omitempty"`
	Resumed                     bool                `json:"resumed,omitempty"`
	ReasoningEffort             string              `json:"reasoning_effort,omitempty"`
	ReasoningConfigured         bool                `json:"reasoning_configured,omitempty"`
	Permission                  PermissionMode      `json:"permission,omitempty"`
	Autonomy                    AutonomyMode        `json:"autonomy,omitempty"`
	InstructionSources          []InstructionSource `json:"instruction_sources,omitempty"`
	InstructionSourcesTruncated bool                `json:"instruction_sources_truncated,omitempty"`
	InstructionWarningCount     int                 `json:"instruction_warning_count,omitempty"`
	Account                     *ProviderAccount    `json:"account,omitempty"`
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
	TurnID          string                      `json:"turn_id"`
	CallID          string                      `json:"call_id"`
	Name            string                      `json:"name"`
	Arguments       string                      `json:"arguments,omitempty"`
	Output          string                      `json:"output,omitempty"`
	Status          ToolStatus                  `json:"status"`
	Duration        time.Duration               `json:"duration,omitempty"`
	OutputTruncated bool                        `json:"output_truncated,omitempty"`
	PlanObserved    bool                        `json:"plan_observed,omitempty"`
	WriteAudit      []WriteAudit                `json:"write_audit,omitempty"`
	Command         *CommandState               `json:"command,omitempty"`
	Exploration     *ExplorationState           `json:"exploration,omitempty"`
	MCP             *MCPState                   `json:"mcp,omitempty"`
	RepositoryDiff  *codingworkspace.DiffResult `json:"repository_diff,omitempty"`
}

type MCPOutcome string

const (
	MCPOutcomeRunning   MCPOutcome = "running"
	MCPOutcomeSucceeded MCPOutcome = "succeeded"
	MCPOutcomeFailed    MCPOutcome = "failed"
	MCPOutcomeCanceled  MCPOutcome = "canceled"
	MCPOutcomeTimedOut  MCPOutcome = "timed_out"
	MCPOutcomeUncertain MCPOutcome = "uncertain"
)

// MCPState is bounded MCP-wrapper-owned presentation evidence. Argument
// values are intentionally absent; Arguments on ToolState is shape-only.
type MCPState struct {
	Server            string     `json:"server"`
	Tool              string     `json:"tool"`
	Purpose           string     `json:"purpose,omitempty"`
	Outcome           MCPOutcome `json:"outcome"`
	Result            string     `json:"result,omitempty"`
	Error             string     `json:"error,omitempty"`
	Truncated         bool       `json:"truncated,omitempty"`
	LoopHaltCode      string     `json:"loop_halt_code,omitempty"`
	LoopHaltCount     int        `json:"loop_halt_count,omitempty"`
	LoopHaltThreshold int        `json:"loop_halt_threshold,omitempty"`
}

type ExplorationOperation string

const (
	ExplorationRead   ExplorationOperation = "read"
	ExplorationList   ExplorationOperation = "list"
	ExplorationSearch ExplorationOperation = "search"
)

// ExplorationState is bounded native-tool-owned read/list/search metadata.
// It is never inferred from a shell command, tool name, or model-facing text.
type ExplorationState struct {
	Operation ExplorationOperation `json:"operation"`
	Path      string               `json:"path,omitempty"`
	Pattern   string               `json:"pattern,omitempty"`
	Workspace string               `json:"workspace,omitempty"`
	Truncated bool                 `json:"truncated,omitempty"`
}

type CommandStatus string

const (
	CommandUnknown   CommandStatus = "unknown"
	CommandRunning   CommandStatus = "running"
	CommandSucceeded CommandStatus = "succeeded"
	CommandFailed    CommandStatus = "failed"
	CommandCanceled  CommandStatus = "canceled"
	CommandTimedOut  CommandStatus = "timed_out"
)

type CommandSource string

const (
	CommandSourceAgent     CommandSource = "agent"
	CommandSourceUserShell CommandSource = "user_shell"
)

type CommandTranscriptEntry struct {
	Sequence uint64 `json:"sequence"`
	Stream   string `json:"stream"`
	Text     string `json:"text"`
}

// CommandState is bounded tool-owned process output and lifecycle state.
type CommandState struct {
	Action      string                   `json:"action,omitempty"`
	Command     string                   `json:"command,omitempty"`
	CWD         string                   `json:"cwd,omitempty"`
	Input       string                   `json:"input,omitempty"`
	Source      CommandSource            `json:"source,omitempty"`
	Stdout      string                   `json:"stdout,omitempty"`
	Stderr      string                   `json:"stderr,omitempty"`
	Output      string                   `json:"output,omitempty"`
	Transcript  []CommandTranscriptEntry `json:"transcript,omitempty"`
	Duration    time.Duration            `json:"duration,omitempty"`
	Status      CommandStatus            `json:"status,omitempty"`
	SessionID   string                   `json:"session_id,omitempty"`
	ExitCode    *int                     `json:"exit_code,omitempty"`
	Truncated   bool                     `json:"truncated,omitempty"`
	Background  bool                     `json:"background,omitempty"`
	OwnsProcess bool                     `json:"owns_process,omitempty"`
	Orphan      bool                     `json:"orphan,omitempty"`
	Canceled    bool                     `json:"canceled,omitempty"`
	TimedOut    bool                     `json:"timed_out,omitempty"`
}

type PlanStepStatus = codingplan.StepStatus

const (
	PlanStepPending    = codingplan.StepPending
	PlanStepInProgress = codingplan.StepInProgress
	PlanStepCompleted  = codingplan.StepCompleted
)

type PlanStepState = codingplan.Step

// PlanState is a bounded tool-owned plan update. It contains only validated
// observation data and never model-facing tool JSON or argument values.
type PlanState = codingplan.State

// PresentationItem is the authoritative ordered unit consumed by coding
// frontends. Exactly one typed payload is present. Sequence and ID are stable;
// Revision advances only when renderer-visible state changes.
type PresentationItem struct {
	ID          string                `json:"id"`
	TurnID      string                `json:"turn_id"`
	Sequence    uint64                `json:"sequence"`
	Revision    uint64                `json:"revision"`
	Kind        PresentationKind      `json:"kind"`
	Lifecycle   PresentationLifecycle `json:"lifecycle"`
	CreatedAt   time.Time             `json:"created_at"`
	StartedAt   time.Time             `json:"started_at"`
	CompletedAt *time.Time            `json:"completed_at,omitempty"`
	Duration    time.Duration         `json:"duration,omitempty"`
	Message     *TranscriptEntry      `json:"message,omitempty"`
	Tool        *ToolState            `json:"tool,omitempty"`
	Plan        *PlanState            `json:"plan,omitempty"`
	Compaction  *CompactionState      `json:"compaction,omitempty"`
	Turn        *TurnBoundaryState    `json:"turn,omitempty"`
}

// TurnBoundaryState records the truthful terminal outcome shown immediately
// before a final answer, or at the end of an abnormal turn without one.
type TurnBoundaryState struct {
	Outcome TurnOutcome `json:"outcome"`
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
	ThreadID     string             `json:"thread_id"`
	ActiveTurnID string             `json:"active_turn_id,omitempty"`
	Metadata     ThreadMetadata     `json:"metadata,omitempty"`
	Runtime      *RuntimeStatus     `json:"runtime,omitempty"`
	Activity     Activity           `json:"activity"`
	LastTurn     *LastTurnOutcome   `json:"last_turn,omitempty"`
	Items        []PresentationItem `json:"items,omitempty"`
	// Entries and Tools are compatibility projections derived from Items while
	// the existing TUI migrates to semantic cells.
	Entries          []TranscriptEntry             `json:"entries,omitempty"`
	Tools            []ToolState                   `json:"tools,omitempty"`
	ChangedFiles     []ChangedFile                 `json:"changed_files,omitempty"`
	ContextUsage     ContextUsage                  `json:"context_usage,omitempty"`
	LastCompaction   *CompactionState              `json:"last_compaction,omitempty"`
	Workspace        *codingworkspace.Snapshot     `json:"workspace,omitempty"`
	RepositoryStatus *codingworkspace.StatusResult `json:"repository_status,omitempty"`
	RepositoryDiff   *codingworkspace.DiffResult   `json:"repository_diff,omitempty"`
	Review           *codingreview.State           `json:"review,omitempty"`
	Status           string                        `json:"status,omitempty"`
	HasOlderEntries  bool                          `json:"has_older_entries,omitempty"`
}

// CurrentPlan returns an independent copy of the latest authoritative plan
// visible in this bounded snapshot.
func (snapshot ThreadSnapshot) CurrentPlan() *PlanState {
	return latestPresentationPlan(snapshot.Items)
}

// ViewSource is the in-process read side of the frontend controller boundary.
// Subscribe atomically returns the current view and a bounded stream of later
// views. A slow subscriber receives the newest view instead of replaying every
// intermediate mutation.
type ViewSource interface {
	Snapshot(context.Context) (ThreadSnapshot, error)
	Subscribe(context.Context) (ThreadSnapshot, <-chan ThreadSnapshot, error)
}

const (
	MaxTurnAttachments = 32
	MaxSteerIDBytes    = 128
	MaxSteersPerTurn   = 256
)

// TurnAttachment is one caller-owned file proposed for admission with a turn.
// Path is ephemeral input: runtimes must copy and replace it with a durable,
// thread-owned reference before persisting or dispatching the turn.
type TurnAttachment struct {
	Path        string `json:"-"`
	Filename    string `json:"filename,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

// TurnInput is the structured command accepted from every coding frontend.
// A turn may contain text, attachments, or both.
type TurnInput struct {
	Text        string           `json:"text,omitempty"`
	Attachments []TurnAttachment `json:"attachments,omitempty"`
}

// Clone prevents mutable frontend slices from crossing the asynchronous
// controller/runtime boundary.
func (input TurnInput) Clone() TurnInput {
	input.Attachments = append([]TurnAttachment(nil), input.Attachments...)
	return input
}

// SteerInput is one append-only instruction for the currently active turn.
// ID is caller-owned idempotency identity: retrying the same ID and text is a
// no-op, while reusing an ID for different text is rejected.
type SteerInput struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// Steerer is an optional controller capability for same-turn guidance. It is
// separate from CommandSink so local frontends do not need to expose remote
// worker controls.
type Steerer interface {
	Steer(context.Context, SteerInput) error
}

// CommandSink is the write side of the frontend controller boundary. Runtime
// implementations own turn and persistence semantics; a TUI never calls agent
// internals directly.
type CommandSink interface {
	Submit(context.Context, TurnInput) error
	Interrupt(context.Context) error
	HardCancel(context.Context) error
	Compact(context.Context) error
	Rename(context.Context, string) error
	SetArchived(context.Context, bool) error
	NewThread(context.Context) error
	Close(context.Context) error
}

// ThreadLifecycle is an optional runtime capability for atomic metadata-only
// lifecycle changes. Implementations must not move or delete project files.
type ThreadLifecycle interface {
	Rename(context.Context, string) error
	SetArchived(context.Context, bool) error
}

// BackgroundCompactionObserver closes the admission gap after a foreground
// turn returns while its routine compaction worker still owns thread context.
type BackgroundCompactionObserver interface {
	BackgroundCompactionActive() bool
}

type Controller interface {
	ViewSource
	CommandSink
}

// TurnSettler is the optional completion barrier used by non-interactive
// frontends. A terminal presentation snapshot can precede post-turn durable
// persistence; AwaitTurn returns only after the admitted controller operation
// has settled and reports that operation's final error.
type TurnSettler interface {
	AwaitTurn(context.Context) error
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

// RepositoryEvidenceReader exposes the same passive typed evidence used by
// coding model tools to non-model frontends.
type RepositoryEvidenceReader interface {
	RepositoryStatus(context.Context) (codingworkspace.StatusResult, error)
	RepositoryDiff(context.Context, codingworkspace.DiffTarget) (codingworkspace.DiffResult, error)
}

// Reviewer is an optional controller capability. Implementations admit one
// native read-only review operation; unsupported frontends do not advertise it.
type Reviewer interface {
	Review(context.Context, codingreview.Target) error
}
