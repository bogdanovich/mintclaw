package telegram

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/media"
)

// Telegram's Bot API documents a 50 MB upload limit for sendVideo and the
// other file-upload methods used here. Decimal bytes avoid accepting a file
// that exceeds the documented bound due to unit ambiguity.
const telegramMediaUploadMaxBytes int64 = 50_000_000

// PreflightMedia validates local attachment sizes before durable admission.
func (c *TelegramChannel) PreflightMedia(
	_ context.Context,
	msg bus.OutboundMediaMessage,
) error {
	store := c.GetMediaStore()
	if store == nil {
		return fmt.Errorf("telegram media preflight: no media store available")
	}
	for _, part := range msg.Parts {
		localPath, err := store.Resolve(part.Ref)
		if err != nil {
			return fmt.Errorf("telegram media preflight resolve %q: %w", part.Ref, err)
		}
		info, err := os.Stat(localPath)
		if err != nil {
			return fmt.Errorf("telegram media preflight stat %q: %w", part.Ref, err)
		}
		if info.Size() > telegramMediaUploadMaxBytes {
			return &channels.MediaConstraintError{
				Channel: "telegram",
				Ref:     part.Ref,
				Size:    info.Size(),
				MaxSize: telegramMediaUploadMaxBytes,
			}
		}
	}
	return nil
}

// SendMedia implements the channels.MediaSender compatibility interface.
func (c *TelegramChannel) SendMedia(
	ctx context.Context,
	msg bus.OutboundMediaMessage,
) ([]string, error) {
	result := c.sendMediaAttempt(ctx, msg)
	return result.MessageIDs, result.Err
}

