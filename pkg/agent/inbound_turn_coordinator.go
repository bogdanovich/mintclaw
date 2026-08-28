// MintClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/commands"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

type inboundTurnCoordinator struct {
	al *AgentLoop
}

func newInboundTurnCoordinator(al *AgentLoop) *inboundTurnCoordinator {
	return &inboundTurnCoordinator{al: al}
}

func (c *inboundTurnCoordinator) handleInbound(ctx context.Context, msg bus.InboundMessage) {
	al := c.al
	if al.outboundCoordinator() != nil {
		ctx = withOutboundTransaction(ctx, msg.SpoolID)
	}

	target, ok := al.resolveSteeringTarget(msg)
	if !ok {
		// Non-routable message (e.g. system) stays synchronous so it preserves
		// the required ordering and does not enter session steering.
		admission := al.processMessageSync(ctx, msg)
		_ = al.settleInboundAdmission(ctx, msg, admission)
		return
	}
	if c.handleScopedInspectionCommand(ctx, msg, target) {
		return
	}
	cancellation, err := al.cancelInteractionForControlMessage(ctx, msg, target)
	if err != nil {
		admission := al.publishInteractionNoticeAdmission(
			ctx,
			msg,
			target.SessionKey,
			"The pending interaction could not be canceled; please retry.",
		)
		_ = al.settleInboundAdmission(ctx, msg, admission)
		return
	}
	if cancellation.CommandHandled {
		if strings.EqualFold(strings.TrimSpace(msg.Context.Channel), "telegram") {
			msg.Context.ReplyToMessageID = strings.TrimSpace(msg.Context.MessageID)
		}
		metadata := bus.OutboundMetadata{
			InteractionKind:     string(cancellation.Kind),
			InteractionControls: bus.OutboundInteractionControlsRemove,
		}
		admission := al.publishStopReply(
			ctx,
			msg,
			target.runtimeSessionScope(),
			target.Agent.ID,
			commands.StopResult{Stopped: cancellation.Canceled},
			nil,
			metadata,
		)
		_ = al.settleInboundAdmission(ctx, msg, admission)
		return
	}
	if c.routeProjectedInteractionAnswer(ctx, msg, target) ||
		c.routeExplicitInteractionAnswer(ctx, msg, target) {
		return
	}
	if al.shouldHandleInteractionInbound(msg, target) {
		c.handleInteractionInbound(ctx, msg, target)
		return
	}
	if al.hasNonterminalInteraction(target.Agent.Workspace, target.SessionKey) {
		c.deferInteractionInbound(ctx, msg, target)
		return
	}

	claim, activeTarget, claimed := c.claimSession(target)
	if !claimed {
		c.handleBusySession(ctx, msg, activeTarget)
		return
	}

	c.startWorker(ctx, msg, target, claim)
}

func (c *inboundTurnCoordinator) handleScopedInspectionCommand(
	ctx context.Context,
	msg bus.InboundMessage,
	target *inboundDispatchTarget,
) bool {
	commandName, ok := commands.CommandName(msg.Content)
	if !ok || commandName != "subagents" || target == nil || target.Agent == nil {
		return false
	}

	al := c.al
	binding := effectiveModelBinding{
		RouteSessionKey: strings.TrimSpace(target.Allocation.RouteScopeKey),
		WorkspaceAgent:  target.Agent,
	}
	opts := turnSpec{
		Dispatch: DispatchRequest{
			RouteSessionKey: binding.RouteSessionKey,
			SessionKey:      target.SessionKey,
			InboundContext:  cloneInboundContext(&msg.Context),
			UserMessage:     msg.Content,
		},
		ModelBinding: binding,
	}
	response, handled := al.handleCommand(ctx, msg, binding, &opts)
	if !handled {
		return false
	}

	if al.channelManager != nil {
		defer al.channelManager.InvokeTypingStop(msg.Context.Channel, msg.Context.ChatID)
	}
	metadata := bus.OutboundMetadata{}
	if al.turns.activeTurnState(newRuntimeSessionScope(target.Agent.Workspace, target.SessionKey)) != nil {
		metadata.OutboundKind = bus.OutboundKindInterim
	}
	admission := al.publishResponseWithMetadataAndScopes(
		ctx,
		target.Agent.Workspace,
		target.Agent.ID,
		msg.Context.Channel,
		msg.Context.ChatID,
		target.SessionKey,
		response,
		&msg.Context,
		finalResponseAlwaysPublish,
		metadata,
		nil,
	)
	_ = al.settleInboundAdmission(ctx, msg, admission)
	return true
}

