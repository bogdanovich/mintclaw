package protocoltypes

import (
	"time"

	"github.com/bogdanovich/mintclaw/pkg/taskresult"
)

type ToolCall struct {
	ID                      string         `json:"id"`
	Type                    string         `json:"type,omitempty"`
	Name                    string         `json:"name"`
	Arguments               map[string]any `json:"arguments"`
	ThoughtSignature        string         `json:"thought_signature,omitempty"`
	ToolFeedbackExplanation string         `json:"tool_feedback_explanation,omitempty"`
}

type LLMResponse struct {
	Content          string            `json:"content"`
	ReasoningContent string            `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall        `json:"tool_calls,omitempty"`
	FinishReason     string            `json:"finish_reason"`
	Usage            *UsageInfo        `json:"usage,omitempty"`
	Reasoning        string            `json:"reasoning"`
	ReasoningDetails []ReasoningDetail `json:"reasoning_details"`
}

type StreamChunk struct {
	Content          string
	ReasoningContent string
}

type ReasoningDetail struct {
	Format string `json:"format"`
	Index  int    `json:"index"`
	Type   string `json:"type"`
	Text   string `json:"text"`
}

type UsageInfo struct {
	PromptTokens          int  `json:"prompt_tokens"`
	CompletionTokens      int  `json:"completion_tokens"`
	TotalTokens           int  `json:"total_tokens"`
	CacheReadInputTokens  *int `json:"cache_read_input_tokens,omitempty"`
	CacheWriteInputTokens *int `json:"cache_write_input_tokens,omitempty"`
}

// KnownTokenCount represents a provider-reported token count. A pointer to
// zero is a known zero (for example, a measured cache miss); nil means the
// provider did not report the dimension and must not be interpreted as zero.
func KnownTokenCount(tokens int) *int {
	if tokens < 0 {
		tokens = 0
	}
	return &tokens
}

// CacheControl marks a content block for LLM-side prefix caching.
// Currently only "ephemeral" is supported (used by Anthropic).
type CacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

const PromptCachePlanVersion1 = 1

type PromptCacheWritePolicy string

const (
	PromptCacheWriteReuse   PromptCacheWritePolicy = "reuse"
	PromptCacheWriteNoWrite PromptCacheWritePolicy = "no_write"
)

// PromptCachePlan is MintClaw's provider-neutral cache intent. Message indexes
// refer to the exact provider-bound message slice supplied with the same Chat
// call. Provider adapters may compile only the fields they explicitly support;
// the plan itself is never serialized as an API request field.
type PromptCachePlan struct {
	Version                  int
	LineageKey               string
	WritePolicy              PromptCacheWritePolicy
	BreakpointMessageIndexes []int
}

// ContentBlock represents a structured segment of a system message.
// Adapters that understand SystemParts can use these blocks to set
// per-block cache control (e.g. Anthropic's cache_control: ephemeral).
type ContentBlock struct {
	Type         string        `json:"type"` // "text"
	Text         string        `json:"text"`
	CacheControl *CacheControl `json:"cache_control,omitempty"`

	// Prompt metadata is internal to the agent runtime. It records which
	// structured prompt segment produced this block without changing provider
	// JSON.
	PromptLayer  string `json:"-"`
	PromptSlot   string `json:"-"`
	PromptSource string `json:"-"`
}

type Attachment struct {
	Type        string `json:"type,omitempty"`
	Ref         string `json:"ref,omitempty"`
	URL         string `json:"url,omitempty"`
	Filename    string `json:"filename,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

type ImageGenerationRequest struct {
	Prompt        string
	Model         string
	Size          string
	Quality       string
	OutputFormat  string
	Count         int
	InputImages   []ImageGenerationInput
	InputFidelity string
}

// ImageGenerationInput is a bounded, caller-validated source image for an
// image-edit request. Providers must not interpret Filename as a local path.
type ImageGenerationInput struct {
	Data        []byte
	Filename    string
	ContentType string
}

type GeneratedImage struct {
	Data     []byte
	MimeType string
	Ext      string
}

type ImageGenerationResponse struct {
	Images []GeneratedImage
}

type Message struct {
	Role             string           `json:"role"`
	Content          string           `json:"content"`
	ModelName        string           `json:"model_name,omitempty"`
	CreatedAt        *time.Time       `json:"created_at,omitempty"`
	Media            []string         `json:"media,omitempty"`
	Attachments      []Attachment     `json:"attachments,omitempty"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	SystemParts      []ContentBlock   `json:"system_parts,omitempty"` // structured system blocks for cache-aware adapters
	ToolCalls        []ToolCall       `json:"tool_calls,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
	ToolResultStatus ToolResultStatus `json:"tool_result_status,omitempty"`
	ToolExecutions   []ToolExecution  `json:"tool_executions,omitempty"`

	// Deliverable is canonical-session-only task output. The agent removes it
	// from provider-bound history just like durable tool execution markers.
	Deliverable *taskresult.Deliverable `json:"deliverable,omitempty"`

	// RootTurnStart is canonical-session-only identity for the user message
	// that admitted a new root turn. In-turn user-shaped messages do not set it.
	RootTurnStart bool `json:"root_turn_start,omitempty"`

	// OutboundDeliveryID makes a confirmed proactive transcript append
	// idempotent across retries and restarts. Provider projection strips it.
	OutboundDeliveryID string `json:"outbound_delivery_id,omitempty"`

	// TurnEnvelope is canonical-session-only, provider-neutral context frozen
	// when a root turn is admitted. Provider projection operates on a copy and
	// canonical sanitization removes this sidecar before provider adapters,
	// hooks, diagnostics, or presentation layers can observe it.
	TurnEnvelope *TurnEnvelope `json:"turn_envelope,omitempty"`

	// Prompt metadata is internal to the agent runtime. It records where a
	// message or system part came from without changing provider/session JSON.
	PromptLayer    string `json:"-"`
	PromptSlot     string `json:"-"`
	PromptSource   string `json:"-"`
	InboundSpoolID string `json:"-"`
	// CodingSteerID correlates a local coding frontend's accepted guidance
	// with its later durable context injection. It is never persisted or sent
	// to a provider.
	CodingSteerID string `json:"-"`
	// LiveToolContextID identifies one in-memory tool-result occurrence whose
	// extra context must be consumed after a model call. Provider call IDs may
	// be reused by later batches; this identity is never persisted or sent.
	LiveToolContextID string `json:"-"`
	// SteeringSenderID preserves the admission scope of an in-memory steering
	// message when a suspended turn returns it to the runtime queue.
	SteeringSenderID string `json:"-"`
}

const TurnEnvelopeVersion1 = 1

// TurnEnvelope is a versioned, ordered carrier for the hidden context that
// belongs to one admitted root turn. Content remains separate from the user
// message so canonical search and presentation continue to use Message.Content.
type TurnEnvelope struct {
	Version int                `json:"version"`
	Parts   []TurnEnvelopePart `json:"parts,omitempty"`
}

// Clone returns a detached copy suitable for crossing a session boundary.
func (e *TurnEnvelope) Clone() *TurnEnvelope {
	if e == nil {
		return nil
	}
	cloned := *e
	cloned.Parts = append([]TurnEnvelopePart(nil), e.Parts...)
	return &cloned
}

// TurnEnvelopePart is one stable, ordered fragment of frozen turn context.
// ID identifies the context source; Content is its exact replay text.
type TurnEnvelopePart struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

// ToolExecution is canonical-journal-only evidence that a tool invocation
// crossed the durable start boundary. It deliberately stores no model-authored
// argument values. RecoveryResult is an optional bounded, tool-owned structural
// result that makes an accepted external effect observable after a crash;
// providers receive copies with all of this metadata removed.
type ToolExecution struct {
	CallIDHash     string    `json:"call_id_hash"`
	Tool           string    `json:"tool"`
	State          string    `json:"state"`
	StartedAt      time.Time `json:"started_at"`
	RecoveryResult string    `json:"recovery_result,omitempty"`
}

// ToolResultStatus records whether a persisted tool result is safe to compact.
// Empty means unknown and must be treated conservatively.
type ToolResultStatus string

const (
	ToolResultStatusSuccess     ToolResultStatus = "success"
	ToolResultStatusError       ToolResultStatus = "error"
	ToolResultStatusUnresolved  ToolResultStatus = "unresolved"
	ToolResultStatusInterrupted ToolResultStatus = "interrupted"
	ToolResultStatusUnknown     ToolResultStatus = "unknown"
)

type ToolDefinition struct {
	Type     string                 `json:"type"`
	Function ToolFunctionDefinition `json:"function"`

	// Prompt metadata is internal to the agent runtime. Tool definitions are
	// model-visible capability prompts even though providers send them outside
	// the system message.
	PromptLayer  string `json:"-"`
	PromptSlot   string `json:"-"`
	PromptSource string `json:"-"`
}

type ToolFunctionDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}
