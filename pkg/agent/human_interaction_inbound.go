package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/commands"
	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	taskregistry "github.com/bogdanovich/mintclaw/pkg/tasks"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

const answerCommand = "/answer"

const (
	interactionAnswerClaimAttempts = 8
	interactionAnswerClaimDelay    = 10 * time.Millisecond
	interactionCancelClaimAttempts = 100
	interactionCancelClaimDelay    = 10 * time.Millisecond
	interactionCancelFenceAttempts = 16
)

type interactionLifecycleBoundaryHook func(string)

type interactionLifecycleBoundaryHookKey struct{}

const (
	interactionBoundaryCancelAfterLoad  = "cancel_after_load"
	interactionBoundaryPrecomputedFinal = "precomputed_final"
	interactionBoundaryModelFinal       = "model_final"
	interactionBoundaryFinalPrepared    = "final_delivery_prepared"
	interactionBoundaryTaskCompleted    = "task_completion_persisted"
	interactionBoundaryResumeFlightRead = "resume_flight_read"
)

type interactionFinalizationDisposition uint8

const (
	interactionFinalizationDelivered interactionFinalizationDisposition = iota
	interactionFinalizationCanceled
)

var errInteractionFinalizationCanceled = errors.New(
	"interaction finalization was canceled before delivery",
)

func runInteractionLifecycleBoundaryHook(ctx context.Context, boundary string) {
	if ctx == nil {
		return
	}
	if hook, ok := ctx.Value(interactionLifecycleBoundaryHookKey{}).(interactionLifecycleBoundaryHook); ok &&
		hook != nil {
		hook(boundary)
	}
}

type interactionInboundOwnership int

const (
	interactionInboundCallerOwned interactionInboundOwnership = iota
	interactionInboundClaimed
	interactionInboundDeferred
)

type interactionControlCancellationResult struct {
	Matched        bool
	Canceled       bool
	Failed         bool
	CommandHandled bool
	TaskID         string
	Kind           interactions.Kind
}

type explicitInteractionAnswerDisposition string

const (
	explicitInteractionAnswerActive       explicitInteractionAnswerDisposition = "active"
	explicitInteractionAnswerReplay       explicitInteractionAnswerDisposition = "replay"
	explicitInteractionAnswerDuplicate    explicitInteractionAnswerDisposition = "duplicate"
	explicitInteractionAnswerWrongID      explicitInteractionAnswerDisposition = "wrong_id"
	explicitInteractionAnswerUnauthorized explicitInteractionAnswerDisposition = "unauthorized"
	explicitInteractionAnswerUnavailable  explicitInteractionAnswerDisposition = "unavailable"
	explicitInteractionAnswerRetry        explicitInteractionAnswerDisposition = "retry"
	explicitInteractionAnswerInvalid      explicitInteractionAnswerDisposition = "invalid"
)

type explicitInteractionAnswer struct {
	Record      interactions.Record
	Disposition explicitInteractionAnswerDisposition
}

func (al *AgentLoop) cancelInteractionForControlMessage(
	ctx context.Context,
	msg bus.InboundMessage,
	target *inboundDispatchTarget,
) (interactionControlCancellationResult, error) {
	result := interactionControlCancellationResult{}
	if strings.TrimSpace(msg.Context.Raw[bus.InboundMetadataKeyInteractionResponse]) != "" {
		return result, nil
	}
	name, ok := commands.CommandName(msg.Content)
	if strings.TrimSpace(msg.Context.Raw[bus.InboundMetadataKeyInteractionChoice]) ==
		bus.InboundInteractionChoiceCancel {
		name = "stop"
		ok = true
	}
	if !ok || (name != "new" && name != "reset" && name != "clear" && name != "stop") ||
		al == nil || target == nil || target.Agent == nil {
		return result, nil
	}
	registry := al.interactionRegistryForWorkspace(target.Agent.Workspace)
	record, found := activeInteractionForSession(registry, target.SessionKey)
	if !found || !interactionRouteAuthorizes(record.Route, target, msg.Context) {
		return result, nil
	}
	projectedChoice := strings.TrimSpace(msg.Context.Raw[bus.InboundMetadataKeyInteractionChoice])
	projectedShortID := strings.TrimSpace(msg.Context.Raw[bus.InboundMetadataKeyInteractionShortID])
	if projectedChoice == bus.InboundInteractionChoiceCancel && projectedShortID == "" {
		return result, nil
	}
	if projectedShortID != "" && !strings.EqualFold(projectedShortID, record.ShortID) {
		return result, nil
	}
	result.Matched = true
	result.TaskID = strings.TrimSpace(record.Origin.TaskID)
	result.Kind = record.Kind
	runInteractionLifecycleBoundaryHook(ctx, interactionBoundaryCancelAfterLoad)

	claimTurnID := fmt.Sprintf(
		"pending-interaction-cancel-%s-%d",
		record.ShortID,
		al.turnSeq.Add(1),
	)
	claim, _, claimed := al.claimRuntimeRouteSession(
		target,
		claimTurnID,
	)
	if !claimed {
		current, err := al.beginInteractionCancellationFence(
			registry, record, target, msg.Context, "session_control_"+name,
		)
		if err != nil {
			result.Failed = true
			return result, err
		}
		record = current
		result.TaskID = strings.TrimSpace(record.Origin.TaskID)
		result.Kind = record.Kind
		if err := al.abortInteractionContinuation(record, target); err != nil {
			result.Failed = true
			return result, fmt.Errorf("abort interaction continuation: %w", err)
		}
		claim, claimed = al.waitForInteractionCancellationClaim(ctx, target, claimTurnID)
	}
	if !claimed {
		result.Failed = true
		return result, fmt.Errorf("interaction session is busy while canceling")
	}
	defer claim.releaseIfOwned()

	current, active := activeInteractionForSession(registry, target.SessionKey)
	if !active || current.ID != record.ID ||
		!interactionRouteAuthorizes(current.Route, target, msg.Context) {
		result.Failed = true
		return result, fmt.Errorf("interaction changed while waiting to cancel")
	}
	record = current
	result.TaskID = strings.TrimSpace(record.Origin.TaskID)
	result.Kind = record.Kind
	if interactionFinalizationStarted(record) {
		result.Failed = true
		return result, fmt.Errorf("interaction finalization already started")
	}
	continuationAgent := al.interactionContinuationAgent(record, target.Agent)
	if continuationAgent != nil {
		al.takePendingStop(newRuntimeSessionScope(
			continuationAgent.Workspace,
			interactionContinuationSessionKey(record),
		))
	}

	if record.Status != interactions.StatusCanceling {
		var err error
		record, err = registry.BeginCancellation(
			record.ID,
			record.Revision,
			"session_control_"+name,
		)
		if err != nil {
			result.Failed = true
			return result, fmt.Errorf("begin %s cancellation: %w", name, err)
		}
	}
	al.syncInteractionControls(target.Agent.Workspace, record, bus.OutboundInteractionControlsRemove)
	if err := al.ensureInteractionCancellationToolResult(
		ctx,
		al.interactionContinuationAgent(record, target.Agent),
		record,
		record.FailureCode,
	); err != nil {
		result.Failed = true
		return result, fmt.Errorf("persist %s cancellation result: %w", name, err)
	}
	completed, err := registry.CompleteCancellation(record.ID, record.Revision)
	if err != nil {
		result.Failed = true
		return result, fmt.Errorf("complete %s cancellation: %w", name, err)
	}
	al.cleanupInteractionOriginTools(
		ctx,
		al.interactionContinuationAgent(completed, target.Agent),
		completed,
	)
	result.Canceled = true
	result.CommandHandled = name == "stop"
	return result, nil
}

func (al *AgentLoop) beginInteractionCancellationFence(
	registry *interactions.Registry,
	loaded interactions.Record,
	target *inboundDispatchTarget,
	inbound bus.InboundContext,
	code string,
) (interactions.Record, error) {
	for attempt := 0; attempt < interactionCancelFenceAttempts; attempt++ {
		current, active := activeInteractionForSession(registry, target.SessionKey)
		if !active || current.ID != loaded.ID ||
			!interactionRouteAuthorizes(current.Route, target, inbound) {
			return interactions.Record{}, fmt.Errorf("interaction changed while preparing cancellation")
		}
		if current.Status == interactions.StatusCanceling {
			return current, nil
		}
		if interactionFinalizationStarted(current) {
			return interactions.Record{}, fmt.Errorf("interaction finalization already started")
		}
		fenced, err := registry.BeginCancellation(current.ID, current.Revision, code)
		if err == nil {
			return fenced, nil
		}
		if !errors.Is(err, interactions.ErrConflict) {
			return interactions.Record{}, fmt.Errorf("begin cancellation fence: %w", err)
		}
	}
	return interactions.Record{}, fmt.Errorf("interaction kept changing while preparing cancellation")
}

func interactionFinalizationStarted(record interactions.Record) bool {
	return record.FinalDelivered ||
		record.FinalDeliveryState == interactions.DeliveryStateSending ||
		record.FinalDeliveryState == interactions.DeliveryStateDelivered ||
		record.FinalDeliveryState == interactions.DeliveryStateAmbiguous
}

func (al *AgentLoop) abortInteractionContinuation(
	record interactions.Record,
	target *inboundDispatchTarget,
) error {
	agent := al.interactionContinuationAgent(record, target.Agent)
	if agent == nil {
		return fmt.Errorf("interaction continuation agent is unavailable")
	}
	scope := newRuntimeSessionScope(
		agent.Workspace,
		interactionContinuationSessionKey(record),
	)
	ts := al.getActiveTurnState(scope)
	if ts == nil {
		al.markPendingStop(scope)
		return nil
	}
	if strings.HasPrefix(ts.snapshot().TurnID, pendingTurnPrefix) {
		al.markPendingStop(scope)
		return nil
	}
	if err := al.hardAbortScope(scope); err != nil && al.getActiveTurnState(scope) != nil {
		return err
	}
	return nil
}

func (al *AgentLoop) waitForInteractionCancellationClaim(
	ctx context.Context,
	target *inboundDispatchTarget,
	turnID string,
) (*runtimeSessionClaim, bool) {
	for attempt := 0; attempt < interactionCancelClaimAttempts; attempt++ {
		timer := time.NewTimer(interactionCancelClaimDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, false
		case <-timer.C:
		}
		claim, _, claimed := al.claimRuntimeRouteSession(target, turnID)
		if claimed {
			return claim, true
		}
	}
	return nil, false
}

func (al *AgentLoop) shouldHandleInteractionInbound(
	msg bus.InboundMessage,
	target *inboundDispatchTarget,
) bool {
	if al == nil || target == nil || target.Agent == nil {
		return false
	}
	registry := al.interactionRegistryForWorkspace(target.Agent.Workspace)
	if registry == nil {
		return false
	}
	if registry.LastLoadError() != nil {
		return true
	}
	record, ok := activeInteractionForSession(registry, target.SessionKey)
	if !ok || !interactionRouteAuthorizes(record.Route, target, msg.Context) {
		return false
	}
	_, _, answerCommandMatched, _ := parseInteractionAnswerEnvelope(msg.Content)
	if commands.HasCommandPrefix(msg.Content) && !answerCommandMatched {
		return false
	}
	switch record.Status {
	case interactions.StatusWaiting, interactions.StatusClaimed, interactions.StatusResuming:
		return true
	default:
		return false
	}
}

