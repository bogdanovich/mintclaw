package telegram

import (
	"context"
	"fmt"
	"slices"
	"strconv"
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
	shortID         string
	promptMessageID string
	kind            string
	choices         []string
}

type telegramInteractionReply struct {
	choice   string
	response string
	shortID  string
}

func (c *TelegramChannel) updateInteractionControls(
	msg bus.OutboundMessage,
	chatID int64,
	threadID int,
	promptMessageID string,
) {
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
		shortID := strings.TrimSpace(metadata.InteractionShortID)
		if controls, active := c.interactionControls[key]; active &&
			(shortID == "" || controls.shortID == shortID) &&
			(promptMessageID == "" || controls.promptMessageID == promptMessageID) {
			delete(c.interactionControls, key)
		}
		return
	}
	if c.interactionControls == nil {
		c.interactionControls = make(map[telegramInteractionControlKey]telegramInteractionControls)
	}
	c.interactionControls[key] = telegramInteractionControls{
		shortID:         strings.TrimSpace(metadata.InteractionShortID),
		promptMessageID: strings.TrimSpace(promptMessageID),
		kind:            metadata.InteractionKind,
		choices:         metadata.InteractionChoices(),
	}
}

// SyncInteractionControls projects durable interaction routing state. Terminal
// sync may edit the original prompt to remove its inline controls, but it does
// not deliver a new Telegram message.
func (c *TelegramChannel) SyncInteractionControls(msg bus.OutboundMessage) error {
	chatID, threadID, err := resolveTelegramOutboundTarget(msg.ChatID, &msg.Context)
	if err != nil {
		return err
	}
	c.updateInteractionControls(msg, chatID, threadID, strings.TrimSpace(msg.ReplyToMessageID))
	if msg.Metadata.RemovesInteractionControls() && strings.TrimSpace(msg.ReplyToMessageID) != "" {
		messageID, parseErr := strconv.Atoi(strings.TrimSpace(msg.ReplyToMessageID))
		if parseErr != nil || messageID <= 0 {
			return fmt.Errorf("invalid interaction prompt message ID %q", msg.ReplyToMessageID)
		}
		return c.removeInteractionReplyMarkup(c.ctx, chatID, messageID)
	}
	return nil
}

func (c *TelegramChannel) removeInteractionReplyMarkup(ctx context.Context, chatID int64, messageID int) error {
	if c == nil || c.bot == nil {
		return nil
	}
	timeout := c.interactionUITimeout
	if timeout <= 0 {
		timeout = defaultTelegramInteractionUITimeout
	}
	baseCtx := ctx
	if baseCtx == nil {
		baseCtx = c.ctx
	}
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	uiCtx, cancel := context.WithTimeout(baseCtx, timeout)
	defer cancel()
	_, err := c.bot.EditMessageReplyMarkup(uiCtx, &telego.EditMessageReplyMarkupParams{
		ChatID: telego.ChatID{ID: chatID}, MessageID: messageID,
		ReplyMarkup: &telego.InlineKeyboardMarkup{InlineKeyboard: [][]telego.InlineKeyboardButton{}},
	})
	if err != nil && !strings.Contains(err.Error(), "message is not modified") {
		return err
	}
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
	repliesToOwnBot := message.ReplyToMessage != nil && c.isOwnBotUser(message.ReplyToMessage.From)
	if controlsActive && controls.kind == bus.OutboundInteractionQuestion {
		if message.Text == bus.InboundInteractionCancelLabel {
			return telegramInteractionReply{
				choice: bus.InboundInteractionChoiceCancel, shortID: shortID,
			}
		}
		if repliesToOwnBot {
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
	if !repliesToOwnBot {
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
		if shortID == "" {
			return telegramInteractionReply{}
		}
		return telegramInteractionReply{response: strings.TrimSpace(content), shortID: shortID}
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
	shortID, _, _ := locateTelegramInteractionFooter(strings.Split(reply.Text, "\n"))
	return shortID
}

func splitTelegramInteractionFooter(text string) (string, string, bool) {
	lines := strings.Split(text, "\n")
	_, start, ok := locateTelegramInteractionFooter(lines)
	if !ok || start <= 0 {
		return "", "", false
	}
	body := strings.TrimRight(strings.Join(lines[:start], "\n"), "\n")
	footer := strings.TrimSpace(strings.Join(lines[start:], "\n"))
	if body == "" || footer == "" {
		return "", "", false
	}
	return body, footer, true
}

func locateTelegramInteractionFooter(lines []string) (string, int, bool) {
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	if end < 2 {
		return "", 0, false
	}
	last := telegramInteractionFooterFields(lines[end-1])
	previous := telegramInteractionFooterFields(lines[end-2])
	if len(last) == 1 && last[0] == "/stop" {
		if len(previous) >= 2 && previous[0] == "/answer" {
			return previous[1], end - 2, true
		}
		return "", 0, false
	}
	if len(previous) == 3 && len(last) == 3 &&
		previous[0] == "/answer" && last[0] == "/answer" &&
		previous[2] == "allow_once" && last[2] == "deny" &&
		strings.EqualFold(previous[1], last[1]) {
		return previous[1], end - 2, true
	}
	templates := 0
	for index := end - 1; index >= 0; index-- {
		fields := telegramInteractionFooterFields(lines[index])
		if len(fields) == 2 && fields[1] == "…" &&
			strings.TrimSuffix(fields[0], ":") != "" && strings.HasSuffix(fields[0], ":") {
			templates++
			continue
		}
		if templates >= 2 && len(fields) == 2 && fields[0] == "/answer" {
			return fields[1], index, true
		}
		return "", 0, false
	}
	return "", 0, false
}

func telegramInteractionFooterFields(line string) []string {
	return strings.Fields(strings.Trim(strings.TrimSpace(line), "`"))
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

func (c *TelegramChannel) interactionControlsMatchPrompt(
	chatID int64,
	threadID int,
	senderID string,
	shortID string,
	promptMessageID string,
) bool {
	senderID = strings.TrimSpace(senderID)
	shortID = strings.TrimSpace(shortID)
	promptMessageID = strings.TrimSpace(promptMessageID)
	if shortID == "" || promptMessageID == "" {
		return false
	}
	key := telegramInteractionControlKey{
		chatID: chatID, threadID: threadID, senderID: senderID,
	}
	c.interactionControlsMu.RLock()
	defer c.interactionControlsMu.RUnlock()
	controls, active := c.interactionControls[key]
	return active && controls.shortID == shortID && controls.promptMessageID == promptMessageID
}

func (c *TelegramChannel) removeInteractionControls(
	chatID int64,
	threadID int,
	senderID string,
	shortID string,
	promptMessageID string,
) {
	key := telegramInteractionControlKey{
		chatID: chatID, threadID: threadID, senderID: strings.TrimSpace(senderID),
	}
	c.interactionControlsMu.Lock()
	if controls, active := c.interactionControls[key]; active && controls.shortID == shortID &&
		controls.promptMessageID == strings.TrimSpace(promptMessageID) {
		delete(c.interactionControls, key)
	}
	c.interactionControlsMu.Unlock()
}
