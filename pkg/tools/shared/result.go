package toolshared

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
)

const (
	HandledToolLLMNote   = "The requested output has already been delivered to the user in the current chat. Do not call send_file or any other delivery tool again. If you reply, provide only a brief confirmation."
	ArtifactPathsLLMNote = "Use `send_file` with one of these paths to send it to the user, or use file/exec tools to save it inside the workspace if requested."
)

type AsyncDeliveryMode string

const (
	AsyncDeliveryUserOnly      AsyncDeliveryMode = "user_only"
	AsyncDeliveryParentOnly    AsyncDeliveryMode = "parent_only"
	AsyncDeliveryUserAndParent AsyncDeliveryMode = "user_and_parent"
)

type DeliveryIntent string

const (
	DeliveryDefault           DeliveryIntent = ""
	DeliveryImmediateContinue DeliveryIntent = "immediate_continue"
	DeliveryFinalHandled      DeliveryIntent = "final_handled"
	DeliverySilent            DeliveryIntent = "silent"
)

// ToolResult is the stable output produced by a tool. Runtime control and
// delivery directives are grouped separately so output cannot acquire
// contradictory execution or routing flags.
type ToolResult struct {
	// ForLLM is the content sent to the LLM for context.
	// Required for all results.
	ForLLM string `json:"for_llm"`

	// ForUser is the content sent directly to the user.
	// If empty, no user message is sent.
	ForUser string `json:"for_user,omitempty"`

	// IsError indicates whether the tool execution failed.
	// When true, the result should be treated as an error.
	IsError bool `json:"is_error"`

	// Err is the underlying error (not JSON serialized).
	// Used for internal error handling and logging.
	Err error `json:"-"`

	// Media contains media store refs produced by this tool.
	// When non-empty, the agent will publish these as OutboundMediaMessage.
	Media []string `json:"media,omitempty"`

	// ContextMedia contains media refs exposed only to the current model turn.
	// Unlike Media, these refs are neither promoted to deliverable artifacts nor
	// persisted in canonical tool-result history.
	ContextMedia []string `json:"-"`

	// Deliverable describes the actual artifact/result produced by the tool,
	// independent from LLM context or user-facing phrasing.
	Deliverable *taskresult.Deliverable `json:"deliverable,omitempty"`

	// WriteAudit records verified write-side effects performed by this tool.
	// Agents should use this as the source of truth for claims like "saved",
	// "updated", or "created"; prose in ForLLM/ForUser is only descriptive.
	WriteAudit []WriteAuditEntry `json:"write_audit,omitempty"`

	// Observation carries bounded tool-owned frontend state. It is neither
	// model-visible nor persisted in canonical tool-result history.
	Observation *ToolObservation `json:"-"`

	Control  ToolControl  `json:"control,omitzero"`
	Delivery ToolDelivery `json:"delivery,omitzero"`
}

// ToolControl carries runtime execution directives. It is not produced output
// and must not be interpreted by presentation or delivery code.
type ToolControl struct {
	// Async means the tool will complete later through its callback.
	Async bool `json:"async,omitempty"`

	// TaskID links an asynchronous completion to its durable task record.
	TaskID string `json:"task_id,omitempty"`

	// TaskSuspended prevents a caller from publishing completion while durable
	// continuation ownership remains with a descendant task.
	TaskSuspended bool `json:"-"`

	// Suspension asks the runtime to durably pause this tool call for human
	// input. The runtime enriches it with trusted route and origin data.
	Suspension *interactions.SuspensionRequest `json:"-"`

	// ResolveSuspension performs the bounded domain transition after the
	// interaction is answered, times out, is canceled, or fails.
	ResolveSuspension func(context.Context, interactions.Outcome) error `json:"-"`
}