func (c *inboundTurnCoordinator) routeExplicitInteractionAnswer(
	ctx context.Context,
	msg bus.InboundMessage,
	target *inboundDispatchTarget,
) bool {
	classification, explicit := c.al.classifyExplicitInteractionAnswer(msg, target)
	if !explicit {
		return false
	}
	if classification.Disposition == explicitInteractionAnswerActive {
		c.handleInteractionInbound(ctx, msg, target)
		return true
	}
	c.consumeExplicitInteractionAnswer(ctx, msg, target, classification)
	return true
}

func (al *AgentLoop) classifyInteractionAnswer(
	msg bus.InboundMessage,
	target *inboundDispatchTarget,
) (explicitInteractionAnswer, bool) {
	if shortID, projected := projectedInteractionAnswer(msg); projected {
		return al.classifyProjectedInteractionAnswer(msg, target, shortID), true
	}
	if classification, explicit := al.classifyExplicitInteractionAnswer(msg, target); explicit {
		return classification, true
	}
	if !al.shouldHandleInteractionInbound(msg, target) {
		return explicitInteractionAnswer{}, false
	}
	registry := al.interactionRegistryForWorkspace(target.Agent.Workspace)
	record, ok := activeInteractionForSession(registry, target.SessionKey)
	if !ok {
		return explicitInteractionAnswer{}, false
	}
	if record.Status == interactions.StatusWaiting || interactionInboundReplaysAnswer(record, msg.Context) {
		return explicitInteractionAnswer{
			Record: record, Disposition: explicitInteractionAnswerActive,
		}, true
	}
	return explicitInteractionAnswer{}, false
}

func (c *inboundTurnCoordinator) routeProjectedInteractionAnswer(
	ctx context.Context,
	msg bus.InboundMessage,
	target *inboundDispatchTarget,
) bool {
	shortID, projected := projectedInteractionAnswer(msg)
	if !projected {
		return false
	}
	classification := c.al.classifyProjectedInteractionAnswer(msg, target, shortID)
	responseError := strings.TrimSpace(
		msg.Context.Raw[bus.InboundMetadataKeyInteractionResponseError],
	)
	if responseError != "" && classification.Disposition == explicitInteractionAnswerActive {
		logExplicitInteractionAnswerDisposition(
			classification.Record,
			msg,
			explicitInteractionAnswerInvalid,
		)
		_ = c.al.settleInboundAdmission(
			ctx,
			msg,
			finalResponseAdmission{status: finalResponseAdmissionNotRequired},
		)
		return true
	}
	if classification.Disposition == explicitInteractionAnswerActive {
		c.handleInteractionInbound(ctx, msg, target)
		return true
	}
	if shortID != "" && (classification.Disposition == explicitInteractionAnswerRetry ||
		classification.Disposition == explicitInteractionAnswerUnavailable) {
		logExplicitInteractionAnswerDisposition(classification.Record, msg, classification.Disposition)
		c.al.releaseInboundMessage(
			context.Background(),
			msg,
			fmt.Errorf("interaction answer identity is waiting for durable admission"),
		)
		return true
	}
	logExplicitInteractionAnswerDisposition(classification.Record, msg, classification.Disposition)
	_ = c.al.settleInboundAdmission(
		ctx,
		msg,
		finalResponseAdmission{status: finalResponseAdmissionNotRequired},
	)
	return true
}

func projectedInteractionAnswer(msg bus.InboundMessage) (string, bool) {
	if len(msg.Context.Raw) == 0 {
		return "", false
	}
	choice := strings.TrimSpace(msg.Context.Raw[bus.InboundMetadataKeyInteractionChoice])
	response := strings.TrimSpace(msg.Context.Raw[bus.InboundMetadataKeyInteractionResponse])
	responseError := strings.TrimSpace(msg.Context.Raw[bus.InboundMetadataKeyInteractionResponseError])
	if choice == "" && response == "" && responseError == "" {
		return "", false
	}
	return strings.TrimSpace(msg.Context.Raw[bus.InboundMetadataKeyInteractionShortID]), true
}

func (al *AgentLoop) classifyProjectedInteractionAnswer(
	msg bus.InboundMessage,
	target *inboundDispatchTarget,
	shortID string,
) explicitInteractionAnswer {
	if al == nil || target == nil || target.Agent == nil || shortID == "" {
		return explicitInteractionAnswer{Disposition: explicitInteractionAnswerUnavailable}
	}
	registry := al.interactionRegistryForWorkspace(target.Agent.Workspace)
	if registry == nil || registry.LastLoadError() != nil {
		return explicitInteractionAnswer{Disposition: explicitInteractionAnswerUnavailable}
	}
	var unauthorizedMatch interactions.Record
	for _, record := range registry.List() {
		if !strings.EqualFold(shortID, record.ShortID) {
			continue
		}
		if !interactionRouteAuthorizes(record.Route, target, msg.Context) {
			unauthorizedMatch = record
			continue
		}
		if interactionInboundReplaysAnswer(record, msg.Context) {
			return explicitInteractionAnswer{
				Record: record, Disposition: explicitInteractionAnswerReplay,
			}
		}
		switch record.Status {
		case interactions.StatusWaiting:
			return explicitInteractionAnswer{
				Record: record, Disposition: explicitInteractionAnswerActive,
			}
		case interactions.StatusCreated:
			return explicitInteractionAnswer{
				Record: record, Disposition: explicitInteractionAnswerRetry,
			}
		default:
			return explicitInteractionAnswer{
				Record: record, Disposition: explicitInteractionAnswerDuplicate,
			}
		}
	}
	if unauthorizedMatch.ID != "" {
		return explicitInteractionAnswer{
			Record: unauthorizedMatch, Disposition: explicitInteractionAnswerUnauthorized,
		}
	}
	return explicitInteractionAnswer{Disposition: explicitInteractionAnswerWrongID}
}

func (al *AgentLoop) classifyExplicitInteractionAnswer(
	msg bus.InboundMessage,
	target *inboundDispatchTarget,
) (explicitInteractionAnswer, bool) {
	shortID, answerText, explicit := splitExplicitInteractionAnswer(msg.Content)
	if !explicit {
		return explicitInteractionAnswer{}, false
	}
	if al == nil || target == nil || target.Agent == nil {
		return explicitInteractionAnswer{Disposition: explicitInteractionAnswerUnavailable}, true
	}
	registry := al.interactionRegistryForWorkspace(target.Agent.Workspace)
	if registry == nil || registry.LastLoadError() != nil {
		return explicitInteractionAnswer{Disposition: explicitInteractionAnswerUnavailable}, true
	}
	for _, record := range registry.List() {
		if strings.EqualFold(shortID, record.ShortID) &&
			interactionRouteAuthorizes(record.Route, target, msg.Context) &&
			interactionInboundReplaysAnswer(record, msg.Context) {
			return explicitInteractionAnswer{
				Record: record, Disposition: explicitInteractionAnswerReplay,
			}, true
		}
	}
	if record, ok := activeInteractionForSession(registry, target.SessionKey); ok {
		if !interactionRouteAuthorizes(record.Route, target, msg.Context) {
			return explicitInteractionAnswer{
				Record: record, Disposition: explicitInteractionAnswerUnauthorized,
			}, true
		}
		if shortID == "" || answerText == "" || !strings.EqualFold(shortID, record.ShortID) {
			return explicitInteractionAnswer{
				Record: record, Disposition: explicitInteractionAnswerWrongID,
			}, true
		}
		return explicitInteractionAnswer{
			Record: record, Disposition: explicitInteractionAnswerActive,
		}, true
	}
	var unauthorizedMatch interactions.Record
	for _, record := range registry.List() {
		if !strings.EqualFold(shortID, record.ShortID) {
			continue
		}
		if !interactionRouteAuthorizes(record.Route, target, msg.Context) {
			unauthorizedMatch = record
			continue
		}
		disposition := explicitInteractionAnswerDuplicate
		if interactionInboundReplaysAnswer(record, msg.Context) {
			disposition = explicitInteractionAnswerReplay
		}
		return explicitInteractionAnswer{Record: record, Disposition: disposition}, true
	}
	if unauthorizedMatch.ID != "" {
		return explicitInteractionAnswer{
			Record: unauthorizedMatch, Disposition: explicitInteractionAnswerUnauthorized,
		}, true
	}
	return explicitInteractionAnswer{Disposition: explicitInteractionAnswerWrongID}, true
}

func splitExplicitInteractionAnswer(content string) (string, string, bool) {
	shortID, answerText, matched, _ := parseInteractionAnswerEnvelope(content)
	return shortID, answerText, matched
}

func (c *inboundTurnCoordinator) consumeExplicitInteractionAnswer(
	ctx context.Context,
	msg bus.InboundMessage,
	target *inboundDispatchTarget,
	classification explicitInteractionAnswer,
) {
	disposition := classification.Disposition
	record := classification.Record
	logExplicitInteractionAnswerDisposition(record, msg, disposition)
	if disposition == explicitInteractionAnswerReplay {
		_ = c.al.settleInboundAdmission(
			ctx,
			msg,
			finalResponseAdmission{status: finalResponseAdmissionNotRequired},
		)
		return
	}
	sessionKey := target.SessionKey
	notice := "No matching pending interaction is accepting that answer."
	switch disposition {
	case explicitInteractionAnswerDuplicate:
		notice = "An answer has already been accepted for this interaction."
	case explicitInteractionAnswerWrongID:
		if record.ShortID != "" {
			notice = fmt.Sprintf("I could not accept that answer: use `/answer %s <answer>`", record.ShortID)
		}
	case explicitInteractionAnswerUnauthorized:
		notice = "This answer is not authorized for the current route."
	case explicitInteractionAnswerUnavailable:
		notice = "Pending input state is unavailable; this session cannot continue until it is recovered."
	}
	_ = c.al.settleInboundAdmission(
		ctx,
		msg,
		c.al.publishInteractionNoticeAdmission(ctx, msg, sessionKey, notice),
	)
}

func logExplicitInteractionAnswerDisposition(
	record interactions.Record,
	msg bus.InboundMessage,
	disposition explicitInteractionAnswerDisposition,
) {
	fields := map[string]any{
		"disposition":        disposition,
		"inbound_message_id": strings.TrimSpace(msg.Context.MessageID),
	}
	if record.ID != "" {
		fields["interaction_id"] = record.ID
		fields["interaction_short_id"] = record.ShortID
	}
	if record.Answer != nil && record.Answer.MessageID != "" {
		fields["accepted_message_id"] = record.Answer.MessageID
	}
	logger.InfoCF("agent", "Interaction answer ingress rejected or replayed", fields)
}