func (c *TelegramChannel) sendMediaAttempt(
	ctx context.Context,
	msg bus.OutboundMediaMessage,
) channels.DeliveryResult[bus.OutboundMediaMessage] {
	if !c.IsRunning() {
		return telegramMediaFailure(nil, &msg, channels.ErrNotRunning)
	}
	useMarkdownV2 := c.tgCfg.UseMarkdownV2

	chatID, threadID, err := resolveTelegramOutboundTarget(msg.ChatID, &msg.Context)
	if err != nil {
		cause := fmt.Errorf("invalid chat ID %s: %w", msg.ChatID, channels.ErrSendFailed)
		return telegramMediaFailure(nil, &msg, cause)
	}

	store := c.GetMediaStore()
	if store == nil {
		cause := fmt.Errorf("no media store available: %w", channels.ErrSendFailed)
		return telegramMediaFailure(nil, &msg, cause)
	}

	var messageIDs []string
	leadingCaption := channels.FirstPartCaption(msg.Parts)
	if len([]rune(leadingCaption)) > telegramCaptionLimit {
		leadingResult := c.sendCaptionTextResult(ctx, chatID, threadID, leadingCaption)
		if !leadingResult.Delivered() {
			remainder := channels.ClearMediaCaptions(msg)
			if len(remainder.Parts) > 0 && len(leadingResult.Remaining) > 0 {
				remainder.Parts[0].Caption = strings.Join(leadingResult.Remaining, "\n")
			}
			return telegramMediaFailure(leadingResult.MessageIDs, &remainder, leadingResult.Err)
		}
		messageIDs = append(messageIDs, leadingResult.MessageIDs...)
		msg = channels.ClearMediaCaptions(msg)
	}

	if len(msg.Parts) > 1 && telegramCanSendMediaGroup(msg.Parts) {
		groupIDs, remainingParts, err := c.sendImageMediaGroups(ctx, chatID, threadID, store, msg.Parts)
		if err != nil {
			logger.ErrorCF("telegram", "Failed to send media group", map[string]any{
				"count": len(msg.Parts),
				"error": err.Error(),
			})
			messageIDs = append(messageIDs, groupIDs...)
			wrapped := wrapTelegramSendError("telegram send media group", err)
			remainder := msg
			remainder.Parts = append([]bus.MediaPart(nil), remainingParts...)
			return telegramMediaFailure(messageIDs, &remainder, wrapped)
		}
		if len(groupIDs) > 0 {
			messageIDs = append(messageIDs, groupIDs...)
			return channels.SuccessfulDelivery[bus.OutboundMediaMessage](messageIDs)
		}
	}

	for partIndex, part := range msg.Parts {
		localPath, err := store.Resolve(part.Ref)
		if err != nil {
			logger.ErrorCF("telegram", "Failed to resolve media ref", map[string]any{
				"ref":   part.Ref,
				"error": err.Error(),
			})
			cause := fmt.Errorf(
				"telegram resolve media ref %q: %w: %w",
				part.Ref, err, channels.ErrSendFailed,
			)
			return telegramMediaPartsFailure(messageIDs, msg, partIndex, cause)
		}

		file, err := os.Open(localPath)
		if err != nil {
			logger.ErrorCF("telegram", "Failed to open media file", map[string]any{
				"path":  localPath,
				"error": err.Error(),
			})
			cause := fmt.Errorf(
				"telegram open media file %q: %w: %w",
				localPath, err, channels.ErrSendFailed,
			)
			return telegramMediaPartsFailure(messageIDs, msg, partIndex, cause)
		}

		var tgResult *telego.Message
		switch part.Type {
		case "image":
			params := &telego.SendPhotoParams{
				ChatID:          tu.ID(chatID),
				MessageThreadID: threadID,
				Photo:           telego.InputFile{File: file},
			}
			telegramApplyCaptionParseMode(
				&params.Caption,
				&params.ParseMode,
				part.Caption,
				useMarkdownV2,
			)
			tgResult, err = c.bot.SendPhoto(ctx, params)
			if err != nil && telegramIsParseModeError(err) {
				if rewindErr := rewindTelegramUpload(file); rewindErr != nil {
					_ = file.Close()
					return telegramMediaRewindFailure(
						messageIDs, msg, partIndex, "caption parse failure", rewindErr,
					)
				}
				params.Caption = part.Caption
				params.ParseMode = ""
				tgResult, err = c.bot.SendPhoto(ctx, params)
			}
			if err != nil && strings.Contains(err.Error(), "PHOTO_INVALID_DIMENSIONS") {
				if rewindErr := rewindTelegramUpload(file); rewindErr != nil {
					_ = file.Close()
					return telegramMediaRewindFailure(
						messageIDs, msg, partIndex, "photo failure", rewindErr,
					)
				}

				docParams := &telego.SendDocumentParams{
					ChatID:          tu.ID(chatID),
					MessageThreadID: threadID,
					Document:        telego.InputFile{File: file},
				}
				telegramApplyCaptionParseMode(
					&docParams.Caption,
					&docParams.ParseMode,
					part.Caption,
					useMarkdownV2,
				)
				tgResult, err = c.bot.SendDocument(ctx, docParams)
				if err != nil && telegramIsParseModeError(err) {
					if rewindErr := rewindTelegramUpload(file); rewindErr != nil {
						_ = file.Close()
						return telegramMediaRewindFailure(
							messageIDs, msg, partIndex, "caption parse failure", rewindErr,
						)
					}
					docParams.Caption = part.Caption
					docParams.ParseMode = ""
					tgResult, err = c.bot.SendDocument(ctx, docParams)
				}
			}
		case "audio":
			// Send OGG files with "voice" in the filename as Telegram voice
			// bubbles (SendVoice) instead of audio attachments (SendAudio).
			fn := strings.ToLower(part.Filename)
			if strings.Contains(fn, "voice") &&
				(strings.HasSuffix(fn, ".ogg") || strings.HasSuffix(fn, ".oga")) {
				vparams := &telego.SendVoiceParams{
					ChatID:          tu.ID(chatID),
					MessageThreadID: threadID,
					Voice:           telego.InputFile{File: file},
				}
				telegramApplyCaptionParseMode(
					&vparams.Caption,
					&vparams.ParseMode,
					part.Caption,
					useMarkdownV2,
				)
				tgResult, err = c.bot.SendVoice(ctx, vparams)
				if err != nil && telegramIsParseModeError(err) {
					if rewindErr := rewindTelegramUpload(file); rewindErr != nil {
						_ = file.Close()
						return telegramMediaRewindFailure(
							messageIDs, msg, partIndex, "caption parse failure", rewindErr,
						)
					}
					vparams.Caption = part.Caption
					vparams.ParseMode = ""
					tgResult, err = c.bot.SendVoice(ctx, vparams)
				}
			} else {
				params := &telego.SendAudioParams{
					ChatID:          tu.ID(chatID),
					MessageThreadID: threadID,
					Audio:           telego.InputFile{File: file},
				}
				telegramApplyCaptionParseMode(
					&params.Caption,
					&params.ParseMode,
					part.Caption,
					useMarkdownV2,
				)
				tgResult, err = c.bot.SendAudio(ctx, params)
				if err != nil && telegramIsParseModeError(err) {
					if rewindErr := rewindTelegramUpload(file); rewindErr != nil {
						_ = file.Close()
						return telegramMediaRewindFailure(
							messageIDs, msg, partIndex, "caption parse failure", rewindErr,
						)
					}
					params.Caption = part.Caption
					params.ParseMode = ""
					tgResult, err = c.bot.SendAudio(ctx, params)
				}
			}
		case "video":
			params := &telego.SendVideoParams{
				ChatID:          tu.ID(chatID),
				MessageThreadID: threadID,
				Video:           telego.InputFile{File: file},
			}
			telegramApplyCaptionParseMode(
				&params.Caption,
				&params.ParseMode,
				part.Caption,
				useMarkdownV2,
			)
			tgResult, err = c.bot.SendVideo(ctx, params)
			if err != nil && telegramIsParseModeError(err) {
				if rewindErr := rewindTelegramUpload(file); rewindErr != nil {
					_ = file.Close()
					return telegramMediaRewindFailure(
						messageIDs, msg, partIndex, "caption parse failure", rewindErr,
					)
				}
				params.Caption = part.Caption
				params.ParseMode = ""
				tgResult, err = c.bot.SendVideo(ctx, params)
			}
		default: // "file" or unknown types
			params := &telego.SendDocumentParams{
				ChatID:          tu.ID(chatID),
				MessageThreadID: threadID,
				Document:        telego.InputFile{File: file},
			}
			telegramApplyCaptionParseMode(
				&params.Caption,
				&params.ParseMode,
				part.Caption,
				useMarkdownV2,
			)
			tgResult, err = c.bot.SendDocument(ctx, params)
			if err != nil && telegramIsParseModeError(err) {
				if rewindErr := rewindTelegramUpload(file); rewindErr != nil {
					_ = file.Close()
					return telegramMediaRewindFailure(
						messageIDs, msg, partIndex, "caption parse failure", rewindErr,
					)
				}
				params.Caption = part.Caption
				params.ParseMode = ""
				tgResult, err = c.bot.SendDocument(ctx, params)
			}
		}

		if tgResult != nil {
			messageIDs = append(messageIDs, strconv.Itoa(tgResult.MessageID))
		}
		_ = file.Close()

		if err != nil {
			logger.ErrorCF("telegram", "Failed to send media", map[string]any{
				"type":  part.Type,
				"error": err.Error(),
			})
			wrapped := wrapTelegramSendError("telegram send media", err)
			return telegramMediaPartsFailure(messageIDs, msg, partIndex, wrapped)
		}
	}

	return channels.SuccessfulDelivery[bus.OutboundMediaMessage](messageIDs)
}

