package telegram

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

func (c *TelegramChannel) DeliverText(
	ctx context.Context,
	pending []bus.OutboundMessage,
) channels.DeliveryResult[bus.OutboundMessage] {
	if len(pending) == 0 {
		return channels.RejectedDelivery[bus.OutboundMessage](errors.New("telegram delivery payload is empty"))
	}
	if !c.IsRunning() {
		return channels.RejectedDelivery[bus.OutboundMessage](channels.ErrNotRunning)
	}
	msg := pending[0]

	useMarkdownV2 := c.tgCfg.UseMarkdownV2

	chatID, threadID, err := resolveTelegramOutboundTarget(msg.ChatID, &msg.Context)
	if err != nil {
		return channels.RejectedDelivery[bus.OutboundMessage](
			fmt.Errorf("invalid chat ID %s: %w", msg.ChatID, channels.ErrSendFailed),
		)
	}

	if msg.Content == "" {
		return channels.SuccessfulDelivery[bus.OutboundMessage](nil)
	}

	isToolFeedback := outboundMessageIsToolFeedback(msg)
	replyMarkup := telegramInteractionReplyMarkup(msg)
	textContent := msg.Content
	if isToolFeedback {
		textContent = fitToolFeedbackForTelegram(msg.Content, useMarkdownV2, 4096)
	}
	queue := make([]string, 0, len(pending))
	queue = append(queue, textContent)
	for _, pendingMessage := range pending[1:] {
		queue = append(queue, pendingMessage.Content)
	}
	result := c.sendTextChunkQueue(ctx, queue, sendChunkParams{
		chatID:        chatID,
		threadID:      threadID,
		replyToID:     msg.ReplyToMessageID,
		useMarkdownV2: useMarkdownV2,
		replyMarkup:   replyMarkup,
	}, c.richMessagesEnabled(useMarkdownV2) && !isToolFeedback && replyMarkup == nil, isToolFeedback)
	if result.Delivered() {
		c.updateInteractionControls(msg, chatID, threadID)
	}
	var remaining []bus.OutboundMessage
	if result.Remaining != nil {
		remaining = make([]bus.OutboundMessage, 0, len(result.Remaining))
		for _, content := range result.Remaining {
			pending := msg
			pending.Content = content
			remaining = append(remaining, pending)
		}
	}
	return channels.DeliveryResult[bus.OutboundMessage]{
		MessageIDs: append([]string(nil), result.MessageIDs...),
		Status:     result.Status,
		Acceptance: result.Acceptance,
		Remaining:  remaining,
		RetryAfter: result.RetryAfter,
		RetryAt:    result.RetryAt,
		Attempts:   result.Attempts,
		Err:        result.Err,
	}
}

type sendChunkParams struct {
	chatID        int64
	threadID      int
	content       string
	replyToID     string
	mdFallback    string
	useMarkdownV2 bool
	replyMarkup   telego.ReplyMarkup
}

func telegramInteractionReplyMarkup(msg bus.OutboundMessage) telego.ReplyMarkup {
	metadata := bus.OutboundMetadataFromMessage(msg)
	shortID := strings.TrimSpace(msg.Context.Raw[bus.OutboundMetadataKeyInteractionShortID])
	if (metadata.IsApprovalPrompt() || metadata.IsQuestionPrompt()) && shortID == "" {
		return nil
	}
	if metadata.IsApprovalPrompt() {
		return &telego.InlineKeyboardMarkup{
			InlineKeyboard: [][]telego.InlineKeyboardButton{{
				{Text: "Allow once", CallbackData: telegramInteractionCallback(shortID, "allow", -1)},
				{Text: "Deny", CallbackData: telegramInteractionCallback(shortID, "deny", -1)},
			}},
		}
	}
	if metadata.IsQuestionPrompt() {
		choices := metadata.InteractionChoices()
		keyboard := make([][]telego.InlineKeyboardButton, 0, len(choices)+1)
		for index, choice := range choices {
			keyboard = append(keyboard, []telego.InlineKeyboardButton{{
				Text: choice, CallbackData: telegramInteractionCallback(shortID, "option", index),
			}})
		}
		keyboard = append(keyboard, []telego.InlineKeyboardButton{{
			Text:         bus.InboundInteractionCancelLabel,
			CallbackData: telegramInteractionCallback(shortID, "cancel", -1),
		}})
		return &telego.InlineKeyboardMarkup{
			InlineKeyboard: keyboard,
		}
	}
	if metadata.RemovesInteractionControls() {
		return &telego.ReplyKeyboardRemove{RemoveKeyboard: true, Selective: true}
	}
	return nil
}