func (c *inboundTurnCoordinator) deferInteractionInbound(
	ctx context.Context,
	msg bus.InboundMessage,
	target *inboundDispatchTarget,
) {
	if err := c.enqueueDeferredInteractionInbound(ctx, msg, target); err != nil {
		c.al.turns.inbound.release(context.Background(), msg, err)
	}
}

func (c *inboundTurnCoordinator) enqueueDeferredInteractionInbound(
	ctx context.Context,
	msg bus.InboundMessage,
	target *inboundDispatchTarget,
) error {
	msg = c.al.prepareInboundMessageForAgent(ctx, msg)
	return c.al.enqueueSteeringMessageWithSender(
		target.runtimeSessionScope(),
		target.Agent.ID,
		msg.Context.SenderID,
		providers.Message{
			Role:           "user",
			Content:        msg.Content,
			Media:          append([]string(nil), msg.Media...),
			InboundSpoolID: msg.SpoolID,
		},
	)
}

func (c *inboundTurnCoordinator) claimSession(
	target *inboundDispatchTarget,
) (*runtimeSessionClaim, *inboundDispatchTarget, bool) {
	al := c.al
	return al.turns.claimRuntimeRouteSession(
		target,
		makePendingTurnID(target.SessionKey, al.turns.nextSequence()),
	)
}

func (c *inboundTurnCoordinator) handleBusySession(
	ctx context.Context,
	msg bus.InboundMessage,
	target *inboundDispatchTarget,
) {
	al := c.al
	if target == nil || target.Agent == nil {
		al.turns.inbound.release(ctx, msg, fmt.Errorf("active session target is unavailable"))
		return
	}
	scope := target.runtimeSessionScope()
	if handled, admission := al.tryHandleStopCommand(ctx, msg, scope, target.Agent.ID); handled {
		_ = al.settleInboundAdmission(ctx, msg, admission)
		return
	}

	msg = al.prepareInboundMessageForAgent(ctx, msg)
	if err := al.enqueueSteeringMessageWithSender(
		scope,
		target.Agent.ID,
		msg.Context.SenderID,
		providers.Message{
			Role:           "user",
			Content:        msg.Content,
			Media:          append([]string(nil), msg.Media...),
			InboundSpoolID: msg.SpoolID,
		},
	); err != nil {
		logger.WarnCF("agent", "Failed to enqueue steering message",
			map[string]any{
				"error":       err.Error(),
				"channel":     msg.Context.Channel,
				"chat_id":     msg.Context.ChatID,
				"session_key": scope.sessionKey,
			})
		al.turns.inbound.release(ctx, msg, err)
	}
}

func (c *inboundTurnCoordinator) startWorker(
	ctx context.Context,
	msg bus.InboundMessage,
	target *inboundDispatchTarget,
	claim *runtimeSessionClaim,
) {
	go c.runWorker(ctx, msg, target, claim)
}

func (al *AgentLoop) settleInboundAdmission(
	ctx context.Context,
	msg bus.InboundMessage,
	admission finalResponseAdmission,
) error {
	admission = transactionAdmission(ctx, admission)
	if admission.permitsInboundAck() {
		if err := al.turns.inbound.ack(ctx, msg); err != nil {
			al.turns.inbound.release(context.Background(), msg, err)
			return err
		}
		return nil
	}
	al.turns.inbound.release(context.Background(), msg, admission.err)
	return admission.err
}

