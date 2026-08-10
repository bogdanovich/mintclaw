package telegram

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/identity"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/utils"
)

func (c *TelegramChannel) handleMessage(ctx context.Context, message *telego.Message) error {
	if message != nil && strings.TrimSpace(message.MediaGroupID) != "" {
		return c.bufferMediaGroupMessage(ctx, message)
	}
	return c.handleMessages(ctx, []*telego.Message{message})
}

func (c *TelegramChannel) bufferMediaGroupMessage(
	ctx context.Context,
	message *telego.Message,
) error {
	if message == nil {
		return fmt.Errorf("message is nil")
	}
	groupID := strings.TrimSpace(message.MediaGroupID)
	if groupID == "" {
		return c.handleMessages(ctx, []*telego.Message{message})
	}

	msgCopy := *message
	msgCopy.Photo = append([]telego.PhotoSize(nil), message.Photo...)
	key := fmt.Sprintf("%d:%s", message.Chat.ID, groupID)

	c.mediaGroupMu.Lock()
	if c.mediaGroups == nil {
		c.mediaGroups = make(map[string]*telegramMediaGroup)
	}
	group := c.mediaGroups[key]
	if group == nil {
		group = &telegramMediaGroup{}
		c.mediaGroups[key] = group
	}
	group.messages = append(group.messages, &msgCopy)
	group.generation++
	generation := group.generation
	if group.timer != nil {
		group.timer.Stop()
	}
	delay := c.mediaGroupDelay
	if delay <= 0 {
		delay = defaultMediaGroupDelay
	}
	group.timer = time.AfterFunc(delay, func() {
		c.flushMediaGroup(c.ctx, key, generation)
	})
	c.mediaGroupMu.Unlock()

	logger.DebugCF("telegram", "Buffered media group message", map[string]any{
		"chat_id":        message.Chat.ID,
		"media_group_id": groupID,
		"message_id":     message.MessageID,
	})
	return nil
}

func (c *TelegramChannel) flushPendingMediaGroups(ctx context.Context) {
	c.mediaGroupMu.Lock()
	keys := make([]string, 0, len(c.mediaGroups))
	for key, group := range c.mediaGroups {
		if group.timer != nil {
			group.timer.Stop()
		}
		keys = append(keys, key)
	}
	c.mediaGroupMu.Unlock()

	for _, key := range keys {
		c.flushMediaGroup(ctx, key, 0)
	}
}

func (c *TelegramChannel) flushMediaGroup(ctx context.Context, key string, generation uint64) {
	c.mediaGroupMu.Lock()
	group := c.mediaGroups[key]
	if group == nil {
		c.mediaGroupMu.Unlock()
		return
	}
	if generation != 0 && group.generation != generation {
		c.mediaGroupMu.Unlock()
		return
	}
	delete(c.mediaGroups, key)
	if group.timer != nil {
		group.timer.Stop()
	}
	messages := append([]*telego.Message(nil), group.messages...)
	c.mediaGroupMu.Unlock()

	if len(messages) == 0 {
		return
	}
	slices.SortFunc(messages, func(a, b *telego.Message) int {
		switch {
		case a == nil && b == nil:
			return 0
		case a == nil:
			return -1
		case b == nil:
			return 1
		default:
			return a.MessageID - b.MessageID
		}
	})
	if ctx == nil {
		ctx = context.Background()
	}
	if err := c.handleMessages(ctx, messages); err != nil {
		logger.ErrorCF("telegram", "Failed to handle media group", map[string]any{
			"key":   key,
			"error": err.Error(),
		})
	}
}