// ToolDelivery is the single routing directive consumed by delivery code.
// Intent replaces the former overlapping silent/handled/immediate flags.
type ToolDelivery struct {
	Intent DeliveryIntent `json:"intent,omitempty"`

	// AsyncMode controls where an asynchronous completion is routed. Empty
	// means the runtime default.
	AsyncMode AsyncDeliveryMode `json:"async_mode,omitempty"`

	// Outbound is a fully resolved chat output for the delivery coordinator.
	Outbound *OutboundDelivery `json:"outbound,omitempty"`

	// Commit durably records that the prepared outbound attempt is being
	// scheduled after journaling and immediately before synchronous delivery.
	Commit func(context.Context) error `json:"-"`

	// Confirm records bookkeeping valid only after remote acceptance.
	Confirm func() `json:"-"`
}

func (delivery ToolDelivery) IsFinalHandled() bool {
	return delivery.Intent == DeliveryFinalHandled
}

func (delivery ToolDelivery) IsImmediate() bool {
	return delivery.Intent == DeliveryImmediateContinue
}

func (delivery ToolDelivery) IsSilent() bool {
	return delivery.Intent == DeliverySilent
}

// SuppressesImplicitUserOutput reports whether delivery has explicit ownership
// of user routing. The ordinary ForUser path runs only for the default intent.
func (delivery ToolDelivery) SuppressesImplicitUserOutput() bool {
	return delivery.Intent != DeliveryDefault
}

// ToolObservation is a bounded structured observation produced by a tool.
// Exactly one pointer is populated. Pointer variants keep the union open to
// future repository and process observations without exposing arbitrary tool
// output to frontends.
type ToolObservation struct {
	Command *CommandObservation
	Plan    *PlanObservation
}

// CommandObservation describes command output and process lifecycle without
// requiring a frontend to parse ForLLM or ForUser prose.
type CommandObservation struct {
	Stdout     string
	Stderr     string
	Output     string
	Truncated  bool
	Background bool
	Canceled   bool
	TimedOut   bool
	SessionID  string
	Status     string
	ExitCode   *int
}

// PlanStepStatus is one of the validated update_plan lifecycle states.
type PlanStepStatus string

const (
	PlanStepPending    PlanStepStatus = "pending"
	PlanStepInProgress PlanStepStatus = "in_progress"
	PlanStepCompleted  PlanStepStatus = "completed"
)

// PlanStepObservation is one bounded, ordered plan step.
type PlanStepObservation struct {
	Step   string
	Status PlanStepStatus
}

// PlanObservation is trusted presentation input produced from a validated
// update_plan call. It is not reconstructed from ForLLM or tool arguments.
type PlanObservation struct {
	Explanation string
	Steps       []PlanStepObservation
	Truncated   bool
}

type OutboundDelivery struct {
	Channel          string                `json:"channel,omitempty"`
	ChatID           string                `json:"chat_id,omitempty"`
	ReplyToMessageID string                `json:"reply_to_message_id,omitempty"`
	Text             string                `json:"text,omitempty"`
	Media            []bus.MediaPart       `json:"media,omitempty"`
	Recovery         *bus.OutboundRecovery `json:"recovery,omitempty"`
}

