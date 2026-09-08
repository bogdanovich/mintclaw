package worker

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	MaxEventTextBytes       = 64 << 10
	MaxStatusBytes          = 4 << 10
	MaxSnapshotBytes        = MaxWirePayloadBytes - (2 << 10)
	MaxSnapshotItems        = 128
	MaxSnapshotItemsBytes   = MaxSnapshotBytes
	MaxEventWriteAudits     = 64
	MaxCommandTranscript    = 128
	MaxExplorationValue     = 1 << 10
	MaxAuditTargetBytes     = 4 << 10
	MaxEventPlanSteps       = 32
	MaxPlanExplanationBytes = 4 << 10
	MaxPlanStepBytes        = 768
	MaxQuestionOptions      = 32
	MaxQuestionTextBytes    = 8 << 10
	MaxItemIdentityBytes    = 4 << 10
)

type EventName string

const (
	EventWorkerReady   EventName = "worker.ready"
	EventItemUpdated   EventName = "item.updated"
	EventStatusChanged EventName = "status.changed"
	EventQuestionState EventName = "question.changed"
	EventContextUsage  EventName = "context.updated"
	EventTurnTerminal  EventName = "turn.terminal"
	EventWorkerStopped EventName = "worker.stopped"
)

func (event EventName) Valid() bool {
	switch event {
	case EventWorkerReady, EventItemUpdated, EventStatusChanged, EventQuestionState,
		EventContextUsage, EventTurnTerminal, EventWorkerStopped:
		return true
	default:
		return false
	}
}

// Snapshot is the bounded worker-wire projection of the authoritative
// frontend view. Repository, review, workspace, and compaction detail remain
// owned by their in-process domains.
type Snapshot struct {
	ThreadID       string           `json:"thread_id"`
	ActiveTurnID   string           `json:"active_turn_id,omitempty"`
	Activity       Activity         `json:"activity"`
	LastTurn       *LastTurnOutcome `json:"last_turn,omitempty"`
	Question       *QuestionState   `json:"question,omitempty"`
	Items          []Item           `json:"items,omitempty"`
	ItemsTruncated bool             `json:"items_truncated"`
	ContextUsage   ContextUsage     `json:"context_usage,omitempty"`
	Status         string           `json:"status,omitempty"`
}

// WorkerReadyPayload is emitted only after the bound controller and thread
// lease are ready to accept commands.
type WorkerReadyPayload struct {
	ControlIdentity
	Snapshot Snapshot `json:"snapshot"`
}

func (payload WorkerReadyPayload) Validate() error {
	if err := validateSnapshot(payload.ControlIdentity, payload.Snapshot); err != nil {
		return err
	}
	return validateEncodedSize("worker.ready payload", payload, MaxWirePayloadBytes)
}

// ItemUpdatedPayload carries one complete renderer-neutral item revision.
type ItemUpdatedPayload struct {
	ControlIdentity
	Item Item `json:"item"`
}

func (payload ItemUpdatedPayload) Validate() error {
	if err := payload.ControlIdentity.Validate(); err != nil {
		return err
	}
	if err := payload.Item.Validate(); err != nil {
		return err
	}
	return validateEncodedSize("item.updated payload", payload, MaxWirePayloadBytes)
}

type StatusChangedPayload struct {
	ControlIdentity
	Activity Activity `json:"activity"`
	Status   string   `json:"status"`
}

func (payload StatusChangedPayload) Validate() error {
	if err := payload.ControlIdentity.Validate(); err != nil {
		return err
	}
	if !validActivity(payload.Activity) || !validBoundedText(payload.Status, MaxStatusBytes) {
		return fmt.Errorf("%w: malformed coding status event", ErrInvalidRecord)
	}
	return validateEncodedSize("status.changed payload", payload, MaxWirePayloadBytes)
}

type QuestionStatus string

const (
	QuestionWaiting  QuestionStatus = "waiting"
	QuestionAnswered QuestionStatus = "answered"
	QuestionCanceled QuestionStatus = "canceled"
)

type QuestionOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// QuestionState is the complete versioned question identity exposed to the
// trusted parent. Answers correlate this identity separately.
type QuestionState struct {
	QuestionID string           `json:"question_id"`
	Revision   uint64           `json:"revision"`
	Status     QuestionStatus   `json:"status"`
	Prompt     string           `json:"prompt"`
	Options    []QuestionOption `json:"options,omitempty"`
}