func (al *AgentLoop) hasNonterminalInteraction(workspace, sessionKey string) bool {
	registry := al.interactionRegistryForWorkspace(workspace)
	if registry == nil {
		return false
	}
	if registry.LastLoadError() != nil {
		return true
	}
	for _, record := range registry.ListNonterminal() {
		if record.Route.SessionKey == sessionKey {
			return true
		}
	}
	return false
}

func (c *inboundTurnCoordinator) handleInteractionInbound(
	ctx context.Context,
	msg bus.InboundMessage,
	target *inboundDispatchTarget,
) {
	claim, _, claimed := c.claimSession(target)
	if !claimed {
		if _, interactionAnswer := c.al.classifyInteractionAnswer(msg, target); interactionAnswer {
			go c.runContendedInteractionInbound(ctx, msg, target)
			return
		}
		registry := c.al.interactionRegistryForWorkspace(target.Agent.Workspace)
		if record, ok := activeInteractionForSession(registry, target.SessionKey); ok &&
			interactionRouteAuthorizes(record.Route, target, msg.Context) &&
			(record.Status == interactions.StatusClaimed || record.Status == interactions.StatusResuming) {
			if err := c.enqueueContendedInteractionInbound(ctx, msg, target, record); err != nil {
				c.al.releaseInboundMessage(context.Background(), msg, err)
			}
			return
		}
		c.deferInteractionInbound(ctx, msg, target)
		return
	}
	go c.runInteractionWorker(ctx, msg, target, claim)
}

func (c *inboundTurnCoordinator) enqueueContendedInteractionInbound(
	ctx context.Context,
	msg bus.InboundMessage,
	target *inboundDispatchTarget,
	record interactions.Record,
) error {
	flight, ok := c.al.loadInteractionResumeFlight(target.Agent.Workspace, record.ID)
	if !ok {
		return c.enqueueDeferredInteractionInbound(ctx, msg, target)
	}
	runInteractionLifecycleBoundaryHook(ctx, interactionBoundaryResumeFlightRead)

	flight.handoffMu.Lock()
	defer flight.handoffMu.Unlock()
	if flight.handoffSealed || !flight.handoff.configured() {
		return c.enqueueDeferredInteractionInbound(ctx, msg, target)
	}
	return c.al.enqueueInteractionContinuationInboundForScope(
		ctx,
		msg,
		flight.handoff.sourceScope,
		flight.handoff.sourceAgentID,
	)
}

func (c *inboundTurnCoordinator) runContendedInteractionInbound(
	ctx context.Context,
	msg bus.InboundMessage,
	target *inboundDispatchTarget,
) {
	for attempt := 0; attempt < interactionAnswerClaimAttempts; attempt++ {
		classification, _ := c.al.classifyInteractionAnswer(msg, target)
		if classification.Disposition != explicitInteractionAnswerActive {
			c.consumeExplicitInteractionAnswer(ctx, msg, target, classification)
			return
		}
		record := classification.Record
		if record.Status == interactions.StatusClaimed || record.Status == interactions.StatusResuming {
			disposition := explicitInteractionAnswerDuplicate
			if interactionInboundReplaysAnswer(record, msg.Context) {
				disposition = explicitInteractionAnswerReplay
			}
			classification.Disposition = disposition
			c.consumeExplicitInteractionAnswer(ctx, msg, target, classification)
			return
		}
		if claim, _, claimed := c.claimSession(target); claimed {
			current, _ := c.al.classifyInteractionAnswer(msg, target)
			if current.Disposition != explicitInteractionAnswerActive {
				claim.releaseIfOwned()
				c.consumeExplicitInteractionAnswer(ctx, msg, target, current)
				return
			}
			c.runInteractionWorker(ctx, msg, target, claim)
			return
		}
		timer := time.NewTimer(interactionAnswerClaimDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			c.al.releaseInboundMessage(context.Background(), msg, ctx.Err())
			return
		case <-timer.C:
		}
	}
	classification, _ := c.al.classifyInteractionAnswer(msg, target)
	if classification.Disposition == explicitInteractionAnswerActive {
		if classification.Record.Status == interactions.StatusCreated ||
			classification.Record.Status == interactions.StatusWaiting {
			logExplicitInteractionAnswerDisposition(
				classification.Record,
				msg,
				explicitInteractionAnswerRetry,
			)
			c.al.releaseInboundMessage(
				context.Background(),
				msg,
				errors.New("interaction answer is waiting for durable admission"),
			)
			return
		}
		classification.Disposition = explicitInteractionAnswerDuplicate
	}
	c.consumeExplicitInteractionAnswer(ctx, msg, target, classification)
}

func (c *inboundTurnCoordinator) runInteractionWorker(
	ctx context.Context,
	msg bus.InboundMessage,
	target *inboundDispatchTarget,
	claim *runtimeSessionClaim,
) {
	admittedCtx, releaseCapacity, err := c.acquireTurnCapacity(ctx, target.Agent.ID)
	if err != nil {
		claim.releaseIfOwned()
		c.al.releaseInboundMessage(context.Background(), msg, err)
		return
	}
	defer releaseCapacity()
	ctx = admittedCtx
	defer claim.releaseIfOwned()
	defer c.recoverWorkerPanic(claim.scope.sessionKey, msg)
	if c.al.channelManager != nil {
		defer c.al.channelManager.InvokeTypingStop(msg.Channel, msg.ChatID)
	}

	ownership, admission, processErr := c.al.processInteractionInbound(ctx, msg, target)
	if processErr != nil {
		logger.WarnCF("agent", "Failed to process human interaction answer", map[string]any{
			"session_key": target.SessionKey,
			"error":       processErr.Error(),
		})
		if ownership == interactionInboundCallerOwned {
			c.al.releaseInboundMessage(context.Background(), msg, processErr)
		}
		return
	}
	if ownership == interactionInboundDeferred {
		return
	}
	if ownership == interactionInboundCallerOwned {
		if err := c.al.settleInboundAdmission(ctx, msg, admission); err != nil {
			return
		}
	}
	if c.al.hasNonterminalInteraction(target.Agent.Workspace, target.SessionKey) {
		return
	}
	// Continuation must reacquire the session. Keep the route claim across the
	// handoff so newer inbound work cannot overtake already deferred messages.
	claim.releaseSessionIfOwned()
	if err := c.al.drainDeferredInteractionIngress(ctx, target.Agent.Workspace, interactions.Route{
		SessionKey: target.SessionKey,
		Channel:    msg.Channel,
		ChatID:     msg.ChatID,
	}, msg.Context); err != nil {
		logger.WarnCF("agent", "Failed to continue messages deferred by human interaction", map[string]any{
			"session_key": target.SessionKey,
			"error":       err.Error(),
		})
	}
}

func (al *AgentLoop) drainDeferredInteractionIngress(
	ctx context.Context,
	workspace string,
	route interactions.Route,
	inbound bus.InboundContext,
) error {
	if al.hasNonterminalInteraction(workspace, route.SessionKey) {
		return nil
	}
	target := &continuationTarget{
		AgentID:    route.AgentID,
		SessionKey: route.SessionKey,
		Channel:    route.Channel,
		ChatID:     route.ChatID,
		Workspace:  workspace,
	}
	steeringAggregate, err := al.drainSteeringForAggregate(ctx, target)
	if err != nil {
		settleErr := al.settleSteeringMessages(
			rejectedFinalResponseAdmission(err),
			steeringAggregate.messages,
		)
		return errors.Join(err, settleErr)
	}
	admission := finalResponseAdmission{status: finalResponseAdmissionNotRequired}
	if strings.TrimSpace(steeringAggregate.response) != "" {
		admission = al.publishResponseWithMetadataAndScopes(
			ctx,
			workspace,
			route.AgentID,
			route.Channel,
			route.ChatID,
			route.SessionKey,
			steeringAggregate.response,
			&inbound,
			finalResponseAlwaysPublish,
			target.responseMetadata,
			target.traceScopes,
		)
	}
	if settleErr := al.settleSteeringMessages(admission, steeringAggregate.messages); settleErr != nil {
		return settleErr
	}
	if !admission.permitsInboundAck() {
		return admission.err
	}
	return nil
}

