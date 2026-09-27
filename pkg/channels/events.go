package channels

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

const (
	deliveredTranscriptProjectionTimeout = 5 * time.Second
	deliveredTranscriptRetryInitial      = time.Second
	deliveredTranscriptRetryMax          = 30 * time.Second
)

func (m *Manager) publishChannelEvent(
	kind runtimeevents.Kind,
	channelName string,
	scope runtimeevents.Scope,
	severity runtimeevents.Severity,
	payload any,
) {
	if m == nil || m.runtimeEvents == nil {
		return
	}
	if scope.Channel == "" {
		scope.Channel = channelName
	}
	m.runtimeEvents.PublishNonBlocking(runtimeevents.Event{
		Kind:     kind,
		Source:   runtimeevents.Source{Component: "channel", Name: channelName},
		Scope:    scope,
		Severity: severity,
		Payload:  payload,
		Attrs:    channelEventAttrs(payload),
	})
}

func channelEventAttrs(payload any) map[string]any {
	switch payload := payload.(type) {
	case ChannelLifecyclePayload:
		attrs := map[string]any{}
		setAttrString(attrs, "type", payload.Type)
		setAttrString(attrs, "error", payload.Error)
		return attrs
	case ChannelOutboundPayload:
		attrs := map[string]any{}
		setAttrString(attrs, "delivery_id", payload.DeliveryID)
		if len(payload.TraceScopes) > 0 {
			attrs["trace_scopes_count"] = len(payload.TraceScopes)
		}
		if payload.Media {
			attrs["media"] = payload.Media
		}
		if payload.TraceSettlement {
			attrs["trace_settlement"] = true
		}
		if payload.ContentLen > 0 {
			attrs["content_len"] = payload.ContentLen
		}
		if len(payload.MessageIDs) > 0 {
			attrs["message_ids_count"] = len(payload.MessageIDs)
		}
		setAttrString(attrs, "reply_to_message_id", payload.ReplyToMessageID)
		setAttrString(attrs, "error", payload.Error)
		if payload.Retries > 0 {
			attrs["retries"] = payload.Retries
		}
		return attrs
	default:
		return nil
	}
}

func setAttrString(attrs map[string]any, key, value string) {
	if value != "" {
		attrs[key] = value
	}
}

func (m *Manager) publishOutboundSent(
	ctx context.Context,
	channelName string,
	msg bus.OutboundMessage,
	messageIDs []string,
) {
	m.projectDeliveredTranscript(ctx, msg.Transcript, msg.DeliveryID)
	m.publishChannelEvent(
		runtimeevents.KindChannelMessageOutboundSent,
		channelName,
		scopeFromOutboundContext(msg.Context),
		runtimeevents.SeverityInfo,
		ChannelOutboundPayload{
			DeliveryID:       msg.DeliveryID,
			TraceScopes:      append([]runtimeevents.TraceScope(nil), msg.TraceScopes...),
			TraceSettlement:  msg.TraceSettlement,
			ContentLen:       len([]rune(msg.Content)),
			MessageIDs:       append([]string(nil), messageIDs...),
			ReplyToMessageID: msg.ReplyToMessageID,
		},
	)
}

func (m *Manager) publishOutboundQueued(
	channelName string,
	msg bus.OutboundMessage,
) {
	m.publishChannelEvent(
		runtimeevents.KindChannelMessageOutboundQueued,
		channelName,
		scopeFromOutboundContext(msg.Context),
		runtimeevents.SeverityInfo,
		ChannelOutboundPayload{
			DeliveryID:       msg.DeliveryID,
			TraceScopes:      append([]runtimeevents.TraceScope(nil), msg.TraceScopes...),
			TraceSettlement:  msg.TraceSettlement,
			ContentLen:       len([]rune(msg.Content)),
			ReplyToMessageID: msg.ReplyToMessageID,
		},
	)
}

