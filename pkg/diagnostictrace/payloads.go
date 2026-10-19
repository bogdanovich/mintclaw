package diagnostictrace

// Payload structs intentionally contain only normalized evidence. Capture
// adapters must project source-specific values into these fields instead of
// storing arbitrary runtime payloads.

type TurnPayload struct {
	Status       string `json:"status,omitempty"`
	InputHash    string `json:"input_hash,omitempty"`
	InputLen     int    `json:"input_len,omitempty"`
	FinalHash    string `json:"final_hash,omitempty"`
	FinalLen     int    `json:"final_len,omitempty"`
	Iterations   int    `json:"iterations,omitempty"`
	InputPreview string `json:"input_preview,omitempty"`
	FinalPreview string `json:"final_preview,omitempty"`
}

type ModelPayload struct {
	Provider                      string                   `json:"provider,omitempty"`
	Model                         string                   `json:"model,omitempty"`
	IdentityKey                   string                   `json:"identity_key,omitempty"`
	Attempt                       int                      `json:"attempt,omitempty"`
	Status                        string                   `json:"status,omitempty"`
	Reason                        string                   `json:"reason,omitempty"`
	Messages                      int                      `json:"messages,omitempty"`
	Tools                         int                      `json:"tools,omitempty"`
	PromptHash                    string                   `json:"prompt_hash,omitempty"`
	SystemSources                 *SystemSourceFingerprint `json:"system_sources,omitempty"`
	PromptCacheMode               string                   `json:"prompt_cache_mode,omitempty"`
	StablePrefixVersion           string                   `json:"stable_prefix_version,omitempty"`
	StablePrefixHash              string                   `json:"stable_prefix_hash,omitempty"`
	StableSystemHash              string                   `json:"stable_system_hash,omitempty"`
	DynamicSystemHash             string                   `json:"dynamic_system_hash,omitempty"`
	ToolSchemaHash                string                   `json:"tool_schema_hash,omitempty"`
	HistoryHash                   string                   `json:"history_hash,omitempty"`
	DynamicTailHash               string                   `json:"dynamic_tail_hash,omitempty"`
	StableSystemParts             int                      `json:"stable_system_parts,omitempty"`
	DynamicSystemParts            int                      `json:"dynamic_system_parts,omitempty"`
	HistoryMessages               int                      `json:"history_messages,omitempty"`
	DynamicTailMessages           int                      `json:"dynamic_tail_messages,omitempty"`
	TailBoundaryFound             bool                     `json:"tail_boundary_found,omitempty"`
	DynamicSystemBeforeTranscript bool                     `json:"dynamic_system_before_transcript,omitempty"`
	ResponseHash                  string                   `json:"response_hash,omitempty"`
	PromptTokens                  int                      `json:"prompt_tokens,omitempty"`
	ResponseTokens                int                      `json:"response_tokens,omitempty"`
	HasProviderUsage              *bool                    `json:"has_provider_usage,omitempty"`
	CacheReadInputTokens          int                      `json:"cache_read_input_tokens,omitempty"`
	CacheReadKnown                bool                     `json:"cache_read_known,omitempty"`
	CacheWriteInputTokens         int                      `json:"cache_write_input_tokens,omitempty"`
	CacheWriteKnown               bool                     `json:"cache_write_known,omitempty"`
	CacheOutcome                  string                   `json:"cache_outcome,omitempty"`
	Skipped                       bool                     `json:"skipped,omitempty"`
	ErrorCode                     string                   `json:"error_code,omitempty"`
	ClassificationSource          string                   `json:"classification_source,omitempty"`
	ProviderErrorKind             string                   `json:"provider_error_kind,omitempty"`
	HTTPStatus                    int                      `json:"http_status,omitempty"`
	RetryAfterMS                  int64                    `json:"retry_after_ms,omitempty"`
	RequestID                     string                   `json:"request_id,omitempty"`
	MessagesPreview               string                   `json:"messages_preview,omitempty"`
	ResponsePreview               string                   `json:"response_preview,omitempty"`
	ReasoningPreview              string                   `json:"reasoning_preview,omitempty"`
	ToolCallsPreview              string                   `json:"tool_calls_preview,omitempty"`
	ErrorPreview                  string                   `json:"error_preview,omitempty"`
}

type ToolPayload struct {
	Tool             string `json:"tool"`
	ArgsHash         string `json:"args_hash,omitempty"`
	ResultHash       string `json:"result_hash,omitempty"`
	Status           string `json:"status,omitempty"`
	Executed         bool   `json:"executed,omitempty"`
	IsError          bool   `json:"is_error,omitempty"`
	Action           string `json:"action,omitempty"`
	DecisionCode     string `json:"decision_code,omitempty"`
	Count            int    `json:"count,omitempty"`
	Threshold        int    `json:"threshold,omitempty"`
	ArgumentsPreview string `json:"arguments_preview,omitempty"`
	ResultPreview    string `json:"result_preview,omitempty"`
}

