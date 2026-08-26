package agent

import (
	"time"

	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

// TurnEndStatus describes the terminal state of a turn.
type TurnEndStatus string

const (
	// TurnEndStatusCompleted indicates the turn finished normally.
	TurnEndStatusCompleted TurnEndStatus = "completed"
	// TurnEndStatusError indicates the turn ended because of an error.
	TurnEndStatusError TurnEndStatus = "error"
	// TurnEndStatusAborted indicates the turn was hard-aborted and rolled back.
	TurnEndStatusAborted TurnEndStatus = "aborted"
	// TurnEndStatusSuspended indicates durable continuation ownership moved to
	// the runtime or a descendant task without completing or failing the turn.
	TurnEndStatusSuspended TurnEndStatus = "suspended"
)

// TurnStartPayload describes the start of a turn.
type TurnStartPayload struct {
	UserMessage string
	MediaCount  int
	Workspace   string
}

type LLMFallbackAttemptPayload struct {
	Provider             string
	Model                string
	IdentityKey          string
	Attempt              int
	Status               string
	Reason               string
	ErrorCode            string
	ClassificationSource string
	ProviderErrorKind    string
	HTTPStatus           int
	RetryAfter           time.Duration
	RequestID            string
	DiagnosticMessage    string
	Skipped              bool
}

const (
	skillContextTriggerInitialBuild        = "initial_build"
	skillContextTriggerContextRetryRebuild = "context_retry_rebuild"
)

type SkillContextSnapshot struct {
	Sequence   int      `json:"sequence"`
	Trigger    string   `json:"trigger"`
	SkillNames []string `json:"skill_names,omitempty"`
}

type ToolExecutionRecord struct {
	Name         string   `json:"name"`
	Success      bool     `json:"success"`
	ErrorSummary string   `json:"error_summary,omitempty"`
	SkillNames   []string `json:"skill_names,omitempty"`
}

// TurnEndPayload describes the completion of a turn.
type TurnEndPayload struct {
	Status                TurnEndStatus
	Workspace             string
	DeliveryExpected      bool
	Iterations            int
	Duration              time.Duration
	LLMCalls              int
	PromptTokens          int
	CompletionTokens      int
	TotalTokens           int
	ContextUsedTokens     int
	ContextLimitTokens    int
	FinalContentLen       int
	FinalContentProtected bool
	UserMessage           string
	FinalContent          string
	ActiveSkills          []string
	AttemptedSkills       []string
	FinalSuccessfulPath   []string
	SkillContextSnapshots []SkillContextSnapshot
	ToolKinds             []string
	ToolExecutions        []ToolExecutionRecord
	InteractionID         string
}

// LLMRequestPayload describes an outbound LLM request.
type LLMRequestPayload struct {
	Provider           string
	Model              string
	PromptHash         string
	MessagesCount      int
	ToolsCount         int
	MaxTokens          int
	Temperature        float64
	DiagnosticMessages string
}

// LLMResponsePayload describes an inbound LLM response.
type LLMResponsePayload struct {
	ResponseHash        string
	ContentLen          int
	ToolCalls           int
	HasReasoning        bool
	PromptTokens        int
	CompletionTokens    int
	TotalTokens         int
	HasProviderUsage    bool
	DiagnosticContent   string
	DiagnosticReasoning string
	DiagnosticToolCalls string
}

// LLMDeltaPayload describes a streamed LLM delta.
type LLMDeltaPayload struct {
	ContentDeltaLen   int
	ReasoningDeltaLen int
}

// LLMRetryPayload describes a retry of an LLM request.
type LLMRetryPayload struct {
	Attempt    int
	MaxRetries int
	Reason     string
	Error      string
	Backoff    time.Duration
}

// ContextCompressReason identifies why emergency compression ran.
type ContextCompressReason string

const (
	// ContextCompressReasonProactive indicates compression before the first LLM call.
	ContextCompressReasonProactive ContextCompressReason = "proactive_budget"
	// ContextCompressReasonRetry indicates compression during context-error retry handling.
	ContextCompressReasonRetry ContextCompressReason = "llm_retry"
	// ContextCompressReasonSummarize indicates post-turn async summarization.
	ContextCompressReasonSummarize ContextCompressReason = "summarize"
	// ContextCompressReasonManual indicates explicit foreground user compaction.
	ContextCompressReasonManual ContextCompressReason = "manual"
)

// ContextCompressPayload describes a forced history compression.
type ContextCompressPayload struct {
	Reason                   ContextCompressReason
	DroppedMessages          int
	RemainingMessages        int
	ContextWindow            int
	OutputReserve            int
	NonHistoryReserve        int
	AvailableContext         int
	HistoryBudget            int
	SummaryBudget            int
	SourceHistoryTokens      int
	SourceSummaryTokens      int
	SelectedHistoryTokens    int
	SelectedSummaryTokens    int
	RequestedRecentTailTurns int
	RecentTailTurns          int
	RecentTailTokens         int
	RecentTailOverflowTokens int
	RecentTailDegraded       bool
	Truncated                bool
	PressureReasons          []string
	TokensSaved              int
	SummariesCreated         int
	LeafSummaries            int
	CondensedSummaries       int
}

type ContextCompressLifecycleStatus string

const (
	ContextCompressLifecycleStarted     ContextCompressLifecycleStatus = "started"
	ContextCompressLifecycleProgress    ContextCompressLifecycleStatus = "progress"
	ContextCompressLifecycleCompleted   ContextCompressLifecycleStatus = "completed"
	ContextCompressLifecycleNoProgress  ContextCompressLifecycleStatus = "no_progress"
	ContextCompressLifecycleInterrupted ContextCompressLifecycleStatus = "interrupted"
	ContextCompressLifecycleFailed      ContextCompressLifecycleStatus = "failed"
)

// ContextCompressLifecyclePayload pairs every attempted compaction without
// carrying raw errors or summarized content into frontend observations.
type ContextCompressLifecyclePayload struct {
	AttemptID          string
	ThreadID           string
	TranscriptRevision uint64
	TranscriptCount    int
	Reason             ContextCompressReason
	Status             ContextCompressLifecycleStatus
	TokensSaved        int
}

type ContextSnapshotPayload struct {
	MessageCount     int
	SnapshotHash     string
	GoalHash         string
	SteeringCount    int
	ToolPairingValid bool
}

type WorkspaceSnapshotPayload struct {
	Snapshot codingworkspace.Snapshot
}

// SessionSummarizePayload describes a completed async session summarization.
type SessionSummarizePayload struct {
	SummarizedMessages int
	KeptMessages       int
	SummaryLen         int
	OmittedOversized   bool
}

// ToolExecStartPayload describes a tool execution request.
type ToolExecStartPayload struct {
	ToolCallID string
	Tool       string
	Arguments  map[string]any
}

// ToolExecEndPayload describes the outcome of a tool execution.
type ToolExecEndPayload struct {
	ToolCallID       string
	Tool             string
	Duration         time.Duration
	ForLLMLen        int
	ForUserLen       int
	IsError          bool
	Async            bool
	ResultHash       string
	Suspended        bool
	InteractionID    string
	DiagnosticResult string
	WriteAudit       []toolshared.WriteAuditEntry
	Observation      *toolshared.ToolObservation
}

// ToolExecSkippedPayload describes a skipped tool call.
type ToolExecSkippedPayload struct {
	ToolCallID string
	Tool       string
	Reason     string
}

// ToolLoopDecisionPayload contains only hash-safe loop protection metadata.
type ToolLoopDecisionPayload struct {
	Tool      string
	ArgsHash  string
	Action    string
	Code      string
	Count     int
	Threshold int
}

// SteeringInjectedPayload describes steering messages appended before the next LLM call.
type SteeringInjectedPayload struct {
	Count           int
	TotalContentLen int
}

// FollowUpQueuedPayload describes an async follow-up queued back into the inbound bus.
type FollowUpQueuedPayload struct {
	SourceTool string
	ContentLen int
}

// AsyncCompletionPayload describes a typed async tool completion event before
// the runtime applies user/parent delivery policy.
type AsyncCompletionPayload struct {
	SourceTool   string
	CompletionID string
	TaskID       string
	DeliveryMode string
	ContentLen   int
	ForUserLen   int
	MediaCount   int
	IsError      bool
	WillUser     bool
	WillParent   bool
}

type InterruptKind string

const (
	InterruptKindSteering InterruptKind = "steering"
	InterruptKindGraceful InterruptKind = "graceful"
	InterruptKindHard     InterruptKind = "hard_abort"
)

// InterruptReceivedPayload describes accepted turn-control input.
type InterruptReceivedPayload struct {
	Kind              InterruptKind
	Role              string
	ContentLen        int
	QueueDepth        int
	HintLen           int
	MessageHash       string
	DiagnosticContent string
}

// SubTurnSpawnPayload describes the creation of a child turn.
type SubTurnSpawnPayload struct {
	AgentID      string
	Label        string
	ParentTurnID string
}

// SubTurnAdmissionPayload describes pre-start target-agent capacity state.
type SubTurnAdmissionPayload struct {
	AgentID      string
	ChildTurnID  string
	ParentTurnID string
	Stage        string
	State        string
	Active       int
	Limit        int
	WaitDuration time.Duration
	WaitTimeout  time.Duration
}

// SubTurnEndPayload describes the completion of a child turn.
type SubTurnEndPayload struct {
	AgentID string
	Status  string
}

// SubTurnResultDeliveredPayload describes delivery of a sub-turn result.
type SubTurnResultDeliveredPayload struct {
	TargetChannel string
	TargetChatID  string
	ContentLen    int
}

// SubTurnOrphanPayload describes a sub-turn result that could not be delivered.
type SubTurnOrphanPayload struct {
	ParentTurnID string
	ChildTurnID  string
	Reason       string
}

// ErrorPayload describes an execution error inside the agent loop.
type ErrorPayload struct {
	Stage   string
	Message string
}
