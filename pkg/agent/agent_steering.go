// MintClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	agentinterfaces "github.com/bogdanovich/mintclaw/pkg/agent/interfaces"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

func (al *AgentLoop) processMessageSync(ctx context.Context, msg bus.InboundMessage) finalResponseAdmission {
	if al.channelManager != nil {
		defer al.channelManager.InvokeTypingStop(msg.Context.Channel, msg.Context.ChatID)
	}

	_, routedAgent, _ := al.resolveMessageRoute(msg)
	workspace, agentID := "", ""
	channel, chatID, sessionKey := msg.Context.Channel, msg.Context.ChatID, msg.SessionKey
	inboundCtx := &msg.Context
	if msg.Context.Channel == "system" {
		origin := systemMessageOrigin(msg)
		channel, chatID = origin.Channel, origin.ChatID
		inboundCtx = &origin
		routedAgent = al.GetRegistry().GetDefaultAgent()
		if routedAgent != nil {
			sessionKey = session.BuildMainSessionKey(routedAgent.ID)
		}
	}
	if routedAgent != nil {
		workspace, agentID = routedAgent.Workspace, routedAgent.ID
	}
	response, err := al.processMessage(ctx, msg)
	if err != nil {
		if isNonPublishableTurnError(err) {
			return rejectedFinalResponseAdmission(err)
		}
		return al.publishResponseWithContextIfNeeded(
			ctx,
			workspace,
			agentID,
			channel,
			chatID,
			sessionKey,
			formatUserFacingAgentError(err),
			inboundCtx,
			finalResponseAlwaysPublish,
		)
	}
	return al.publishResponseWithContextIfNeeded(
		ctx,
		workspace,
		agentID,
		channel,
		chatID,
		sessionKey,
		response,
		inboundCtx,
		finalResponseAlwaysPublish,
	)
}

type inboundSpool struct {
	bus agentinterfaces.MessageBus
}

func (s *inboundSpool) ack(ctx context.Context, msg bus.InboundMessage) error {
	if msg.SpoolID == "" || s == nil || s.bus == nil {
		return nil
	}
	if err := s.bus.AckInbound(ctx, msg); err != nil {
		logger.WarnCF("agent", "Failed to ack inbound spool entry",
			map[string]any{
				"spool_id":    msg.SpoolID,
				"channel":     msg.Context.Channel,
				"chat_id":     msg.Context.ChatID,
				"session_key": msg.SessionKey,
				"error":       err.Error(),
			})
		return err
	}
	return nil
}

func (s *inboundSpool) release(
	ctx context.Context,
	msg bus.InboundMessage,
	cause error,
) {
	if msg.SpoolID == "" || s == nil || s.bus == nil {
		return
	}
	if err := s.bus.ReleaseInbound(ctx, msg, cause); err != nil {
		logger.WarnCF("agent", "Failed to release inbound spool entry",
			map[string]any{
				"spool_id":    msg.SpoolID,
				"channel":     msg.Context.Channel,
				"chat_id":     msg.Context.ChatID,
				"session_key": msg.SessionKey,
				"error":       err.Error(),
			})
	}
}

func (al *AgentLoop) runInboundTurnWithSteering(
	ctx context.Context,
	turn inboundMessageTurn,
) finalResponseAdmission {
	target := &continuationTarget{
		SessionKey: turn.SessionKey,
		Channel:    turn.Message.Context.Channel,
		ChatID:     turn.Message.Context.ChatID,
	}
	turn.Options.FinalDeliveryObservation = &target.finalDeliveryObservation
	if turn.Agent != nil {
		target.AgentID = turn.Agent.ID
		target.Workspace = turn.Agent.Workspace
	}
	return al.runTurnAndDrainSteering(ctx, turn.Message, func() (string, error) {
		return al.processInboundMessageTurn(ctx, turn)
	}, target)
}

