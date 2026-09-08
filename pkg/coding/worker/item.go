package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

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

type Command struct {
	Action      string                   `json:"action,omitempty"`
	Command     string                   `json:"command,omitempty"`
	CWD         string                   `json:"cwd,omitempty"`
	Input       string                   `json:"input,omitempty"`
	Source      CommandSource            `json:"source,omitempty"`
	Stdout      string                   `json:"stdout,omitempty"`
	Stderr      string                   `json:"stderr,omitempty"`
	Output      string                   `json:"output,omitempty"`
	Transcript  []CommandTranscriptEntry `json:"transcript,omitempty"`
	Duration    int64                    `json:"duration_ns,omitempty"`
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

type Tool struct {
	CallID          string       `json:"call_id"`
	Name            string       `json:"name"`
	Arguments       string       `json:"arguments,omitempty"`
	Output          string       `json:"output,omitempty"`
	Status          ToolStatus   `json:"status"`
	Duration        int64        `json:"duration_ns,omitempty"`
	Truncated       bool         `json:"truncated,omitempty"`
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
// both the item-count and encoded-size budgets after accounting for the actual
// encoded pending question.
func SnapshotFromFrontend(source frontend.ThreadSnapshot, question *QuestionState) Snapshot {
	source = source.Clone()
	status, _ := boundedWireContent(source.Status, MaxStatusBytes)
	snapshot := Snapshot{
		ThreadID:       source.ThreadID,
		ActiveTurnID:   optionalBoundedWireIdentity(source.ActiveTurnID),
		Activity:       Activity(source.Activity),
		ItemsTruncated: source.HasOlderEntries,
		ContextUsage: ContextUsage{
			UsedTokens:  source.ContextUsage.UsedTokens,
			LimitTokens: source.ContextUsage.LimitTokens,
		},
		Status: status,
	}
	if question != nil {
		clonedQuestion := *question
		clonedQuestion.Options = slices.Clone(question.Options)
		snapshot.Question = &clonedQuestion
	}
	if source.LastTurn != nil {
		snapshot.LastTurn = &LastTurnOutcome{
			TurnID:  boundedWireIdentity(source.LastTurn.TurnID),
			Outcome: TurnOutcome(source.LastTurn.Outcome),
		}
	}
	itemsBudget := snapshotItemsBudget(snapshot)
	// Account for the encoded JSON array brackets up front, then for the comma
	// before every item after the first.
	usedBytes := 2
	for index := len(source.Items) - 1; index >= 0; index-- {
		item := itemFromFrontend(source.Items[index])
		encoded, err := json.Marshal(item)
		if err != nil || len(encoded) > itemsBudget {
			snapshot.ItemsTruncated = true
			break
		}
		separatorBytes := 0
		if len(snapshot.Items) != 0 {
			separatorBytes = 1
		}
		if len(snapshot.Items) >= MaxSnapshotItems ||
			usedBytes+separatorBytes+len(encoded) > itemsBudget {
			snapshot.ItemsTruncated = true
			break
		}
		snapshot.Items = append(snapshot.Items, item)
		usedBytes += separatorBytes + len(encoded)
	}
	slices.Reverse(snapshot.Items)
	return snapshot
}

func snapshotItemsBudget(snapshot Snapshot) int {
	snapshot.Items = nil
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return 0
	}
	// Items are omitted from the base snapshot. Account for the comma, key,
	// colon, and array that are added when at least one item is present.
	const itemsMemberBytes = len(`,"items":`)
	return max(0, MaxSnapshotBytes-len(encoded)-itemsMemberBytes)
}

func itemFromFrontend(source frontend.PresentationItem) Item {
	item := Item{
		ID:       boundedWireIdentity(source.ID),
		TurnID:   boundedWireIdentity(source.TurnID),
		Sequence: source.Sequence,
		Revision: source.Revision,
	}
	if source.Message != nil {
		text, truncated := boundedWireContent(source.Message.Text, MaxEventTextBytes)
		item.Message = &Message{
			Kind:      MessageKind(source.Message.Kind),
			Phase:     AssistantPhase(source.Message.Phase),
			Text:      text,
			Complete:  source.Message.Complete,
			Truncated: source.Message.Truncated || truncated,
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
	name, nameTruncated := boundedWireStructural(source.Name, MaxAttachmentMeta)
	if name == "" {
		name = "unknown"
		nameTruncated = true
	}
	arguments, argumentsTruncated := boundedWireContent(source.Arguments, MaxEventTextBytes)
	output, outputTruncated := boundedWireContent(source.Output, MaxEventTextBytes)
	tool := &Tool{
		CallID:          boundedWireIdentity(source.CallID),
		Name:            name,
		Arguments:       arguments,
		Output:          output,
		Status:          ToolStatus(source.Status),
		Duration:        max(0, int64(source.Duration)),
		Truncated:       nameTruncated || argumentsTruncated,
		OutputTruncated: source.OutputTruncated || outputTruncated,
		WriteAudit:      make([]WriteAudit, 0, min(len(source.WriteAudit), MaxEventWriteAudits)),
	}
	if len(source.WriteAudit) > MaxEventWriteAudits {
		tool.Truncated = true
	}
	for index, audit := range source.WriteAudit {
		if index >= MaxEventWriteAudits {
			break
		}
		kind, kindTruncated := boundedWireStructural(audit.Kind, MaxAttachmentMeta)
		target, targetTruncated := boundedWireStructural(audit.Target, MaxAuditTargetBytes)
		action, actionTruncated := boundedWireStructural(audit.Action, MaxAttachmentMeta)
		auditTool, toolTruncated := boundedWireStructural(audit.Tool, MaxAttachmentMeta)
		if kind == "" || target == "" || action == "" {
			tool.Truncated = true
			continue
		}
		tool.Truncated = tool.Truncated || kindTruncated || targetTruncated || actionTruncated || toolTruncated
		tool.WriteAudit = append(tool.WriteAudit, WriteAudit{
			Kind:    kind,
			Target:  target,
			Action:  action,
			Success: audit.Success,
			Tool:    auditTool,
		})
	}
	if source.Command != nil {
		action, actionTruncated := boundedWireStructural(source.Command.Action, MaxAttachmentMeta)
		commandText, commandTruncated := boundedWireContent(source.Command.Command, MaxEventTextBytes)
		cwd, cwdTruncated := boundedWireCommandCWD(source.Command.CWD)
		input, inputTruncated := boundedWireContent(source.Command.Input, MaxEventTextBytes)
		stdout, stdoutTruncated := boundedWireContent(source.Command.Stdout, MaxEventTextBytes)
		stderr, stderrTruncated := boundedWireContent(source.Command.Stderr, MaxEventTextBytes)
		commandOutput, commandOutputTruncated := boundedWireContent(source.Command.Output, MaxEventTextBytes)
		sessionID, sessionIDTruncated := boundedWireStructural(source.Command.SessionID, MaxAttachmentMeta)
		sourceName, sourceTruncated := commandSourceFromFrontend(source.Command.Source)
		transcript, transcriptTruncated := commandTranscriptFromFrontend(source.Command.Transcript)
		tool.Command = &Command{
			Action:     action,
			Command:    commandText,
			CWD:        cwd,
			Input:      input,
			Source:     sourceName,
			Stdout:     stdout,
			Stderr:     stderr,
			Output:     commandOutput,
			Transcript: transcript,
			Duration:   max(0, int64(source.Command.Duration)),
			Status:     CommandStatus(source.Command.Status),
			SessionID:  sessionID,
			Truncated: source.Command.Truncated || actionTruncated || commandTruncated || cwdTruncated ||
				inputTruncated || stdoutTruncated || stderrTruncated || commandOutputTruncated ||
				sessionIDTruncated || sourceTruncated || transcriptTruncated,
			Background:  source.Command.Background,
			OwnsProcess: source.Command.OwnsProcess,
			Orphan:      source.Command.Orphan,
			Canceled:    source.Command.Canceled,
			TimedOut:    source.Command.TimedOut,
		}
		if source.Command.ExitCode != nil {
			exitCode := *source.Command.ExitCode
			tool.Command.ExitCode = &exitCode
		}
	}
	return tool
}

func boundedWireCommandCWD(source string) (string, bool) {
	bounded, truncated := boundedWireStructural(source, MaxPathBytes)
	if bounded != "" && !validPath(bounded) {
		return "", true
	}
	return bounded, truncated
}

func commandSourceFromFrontend(source frontend.CommandSource) (CommandSource, bool) {
	switch source {
	case frontend.CommandSourceAgent:
		return CommandSourceAgent, false
	case frontend.CommandSourceUserShell:
		return CommandSourceUserShell, false
	case "":
		return "", false
	default:
		return "", true
	}
}

func commandTranscriptFromFrontend(source []frontend.CommandTranscriptEntry) ([]CommandTranscriptEntry, bool) {
	result := make([]CommandTranscriptEntry, 0, min(len(source), MaxCommandTranscript))
	remaining := MaxEventTextBytes
	truncated := len(source) > MaxCommandTranscript
	for index, entry := range source {
		if index >= MaxCommandTranscript || remaining <= 0 {
			truncated = true
			break
		}
		stream, streamTruncated := boundedWireStructural(entry.Stream, MaxAttachmentMeta)
		if stream == "" {
			stream = "unknown"
			streamTruncated = true
		}
		text, textTruncated := boundedWireContent(entry.Text, remaining)
		truncated = truncated || streamTruncated || textTruncated
		remaining -= len(text)
		if text == "" {
			continue
		}
		result = append(result, CommandTranscriptEntry{Sequence: entry.Sequence, Stream: stream, Text: text})
	}
	return result, truncated
}

func planFromFrontend(source frontend.PlanState) *Plan {
	explanation, explanationTruncated := boundedWireContent(source.Explanation, MaxPlanExplanationBytes)
	plan := &Plan{
		CallID: boundedWireIdentity(source.CallID), Explanation: explanation,
		Steps:     make([]PlanStep, 0, min(len(source.Steps), MaxEventPlanSteps)),
		Truncated: source.Truncated || explanationTruncated || len(source.Steps) > MaxEventPlanSteps,
	}
	for index, step := range source.Steps {
		if index >= MaxEventPlanSteps {
			break
		}
		text, truncated := boundedWireContent(step.Step, MaxPlanStepBytes)
		plan.Truncated = plan.Truncated || truncated
		plan.Steps = append(plan.Steps, PlanStep{Step: text, Status: PlanStepStatus(step.Status)})
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

func boundedWireContent(value string, maximum int) (string, bool) {
	original := value
	value = strings.ToValidUTF8(value, "�")
	value = strings.Map(func(character rune) rune {
		if character == '\n' || character == '\r' || character == '\t' {
			return character
		}
		if character == '\x1b' || unicode.IsControl(character) {
			return '�'
		}
		return character
	}, value)
	value, truncated := truncateWireText(value, maximum, "\n… worker projection truncated …")
	return value, truncated || value != original
}

func boundedWireStructural(value string, maximum int) (string, bool) {
	original := value
	value = strings.ToValidUTF8(value, "�")
	value = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, value)
	value = strings.TrimSpace(value)
	value, truncated := truncateWireText(value, maximum, "…")
	return value, truncated || value != original
}

func truncateWireText(value string, maximum int, marker string) (string, bool) {
	if maximum <= 0 || len(value) <= maximum {
		return value, false
	}
	if maximum <= len(marker) {
		return marker[:validUTF8PrefixEnd(marker, maximum)], true
	}
	end := validUTF8PrefixEnd(value, maximum-len(marker))
	return value[:end] + marker, true
}

func validUTF8PrefixEnd(value string, end int) int {
	end = min(max(0, end), len(value))
	for end > 0 && end < len(value) && !utf8.RuneStart(value[end]) {
		end--
	}
	return end
}
