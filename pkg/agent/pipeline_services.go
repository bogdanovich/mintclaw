package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type turnAdmissionError struct {
	err error
}

func (e *turnAdmissionError) Error() string { return fmt.Sprintf("turn admission rejected: %v", e.err) }
func (e *turnAdmissionError) Unwrap() error { return e.err }

func persistFullSessionMessage(
	ctx context.Context,
	store session.SessionStore,
	sessionKey string,
	msg *providers.Message,
) error {
	assignCanonicalTimestamp(msg, time.Now())
	return store.AppendTurnMessage(ctx, sessionKey, *msg)
}

// canonicalMessageAppendCommitted distinguishes a durable append with a
// post-commit warning from pre-commit and indeterminate failures. Callers must
// still surface the warning, but must not roll back state already admitted to
// canonical history.
func canonicalMessageAppendCommitted(err error) bool {
	return err == nil || memory.IsCommittedAppendError(err)
}

func assignCanonicalTimestamp(msg *providers.Message, fallback time.Time) {
	if msg.CreatedAt != nil && !msg.CreatedAt.IsZero() {
		return
	}
	createdAt := fallback
	msg.CreatedAt = &createdAt
}

func assignCanonicalPairTimestamps(live, durable *providers.Message, fallback time.Time) {
	if durable.CreatedAt != nil && !durable.CreatedAt.IsZero() {
		createdAt := *durable.CreatedAt
		live.CreatedAt = &createdAt
		return
	}
	if live.CreatedAt != nil && !live.CreatedAt.IsZero() {
		createdAt := *live.CreatedAt
		durable.CreatedAt = &createdAt
		return
	}
	assignCanonicalTimestamp(live, fallback)
	assignCanonicalTimestamp(durable, fallback)
}

func assignCanonicalBatchTimestamps(live, durable []providers.Message) {
	now := time.Now()
	for i := range min(len(live), len(durable)) {
		assignCanonicalPairTimestamps(&live[i], &durable[i], now)
	}
}

func (p *Pipeline) ingestMessage(
	ctx context.Context,
	ts *turnState,
	msg providers.Message,
	canonicalWriteErr error,
) {
	if p == nil || ts == nil || p.Context.Runtime == nil {
		return
	}
	if canonicalWriteErr != nil {
		logger.WarnCF("agent", "Canonical session write failed before context ingest", map[string]any{
			"session_key": ts.sessionKey,
			"error":       canonicalWriteErr.Error(),
		})
	}
	if err := p.Context.Runtime.Ingest(ctx, &IngestRequest{
		Agent:             ts.agent,
		SessionKey:        ts.sessionKey,
		Message:           msg,
		CanonicalWriteErr: canonicalWriteErr,
	}); err != nil {
		logger.WarnCF("agent", "Context manager ingest failed", map[string]any{
			"session_key": ts.sessionKey,
			"error":       err.Error(),
		})
	}
}

func (p *Pipeline) scheduleBackgroundCompaction(
	agent *AgentInstance,
	sessionKey string,
	reason ContextCompressReason,
	budget int,
	messageKind string,
) {
	if p == nil || p.Context.BackgroundCompaction == nil {
		return
	}
	p.Context.BackgroundCompaction.scheduleBackgroundCompaction(
		agent,
		sessionKey,
		reason,
		budget,
		messageKind,
	)
}

func (p *Pipeline) dequeueSteeringMessagesForTurn(ts *turnState) []providers.Message {
	if p == nil || p.Context.Steering == nil || ts == nil {
		return nil
	}
	return p.Context.Steering.dequeueSteeringMessagesForTurn(
		ts.runtimeSessionScope(),
		ts.opts.Dispatch.SenderID(),
	)
}

func (p *Pipeline) drainSteeringMessagesForTurn(ts *turnState) []providers.Message {
	if p == nil || p.Context.Steering == nil || ts == nil {
		return nil
	}
	return p.Context.Steering.drainSteeringMessagesForTurn(
		ts.runtimeSessionScope(),
		ts.opts.Dispatch.SenderID(),
	)
}

