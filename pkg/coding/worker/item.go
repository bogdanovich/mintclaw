package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

// Activity is the protocol-v1 worker lifecycle projection.
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

type LastTurnOutcome struct {
	TurnID  string      `json:"turn_id"`
	Outcome TurnOutcome `json:"outcome"`
}

type ContextUsage struct {
	UsedTokens  int `json:"used_tokens,omitempty"`
	LimitTokens int `json:"limit_tokens,omitempty"`
}

type ItemKind string

const (
	ItemUserMessage      ItemKind = "user_message"
	ItemAssistantMessage ItemKind = "assistant_message"
	ItemReasoning        ItemKind = "reasoning"
	ItemToolMessage      ItemKind = "tool_message"
	ItemToolCall         ItemKind = "tool_call"
	ItemPlanUpdate       ItemKind = "plan_update"
	ItemWarning          ItemKind = "warning"
	ItemError            ItemKind = "error"
)

type ItemLifecycle string

const (
	ItemActive      ItemLifecycle = "active"
	ItemCompleted   ItemLifecycle = "completed"
	ItemFailed      ItemLifecycle = "failed"
	ItemInterrupted ItemLifecycle = "interrupted"
	ItemSuspended   ItemLifecycle = "suspended"
	ItemUnknown     ItemLifecycle = "unknown"
)

type MessageKind string

const (
	MessageUser      MessageKind = "user"
	MessageAssistant MessageKind = "assistant"
	MessageReasoning MessageKind = "reasoning"
	MessageTool      MessageKind = "tool"
	MessageWarning   MessageKind = "warning"
	MessageError     MessageKind = "error"
)

type AssistantPhase string

const (
	AssistantPhaseCommentary AssistantPhase = "commentary"
	AssistantPhaseFinal      AssistantPhase = "final"
)