func (question QuestionState) Validate() error {
	if !validIdentifier(question.QuestionID) || question.Revision == 0 ||
		(question.Status != QuestionWaiting && question.Status != QuestionAnswered &&
			question.Status != QuestionCanceled) ||
		!validContentText(question.Prompt, MaxQuestionTextBytes, true) ||
		len(question.Options) > MaxQuestionOptions {
		return fmt.Errorf("%w: malformed coding question state", ErrInvalidRecord)
	}
	seen := make(map[string]struct{}, len(question.Options))
	for _, option := range question.Options {
		if !validIdentifier(option.ID) ||
			!validContentText(option.Label, MaxAttachmentMeta, true) ||
			!validContentText(option.Description, MaxQuestionTextBytes, false) {
			return fmt.Errorf("%w: malformed coding question option", ErrInvalidRecord)
		}
		if _, duplicate := seen[option.ID]; duplicate {
			return fmt.Errorf("%w: duplicate coding question option", ErrInvalidRecord)
		}
		seen[option.ID] = struct{}{}
	}
	return nil
}

type QuestionStatePayload struct {
	ControlIdentity
	Question QuestionState `json:"question"`
}

func (payload QuestionStatePayload) Validate() error {
	if err := payload.ControlIdentity.Validate(); err != nil {
		return err
	}
	if err := payload.Question.Validate(); err != nil {
		return err
	}
	return validateEncodedSize("question.changed payload", payload, MaxWirePayloadBytes)
}

type ContextUsagePayload struct {
	ControlIdentity
	Usage ContextUsage `json:"usage"`
}

func (payload ContextUsagePayload) Validate() error {
	if err := payload.ControlIdentity.Validate(); err != nil {
		return err
	}
	if !validContextUsage(payload.Usage) {
		return fmt.Errorf("%w: malformed coding context usage", ErrInvalidRecord)
	}
	return validateEncodedSize("context.updated payload", payload, MaxWirePayloadBytes)
}

type TurnTerminalPayload struct {
	ControlIdentity
	TurnID  string      `json:"turn_id"`
	Outcome TurnOutcome `json:"outcome"`
	Status  string      `json:"status"`
}

func (payload TurnTerminalPayload) Validate() error {
	if err := payload.ControlIdentity.Validate(); err != nil {
		return err
	}
	if !validItemIdentity(payload.TurnID) || !validTurnOutcome(payload.Outcome) ||
		!validContentText(payload.Status, MaxStatusBytes, true) {
		return fmt.Errorf("%w: malformed coding terminal event", ErrInvalidRecord)
	}
	return validateEncodedSize("turn.terminal payload", payload, MaxWirePayloadBytes)
}

type WorkerStopReason string

const (
	WorkerStopCompleted WorkerStopReason = "completed"
	WorkerStopShutdown  WorkerStopReason = "shutdown"
	WorkerStopIdle      WorkerStopReason = "idle_timeout"
	WorkerStopCanceled  WorkerStopReason = "canceled"
	WorkerStopFailed    WorkerStopReason = "failed"
)

type WorkerStoppedPayload struct {
	ControlIdentity
	Reason WorkerStopReason `json:"reason"`
	Error  *ProtocolError   `json:"error,omitempty"`
}

