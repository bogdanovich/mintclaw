package telegram

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	"github.com/mymmrac/telego/telegoapi"
	tu "github.com/mymmrac/telego/telegoutil"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

const defaultTelegramEditRequestTimeout = 10 * time.Second

// EditMessage implements channels.MessageEditor.
func (c *TelegramChannel) EditMessage(
	ctx context.Context,
	chatID string,
	messageID string,
	content string,
) error {
	return c.editMessageText(ctx, chatID, messageID, content, true)
}

func (c *TelegramChannel) EditToolFeedbackMessage(
	ctx context.Context,
	chatID string,
	messageID string,
	content string,
) error {
	return c.editMessageText(ctx, chatID, messageID, content, false)
}

func (c *TelegramChannel) editMessageText(
	ctx context.Context,
	chatID string,
	messageID string,
	content string,
	useRichMessages bool,
) error {
	timeout := c.editRequestTimeout
	if timeout <= 0 {
		timeout = defaultTelegramEditRequestTimeout
	}
	requestCtx, requestCancel := context.WithTimeout(ctx, timeout)
	defer requestCancel()
	ctx = requestCtx

	useMarkdownV2 := c.tgCfg.UseMarkdownV2
	cid, _, err := parseTelegramChatID(chatID)
	if err != nil {
		return err
	}
	mid, err := strconv.Atoi(messageID)
	if err != nil {
		return err
	}
	var editMsg *telego.EditMessageTextParams
	if useRichMessages && c.richMessagesEnabled(useMarkdownV2) {
		richMessage := renderTelegramOutboundRichMessage(content)
		editMsg = tu.EditMessageText(tu.ID(cid), mid, "")
		editMsg.RichMessage = &richMessage
	} else {
		parsedContent := parseContent(content, useMarkdownV2)
		editMsg = tu.EditMessageText(tu.ID(cid), mid, parsedContent)
		if useMarkdownV2 {
			editMsg.WithParseMode(telego.ModeMarkdownV2)
		} else {
			editMsg.WithParseMode(telego.ModeHTML)
		}
	}
	_, err = c.bot.EditMessageText(ctx, editMsg)
	if err != nil {
		// If it failed because it was already modified (likely from a previous
		// attempt that timed out on our end but landed on Telegram), we treat
		// it as success to prevent the Manager from sending a duplicate message.
		if strings.Contains(err.Error(), "message is not modified") {
			return nil
		}

		// Only fallback to plain text for formatting/rich-message errors. Network
		// errors or timeouts should not trigger a retry with different content.
		if useRichMessages && c.richMessagesEnabled(useMarkdownV2) &&
			(shouldFallbackFromRichMessage(err) || shouldFallbackToPlainText(err)) {
			logger.WarnCF(
				"telegram",
				"rich edit rejected, falling back to plain text",
				map[string]any{
					"chat_id": chatID,
					"mid":     mid,
					"error":   err.Error(),
				},
			)
			textEditMsg := tu.EditMessageText(tu.ID(cid), mid, parseContent(content, useMarkdownV2))
			if useMarkdownV2 {
				textEditMsg.WithParseMode(telego.ModeMarkdownV2)
			} else {
				textEditMsg.WithParseMode(telego.ModeHTML)
			}
			_, err = c.bot.EditMessageText(ctx, textEditMsg)
			if err != nil && shouldFallbackToPlainText(err) {
				logFormattingFallback(err, useMarkdownV2)
				_, err = c.bot.EditMessageText(
					ctx,
					tu.EditMessageText(tu.ID(cid), mid, unwrapTelegramRichFooter(content)),
				)
			}
		} else if shouldFallbackToPlainText(err) {
			logFormattingFallback(err, useMarkdownV2)
			_, err = c.bot.EditMessageText(
				ctx,
				tu.EditMessageText(tu.ID(cid), mid, unwrapTelegramRichFooter(content)),
			)
		}
	}

	if err != nil {
		if strings.Contains(err.Error(), "message is not modified") {
			return nil
		}

		if isPostConnectError(err) {
			logger.WarnCF(
				"telegram",
				"EditMessage likely landed but result is unknown; swallowing error to prevent duplicate",
				map[string]any{
					"chat_id": chatID,
					"mid":     mid,
					"error":   err.Error(),
				},
			)
			return nil // Swallow to prevent Manager fallback to a new SendMessage
		}
	}

	return err
}

// DeleteMessage implements channels.MessageDeleter.
func (c *TelegramChannel) DeleteMessage(
	ctx context.Context,
	chatID string,
	messageID string,
) error {
	cid, _, err := parseTelegramChatID(chatID)
	if err != nil {
		return err
	}
	mid, err := strconv.Atoi(messageID)
	if err != nil {
		return err
	}
	err = c.bot.DeleteMessage(ctx, &telego.DeleteMessageParams{
		ChatID:    tu.ID(cid),
		MessageID: mid,
	})
	if telegramMessageDeleteAlreadyAbsent(err) {
		return nil
	}
	return err
}

func telegramMessageDeleteAlreadyAbsent(err error) bool {
	var apiErr *telegoapi.Error
	if !errors.As(err, &apiErr) || apiErr.ErrorCode != 400 {
		return false
	}
	description := strings.ToLower(strings.TrimSpace(apiErr.Description))
	return strings.Contains(description, "message to delete not found") ||
		strings.Contains(description, "message not found")
}

func outboundMessageIsToolFeedback(msg bus.OutboundMessage) bool {
	return bus.OutboundMetadataFromMessage(msg).IsToolFeedback()
}

// SendPlaceholder implements channels.PlaceholderCapable.
// It sends a placeholder message (e.g. "Thinking... 💭") that will later be
// edited to the actual response via EditMessage (channels.MessageEditor).
func (c *TelegramChannel) SendPlaceholder(ctx context.Context, chatID string) (string, error) {
	phCfg := c.bc.Placeholder
	if !phCfg.Enabled {
		return "", nil
	}

	text := phCfg.GetRandomText()

	cid, threadID, err := parseTelegramChatID(chatID)
	if err != nil {
		return "", err
	}

	phMsg := tu.Message(tu.ID(cid), text)
	phMsg.MessageThreadID = threadID
	pMsg, err := c.bot.SendMessage(ctx, phMsg)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%d", pMsg.MessageID), nil
}
