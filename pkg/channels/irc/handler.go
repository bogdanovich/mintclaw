package irc

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/ergochat/irc-go/ircevent"
	"github.com/ergochat/irc-go/ircmsg"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/identity"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

// onConnect is called after a successful connection (and on reconnect).
// It returns the first setup error so the initial connection can fail
// closed; reconnect setup failures are logged without the success message.
func (c *IRCChannel) onConnect(conn *ircevent.Connection) error {
	var setupErr error

	// NickServ auth (only if SASL is not configured)
	if c.config.NickServPassword.String() != "" && c.config.SASLUser == "" {
		if err := conn.Privmsg(
			"NickServ",
			"IDENTIFY "+c.config.NickServPassword.String(),
		); err != nil {
			logger.ErrorCF("irc", "Failed to authenticate with NickServ", map[string]any{
				"error": err,
			})
			if setupErr == nil {
				setupErr = fmt.Errorf("irc nickserv identify failed: %w", err)
			}
		}
	}

	// Join configured channels
	for _, ch := range c.config.Channels {
		if err := conn.Join(ch); err != nil {
			logger.ErrorCF("irc", "Failed to join IRC channel", map[string]any{
				"channel": ch,
				"error":   err,
			})
			if setupErr == nil {
				setupErr = fmt.Errorf("irc join %s failed: %w", ch, err)
			}
			continue
		}
		logger.InfoCF("irc", "Joined IRC channel", map[string]any{
			"channel": ch,
		})
	}

	return setupErr
}

// onPrivmsg handles incoming PRIVMSG events.
func (c *IRCChannel) onPrivmsg(conn *ircevent.Connection, e ircmsg.Message) {
	if len(e.Params) < 2 {
		return
	}

	nick := e.Nick()
	currentNick := conn.CurrentNick()

	// Ignore own messages
	if strings.EqualFold(nick, currentNick) {
		return
	}

	target := e.Params[0]  // channel name or bot's nick
	content := e.Params[1] // message text

	// Determine if this is a DM or channel message
	isDM := !strings.HasPrefix(target, "#") && !strings.HasPrefix(target, "&")

	var chatID string

	if isDM {
		chatID = nick
	} else {
		chatID = target
	}

	sender := bus.SenderInfo{
		Platform:    "irc",
		PlatformID:  nick,
		CanonicalID: identity.BuildCanonicalID("irc", nick),
		Username:    nick,
		DisplayName: nick,
	}

	if !c.IsAllowedSender(sender) {
		return
	}

	isMentioned := false

	// For channel messages, check group trigger (mention detection)
	if !isDM {
		isMentioned = isBotMentioned(content, currentNick)
		if isMentioned {
			content = stripBotMention(content, currentNick)
		}
		respond, cleaned := c.ShouldRespondInGroup(isMentioned, content)
		if !respond {
			return
		}
		content = cleaned
	}

	if strings.TrimSpace(content) == "" {
		return
	}

	messageID := fmt.Sprintf("%s-%d", nick, time.Now().UnixNano())

	metadata := map[string]string{
		"platform": "irc",
		"server":   c.config.Server,
	}
	if !isDM {
		metadata["channel"] = target
	}

	inboundCtx := bus.InboundContext{
		Channel:   "irc",
		ChatID:    chatID,
		SenderID:  nick,
		MessageID: messageID,
		Mentioned: isMentioned,
		Raw:       metadata,
	}
	if isDM {
		inboundCtx.ChatType = "direct"
	} else {
		inboundCtx.ChatType = "group"
	}

	_ = c.HandleInboundContext(c.ctx, chatID, content, nil, inboundCtx, sender)
}

// nickMentionedAt returns the byte index where botNick is mentioned in content
// with word-boundary checks, or -1 if not found. Also checks for "nick:" /
// "nick," prefix convention.
func nickMentionedAt(content, botNick string) int {
	lower := strings.ToLower(content)
	lowerNick := strings.ToLower(botNick)

	// "nick:" or "nick," at start (most common IRC convention)
	if strings.HasPrefix(lower, lowerNick+":") || strings.HasPrefix(lower, lowerNick+",") {
		return 0
	}

	// Word-boundary match anywhere in the message
	idx := strings.Index(lower, lowerNick)
	if idx < 0 {
		return -1
	}
	runes := []rune(lower)
	nickRunes := []rune(lowerNick)
	endIdx := idx + len(string(nickRunes))
	before := idx == 0 || !unicode.IsLetter(runes[idx-1]) && !unicode.IsDigit(runes[idx-1])
	after := endIdx >= len(lower) || !unicode.IsLetter(rune(lower[endIdx])) && !unicode.IsDigit(rune(lower[endIdx]))
	if before && after {
		return idx
	}
	return -1
}

// isBotMentioned checks if the bot's nick appears in the message.
func isBotMentioned(content, botNick string) bool {
	return nickMentionedAt(content, botNick) >= 0
}

// stripBotMention removes "nick: " or "nick, " prefix from content.
func stripBotMention(content, botNick string) string {
	idx := nickMentionedAt(content, botNick)
	if idx != 0 {
		return content
	}
	lowerNick := strings.ToLower(botNick)
	lower := strings.ToLower(content)
	for _, sep := range []string{":", ","} {
		prefix := lowerNick + sep
		if strings.HasPrefix(lower, prefix) {
			return strings.TrimSpace(content[len(prefix):])
		}
	}
	return content
}
