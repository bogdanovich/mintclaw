package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

// CodingInteractionQuestion is the bounded, route-free question projection
// exposed to the trusted native coding frontend. The interaction registry
// remains authoritative for correlation and continuation state.
type CodingInteractionQuestion struct {
	ID       string
	Revision uint64
	Status   CodingInteractionQuestionStatus
	Prompt   string
	Options  []CodingInteractionOption
}

type CodingInteractionQuestionStatus string

const (
	CodingInteractionWaiting  CodingInteractionQuestionStatus = "waiting"
	CodingInteractionAnswered CodingInteractionQuestionStatus = "answered"
	CodingInteractionCanceled CodingInteractionQuestionStatus = "canceled"
)

type CodingInteractionOption struct {
	ID          string
	Label       string
	Description string
}

// CodingInteractionAnswerContinuation resumes a question whose answer has
// already crossed the durable interaction-registry acceptance boundary.
type CodingInteractionAnswerContinuation interface {
	Resume(context.Context) error
}

type codingInteractionAnswerContinuation struct {
	loop      *AgentLoop
	registry  *interactions.Registry
	workspace string
	record    interactions.Record
	answerID  string
}

// CodingInteractionQuestion returns only the interaction owned by the exact
// local coding runtime scope. Chat routes and personal-agent interactions are
// never visible through this boundary.
func (al *AgentLoop) CodingInteractionQuestion(
	workspace string,
	sessionKey string,
) (*CodingInteractionQuestion, error) {
	record, found, err := al.codingInteractionRecord(workspace, sessionKey)
	if err != nil || !found {
		return nil, err
	}
	if record.Kind != interactions.KindQuestion || len(record.Questions) != 1 || record.Revision <= 0 {
		return nil, fmt.Errorf("coding interaction question is malformed")
	}
	status := CodingInteractionWaiting
	switch record.Status {
	case interactions.StatusCreated:
		// Delivery transitions the record to waiting and advances its durable
		// revision. Do not expose the pre-delivery generation as answerable.
		return nil, nil
	case interactions.StatusWaiting:
	case interactions.StatusClaimed, interactions.StatusResuming:
		status = CodingInteractionAnswered
	case interactions.StatusCanceling:
		status = CodingInteractionCanceled
	default:
		return nil, nil
	}
	question := record.Questions[0]
	prompt := strings.TrimSpace(question.Question)
	if header := strings.TrimSpace(question.Header); header != "" {
		prompt = header + "\n\n" + prompt
	}
	projected := &CodingInteractionQuestion{
		ID: record.ID, Revision: uint64(record.Revision), Status: status, Prompt: prompt,
		Options: make([]CodingInteractionOption, 0, len(question.Options)),
	}
	for index, option := range question.Options {
		projected.Options = append(projected.Options, CodingInteractionOption{
			ID:          fmt.Sprintf("option-%d", index+1),
			Label:       option.Label,
			Description: option.Description,
		})
	}
	return projected, nil
}

// ClaimCodingInteractionAnswer durably accepts one exact local coding answer
// and returns its continuation. The caller may acknowledge the answer only
// after this method succeeds; the potentially long continuation runs through
// the returned value.
func (al *AgentLoop) ClaimCodingInteractionAnswer(
	workspace string,
	sessionKey string,
	questionID string,
	questionRevision uint64,
	answerID string,
	text string,
) (CodingInteractionAnswerContinuation, error) {
	record, found, err := al.codingInteractionRecord(workspace, sessionKey)
	if err != nil {
		return nil, err
	}
	if !found || record.ID != questionID || record.Revision <= 0 ||
		uint64(record.Revision) != questionRevision || record.Status != interactions.StatusWaiting ||
		record.Kind != interactions.KindQuestion || len(record.Questions) != 1 {
		return nil, fmt.Errorf("coding interaction question identity changed")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("coding interaction answer is empty")
	}
	answerID = strings.TrimSpace(answerID)
	if answerID == "" {
		return nil, fmt.Errorf("coding interaction answer identity is required")
	}
	answer := interactions.Answer{
		Text: text,
		Values: map[string]string{
			record.Questions[0].ID: text,
		},
		MessageID:  answerID,
		ReceivedAt: time.Now().UnixMilli(),
	}
	registry := al.interactionRegistryForWorkspace(workspace)
	claimed, err := registry.ClaimAnswer(record.ID, record.Revision, answer, interactions.OutcomeAnswered)
	if err != nil {
		return nil, err
	}
	return &codingInteractionAnswerContinuation{
		loop:      al,
		registry:  registry,
		workspace: workspace,
		record:    claimed,
		answerID:  answerID,
	}, nil
}