func (al *AgentLoop) runTurnAndDrainSteering(
	ctx context.Context,
	initialMsg bus.InboundMessage,
	process func() (string, error),
	target *continuationTarget,
) finalResponseAdmission {
	response, err := process()
	initialAdmission := finalResponseAdmission{status: finalResponseAdmissionNotRequired}
	initialResponsePublished := false
	initialRequiresAggregate := strings.TrimSpace(response) != ""
	if err != nil {
		var admissionErr *turnAdmissionError
		rootAdmissionRejected := errors.As(err, &admissionErr)
		initialResponsePublished = true
		initialAdmission = al.maybePublishErrorWithScopes(
			ctx,
			target.Workspace,
			target.AgentID,
			initialMsg.Context.Channel,
			initialMsg.Context.ChatID,
			initialMsg.SessionKey,
			err,
			finalResponseAlwaysPublish,
			target.traceScopes,
		)
		if rootAdmissionRejected {
			initialAdmission = rejectedFinalResponseAdmission(err)
		}
		if isNonPublishableTurnError(initialAdmission.err) {
			return initialAdmission
		}
		response = ""
	}
	responses := appendSteeringResponse(nil, response)
	initialMetadata := target.responseMetadata
	target.responseMetadata = bus.OutboundMetadata{}
	steeringAggregate, continueErr := al.drainSteeringForAggregate(ctx, target)
	continued := steeringAggregate.response
	if continueErr != nil {
		if isNonPublishableTurnError(continueErr) {
			admission := rejectedFinalResponseAdmission(continueErr)
			if settleErr := al.settleSteeringMessages(
				admission,
				steeringAggregate.messages,
			); settleErr != nil {
				return rejectedFinalResponseAdmission(settleErr)
			}
			return admission
		}
		if ctx.Err() == nil {
			logger.WarnCF("agent", "Failed to continue queued steering",
				map[string]any{
					"channel": target.Channel,
					"chat_id": target.ChatID,
					"error":   continueErr.Error(),
				})
		}
	}
	continuedResponses := appendSteeringResponse(nil, continued)
	if len(continuedResponses) > 0 {
		responses = continuedResponses
	} else {
		target.responseMetadata = initialMetadata
	}

	// Publish final response
	aggregateAdmission := finalResponseAdmission{status: finalResponseAdmissionNotRequired}
	finalResponse := joinSteeringResponses(responses)
	if finalResponse != "" {
		finalContext := outboundContextFromInbound(
			&initialMsg.Context,
			target.Channel,
			target.ChatID,
			initialMsg.Context.ReplyToMessageID,
		)
		target.responseMetadata = target.responseMetadata.Merge(bus.OutboundMetadata{
			MessageKind: bus.OutboundMessageKindFinalReply,
		})
		aggregateAdmission = al.publishResponseWithMetadataAndScopes(
			ctx,
			target.Workspace,
			target.AgentID,
			target.Channel,
			target.ChatID,
			target.SessionKey,
			finalResponse,
			&finalContext,
			finalResponseAlwaysPublish,
			target.responseMetadata,
			target.traceScopes,
		)
	}
	if settleErr := al.settleSteeringMessages(aggregateAdmission, steeringAggregate.messages); settleErr != nil {
		return rejectedFinalResponseAdmission(settleErr)
	}
	if initialResponsePublished {
		return initialAdmission
	}
	if !initialRequiresAggregate {
		return initialAdmission
	}
	return aggregateAdmission
}

func (t *continuationTarget) beginSteeringSettlement() {
	if t == nil {
		return
	}
	t.holdSteeringSettlement = true
}

func (t *continuationTarget) heldFinalDeliveryObservation() *finalDeliveryObservation {
	if t == nil || !t.holdSteeringSettlement {
		return nil
	}
	return &t.finalDeliveryObservation
}

func (t *continuationTarget) takeUnsettledSteering() []providers.Message {
	if t == nil {
		return nil
	}
	t.holdSteeringSettlement = false
	return t.finalDeliveryObservation.takeUnsettledSteering()
}

func (al *AgentLoop) settleSteeringMessages(
	admission finalResponseAdmission,
	messages []providers.Message,
) error {
	if admission.permitsInboundAck() {
		return al.turns.inbound.ackAcceptedSteeringMessagesChecked(context.Background(), messages)
	}
	al.turns.inbound.releaseSteeringMessages(context.Background(), messages, admission.err)
	return nil
}

type steeringAggregateInput struct {
	response string
	messages []providers.Message
}

func (al *AgentLoop) drainSteeringForAggregate(
	ctx context.Context,
	target *continuationTarget,
) (steeringAggregateInput, error) {
	target.beginSteeringSettlement()
	response, err := al.drainQueuedSteeringContinuations(ctx, target)
	return steeringAggregateInput{
		response: response,
		messages: target.takeUnsettledSteering(),
	}, err
}

func (o *finalDeliveryObservation) observeTurn(scope runtimeevents.TraceScope) {
	if o == nil {
		return
	}
	o.traceScopes = appendUniqueTraceScope(o.traceScopes, scope)
}

func (o *finalDeliveryObservation) observeResponse(metadata bus.OutboundMetadata) {
	if o == nil {
		return
	}
	if metadata.ModelName != "" {
		o.responseMetadata.ModelName = metadata.ModelName
	}
	if metadata.DefaultModelName != "" {
		o.responseMetadata.DefaultModelName = metadata.DefaultModelName
	}
	o.responseMetadata.UsageInputTokens += metadata.UsageInputTokens
	o.responseMetadata.UsageOutputTokens += metadata.UsageOutputTokens
	o.responseMetadata.UsageTotalTokens += metadata.UsageTotalTokens
}

