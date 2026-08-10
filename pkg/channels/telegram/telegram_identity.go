package telegram

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/utils"
)

func (c *TelegramChannel) isOwnBotUser(user *telego.User) bool {
	if c == nil || user == nil || !user.IsBot {
		return false
	}

	botID, botUsername := c.ownBotIdentity()
	if botID != 0 && user.ID == botID {
		return true
	}
	if botUsername == "" {
		return false
	}
	return strings.EqualFold(strings.TrimPrefix(strings.TrimSpace(user.Username), "@"), botUsername)
}

func (c *TelegramChannel) telegramInteractionChoice(message *telego.Message) string {
	if message != nil && message.Text == bus.InboundInteractionCancelLabel {
		return bus.InboundInteractionChoiceCancel
	}
	if message == nil || message.ReplyToMessage == nil ||
		!c.isOwnBotUser(message.ReplyToMessage.From) {
		return ""
	}

	switch message.Text {
	case "Allow once":
		return bus.InboundInteractionChoiceAllowOnce
	case "Deny":
		return bus.InboundInteractionChoiceDeny
	default:
		return ""
	}
}

func (c *TelegramChannel) telegramInteractionResponse(
	message *telego.Message,
	content string,
	senderID string,
	interactionChoice string,
) string {
	if message == nil || message.ReplyToMessage == nil ||
		!c.isOwnBotUser(message.ReplyToMessage.From) {
		return ""
	}
	isApprovalChoice := interactionChoice == bus.InboundInteractionChoiceAllowOnce ||
		interactionChoice == bus.InboundInteractionChoiceDeny
	if _, active := c.activeQuestionControls(message, senderID); !active && !isApprovalChoice {
		return ""
	}
	return strings.TrimSpace(content)
}

func (c *TelegramChannel) ownBotIdentity() (int64, string) {
	if c == nil {
		return 0, ""
	}

	c.selfMu.RLock()
	botID := c.selfID
	botUsername := c.selfName
	c.selfMu.RUnlock()
	if botID != 0 || botUsername != "" {
		return botID, botUsername
	}

	c.refreshOwnBotIdentity(context.Background())

	c.selfMu.RLock()
	defer c.selfMu.RUnlock()
	return c.selfID, c.selfName
}

func (c *TelegramChannel) ownBotUsername() string {
	_, username := c.ownBotIdentity()
	return username
}

func (c *TelegramChannel) refreshOwnBotIdentity(ctx context.Context) {
	if c == nil || c.bot == nil {
		return
	}

	me, err := c.bot.GetMe(ctx)
	if err != nil {
		logger.DebugCF("telegram", "Telegram bot self lookup failed", map[string]any{
			"error": err.Error(),
		})
		return
	}

	c.selfMu.Lock()
	c.selfID = me.ID
	c.selfName = strings.TrimPrefix(strings.TrimSpace(me.Username), "@")
	c.selfMu.Unlock()
}

func telegramQuotedAuthor(message *telego.Message) string {
	if message == nil || message.From == nil {
		return "unknown"
	}
	if username := strings.TrimSpace(message.From.Username); username != "" {
		return username
	}
	if firstName := strings.TrimSpace(message.From.FirstName); firstName != "" {
		return firstName
	}
	return "unknown"
}

func telegramQuotedContent(message *telego.Message) string {
	if message == nil {
		return ""
	}

	var parts []string
	if text := strings.TrimSpace(message.Text); text != "" {
		parts = append(parts, text)
	}
	if caption := strings.TrimSpace(message.Caption); caption != "" {
		parts = append(parts, caption)
	}
	switch {
	case len(message.Photo) > 0:
		parts = append(parts, "[image: photo]")
	}
	switch {
	case message.Voice != nil:
		parts = append(parts, "[voice]")
	case message.Audio != nil:
		parts = append(parts, "[audio]")
	}
	if message.Document != nil {
		parts = append(parts, "[file]")
	}

	return strings.Join(parts, "\n")
}

func quotedTelegramMediaRefs(
	message *telego.Message,
	resolve func(fileID, ext, filename string) string,
) []string {
	if message == nil || resolve == nil {
		return nil
	}

	var refs []string
	if message.Voice != nil {
		if ref := resolve(message.Voice.FileID, ".ogg", "voice.ogg"); ref != "" {
			refs = append(refs, ref)
		}
	}
	if message.Audio != nil {
		if ref := resolve(message.Audio.FileID, ".mp3", "audio.mp3"); ref != "" {
			refs = append(refs, ref)
		}
	}
	return refs
}

func (c *TelegramChannel) downloadPhoto(ctx context.Context, fileID string) string {
	file, err := c.getFile(ctx, fileID)
	if err != nil {
		logger.ErrorCF("telegram", "Failed to get photo file", map[string]any{
			"error": err.Error(),
		})
		return ""
	}

	return c.downloadFileWithInfo(file, ".jpg")
}

