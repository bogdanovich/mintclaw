package matrix

import (
	"context"
	"time"

	"maunium.net/go/mautrix/id"
	_ "modernc.org/sqlite"

	"github.com/bogdanovich/mintclaw/pkg/logger"
)

func (c *MatrixChannel) typingLoop(ctx context.Context, roomID id.RoomID, session *typingSession) {
	sendTyping := func() {
		_, err := c.client.UserTyping(ctx, roomID, true, typingServerTTL)
		if err != nil {
			logger.DebugCF("matrix", "Failed to send typing status", map[string]any{
				"room_id": roomID.String(),
				"error":   err.Error(),
			})
		}
	}

	sendTyping()
	ticker := time.NewTicker(typingRefreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-session.stopCh:
			return
		case <-ticker.C:
			sendTyping()
		}
	}
}

func (c *MatrixChannel) stopTypingSessions(ctx context.Context) {
	c.typingMu.Lock()
	sessions := c.typingSessions
	c.typingSessions = make(map[string]*typingSession)
	c.typingMu.Unlock()

	stopCtx := ctx
	if stopCtx == nil {
		stopCtx = context.Background()
	}
	for roomID, session := range sessions {
		session.stop()
		_, _ = c.client.UserTyping(stopCtx, id.RoomID(roomID), false, 0)
	}
}

func (c *MatrixChannel) baseContext() context.Context {
	if c.ctx != nil {
		return c.ctx
	}
	return context.Background()
}

func (c *MatrixChannel) runRoomKindCacheJanitor(ctx context.Context) {
	ticker := time.NewTicker(roomKindCacheCleanupPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			c.roomKindCache.cleanupExpired(now)
		}
	}
}
