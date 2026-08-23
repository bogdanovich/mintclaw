package mintclaw

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
)

// mintclawConn represents a single WebSocket connection.

func (c *MintClawChannel) Send(ctx context.Context, msg bus.OutboundMessage) ([]string, error) {
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}
	isThought := outboundMessageIsThought(msg)
	isToolCalls := outboundMessageIsToolCalls(msg)
	msgID := uuid.New().String()

	payload := map[string]any{
		PayloadKeyContent: msg.Content,
		"message_id":      msgID,
	}
	setOutboundIdentityPayload(payload, msg)
	if modelName := strings.TrimSpace(msg.Context.Raw[PayloadKeyModelName]); modelName != "" {
		payload[PayloadKeyModelName] = modelName
	}
	metadata := bus.OutboundMetadataFromMessage(msg)
	switch {
	case isThought:
		payload[PayloadKeyKind] = MessageKindThought
	case isToolCalls:
		payload[PayloadKeyKind] = MessageKindToolCalls
		if toolCalls, ok := mintclawToolCallsPayload(msg); ok {
			payload[PayloadKeyToolCalls] = toolCalls
		}
	case metadata.IsFinalReply():
		payload[PayloadKeyKind] = MessageKindFinalReply
	}
	setOutboundControlPayload(payload, metadata)
	setContextUsagePayload(payload, msg.ContextUsage)
	outMsg := newMessage(TypeMessageCreate, payload)

	if err := c.broadcastToSessionContext(ctx, msg.ChatID, outMsg); err != nil {
		return nil, err
	}
	return []string{msgID}, nil
}

// EditMessage implements channels.MessageEditor.
func (c *MintClawChannel) EditMessage(ctx context.Context, chatID string, messageID string, content string) error {
	return c.editMessage(ctx, chatID, messageID, content, nil)
}

func (c *MintClawChannel) EditMessageWithPayload(
	ctx context.Context,
	chatID string,
	messageID string,
	payload map[string]any,
) error {
	return c.editMessagePayload(ctx, chatID, messageID, payload, nil)
}

// DeleteMessage implements channels.MessageDeleter.
func (c *MintClawChannel) DeleteMessage(ctx context.Context, chatID string, messageID string) error {
	outMsg := newMessage(TypeMessageDelete, map[string]any{
		"message_id": messageID,
	})
	return c.broadcastToSessionContext(ctx, chatID, outMsg)
}

// StartTyping implements channels.TypingCapable.
func (c *MintClawChannel) StartTyping(ctx context.Context, chatID string) (func(), error) {
	startMsg := newMessage(TypeTypingStart, nil)
	if err := c.broadcast(ctx, chatID, startMsg); err != nil {
		return func() {}, err
	}
	return func() {
		stopMsg := newMessage(TypeTypingStop, nil)
		_ = c.broadcast(c.ctx, chatID, stopMsg)
	}, nil
}

// SendPlaceholder implements channels.PlaceholderCapable.
// It sends a placeholder message via the MintClaw Protocol that will later be
// edited to the actual response via EditMessage (channels.MessageEditor).
func (c *MintClawChannel) SendPlaceholder(ctx context.Context, chatID string) (string, error) {
	if !c.bc.Placeholder.Enabled {
		return "", nil
	}

	text := c.bc.Placeholder.GetRandomText()

	msgID := uuid.New().String()
	outMsg := newMessage(TypeMessageCreate, map[string]any{
		PayloadKeyContent:     text,
		PayloadKeyPlaceholder: true,
		"message_id":          msgID,
	})

	if err := c.broadcast(ctx, chatID, outMsg); err != nil {
		return "", err
	}

	return msgID, nil
}

// BeginStream implements channels.StreamingCapable for MintClaw WebUI.
func (c *MintClawChannel) BeginStream(ctx context.Context, chatID string) (channels.Streamer, error) {
	return c.beginStream(ctx, chatID, "", "", runtimeevents.TraceScope{})
}

// BeginStreamForScope preserves live turn correlation for operator clients.
func (c *MintClawChannel) BeginStreamForScope(
	ctx context.Context,
	chatID string,
	sessionKey string,
	requestID string,
	traceScope runtimeevents.TraceScope,
) (channels.Streamer, error) {
	return c.beginStream(ctx, chatID, sessionKey, requestID, traceScope)
}

func (c *MintClawChannel) beginStream(
	ctx context.Context,
	chatID string,
	sessionKey string,
	requestID string,
	traceScope runtimeevents.TraceScope,
) (channels.Streamer, error) {
	if c == nil || c.config == nil || !c.config.Streaming.Enabled {
		return nil, fmt.Errorf("streaming disabled in config")
	}
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}
	streamCfg := c.config.Streaming.WithDefaults(0, 1)
	return &mintclawStreamer{
		channel:          c,
		chatID:           chatID,
		sessionKey:       sessionKey,
		requestID:        requestID,
		traceScope:       traceScope,
		throttleInterval: time.Duration(streamCfg.ThrottleSeconds) * time.Second,
		minGrowth:        streamCfg.MinGrowthChars,
	}, nil
}

func (c *MintClawChannel) editMessage(
	ctx context.Context,
	chatID string,
	messageID string,
	content string,
	contextUsage *bus.ContextUsage,
) error {
	return c.editMessagePayload(ctx, chatID, messageID, map[string]any{
		PayloadKeyContent: content,
	}, contextUsage)
}

func (c *MintClawChannel) editMessagePayload(
	ctx context.Context,
	chatID string,
	messageID string,
	payload map[string]any,
	contextUsage *bus.ContextUsage,
) error {
	if payload == nil {
		payload = map[string]any{}
	}
	normalized := make(map[string]any, len(payload)+1)
	for key, value := range payload {
		normalized[key] = value
	}
	if _, ok := normalized[PayloadKeyContent]; !ok {
		normalized[PayloadKeyContent] = ""
	}
	normalized["message_id"] = messageID
	setContextUsagePayload(normalized, contextUsage)
	outMsg := newMessage(TypeMessageUpdate, normalized)
	return c.broadcastToSessionContext(ctx, chatID, outMsg)
}