// dequeueOrSealSteeringForTerminal performs the final queue observation under
// the same turn-owned gate used by active coding steering admission. Once it
// returns no messages, a concurrent coding steer must fail instead of being
// acknowledged after the turn has lost its last opportunity to consume it.
func (p *Pipeline) dequeueOrSealSteeringForTerminal(ts *turnState) []providers.Message {
	return p.dequeueOrSealSteeringAtExit(ts, false)
}

// dequeueOrSealSteeringAtExit linearizes a turn exit with both steering that
// is still queued and steering already transferred into the next iteration.
func (p *Pipeline) dequeueOrSealSteeringAtExit(
	ts *turnState,
	hasAdmittedSteering bool,
) []providers.Message {
	if ts == nil {
		return nil
	}
	ts.steeringAdmissionMu.Lock()
	defer ts.steeringAdmissionMu.Unlock()
	messages := p.dequeueSteeringMessagesForTurn(ts)
	if len(messages) == 0 && !hasAdmittedSteering {
		ts.steeringOpen = false
	}
	return messages
}

// openSteeringAdmission exposes a coding turn only after setup has completed
// and only if a hard cancellation did not win the same admission boundary.
func (p *Pipeline) openSteeringAdmission(ts *turnState) bool {
	if ts == nil {
		return false
	}
	ts.steeringAdmissionMu.Lock()
	defer ts.steeringAdmissionMu.Unlock()
	if ts.hardAbortRequested() {
		return false
	}
	ts.steeringOpen = true
	return true
}

// sealSteeringAdmission gives explicit cancellation and hard-abort exits
// priority over queued guidance while preventing any later acknowledgement.
func (p *Pipeline) sealSteeringAdmission(ts *turnState) {
	if ts == nil {
		return
	}
	ts.steeringAdmissionMu.Lock()
	ts.steeringOpen = false
	ts.steeringAdmissionMu.Unlock()
}

// drainAndSealSteeringAdmission is the fatal pending-input boundary. It moves
// every steer that was acknowledged before the gate closed into turn-owned
// settlement state and prevents any later acknowledgement.
func (p *Pipeline) drainAndSealSteeringAdmission(ts *turnState) []providers.Message {
	if ts == nil {
		return nil
	}
	ts.steeringAdmissionMu.Lock()
	defer ts.steeringAdmissionMu.Unlock()
	messages := p.drainSteeringMessagesForTurn(ts)
	ts.steeringOpen = false
	return messages
}

// dequeueOrSealSteeringForSuspension gives already-admitted guidance priority
// over creating durable human-interaction ownership. When no guidance is
// queued, admission closes before the suspension manager can commit, so a
// later steer cannot be acknowledged against a suspended turn.
func (p *Pipeline) dequeueOrSealSteeringForSuspension(ts *turnState) ([]providers.Message, bool) {
	if ts == nil || ts.opts.mode != turnModeCoding {
		return nil, false
	}
	ts.steeringAdmissionMu.Lock()
	defer ts.steeringAdmissionMu.Unlock()

	messages := p.dequeueSteeringMessagesForTurn(ts)
	if len(messages) > 0 {
		return messages, false
	}
	ts.steeringOpen = false
	return nil, true
}

func (p *Pipeline) returnSteeringMessagesForTurn(ts *turnState, messages []providers.Message) {
	if p == nil || p.Context.Steering == nil || ts == nil || len(messages) == 0 {
		return
	}
	p.Context.Steering.returnSteeringMessagesForTurn(ts.runtimeSessionScope(), messages)
}

func (p *Pipeline) updateAutoFallbackSelection(
	routeSessionKey string,
	selectedCandidates []providers.FallbackCandidate,
	result *providers.FallbackResult,
	usedLight bool,
) {
	if p == nil || p.Context.ModelExecution == nil {
		return
	}
	p.Context.ModelExecution.updateAutoFallbackSelection(
		routeSessionKey,
		selectedCandidates,
		result,
		usedLight,
	)
}

