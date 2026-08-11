package mintclaw

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/identity"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

// mintclawConn represents a single WebSocket connection.

func (c *MintClawChannel) readLoop(pc *mintclawConn) {
	defer func() {
		pc.close()
		if removed := c.removeConnection(pc.id); removed != nil {
			logger.InfoCF("mintclaw", "WebSocket client disconnected", map[string]any{
				"conn_id":    removed.id,
				"session_id": removed.sessionID,
			})
		}
	}()

	readTimeout := time.Duration(c.config.ReadTimeout) * time.Second
	if readTimeout <= 0 {
		readTimeout = 60 * time.Second
	}

	_ = pc.conn.SetReadDeadline(time.Now().Add(readTimeout))
	pc.conn.SetPongHandler(func(appData string) error {
		_ = pc.conn.SetReadDeadline(time.Now().Add(readTimeout))
		return nil
	})

	// Start ping ticker
	pingInterval := time.Duration(c.config.PingInterval) * time.Second
	if pingInterval <= 0 {
		pingInterval = 30 * time.Second
	}
	go c.pingLoop(pc, pingInterval)

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		_, rawMsg, err := pc.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				logger.DebugCF("mintclaw", "WebSocket read error", map[string]any{
					"conn_id": pc.id,
					"error":   err.Error(),
				})
			}
			return
		}

		_ = pc.conn.SetReadDeadline(time.Now().Add(readTimeout))

		var msg MintClawMessage
		if err := json.Unmarshal(rawMsg, &msg); err != nil {
			errMsg := newError("invalid_message", "failed to parse message")
			_ = pc.writeJSON(c.ctx, errMsg)
			continue
		}

		c.handleMessage(pc, msg)
	}
}

// pingLoop sends periodic ping frames to keep the connection alive.
func (c *MintClawChannel) pingLoop(pc *mintclawConn, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			if pc.closed.Load() {
				return
			}
			err := pc.writeMessage(c.ctx, websocket.PingMessage, nil)
			if err != nil {
				return
			}
		}
	}
}

// handleMessage processes an inbound MintClaw Protocol message.
func (c *MintClawChannel) handleMessage(pc *mintclawConn, msg MintClawMessage) {
	switch msg.Type {
	case TypePing:
		pong := newMessage(TypePong, nil)
		pong.ID = msg.ID
		_ = pc.writeJSON(c.ctx, pong)

	case TypeMessageSend:
		c.handleMessageSend(pc, msg)

	case TypeMediaSend:
		c.handleMessageSend(pc, msg)

	default:
		errMsg := newError("unknown_type", fmt.Sprintf("unknown message type: %s", msg.Type))
		_ = pc.writeJSON(c.ctx, errMsg)
	}
}

// handleMessageSend processes an inbound message.send from a client.
func (c *MintClawChannel) handleMessageSend(pc *mintclawConn, msg MintClawMessage) {
	content, _ := msg.Payload["content"].(string)
	media, err := parseInlineImageMedia(msg.Payload)
	if err != nil {
		errMsg := newErrorWithPayload("invalid_media", err.Error(), map[string]any{
			"request_id": msg.ID,
		})
		_ = pc.writeJSON(c.ctx, errMsg)
		return
	}

	if strings.TrimSpace(content) == "" && len(media) == 0 {
		errMsg := newErrorWithPayload("empty_content", "message content is empty", map[string]any{
			"request_id": msg.ID,
		})
		_ = pc.writeJSON(c.ctx, errMsg)
		return
	}

	sessionID := msg.SessionID
	if sessionID == "" {
		sessionID = pc.sessionID
	}

	chatID := "mintclaw:" + sessionID
	senderID := "mintclaw-user"

	metadata := map[string]string{
		"platform":   "mintclaw",
		"session_id": sessionID,
		"conn_id":    pc.id,
	}

	logger.DebugCF("mintclaw", "Received message", map[string]any{
		"session_id": sessionID,
		"preview":    truncate(content, 50),
		"media":      len(media),
	})

	sender := bus.SenderInfo{
		Platform:    "mintclaw",
		PlatformID:  senderID,
		CanonicalID: identity.BuildCanonicalID("mintclaw", senderID),
	}

	if !c.IsAllowedSender(sender) {
		return
	}

	inboundCtx := bus.InboundContext{
		Channel:   "mintclaw",
		ChatID:    chatID,
		ChatType:  "direct",
		SenderID:  senderID,
		MessageID: msg.ID,
		Raw:       metadata,
	}

	_ = c.HandleInboundContext(c.ctx, chatID, content, media, inboundCtx, sender)
}

// truncate truncates a string to maxLen runes.
