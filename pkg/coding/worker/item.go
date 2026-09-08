package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

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
	CallID          string       `json:"call_id"`
	Name            string       `json:"name"`
	Arguments       string       `json:"arguments,omitempty"`
	Output          string       `json:"output,omitempty"`
	Status          ToolStatus   `json:"status"`
	Duration        int64        `json:"duration_ns,omitempty"`
	OutputTruncated bool         `json:"output_truncated,omitempty"`
	WriteAudit      []WriteAudit `json:"write_audit,omitempty"`
	Command         *Command     `json:"command,omitempty"`
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
	ID       string   `json:"id"`
	TurnID   string   `json:"turn_id"`
	Sequence uint64   `json:"sequence"`
	Revision uint64   `json:"revision"`
	Message  *Message `json:"message,omitempty"`
	Tool     *Tool    `json:"tool,omitempty"`
	Plan     *Plan    `json:"plan,omitempty"`
}

// SnapshotFromFrontend copies the bounded protocol-v1 projection from the
// in-process frontend snapshot. It retains the newest canonical items that fit
// both the item-count and encoded-size budgets.
func SnapshotFromFrontend(source frontend.ThreadSnapshot) Snapshot {
	source = source.Clone()
	snapshot := Snapshot{
		ThreadID:       source.ThreadID,
		ActiveTurnID:   optionalBoundedWireIdentity(source.ActiveTurnID),
		Activity:       Activity(source.Activity),
		ItemsTruncated: source.HasOlderEntries,
		ContextUsage: ContextUsage{
			UsedTokens:  source.ContextUsage.UsedTokens,
			LimitTokens: source.ContextUsage.LimitTokens,
		},
		Status: source.Status,
	}
	if source.LastTurn != nil {
		snapshot.LastTurn = &LastTurnOutcome{
			TurnID:  boundedWireIdentity(source.LastTurn.TurnID),
			Outcome: TurnOutcome(source.LastTurn.Outcome),
		}
	}
	// Account for the encoded JSON array brackets up front, then for the
	// comma before every item after the first.
	usedBytes := 2
	for index := len(source.Items) - 1; index >= 0; index-- {
		item := itemFromFrontend(source.Items[index])
		encoded, err := json.Marshal(item)
		if err != nil || len(encoded) > MaxSnapshotItemsBytes {
			snapshot.ItemsTruncated = true
			break
		}
		separatorBytes := 0
		if len(snapshot.Items) != 0 {
			separatorBytes = 1
		}
		if len(snapshot.Items) >= MaxSnapshotItems ||
			usedBytes+separatorBytes+len(encoded) > MaxSnapshotItemsBytes {
			snapshot.ItemsTruncated = true
			break
		}
		snapshot.Items = append(snapshot.Items, item)
		usedBytes += separatorBytes + len(encoded)
	}
	slices.Reverse(snapshot.Items)
	return snapshot
}

func itemFromFrontend(source frontend.PresentationItem) Item {
	item := Item{
		ID:       boundedWireIdentity(source.ID),
		TurnID:   boundedWireIdentity(source.TurnID),
		Sequence: source.Sequence,
		Revision: source.Revision,
	}
	if source.Message != nil {
		item.Message = &Message{
			Kind:      MessageKind(source.Message.Kind),
			Phase:     AssistantPhase(source.Message.Phase),
			Text:      source.Message.Text,
			Complete:  source.Message.Complete,
			Truncated: source.Message.Truncated,
		}
	}
	if source.Tool != nil {
		item.Tool = toolFromFrontend(*source.Tool)
	}
	if source.Plan != nil {
		item.Plan = planFromFrontend(*source.Plan)
	}
	return item
}

func toolFromFrontend(source frontend.ToolState) *Tool {
	tool := &Tool{
		CallID:          boundedWireIdentity(source.CallID),
		Name:            source.Name,
		Arguments:       source.Arguments,
		Output:          source.Output,
		Status:          ToolStatus(source.Status),
		Duration:        int64(source.Duration),
		OutputTruncated: source.OutputTruncated,
		WriteAudit:      make([]WriteAudit, len(source.WriteAudit)),
	}
	for index, audit := range source.WriteAudit {
		tool.WriteAudit[index] = WriteAudit{
			Kind:    audit.Kind,
			Target:  audit.Target,
			Action:  audit.Action,
			Success: audit.Success,
			Tool:    audit.Tool,
		}
	}
	if source.Command != nil {
		tool.Command = &Command{
			Stdout:     source.Command.Stdout,
			Stderr:     source.Command.Stderr,
			Output:     source.Command.Output,
			Status:     CommandStatus(source.Command.Status),
			SessionID:  source.Command.SessionID,
			Truncated:  source.Command.Truncated,
			Background: source.Command.Background,
			Canceled:   source.Command.Canceled,
			TimedOut:   source.Command.TimedOut,
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
		CallID: boundedWireIdentity(source.CallID), Explanation: source.Explanation,
		Steps: make([]PlanStep, len(source.Steps)), Truncated: source.Truncated,
	}
	for index, step := range source.Steps {
		plan.Steps[index] = PlanStep{Step: step.Step, Status: PlanStepStatus(step.Status)}
	}
	return plan
}

func boundedWireIdentity(value string) string {
	if validItemIdentity(value) {
		return value
	}
	digest := sha256.Sum256([]byte(value))
	return "wire:" + hex.EncodeToString(digest[:])
}

func optionalBoundedWireIdentity(value string) string {
	if value == "" {
		return ""
	}
	return boundedWireIdentity(value)
}
