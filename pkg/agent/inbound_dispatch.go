package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/routing"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

type inboundDispatchTarget struct {
	Route         routing.ResolvedRoute
	Agent         *AgentInstance
	Allocation    session.Allocation
	SessionKey    string
	RouteClaimKey string
	relationRoot  *inboundRelationRoot
}

type inboundMessageBuildOptions struct {
	skipRelationHistory bool
}

// inboundRelationRoot keeps the event identity needed to classify follow-ups
// while a claimed root is waiting to reach canonical session history.
type inboundRelationRoot struct {
	SpoolID    string
	MessageID  string
	ReceivedAt time.Time
}

type inboundMessageTurn struct {
	Message      bus.InboundMessage
	Agent        *AgentInstance
	Options      turnSpec
	ScopeKey     string
	SessionKey   string
	ModelBinding effectiveModelBinding
}

func (t inboundMessageTurn) Cleanup() {
	t.ModelBinding.Cleanup()
}

func (t inboundMessageTurn) resetMessageToolRound() {
	if t.Agent == nil {
		return
	}
	tool, ok := t.Agent.Tools.Get("message")
	if !ok {
		return
	}
	resetter, ok := tool.(interface{ ResetSentInRound(sessionKey string) })
	if !ok {
		return
	}
	resetter.ResetSentInRound(t.SessionKey)
}

func (al *AgentLoop) buildInboundMessageTurn(
	ctx context.Context,
	msg bus.InboundMessage,
) (inboundMessageTurn, error) {
	return al.buildInboundMessageTurnWithOptions(ctx, msg, inboundMessageBuildOptions{})
}

func (al *AgentLoop) buildInboundMessageTurnWithOptions(
	ctx context.Context,
	msg bus.InboundMessage,
	options inboundMessageBuildOptions,
) (inboundMessageTurn, error) {
	if msg.Context.Channel == "system" {
		msg = al.prepareInboundMessageForAgent(ctx, msg)
		return inboundMessageTurn{Message: msg}, nil
	}
	msg = bus.NormalizeInboundMessage(msg)

	target, err := al.resolveInboundDispatchTarget(msg)
	if err != nil {
		return inboundMessageTurn{}, err
	}
	if err := bindInboundMediaOwnerForTarget(al.mediaStore, target, msg); err != nil {
		return inboundMessageTurn{}, fmt.Errorf("admit inbound media: %w", err)
	}
	return al.buildInboundMessageTurnForTargetWithOptions(ctx, msg, target, options)
}

func (al *AgentLoop) resolveInboundDispatchTarget(msg bus.InboundMessage) (*inboundDispatchTarget, error) {
	route, agent, routeErr := al.resolveMessageRoute(msg)
	if routeErr != nil {
		return nil, routeErr
	}

	allocation := al.allocateRouteSession(route, msg)
	allocation, routeErr = al.applySessionLifecycle(allocation, route.SessionPolicy.Lifecycle)
	if routeErr != nil {
		return nil, routeErr
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
		RouteClaimKey: runtimeRouteClaimKey(allocation.RouteScopeKey, msg.SessionKey),
	}, nil
}

func runtimeRouteClaimKey(routeScopeKey, explicitSessionKey string) string {
	if isExplicitSessionKey(explicitSessionKey) {
		return "session:" + strings.TrimSpace(explicitSessionKey)
	}
	return "route:" + strings.TrimSpace(routeScopeKey)
}

func targetWithInboundRelationRoot(
	target *inboundDispatchTarget,
	msg bus.InboundMessage,
) *inboundDispatchTarget {
	if target == nil {
		return nil
	}
	claimedTarget := *target
	claimedTarget.relationRoot = &inboundRelationRoot{
		SpoolID:    msg.SpoolID,
		MessageID:  strings.TrimSpace(msg.Context.MessageID),
		ReceivedAt: msg.Context.ReceivedAt,
	}
	return &claimedTarget
}

func (al *AgentLoop) buildInboundMessageTurnForTarget(
	ctx context.Context,
	msg bus.InboundMessage,
	target *inboundDispatchTarget,
) (inboundMessageTurn, error) {
	return al.buildInboundMessageTurnForTargetWithOptions(ctx, msg, target, inboundMessageBuildOptions{})
}

func (al *AgentLoop) buildInboundMessageTurnForTargetWithOptions(
	ctx context.Context,
	msg bus.InboundMessage,
	target *inboundDispatchTarget,
	options inboundMessageBuildOptions,
) (inboundMessageTurn, error) {
	var err error
	msg, err = al.prepareInboundMessageForTargetWithOptions(ctx, msg, target, options)
	if err != nil {
		return inboundMessageTurn{}, err
	}
	allocation := target.Allocation
	sessionKey := target.SessionKey
	modelBinding := al.bindEffectiveModel(allocation.RouteScopeKey, target.Agent)

	dispatch := DispatchRequest{
		RouteSessionKey: allocation.RouteScopeKey,
		BaseSessionKey:  allocation.SessionKey,
		SessionKey:      sessionKey,
		InboundContext:  cloneInboundContext(&msg.Context),
		RouteResult:     cloneResolvedRoute(&target.Route),
		SessionScope:    session.CloneScope(&allocation.Scope),
		UserMessage:     msg.Content,
		Media:           append([]string(nil), msg.Media...),
	}
	opts := newTurnSpec(turnModeInbound, dispatch, modelBinding)
	opts.SenderDisplayName = msg.Sender.DisplayName

	return inboundMessageTurn{
		Message:      msg,
		Agent:        target.Agent,
		Options:      opts,
		ScopeKey:     sessionKey,
		SessionKey:   sessionKey,
		ModelBinding: modelBinding,
	}, nil
}