func (payload WorkerStoppedPayload) Validate() error {
	if err := payload.ControlIdentity.Validate(); err != nil {
		return err
	}
	switch payload.Reason {
	case WorkerStopCompleted, WorkerStopShutdown, WorkerStopIdle, WorkerStopCanceled:
		if payload.Error != nil {
			return fmt.Errorf("%w: successful worker stop contains an error", ErrInvalidRecord)
		}
	case WorkerStopFailed:
		if payload.Error == nil {
			return fmt.Errorf("%w: failed worker stop requires an error", ErrInvalidRecord)
		}
		if err := payload.Error.Validate(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: malformed worker stop reason", ErrInvalidRecord)
	}
	return validateEncodedSize("worker.stopped payload", payload, MaxWirePayloadBytes)
}

// DecodeEventPayload applies the closed-world schema owned by each event.
func DecodeEventPayload(event EventName, raw json.RawMessage) (any, error) {
	if !event.Valid() {
		return nil, fmt.Errorf("%w: unsupported event payload %q", ErrInvalidRecord, event)
	}
	if err := validateProtocolText(raw); err != nil {
		return nil, err
	}
	var payload interface{ Validate() error }
	switch event {
	case EventWorkerReady:
		payload = &WorkerReadyPayload{}
	case EventItemUpdated:
		payload = &ItemUpdatedPayload{}
	case EventStatusChanged:
		payload = &StatusChangedPayload{}
	case EventQuestionState:
		payload = &QuestionStatePayload{}
	case EventContextUsage:
		payload = &ContextUsagePayload{}
	case EventTurnTerminal:
		payload = &TurnTerminalPayload{}
	case EventWorkerStopped:
		payload = &WorkerStoppedPayload{}
	}
	if err := DecodePayload(raw, payload); err != nil {
		return nil, err
	}
	if err := payload.Validate(); err != nil {
		return nil, fmt.Errorf("%w: invalid %s event payload: %w", ErrInvalidRecord, event, err)
	}
	return payload, nil
}

func validateSnapshot(identity ControlIdentity, snapshot Snapshot) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	parsed, err := uuid.Parse(snapshot.ThreadID)
	if err != nil || parsed.String() != snapshot.ThreadID ||
		!validActivity(snapshot.Activity) ||
		(snapshot.ActiveTurnID != "" && !validItemIdentity(snapshot.ActiveTurnID)) ||
		!validContentText(snapshot.Status, MaxStatusBytes, false) {
		return fmt.Errorf("%w: malformed coding worker snapshot", ErrInvalidRecord)
	}
	if snapshot.LastTurn != nil &&
		(!validItemIdentity(snapshot.LastTurn.TurnID) ||
			!validTurnOutcome(snapshot.LastTurn.Outcome)) {
		return fmt.Errorf("%w: malformed coding worker snapshot terminal state", ErrInvalidRecord)
	}
	if snapshot.Question != nil {
		if snapshot.Question.Status != QuestionWaiting {
			return fmt.Errorf("%w: malformed pending coding worker question", ErrInvalidRecord)
		}
		if validationErr := snapshot.Question.Validate(); validationErr != nil {
			return fmt.Errorf(
				"%w: malformed pending coding worker question: %w",
				ErrInvalidRecord,
				validationErr,
			)
		}
	}
	if len(snapshot.Items) > MaxSnapshotItems {
		return fmt.Errorf("%w: coding worker snapshot exceeds item limit", ErrInvalidRecord)
	}
	encodedItems, err := json.Marshal(snapshot.Items)
	if err != nil || len(encodedItems) > MaxSnapshotItemsBytes {
		return fmt.Errorf("%w: coding worker snapshot exceeds byte limit", ErrInvalidRecord)
	}
	seen := make(map[string]struct{}, len(snapshot.Items))
	var previousSequence uint64
	for _, item := range snapshot.Items {
		if err := item.Validate(); err != nil {
			return err
		}
		if _, duplicate := seen[item.ID]; duplicate {
			return fmt.Errorf("%w: duplicate coding snapshot item identity", ErrInvalidRecord)
		}
		if previousSequence != 0 && item.Sequence <= previousSequence {
			return fmt.Errorf("%w: coding snapshot item sequence is not increasing", ErrInvalidRecord)
		}
		seen[item.ID] = struct{}{}
		previousSequence = item.Sequence
	}
	if !validContextUsage(snapshot.ContextUsage) {
		return fmt.Errorf("%w: malformed coding snapshot context usage", ErrInvalidRecord)
	}
	return validateEncodedSize("coding worker snapshot", snapshot, MaxSnapshotBytes)
}

func (item Item) Validate() error {
	if !validItemIdentity(item.ID) || !validItemIdentity(item.TurnID) ||
		item.Sequence == 0 || item.Revision == 0 {
		return fmt.Errorf("%w: malformed coding item identity", ErrInvalidRecord)
	}
	payloads := 0
	if item.Message != nil {
		payloads++
		if !validMessage(*item.Message) {
			return fmt.Errorf("%w: malformed coding message item", ErrInvalidRecord)
		}
	}
	if item.Tool != nil {
		payloads++
		if !validTool(*item.Tool) {
			return fmt.Errorf("%w: malformed coding tool item", ErrInvalidRecord)
		}
	}
	if item.Plan != nil {
		payloads++
		if !validPlan(*item.Plan) {
			return fmt.Errorf("%w: malformed coding plan item", ErrInvalidRecord)
		}
	}
	if payloads != 1 {
		return fmt.Errorf("%w: coding item requires exactly one typed payload", ErrInvalidRecord)
	}
	return nil
}

func validMessage(message Message) bool {
	switch message.Kind {
	case MessageUser, MessageReasoning, MessageTool, MessageWarning, MessageError:
		if message.Phase != "" {
			return false
		}
	case MessageAssistant:
		if message.Phase != "" &&
			message.Phase != AssistantPhaseCommentary &&
			message.Phase != AssistantPhaseFinal {
			return false
		}
	default:
		return false
	}
	return validContentText(message.Text, MaxEventTextBytes, false)
}

