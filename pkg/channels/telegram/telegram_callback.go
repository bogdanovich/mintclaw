package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/identity"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

const telegramInteractionCallbackPrefix = "mc:i:"

const defaultTelegramInteractionUITimeout = 3 * time.Second

type telegramInteractionCallbackData struct {
	shortID string
	action  string
	index   int
}

func telegramInteractionCallback(shortID, action string, index int) string {
	if strings.TrimSpace(shortID) == "" {
		return ""
	}
	value := telegramInteractionCallbackPrefix + shortID + ":" + action
	if index >= 0 {
		value += ":" + strconv.Itoa(index)
	}
	return value
}

func parseTelegramInteractionCallback(value string) (telegramInteractionCallbackData, bool) {
	if !strings.HasPrefix(value, telegramInteractionCallbackPrefix) {
		return telegramInteractionCallbackData{}, false
	}
	parts := strings.Split(strings.TrimPrefix(value, telegramInteractionCallbackPrefix), ":")
	if len(parts) < 2 || strings.TrimSpace(parts[0]) == "" {
		return telegramInteractionCallbackData{}, false
	}
	parsed := telegramInteractionCallbackData{shortID: parts[0], action: parts[1], index: -1}
	switch parsed.action {
	case "allow", "deny", "cancel":
		return parsed, len(parts) == 2
	case "option":
		if len(parts) != 3 {
			return telegramInteractionCallbackData{}, false
		}
		index, err := strconv.Atoi(parts[2])
		if err != nil || index < 0 {
			return telegramInteractionCallbackData{}, false
		}
		parsed.index = index
		return parsed, true
	default:
		return telegramInteractionCallbackData{}, false
	}
}

func (c *TelegramChannel) handleInteractionCallback(
	ctx context.Context,
	query telego.CallbackQuery,
) error {
	callback, ok := parseTelegramInteractionCallback(query.Data)
	if !ok || query.Message == nil || query.Message.Message() == nil {
		return nil
	}
	message := query.Message.Message()
	platformID := strconv.FormatInt(query.From.ID, 10)
	sender := bus.SenderInfo{
		Platform: "telegram", PlatformID: platformID,
		CanonicalID: identity.BuildCanonicalID("telegram", platformID),
		Username:    query.From.Username, DisplayName: query.From.FirstName,
	}
	if !c.IsAllowedSender(sender) ||
		(message.Chat.IsForum && message.MessageThreadID != 0 && !c.topicAllowed(message.MessageThreadID)) {
		return nil
	}

	content, choice, response, resolved := c.resolveInteractionCallback(
		message.Chat.ID,
		message.MessageThreadID,
		platformID,
		message.MessageID,
		callback,
	)
	metadata := map[string]string{
		"user_id": platformID, "username": query.From.Username,
		"first_name":                             query.From.FirstName,
		"is_group":                               strconv.FormatBool(message.Chat.Type != "private"),
		bus.InboundMetadataKeyInteractionShortID: callback.shortID,
		bus.InboundMetadataKeyInteractionResponseMessageID: strconv.Itoa(message.MessageID),
	}
	if choice != "" {
		metadata[bus.InboundMetadataKeyInteractionChoice] = choice
	}
	if response != "" {
		metadata[bus.InboundMetadataKeyInteractionResponse] = response
	}
	if !resolved {
		metadata[bus.InboundMetadataKeyInteractionResponseError] = "unresolved callback option"
	}
	chatID := strconv.FormatInt(message.Chat.ID, 10)
	if message.Chat.IsForum && message.MessageThreadID != 0 {
		chatID += "/" + strconv.Itoa(message.MessageThreadID)
	}
	inbound := bus.InboundContext{
		Channel: c.Name(), ChatID: chatID, SenderID: platformID,
		MessageID: query.ID, ReplyToMessageID: strconv.Itoa(message.MessageID), Raw: metadata,
	}
	if message.Chat.Type == "private" {
		inbound.ChatType = "direct"
	} else {
		inbound.ChatType = "group"
	}
	if message.MessageThreadID != 0 {
		inbound.TopicID = strconv.Itoa(message.MessageThreadID)
	}
	c.chatIDsMu.Lock()
	c.chatIDs[platformID] = message.Chat.ID
	c.chatIDsMu.Unlock()
	if err := c.HandleMessageWithContext(ctx, chatID, content, nil, inbound, sender); err != nil {
		return err
	}
	c.settleInteractionCallbackUI(
		ctx, message, query.ID, platformID, callback.shortID,
	)
	return nil
}

func (c *TelegramChannel) settleInteractionCallbackUI(
	ctx context.Context,
	message *telego.Message,
	callbackQueryID string,
	senderID string,
	shortID string,
) {
	timeout := c.interactionUITimeout
	if timeout <= 0 {
		timeout = defaultTelegramInteractionUITimeout
	}
	uiCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := c.bot.AnswerCallbackQuery(uiCtx, &telego.AnswerCallbackQueryParams{
		CallbackQueryID: callbackQueryID,
		Text:            telegramInteractionCallbackAcknowledgement(),
	}); err != nil {
		logger.WarnCF("telegram", "Failed to acknowledge interaction callback", map[string]any{
			"callback_query_id": callbackQueryID, "error": err.Error(),
		})
	}
	if uiCtx.Err() != nil {
		return
	}
	if c.interactionControlsOwnedByDifferentSender(
		message.Chat.ID, message.MessageThreadID, senderID, shortID, strconv.Itoa(message.MessageID),
	) {
		return
	}
	if err := c.removeInteractionReplyMarkup(uiCtx, message.Chat.ID, message.MessageID); err != nil {
		logger.WarnCF("telegram", "Failed to remove interaction callback controls", map[string]any{
			"callback_query_id": callbackQueryID, "error": err.Error(),
		})
		return
	}
	c.removeInteractionControls(
		message.Chat.ID, message.MessageThreadID, senderID, shortID, strconv.Itoa(message.MessageID),
	)
}

func telegramInteractionCallbackAcknowledgement() string {
	return "Response received."
}

func (c *TelegramChannel) resolveInteractionCallback(
	chatID int64,
	threadID int,
	senderID string,
	promptMessageID int,
	callback telegramInteractionCallbackData,
) (content, choice, response string, resolved bool) {
	switch callback.action {
	case "allow":
		return "Allow once", bus.InboundInteractionChoiceAllowOnce, "Allow once", true
	case "deny":
		return "Deny", bus.InboundInteractionChoiceDeny, "Deny", true
	case "cancel":
		return bus.InboundInteractionCancelLabel, bus.InboundInteractionChoiceCancel, "", true
	case "option":
		key := telegramInteractionControlKey{chatID: chatID, threadID: threadID, senderID: senderID}
		c.interactionControlsMu.RLock()
		controls, active := c.interactionControls[key]
		c.interactionControlsMu.RUnlock()
		if active && controls.shortID == callback.shortID &&
			controls.promptMessageID == strconv.Itoa(promptMessageID) &&
			callback.index >= 0 && callback.index < len(controls.choices) {
			response = controls.choices[callback.index]
			return response, "", response, true
		}
		return fmt.Sprintf("Interaction option %d", callback.index+1), "", "", false
	default:
		return "Interaction response", "", "", false
	}
}