func (c *TelegramChannel) downloadFileWithInfo(file *telego.File, ext string) string {
	if file.FilePath == "" {
		return ""
	}

	url := c.bot.FileDownloadURL(file.FilePath)
	logger.DebugCF("telegram", "File URL", map[string]any{"url": url})

	// Use FilePath as filename for better identification
	filename := file.FilePath + ext
	return utils.DownloadFile(url, filename, utils.DownloadOptions{
		LoggerPrefix: "telegram",
	})
}

func (c *TelegramChannel) downloadFile(ctx context.Context, fileID, ext string) string {
	file, err := c.getFile(ctx, fileID)
	if err != nil {
		logger.ErrorCF("telegram", "Failed to get file", map[string]any{
			"error": err.Error(),
		})
		return ""
	}

	return c.downloadFileWithInfo(file, ext)
}

func (c *TelegramChannel) getFile(ctx context.Context, fileID string) (*telego.File, error) {
	requestCtx, cancel := context.WithTimeout(ctx, telegramFileMetadataTotalTimeout)
	defer cancel()

	var lastErr error
	attempts := 0
	for attempt := 0; attempt < telegramFileMetadataMaxAttempts; attempt++ {
		attempts++
		attemptTimeout := telegramFileMetadataFirstAttemptTimeout
		if attempt > 0 {
			attemptTimeout = telegramFileMetadataRetryTimeout
		}
		attemptCtx, attemptCancel := context.WithTimeout(requestCtx, attemptTimeout)
		file, err := c.bot.GetFile(attemptCtx, &telego.GetFileParams{FileID: fileID})
		attemptCancel()
		if err == nil {
			return file, nil
		}
		lastErr = err
		if attempt == telegramFileMetadataMaxAttempts-1 || !retryableTelegramFileError(err) {
			break
		}

		delay := telegramFileMetadataRetryDelayFor(err)
		select {
		case <-time.After(delay):
		case <-requestCtx.Done():
			return nil, errors.Join(lastErr, requestCtx.Err())
		}
	}
	return nil, fmt.Errorf("telegram file metadata request failed after %d attempt(s): %w", attempts, lastErr)
}

func retryableTelegramFileError(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	var apiErr *ta.Error
	if !errors.As(err, &apiErr) {
		return true
	}
	return apiErr.ErrorCode == http.StatusTooManyRequests || apiErr.ErrorCode >= http.StatusInternalServerError
}

func telegramFileMetadataRetryDelayFor(err error) time.Duration {
	var apiErr *ta.Error
	if errors.As(err, &apiErr) && apiErr.ErrorCode == http.StatusTooManyRequests && apiErr.Parameters != nil &&
		apiErr.Parameters.RetryAfter > 0 {
		return time.Duration(apiErr.Parameters.RetryAfter) * time.Second
	}
	return telegramFileMetadataRetryDelay
}

func telegramRetryDelayFor(err error) time.Duration {
	var apiErr *ta.Error
	if errors.As(err, &apiErr) && apiErr.ErrorCode == http.StatusTooManyRequests && apiErr.Parameters != nil &&
		apiErr.Parameters.RetryAfter > 0 {
		return time.Duration(apiErr.Parameters.RetryAfter) * time.Second
	}
	return 0
}

func wrapTelegramSendError(operation string, err error) error {
	classification := channels.ErrTemporary
	switch {
	case errors.Is(err, channels.ErrNotRunning):
		classification = channels.ErrNotRunning
	case errors.Is(err, channels.ErrSendFailed):
		classification = channels.ErrSendFailed
	case errors.Is(err, channels.ErrRateLimit):
		classification = channels.ErrRateLimit
	}
	var apiErr *ta.Error
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.ErrorCode == http.StatusTooManyRequests:
			classification = channels.ErrRateLimit
		case apiErr.ErrorCode >= http.StatusBadRequest && apiErr.ErrorCode < http.StatusInternalServerError:
			classification = channels.ErrSendFailed
		}
	}
	return fmt.Errorf("%s: %w: %w", operation, classification, err)
}

func telegramMediaFailure(
	messageIDs []string,
	remainder *bus.OutboundMediaMessage,
	err error,
) channels.DeliveryResult[bus.OutboundMediaMessage] {
	var pending []bus.OutboundMediaMessage
	if remainder != nil {
		pending = []bus.OutboundMediaMessage{*remainder}
	}
	return channels.FailedDelivery(
		messageIDs,
		pending,
		telegramRetryDelayFor(err),
		err,
	)
}

func telegramMediaRewindFailure(
	messageIDs []string,
	msg bus.OutboundMediaMessage,
	partIndex int,
	after string,
	err error,
) channels.DeliveryResult[bus.OutboundMediaMessage] {
	cause := fmt.Errorf("telegram rewind media after %s: %w: %w", after, err, channels.ErrSendFailed)
	return telegramMediaPartsFailure(messageIDs, msg, partIndex, cause)
}

func telegramMediaPartsFailure(
	messageIDs []string,
	msg bus.OutboundMediaMessage,
	partIndex int,
	err error,
) channels.DeliveryResult[bus.OutboundMediaMessage] {
	remainder := msg
	remainder.Parts = append([]bus.MediaPart(nil), msg.Parts[partIndex:]...)
	return telegramMediaFailure(messageIDs, &remainder, err)
}