type SteeringPayload struct {
	Status         string `json:"status,omitempty"`
	Role           string `json:"role,omitempty"`
	MessageHash    string `json:"message_hash,omitempty"`
	ContentLen     int    `json:"content_len,omitempty"`
	Count          int    `json:"count,omitempty"`
	QueueDepth     int    `json:"queue_depth,omitempty"`
	ContentPreview string `json:"content_preview,omitempty"`
}

type DeliveryPayload struct {
	Mode         string `json:"mode,omitempty"`
	Status       string `json:"status,omitempty"`
	TaskStatus   string `json:"task_status,omitempty"`
	RemoteState  string `json:"remote_state,omitempty"`
	FailureCode  string `json:"failure_code,omitempty"`
	TargetHash   string `json:"target_hash,omitempty"`
	Attempt      int    `json:"attempt,omitempty"`
	WillUser     bool   `json:"will_user,omitempty"`
	WillParent   bool   `json:"will_parent,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
	ContentLen   int    `json:"content_len,omitempty"`
	ErrorPreview string `json:"error_preview,omitempty"`
}

// InteractionPayload retains only bounded lifecycle metadata. Question,
// answer, approval, failure-detail, and execution-context content remain in
// the authoritative interaction registry and are never copied into a trace.
type InteractionPayload struct {
	Kind     string `json:"kind"`
	Event    string `json:"event"`
	Status   string `json:"status"`
	Outcome  string `json:"outcome,omitempty"`
	Sequence int64  `json:"sequence"`
	Code     string `json:"code,omitempty"`
	Success  *bool  `json:"success,omitempty"`
}

type ContextPayload struct {
	Reason                        string   `json:"reason,omitempty"`
	Status                        string   `json:"status,omitempty"`
	SnapshotType                  string   `json:"snapshot_type,omitempty"`
	BeforeMessages                int      `json:"before_messages,omitempty"`
	AfterMessages                 int      `json:"after_messages,omitempty"`
	TranscriptRevision            uint64   `json:"transcript_revision,omitempty"`
	TranscriptCount               int      `json:"transcript_count,omitempty"`
	TokensBefore                  int      `json:"tokens_before,omitempty"`
	TokensAfter                   int      `json:"tokens_after,omitempty"`
	TokensSaved                   int      `json:"tokens_saved,omitempty"`
	TokenCountsObserved           bool     `json:"token_counts_observed,omitempty"`
	SummariesCreated              int      `json:"summaries_created,omitempty"`
	LeafSummaries                 int      `json:"leaf_summaries,omitempty"`
	CondensedSummaries            int      `json:"condensed_summaries,omitempty"`
	DurationMS                    int64    `json:"duration_ms,omitempty"`
	Background                    bool     `json:"background,omitempty"`
	SnapshotHash                  string   `json:"snapshot_hash,omitempty"`
	RepositoryAvailable           bool     `json:"repository_available,omitempty"`
	RepositoryStatusAvailable     bool     `json:"repository_status_available,omitempty"`
	RepositoryDetached            bool     `json:"repository_detached,omitempty"`
	RepositoryUnborn              bool     `json:"repository_unborn,omitempty"`
	RepositoryWorktree            bool     `json:"repository_worktree,omitempty"`
	RepositoryDirty               bool     `json:"repository_dirty,omitempty"`
	DiffStatAvailable             bool     `json:"diff_stat_available,omitempty"`
	ChangedFiles                  int      `json:"changed_files,omitempty"`
	AddedLines                    int      `json:"added_lines,omitempty"`
	DeletedLines                  int      `json:"deleted_lines,omitempty"`
	BinaryFiles                   int      `json:"binary_files,omitempty"`
	SubmoduleWorktreeStateIgnored bool     `json:"submodule_worktree_state_ignored,omitempty"`
	SnapshotTruncated             bool     `json:"snapshot_truncated,omitempty"`
	ProtectedFactRefs             []string `json:"protected_fact_refs,omitempty"`
}

type SubTurnAdmissionPayload struct {
	State       string `json:"state"`
	Stage       string `json:"stage"`
	AgentID     string `json:"agent_id,omitempty"`
	ChildTurnID string `json:"child_turn_id,omitempty"`
	Active      int    `json:"active,omitempty"`
	Limit       int    `json:"limit,omitempty"`
	WaitMS      int64  `json:"wait_ms,omitempty"`
	TimeoutMS   int64  `json:"timeout_ms,omitempty"`
}

type RuntimeErrorPayload struct {
	Stage                string `json:"stage,omitempty"`
	ErrorCode            string `json:"error_code,omitempty"`
	ClassificationSource string `json:"classification_source,omitempty"`
	ProviderErrorKind    string `json:"provider_error_kind,omitempty"`
	HTTPStatus           int    `json:"http_status,omitempty"`
	RequestID            string `json:"request_id,omitempty"`
	MessagePreview       string `json:"message_preview,omitempty"`
}