func (c *TelegramChannel) handleMessages(ctx context.Context, messages []*telego.Message) error {
	if len(messages) == 0 {
		return nil
	}
	message := messages[0]
	for _, candidate := range messages {
		if candidate == nil {
			continue
		}
		if strings.TrimSpace(candidate.Text) != "" || strings.TrimSpace(candidate.Caption) != "" {
			message = candidate
			break
		}
	}
	if message == nil {
		return fmt.Errorf("message is nil")
	}

	user := message.From
	if user == nil {
		return fmt.Errorf("message sender (user) is nil")
	}

	platformID := fmt.Sprintf("%d", user.ID)
	sender := bus.SenderInfo{
		Platform:    "telegram",
		PlatformID:  platformID,
		CanonicalID: identity.BuildCanonicalID("telegram", platformID),
		Username:    user.Username,
		DisplayName: user.FirstName,
	}

	// check allowlist to avoid downloading attachments for rejected users
	if !c.IsAllowedSender(sender) {
		logger.DebugCF("telegram", "Message rejected by allowlist", map[string]any{
			"user_id": platformID,
		})
		return nil
	}

	chatID := message.Chat.ID
	threadID := message.MessageThreadID
	if message.Chat.IsForum && threadID != 0 && !c.topicAllowed(threadID) {
		logger.DebugCF("telegram", "Message ignored by topic filter", map[string]any{
			"chat_id":    chatID,
			"thread_id":  threadID,
			"message_id": message.MessageID,
		})
		return nil
	}
	c.chatIDsMu.Lock()
	c.chatIDs[platformID] = chatID
	c.chatIDsMu.Unlock()

	content := ""
	mediaPaths := []string{}

	chatIDStr := fmt.Sprintf("%d", chatID)
	messageIDStr := fmt.Sprintf("%d", message.MessageID)
	scope := channels.BuildMediaScope("telegram", chatIDStr, messageIDStr)

	// Helper to register a local file with the media store
	storeMedia := func(localPath, filename string) string {
		if store := c.GetMediaStore(); store != nil {
			ref, err := store.Store(localPath, media.MediaMeta{
				Filename:      filename,
				Source:        "telegram",
				CleanupPolicy: media.CleanupPolicyDeleteOnCleanup,
			}, scope)
			if err == nil {
				return ref
			}
		}
		return localPath // fallback: use raw path
	}

	for i, msg := range messages {
		if msg == nil {
			continue
		}
		parts := c.collectTelegramMessageParts(ctx, msg, i, len(messages), storeMedia)
		for _, part := range parts.content {
			if content != "" {
				content += "\n"
			}
			content += part
		}
		mediaPaths = append(mediaPaths, parts.mediaPaths...)
	}

	if content == "" && len(mediaPaths) == 0 {
		return nil
	}

	if content == "" {
		content = "[media only]"
	}
	mediaGroupMetadata := telegramMediaGroupMetadata(messages)
	interactionChoice := c.telegramInteractionChoice(message)
	interactionResponse := c.telegramInteractionResponse(message, content, platformID, interactionChoice)
	if interactionResponse == "" {
		interactionResponse = c.telegramQuestionControlResponse(message, platformID)
	}

	// In group chats, apply unified group trigger filtering
	isMentioned := false
	if message.Chat.Type != "private" {
		isMentioned = c.isBotMentioned(message)
		topicID := ""
		if message.Chat.IsForum && message.MessageThreadID != 0 {
			topicID = fmt.Sprintf("%d", message.MessageThreadID)
		}
		if !isMentioned && c.IgnoreNonBotMentionsForTopic(topicID, true) &&
			c.hasNonBotMention(message) {
			c.observeSuppressedTelegramMessage(
				ctx,
				message,
				chatID,
				content,
				mediaPaths,
				sender,
				isMentioned,
				"mentions another user/bot without mentioning this bot",
				mediaGroupMetadata,
			)
			logger.DebugCF(
				"telegram",
				"Message ignored because it mentions another user/bot but not this bot",
				map[string]any{
					"chat_id":    chatIDStr,
					"message_id": messageIDStr,
				},
			)
			return nil
		}
		if !isMentioned && c.IgnoreNonBotRepliesForTopic(topicID, false) &&
			c.isReplyToNonBotMessage(message) {
			observedContent := content
			if message.ReplyToMessage != nil {
				observedContent = c.prependTelegramQuotedReply(
					observedContent,
					message.ReplyToMessage,
				)
			}
			c.observeSuppressedTelegramMessage(
				ctx,
				message,
				chatID,
				observedContent,
				mediaPaths,
				sender,
				isMentioned,
				"reply to a non-bot message without mentioning this bot",
				mediaGroupMetadata,
			)
			logger.DebugCF(
				"telegram",
				"Message ignored because it replies to a non-bot message without mentioning this bot",
				map[string]any{
					"chat_id":    chatIDStr,
					"message_id": messageIDStr,
				},
			)
			return nil
		}
		if isMentioned {
			content = c.stripBotMention(message, content)
		}
		directedToBot := isMentioned || interactionChoice != "" || interactionResponse != ""
		respond, cleaned := c.ShouldRespondInGroupForTopic(directedToBot, content, topicID)
		if !respond {
			return nil
		}
		content = cleaned
	}

	if message.ReplyToMessage != nil {
		quotedMedia := quotedTelegramMediaRefs(
			message.ReplyToMessage,
			func(fileID, ext, filename string) string {
				localPath := c.downloadFile(ctx, fileID, ext)
				if localPath == "" {
					return ""
				}
				return storeMedia(localPath, filename)
			},
		)
		if len(quotedMedia) > 0 {
			mediaPaths = append(quotedMedia, mediaPaths...)
		}
		content = c.prependTelegramQuotedReply(content, message.ReplyToMessage)
	}

	// For forum topics, embed the thread ID as "chatID/threadID" so replies
	// route to the correct topic and each topic gets its own session.
	// Only forum groups (IsForum) are handled; regular group reply threads
	// must share one session per group.
	compositeChatID := fmt.Sprintf("%d", chatID)
	if message.Chat.IsForum && threadID != 0 {
		compositeChatID = fmt.Sprintf("%d/%d", chatID, threadID)
	}

	logger.DebugCF("telegram", "Received message", map[string]any{
		"sender_id": sender.CanonicalID,
		"chat_id":   compositeChatID,
		"thread_id": threadID,
		"preview":   utils.Truncate(content, 50),
	})

	peerKind := "direct"
	if message.Chat.Type != "private" {
		peerKind = "group"
	}
	messageID := fmt.Sprintf("%d", message.MessageID)

	metadata := map[string]string{
		"user_id":    fmt.Sprintf("%d", user.ID),
		"username":   user.Username,
		"first_name": user.FirstName,
		"is_group":   fmt.Sprintf("%t", message.Chat.Type != "private"),
	}
	if interactionChoice != "" {
		metadata[bus.InboundMetadataKeyInteractionChoice] = interactionChoice
	}
	if interactionResponse != "" {
		metadata[bus.InboundMetadataKeyInteractionResponse] = interactionResponse
	}
	mergeTelegramRawMetadata(metadata, mediaGroupMetadata)

	inboundCtx := bus.InboundContext{
		Channel:   c.Name(),
		ChatID:    compositeChatID,
		ChatType:  peerKind,
		SenderID:  platformID,
		MessageID: messageID,
		Mentioned: isMentioned,
		Raw:       metadata,
	}
	if message.Chat.IsForum && threadID != 0 {
		inboundCtx.TopicID = fmt.Sprintf("%d", threadID)
	}
	if message.ReplyToMessage != nil {
		inboundCtx.ReplyToMessageID = fmt.Sprintf("%d", message.ReplyToMessage.MessageID)
	}

	_ = c.HandleMessageWithContext(
		c.ctx,
		compositeChatID,
		content,
		mediaPaths,
		inboundCtx,
		sender,
	)
	return nil
}