func (c *TelegramChannel) sendTextChunkQueue(
	ctx context.Context,
	queue []string,
	baseParams sendChunkParams,
	useRich bool,
	isToolFeedback bool,
) channels.DeliveryResult[string] {
	var messageIDs []string
	for len(queue) > 0 {
		chunk := queue[0]
		queue = queue[1:]

		content := parseContent(chunk, baseParams.useMarkdownV2)
		payload := telegramTextLimitPayload(chunk, baseParams.useMarkdownV2, useRich)

		if len([]rune(payload)) > telegramTextLimit {
			if isToolFeedback {
				fittedChunk := fitToolFeedbackForTelegram(chunk, baseParams.useMarkdownV2, telegramTextLimit)
				if fittedChunk != "" && fittedChunk != chunk {
					queue = append([]string{fittedChunk}, queue...)
					continue
				}
			}

			runeChunk := []rune(chunk)
			ratio := float64(len(runeChunk)) / float64(len([]rune(payload)))
			smallerLen := int(float64(telegramTextLimit) * ratio * 0.95) // 5% safety margin

			// Guarantee progress: if estimated length is >= chunk length, force it smaller.
			if smallerLen >= len(runeChunk) {
				smallerLen = len(runeChunk) - 1
			}

			if smallerLen <= 0 {
				msgID, err := c.sendChunk(ctx, sendChunkParams{
					chatID:        baseParams.chatID,
					threadID:      baseParams.threadID,
					content:       content,
					replyToID:     baseParams.replyToID,
					mdFallback:    unwrapTelegramRichFooter(chunk),
					useMarkdownV2: baseParams.useMarkdownV2,
					replyMarkup:   baseParams.replyMarkup,
				})
				if err != nil {
					return channels.FailedDelivery(
						messageIDs,
						append([]string{chunk}, queue...),
						telegramRetryDelayFor(err),
						err,
					)
				}
				messageIDs = append(messageIDs, msgID)
				baseParams.replyToID = ""
				baseParams.replyMarkup = nil
				continue
			}

			// Use the estimated smaller length as a guide for SplitMessage.
			// SplitMessage will find natural break points (newlines/spaces) and respect code blocks.
			subChunks := channels.SplitMessage(chunk, smallerLen)

			// Safety fallback: If SplitMessage failed to shorten the chunk, force a manual hard split.
			if len(subChunks) == 1 && subChunks[0] == chunk {
				part1 := string(runeChunk[:smallerLen])
				part2 := string(runeChunk[smallerLen:])
				subChunks = []string{part1, part2}
			}

			// Filter out empty chunks to avoid sending empty messages to Telegram.
			nonEmpty := make([]string, 0, len(subChunks))
			for _, s := range subChunks {
				if s != "" {
					nonEmpty = append(nonEmpty, s)
				}
			}

			// Push sub-chunks back to the front of the queue.
			queue = append(nonEmpty, queue...)
			continue
		}

		params := sendChunkParams{
			chatID:        baseParams.chatID,
			threadID:      baseParams.threadID,
			content:       content,
			replyToID:     baseParams.replyToID,
			mdFallback:    unwrapTelegramRichFooter(chunk),
			useMarkdownV2: baseParams.useMarkdownV2,
			replyMarkup:   baseParams.replyMarkup,
		}

		var msgID string
		var err error
		if useRich {
			msgID, err = c.sendRichChunk(ctx, chunk, params)
		} else {
			msgID, err = c.sendChunk(ctx, params)
		}
		if err != nil {
			if useRich && errors.Is(err, errTelegramMessageTooLong) {
				runeChunk := []rune(chunk)
				if len(runeChunk) <= 1 {
					return channels.FailedDelivery(
						messageIDs,
						append([]string{chunk}, queue...),
						telegramRetryDelayFor(err),
						err,
					)
				}
				smallerLen := len(runeChunk) / 2
				subChunks := channels.SplitMessage(chunk, smallerLen)
				if len(subChunks) == 1 && subChunks[0] == chunk {
					subChunks = []string{
						string(runeChunk[:smallerLen]),
						string(runeChunk[smallerLen:]),
					}
				}
				nonEmpty := make([]string, 0, len(subChunks))
				for _, s := range subChunks {
					if s != "" {
						nonEmpty = append(nonEmpty, s)
					}
				}
				queue = append(nonEmpty, queue...)
				continue
			}
			return channels.FailedDelivery(
				messageIDs,
				append([]string{chunk}, queue...),
				telegramRetryDelayFor(err),
				err,
			)
		}
		messageIDs = append(messageIDs, msgID)
		// Only the first chunk should be a reply; subsequent chunks are normal messages.
		baseParams.replyToID = ""
		baseParams.replyMarkup = nil
	}
	return channels.SuccessfulDelivery[string](messageIDs)
}

