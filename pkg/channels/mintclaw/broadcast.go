package mintclaw

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

// mintclawConn represents a single WebSocket connection.

func (c *MintClawChannel) broadcast(ctx context.Context, chatID string, msg MintClawMessage) error {
	if c.broadcastFn != nil {
		return c.broadcastFn(ctx, chatID, msg)
	}
	return c.broadcastToSessionContext(ctx, chatID, msg)
}

// broadcastToSession sends a message to all connections with a matching session.
func (c *MintClawChannel) broadcastToSession(chatID string, msg MintClawMessage) error {
	return c.broadcastToSessionContext(context.Background(), chatID, msg)
}

func (c *MintClawChannel) broadcastToSessionContext(ctx context.Context, chatID string, msg MintClawMessage) error {
	// chatID format: "mintclaw:<sessionID>"
	sessionID := strings.TrimPrefix(chatID, "mintclaw:")
	msg.SessionID = sessionID
	return c.broadcastToConnectionsContext(ctx, sessionID, msg, c.sessionConnectionsSnapshot(sessionID))
}

func (c *MintClawChannel) broadcastToConnectionsContext(
	ctx context.Context,
	sessionID string,
	msg MintClawMessage,
	connections []*mintclawConn,
) error {
	msg.SessionID = sessionID
	type connectionWriteResult struct {
		err error
	}
	results := make(chan connectionWriteResult, len(connections))
	for _, pc := range connections {
		pending := pc.enqueueJSON(ctx, msg)
		go func(pc *mintclawConn, pending <-chan error) {
			err := <-pending
			if err != nil {
				logger.DebugCF("mintclaw", "Write to connection failed", map[string]any{
					"conn_id": pc.id,
					"error":   err.Error(),
				})
			}
			results <- connectionWriteResult{err: err}
		}(pc, pending)
	}

	var canceled, timedOut bool
	for range connections {
		result := <-results
		if result.err != nil {
			canceled = canceled || errors.Is(result.err, context.Canceled)
			timedOut = timedOut || errors.Is(result.err, context.DeadlineExceeded)
		} else {
			// Delivery to any active peer satisfies the session contract. Other
			// already-started writes remain bounded and continue best-effort.
			return nil
		}
	}

	err := fmt.Errorf("no active connections for session %s: %w", sessionID, channels.ErrSendFailed)
	if canceled {
		err = errors.Join(err, context.Canceled)
	}
	if timedOut {
		err = errors.Join(err, context.DeadlineExceeded)
	}
	return err
}

// handleWebSocket upgrades the HTTP connection and manages the WebSocket lifecycle.
