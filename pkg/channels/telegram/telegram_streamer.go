package telegram

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"

	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

// BeginStream implements channels.StreamingCapable.
func (c *TelegramChannel) BeginStream(
	ctx context.Context,
	chatID string,
) (channels.Streamer, error) {
	if !c.tgCfg.Streaming.Enabled {
		return nil, fmt.Errorf("streaming disabled in config")
	}

	cid, threadID, err := parseTelegramChatID(chatID)
	if err != nil {
		return nil, err
	}

	streamCfg := c.tgCfg.Streaming.WithDefaults(3, 200)
	return &telegramStreamer{
		channel:          c,
		bot:              c.bot,
		chatID:           cid,
		threadID:         threadID,
		draftID:          cryptoRandInt(),
		throttleInterval: time.Duration(streamCfg.ThrottleSeconds) * time.Second,
		minGrowth:        streamCfg.MinGrowthChars,
		richMessages:     c.richMessagesEnabled(c.tgCfg.UseMarkdownV2),
	}, nil
}

// telegramStreamer streams partial LLM output via Telegram's sendMessageDraft API.
// Draft update failures are returned to the agent, which decides whether the
// stream was already visible enough to keep or should fall back to Chat().
type telegramStreamer struct {
	channel          *TelegramChannel
	bot              *telego.Bot
	chatID           int64
	threadID         int
	draftID          int
	throttleInterval time.Duration
	minGrowth        int
	richMessages     bool
	lastLen          int
	lastAt           time.Time
	failed           bool
	draftTouched     bool
	mu               sync.Mutex
}

func (s *telegramStreamer) Update(ctx context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.failed {
		return fmt.Errorf("telegram streaming disabled after previous draft failure")
	}

	// Throttle: skip if not enough time or content has passed
	now := time.Now()
	growth := len(content) - s.lastLen
	if s.lastLen > 0 && now.Sub(s.lastAt) < s.throttleInterval && growth < s.minGrowth {
		return nil
	}

	s.draftTouched = true
	var err error
	if s.richMessages {
		if len([]rune(telegramTextLimitPayload(content, false, true))) > telegramTextLimit {
			err = s.bot.SendMessageDraft(ctx, &telego.SendMessageDraftParams{
				ChatID:          s.chatID,
				MessageThreadID: s.threadID,
				DraftID:         s.draftID,
				Text: telegramClampText(
					unwrapTelegramRichFooter(content),
					telegramTextLimit,
				),
			})
		} else {
			err = s.bot.SendRichMessageDraft(ctx, &telego.SendRichMessageDraftParams{
				ChatID:          s.chatID,
				MessageThreadID: s.threadID,
				DraftID:         s.draftID,
				RichMessage:     renderTelegramOutboundRichMessage(content),
			})
			if err != nil && telegramIsMessageTooLong(err) {
				err = s.bot.SendMessageDraft(ctx, &telego.SendMessageDraftParams{
					ChatID:          s.chatID,
					MessageThreadID: s.threadID,
					DraftID:         s.draftID,
					Text: telegramClampText(
						unwrapTelegramRichFooter(content),
						telegramTextLimit,
					),
				})
			}
			if err != nil && (shouldFallbackFromRichMessage(err) || shouldFallbackToPlainText(err)) {
				logger.DebugCF(
					"telegram",
					"rich draft rejected, falling back to plain draft",
					map[string]any{
						"chat_id": s.chatID,
						"error":   err.Error(),
					},
				)
				err = s.bot.SendMessageDraft(ctx, &telego.SendMessageDraftParams{
					ChatID:          s.chatID,
					MessageThreadID: s.threadID,
					DraftID:         s.draftID,
					Text:            markdownToTelegramHTML(content),
					ParseMode:       telego.ModeHTML,
				})
				if err != nil && shouldFallbackToPlainText(err) {
					err = s.bot.SendMessageDraft(ctx, &telego.SendMessageDraftParams{
						ChatID:          s.chatID,
						MessageThreadID: s.threadID,
						DraftID:         s.draftID,
						Text:            unwrapTelegramRichFooter(content),
					})
				}
			}
		}
	} else {
		err = s.bot.SendMessageDraft(ctx, &telego.SendMessageDraftParams{
			ChatID:          s.chatID,
			MessageThreadID: s.threadID,
			DraftID:         s.draftID,
			Text:            markdownToTelegramHTML(content),
			ParseMode:       telego.ModeHTML,
		})
		if err != nil && shouldFallbackToPlainText(err) {
			err = s.bot.SendMessageDraft(ctx, &telego.SendMessageDraftParams{
				ChatID:          s.chatID,
				MessageThreadID: s.threadID,
				DraftID:         s.draftID,
				Text:            unwrapTelegramRichFooter(content),
			})
		}
	}
	if err != nil {
		logger.WarnCF("telegram", "sendMessageDraft failed, disabling streaming", map[string]any{
			"error": err.Error(),
		})
		s.failed = true
		return fmt.Errorf("telegram draft update: %w", err)
	}

	s.lastLen = len(content)
	s.lastAt = now
	return nil
}

