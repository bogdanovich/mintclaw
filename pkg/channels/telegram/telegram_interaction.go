package telegram

import (
	"slices"
	"strings"

	"github.com/mymmrac/telego"

	"github.com/bogdanovich/mintclaw/pkg/bus"
)

type telegramQuestionControlKey struct {
	chatID   int64
	threadID int
	senderID string
}

type telegramQuestionControls struct {
	shortID string
	choices []string
}

type telegramInteractionReply struct {
	choice   string
	response string
	shortID  string
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
	if c.questionControls == nil {
		c.questionControls = make(map[telegramQuestionControlKey]telegramQuestionControls)
	}
	c.questionControls[key] = telegramQuestionControls{
		shortID: strings.TrimSpace(msg.Context.Raw[bus.OutboundMetadataKeyInteractionShortID]),
		choices: metadata.InteractionChoices(),
	}
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

func (c *TelegramChannel) telegramInteractionReplyMetadata(
	message *telego.Message,
	content string,
	senderID string,
) telegramInteractionReply {
	if c == nil || message == nil {
		return telegramInteractionReply{}
	}
	shortID := telegramInteractionShortID(message.ReplyToMessage)
	controls, questionActive := c.activeQuestionControls(message, senderID)
	if questionActive {
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

func (c *TelegramChannel) activeQuestionControls(
	message *telego.Message,
	senderID string,
) (telegramQuestionControls, bool) {
	if c == nil || message == nil {
		return telegramQuestionControls{}, false
	}
	key := telegramQuestionControlKey{
		chatID: message.Chat.ID, threadID: message.MessageThreadID, senderID: strings.TrimSpace(senderID),
	}
	c.questionControlsMu.RLock()
	controls, active := c.questionControls[key]
	c.questionControlsMu.RUnlock()
	return controls, active
}