func rewindTelegramUpload(file *os.File) error {
	if file == nil {
		return fmt.Errorf("file is nil")
	}
	_, err := file.Seek(0, io.SeekStart)
	return err
}

func telegramCanSendMediaGroup(parts []bus.MediaPart) bool {
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part.Type != "image" {
			return false
		}
	}
	return true
}

func (c *TelegramChannel) sendImageMediaGroups(
	ctx context.Context,
	chatID int64,
	threadID int,
	store media.MediaStore,
	parts []bus.MediaPart,
) ([]string, []bus.MediaPart, error) {
	const maxGroupSize = 10

	messageIDs := make([]string, 0, len(parts))
	for start := 0; start < len(parts); start += maxGroupSize {
		end := start + maxGroupSize
		if end > len(parts) {
			end = len(parts)
		}
		groupIDs, err := c.sendSingleImageMediaGroup(ctx, chatID, threadID, store, parts[start:end])
		if err != nil {
			return messageIDs, append([]bus.MediaPart(nil), parts[start:]...), err
		}
		messageIDs = append(messageIDs, groupIDs...)
	}
	return messageIDs, nil, nil
}

func (c *TelegramChannel) sendSingleImageMediaGroup(
	ctx context.Context,
	chatID int64,
	threadID int,
	store media.MediaStore,
	parts []bus.MediaPart,
) ([]string, error) {
	opened := make([]*os.File, 0, len(parts))
	defer func() {
		for _, file := range opened {
			_ = file.Close()
		}
	}()

	buildInputMedia := func(useParseMode bool) ([]telego.InputMedia, error) {
		inputMedia := make([]telego.InputMedia, 0, len(parts))
		for i, part := range parts {
			var file *os.File
			if len(opened) > i {
				file = opened[i]
				if _, err := file.Seek(0, io.SeekStart); err != nil {
					return nil, err
				}
			} else {
				localPath, err := store.Resolve(part.Ref)
				if err != nil {
					logger.ErrorCF("telegram", "Failed to resolve media ref for media group", map[string]any{
						"ref":   part.Ref,
						"error": err.Error(),
					})
					return nil, err
				}

				file, err = os.Open(localPath)
				if err != nil {
					logger.ErrorCF("telegram", "Failed to open media file for media group", map[string]any{
						"path":  localPath,
						"error": err.Error(),
					})
					return nil, err
				}
				opened = append(opened, file)
			}

			mediaItem := &telego.InputMediaPhoto{
				Type:  telego.MediaTypePhoto,
				Media: telego.InputFile{File: file},
			}
			if i == 0 {
				if useParseMode {
					telegramApplyCaptionParseMode(
						&mediaItem.Caption,
						&mediaItem.ParseMode,
						part.Caption,
						c.tgCfg.UseMarkdownV2,
					)
				} else {
					mediaItem.Caption = part.Caption
				}
			}
			inputMedia = append(inputMedia, mediaItem)
		}
		return inputMedia, nil
	}

	inputMedia, err := buildInputMedia(true)
	if err != nil {
		return nil, fmt.Errorf("telegram prepare media group: %w: %w", err, channels.ErrSendFailed)
	}

	results, err := c.bot.SendMediaGroup(ctx, &telego.SendMediaGroupParams{
		ChatID:          tu.ID(chatID),
		MessageThreadID: threadID,
		Media:           inputMedia,
	})
	if err != nil && telegramIsParseModeError(err) {
		inputMedia, rebuildErr := buildInputMedia(false)
		if rebuildErr != nil {
			return nil, fmt.Errorf(
				"telegram prepare media group fallback: %w: %w",
				rebuildErr,
				channels.ErrSendFailed,
			)
		}
		results, err = c.bot.SendMediaGroup(ctx, &telego.SendMediaGroupParams{
			ChatID:          tu.ID(chatID),
			MessageThreadID: threadID,
			Media:           inputMedia,
		})
	}
	if err != nil {
		return nil, err
	}

	messageIDs := make([]string, 0, len(results))
	for _, result := range results {
		messageIDs = append(messageIDs, strconv.Itoa(result.MessageID))
	}
	return messageIDs, nil
}

