package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

func sessionScopeFromOutboundScope(scope *bus.OutboundScope) *session.SessionScope {
	if scope == nil {
		return nil
	}
	result := &session.SessionScope{
		Version:       scope.Version,
		AgentID:       scope.AgentID,
		Channel:       scope.Channel,
		Account:       scope.Account,
		RouteScopeKey: scope.RouteScopeKey,
	}
	if scope.Dimensions != nil {
		result.Dimensions = append([]string(nil), scope.Dimensions...)
	}
	if scope.Values != nil {
		result.Values = make(map[string]string, len(scope.Values))
		for key, value := range scope.Values {
			result.Values[key] = value
		}
	}
	if scope.Epoch != nil {
		result.Epoch = &session.SessionEpoch{
			Strategy: scope.Epoch.Strategy,
			ID:       scope.Epoch.ID,
			Start:    scope.Epoch.Start,
		}
	}
	return result
}

func newOutboundTranscriptProjection(
	agentID, sessionKey string,
	scope *session.SessionScope,
	content string,
	media []string,
) *bus.OutboundTranscriptProjection {
	if strings.TrimSpace(agentID) == "" || strings.TrimSpace(sessionKey) == "" || scope == nil ||
		strings.TrimSpace(content) == "" && len(media) == 0 {
		return nil
	}
	return &bus.OutboundTranscriptProjection{
		AgentID:    strings.TrimSpace(agentID),
		SessionKey: strings.TrimSpace(sessionKey),
		Scope:      outboundScopeFromSessionScope(scope),
		Content:    content,
		Media:      append([]string(nil), media...),
	}
}

func outboundMediaRefs(parts []bus.MediaPart) []string {
	refs := make([]string, 0, len(parts))
	for _, part := range parts {
		if ref := strings.TrimSpace(part.Ref); ref != "" {
			refs = append(refs, ref)
		}
	}
	return refs
}

func (al *AgentLoop) transcriptProjectionFromSourceSession(
	agent *AgentInstance,
	sourceSessionKey, content string,
	media []string,
) *bus.OutboundTranscriptProjection {
	if al == nil || agent == nil || strings.TrimSpace(sourceSessionKey) == "" {
		return nil
	}
	metadataStore, ok := agent.Sessions.(interface {
		GetSessionScope(sessionKey string) *session.SessionScope
	})
	if !ok {
		return nil
	}
	scope := metadataStore.GetSessionScope(sourceSessionKey)
	if scope == nil || strings.TrimSpace(scope.RouteScopeKey) == "" {
		return nil
	}
	baseSessionKey := session.BuildSessionKey(*scope)
	targetSessionKey := al.resolveEffectiveSessionKey(scope.RouteScopeKey, baseSessionKey, "")
	if targetSessionKey == "" || targetSessionKey == sourceSessionKey {
		return nil
	}
	return newOutboundTranscriptProjection(agent.ID, targetSessionKey, scope, content, media)
}

func (al *AgentLoop) resolveOutboundTranscriptTarget(
	ctx context.Context,
	sourceSessionKey string,
	inbound bus.InboundContext,
	content string,
	media []string,
) (*bus.OutboundTranscriptProjection, *AgentInstance, error) {
	if al == nil {
		return nil, nil, errors.New("agent loop is unavailable")
	}
	if strings.TrimSpace(content) == "" && len(media) == 0 {
		return nil, nil, errors.New("delivered transcript content is empty")
	}
	inbound = bus.NormalizeInboundContext(inbound)
	if strings.TrimSpace(inbound.Channel) == "" || strings.TrimSpace(inbound.ChatID) == "" {
		return nil, nil, errors.New("delivered transcript target is incomplete")
	}
	message := bus.InboundMessage{Context: inbound}
	route, agent, err := al.resolveMessageRoute(message)
	if err != nil {
		return nil, nil, err
	}
	allocation := al.allocateRouteSession(route, message)
	allocation, err = al.applySessionLifecycle(allocation, route.SessionPolicy.Lifecycle)
	if err != nil {
		return nil, nil, err
	}
	targetSessionKey := al.resolveEffectiveSessionKey(
		allocation.RouteScopeKey,
		allocation.SessionKey,
		"",
	)
	if targetSessionKey == "" {
		return nil, nil, errors.New("delivered transcript target session is empty")
	}
	if targetSessionKey == strings.TrimSpace(sourceSessionKey) {
		return nil, agent, nil
	}
	projection := newOutboundTranscriptProjection(
		agent.ID,
		targetSessionKey,
		&allocation.Scope,
		content,
		media,
	)
	if projection == nil {
		return nil, nil, errors.New("failed to construct delivered transcript projection")
	}
	return projection, agent, nil
}