func (al *AgentLoop) processInteractionInbound(
	ctx context.Context,
	msg bus.InboundMessage,
	target *inboundDispatchTarget,
) (interactionInboundOwnership, finalResponseAdmission, error) {
	notRequired := finalResponseAdmission{status: finalResponseAdmissionNotRequired}
	registry := al.interactionRegistryForWorkspace(target.Agent.Workspace)
	if registry.LastLoadError() != nil {
		return al.interactionNoticeResult(
			ctx,
			msg,
			target.SessionKey,
			"Pending input state is unavailable; this session cannot continue until it is recovered.",
		)
	}
	record, ok := activeInteractionForSession(registry, target.SessionKey)
	if !ok {
		return interactionInboundCallerOwned, notRequired, fmt.Errorf(
			"active interaction disappeared for session %q",
			target.SessionKey,
		)
	}
	if record.Status == interactions.StatusClaimed || record.Status == interactions.StatusResuming {
		if interactionInboundReplaysAnswer(record, msg.Context) {
			if err := al.settleInboundAdmission(ctx, msg, notRequired); err != nil {
				return interactionInboundClaimed, notRequired, err
			}
			return interactionInboundClaimed, notRequired, al.resumeClaimedInteraction(
				ctx,
				registry,
				target.Agent.Workspace,
				al.interactionContinuationAgent(record, target.Agent),
				&target.Allocation.Scope,
				msg.Context,
				record,
			)
		}
		if _, _, explicit := splitExplicitInteractionAnswer(msg.Content); explicit {
			logExplicitInteractionAnswerDisposition(
				record,
				msg,
				explicitInteractionAnswerDuplicate,
			)
			return interactionInboundCallerOwned, al.publishInteractionNoticeAdmission(
				ctx,
				msg,
				target.SessionKey,
				"An answer has already been accepted for this interaction.",
			), nil
		}
		if err := al.enqueueInteractionContinuationInbound(ctx, msg, target, record); err != nil {
			return interactionInboundCallerOwned, notRequired, err
		}
		return interactionInboundDeferred, notRequired, nil
	}
	if record.Status != interactions.StatusWaiting {
		return interactionInboundCallerOwned, notRequired, fmt.Errorf(
			"interaction %q is not accepting input from status %q",
			record.ID,
			record.Status,
		)
	}
	if !interactionRouteAuthorizes(record.Route, target, msg.Context) {
		return interactionInboundCallerOwned, al.publishInteractionNoticeAdmission(
			ctx,
			msg,
			target.SessionKey,
			"This session is waiting for an answer from the authorized user.",
		), nil
	}
	if interactionApprovalSupersededByInbound(record, msg) {
		msg = al.prepareInboundMessageForAgent(ctx, msg)
		answer := interactions.Answer{
			Text:       msg.Content,
			Media:      append([]string(nil), msg.Media...),
			Superseded: true,
			MessageID:  strings.TrimSpace(msg.Context.MessageID),
			ReceivedAt: time.Now().UnixMilli(),
		}
		claimed, err := registry.ClaimAnswer(
			record.ID,
			record.Revision,
			answer,
			interactions.OutcomeDenied,
		)
		if err != nil {
			if errors.Is(err, interactions.ErrAnswerTooLate) || errors.Is(err, interactions.ErrDuplicateAnswer) {
				return interactionInboundCallerOwned, al.publishInteractionNoticeAdmission(
					ctx,
					msg,
					target.SessionKey,
					"This interaction changed while applying your new guidance; please retry.",
				), nil
			}
			return interactionInboundCallerOwned, notRequired, err
		}
		al.syncInteractionControls(target.Agent.Workspace, claimed, bus.OutboundInteractionControlsRemove)
		if err := al.settleInboundAdmission(ctx, msg, notRequired); err != nil {
			return interactionInboundClaimed, notRequired, err
		}
		return interactionInboundClaimed, notRequired, al.resumeClaimedInteraction(
			ctx,
			registry,
			target.Agent.Workspace,
			al.interactionContinuationAgent(claimed, target.Agent),
			&target.Allocation.Scope,
			msg.Context,
			claimed,
		)
	}
	answerContent := al.interactionAnswerContent(record, msg)
	answer, err := parseInteractionAnswer(record, answerContent, msg.Context.MessageID)
	if err != nil {
		return al.interactionNoticeResult(
			ctx,
			msg,
			target.SessionKey,
			"I could not accept that answer: "+err.Error(),
		)
	}
	answer.ResponseMessageID = strings.TrimSpace(
		msg.Context.Raw[bus.InboundMetadataKeyInteractionResponseMessageID],
	)
	outcome := interactionAnswerOutcome(record, answer)
	claimed, err := registry.ClaimAnswer(
		record.ID,
		record.Revision,
		answer,
		outcome,
	)
	if err != nil {
		if errors.Is(err, interactions.ErrAnswerTooLate) || errors.Is(err, interactions.ErrDuplicateAnswer) {
			return interactionInboundCallerOwned, al.publishInteractionNoticeAdmission(
				ctx,
				msg,
				target.SessionKey,
				"An answer is already being processed for this session.",
			), nil
		}
		return interactionInboundCallerOwned, notRequired, err
	}
	al.syncInteractionControls(target.Agent.Workspace, claimed, bus.OutboundInteractionControlsRemove)
	if err := al.settleInboundAdmission(ctx, msg, notRequired); err != nil {
		return interactionInboundClaimed, notRequired, err
	}
	return interactionInboundClaimed, notRequired, al.resumeClaimedInteraction(
		ctx,
		registry,
		target.Agent.Workspace,
		al.interactionContinuationAgent(claimed, target.Agent),
		&target.Allocation.Scope,
		msg.Context,
		claimed,
	)
}

func interactionApprovalSupersededByInbound(
	record interactions.Record,
	msg bus.InboundMessage,
) bool {
	if record.Kind != interactions.KindApproval {
		return false
	}
	if _, projected := projectedInteractionAnswer(msg); projected {
		return false
	}
	if _, _, explicit, _ := parseInteractionAnswerEnvelope(msg.Content); explicit {
		return false
	}
	_, err := parseInteractionAnswer(record, msg.Content, msg.Context.MessageID)
	return err != nil
}

func (al *AgentLoop) enqueueInteractionContinuationInbound(
	ctx context.Context,
	msg bus.InboundMessage,
	target *inboundDispatchTarget,
	record interactions.Record,
) error {
	agent := al.interactionContinuationAgent(record, target.Agent)
	if agent == nil {
		return fmt.Errorf("interaction continuation agent is unavailable")
	}
	return al.enqueueInteractionContinuationInboundForScope(
		ctx,
		msg,
		newRuntimeSessionScope(agent.Workspace, interactionContinuationSessionKey(record)),
		agent.ID,
	)
}

func (al *AgentLoop) enqueueInteractionContinuationInboundForScope(
	ctx context.Context,
	msg bus.InboundMessage,
	scope runtimeSessionScope,
	agentID string,
) error {
	msg = al.prepareInboundMessageForAgent(ctx, msg)
	return al.enqueueSteeringMessageWithSender(
		scope,
		agentID,
		msg.SenderID,
		providers.Message{
			Role:           "user",
			Content:        msg.Content,
			Media:          append([]string(nil), msg.Media...),
			InboundSpoolID: msg.SpoolID,
		},
	)
}

func (al *AgentLoop) interactionNoticeResult(
	ctx context.Context,
	msg bus.InboundMessage,
	sessionKey string,
	content string,
) (interactionInboundOwnership, finalResponseAdmission, error) {
	return interactionInboundCallerOwned, al.publishInteractionNoticeAdmission(ctx, msg, sessionKey, content), nil
}

func (al *AgentLoop) interactionAnswerContent(record interactions.Record, msg bus.InboundMessage) string {
	if strings.TrimSpace(msg.Context.ReplyToMessageID) == "" {
		return msg.Content
	}
	if al == nil {
		return msg.Content
	}
	cfg := al.GetConfig()
	if cfg == nil {
		return msg.Content
	}
	channel := cfg.Channels.Get(msg.Context.Channel)
	if channel == nil || channel.Type != config.ChannelTelegram {
		return msg.Content
	}

	if record.Kind == interactions.KindApproval {
		choice := strings.TrimSpace(msg.Context.Raw[bus.InboundMetadataKeyInteractionChoice])
		switch choice {
		case bus.InboundInteractionChoiceAllowOnce, bus.InboundInteractionChoiceDeny:
			return choice
		}
	}
	if record.Kind == interactions.KindQuestion {
		if response := strings.TrimSpace(
			msg.Context.Raw[bus.InboundMetadataKeyInteractionResponse],
		); response != "" {
			return response
		}
	}
	return msg.Content
}

func interactionAnswerOutcome(
	record interactions.Record,
	answer interactions.Answer,
) interactions.Outcome {
	if record.Kind != interactions.KindApproval {
		return interactions.OutcomeAnswered
	}
	if answer.Text == "allow_once" {
		return interactions.OutcomeAllowed
	}
	return interactions.OutcomeDenied
}

func interactionInboundReplaysAnswer(record interactions.Record, inbound bus.InboundContext) bool {
	return record.Answer != nil && record.Answer.MessageID != "" &&
		record.Answer.MessageID == strings.TrimSpace(inbound.MessageID)
}

func activeInteractionForSession(
	registry *interactions.Registry,
	sessionKey string,
) (interactions.Record, bool) {
	if registry == nil {
		return interactions.Record{}, false
	}
	for _, record := range registry.ListNonterminal() {
		if record.Route.SessionKey == sessionKey {
			return record, true
		}
	}
	return interactions.Record{}, false
}

func interactionRouteAuthorizes(
	route interactions.Route,
	target *inboundDispatchTarget,
	inbound bus.InboundContext,
) bool {
	if target == nil || route.SessionKey != target.SessionKey ||
		route.Channel != inbound.Channel || route.ChatID != inbound.ChatID ||
		route.SenderID != inbound.SenderID {
		return false
	}
	if route.RouteSessionKey != "" && route.RouteSessionKey != target.Allocation.RouteScopeKey {
		return false
	}
	checks := [][2]string{
		{route.AccountID, inbound.Account},
		{route.ChatType, inbound.ChatType},
		{route.TopicID, inbound.TopicID},
		{route.SpaceID, inbound.SpaceID},
		{route.SpaceType, inbound.SpaceType},
	}
	for _, check := range checks {
		if check[0] != "" && check[0] != check[1] {
			return false
		}
	}
	return true
}

func parseInteractionAnswer(
	record interactions.Record,
	content string,
	messageID string,
) (interactions.Answer, error) {
	content = strings.TrimSpace(content)
	shortID, answerText, commandMatched, err := parseInteractionAnswerEnvelope(content)
	if err != nil {
		return interactions.Answer{}, fmt.Errorf("use `/answer %s <answer>`", record.ShortID)
	}
	if commandMatched {
		if !strings.EqualFold(shortID, record.ShortID) {
			return interactions.Answer{}, fmt.Errorf("use `/answer %s <answer>`", record.ShortID)
		}
		content = answerText
	}
	if content == "" {
		return interactions.Answer{}, fmt.Errorf("answer cannot be empty")
	}
	answer := interactions.Answer{
		Text: content, MessageID: strings.TrimSpace(messageID), ReceivedAt: time.Now().UnixMilli(),
	}
	if record.Kind == interactions.KindApproval {
		normalized := strings.NewReplacer("-", "_", " ", "_").Replace(strings.ToLower(content))
		switch normalized {
		case "allow", "allow_once":
			answer.Text = "allow_once"
		case "deny":
			answer.Text = "deny"
		default:
			return interactions.Answer{}, fmt.Errorf("reply `allow_once` or `deny`")
		}
		return answer, nil
	}
	if len(record.Questions) == 1 {
		answer.Values = map[string]string{record.Questions[0].ID: content}
		return answer, nil
	}
	values := make(map[string]string, len(record.Questions))
	known := make(map[string]struct{}, len(record.Questions))
	for _, question := range record.Questions {
		known[question.ID] = struct{}{}
	}
	for _, line := range strings.Split(content, "\n") {
		key, value, ok := strings.Cut(line, ":")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !ok || key == "" || value == "" {
			return interactions.Answer{}, fmt.Errorf("use one `question_id: answer` line per question")
		}
		if _, ok := known[key]; !ok {
			return interactions.Answer{}, fmt.Errorf("unknown question id %q", key)
		}
		if _, duplicate := values[key]; duplicate {
			return interactions.Answer{}, fmt.Errorf("duplicate answer for %q", key)
		}
		values[key] = value
	}
	for _, question := range record.Questions {
		if values[question.ID] == "" {
			return interactions.Answer{}, fmt.Errorf("missing answer for %q", question.ID)
		}
	}
	answer.Values = values
	return answer, nil
}

func parseInteractionAnswerEnvelope(content string) (shortID, body string, matched bool, err error) {
	commandToken, remainder, ok := cutInteractionAnswerToken(content)
	if !ok || !isInteractionAnswerCommandToken(commandToken) {
		return "", "", false, nil
	}
	shortID, remainder, ok = cutInteractionAnswerToken(remainder)
	if !ok {
		return "", "", true, fmt.Errorf("short interaction id is required")
	}
	return shortID, strings.TrimSpace(remainder), true, nil
}

func cutInteractionAnswerToken(content string) (token, remainder string, ok bool) {
	content = strings.TrimLeftFunc(content, unicode.IsSpace)
	if content == "" {
		return "", "", false
	}
	for index, char := range content {
		if unicode.IsSpace(char) {
			return content[:index], content[index:], true
		}
	}
	return content, "", true
}

