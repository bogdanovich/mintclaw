package mintclaw

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

// mintclawConn represents a single WebSocket connection.

type MintClawChannel struct {
	*channels.BaseChannel
	bc                 *config.Channel
	config             *config.MintClawSettings
	upgrader           websocket.Upgrader
	connections        map[string]*mintclawConn            // connID -> *mintclawConn
	sessionConnections map[string]map[string]*mintclawConn // sessionID -> connID -> *mintclawConn
	connsMu            sync.RWMutex
	ctx                context.Context
	cancel             context.CancelFunc
	// broadcastFn lets tests intercept outbound broadcasts. nil routes to active connections.
	broadcastFn func(ctx context.Context, chatID string, msg MintClawMessage) error
}

// NewMintClawChannel creates a new MintClaw Protocol channel.
func NewMintClawChannel(
	bc *config.Channel,
	cfg *config.MintClawSettings,
	messageBus *bus.MessageBus,
) (*MintClawChannel, error) {
	if cfg.Token.String() == "" {
		return nil, fmt.Errorf("mintclaw token is required")
	}

	base := channels.NewBaseChannel(bc.Name(), cfg, messageBus, bc.AllowFrom)

	allowOrigins := cfg.AllowOrigins
	checkOrigin := func(r *http.Request) bool {
		if len(allowOrigins) == 0 {
			return true // allow all if not configured
		}
		origin := r.Header.Get("Origin")
		for _, allowed := range allowOrigins {
			if allowed == "*" || allowed == origin {
				return true
			}
		}
		return false
	}

	ch := &MintClawChannel{
		BaseChannel: base,
		bc:          bc,
		config:      cfg,
		upgrader: websocket.Upgrader{
			CheckOrigin:     checkOrigin,
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
		},
		connections:        make(map[string]*mintclawConn),
		sessionConnections: make(map[string]map[string]*mintclawConn),
	}
	return ch, nil
}

// createAndAddConnection checks MaxConnections and registers a connection atomically.
func (c *MintClawChannel) createAndAddConnection(
	conn *websocket.Conn,
	sessionID string,
	maxConns int,
) (*mintclawConn, error) {
	c.connsMu.Lock()
	defer c.connsMu.Unlock()
	if len(c.connections) >= maxConns {
		return nil, channels.ErrTemporary
	}

	var connID string
	for {
		connID = uuid.New().String()
		if _, exists := c.connections[connID]; !exists {
			break
		}
	}

	pc := &mintclawConn{
		id:        connID,
		conn:      conn,
		sessionID: sessionID,
	}

	c.connections[pc.id] = pc
	bySession, ok := c.sessionConnections[pc.sessionID]
	if !ok {
		bySession = make(map[string]*mintclawConn)
		c.sessionConnections[pc.sessionID] = bySession
	}
	bySession[pc.id] = pc

	return pc, nil
}

// removeConnection deletes a connection from indexes and returns it when found.
func (c *MintClawChannel) removeConnection(connID string) *mintclawConn {
	c.connsMu.Lock()
	defer c.connsMu.Unlock()

	pc, ok := c.connections[connID]
	if !ok {
		return nil
	}

	delete(c.connections, connID)
	if bySession, ok := c.sessionConnections[pc.sessionID]; ok {
		delete(bySession, connID)
		if len(bySession) == 0 {
			delete(c.sessionConnections, pc.sessionID)
		}
	}

	return pc
}

// takeAllConnections snapshots and clears all connection indexes.
func (c *MintClawChannel) takeAllConnections() []*mintclawConn {
	c.connsMu.Lock()
	defer c.connsMu.Unlock()

	all := make([]*mintclawConn, 0, len(c.connections))
	for _, pc := range c.connections {
		all = append(all, pc)
	}
	clear(c.connections)
	clear(c.sessionConnections)

	return all
}

// sessionConnectionsSnapshot returns all active connections for a session.
func (c *MintClawChannel) sessionConnectionsSnapshot(sessionID string) []*mintclawConn {
	c.connsMu.RLock()
	defer c.connsMu.RUnlock()

	bySession, ok := c.sessionConnections[sessionID]
	if !ok || len(bySession) == 0 {
		return nil
	}

	conns := make([]*mintclawConn, 0, len(bySession))
	for _, pc := range bySession {
		conns = append(conns, pc)
	}
	return conns
}

// currentConnCount returns a lock-protected snapshot of active connection count.
func (c *MintClawChannel) currentConnCount() int {
	c.connsMu.RLock()
	defer c.connsMu.RUnlock()
	return len(c.connections)
}

// Start implements Channel.
func (c *MintClawChannel) Start(ctx context.Context) error {
	logger.InfoC("mintclaw", "Starting MintClaw Protocol channel")
	c.ctx, c.cancel = context.WithCancel(ctx)
	c.SetRunning(true)
	logger.InfoC("mintclaw", "MintClaw Protocol channel started")
	return nil
}

// Stop implements Channel.
func (c *MintClawChannel) Stop(ctx context.Context) error {
	logger.InfoC("mintclaw", "Stopping MintClaw Protocol channel")
	c.SetRunning(false)

	// Close all connections
	for _, pc := range c.takeAllConnections() {
		pc.close()
	}

	if c.cancel != nil {
		c.cancel()
	}
	logger.InfoC("mintclaw", "MintClaw Protocol channel stopped")
	return nil
}

// WebhookPath implements channels.WebhookHandler.
func (c *MintClawChannel) WebhookPath() string { return "/mintclaw/" }

// ServeHTTP implements http.Handler for the shared HTTP server.
func (c *MintClawChannel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/mintclaw")

	switch path {
	case "/ws", "/ws/":
		c.handleWebSocket(w, r)
	default:
		if strings.HasPrefix(path, "/media/") {
			c.handleMediaDownload(w, r)
			return
		}
		http.NotFound(w, r)
	}
}
