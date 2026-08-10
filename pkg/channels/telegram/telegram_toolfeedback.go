package telegram

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/utils"
)

func parseContent(text string, useMarkdownV2 bool) string {
	if useMarkdownV2 {
		return markdownToTelegramMarkdownV2(text)
	}

	return markdownToTelegramHTML(text)
}

func fitToolFeedbackForTelegram(content string, useMarkdownV2 bool, maxParsedLen int) string {
	content = strings.TrimSpace(content)
	if content == "" || maxParsedLen <= 0 {
		return ""
	}
	animationSafeLen := maxParsedLen - channels.MaxToolFeedbackAnimationFrameLength()
	if animationSafeLen <= 0 {
		animationSafeLen = maxParsedLen
	}
	if len([]rune(parseContent(content, useMarkdownV2))) <= animationSafeLen {
		return content
	}

	low := 1
	high := len([]rune(content))
	best := utils.Truncate(content, 1)

	for low <= high {
		mid := (low + high) / 2
		candidate := utils.FitToolFeedbackMessage(content, mid)
		if candidate == "" {
			high = mid - 1
			continue
		}
		if len([]rune(parseContent(candidate, useMarkdownV2))) <= animationSafeLen {
			best = candidate
			low = mid + 1
			continue
		}
		high = mid - 1
	}

	return best
}

func (c *TelegramChannel) PrepareToolFeedbackMessageContent(content string) string {
	if c == nil || c.tgCfg == nil {
		return strings.TrimSpace(content)
	}
	return fitToolFeedbackForTelegram(content, c.tgCfg.UseMarkdownV2, 4096)
}

func telegramToolFeedbackChatKey(chatID string, outboundCtx *bus.InboundContext) string {
	resolvedChatID, threadID, err := resolveTelegramOutboundTarget(chatID, outboundCtx)
	if err != nil || threadID == 0 {
		return strings.TrimSpace(chatID)
	}
	return fmt.Sprintf("%d/%d", resolvedChatID, threadID)
}

func (c *TelegramChannel) ToolFeedbackMessageChatID(
	chatID string,
	outboundCtx *bus.InboundContext,
) string {
	return telegramToolFeedbackChatKey(chatID, outboundCtx)
}

// parseTelegramChatID splits "chatID/threadID" into its components.
// Returns threadID=0 when no "/" is present (non-forum messages).
func parseTelegramChatID(chatID string) (int64, int, error) {
	idx := strings.Index(chatID, "/")
	if idx == -1 {
		cid, err := strconv.ParseInt(chatID, 10, 64)
		return cid, 0, err
	}
	cid, err := strconv.ParseInt(chatID[:idx], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	tid, err := strconv.Atoi(chatID[idx+1:])
	if err != nil {
		return 0, 0, fmt.Errorf("invalid thread ID in chat ID %q: %w", chatID, err)
	}
	return cid, tid, nil
}

func resolveTelegramOutboundTarget(
	chatID string,
	outboundCtx *bus.InboundContext,
) (int64, int, error) {
	targetChatID := channels.EffectiveOutboundChatID(chatID, outboundCtx)
	resolvedChatID, resolvedThreadID, err := parseTelegramChatID(targetChatID)
	if err != nil {
		return 0, 0, err
	}
	if resolvedThreadID != 0 || outboundCtx == nil {
		return resolvedChatID, resolvedThreadID, nil
	}
	topicID := channels.EffectiveOutboundTopicID("", outboundCtx)
	if topicID == "" {
		return resolvedChatID, resolvedThreadID, nil
	}
	if threadID, convErr := strconv.Atoi(topicID); convErr == nil {
		return resolvedChatID, threadID, nil
	}
	return resolvedChatID, resolvedThreadID, nil
}

// ResolveOutboundChatID returns the concrete delivery key used for Telegram
// cleanup state such as typing indicators, reactions, and placeholders. Forum
// topics are registered as "<chat>/<thread>", while the normalized bus context
// keeps ChatID and TopicID separate.
func (c *TelegramChannel) ResolveOutboundChatID(
	chatID string,
	outboundCtx *bus.InboundContext,
) string {
	resolvedChatID, resolvedThreadID, err := resolveTelegramOutboundTarget(chatID, outboundCtx)
	if err != nil {
		return strings.TrimSpace(chatID)
	}
	if resolvedThreadID != 0 {
		return fmt.Sprintf("%d/%d", resolvedChatID, resolvedThreadID)
	}
	return fmt.Sprintf("%d", resolvedChatID)
}

func logFormattingFallback(err error, useMarkdownV2 bool) {
	logger.ErrorCF(
		"telegram",
		fmt.Sprintf(
			"%s formatting rejected, falling back to plain text",
			telegramParseModeName(useMarkdownV2),
		),
		map[string]any{
			"error": err.Error(),
		},
	)
}

func telegramParseModeName(useMarkdownV2 bool) string {
	if useMarkdownV2 {
		return "MarkdownV2"
	}
	return "HTML"
}

func shouldFallbackToPlainText(err error) bool {
	return telegramIsParseModeError(err)
}

func telegramIsMessageTooLong(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "message is too long") ||
		strings.Contains(msg, "message text is too long") ||
		strings.Contains(msg, "message too long") ||
		strings.Contains(msg, "too long")
}

func shouldFallbackFromRichMessage(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return (strings.Contains(msg, "404") && strings.Contains(msg, "not found")) ||
		strings.Contains(msg, "method not found") ||
		strings.Contains(msg, "sendrichmessage not found") ||
		strings.Contains(msg, "sendrichmessage is not supported") ||
		strings.Contains(msg, "rich message is not supported") ||
		strings.Contains(msg, "rich messages are not supported")
}
