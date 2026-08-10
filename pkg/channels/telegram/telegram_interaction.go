package telegram

import (
	"strings"

	"github.com/mymmrac/telego"

	"github.com/bogdanovich/mintclaw/pkg/bus"
)

type telegramQuestionControlKey struct {
	chatID   int64
	threadID int
	senderID string
}

func (c *TelegramChannel) updateQuestionControls(msg bus.OutboundMessage, chatID int64, threadID int) {
	metadata := bus.OutboundMetadataFromMessage(msg)
	if !metadata.IsQuestionPrompt() && !metadata.RemovesInteractionControls() {
		return
	}
	key := telegramQuestionControlKey{
		chatID: chatID, threadID: threadID, senderID: strings.TrimSpace(msg.Context.SenderID),
	}
	if key.senderID == "" {
		return
	}
	c.questionControlsMu.Lock()
	defer c.questionControlsMu.Unlock()
	if metadata.RemovesInteractionControls() {
		delete(c.questionControls, key)
		return
	}
	choices := metadata.InteractionChoices()
	if len(choices) == 0 {
		return
	}
	if c.questionControls == nil {
		c.questionControls = make(map[telegramQuestionControlKey]map[string]struct{})
	}
	allowed := make(map[string]struct{}, len(choices))
	for _, choice := range choices {
		allowed[choice] = struct{}{}
	}
	c.questionControls[key] = allowed
}

// SyncInteractionControls projects durable question routing state without
// delivering a Telegram message.
func (c *TelegramChannel) SyncInteractionControls(msg bus.OutboundMessage) error {
	chatID, threadID, err := resolveTelegramOutboundTarget(msg.ChatID, &msg.Context)
	if err != nil {
		return err
	}
	c.updateQuestionControls(msg, chatID, threadID)
	return nil
}

func (c *TelegramChannel) telegramQuestionControlResponse(
	message *telego.Message,
	senderID string,
) string {
	if c == nil || message == nil {
		return ""
	}
	response := strings.TrimSpace(message.Text)
	if response == "" || response != message.Text {
		return ""
	}
	key := telegramQuestionControlKey{
		chatID: message.Chat.ID, threadID: message.MessageThreadID, senderID: strings.TrimSpace(senderID),
	}
	c.questionControlsMu.RLock()
	_, ok := c.questionControls[key][response]
	c.questionControlsMu.RUnlock()
	if !ok {
		return ""
	}
	return response
}