func isInteractionAnswerCommandToken(token string) bool {
	command, mention, hasMention := strings.Cut(token, "@")
	if !strings.EqualFold(command, answerCommand) {
		return false
	}
	if !hasMention {
		return true
	}
	if mention == "" {
		return false
	}
	for _, char := range mention {
		if (char < 'a' || char > 'z') &&
			(char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') &&
			char != '_' {
			return false
		}
	}
	return true
}

func (al *AgentLoop) publishInteractionNoticeAdmission(
	ctx context.Context,
	msg bus.InboundMessage,
	sessionKey string,
	content string,
) finalResponseAdmission {
	if al == nil || al.bus == nil {
		return rejectedFinalResponseAdmission(fmt.Errorf("message bus unavailable"))
	}
	workspace, agentID := "", ""
	if _, routedAgent, _ := al.resolveMessageRoute(msg); routedAgent != nil {
		workspace, agentID = routedAgent.Workspace, routedAgent.ID
	}
	return al.publishResponseWithContextIfNeeded(
		ctx,
		workspace,
		agentID,
		msg.Channel,
		msg.ChatID,
		sessionKey,
		content,
		&msg.Context,
		finalResponseAlwaysPublish,
	)
}

type interactionToolResultPayload struct {
	InteractionID string               `json:"interaction_id"`
	Outcome       interactions.Outcome `json:"outcome"`
	Answers       map[string]string    `json:"answers,omitempty"`
	Text          string               `json:"text,omitempty"`
}

type interactionResumeFlight struct {
	done          chan struct{}
	handled       bool
	err           error
	handoffMu     sync.Mutex
	handoffSealed bool
	handoff       interactionSteeringHandoff
}

type interactionSteeringHandoff struct {
	sourceScope      runtimeSessionScope
	sourceAgentID    string
	destinationScope runtimeSessionScope
}

func (h interactionSteeringHandoff) configured() bool {
	return h.sourceScope.complete() && strings.TrimSpace(h.sourceAgentID) != "" &&
		h.destinationScope.complete()
}

func (al *AgentLoop) loadInteractionResumeFlight(
	workspace string,
	interactionID string,
) (*interactionResumeFlight, bool) {
	flight, ok := al.interactionResumeFlights.Load(interactionResumeFlightKey(workspace, interactionID))
	if !ok {
		return nil, false
	}
	return flight.(*interactionResumeFlight), true
}

func configureInteractionSteeringHandoff(
	flight *interactionResumeFlight,
	interactionWorkspace string,
	record interactions.Record,
	agent *AgentInstance,
) error {
	if flight == nil || agent == nil {
		return fmt.Errorf("interaction steering handoff runtime is unavailable")
	}
	handoff := interactionSteeringHandoff{
		sourceScope: newRuntimeSessionScope(
			agent.Workspace,
			interactionContinuationSessionKey(record),
		),
		sourceAgentID: agent.ID,
		destinationScope: newRuntimeSessionScope(
			interactionWorkspace,
			record.Route.SessionKey,
		),
	}
	if !handoff.configured() {
		return fmt.Errorf("interaction steering handoff scope is incomplete")
	}
	flight.handoffMu.Lock()
	defer flight.handoffMu.Unlock()
	if flight.handoffSealed {
		return fmt.Errorf("interaction steering handoff is already sealed")
	}
	if flight.handoff.configured() && flight.handoff != handoff {
		return fmt.Errorf("interaction steering handoff was configured with a different route")
	}
	flight.handoff = handoff
	return nil
}

func (al *AgentLoop) sealInteractionSteeringHandoff(flight *interactionResumeFlight) {
	if flight == nil {
		return
	}
	flight.handoffMu.Lock()
	defer flight.handoffMu.Unlock()
	if flight.handoffSealed {
		return
	}
	flight.handoffSealed = true
	if al.steering == nil || !flight.handoff.configured() {
		return
	}
	al.steering.moveScope(
		flight.handoff.sourceScope,
		flight.handoff.destinationScope,
	)
}

func (al *AgentLoop) sealActiveInteractionSteeringHandoff(workspace, interactionID string) {
	flight, ok := al.loadInteractionResumeFlight(workspace, interactionID)
	if ok {
		al.sealInteractionSteeringHandoff(flight)
	}
}

func interactionResumeFlightKey(workspace, interactionID string) string {
	return strings.TrimSpace(workspace) + "\x00" + strings.TrimSpace(interactionID)
}

func (al *AgentLoop) startInteractionResumeFlight(
	workspace string,
	interactionID string,
) (string, *interactionResumeFlight, bool) {
	key := interactionResumeFlightKey(workspace, interactionID)
	candidate := &interactionResumeFlight{done: make(chan struct{})}
	actual, loaded := al.interactionResumeFlights.LoadOrStore(key, candidate)
	if loaded {
		return key, actual.(*interactionResumeFlight), false
	}
	return key, candidate, true
}

func (al *AgentLoop) finishInteractionResumeFlight(
	key string,
	flight *interactionResumeFlight,
	handled bool,
	err error,
) {
	al.sealInteractionSteeringHandoff(flight)
	flight.handled = handled
	flight.err = err
	close(flight.done)
	al.interactionResumeFlights.Delete(key)
}

func (al *AgentLoop) resumeClaimedInteraction(
	ctx context.Context,
	registry *interactions.Registry,
	interactionWorkspace string,
	agent *AgentInstance,
	scope *session.SessionScope,
	inbound bus.InboundContext,
	record interactions.Record,
) error {
	if al == nil || registry == nil || agent == nil {
		return fmt.Errorf("interaction continuation runtime is unavailable")
	}
	for {
		flightKey, flight, owner := al.startInteractionResumeFlight(
			interactionWorkspace, record.ID,
		)
		if !owner {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-flight.done:
				if flight.handled {
					return flight.err
				}
				continue
			}
		}
		var resumeErr error
		defer func() {
			al.finishInteractionResumeFlight(flightKey, flight, true, resumeErr)
		}()
		if err := configureInteractionSteeringHandoff(
			flight,
			interactionWorkspace,
			record,
			agent,
		); err != nil {
			resumeErr = err
			return resumeErr
		}
		resumeErr = al.resumeClaimedInteractionOwned(
			ctx, registry, interactionWorkspace, agent, scope, inbound, record,
		)
		return resumeErr
	}
}

func (al *AgentLoop) resumeClaimedInteractionOwned(
	ctx context.Context,
	registry *interactions.Registry,
	interactionWorkspace string,
	agent *AgentInstance,
	scope *session.SessionScope,
	inbound bus.InboundContext,
	record interactions.Record,
) error {
	current, ok := registry.Get(record.ID)
	if !ok {
		return interactions.ErrNotFound
	}
	switch current.Status {
	case interactions.StatusResolved, interactions.StatusCancelled, interactions.StatusFailed:
		return nil
	case interactions.StatusClaimed, interactions.StatusResuming:
		record = current
	default:
		return fmt.Errorf("cannot resume interaction from status %q", current.Status)
	}
	continuationSessionKey := interactionContinuationSessionKey(record)
	continuationScope := sessionScopeForRecovery(agent.Sessions, continuationSessionKey)
	if continuationScope == nil {
		continuationScope = session.CloneScope(scope)
	}
	if continuationScope != nil {
		if continuationScope.RouteScopeKey == "" && scope != nil {
			continuationScope.RouteScopeKey = scope.RouteScopeKey
		}
		if continuationScope.RouteScopeKey == "" {
			continuationScope.RouteScopeKey = record.Route.RouteSessionKey
			if continuationScope.RouteScopeKey == "" {
				continuationScope.RouteScopeKey = record.Route.SessionKey
			}
		}
		// Approval replies arrive on the parent route, but execution resumes in
		// the agent that owns the durable continuation. This also repairs legacy
		// metadata written before cross-agent ownership was persisted correctly.
		continuationScope.AgentID = agent.ID
		ensureSessionMetadata(agent.Sessions, continuationSessionKey, continuationScope, nil)
	}
	approvalAllowed := record.Kind == interactions.KindApproval &&
		record.Outcome == interactions.OutcomeAllowed
	if !approvalAllowed {
		if err := al.ensureInteractionToolResult(ctx, agent, record); err != nil {
			_, _ = registry.RecordResumeFailure(record.ID, record.Revision, err.Error())
			return err
		}
	}
	supersedingSteering := interactionSupersedingSteering(
		record,
		agent.Sessions.GetHistory(continuationSessionKey),
	)
	resuming := record
	if record.Status == interactions.StatusClaimed {
		var err error
		resuming, err = registry.MarkResuming(record.ID, record.Revision)
		if err != nil {
			return err
		}
	} else if record.Status != interactions.StatusResuming {
		return fmt.Errorf("cannot resume interaction from status %q", record.Status)
	}
	if approvalAllowed {
		current, ok := registry.Get(resuming.ID)
		if !ok {
			return interactions.ErrNotFound
		}
		resuming = current
		if _, resultIndex := interactionToolPairIndexes(
			agent.Sessions.GetHistory(continuationSessionKey),
			resuming.Origin.ToolCallID,
		); resultIndex < 0 {
			if resuming.ApprovalConsumedAt != 0 {
				outcome := interactions.OutcomeDeliveryUnknown
				text := "The one-time approval was consumed before restart, but tool execution " +
					"could not be confirmed. The tool was not retried."
				if receiptIDs := interactionOutcomeReceiptIDs(resuming); len(receiptIDs) > 0 {
					outcome = interactions.OutcomeAllowed
					text = "The approved external action completed before restart. Verified runtime receipt IDs: " +
						strings.Join(receiptIDs, ", ") + "."
				}
				if err := al.persistInteractionToolResult(
					ctx,
					agent,
					resuming,
					interactionToolResultPayload{
						InteractionID: resuming.ID,
						Outcome:       outcome,
						Text:          text,
					},
				); err != nil {
					return err
				}
			} else {
				control, aborted, err := al.executeApprovedInteractionTool(
					ctx, registry, interactionWorkspace, agent, continuationScope, resuming,
				)
				if err != nil {
					_, _ = registry.RecordResumeFailure(resuming.ID, resuming.Revision, err.Error())
					return err
				}
				if aborted {
					return nil
				}
				if control == ToolControlSuspend {
					return nil
				}
			}
		}
		current, ok = registry.Get(resuming.ID)
		if !ok {
			return interactions.ErrNotFound
		}
		if current.Status == interactions.StatusResolved {
			return nil
		}
		resuming = current
	}
	if finalContent, recoveredDeliverable, ok := interactionFinalAfterToolResult(
		agent.Sessions.GetHistory(continuationSessionKey),
		record.Origin.ToolCallID,
	); ok {
		cleanContent, objectiveOutcome := extractResumedObjectiveOutcome(
			finalContent, interactionOutcomeAudits(resuming), resuming, agent,
		)
		al.sealActiveInteractionSteeringHandoff(interactionWorkspace, resuming.ID)
		_, finalizeErr := al.finalizeResumedInteraction(
			ctx, registry, interactionWorkspace, resuming, inbound, cleanContent,
			terminalTurnDeliverable(recoveredDeliverable, cleanContent, objectiveOutcome), nil,
			interactionBoundaryPrecomputedFinal,
		)
		return finalizeErr
	}

	routeSessionKey := record.Route.RouteSessionKey
	if routeSessionKey == "" {
		routeSessionKey = record.Route.SessionKey
	}
	modelBinding := al.bindEffectiveModel(routeSessionKey, agent)
	defer modelBinding.Cleanup()
	turnStatus := TurnEndStatusCompleted
	expectFinalDelivery := al.interactionContinuationExpectsUserDelivery(
		interactionWorkspace, record,
	)
	var deliveryObservation *finalDeliveryObservation
	if expectFinalDelivery {
		deliveryObservation = &finalDeliveryObservation{}
	}
	var resumedTurn turnResult
	finalContent, runErr := al.runAgentLoop(ctx, agent, processOptions{
		ModelBinding:               modelBinding,
		TaskID:                     record.Origin.TaskID,
		InteractionWorkspace:       interactionWorkspace,
		InteractionSessionKey:      record.Route.SessionKey,
		InteractionRouteKey:        routeSessionKey,
		InteractionOriginExecution: record.Origin.ExecutionID,
		InteractionOriginContext:   cloneInboundContext(record.Origin.ExecutionContext),
		ObjectiveChecklist:         runtimeObjectiveChecklist(record.Origin.ObjectiveChecklist),
		TurnStatus:                 &turnStatus,
		TurnResult:                 &resumedTurn,
		Dispatch: DispatchRequest{
			RouteSessionKey: routeSessionKey,
			BaseSessionKey:  continuationSessionKey,
			SessionKey:      continuationSessionKey,
			InboundContext:  cloneInboundContext(&inbound),
			SessionScope:    session.CloneScope(continuationScope),
		},
		DefaultResponse:             defaultResponse,
		EnableSummary:               true,
		SendResponse:                false,
		ExpectFinalDelivery:         expectFinalDelivery,
		FinalDeliveryObservation:    deliveryObservation,
		AllowInterimMintClawPublish: true,
		InitialSteeringMessages:     supersedingSteering,
		SkipInitialSteeringPoll:     true,
	})
	if runErr != nil {
		_, _ = registry.RecordResumeFailure(resuming.ID, resuming.Revision, runErr.Error())
		return runErr
	}
	if turnStatus == TurnEndStatusSuspended {
		return nil
	}
	if turnStatus == TurnEndStatusAborted {
		return nil
	}
	audits := interactionOutcomeAudits(resuming)
	audits = appendTurnWriteAudit(audits, "", resumedTurn.writeAudit)
	finalContent, objectiveOutcome := extractResumedObjectiveOutcome(
		finalContent, audits, resuming, agent,
	)
	al.sealActiveInteractionSteeringHandoff(interactionWorkspace, resuming.ID)
	var traceScopes []runtimeevents.TraceScope
	if deliveryObservation != nil {
		traceScopes = deliveryObservation.traceScopes
	}
	finalization, deliveryErr := al.finalizeResumedInteraction(
		ctx,
		registry,
		interactionWorkspace,
		resuming,
		inbound,
		finalContent,
		terminalTurnDeliverable(resumedTurn.deliverable, finalContent, objectiveOutcome),
		traceScopes,
		interactionBoundaryModelFinal,
	)
	if deliveryObservation != nil {
		admission := finalResponseAdmission{status: finalResponseAdmissionAccepted}
		if finalization == interactionFinalizationCanceled {
			admission = rejectedFinalResponseAdmission(errInteractionFinalizationCanceled)
		} else if deliveryErr != nil {
			admission = rejectedFinalResponseAdmission(deliveryErr)
		}
		if settleErr := al.settleSteeringMessages(
			admission,
			deliveryObservation.takeUnsettledSteering(),
		); settleErr != nil {
			deliveryErr = errors.Join(deliveryErr, settleErr)
		}
	}
	return deliveryErr
}