func validTool(tool Tool) bool {
	if !validItemIdentity(tool.CallID) ||
		!validBoundedText(tool.Name, MaxAttachmentMeta) ||
		!validContentText(tool.Arguments, MaxEventTextBytes, false) ||
		!validContentText(tool.Output, MaxEventTextBytes, false) ||
		tool.Duration < 0 || len(tool.WriteAudit) > MaxEventWriteAudits {
		return false
	}
	switch tool.Status {
	case ToolRunning, ToolSuspended, ToolSucceeded, ToolFailed, ToolInterrupted, ToolUnknown:
	default:
		return false
	}
	for _, audit := range tool.WriteAudit {
		if !validBoundedText(audit.Kind, MaxAttachmentMeta) ||
			!validBoundedText(audit.Target, MaxAuditTargetBytes) ||
			!validBoundedText(audit.Action, MaxAttachmentMeta) ||
			!validOptionalText(audit.Tool, MaxAttachmentMeta) {
			return false
		}
	}
	if tool.Exploration != nil {
		if tool.Command != nil || tool.RepositoryDiff != nil || !validExploration(*tool.Exploration) {
			return false
		}
	}
	if tool.RepositoryDiff != nil {
		if tool.Command != nil || !validRepositoryDiff(*tool.RepositoryDiff) {
			return false
		}
	}
	if tool.Command == nil {
		return true
	}
	command := tool.Command
	if !validOptionalText(command.Action, MaxAttachmentMeta) ||
		!validContentText(command.Command, MaxEventTextBytes, false) ||
		!validOptionalPath(command.CWD) ||
		!validContentText(command.Input, MaxEventTextBytes, false) ||
		!validContentText(command.Stdout, MaxEventTextBytes, false) ||
		!validContentText(command.Stderr, MaxEventTextBytes, false) ||
		!validContentText(command.Output, MaxEventTextBytes, false) ||
		!validOptionalText(command.SessionID, MaxAttachmentMeta) ||
		command.Duration < 0 || len(command.Transcript) > MaxCommandTranscript {
		return false
	}
	if command.Source != "" && command.Source != CommandSourceAgent && command.Source != CommandSourceUserShell {
		return false
	}
	transcriptBytes := 0
	for _, entry := range command.Transcript {
		if !validBoundedText(entry.Stream, MaxAttachmentMeta) ||
			!validContentText(entry.Text, MaxEventTextBytes, true) {
			return false
		}
		transcriptBytes += len(entry.Text)
		if transcriptBytes > MaxEventTextBytes {
			return false
		}
	}
	switch command.Status {
	case CommandUnknown, CommandRunning, CommandSucceeded, CommandFailed, CommandCanceled, CommandTimedOut:
		return true
	default:
		return false
	}
}

func validExploration(exploration Exploration) bool {
	switch exploration.Operation {
	case ExplorationRead, ExplorationList, ExplorationSearch:
	default:
		return false
	}
	return validOptionalText(exploration.Path, MaxExplorationValue) &&
		validOptionalText(exploration.Pattern, MaxExplorationValue) &&
		validOptionalText(exploration.Workspace, MaxExplorationValue)
}

func validPlan(plan Plan) bool {
	if !validItemIdentity(plan.CallID) || len(plan.Steps) == 0 ||
		len(plan.Steps) > MaxEventPlanSteps ||
		!validContentText(plan.Explanation, MaxPlanExplanationBytes, false) {
		return false
	}
	inProgress := 0
	for _, step := range plan.Steps {
		if !validContentText(step.Step, MaxPlanStepBytes, true) {
			return false
		}
		switch step.Status {
		case PlanStepPending, PlanStepCompleted:
		case PlanStepInProgress:
			inProgress++
		default:
			return false
		}
	}
	return inProgress <= 1
}

func validActivity(activity Activity) bool {
	switch activity {
	case ActivityIdle, ActivityRunning, ActivityInterrupting, ActivityCompacting,
		ActivityReviewing, ActivityWaitingInput, ActivityFailed:
		return true
	default:
		return false
	}
}

func validTurnOutcome(outcome TurnOutcome) bool {
	switch outcome {
	case TurnOutcomeCompleted, TurnOutcomeSuspended, TurnOutcomeFailed, TurnOutcomeInterrupted:
		return true
	default:
		return false
	}
}

func validContextUsage(usage ContextUsage) bool {
	return usage.UsedTokens >= 0 && usage.LimitTokens >= 0
}

func validItemIdentity(value string) bool {
	return validBoundedText(value, MaxItemIdentityBytes)
}

func validContentText(value string, maximum int, required bool) bool {
	if len(value) > maximum || !utf8.ValidString(value) || containsTerminalControl(value) {
		return false
	}
	return !required || strings.TrimSpace(value) != ""
}

type SnapshotResult struct {
	ControlIdentity
	Snapshot Snapshot `json:"snapshot"`
}

func (result SnapshotResult) Validate() error {
	if err := validateSnapshot(result.ControlIdentity, result.Snapshot); err != nil {
		return err
	}
	return validateEncodedSize("snapshot.read result", result, MaxWirePayloadBytes)
}