func (c *inboundTurnCoordinator) runWorker(
	ctx context.Context,
	msg bus.InboundMessage,
	target *inboundDispatchTarget,
	claim *runtimeSessionClaim,
) {
	al := c.al
	admittedCtx, releaseCapacity, err := c.acquireTurnCapacity(ctx, target.Agent.ID)
	if err != nil {
		claim.releaseIfOwned()
		al.turns.inbound.release(context.Background(), msg, err)
		return
	}
	defer releaseCapacity()
	ctx = admittedCtx
	currentAgent, changed, err := al.currentAgentGeneration(target.Agent)
	if err != nil {
		claim.releaseIfOwned()
		al.turns.inbound.release(context.Background(), msg, err)
		return
	}
	if changed {
		refreshed := *target
		refreshed.Agent = currentAgent
		target = &refreshed
	}

	defer claim.releaseIfOwned()
	defer c.recoverWorkerPanic(claim.scope.sessionKey, msg)

	if al.channelManager != nil {
		defer al.channelManager.InvokeTypingStop(msg.Context.Channel, msg.Context.ChatID)
	}

	if al.turns.takePendingStop(claim.scope) {
		c.handlePendingStop(ctx, msg, claim, target)
		return
	}

	turn := al.buildInboundMessageTurnForTarget(ctx, msg, target)
	admission := al.runInboundTurnWithSteering(ctx, turn)
	_ = al.settleInboundAdmission(ctx, msg, admission)
}

func (c *inboundTurnCoordinator) acquireTurnCapacity(
	ctx context.Context,
	agentID string,
) (context.Context, func(), error) {
	for {
		admittedCtx, releaseAdmission, err := c.al.turns.acquireAgentTurn(ctx, agentID)
		if err != nil {
			return ctx, nil, err
		}
		select {
		case c.al.workerSem <- struct{}{}:
			return admittedCtx, func() {
				<-c.al.workerSem
				releaseAdmission()
			}, nil
		default:
			releaseAdmission()
		}

		// Wait for worker progress without retaining the agent admission. The
		// worker token is released immediately and both resources are retried.
		select {
		case c.al.workerSem <- struct{}{}:
			<-c.al.workerSem
		case <-ctx.Done():
			return ctx, nil, ctx.Err()
		}
	}
}

func (c *inboundTurnCoordinator) handlePendingStop(
	ctx context.Context,
	msg bus.InboundMessage,
	claim *runtimeSessionClaim,
	dispatchTarget *inboundDispatchTarget,
) {
	al := c.al
	claim.releaseIfOwned()

	target := &continuationTarget{
		SessionKey: claim.scope.sessionKey,
		Channel:    msg.Context.Channel,
		ChatID:     msg.Context.ChatID,
		Workspace:  claim.scope.workspace,
	}
	if dispatchTarget != nil && dispatchTarget.Agent != nil {
		target.AgentID = dispatchTarget.Agent.ID
	}
	steeringAggregate, continueErr := al.drainSteeringForAggregate(ctx, target)
	if continueErr != nil {
		admission := al.maybePublishErrorWithScopes(
			ctx,
			target.Workspace,
			target.AgentID,
			msg.Context.Channel,
			msg.Context.ChatID,
			claim.scope.sessionKey,
			continueErr,
			finalResponseAlwaysPublish,
			target.traceScopes,
		)
		settleErr := al.settleSteeringMessages(
			rejectedFinalResponseAdmission(continueErr),
			steeringAggregate.messages,
		)
		if settleErr != nil {
			admission = rejectedFinalResponseAdmission(settleErr)
		}
		_ = al.settleInboundAdmission(ctx, msg, admission)
		return
	}
	admission := finalResponseAdmission{status: finalResponseAdmissionNotRequired}
	if steeringAggregate.response != "" {
		admission = al.publishResponseWithMetadataAndScopes(
			ctx,
			target.Workspace,
			target.AgentID,
			target.Channel,
			target.ChatID,
			target.SessionKey,
			steeringAggregate.response,
			&msg.Context,
			finalResponseAlwaysPublish,
			target.responseMetadata,
			target.traceScopes,
		)
	}
	if settleErr := al.settleSteeringMessages(admission, steeringAggregate.messages); settleErr != nil {
		admission = rejectedFinalResponseAdmission(settleErr)
	}
	_ = al.settleInboundAdmission(ctx, msg, admission)
}

func (c *inboundTurnCoordinator) recoverWorkerPanic(sessionKey string, msg bus.InboundMessage) {
	if r := recover(); r != nil {
		logger.RecoverPanicNoExit(r)
		logger.ErrorCF("agent", "Worker goroutine panicked",
			map[string]any{
				"session_key": sessionKey,
				"channel":     msg.Context.Channel,
				"chat_id":     msg.Context.ChatID,
				"panic":       fmt.Sprintf("%v", r),
			})
	}
}

func isPendingTurnState(ts *turnState) bool {
	return ts != nil && strings.HasPrefix(ts.turnID, pendingTurnPrefix)
}