func (m *Manager) publishOutboundFailed(
	channelName string,
	msg bus.OutboundMessage,
	err error,
	media bool,
) {
	payload := ChannelOutboundPayload{
		DeliveryID:       msg.DeliveryID,
		TraceScopes:      append([]runtimeevents.TraceScope(nil), msg.TraceScopes...),
		TraceSettlement:  msg.TraceSettlement,
		Media:            media,
		ContentLen:       len([]rune(msg.Content)),
		ReplyToMessageID: msg.ReplyToMessageID,
		Retries:          maxRetries,
	}
	if err != nil {
		payload.Error = err.Error()
	}
	m.publishChannelEvent(
		runtimeevents.KindChannelMessageOutboundFailed,
		channelName,
		scopeFromOutboundContext(msg.Context),
		runtimeevents.SeverityError,
		payload,
	)
}

func (m *Manager) publishOutboundMediaSent(
	ctx context.Context,
	channelName string,
	msg bus.OutboundMediaMessage,
	messageIDs []string,
) {
	m.projectDeliveredTranscript(ctx, msg.Transcript, msg.DeliveryID)
	m.publishChannelEvent(
		runtimeevents.KindChannelMessageOutboundSent,
		channelName,
		scopeFromOutboundContext(msg.Context),
		runtimeevents.SeverityInfo,
		ChannelOutboundPayload{
			DeliveryID:      msg.DeliveryID,
			TraceScopes:     append([]runtimeevents.TraceScope(nil), msg.TraceScopes...),
			TraceSettlement: msg.TraceSettlement,
			Media:           true,
			MessageIDs:      append([]string(nil), messageIDs...),
		},
	)
}

func (m *Manager) projectDeliveredTranscript(
	ctx context.Context,
	projection *bus.OutboundTranscriptProjection,
	deliveryID string,
) {
	if m == nil || m.transcriptProjector == nil || projection == nil {
		return
	}
	cloned := cloneTranscriptProjection(*projection)
	err := m.settleDeliveredTranscript(context.WithoutCancel(ctx), &cloned, deliveryID)
	if err == nil {
		return
	}
	m.logTranscriptProjectionFailure(&cloned, deliveryID, err)
	m.retryDeliveredTranscript(&cloned, deliveryID)
}

func (m *Manager) settleDeliveredTranscript(
	ctx context.Context,
	projection *bus.OutboundTranscriptProjection,
	deliveryID string,
) error {
	if m == nil || m.transcriptProjector == nil || projection == nil {
		return errors.New("delivered transcript projector is unavailable")
	}
	deliveryID = strings.TrimSpace(deliveryID)
	if deliveryID != "" {
		if projection.DeliveryID != "" && projection.DeliveryID != deliveryID {
			return errors.New("delivered transcript identity does not match delivery")
		}
		projection.DeliveryID = deliveryID
	}
	projectionCtx, cancel := context.WithTimeout(ctx, deliveredTranscriptProjectionTimeout)
	defer cancel()
	if err := m.transcriptProjector.ProjectDeliveredTranscript(projectionCtx, *projection); err != nil {
		return err
	}
	if deliveryID == "" {
		return nil
	}
	if m.outboundOutbox == nil {
		return errors.New("durable transcript projection receipt is unavailable")
	}
	return m.outboundOutbox.MarkTranscriptProjected(deliveryID)
}