func interactionSupersedingSteering(
	record interactions.Record,
	history []providers.Message,
) []providers.Message {
	if record.Answer == nil || !record.Answer.Superseded {
		return nil
	}
	message := steeringPromptMessage(providers.Message{
		Role:    "user",
		Content: record.Answer.Text,
		Media:   append([]string(nil), record.Answer.Media...),
	})
	_, resultIndex := interactionToolPairIndexes(history, record.Origin.ToolCallID)
	if resultIndex >= 0 {
		for _, existing := range history[resultIndex+1:] {
			if existing.Role == message.Role && existing.Content == message.Content &&
				slices.Equal(existing.Media, message.Media) {
				return nil
			}
		}
	}
	return []providers.Message{message}
}

func (al *AgentLoop) finalizeResumedInteraction(
	ctx context.Context,
	registry *interactions.Registry,
	interactionWorkspace string,
	record interactions.Record,
	inbound bus.InboundContext,
	content string,
	deliverable *taskresult.Deliverable,
	traceScopes []runtimeevents.TraceScope,
	boundary string,
) (interactionFinalizationDisposition, error) {
	runInteractionLifecycleBoundaryHook(ctx, boundary)
	current, ok := registry.Get(record.ID)
	if !ok {
		return interactionFinalizationDelivered, interactions.ErrNotFound
	}
	switch current.Status {
	case interactions.StatusCanceling, interactions.StatusCancelled:
		return interactionFinalizationCanceled, nil
	case interactions.StatusResuming:
		return interactionFinalizationDelivered, al.deliverInteractionFinal(
			ctx, registry, interactionWorkspace, current, inbound, content, deliverable, traceScopes,
		)
	default:
		return interactionFinalizationDelivered, fmt.Errorf(
			"cannot finalize interaction from status %q", current.Status,
		)
	}
}

func terminalTurnDeliverable(
	base *taskresult.Deliverable,
	content string,
	objectiveOutcome *taskresult.Outcome,
) *taskresult.Deliverable {
	deliverable := taskresult.CloneDeliverable(base)
	if deliverable == nil {
		deliverable = &taskresult.Deliverable{}
	}
	if strings.TrimSpace(deliverable.Text) == "" {
		deliverable.Text = content
	}
	if objectiveOutcome != nil {
		deliverable.ObjectiveOutcome = taskresult.CloneOutcome(objectiveOutcome)
	}
	if strings.TrimSpace(deliverable.Text) == "" && len(deliverable.Artifacts) == 0 &&
		len(deliverable.Metadata) == 0 && deliverable.Report == nil && deliverable.ObjectiveOutcome == nil {
		return nil
	}
	return deliverable
}

func extractResumedObjectiveOutcome(
	content string,
	audits []toolshared.WriteAuditEntry,
	record interactions.Record,
	agent *AgentInstance,
) (string, *taskresult.Outcome) {
	required := strings.TrimSpace(record.Origin.TaskID) != "" && agent != nil &&
		agent.Tools != nil && agent.Tools.HasRegistered("browser_act")
	return extractObjectiveOutcome(
		content, audits, required, runtimeObjectiveChecklist(record.Origin.ObjectiveChecklist),
	)
}

func (al *AgentLoop) executeApprovedInteractionTool(
	ctx context.Context,
	registry *interactions.Registry,
	interactionWorkspace string,
	agent *AgentInstance,
	scope *session.SessionScope,
	record interactions.Record,
) (ToolControl, bool, error) {
	history := agent.Sessions.GetHistory(interactionContinuationSessionKey(record))
	toolCall, ok := interactionOriginToolCall(history, record.Origin.ToolCallID)
	if !ok {
		return ToolControlBreak, false, fmt.Errorf(
			"originating approval tool call %q is missing",
			record.Origin.ToolCallID,
		)
	}
	originalInbound := cloneInboundContext(record.Origin.ExecutionContext)
	if originalInbound == nil {
		if err := al.persistInteractionToolResult(
			ctx,
			agent,
			record,
			interactionToolResultPayload{
				InteractionID: record.ID,
				Outcome:       interactions.OutcomeDenied,
				Text: "The protected tool was not executed because its original execution " +
					"context is unavailable after restart.",
			},
		); err != nil {
			return ToolControlBreak, false, err
		}
		return ToolControlBreak, false, nil
	}
	toolCall = providers.NormalizeToolCall(toolCall)
	routeSessionKey := record.Route.RouteSessionKey
	if routeSessionKey == "" {
		routeSessionKey = record.Route.SessionKey
	}
	opts := processOptions{
		TaskID:                record.Origin.TaskID,
		InteractionWorkspace:  interactionWorkspace,
		InteractionSessionKey: record.Route.SessionKey,
		InteractionRouteKey:   routeSessionKey,
		ApprovalGrant: &ToolApprovalGrant{
			InteractionID:      record.ID,
			Revision:           record.Revision,
			OriginExecutionID:  record.Origin.ExecutionID,
			OriginArgumentHash: record.Origin.ArgumentHash,
		},
		Dispatch: DispatchRequest{
			RouteSessionKey: routeSessionKey,
			BaseSessionKey:  interactionContinuationSessionKey(record),
			SessionKey:      interactionContinuationSessionKey(record),
			InboundContext:  originalInbound,
			SessionScope:    session.CloneScope(scope),
		},
		DefaultResponse: defaultResponse,
		EnableSummary:   true,
		SendResponse:    false,
	}
	var err error
	opts, err = resolveTurnProfileOptions(al.GetConfig(), opts)
	if err != nil {
		return ToolControlBreak, false, fmt.Errorf("resolve approved tool profile: %w", err)
	}
	turnScope := al.newTurnEventScope(
		agent.ID,
		agent.Workspace,
		opts.Dispatch.SessionKey,
		newTurnContext(opts.Dispatch.InboundContext, nil, opts.Dispatch.SessionScope),
	)
	ts := newTurnState(agent, opts, turnScope)
	pipeline := NewPipeline(al)
	turnCtx, turnCancel := context.WithCancel(ctx)
	defer turnCancel()
	ts.setTurnCancel(turnCancel)
	ts.ctx = turnCtx
	turnCtx = withTurnState(turnCtx, ts)
	turnCtx = WithAgentLoop(turnCtx, al)
	al.registerActiveTurn(ts)
	defer al.clearActiveTurn(ts)
	defer func() {
		if ts.hardAbortRequested() {
			al.cleanupInteractionOriginTools(ctx, agent, record)
		}
	}()
	defer func() { ts.Finish(ts.hardAbortRequested()) }()
	if al.takePendingStop(ts.runtimeSessionScope()) {
		_ = ts.requestHardAbort()
	}
	if ts.hardAbortRequested() {
		return ToolControlBreak, true, nil
	}
	exec, err := pipeline.SetupTurn(turnCtx, ts)
	if err != nil {
		return ToolControlBreak, false, err
	}
	if exec.model.cleanup != nil {
		defer exec.model.cleanup()
	}
	llm := newLLMIterationState(1)
	llm.response = &providers.LLMResponse{ToolCalls: []providers.ToolCall{toolCall}}
	llm.normalizedToolCalls = []providers.ToolCall{toolCall}
	llm.toolResponseDisposition = toolResponseHandled
	llm.assistantToolCallsPersisted = true
	outcome := pipeline.ExecuteTools(turnCtx, turnCtx, ts, exec, llm)
	pauseCtx, pauseCancel := context.WithTimeout(context.WithoutCancel(turnCtx), 3*time.Second)
	pipeline.pauseToolFeedbackForTurn(pauseCtx, ts)
	pauseCancel()
	if receipts := interactionOutcomeReceipts(exec.writeAudit); len(receipts) > 0 {
		current, exists := registry.Get(record.ID)
		if !exists {
			return outcome.Control, false, interactions.ErrNotFound
		}
		if _, err = registry.RecordOutcomeReceipts(current.ID, current.Revision, receipts); err != nil {
			return outcome.Control, false, fmt.Errorf("persist approved action receipts: %w", err)
		}
	}
	if outcome.TurnErr != nil {
		return outcome.Control, false, outcome.TurnErr
	}
	if outcome.JournalErr != nil {
		return outcome.Control, false, outcome.JournalErr
	}
	if ts.hardAbortRequested() || outcome.AbortCause == TurnAbortHard {
		return outcome.Control, true, nil
	}
	if outcome.Control == ToolControlSuspend {
		return outcome.Control, false, nil
	}
	if _, resultIndex := interactionToolPairIndexes(
		agent.Sessions.GetHistory(interactionContinuationSessionKey(record)),
		record.Origin.ToolCallID,
	); resultIndex < 0 {
		return outcome.Control, false, fmt.Errorf("approved tool execution did not persist a matching result")
	}
	_, ok = registry.Get(record.ID)
	if !ok {
		return outcome.Control, false, interactions.ErrNotFound
	}
	return outcome.Control, false, nil
}