type WriteAuditEntry struct {
	Kind     string            `json:"kind"`
	Target   string            `json:"target"`
	Action   string            `json:"action"`
	Success  bool              `json:"success"`
	Tool     string            `json:"tool,omitempty"`
	Summary  string            `json:"summary,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// ObjectiveSpec is a caller-declared objective that the runtime binds to a
// stable checklist ID before a configured child runs. It verifies the declared
// contract; it does not infer omitted intent from free-form text.
type ObjectiveSpec struct {
	Item       string                          `json:"item"`
	Kind       string                          `json:"kind"`
	Acceptance *taskresult.ObjectiveAcceptance `json:"acceptance,omitempty"`
}

// ContentForLLM returns the normalized textual content to append to the
// conversation after a tool call. Errors fall back to Err when ForLLM is empty.
func (tr *ToolResult) ContentForLLM() string {
	if tr == nil {
		return ""
	}
	content := tr.ForLLM
	if content == "" && tr.Err != nil {
		content = tr.Err.Error()
	}
	if tr.Delivery.IsFinalHandled() {
		if content == "" {
			return HandledToolLLMNote
		}
		if !strings.Contains(content, HandledToolLLMNote) {
			content += "\n" + HandledToolLLMNote
		}
	}
	if artifactTags := deliverableArtifactTags(tr.Deliverable); len(artifactTags) > 0 {
		artifactNote := "Local artifact paths: " + strings.Join(artifactTags, " ") + "\n" + ArtifactPathsLLMNote
		if content == "" {
			content = artifactNote
		} else if !strings.Contains(content, artifactNote) {
			content += "\n" + artifactNote
		}
	}
	content = appendUniqueLLMNote(content, tr.deliverableNoteForLLM())
	if content != "" {
		return content
	}
	return ""
}

func deliverableArtifactTags(deliverable *taskresult.Deliverable) []string {
	if deliverable == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(deliverable.Artifacts))
	tags := make([]string, 0, len(deliverable.Artifacts))
	for _, artifact := range deliverable.Artifacts {
		path := strings.TrimSpace(artifact.LocalPath)
		if path == "" && strings.HasPrefix(artifact.Ref, "file:") {
			path = strings.TrimSpace(strings.TrimPrefix(artifact.Ref, "file:"))
		}
		if path == "" {
			continue
		}
		kind := strings.ToLower(strings.TrimSpace(artifact.Kind))
		switch kind {
		case "image", "audio", "video":
		default:
			kind = "file"
		}
		tag := "[" + kind + ":" + path + "]"
		if _, exists := seen[tag]; exists {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	return tags
}

func appendUniqueLLMNote(content, note string) string {
	if note == "" {
		return content
	}
	if content == "" {
		return note
	}
	if !strings.Contains(content, note) {
		return content + "\n" + note
	}
	return content
}

func (tr *ToolResult) deliverableNoteForLLM() string {
	if tr == nil || tr.Deliverable == nil {
		return ""
	}
	payload := tr.Deliverable
	if strings.TrimSpace(payload.Text) == "" &&
		len(payload.Artifacts) == 0 &&
		len(payload.Metadata) == 0 &&
		payload.Report == nil &&
		payload.ObjectiveOutcome == nil {
		return ""
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return "Structured deliverable: " + string(data)
}

// WithWriteAudit appends a verified write-side effect to the result.
func (tr *ToolResult) WithWriteAudit(entry WriteAuditEntry) *ToolResult {
	if tr == nil {
		return tr
	}
	entry.Kind = strings.TrimSpace(entry.Kind)
	entry.Target = strings.TrimSpace(entry.Target)
	entry.Action = strings.TrimSpace(entry.Action)
	entry.Tool = strings.TrimSpace(entry.Tool)
	entry.Summary = strings.TrimSpace(entry.Summary)
	if entry.Kind == "" {
		entry.Kind = "file"
	}
	if entry.Action == "" {
		entry.Action = "write"
	}
	entry.Success = true
	tr.WriteAudit = append(tr.WriteAudit, entry)
	return tr
}

// WithObservation attaches a tool-owned frontend observation.
func (tr *ToolResult) WithObservation(command CommandObservation) *ToolResult {
	if tr == nil {
		return tr
	}
	tr.Observation = SanitizeToolObservation(&ToolObservation{Command: &command})
	return tr
}

// WithFileWriteAudit records a successful file write/edit side effect.
func (tr *ToolResult) WithFileWriteAudit(path, action, toolName string) *ToolResult {
	return tr.WithWriteAudit(WriteAuditEntry{
		Kind:    "file",
		Target:  path,
		Action:  action,
		Tool:    toolName,
		Success: true,
	})
}

// NewToolResult creates a basic ToolResult with content for the LLM.
// Use this when you need a simple result with default behavior.
//
// Example:
//
//	result := NewToolResult("File updated successfully")
func NewToolResult(forLLM string) *ToolResult {
	return &ToolResult{
		ForLLM: forLLM,
	}
}

// SilentResult creates a ToolResult that is silent (no user message).
// The content is only sent to the LLM for context.
//
// Use this for operations that should not spam the user, such as:
// - File reads/writes
// - Status updates
// - Background operations
//
// Example:
//
//	result := SilentResult("Config file saved")
func SilentResult(forLLM string) *ToolResult {
	return &ToolResult{
		ForLLM:   forLLM,
		IsError:  false,
		Delivery: ToolDelivery{Intent: DeliverySilent},
	}
}

// AsyncResult creates a ToolResult for async operations.
// The task will run in the background and complete later.
//
// Use this for long-running operations like:
// - Subagent spawns
// - Background processing
// - External API calls with callbacks
//
// Example:
//
//	result := AsyncResult("Subagent spawned, will report back")
func AsyncResult(forLLM string) *ToolResult {
	return &ToolResult{
		ForLLM:  forLLM,
		IsError: false,
		Control: ToolControl{Async: true},
	}
}

// ErrorResult creates a ToolResult representing an error.
// Sets IsError=true and includes the error message.
//
// Example:
//
//	result := ErrorResult("Failed to connect to database: connection refused")
func ErrorResult(message string) *ToolResult {
	return &ToolResult{
		ForLLM:  message,
		IsError: true,
	}
}

// UserResult creates a ToolResult with content for both LLM and user.
// Both ForLLM and ForUser are set to the same content.
//
// Use this when the user needs to see the result directly:
// - Command execution output
// - Fetched web content
// - Query results
//
// Example:
//
//	result := UserResult("Total files found: 42")
func UserResult(content string) *ToolResult {
	return &ToolResult{
		ForLLM:  content,
		ForUser: content,
		IsError: false,
	}
}

// MediaResult creates a ToolResult with media refs for the user.
// The agent will publish these refs as OutboundMediaMessage.
//
// Example:
//
//	result := MediaResult("Image generated successfully", []string{"media://abc123"})
func MediaResult(forLLM string, mediaRefs []string) *ToolResult {
	result := &ToolResult{
		ForLLM: forLLM,
		Media:  mediaRefs,
	}
	if len(mediaRefs) > 0 {
		result.Deliverable = &taskresult.Deliverable{}
		for _, ref := range mediaRefs {
			result.Deliverable.Artifacts = append(result.Deliverable.Artifacts, taskresult.Artifact{
				Ref: ref,
			})
		}
	}
	return result
}

// WithError sets the Err field and returns the result for chaining.
// This preserves the error for logging while keeping it out of JSON.
//
// Example:
//
//	result := ErrorResult("Operation failed").WithError(err)
func (tr *ToolResult) WithError(err error) *ToolResult {
	tr.Err = err
	return tr
}

func (tr *ToolResult) WithDeliveryIntent(intent DeliveryIntent) *ToolResult {
	tr.Delivery.Intent = intent
	return tr
}

func (tr *ToolResult) WithOutboundDelivery(outbound OutboundDelivery) *ToolResult {
	tr.Delivery.Outbound = &outbound
	return tr
}

func (tr *ToolResult) WithOutboundCommit(commit func(context.Context) error) *ToolResult {
	tr.Delivery.Commit = commit
	return tr
}

// WithAsyncDelivery sets the async delivery policy for this tool result.
func (tr *ToolResult) WithAsyncDelivery(mode AsyncDeliveryMode) *ToolResult {
	tr.Delivery.AsyncMode = mode
	return tr
}

// WithTaskID links this result to a durable task registry record.
func (tr *ToolResult) WithTaskID(taskID string) *ToolResult {
	tr.Control.TaskID = strings.TrimSpace(taskID)
	return tr
}

// WithDeliverable attaches a generic durable output payload to this result.
func (tr *ToolResult) WithDeliverable(deliverable *taskresult.Deliverable) *ToolResult {
	tr.Deliverable = deliverable
	return tr
}