func (m *Manager) retryDeliveredTranscript(
	projection *bus.OutboundTranscriptProjection,
	deliveryID string,
) {
	deliveryID = strings.TrimSpace(deliveryID)
	if m == nil || projection == nil || deliveryID == "" || m.outboundOutbox == nil || m.delivery == nil {
		return
	}
	retryCtx := m.delivery.dispatcherContext()
	if retryCtx == nil {
		return
	}
	m.transcriptRetryMu.Lock()
	if m.transcriptRetries == nil {
		m.transcriptRetries = make(map[string]struct{})
	}
	if _, retrying := m.transcriptRetries[deliveryID]; retrying {
		m.transcriptRetryMu.Unlock()
		return
	}
	m.transcriptRetries[deliveryID] = struct{}{}
	m.transcriptRetryMu.Unlock()

	cloned := cloneTranscriptProjection(*projection)
	go func() {
		defer func() {
			m.transcriptRetryMu.Lock()
			delete(m.transcriptRetries, deliveryID)
			m.transcriptRetryMu.Unlock()
		}()
		delay := deliveredTranscriptRetryInitial
		for {
			timer := time.NewTimer(delay)
			select {
			case <-retryCtx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if err := m.settleDeliveredTranscript(retryCtx, &cloned, deliveryID); err == nil {
				return
			} else {
				m.logTranscriptProjectionFailure(&cloned, deliveryID, err)
			}
			delay = min(delay*2, deliveredTranscriptRetryMax)
		}
	}()
}

func (m *Manager) logTranscriptProjectionFailure(
	projection *bus.OutboundTranscriptProjection,
	deliveryID string,
	err error,
) {
	logger.ErrorCF("channels", "Failed to project delivered outbound into transcript", map[string]any{
		"agent_id":    projection.AgentID,
		"session_key": projection.SessionKey,
		"delivery_id": deliveryID,
		"error":       err.Error(),
	})
}

func cloneTranscriptProjection(projection bus.OutboundTranscriptProjection) bus.OutboundTranscriptProjection {
	projection.Media = append([]string(nil), projection.Media...)
	if projection.Scope != nil {
		scope := *projection.Scope
		scope.Dimensions = append([]string(nil), scope.Dimensions...)
		if scope.Values != nil {
			scope.Values = make(map[string]string, len(scope.Values))
			for key, value := range projection.Scope.Values {
				scope.Values[key] = value
			}
		}
		if scope.Epoch != nil {
			epoch := *scope.Epoch
			scope.Epoch = &epoch
		}
		projection.Scope = &scope
	}
	return projection
}

func (m *Manager) publishOutboundMediaQueued(
	channelName string,
	msg bus.OutboundMediaMessage,
) {
	m.publishChannelEvent(
		runtimeevents.KindChannelMessageOutboundQueued,
		channelName,
		scopeFromOutboundContext(msg.Context),
		runtimeevents.SeverityInfo,
		ChannelOutboundPayload{
			DeliveryID:      msg.DeliveryID,
			TraceScopes:     append([]runtimeevents.TraceScope(nil), msg.TraceScopes...),
			TraceSettlement: msg.TraceSettlement,
			Media:           true,
		},
	)
}

func (m *Manager) publishOutboundMediaFailed(
	channelName string,
	msg bus.OutboundMediaMessage,
	err error,
) {
	payload := ChannelOutboundPayload{
		DeliveryID:      msg.DeliveryID,
		TraceScopes:     append([]runtimeevents.TraceScope(nil), msg.TraceScopes...),
		TraceSettlement: msg.TraceSettlement,
		Media:           true,
		Retries:         maxRetries,
	}
	if err != nil {
		payload.Error = err.Error()
	}
	m.publishChannelEvent(
		runtimeevents.KindChannelMessageOutboundFailed,
		channelName,
		scopeFromOutboundContext(msg.Context),
		runtimeevents.SeverityError,
		payload,
	)
}

func scopeFromOutboundContext(ctx bus.InboundContext) runtimeevents.Scope {
	return runtimeevents.Scope{
		Channel:   ctx.Channel,
		Account:   ctx.Account,
		ChatID:    ctx.ChatID,
		TopicID:   ctx.TopicID,
		SpaceID:   ctx.SpaceID,
		SpaceType: ctx.SpaceType,
		ChatType:  ctx.ChatType,
		SenderID:  ctx.SenderID,
		MessageID: ctx.MessageID,
	}
}