func interactionOutcomeReceipts(audits []toolshared.WriteAuditEntry) []taskresult.Receipt {
	var receipts []taskresult.Receipt
	for _, audit := range audits {
		id := strings.TrimSpace(audit.Metadata["invocation_id"])
		if !audit.Success || audit.Kind != "external_action" || id == "" {
			continue
		}
		receipts = append(receipts, taskresult.Receipt{
			ID: id, Kind: audit.Kind, Target: audit.Target, Action: audit.Action,
			Tool: audit.Tool, Summary: audit.Summary, Metadata: copyObjectiveMetadata(audit.Metadata),
		})
	}
	return receipts
}

func interactionOutcomeAudits(record interactions.Record) []toolshared.WriteAuditEntry {
	audits := make([]toolshared.WriteAuditEntry, 0, len(record.OutcomeReceipts))
	for _, receipt := range record.OutcomeReceipts {
		audits = append(audits, toolshared.WriteAuditEntry{
			Kind: receipt.Kind, Target: receipt.Target, Action: receipt.Action,
			Tool: receipt.Tool, Summary: receipt.Summary, Success: true,
			Metadata: copyObjectiveMetadata(receipt.Metadata),
		})
	}
	return audits
}

func interactionOutcomeReceiptIDs(record interactions.Record) []string {
	ids := make([]string, 0, len(record.OutcomeReceipts))
	for _, receipt := range record.OutcomeReceipts {
		if id := strings.TrimSpace(receipt.ID); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func (al *AgentLoop) interactionContinuationAgent(
	record interactions.Record,
	fallback *AgentInstance,
) *AgentInstance {
	if al != nil && strings.TrimSpace(record.Route.AgentID) != "" {
		if registry := al.GetRegistry(); registry != nil {
			if agent, ok := registry.GetAgent(record.Route.AgentID); ok && agent != nil {
				return agent
			}
		}
	}
	return fallback
}

func interactionContinuationSessionKey(record interactions.Record) string {
	if key := strings.TrimSpace(record.Origin.ContinuationSessionKey); key != "" {
		return key
	}
	return record.Route.SessionKey
}

func (al *AgentLoop) deliverInteractionFinal(
	ctx context.Context,
	registry *interactions.Registry,
	interactionWorkspace string,
	record interactions.Record,
	inbound bus.InboundContext,
	content string,
	deliverable *taskresult.Deliverable,
	traceScopes []runtimeevents.TraceScope,
) error {
	if record.Kind == interactions.KindApproval || record.Kind == interactions.KindQuestion {
		inbound.ReplyToMessageID = interactionResponseReplyTarget(record, inbound)
	}
	bus.OutboundMetadata{
		InteractionKind:     string(record.Kind),
		InteractionControls: bus.OutboundInteractionControlsRemove,
	}.ApplyToContext(&inbound)
	if strings.TrimSpace(record.Origin.TaskID) != "" {
		return al.deliverTaskInteractionFinal(
			ctx, registry, interactionWorkspace, record, inbound, content, deliverable, traceScopes,
		)
	}
	if strings.TrimSpace(content) == "" && record.Kind == interactions.KindQuestion &&
		strings.EqualFold(strings.TrimSpace(record.Route.Channel), "telegram") {
		content = "Response recorded."
	}
	if record.FinalDelivered || strings.TrimSpace(content) == "" {
		updated, err := registry.Resolve(record.ID, record.Revision)
		if err == nil {
			al.dismissInteractionToolFeedback(ctx, updated, inbound, traceScopes)
			al.completeInteractionTask(
				interactionWorkspace, updated, content, taskregistry.DeliveryNotApplicable,
			)
		}
		return err
	}
	bus.OutboundMetadata{
		MessageKind:  bus.OutboundMessageKindFinalReply,
		OutboundKind: bus.OutboundKindFinal,
	}.ApplyToContext(&inbound)
	if al.channelManager == nil {
		_, _ = registry.RecordFinalDeliveryAttempt(
			record.ID, record.Revision, false, "channel manager unavailable",
		)
		return fmt.Errorf("channel manager unavailable")
	}
	prepared, stateErr := prepareInteractionFinalDelivery(registry, record)
	if stateErr != nil {
		return fmt.Errorf("begin final interaction delivery: %w", stateErr)
	}
	runInteractionLifecycleBoundaryHook(ctx, interactionBoundaryFinalPrepared)
	al.dismissInteractionToolFeedback(ctx, prepared, inbound, traceScopes)
	if inbound.Raw == nil {
		inbound.Raw = make(map[string]string)
	}
	inbound.Raw[interactionIDMetadata] = record.ID
	inbound.Raw[interactionShortIDMeta] = record.ShortID
	inbound.Raw["delivery_key"] = interactionDeliveryKey(record.ID, "final")
	message := bus.OutboundMessage{
		Channel: record.Route.Channel, ChatID: record.Route.ChatID,
		Context: inbound, AgentID: record.Route.AgentID,
		SessionKey: record.Route.SessionKey, Content: content,
		ReplyToMessageID: inbound.ReplyToMessageID,
	}
	if err := bus.SetOutboundTraceScopes(&message, traceScopes); err != nil {
		return err
	}
	message.TraceSettlement = len(message.TraceScopes) > 0
	started, stateErr := registry.StartFinalDelivery(prepared.ID, prepared.Revision)
	if stateErr != nil {
		return fmt.Errorf("start final interaction delivery: %w", stateErr)
	}
	deliveryErr := al.sendInteractionMessage(ctx, message)
	updated, stateErr := registry.CompleteFinalDelivery(
		started.ID,
		started.Revision,
		deliveryErr == nil,
		deliveryErr != nil && !channels.DeliveryDefinitelyNotSent(deliveryErr),
		errString(deliveryErr),
	)
	if stateErr != nil {
		return fmt.Errorf("record final interaction delivery: %w", stateErr)
	}
	if deliveryErr != nil {
		return deliveryErr
	}
	resolved, err := registry.Resolve(updated.ID, updated.Revision)
	if err == nil {
		al.completeInteractionTask(
			interactionWorkspace, resolved, content, taskregistry.DeliveryDelivered,
		)
	}
	return err
}

func prepareInteractionFinalDelivery(
	registry *interactions.Registry,
	record interactions.Record,
) (interactions.Record, error) {
	if record.FinalDeliveryState == interactions.DeliveryStateNotSent {
		return record, nil
	}
	return registry.BeginFinalDelivery(record.ID, record.Revision)
}

func interactionResponseReplyTarget(record interactions.Record, inbound bus.InboundContext) string {
	if (record.Kind != interactions.KindApproval && record.Kind != interactions.KindQuestion) ||
		!strings.EqualFold(strings.TrimSpace(record.Route.Channel), "telegram") {
		return ""
	}
	if record.Answer != nil {
		if messageID := strings.TrimSpace(record.Answer.ResponseMessageID); messageID != "" {
			return messageID
		}
		if messageID := strings.TrimSpace(record.Answer.MessageID); messageID != "" {
			return messageID
		}
	}
	return strings.TrimSpace(inbound.MessageID)
}

func (al *AgentLoop) dismissInteractionToolFeedback(
	ctx context.Context,
	record interactions.Record,
	inbound bus.InboundContext,
	traceScopes []runtimeevents.TraceScope,
) {
	target := toolFeedbackTargetForSession(
		record.Route.Channel,
		record.Route.ChatID,
		&inbound,
		record.Route.SessionKey,
		traceScopes,
	)
	dismissCtx, dismissCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	al.toolFeedbackPublisher().dismissToolFeedback(dismissCtx, target)
	dismissCancel()
}

func (al *AgentLoop) deliverTaskInteractionFinal(
	ctx context.Context,
	registry *interactions.Registry,
	workspace string,
	record interactions.Record,
	inbound bus.InboundContext,
	content string,
	deliverable *taskresult.Deliverable,
	traceScopes []runtimeevents.TraceScope,
) error {
	taskRegistry := al.taskRegistryForWorkspace(workspace)
	taskID := strings.TrimSpace(record.Origin.TaskID)
	if taskRegistry == nil || taskID == "" {
		return fmt.Errorf("owning task registry is unavailable")
	}
	task, ok := taskRegistry.Get(taskID)
	if !ok {
		return fmt.Errorf("owning task %q is unavailable", taskID)
	}
	prepared, stateErr := prepareInteractionFinalDelivery(registry, record)
	if stateErr != nil {
		return fmt.Errorf("begin task interaction delivery: %w", stateErr)
	}
	var objectiveOutcome *taskresult.Outcome
	if deliverable != nil {
		objectiveOutcome = deliverable.ObjectiveOutcome
	}
	projection := objectiveOutcomeUserContent(content, objectiveOutcome)
	terminalDeliverable := terminalTurnDeliverable(deliverable, projection, objectiveOutcome)
	runInteractionLifecycleBoundaryHook(ctx, interactionBoundaryFinalPrepared)
	if err := taskRegistry.CompleteInteractionTaskResult(
		taskID, record.ID, projection, terminalDeliverable, taskregistry.DeliveryPending,
	); err != nil {
		return err
	}
	runInteractionLifecycleBoundaryHook(ctx, interactionBoundaryTaskCompleted)
	al.dismissInteractionToolFeedback(ctx, prepared, inbound, traceScopes)
	mode := toolshared.AsyncDeliveryMode(strings.TrimSpace(task.DeliveryMode))
	switch mode {
	case toolshared.AsyncDeliveryParentOnly, toolshared.AsyncDeliveryUserOnly, toolshared.AsyncDeliveryUserAndParent:
	default:
		mode = toolshared.AsyncDeliveryUserOnly
	}
	if mode != toolshared.AsyncDeliveryParentOnly {
		bus.OutboundMetadata{
			MessageKind:  bus.OutboundMessageKindFinalReply,
			OutboundKind: bus.OutboundKindFinal,
		}.ApplyToContext(&inbound)
	}
	started, stateErr := registry.StartFinalDelivery(prepared.ID, prepared.Revision)
	if stateErr != nil {
		return fmt.Errorf("start task interaction delivery: %w", stateErr)
	}
	if mode == toolshared.AsyncDeliveryParentOnly &&
		(record.Kind == interactions.KindApproval || record.Kind == interactions.KindQuestion) &&
		strings.EqualFold(strings.TrimSpace(record.Route.Channel), "telegram") {
		if err := al.deliverInteractionControlsRemoved(ctx, record, inbound); err != nil {
			_, recordErr := registry.CompleteFinalDelivery(
				started.ID,
				started.Revision,
				false,
				!channels.DeliveryDefinitelyNotSent(err),
				err.Error(),
			)
			if recordErr != nil {
				return fmt.Errorf("record interaction control removal: %w", recordErr)
			}
			return err
		}
	}
	result := (&toolshared.ToolResult{
		ForLLM: projection, ForUser: projection,
	}).
		WithAsyncTaskID(taskID).
		WithAsyncDelivery(mode)
	if terminalDeliverable != nil {
		result.WithDeliverable(taskresult.CloneDeliverable(terminalDeliverable))
	}
	agent := al.interactionContinuationAgent(record, nil)
	turnState := &turnState{
		agent: agent, agentID: record.Route.AgentID,
		workspace: workspace, channel: record.Route.Channel, chatID: record.Route.ChatID,
		sessionKey: record.Route.SessionKey,
		opts: processOptions{Dispatch: DispatchRequest{
			RouteSessionKey: record.Route.RouteSessionKey,
			SessionKey:      record.Route.SessionKey,
			InboundContext:  cloneInboundContext(&inbound),
		}},
		scope: al.newTurnEventScope(
			record.Route.AgentID,
			workspace,
			record.Route.SessionKey,
			newTurnContext(&inbound, nil, nil),
		),
	}
	completionID := "interaction:" + record.ID
	al.deliverAsyncToolCompletion(AsyncDeliveryRequest{
		TurnState:    turnState,
		ToolName:     task.TaskKind,
		CompletionID: completionID,
		Result:       result,
		Decision:     decideAsyncToolResultDelivery(result),
		TraceScopes:  traceScopes,
	})
	task, _ = taskRegistry.Get(taskID)
	success := task.DeliveryStatus == taskregistry.DeliveryDelivered ||
		task.DeliveryStatus == taskregistry.DeliverySessionQueued ||
		task.DeliveryStatus == taskregistry.DeliveryNotApplicable
	detail := task.DeliveryError
	ambiguous := !success
	if task.DeliveryStatus == taskregistry.DeliveryParentMissing &&
		mode == toolshared.AsyncDeliveryParentOnly {
		ambiguous = false
	}
	updated, stateErr := registry.CompleteFinalDelivery(
		started.ID, started.Revision, success, ambiguous, detail,
	)
	if stateErr != nil {
		return fmt.Errorf("record task interaction delivery: %w", stateErr)
	}
	if !success {
		if detail == "" {
			detail = "task completion delivery did not reach a final state"
		}
		return fmt.Errorf("deliver resumed task completion: %s", detail)
	}
	_, err := registry.Resolve(updated.ID, updated.Revision)
	return err
}

func (al *AgentLoop) deliverInteractionControlsRemoved(
	ctx context.Context,
	record interactions.Record,
	inbound bus.InboundContext,
) error {
	if inbound.Raw == nil {
		inbound.Raw = make(map[string]string)
	}
	inbound.Raw[interactionIDMetadata] = record.ID
	inbound.Raw[interactionShortIDMeta] = record.ShortID
	inbound.Raw["delivery_key"] = interactionDeliveryKey(record.ID, "controls_removed")
	bus.OutboundMetadata{
		InteractionKind:     string(record.Kind),
		InteractionControls: bus.OutboundInteractionControlsRemove,
	}.ApplyToContext(&inbound)
	replyToMessageID := interactionResponseReplyTarget(record, inbound)
	inbound.ReplyToMessageID = replyToMessageID
	return al.sendInteractionMessage(ctx, bus.OutboundMessage{
		Channel:          record.Route.Channel,
		ChatID:           record.Route.ChatID,
		Context:          inbound,
		AgentID:          record.Route.AgentID,
		SessionKey:       record.Route.SessionKey,
		Content:          "Response recorded.",
		ReplyToMessageID: replyToMessageID,
	})
}

func (al *AgentLoop) interactionContinuationExpectsUserDelivery(
	workspace string,
	record interactions.Record,
) bool {
	taskID := strings.TrimSpace(record.Origin.TaskID)
	if taskID == "" {
		return true
	}
	registry := al.taskRegistryForWorkspace(workspace)
	if registry == nil {
		return true
	}
	task, ok := registry.Get(taskID)
	if !ok {
		return true
	}
	return toolshared.AsyncDeliveryMode(strings.TrimSpace(task.DeliveryMode)) != toolshared.AsyncDeliveryParentOnly
}

func (al *AgentLoop) completeInteractionTask(
	workspace string,
	record interactions.Record,
	content string,
	delivery taskregistry.DeliveryStatus,
) {
	taskID := strings.TrimSpace(record.Origin.TaskID)
	if al == nil || taskID == "" {
		return
	}
	registry := al.taskRegistryForWorkspace(workspace)
	if registry == nil {
		return
	}
	if err := registry.CompleteInteractionTask(
		taskID, record.ID, content, delivery,
	); err != nil {
		logger.WarnCF("agent", "Failed to complete resumed interaction task", map[string]any{
			"workspace": workspace, "task_id": taskID,
			"interaction_id": record.ID, "error": err.Error(),
		})
	}
}

func interactionFinalAfterToolResult(
	history []providers.Message,
	toolCallID string,
) (string, *taskresult.Deliverable, bool) {
	_, resultIndex := interactionToolPairIndexes(history, toolCallID)
	if resultIndex < 0 {
		return "", nil, false
	}
	for _, message := range history[resultIndex+1:] {
		if message.Role == "assistant" && len(message.ToolCalls) == 0 &&
			strings.TrimSpace(message.Content) != "" {
			if message.Content == handledToolResponseSummary && len(message.Attachments) > 0 {
				return "", taskresult.CloneDeliverable(message.Deliverable), true
			}
			return message.Content, taskresult.CloneDeliverable(message.Deliverable), true
		}
	}
	return "", nil, false
}

func (al *AgentLoop) ensureInteractionToolResult(
	ctx context.Context,
	agent *AgentInstance,
	record interactions.Record,
) error {
	history := agent.Sessions.GetHistory(interactionContinuationSessionKey(record))
	originIndex, resultIndex := interactionToolPairIndexes(history, record.Origin.ToolCallID)
	if originIndex < 0 {
		return fmt.Errorf("originating tool call %q is missing from session history", record.Origin.ToolCallID)
	}
	if resultIndex >= 0 {
		return nil
	}
	if record.Answer == nil {
		return fmt.Errorf("interaction %q has no claimed answer", record.ID)
	}
	payload := interactionToolResultPayload{
		InteractionID: record.ID,
		Outcome:       record.Outcome,
		Text:          record.Answer.Text,
		Answers:       record.Answer.Values,
	}
	if record.Answer.Superseded {
		payload.Text = "The pending action was superseded by new user guidance and was not executed."
	}
	return al.persistInteractionToolResult(ctx, agent, record, payload)
}

func (al *AgentLoop) ensureInteractionCancellationToolResult(
	ctx context.Context,
	agent *AgentInstance,
	record interactions.Record,
	code string,
) error {
	history := agent.Sessions.GetHistory(interactionContinuationSessionKey(record))
	originIndex, resultIndex := interactionToolPairIndexes(history, record.Origin.ToolCallID)
	if originIndex < 0 {
		return fmt.Errorf("originating tool call %q is missing from session history", record.Origin.ToolCallID)
	}
	if resultIndex >= 0 {
		return nil
	}
	if record.Kind == interactions.KindApproval && record.ApprovalConsumedAt != 0 {
		return fmt.Errorf("approved tool terminal result is unavailable after approval consumption")
	}
	return al.persistInteractionToolResult(ctx, agent, record, interactionToolResultPayload{
		InteractionID: record.ID,
		Outcome:       interactions.OutcomeCanceled,
		Text:          code,
	})
}

func (al *AgentLoop) persistInteractionToolResult(
	ctx context.Context,
	agent *AgentInstance,
	record interactions.Record,
	payload interactionToolResultPayload,
) error {
	content, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	message := providers.Message{
		Role: "tool", Content: string(content), ToolCallID: record.Origin.ToolCallID,
		ToolResultStatus: providers.ToolResultStatusSuccess,
	}
	continuationSessionKey := interactionContinuationSessionKey(record)
	writeErr := persistFullSessionMessage(ctx, agent.Sessions, continuationSessionKey, message)
	if writeErr != nil {
		return writeErr
	}
	if al.contextManager != nil {
		if err := al.contextManager.Ingest(ctx, &IngestRequest{
			Agent:      agent,
			SessionKey: continuationSessionKey,
			Message:    message,
		}); err != nil {
			logger.WarnCF("agent", "Context ingest failed for interaction answer", map[string]any{
				"interaction_id": record.ID,
				"error":          err.Error(),
			})
		}
	}
	return nil
}

func interactionToolPairIndexes(
	history []providers.Message,
	toolCallID string,
) (originIndex int, resultIndex int) {
	originIndex = -1
	resultIndex = -1
	for index, message := range history {
		if message.Role != "assistant" {
			continue
		}
		for _, call := range message.ToolCalls {
			if call.ID == toolCallID {
				originIndex = index
				resultIndex = -1
				break
			}
		}
	}
	if originIndex < 0 {
		return originIndex, resultIndex
	}
	for index := originIndex + 1; index < len(history); index++ {
		message := history[index]
		if message.Role == "tool" && message.ToolCallID == toolCallID {
			return originIndex, index
		}
	}
	return originIndex, resultIndex
}

func interactionOriginToolCall(
	history []providers.Message,
	toolCallID string,
) (providers.ToolCall, bool) {
	for index := len(history) - 1; index >= 0; index-- {
		message := history[index]
		if message.Role != "assistant" {
			continue
		}
		for _, call := range message.ToolCalls {
			if call.ID == toolCallID {
				return call, true
			}
		}
	}
	return providers.ToolCall{}, false
}