func (s *telegramStreamer) Finalize(ctx context.Context, content string) error {
	var err error
	if s.richMessages {
		_, err = s.finalizeTextChunks(ctx, content, sendChunkParams{
			chatID:        s.chatID,
			threadID:      s.threadID,
			useMarkdownV2: false,
		}, true)
	} else {
		_, err = s.finalizeTextChunks(ctx, content, sendChunkParams{
			chatID:        s.chatID,
			threadID:      s.threadID,
			useMarkdownV2: false,
		}, false)
	}

	if err != nil {
		logger.ErrorCF("telegram", "Finalize failed", map[string]any{
			"chat_id": s.chatID,
			"error":   err.Error(),
			"len":     len(content),
		})
		return fmt.Errorf("telegram finalize: %w", err)
	}
	s.Cancel(ctx)
	return nil
}

func (s *telegramStreamer) finalizeTextChunks(
	ctx context.Context,
	content string,
	baseParams sendChunkParams,
	useRich bool,
) ([]string, error) {
	result := channels.DeliverWithRetry(
		ctx,
		[]string{content},
		channels.DeliveryRetryPolicy{
			MaxRetries:     2,
			RetryAmbiguous: true,
		},
		func(ctx context.Context, pending []string) channels.DeliveryResult[string] {
			return s.channel.sendTextChunkQueue(ctx, pending, baseParams, useRich, false)
		},
		nil,
	)
	return result.MessageIDs, result.Err
}

func (s *telegramStreamer) Cancel(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clearDraft(ctx)
}

func (s *telegramStreamer) clearDraft(ctx context.Context) {
	if !s.draftTouched {
		return
	}
	if err := s.bot.SendMessageDraft(ctx, &telego.SendMessageDraftParams{
		ChatID:          s.chatID,
		MessageThreadID: s.threadID,
		DraftID:         s.draftID,
		Text:            " ",
	}); err != nil {
		logger.DebugCF("telegram", "failed to clear streaming draft", map[string]any{
			"chat_id": s.chatID,
			"error":   err.Error(),
		})
	}
	s.lastLen = 0
	s.draftTouched = false
}

// cryptoRandInt returns a non-zero random int using crypto/rand.
func cryptoRandInt() int {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return int(binary.BigEndian.Uint32(b[:])) | 1 // ensure non-zero
}

// isPostConnectError identifies network errors that likely occurred after
// the request was transmitted to Telegram (e.g. dropped connection while
// waiting for response). Swallowing these for edits prevents duplicate
// fallbacks, at the small risk of leaving a stale placeholder if the
// edit never actually reached the server.
func isPostConnectError(err error) bool {
	if err == nil {
		return false
	}

	// Context errors (timeout/canceled) are too broad; they can be triggered
	// locally before any data is sent. Never swallow them.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}

	msg := strings.ToLower(err.Error())
	// Narrowly target connection dropouts where the request likely landed.
	return strings.Contains(msg, "connection reset by peer") ||
		strings.Contains(msg, "unexpected eof") ||
		strings.Contains(msg, "connection closed by foreign host") ||
		strings.Contains(msg, "broken pipe")
}

// VoiceCapabilities returns the voice capabilities of the channel.
func (c *TelegramChannel) VoiceCapabilities() channels.VoiceCapabilities {
	return channels.VoiceCapabilities{ASR: true, TTS: true}
}