func (o *finalDeliveryObservation) observeSteering(messages []providers.Message) {
	if o == nil {
		return
	}
	o.unsettledSteering = append(o.unsettledSteering, messages...)
}

func (o *finalDeliveryObservation) takeUnsettledSteering() []providers.Message {
	if o == nil {
		return nil
	}
	messages := append([]providers.Message(nil), o.unsettledSteering...)
	o.unsettledSteering = nil
	return messages
}

func (t *continuationTarget) retainResponseMetadata(
	snapshot bus.OutboundMetadata,
	response string,
) bool {
	if t == nil || strings.TrimSpace(response) != "" {
		return true
	}
	t.responseMetadata = snapshot
	return false
}

func (t *continuationTarget) appendContinuationResponse(
	responses []string,
	snapshot bus.OutboundMetadata,
	response string,
) ([]string, bool) {
	if !t.retainResponseMetadata(snapshot, response) {
		return responses, false
	}
	retained := appendSteeringResponse(responses, response)
	if len(retained) == len(responses) {
		t.responseMetadata = snapshot
	}
	return retained, true
}

func (al *AgentLoop) drainQueuedSteeringContinuations(
	ctx context.Context,
	target *continuationTarget,
) (string, error) {
	if target == nil {
		return "", nil
	}

	scope := newRuntimeSessionScope(target.Workspace, target.SessionKey)
	if !scope.complete() {
		return "", fmt.Errorf("continuation workspace and session are required")
	}
	responses := make([]string, 0, 2)
	for al.pendingSteeringCountForScope(scope) > 0 {
		if err := ctx.Err(); err != nil {
			return joinSteeringResponses(responses), err
		}
		if target.Workspace != "" &&
			al.hasNonterminalInteraction(target.Workspace, target.SessionKey) {
			return joinSteeringResponses(responses), nil
		}

		logger.InfoCF("agent", "Continuing queued steering after turn end",
			map[string]any{
				"channel":     target.Channel,
				"chat_id":     target.ChatID,
				"session_key": target.SessionKey,
				"queue_depth": al.pendingSteeringCountForScope(scope),
			})

		metadataBefore := target.responseMetadata
		continued, continueErr := al.continueRuntimeSession(ctx, target)
		if continueErr != nil {
			target.responseMetadata = metadataBefore
			return joinSteeringResponses(responses), continueErr
		}
		var keepDraining bool
		responses, keepDraining = target.appendContinuationResponse(
			responses,
			metadataBefore,
			continued,
		)
		if !keepDraining {
			break
		}
	}

	return joinSteeringResponses(responses), nil
}

func appendUniqueTraceScope(
	values []runtimeevents.TraceScope,
	value runtimeevents.TraceScope,
) []runtimeevents.TraceScope {
	value = runtimeevents.NewTraceScope(value.Workspace, value.TurnID)
	if !value.Complete() {
		return values
	}
	if slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}

func appendSteeringResponse(responses []string, response string) []string {
	response = strings.TrimSpace(response)
	if response == "" {
		return responses
	}
	if n := len(responses); n > 0 && responses[n-1] == response {
		return responses
	}
	return append(responses, response)
}

func joinSteeringResponses(responses []string) string {
	if len(responses) == 0 {
		return ""
	}
	return strings.Join(responses, "\n\n")
}

func (al *AgentLoop) resolveSteeringTarget(msg bus.InboundMessage) (*inboundDispatchTarget, bool) {
	if msg.Context.Channel == "system" {
		return nil, false
	}

	route, agent, err := al.resolveMessageRoute(msg)
	if err != nil || agent == nil {
		return nil, false
	}
	allocation := al.allocateRouteSession(route, msg)
	routeClaimKey := runtimeRouteClaimKey(allocation.RouteScopeKey, msg.SessionKey)
	routeScope := newRuntimeRouteScope(agent.Workspace, routeClaimKey)
	if activeTarget, ok := al.turns.activeRouteSessions.Load(routeScope); ok {
		target, targetOK := activeTarget.(*inboundDispatchTarget)
		if targetOK {
			al.touchActiveSessionLifecycle(target)
		}
		return target, targetOK
	}
	allocation, err = al.applySessionLifecycle(allocation, route.SessionPolicy.Lifecycle)
	if err != nil {
		return nil, false
	}
	return &inboundDispatchTarget{
		Route:      route,
		Agent:      agent,
		Allocation: allocation,
		SessionKey: al.resolveEffectiveSessionKey(
			allocation.RouteScopeKey,
			allocation.SessionKey,
			msg.SessionKey,
		),
		RouteClaimKey: routeClaimKey,
	}, true
}