func (al *AgentLoop) prepareInboundMessageForTarget(
	ctx context.Context,
	msg bus.InboundMessage,
	target *inboundDispatchTarget,
) (bus.InboundMessage, error) {
	return al.prepareInboundMessageForTargetWithOptions(ctx, msg, target, inboundMessageBuildOptions{})
}

func (al *AgentLoop) prepareInboundMessageForTargetWithOptions(
	ctx context.Context,
	msg bus.InboundMessage,
	target *inboundDispatchTarget,
	options inboundMessageBuildOptions,
) (bus.InboundMessage, error) {
	msg, _, err := al.prepareInboundMessageForTargetWithAudioStatusAndOptions(ctx, msg, target, options)
	return msg, err
}

func (al *AgentLoop) prepareInboundMessageForTargetWithAudioStatus(
	ctx context.Context,
	msg bus.InboundMessage,
	target *inboundDispatchTarget,
) (bus.InboundMessage, audioTranscriptionStatus, error) {
	return al.prepareInboundMessageForTargetWithAudioStatusAndOptions(
		ctx,
		msg,
		target,
		inboundMessageBuildOptions{},
	)
}

func (al *AgentLoop) prepareInboundMessageForTargetWithAudioStatusAndOptions(
	ctx context.Context,
	msg bus.InboundMessage,
	target *inboundDispatchTarget,
	options inboundMessageBuildOptions,
) (bus.InboundMessage, audioTranscriptionStatus, error) {
	msg, audioStatus := al.prepareInboundMessageForAgentWithAudioStatus(ctx, msg)
	if msg.Context.Relation.IsZero() {
		var history []providers.Message
		if !options.skipRelationHistory && target != nil && target.Agent != nil && target.Agent.Sessions != nil {
			var err error
			history, err = target.Agent.Sessions.ReadTurnHistory(ctx, target.SessionKey)
			if err != nil {
				return msg, audioStatus, fmt.Errorf("read canonical history for inbound relation: %w", err)
			}
		}
		history = historyWithPendingRelationRoot(history, target, msg)
		msg.Context.Relation = classifyPromptCurrentMessageRelation(
			msg.Content,
			msg.Media,
			msg.Context.ReplyToMessageID,
			allowAdjacentMediaFollowupForChatType(msg.Context.ChatType),
			history,
			msg.Context.ReceivedAt,
		)
	}
	if al.turns != nil && al.turns.inbound != nil {
		if err := al.turns.inbound.persistContext(ctx, msg); err != nil {
			return msg, audioStatus, fmt.Errorf("persist classified inbound relation: %w", err)
		}
	}
	return msg, audioStatus, nil
}

func historyWithPendingRelationRoot(
	history []providers.Message,
	target *inboundDispatchTarget,
	msg bus.InboundMessage,
) []providers.Message {
	if target == nil || target.relationRoot == nil || target.relationRoot.ReceivedAt.IsZero() ||
		target.relationRoot.matches(msg) {
		return history
	}

	rootAt := target.relationRoot.ReceivedAt
	for i := range history {
		if history[i].RootTurnStart && history[i].CreatedAt != nil && history[i].CreatedAt.Equal(rootAt) {
			return history
		}
	}

	insertAt := len(history)
	for i := range history {
		if history[i].CreatedAt != nil && !history[i].CreatedAt.Before(rootAt) {
			insertAt = i
			break
		}
	}
	rootMessage := providers.Message{
		Role:          "user",
		CreatedAt:     &rootAt,
		RootTurnStart: true,
	}
	withRoot := make([]providers.Message, 0, len(history)+1)
	withRoot = append(withRoot, history[:insertAt]...)
	withRoot = append(withRoot, rootMessage)
	withRoot = append(withRoot, history[insertAt:]...)
	return withRoot
}

func (root inboundRelationRoot) matches(msg bus.InboundMessage) bool {
	if root.SpoolID != "" && msg.SpoolID != "" {
		return root.SpoolID == msg.SpoolID
	}
	messageID := strings.TrimSpace(msg.Context.MessageID)
	if root.MessageID != "" && messageID != "" {
		return root.MessageID == messageID
	}
	return !root.ReceivedAt.IsZero() && root.ReceivedAt.Equal(msg.Context.ReceivedAt)
}

func normalizeDispatchInboundRelation(
	ctx context.Context,
	agent *AgentInstance,
	dispatch DispatchRequest,
	fallback time.Time,
) (DispatchRequest, error) {
	if dispatch.InboundContext == nil {
		return dispatch, nil
	}
	inboundContext := *dispatch.InboundContext
	dispatch.InboundContext = &inboundContext
	if dispatch.InboundContext.ReceivedAt.IsZero() {
		dispatch.InboundContext.ReceivedAt = fallback.UTC()
	}
	if !dispatch.InboundContext.Relation.IsZero() {
		return dispatch, nil
	}
	var history []providers.Message
	if agent != nil && agent.Sessions != nil {
		var err error
		history, err = agent.Sessions.ReadTurnHistory(ctx, dispatch.SessionKey)
		if err != nil {
			return dispatch, fmt.Errorf("read canonical history for dispatch relation: %w", err)
		}
	}
	dispatch.InboundContext.Relation = classifyPromptCurrentMessageRelation(
		dispatch.UserMessage,
		dispatch.Media,
		dispatch.ReplyToMessageID(),
		allowAdjacentMediaFollowupForChatType(dispatch.ChatType()),
		history,
		dispatch.InboundContext.ReceivedAt,
	)
	return dispatch, nil
}