func telegramMediaGroupMetadata(messages []*telego.Message) map[string]string {
	messageIDs := make([]string, 0, len(messages))
	mediaGroupID := ""
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		if mediaGroupID == "" {
			mediaGroupID = strings.TrimSpace(msg.MediaGroupID)
		}
		messageIDs = append(messageIDs, strconv.Itoa(msg.MessageID))
	}
	if mediaGroupID == "" {
		return nil
	}
	return map[string]string{
		"media_group_id":          mediaGroupID,
		"media_group_count":       strconv.Itoa(len(messageIDs)),
		"media_group_message_ids": strings.Join(messageIDs, ","),
	}
}

func mergeTelegramRawMetadata(dst, src map[string]string) {
	if dst == nil {
		return
	}
	for key, value := range src {
		dst[key] = value
	}
}

func (c *TelegramChannel) observeSuppressedTelegramMessage(
	ctx context.Context,
	message *telego.Message,
	chatID int64,
	content string,
	mediaPaths []string,
	sender bus.SenderInfo,
	isMentioned bool,
	reason string,
	extraRaw map[string]string,
) {
	if message == nil || message.From == nil {
		return
	}
	threadID := message.MessageThreadID
	compositeChatID := fmt.Sprintf("%d", chatID)
	if message.Chat.IsForum && threadID != 0 {
		compositeChatID = fmt.Sprintf("%d/%d", chatID, threadID)
	}
	peerKind := "direct"
	if message.Chat.Type != "private" {
		peerKind = "group"
	}
	metadata := map[string]string{
		"user_id":    fmt.Sprintf("%d", message.From.ID),
		"username":   message.From.Username,
		"first_name": message.From.FirstName,
		"is_group":   fmt.Sprintf("%t", message.Chat.Type != "private"),
	}
	mergeTelegramRawMetadata(metadata, extraRaw)
	inboundCtx := bus.InboundContext{
		Channel:   c.Name(),
		ChatID:    fmt.Sprintf("%d", chatID),
		ChatType:  peerKind,
		SenderID:  fmt.Sprintf("%d", message.From.ID),
		MessageID: fmt.Sprintf("%d", message.MessageID),
		Mentioned: isMentioned,
		Raw:       metadata,
	}
	if message.Chat.IsForum && threadID != 0 {
		inboundCtx.TopicID = fmt.Sprintf("%d", threadID)
	}
	if message.ReplyToMessage != nil {
		inboundCtx.ReplyToMessageID = fmt.Sprintf("%d", message.ReplyToMessage.MessageID)
	}
	c.ObserveMessageWithContext(
		ctx,
		compositeChatID,
		content,
		mediaPaths,
		inboundCtx,
		reason,
		sender,
	)
}