type Message struct {
	ID        string         `json:"id"`
	TurnID    string         `json:"turn_id"`
	Kind      MessageKind    `json:"kind"`
	Phase     AssistantPhase `json:"phase,omitempty"`
	Text      string         `json:"text"`
	Complete  bool           `json:"complete"`
	Truncated bool           `json:"truncated,omitempty"`
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

type WriteAudit struct {
	Kind    string `json:"kind"`
	Target  string `json:"target"`
	Action  string `json:"action"`
	Success bool   `json:"success"`
	Tool    string `json:"tool,omitempty"`
}

type CommandStatus string

const (
	CommandRunning   CommandStatus = "running"
	CommandSucceeded CommandStatus = "succeeded"
	CommandFailed    CommandStatus = "failed"
	CommandCanceled  CommandStatus = "canceled"
	CommandTimedOut  CommandStatus = "timed_out"
)

type Command struct {
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

type Tool struct {
	TurnID          string        `json:"turn_id"`
	CallID          string        `json:"call_id"`
	Name            string        `json:"name"`
	Arguments       string        `json:"arguments,omitempty"`
	Output          string        `json:"output,omitempty"`
	Status          ToolStatus    `json:"status"`
	Duration        time.Duration `json:"duration,omitempty"`
	OutputTruncated bool          `json:"output_truncated,omitempty"`
	WriteAudit      []WriteAudit  `json:"write_audit,omitempty"`
	Command         *Command      `json:"command,omitempty"`
}

type PlanStepStatus string

const (
	PlanStepPending    PlanStepStatus = "pending"
	PlanStepInProgress PlanStepStatus = "in_progress"
	PlanStepCompleted  PlanStepStatus = "completed"
)

type PlanStep struct {
	Step   string         `json:"step"`
	Status PlanStepStatus `json:"status"`
}

type Plan struct {
	CallID      string     `json:"call_id"`
	Explanation string     `json:"explanation,omitempty"`
	Steps       []PlanStep `json:"steps"`
	Truncated   bool       `json:"truncated,omitempty"`
}

// Item is the closed protocol-v1 renderer-neutral presentation unit. It is
// intentionally distinct from frontend.PresentationItem so frontend growth
// cannot silently change an already negotiated wire revision.
type Item struct {
	ID          string        `json:"id"`
	TurnID      string        `json:"turn_id"`
	Sequence    uint64        `json:"sequence"`
	Revision    uint64        `json:"revision"`
	Kind        ItemKind      `json:"kind"`
	Lifecycle   ItemLifecycle `json:"lifecycle"`
	CreatedAt   time.Time     `json:"created_at"`
	StartedAt   time.Time     `json:"started_at"`
	CompletedAt *time.Time    `json:"completed_at,omitempty"`
	Duration    time.Duration `json:"duration,omitempty"`
	Message     *Message      `json:"message,omitempty"`
	Tool        *Tool         `json:"tool,omitempty"`
	Plan        *Plan         `json:"plan,omitempty"`
}

// SnapshotFromFrontend copies only the protocol-v1 projection from the
// in-process frontend snapshot.
func SnapshotFromFrontend(source frontend.ThreadSnapshot) Snapshot {
	source = source.Clone()
	snapshot := Snapshot{
		ThreadID: source.ThreadID,
		Activity: Activity(source.Activity),
		Items:    make([]Item, len(source.Items)),
		ContextUsage: ContextUsage{
			UsedTokens: source.ContextUsage.UsedTokens, LimitTokens: source.ContextUsage.LimitTokens,
		},
		Status: source.Status,
	}
	if source.LastTurn != nil {
		snapshot.LastTurn = &LastTurnOutcome{
			TurnID: source.LastTurn.TurnID, Outcome: TurnOutcome(source.LastTurn.Outcome),
		}
	}
	for index, item := range source.Items {
		snapshot.Items[index] = itemFromFrontend(item)
	}
	return snapshot
}

func itemFromFrontend(source frontend.PresentationItem) Item {
	item := Item{
		TurnID: source.TurnID, Sequence: source.Sequence, Revision: source.Revision,
		Kind: ItemKind(source.Kind), Lifecycle: ItemLifecycle(source.Lifecycle),
		CreatedAt: source.CreatedAt, StartedAt: source.StartedAt, Duration: source.Duration,
	}
	if source.CompletedAt != nil {
		completedAt := *source.CompletedAt
		item.CompletedAt = &completedAt
	}
	if source.Message != nil {
		item.Message = &Message{
			ID: source.Message.ID, TurnID: source.Message.TurnID,
			Kind: MessageKind(source.Message.Kind), Phase: AssistantPhase(source.Message.Phase),
			Text: source.Message.Text, Complete: source.Message.Complete, Truncated: source.Message.Truncated,
		}
	}
	if source.Tool != nil {
		item.Tool = toolFromFrontend(*source.Tool)
	}
	if source.Plan != nil {
		item.Plan = planFromFrontend(*source.Plan)
	}
	item.ID = canonicalItemID(item)
	return item
}

func toolFromFrontend(source frontend.ToolState) *Tool {
	tool := &Tool{
		TurnID: source.TurnID, CallID: source.CallID, Name: source.Name,
		Arguments: source.Arguments, Output: source.Output, Status: ToolStatus(source.Status),
		Duration: source.Duration, OutputTruncated: source.OutputTruncated,
		WriteAudit: make([]WriteAudit, len(source.WriteAudit)),
	}
	for index, audit := range source.WriteAudit {
		tool.WriteAudit[index] = WriteAudit{
			Kind: audit.Kind, Target: audit.Target, Action: audit.Action, Success: audit.Success, Tool: audit.Tool,
		}
	}
	if source.Command != nil {
		tool.Command = &Command{
			Stdout: source.Command.Stdout, Stderr: source.Command.Stderr, Output: source.Command.Output,
			Status: CommandStatus(source.Command.Status), SessionID: source.Command.SessionID,
			Truncated: source.Command.Truncated, Background: source.Command.Background,
			Canceled: source.Command.Canceled, TimedOut: source.Command.TimedOut,
		}
		if source.Command.ExitCode != nil {
			exitCode := *source.Command.ExitCode
			tool.Command.ExitCode = &exitCode
		}
	}
	return tool
}

func planFromFrontend(source frontend.PlanState) *Plan {
	plan := &Plan{
		CallID: source.CallID, Explanation: source.Explanation,
		Steps: make([]PlanStep, len(source.Steps)), Truncated: source.Truncated,
	}
	for index, step := range source.Steps {
		plan.Steps[index] = PlanStep{Step: step.Step, Status: PlanStepStatus(step.Status)}
	}
	return plan
}

func canonicalItemID(item Item) string {
	switch {
	case item.Message != nil:
		return encodedItemID("message", item.Message.TurnID, item.Message.ID)
	case item.Tool != nil:
		return encodedItemID("tool", item.Tool.TurnID, item.Tool.CallID)
	case item.Plan != nil:
		return encodedItemID("plan", item.TurnID, item.Plan.CallID)
	default:
		return ""
	}
}

func encodedItemID(kind string, parts ...string) string {
	var result strings.Builder
	result.WriteString(kind)
	for _, part := range parts {
		result.WriteByte(':')
		result.WriteString(strconv.Itoa(len(part)))
		result.WriteByte(':')
		result.WriteString(part)
	}
	identity := result.String()
	if len(identity) <= MaxIDBytes {
		return identity
	}
	digest := sha256.Sum256([]byte(identity))
	return "item:" + hex.EncodeToString(digest[:])
}