func (c *TelegramChannel) richMessagesEnabled(useMarkdownV2 bool) bool {
	// Rich messages use Telegram's rich HTML input. If a channel explicitly
	// requests the legacy MarkdownV2 projector, keep that behavior unchanged.
	return c.tgCfg != nil && c.tgCfg.RichMessages.Enabled && !useMarkdownV2
}

func telegramTextLimitPayload(text string, useMarkdownV2 bool, useRich bool) string {
	content := parseContent(text, useMarkdownV2)
	if !useRich {
		return content
	}
	richContent := markdownToTelegramRichMarkdown(text)
	if len([]rune(content)) > len([]rune(richContent)) {
		return content
	}
	return richContent
}

func telegramClampText(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

func (c *TelegramChannel) sendRichChunk(
	ctx context.Context,
	rawContent string,
	fallbackParams sendChunkParams,
) (string, error) {
	if fallbackParams.content == "" {
		fallbackParams.content = parseContent(rawContent, fallbackParams.useMarkdownV2)
	}
	if fallbackParams.mdFallback == "" {
		fallbackParams.mdFallback = unwrapTelegramRichFooter(rawContent)
	}

	params := &telego.SendRichMessageParams{
		ChatID:          tu.ID(fallbackParams.chatID),
		MessageThreadID: fallbackParams.threadID,
		RichMessage:     renderTelegramOutboundRichMessage(rawContent),
	}

	if fallbackParams.replyToID != "" {
		if mid, parseErr := strconv.Atoi(fallbackParams.replyToID); parseErr == nil {
			params.ReplyParameters = &telego.ReplyParameters{
				MessageID: mid,
			}
		}
	}

	pMsg, err := c.bot.SendRichMessage(ctx, params)
	if err != nil {
		if telegramIsMessageTooLong(err) {
			return "", fmt.Errorf("telegram send rich message too long: %w", errTelegramMessageTooLong)
		}
		if shouldFallbackFromRichMessage(err) || shouldFallbackToPlainText(err) {
			logger.WarnCF(
				"telegram",
				"sendRichMessage rejected, falling back to text",
				map[string]any{
					"chat_id":   fallbackParams.chatID,
					"thread_id": fallbackParams.threadID,
					"reply_to":  fallbackParams.replyToID,
					"error":     err.Error(),
				},
			)
			return c.sendChunk(ctx, fallbackParams)
		}
		logger.WarnCF("telegram", "sendRichMessage failed", map[string]any{
			"chat_id":   fallbackParams.chatID,
			"thread_id": fallbackParams.threadID,
			"reply_to":  fallbackParams.replyToID,
			"error":     err.Error(),
		})
		return "", wrapTelegramSendError("telegram send rich message", err)
	}

	return strconv.Itoa(pMsg.MessageID), nil
}

// sendChunk sends a single message through Telegram's legacy text endpoint.
// Rich-message sends bypass this method except when Telegram rejects rich input.
func (c *TelegramChannel) sendChunk(
	ctx context.Context,
	params sendChunkParams,
) (string, error) {
	tgMsg := tu.Message(tu.ID(params.chatID), params.content)
	tgMsg.MessageThreadID = params.threadID
	if params.useMarkdownV2 {
		tgMsg.WithParseMode(telego.ModeMarkdownV2)
	} else {
		tgMsg.WithParseMode(telego.ModeHTML)
	}

	if params.replyToID != "" {
		if mid, parseErr := strconv.Atoi(params.replyToID); parseErr == nil {
			tgMsg.ReplyParameters = &telego.ReplyParameters{
				MessageID: mid,
			}
		}
	}
	if params.replyMarkup != nil {
		tgMsg.ReplyMarkup = params.replyMarkup
	}

	pMsg, err := c.bot.SendMessage(ctx, tgMsg)
	if err != nil {
		if shouldFallbackToPlainText(err) {
			logFormattingFallback(err, params.useMarkdownV2)

			tgMsg.Text = unwrapTelegramRichFooter(params.mdFallback)
			tgMsg.ParseMode = ""
			pMsg, err = c.bot.SendMessage(ctx, tgMsg)
			if err != nil {
				return "", wrapTelegramSendError("telegram send", err)
			}
		} else {
			logger.WarnCF("telegram", "sendMessage failed", map[string]any{
				"chat_id":    params.chatID,
				"thread_id":  params.threadID,
				"reply_to":   params.replyToID,
				"parse_mode": telegramParseModeName(params.useMarkdownV2),
				"error":      err.Error(),
			})
			return "", wrapTelegramSendError("telegram send", err)
		}
	}

	return strconv.Itoa(pMsg.MessageID), nil
}

// maxTypingDuration limits how long the typing indicator can run.
// Prevents endless typing when the LLM fails/hangs and preSend never invokes cancel.
// Matches channels.Manager's typingStopTTL (5 min) so behavior is consistent.
const maxTypingDuration = 5 * time.Minute

// StartTyping implements channels.TypingCapable.
// It sends ChatAction(typing) immediately and then repeats every 4 seconds
// (Telegram's typing indicator expires after ~5s) in a background goroutine.
// The returned stop function is idempotent and cancels the goroutine.
// The goroutine also exits automatically after maxTypingDuration if cancel is
// never called (e.g. when the LLM fails or times out without publishing).
func (c *TelegramChannel) StartTyping(ctx context.Context, chatID string) (func(), error) {
	cid, threadID, err := parseTelegramChatID(chatID)
	if err != nil {
		return func() {}, err
	}

	action := tu.ChatAction(tu.ID(cid), telego.ChatActionTyping)
	action.MessageThreadID = threadID

	// Send the first typing action immediately
	_ = c.bot.SendChatAction(ctx, action)

	typingCtx, cancel := context.WithCancel(ctx)
	// Cap lifetime so the goroutine cannot run indefinitely if cancel is never called
	maxCtx, maxCancel := context.WithTimeout(typingCtx, maxTypingDuration)
	go func() {
		defer maxCancel()
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-maxCtx.Done():
				return
			case <-ticker.C:
				a := tu.ChatAction(tu.ID(cid), telego.ChatActionTyping)
				a.MessageThreadID = threadID
				_ = c.bot.SendChatAction(typingCtx, a)
			}
		}
	}()

	return cancel, nil
}
