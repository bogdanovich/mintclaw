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
	interaction, interactionContent, projected, err := mintclawInboundInteraction(msg.Payload)
	if err != nil || projected && len(media) != 0 {
		message := "interaction choice payload is invalid"
		if err != nil {
			message = err.Error()
		}
		errMsg := newErrorWithPayload("invalid_interaction", message, map[string]any{
			"request_id": msg.ID,
		})
		_ = pc.writeJSON(c.ctx, errMsg)
		return
	}
	if projected {
		if strings.TrimSpace(content) != interactionContent {
			errMsg := newErrorWithPayload(
				"invalid_interaction",
				"interaction choice content does not match its typed action",
				map[string]any{"request_id": msg.ID},
			)
			_ = pc.writeJSON(c.ctx, errMsg)
			return
		}
		content = interactionContent
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
		"platform": "mintclaw",
		"conn_id":  pc.id,
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
		Channel:         "mintclaw",
		ChatID:          chatID,
		ChatType:        "direct",
		SenderID:        senderID,
		MessageID:       msg.ID,
		ClientSessionID: sessionID,
		Raw:             metadata,
	}
	if projected {
		inboundCtx.ReplyToMessageID = interaction.ResponseMessageID
		inboundCtx.Interaction = interaction
	}

	_ = c.HandleInboundContext(c.ctx, chatID, content, media, inboundCtx, sender)
}

func mintclawInboundInteraction(
	payload map[string]any,
) (bus.InboundInteractionProjection, string, bool, error) {
	rawChoice, present := payload[PayloadKeyInteractionChoice]
	if !present {
		return bus.InboundInteractionProjection{}, "", false, nil
	}
	choiceText, ok := rawChoice.(string)
	choice := bus.InboundInteractionChoice(strings.ToLower(strings.TrimSpace(choiceText)))
	if !ok || choice == "" {
		return bus.InboundInteractionProjection{}, "", false, fmt.Errorf("interaction choice is invalid")
	}
	shortID, shortOK := payload[PayloadKeyInteractionShortID].(string)
	shortID = strings.TrimSpace(shortID)
	promptID, promptOK := payload[PayloadKeyInteractionPrompt].(string)
	promptID = strings.TrimSpace(promptID)
	if !shortOK || shortID == "" || len(shortID) > 64 || strings.ContainsAny(shortID, " \t\r\n") ||
		!promptOK || promptID == "" || len(promptID) > 128 || strings.ContainsAny(promptID, " \t\r\n") {
		return bus.InboundInteractionProjection{}, "", false, fmt.Errorf("interaction identity is invalid")
	}

	var content, response string
	switch choice {
	case bus.InboundInteractionChoiceAllowOnce:
		content, response = "Allow once", "Allow once"
	case bus.InboundInteractionChoiceDeny:
		content, response = "Deny", "Deny"
	case bus.InboundInteractionChoiceCancel:
		content = bus.InboundInteractionCancelLabel
	case bus.InboundInteractionChoiceClarify:
		content = bus.InboundInteractionClarifyLabel
	case bus.InboundInteractionChoiceBack:
		content = bus.InboundInteractionBackLabel
	case bus.InboundInteractionChoiceSkip:
		content, response = bus.InboundInteractionSkipLabel, bus.InboundInteractionSkipLabel
	case bus.InboundInteractionChoiceNotApplicable:
		content, response = bus.InboundInteractionNotApplicableLabel, bus.InboundInteractionNotApplicableLabel
	default:
		return bus.InboundInteractionProjection{}, "", false, fmt.Errorf("interaction choice is unsupported")
	}
	return bus.InboundInteractionProjection{
		Choice: choice, Response: response, ShortID: shortID, ResponseMessageID: promptID,
	}, content, true, nil
}

// truncate truncates a string to maxLen runes.