func (p *Pipeline) abortTurn(ts *turnState) (turnResult, error) {
	if p == nil || p.turnControl == nil {
		return turnResult{status: TurnEndStatusAborted}, nil
	}
	return p.turnControl.abortTurn(ts)
}

func (p *Pipeline) targetReasoningChannelID(channelName string) string {
	if p == nil || p.Interaction.Reasoning == nil {
		return ""
	}
	return p.Interaction.Reasoning.targetReasoningChannelID(channelName)
}

func (p *Pipeline) publishMintClawReasoning(
	ctx context.Context,
	reasoningContent, chatID, sessionKey, modelName string,
) {
	if p == nil || p.Interaction.Reasoning == nil {
		return
	}
	p.Interaction.Reasoning.publishMintClawReasoning(
		ctx,
		reasoningContent,
		chatID,
		sessionKey,
		modelName,
	)
}

func (p *Pipeline) publishMintClawToolCallInterim(
	ctx context.Context,
	ts *turnState,
	modelName string,
	reasoningContent string,
	content string,
	toolCalls []providers.ToolCall,
) {
	if p == nil || p.Interaction.Reasoning == nil {
		return
	}
	p.Interaction.Reasoning.publishMintClawToolCallInterim(
		ctx,
		ts,
		modelName,
		reasoningContent,
		content,
		toolCalls,
	)
}

func (p *Pipeline) shouldPublishToolFeedback(ts *turnState) bool {
	if p == nil || p.Interaction.ToolFeedback == nil {
		return false
	}
	return p.Interaction.ToolFeedback.shouldPublishToolFeedback(ts)
}

func (p *Pipeline) handleReasoning(
	ctx context.Context,
	reasoningContent, channelName, channelID string,
) {
	if p == nil || p.Interaction.Reasoning == nil {
		return
	}
	p.Interaction.Reasoning.handleReasoning(ctx, reasoningContent, channelName, channelID)
}

func (p *Pipeline) publishToolFeedbackForCall(
	ctx context.Context,
	ts *turnState,
	response *providers.LLMResponse,
	toolCall providers.ToolCall,
	toolName string,
	toolArgs map[string]any,
	messages []providers.Message,
) {
	if p == nil || p.Interaction.ToolFeedback == nil {
		return
	}
	p.Interaction.ToolFeedback.publishToolFeedbackForCall(
		ctx,
		ts,
		response,
		toolCall,
		toolName,
		toolArgs,
		messages,
	)
}

func (p *Pipeline) applySyncToolResultDelivery(
	ctx context.Context,
	ts *turnState,
	result *toolshared.ToolResult,
	toolName string,
) ([]providers.Attachment, *toolshared.ToolResult) {
	if p == nil || p.Interaction.SyncToolDelivery == nil {
		return nil, result
	}
	// An immediate delivery is interim: its outbound message transiently clears
	// the current carrier, while later tool calls in the same turn must remain
	// able to publish new feedback. Terminal results own terminal cleanup.
	return p.Interaction.SyncToolDelivery.applySyncToolResultDelivery(ctx, ts, result, toolName)
}

func (p *Pipeline) deliverAsyncToolCompletion(req AsyncDeliveryRequest) {
	if p == nil || p.Interaction.ToolDelivery == nil {
		return
	}
	p.Interaction.ToolDelivery.deliverAsyncToolCompletion(req)
}

func (p *Pipeline) dismissToolFeedbackForTurn(ctx context.Context, ts *turnState) {
	if p == nil || p.Interaction.ToolFeedback == nil {
		return
	}
	p.Interaction.ToolFeedback.dismissToolFeedbackForTurn(ctx, ts)
}

func (p *Pipeline) pauseToolFeedbackForTurn(ctx context.Context, ts *turnState) {
	if p == nil || p.Interaction.ToolFeedback == nil {
		return
	}
	p.Interaction.ToolFeedback.pauseToolFeedbackForTurn(ctx, ts)
}