func telegramApplyCaptionParseMode(
	caption *string,
	parseMode *string,
	raw string,
	useMarkdownV2 bool,
) {
	if caption == nil || parseMode == nil {
		return
	}
	*caption = parseContent(raw, useMarkdownV2)
	if useMarkdownV2 {
		*parseMode = telego.ModeMarkdownV2
		return
	}
	*parseMode = telego.ModeHTML
}

func telegramIsParseModeError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "can't parse") ||
		strings.Contains(msg, "parse entities") ||
		strings.Contains(msg, "unsupported start tag") ||
		strings.Contains(msg, "unsupported end tag") ||
		strings.Contains(msg, "entity is not closed") {
		return true
	}

	if !strings.Contains(msg, "bad request") {
		return false
	}

	return strings.Contains(msg, "entity") ||
		strings.Contains(msg, "entities") ||
		strings.Contains(msg, "tag") ||
		strings.Contains(msg, "parse") ||
		strings.Contains(msg, "markup")
}

func (c *TelegramChannel) sendCaptionText(
	ctx context.Context,
	chatID int64,
	threadID int,
	text string,
) ([]string, error) {
	result := c.sendCaptionTextResult(ctx, chatID, threadID, text)
	return result.MessageIDs, result.Err
}

func (c *TelegramChannel) sendCaptionTextResult(
	ctx context.Context,
	chatID int64,
	threadID int,
	text string,
) channels.DeliveryResult[string] {
	text = strings.TrimSpace(text)
	if text == "" {
		return channels.SuccessfulDelivery[string](nil)
	}
	useMarkdownV2 := c.tgCfg.UseMarkdownV2
	return c.sendTextChunkQueue(ctx, []string{text}, sendChunkParams{
		chatID:        chatID,
		threadID:      threadID,
		useMarkdownV2: useMarkdownV2,
	}, c.richMessagesEnabled(useMarkdownV2), false)
}