func (c *TelegramChannel) collectTelegramMessageParts(
	ctx context.Context,
	msg *telego.Message,
	index int,
	total int,
	storeMedia func(localPath, filename string) string,
) telegramMessageParts {
	parts := telegramMessageParts{}
	if msg == nil {
		return parts
	}
	if text := strings.TrimSpace(msg.Text); text != "" {
		parts.content = append(parts.content, text)
	}
	if caption := strings.TrimSpace(msg.Caption); caption != "" {
		parts.content = append(parts.content, caption)
	}
	if msg.Location != nil {
		parts.content = append(parts.content, fmt.Sprintf(
			"[User location: lat=%.6f, lng=%.6f]",
			msg.Location.Latitude,
			msg.Location.Longitude,
		))
	}
	if len(msg.Photo) > 0 {
		photo := msg.Photo[len(msg.Photo)-1]
		photoPath := c.downloadPhoto(ctx, photo.FileID)
		if photoPath != "" {
			photoNumber := index + 1
			parts.mediaPaths = append(
				parts.mediaPaths,
				storeMedia(photoPath, fmt.Sprintf("photo-%d.jpg", photoNumber)),
			)
			parts.content = append(parts.content, fmt.Sprintf("[image: photo %d]", photoNumber))
		}
	}
	if msg.Voice != nil {
		voicePath := c.downloadFile(ctx, msg.Voice.FileID, ".ogg")
		if voicePath != "" {
			parts.mediaPaths = append(
				parts.mediaPaths,
				storeMedia(voicePath, indexedMediaFilename("voice", ".ogg", index, total)),
			)
			parts.content = append(parts.content, "[voice]")
		}
	}
	if msg.Audio != nil {
		audioPath := c.downloadFile(ctx, msg.Audio.FileID, ".mp3")
		if audioPath != "" {
			filename := msg.Audio.FileName
			if strings.TrimSpace(filename) == "" {
				filename = indexedMediaFilename("audio", ".mp3", index, total)
			}
			parts.mediaPaths = append(parts.mediaPaths, storeMedia(audioPath, filename))
			parts.content = append(parts.content, "[audio]")
		}
	}
	if msg.Document != nil {
		docPath := c.downloadFile(ctx, msg.Document.FileID, "")
		if docPath != "" {
			filename := msg.Document.FileName
			if strings.TrimSpace(filename) == "" {
				filename = indexedMediaFilename("document", "", index, total)
			}
			parts.mediaPaths = append(parts.mediaPaths, storeMedia(docPath, filename))
			parts.content = append(parts.content, "[file]")
		}
	}
	return parts
}

func indexedMediaFilename(prefix, ext string, index int, total int) string {
	if total <= 1 {
		return prefix + ext
	}
	return fmt.Sprintf("%s-%d%s", prefix, index+1, ext)
}

func (c *TelegramChannel) prependTelegramQuotedReply(content string, reply *telego.Message) string {
	quoted := strings.TrimSpace(telegramQuotedContent(reply))
	if quoted == "" {
		return content
	}

	author := telegramQuotedAuthor(reply)
	role := c.telegramQuotedRole(reply)
	if strings.TrimSpace(content) == "" {
		return fmt.Sprintf("[quoted %s message from %s]: %s", role, author, quoted)
	}
	return fmt.Sprintf("[quoted %s message from %s]: %s\n\n%s", role, author, quoted, content)
}

func (c *TelegramChannel) telegramQuotedRole(message *telego.Message) string {
	if message == nil {
		return "unknown"
	}

	if message.From != nil {
		if !message.From.IsBot {
			return "user"
		}
		if c.isOwnBotUser(message.From) {
			return "assistant"
		}
		return "bot"
	}

	if message.SenderChat != nil {
		return "chat"
	}

	return "unknown"
}
