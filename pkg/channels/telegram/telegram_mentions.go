package telegram

import (
	"slices"
	"strings"
	"unicode/utf16"

	"github.com/mymmrac/telego"
)

// isBotMentioned checks if the bot is mentioned in the message via entities.
func (c *TelegramChannel) isBotMentioned(message *telego.Message) bool {
	text, entities := telegramEntityTextAndList(message)
	if text == "" || len(entities) == 0 {
		return false
	}

	botUsername := c.ownBotUsername()
	for _, entity := range entities {
		entityText, ok := telegramEntityText(text, entity)
		if !ok {
			continue
		}

		switch entity.Type {
		case telego.EntityTypeMention:
			if botUsername != "" && strings.EqualFold(entityText, "@"+botUsername) {
				return true
			}
		case telego.EntityTypeTextMention:
			if botUsername != "" && entity.User != nil &&
				strings.EqualFold(entity.User.Username, botUsername) {
				return true
			}
		case telego.EntityTypeBotCommand:
			if isBotCommandEntityForThisBot(entityText, botUsername) {
				return true
			}
		}
	}
	return false
}

func (c *TelegramChannel) hasNonBotMention(message *telego.Message) bool {
	text, entities := telegramEntityTextAndList(message)
	if text == "" || len(entities) == 0 {
		return false
	}

	botUsername := c.ownBotUsername()
	for _, entity := range entities {
		entityText, ok := telegramEntityText(text, entity)
		if !ok {
			continue
		}

		switch entity.Type {
		case telego.EntityTypeMention:
			username := strings.TrimPrefix(entityText, "@")
			if username != "" && !strings.EqualFold(username, botUsername) {
				return true
			}
		case telego.EntityTypeTextMention:
			if entity.User == nil {
				continue
			}
			if entity.User.IsBot && botUsername != "" &&
				strings.EqualFold(entity.User.Username, botUsername) {
				continue
			}
			return true
		case telego.EntityTypeBotCommand:
			if strings.Contains(entityText, "@") &&
				!isBotCommandEntityForThisBot(entityText, botUsername) {
				return true
			}
		}
	}
	return false
}

func (c *TelegramChannel) isReplyToNonBotMessage(message *telego.Message) bool {
	if message == nil || message.ReplyToMessage == nil {
		return false
	}
	quoted := message.ReplyToMessage
	if isTelegramServiceMessage(quoted) {
		return false
	}
	if quoted.From == nil {
		return false
	}
	return !c.isOwnBotUser(quoted.From)
}

func isTelegramServiceMessage(message *telego.Message) bool {
	if message == nil {
		return false
	}
	return message.ForumTopicCreated != nil ||
		message.ForumTopicEdited != nil ||
		message.ForumTopicClosed != nil ||
		message.ForumTopicReopened != nil ||
		len(message.NewChatMembers) > 0 ||
		message.LeftChatMember != nil ||
		message.NewChatTitle != "" ||
		len(message.NewChatPhoto) > 0 ||
		message.DeleteChatPhoto ||
		message.GroupChatCreated ||
		message.SupergroupChatCreated ||
		message.ChannelChatCreated
}

func telegramEntityTextAndList(message *telego.Message) (string, []telego.MessageEntity) {
	if message.Text != "" {
		return message.Text, message.Entities
	}
	return message.Caption, message.CaptionEntities
}

func telegramEntityRuneRange(text string, entity telego.MessageEntity) (int, int, bool) {
	if entity.Offset < 0 || entity.Length <= 0 {
		return 0, 0, false
	}
	endOffset := entity.Offset + entity.Length
	if endOffset < entity.Offset {
		return 0, 0, false
	}

	runes := []rune(text)
	start, startOK := telegramUTF16OffsetToRuneIndex(runes, entity.Offset)
	end, endOK := telegramUTF16OffsetToRuneIndex(runes, endOffset)
	if !startOK || !endOK || start >= end {
		return 0, 0, false
	}
	return start, end, true
}

func telegramUTF16OffsetToRuneIndex(runes []rune, offset int) (int, bool) {
	if offset < 0 {
		return 0, false
	}
	if offset == 0 {
		return 0, true
	}

	units := 0
	for index, value := range runes {
		units += utf16.RuneLen(value)
		if units == offset {
			return index + 1, true
		}
		if units > offset {
			return 0, false
		}
	}
	return 0, false
}

func telegramEntityText(text string, entity telego.MessageEntity) (string, bool) {
	start, end, ok := telegramEntityRuneRange(text, entity)
	if !ok {
		return "", false
	}
	return string([]rune(text)[start:end]), true
}

func isBotCommandEntityForThisBot(entityText, botUsername string) bool {
	if !strings.HasPrefix(entityText, "/") {
		return false
	}
	command := strings.TrimPrefix(entityText, "/")
	if command == "" {
		return false
	}

	at := strings.IndexRune(command, '@')
	if at == -1 {
		// A bare /command delivered to this bot is intended for this bot.
		return true
	}

	mentionUsername := command[at+1:]
	if mentionUsername == "" || botUsername == "" {
		return false
	}
	return strings.EqualFold(mentionUsername, botUsername)
}

// stripBotMention removes only Telegram entities that identify this bot. Text
// outside those entity ranges may legitimately contain the same @username.
func (c *TelegramChannel) stripBotMention(message *telego.Message, content string) string {
	botUsername := c.ownBotUsername()
	if botUsername == "" {
		return content
	}
	source, entities := telegramEntityTextAndList(message)
	collectedSource := strings.TrimSpace(source)
	if collectedSource == "" || !strings.HasPrefix(content, collectedSource) {
		return content
	}
	type entityRemoval struct {
		start int
		end   int
	}
	removals := make([]entityRemoval, 0, len(entities))
	sourceRunes := []rune(source)
	for _, entity := range entities {
		entityStart, entityEnd, ok := telegramEntityRuneRange(source, entity)
		if !ok {
			continue
		}
		entityText := string(sourceRunes[entityStart:entityEnd])
		start := entityStart
		switch entity.Type {
		case telego.EntityTypeBotCommand:
			if !isBotCommandEntityForThisBot(entityText, botUsername) {
				continue
			}
			at := strings.IndexRune(entityText, '@')
			if at < 0 {
				continue
			}
			start += len([]rune(entityText[:at]))
		case telego.EntityTypeMention:
			if !strings.EqualFold(entityText, "@"+botUsername) {
				continue
			}
		case telego.EntityTypeTextMention:
			if entity.User == nil || !strings.EqualFold(entity.User.Username, botUsername) {
				continue
			}
		default:
			continue
		}
		removals = append(removals, entityRemoval{
			start: start,
			end:   entityEnd,
		})
	}
	slices.SortFunc(removals, func(left, right entityRemoval) int {
		return right.start - left.start
	})
	for _, removal := range removals {
		if removal.start < 0 || removal.end > len(sourceRunes) || removal.start > removal.end {
			continue
		}
		sourceRunes = append(sourceRunes[:removal.start], sourceRunes[removal.end:]...)
	}
	normalizedSource := strings.TrimSpace(string(sourceRunes))
	remainder := strings.TrimPrefix(content, collectedSource)
	return strings.TrimSpace(normalizedSource + remainder)
}
