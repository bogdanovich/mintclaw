package telegram

import (
	"slices"
	"strings"

	"github.com/mymmrac/telego"

	"github.com/bogdanovich/mintclaw/pkg/bus"
)

type telegramInteractionControlKey struct {
	chatID   int64
	threadID int
	senderID string
}

type telegramInteractionControls struct {
	shortID string
	kind    string
	choices []string
}

type telegramInteractionReply struct {
	choice   string
	response string
	shortID  string
}

func (c *TelegramChannel) updateInteractionControls(msg bus.OutboundMessage, chatID int64, threadID int) {
	metadata := msg.Metadata
	if !metadata.IsQuestionPrompt() && !metadata.IsApprovalPrompt() &&
		!metadata.RemovesInteractionControls() {
		return
	}
	key := telegramInteractionControlKey{
		chatID: chatID, threadID: threadID, senderID: strings.TrimSpace(msg.Context.SenderID),
	}
	if key.senderID == "" {
		return
	}
	c.interactionControlsMu.Lock()
	defer c.interactionControlsMu.Unlock()
	if metadata.RemovesInteractionControls() {
		delete(c.interactionControls, key)
		return
	}
	if c.interactionControls == nil {
		c.interactionControls = make(map[telegramInteractionControlKey]telegramInteractionControls)
	}
	c.interactionControls[key] = telegramInteractionControls{
		shortID: strings.TrimSpace(metadata.InteractionShortID),
		kind:    metadata.InteractionKind,
		choices: metadata.InteractionChoices(),
	}
}

// SyncInteractionControls projects durable interaction routing state without
// delivering a Telegram message.
func (c *TelegramChannel) SyncInteractionControls(msg bus.OutboundMessage) error {
	chatID, threadID, err := resolveTelegramOutboundTarget(msg.ChatID, &msg.Context)
	if err != nil {
		return err
	}
	c.updateInteractionControls(msg, chatID, threadID)
	return nil
}

func (c *TelegramChannel) telegramInteractionReplyMetadata(
	message *telego.Message,
	content string,
	senderID string,
) telegramInteractionReply {
	if c == nil || message == nil {
		return telegramInteractionReply{}
	}
	shortID := telegramInteractionShortID(message.ReplyToMessage)
	controls, controlsActive := c.activeInteractionControls(message, senderID)
	if controlsActive && controls.kind == bus.OutboundInteractionQuestion {
		if message.Text == bus.InboundInteractionCancelLabel {
			return telegramInteractionReply{
				choice: bus.InboundInteractionChoiceCancel, shortID: shortID,
			}
		}
		if message.ReplyToMessage != nil && c.isOwnBotUser(message.ReplyToMessage.From) {
			return telegramInteractionReply{response: strings.TrimSpace(content), shortID: shortID}
		}
		response := strings.TrimSpace(message.Text)
		if response == "" || response != message.Text {
			return telegramInteractionReply{}
		}
		if slices.Contains(controls.choices, response) {
			return telegramInteractionReply{response: response, shortID: shortID}
		}
		return telegramInteractionReply{}
	}
	if message.ReplyToMessage == nil || !c.isOwnBotUser(message.ReplyToMessage.From) {
		return telegramInteractionReply{}
	}
	switch message.Text {
	case "Allow once":
		return telegramInteractionReply{
			choice: bus.InboundInteractionChoiceAllowOnce, response: strings.TrimSpace(content), shortID: shortID,
		}
	case "Deny":
		return telegramInteractionReply{
			choice: bus.InboundInteractionChoiceDeny, response: strings.TrimSpace(content), shortID: shortID,
		}
	default:
		return telegramInteractionReply{}
	}
}

func (c *TelegramChannel) telegramInteractionMetadata(
	message *telego.Message,
	content string,
	senderID string,
) (string, string) {
	reply := c.telegramInteractionReplyMetadata(message, content, senderID)
	return reply.choice, reply.response
}

func telegramInteractionShortID(reply *telego.Message) string {
	if reply == nil {
		return ""
	}
	var shortID string
	for _, line := range strings.Split(reply.Text, "\n") {
		fields := strings.Fields(strings.Trim(strings.TrimSpace(line), "`"))
		if len(fields) < 2 || fields[0] != "/answer" {
			continue
		}
		candidate := fields[1]
		if shortID != "" && !strings.EqualFold(shortID, candidate) {
			return ""
		}
		shortID = candidate
	}
	return shortID
}

func (c *TelegramChannel) activeInteractionControls(
	message *telego.Message,
	senderID string,
) (telegramInteractionControls, bool) {
	if c == nil || message == nil {
		return telegramInteractionControls{}, false
	}
	key := telegramInteractionControlKey{
		chatID: message.Chat.ID, threadID: message.MessageThreadID, senderID: strings.TrimSpace(senderID),
	}
	c.interactionControlsMu.RLock()
	controls, active := c.interactionControls[key]
	c.interactionControlsMu.RUnlock()
	return controls, active
}

func (c *TelegramChannel) interactionControlsMatch(
	chatID int64,
	threadID int,
	senderID string,
	shortID string,
) bool {
	key := telegramInteractionControlKey{
		chatID: chatID, threadID: threadID, senderID: strings.TrimSpace(senderID),
	}
	c.interactionControlsMu.RLock()
	controls, active := c.interactionControls[key]
	c.interactionControlsMu.RUnlock()
	return active && shortID != "" && controls.shortID == shortID
}

func (c *TelegramChannel) removeInteractionControls(
	chatID int64,
	threadID int,
	senderID string,
	shortID string,
) {
	key := telegramInteractionControlKey{
		chatID: chatID, threadID: threadID, senderID: strings.TrimSpace(senderID),
	}
	c.interactionControlsMu.Lock()
	if controls, active := c.interactionControls[key]; active && controls.shortID == shortID {
		delete(c.interactionControls, key)
	}
	c.interactionControlsMu.Unlock()
}