func (continuation *codingInteractionAnswerContinuation) Resume(ctx context.Context) error {
	if continuation == nil || continuation.loop == nil || continuation.registry == nil {
		return fmt.Errorf("coding interaction continuation is unavailable")
	}
	record := continuation.record
	agentInstance := continuation.loop.agentForRuntimeScope(
		newRuntimeSessionScope(continuation.workspace, record.Route.SessionKey),
		record.Route.AgentID,
	)
	if agentInstance == nil {
		return fmt.Errorf("coding interaction continuation agent is unavailable")
	}
	scope := sessionScopeForRecovery(agentInstance.Sessions, record.Route.SessionKey)
	inbound := inboundContextForInteraction(record.Route)
	inbound.MessageID = continuation.answerID
	command, err := newResumeInteractionCommand(
		continuation.registry,
		continuation.workspace,
		agentInstance,
		scope,
		inbound,
		record,
	)
	if err != nil {
		return err
	}
	_, err = newInteractionService(continuation.loop).Resume(ctx, command)
	return err
}

// CancelCodingInteraction closes an outstanding local coding question before
// the worker process settles its canceled turn.
func (al *AgentLoop) CancelCodingInteraction(
	ctx context.Context,
	workspace string,
	sessionKey string,
) (bool, error) {
	record, found, err := al.codingInteractionRecord(workspace, sessionKey)
	if err != nil || !found {
		return found, err
	}
	registry := al.interactionRegistryForWorkspace(workspace)
	if record.Status != interactions.StatusCanceling {
		record, err = registry.BeginCancellation(record.ID, record.Revision, "coding_worker_canceled")
		if err != nil {
			return true, err
		}
	}
	agentInstance := al.agentForRuntimeScope(newRuntimeSessionScope(workspace, sessionKey), record.Route.AgentID)
	if agentInstance == nil {
		return true, fmt.Errorf("coding interaction continuation agent is unavailable")
	}
	continuationScope := newRuntimeSessionScope(
		agentInstance.Workspace,
		interactionContinuationSessionKey(record),
	)
	if al.turns.activeTurnState(continuationScope) != nil {
		_ = al.hardAbortScope(continuationScope)
	}
	if err = al.ensureInteractionCancellationToolResult(
		context.WithoutCancel(ctx),
		agentInstance,
		record,
		record.FailureCode,
	); err != nil {
		return true, err
	}
	current, ok := registry.Get(record.ID)
	if !ok {
		return true, interactions.ErrNotFound
	}
	if current.Status == interactions.StatusCancelled {
		return true, nil
	}
	completed, err := registry.CompleteCancellation(current.ID, current.Revision)
	if err != nil {
		return true, err
	}
	al.cleanupInteractionOriginTools(context.WithoutCancel(ctx), agentInstance, completed)
	return true, nil
}

func (al *AgentLoop) codingInteractionRecord(
	workspace string,
	sessionKey string,
) (interactions.Record, bool, error) {
	if al == nil || !al.usesCodingProfile() {
		return interactions.Record{}, false, fmt.Errorf("coding interaction runtime is unavailable")
	}
	workspace = normalizeRuntimeWorkspace(workspace)
	sessionKey = strings.TrimSpace(sessionKey)
	agentInstance, layout, err := al.codingRuntimeTargetForSession(sessionKey)
	if err != nil {
		return interactions.Record{}, false, err
	}
	if normalizeRuntimeWorkspace(agentInstance.Workspace) != workspace {
		return interactions.Record{}, false, fmt.Errorf("coding interaction workspace does not own the session")
	}
	registry := al.interactionRegistryForWorkspace(workspace)
	if registry == nil || registry.LastLoadError() != nil {
		return interactions.Record{}, false, interactions.ErrStoreUnavailable
	}
	record, found := activeInteractionForSession(registry, sessionKey)
	if !found {
		return interactions.Record{}, false, nil
	}
	if record.Route.AgentID != agentInstance.ID || record.Route.Channel != "coding" ||
		record.Route.ChatID != layout.ThreadID() || record.Route.SessionKey != sessionKey {
		return interactions.Record{}, false, fmt.Errorf("interaction is not owned by the coding runtime")
	}
	return record, true, nil
}
