package irc

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ergochat/irc-go/ircevent"
	"github.com/ergochat/irc-go/ircmsg"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

// IRCChannel implements the Channel interface for IRC servers.
type IRCChannel struct {
	*channels.BaseChannel
	bc     *config.Channel
	config *config.IRCSettings
	conn   *ircevent.Connection
	ctx    context.Context
	cancel context.CancelFunc
}

// NewIRCChannel creates a new IRC channel.
func NewIRCChannel(bc *config.Channel, cfg *config.IRCSettings, messageBus *bus.MessageBus) (*IRCChannel, error) {
	if cfg.Server == "" {
		return nil, fmt.Errorf("irc server is required")
	}
	if cfg.Nick == "" {
		return nil, fmt.Errorf("irc nick is required")
	}

	base := channels.NewBaseChannel(bc.Name(), cfg, messageBus, bc.AllowFrom,
		channels.WithMaxMessageLength(400),
		channels.WithGroupTrigger(bc.GroupTrigger),
		channels.WithReasoningChannelID(bc.ReasoningChannelID),
	)

	return &IRCChannel{
		BaseChannel: base,
		bc:          bc,
		config:      cfg,
	}, nil
}

// Start connects to the IRC server and begins listening.
func (c *IRCChannel) Start(ctx context.Context) error {
	logger.InfoC("irc", "Starting IRC channel")
	c.ctx, c.cancel = context.WithCancel(ctx)

	user := c.config.User
	if user == "" {
		user = c.config.Nick
	}
	realName := c.config.RealName
	if realName == "" {
		realName = c.config.Nick
	}
	caps := c.config.RequestCaps
	if len(caps) == 0 {
		caps = []string{"server-time", "message-tags"}
	}

	conn := &ircevent.Connection{
		Server:      c.config.Server,
		Nick:        c.config.Nick,
		User:        user,
		RealName:    realName,
		Password:    c.config.Password.String(),
		UseTLS:      c.config.TLS,
		RequestCaps: caps,
		QuitMessage: "Goodbye",
		Debug:       false,
		Log:         nil,
	}

	if c.config.TLS {
		conn.TLSConfig = &tls.Config{
			ServerName: extractHost(c.config.Server),
		}
	}

	// SASL auth (takes priority over NickServ)
	if c.config.SASLUser != "" && c.config.SASLPassword.String() != "" {
		conn.SASLLogin = c.config.SASLUser
		conn.SASLPassword = c.config.SASLPassword.String()
	}

	// Register event handlers. The connect callback runs when registration
	// completes (376/422) both on the initial connection and on reconnects;
	// only the first result is reported back so Start can fail closed.
	setupDone := make(chan error, 1)
	var setupOnce sync.Once
	conn.AddConnectCallback(func(e ircmsg.Message) {
		err := c.onConnect(conn)
		setupOnce.Do(func() {
			setupDone <- err
		})
	})
	conn.AddCallback("PRIVMSG", func(e ircmsg.Message) {
		c.onPrivmsg(conn, e)
	})

	if err := conn.Connect(); err != nil {
		return fmt.Errorf("irc connect failed: %w", err)
	}

	// AddConnectCallback fires on 376/422, which some servers omit after the
	// 001 welcome; bound the wait so startup cannot hang, and honor context
	// cancellation so gateway shutdown is not blocked.
	select {
	case err := <-setupDone:
		if err != nil {
			conn.Quit()
			return fmt.Errorf("irc setup failed: %w", err)
		}
	case <-ctx.Done():
		conn.Quit()
		return fmt.Errorf("irc setup canceled: %w", ctx.Err())
	case <-time.After(conn.Timeout):
		conn.Quit()
		return fmt.Errorf("irc setup timed out after %s", conn.Timeout)
	}

	c.conn = conn

	// ircevent.Connection.Loop() handles reconnection internally.
	go conn.Loop()

	c.SetRunning(true)
	logger.InfoCF("irc", "IRC channel started", map[string]any{
		"server": c.config.Server,
		"nick":   c.config.Nick,
	})
	return nil
}

// Stop disconnects from the IRC server.
func (c *IRCChannel) Stop(ctx context.Context) error {
	logger.InfoC("irc", "Stopping IRC channel")
	c.SetRunning(false)

	if c.conn != nil {
		c.conn.Quit()
	}
	if c.cancel != nil {
		c.cancel()
	}

	logger.InfoC("irc", "IRC channel stopped")
	return nil
}

// DeliverText sends messages to an IRC channel or user.
func (c *IRCChannel) DeliverText(
	ctx context.Context,
	pending []bus.OutboundMessage,
) channels.DeliveryResult[bus.OutboundMessage] {
	return channels.DeliverSequentially(ctx, pending, c.sendText)
}

func (c *IRCChannel) sendText(ctx context.Context, msg bus.OutboundMessage) ([]string, error) {
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}

	target := msg.ChatID
	if target == "" {
		return nil, fmt.Errorf("chat ID is empty: %w", channels.ErrSendFailed)
	}

	if strings.TrimSpace(msg.Content) == "" {
		return nil, nil
	}

	// Send each line separately (IRC is line-oriented)
	lines := strings.Split(msg.Content, "\n")
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if err := c.conn.Privmsg(target, line); err != nil {
			return nil, fmt.Errorf("send IRC message to %s: %w", target, err)
		}
	}

	logger.DebugCF("irc", "Message sent", map[string]any{
		"target": target,
		"lines":  len(lines),
	})
	return nil, nil
}

// StartTyping implements channels.TypingCapable using IRCv3 +typing client tag.
// Requires typing.enabled in config and server support for message-tags capability.
func (c *IRCChannel) StartTyping(ctx context.Context, chatID string) (func(), error) {
	noop := func() {}

	if !c.bc.Typing.Enabled || !c.IsRunning() || c.conn == nil {
		return noop, nil
	}

	// Check if server supports message-tags (required for TAGMSG)
	if _, ok := c.conn.AcknowledgedCaps()["message-tags"]; !ok {
		return noop, nil
	}

	_ = c.conn.SendWithTags(map[string]string{"+typing": "active"}, "TAGMSG", chatID)

	return func() {
		if c.IsRunning() && c.conn != nil {
			_ = c.conn.SendWithTags(map[string]string{"+typing": "done"}, "TAGMSG", chatID)
		}
	}, nil
}

// extractHost returns the hostname portion of a host:port string.
func extractHost(server string) string {
	host, _, found := strings.Cut(server, ":")
	if found {
		return host
	}
	return server
}