func (al *AgentLoop) maybeOutboundTranscriptProjection(
	ctx context.Context,
	sourceSessionKey string,
	inbound bus.InboundContext,
	content string,
	media []string,
) *bus.OutboundTranscriptProjection {
	projection, _, err := al.resolveOutboundTranscriptTarget(ctx, sourceSessionKey, inbound, content, media)
	if err != nil {
		logger.WarnCF("agent", "Skipped outbound transcript projection", map[string]any{
			"channel": inbound.Channel,
			"chat_id": inbound.ChatID,
			"error":   err.Error(),
		})
		return nil
	}
	return projection
}

// PublishProactiveMessage publishes semantic assistant content to a routed
// conversation and requests post-delivery transcript persistence.
func (al *AgentLoop) PublishProactiveMessage(
	ctx context.Context,
	inbound bus.InboundContext,
	content string,
) error {
	projection, agent, err := al.resolveOutboundTranscriptTarget(ctx, "", inbound, content, nil)
	if err != nil {
		return err
	}
	if projection == nil || agent == nil {
		return errors.New("proactive message target is unavailable")
	}
	msg := bus.OutboundMessage{
		Context:    inbound,
		AgentID:    projection.AgentID,
		SessionKey: projection.SessionKey,
		Scope:      projection.Scope,
		Transcript: projection,
		Content:    content,
	}
	markFinalOutbound(&msg)
	_, err = al.publishTransactionMessage(ctx, agent.Workspace, msg)
	return err
}

// ProjectDeliveredTranscript implements channels.DeliveredTranscriptProjector.
func (al *AgentLoop) ProjectDeliveredTranscript(
	ctx context.Context,
	projection bus.OutboundTranscriptProjection,
) error {
	if al == nil {
		return errors.New("agent loop is unavailable")
	}
	agentID := strings.TrimSpace(projection.AgentID)
	sessionKey := strings.TrimSpace(projection.SessionKey)
	if agentID == "" || sessionKey == "" {
		return errors.New("delivered transcript identity is incomplete")
	}
	agent, ok := al.GetRegistry().GetAgent(agentID)
	if !ok || agent == nil {
		return fmt.Errorf("delivered transcript agent %q is unavailable", agentID)
	}
	scope := sessionScopeFromOutboundScope(projection.Scope)
	if scope == nil || strings.TrimSpace(scope.AgentID) != agent.ID {
		return errors.New("delivered transcript scope does not match agent")
	}
	if !deliveredTranscriptSessionMatches(agent, sessionKey, scope) {
		return errors.New("delivered transcript session does not match scope")
	}
	if strings.TrimSpace(projection.Content) == "" && len(projection.Media) == 0 {
		return errors.New("delivered transcript is empty")
	}

	ensureSessionMetadata(agent.Sessions, sessionKey, scope)
	message := providers.Message{
		Role:    "assistant",
		Content: projection.Content,
		Media:   append([]string(nil), projection.Media...),
	}
	writeErr := persistFullSessionMessage(ctx, agent.Sessions, sessionKey, &message)
	var ingestErr error
	if al.contextManager != nil {
		ingestErr = al.contextManager.Ingest(ctx, &IngestRequest{
			Agent:             agent,
			SessionKey:        sessionKey,
			Message:           message,
			CanonicalWriteErr: writeErr,
		})
	}
	return errors.Join(writeErr, ingestErr)
}

func deliveredTranscriptSessionMatches(
	agent *AgentInstance,
	sessionKey string,
	scope *session.SessionScope,
) bool {
	if agent == nil || scope == nil || strings.TrimSpace(sessionKey) == "" {
		return false
	}
	if session.BuildSessionKey(*scope) == sessionKey {
		return true
	}
	metadataStore, ok := agent.Sessions.(interface {
		GetSessionScope(sessionKey string) *session.SessionScope
	})
	if !ok {
		return false
	}
	stored := metadataStore.GetSessionScope(sessionKey)
	return stored != nil && stored.AgentID == scope.AgentID &&
		stored.RouteScopeKey != "" && stored.RouteScopeKey == scope.RouteScopeKey
}
