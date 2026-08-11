package matrix

import (
	"context"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gomarkdown/markdown"
	mdhtml "github.com/gomarkdown/markdown/html"
	"github.com/gomarkdown/markdown/parser"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	_ "modernc.org/sqlite"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

func markdownToHTML(md string) string {
	extensions := (parser.CommonExtensions | parser.NoEmptyLineBeforeBlock) &^ parser.DefinitionLists
	p := parser.NewWithExtensions(extensions)
	renderer := mdhtml.NewRenderer(mdhtml.RendererOptions{Flags: mdhtml.UseXHTML})
	return strings.TrimSpace(string(markdown.ToHTML([]byte(md), p, renderer)))
}

func (c *MatrixChannel) Send(ctx context.Context, msg bus.OutboundMessage) ([]string, error) {
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}

	roomID := id.RoomID(strings.TrimSpace(msg.ChatID))
	if roomID == "" {
		return nil, fmt.Errorf("matrix room ID is empty: %w", channels.ErrSendFailed)
	}

	content := strings.TrimSpace(msg.Content)
	if content == "" {
		return nil, nil
	}

	resp, err := c.client.SendMessageEvent(ctx, roomID, event.EventMessage, c.messageContent(content))
	if err != nil {
		return nil, fmt.Errorf("matrix send: %w", channels.ErrTemporary)
	}
	return []string{resp.EventID.String()}, nil
}

func (c *MatrixChannel) messageContent(text string) *event.MessageEventContent {
	mc := &event.MessageEventContent{MsgType: event.MsgText, Body: text}
	if c.config.MessageFormat != "plain" {
		mc.Format = event.FormatHTML
		mc.FormattedBody = markdownToHTML(text)
	}
	return mc
}

// SendMedia implements channels.MediaSender.
func (c *MatrixChannel) SendMedia(ctx context.Context, msg bus.OutboundMediaMessage) ([]string, error) {
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}
	sendCtx := ctx
	if sendCtx == nil {
		sendCtx = context.Background()
	}

	roomID := id.RoomID(strings.TrimSpace(msg.ChatID))
	if roomID == "" {
		return nil, fmt.Errorf("matrix room ID is empty: %w", channels.ErrSendFailed)
	}

	store := c.GetMediaStore()
	if store == nil {
		return nil, fmt.Errorf("no media store available: %w", channels.ErrSendFailed)
	}

	var eventIDs []string
	for _, part := range msg.Parts {
		if err := sendCtx.Err(); err != nil {
			return nil, err
		}

		localPath, meta, err := store.ResolveWithMeta(part.Ref)
		if err != nil {
			logger.ErrorCF("matrix", "Failed to resolve media ref", map[string]any{
				"ref":   part.Ref,
				"error": err.Error(),
			})
			continue
		}

		fileInfo, err := os.Stat(localPath)
		if err != nil {
			logger.ErrorCF("matrix", "Failed to stat media file", map[string]any{
				"path":  localPath,
				"error": err.Error(),
			})
			continue
		}

		file, err := os.Open(localPath)
		if err != nil {
			logger.ErrorCF("matrix", "Failed to open media file", map[string]any{
				"path":  localPath,
				"error": err.Error(),
			})
			continue
		}

		filename := strings.TrimSpace(part.Filename)
		if filename == "" {
			filename = strings.TrimSpace(meta.Filename)
		}
		if filename == "" {
			filename = filepath.Base(localPath)
		}
		if filename == "" {
			filename = "file"
		}

		contentType := strings.TrimSpace(part.ContentType)
		if contentType == "" {
			contentType = strings.TrimSpace(meta.ContentType)
		}
		if contentType == "" {
			contentType = mime.TypeByExtension(strings.ToLower(filepath.Ext(filename)))
		}
		if contentType == "" {
			contentType = "application/octet-stream"
		}

		uploadResp, err := c.client.UploadMedia(sendCtx, mautrix.ReqUploadMedia{
			Content:       file,
			ContentLength: fileInfo.Size(),
			ContentType:   contentType,
			FileName:      filename,
		})
		_ = file.Close()
		if err != nil {
			logger.ErrorCF("matrix", "Failed to upload media", map[string]any{
				"path":  localPath,
				"type":  part.Type,
				"error": err.Error(),
			})
			return nil, fmt.Errorf("matrix upload media: %w", channels.ErrTemporary)
		}

		msgType := matrixOutboundMsgType(part.Type, filename, contentType)
		content := matrixOutboundContent(
			part.Caption,
			filename,
			msgType,
			contentType,
			fileInfo.Size(),
			uploadResp.ContentURI.CUString(),
		)

		sendResp, err := c.client.SendMessageEvent(sendCtx, roomID, event.EventMessage, content)
		if err != nil {
			logger.ErrorCF("matrix", "Failed to send media message", map[string]any{
				"room_id": roomID.String(),
				"type":    msgType,
				"error":   err.Error(),
			})
			return nil, fmt.Errorf("matrix send media: %w", channels.ErrTemporary)
		}
		if sendResp != nil {
			eventIDs = append(eventIDs, sendResp.EventID.String())
		}
	}

	return eventIDs, nil
}

// StartTyping implements channels.TypingCapable.
func (c *MatrixChannel) StartTyping(ctx context.Context, chatID string) (func(), error) {
	if !c.IsRunning() {
		return func() {}, nil
	}

	roomID := id.RoomID(strings.TrimSpace(chatID))
	if roomID == "" {
		return func() {}, fmt.Errorf("matrix room ID is empty")
	}

	session := newTypingSession()

	c.typingMu.Lock()
	if prev := c.typingSessions[chatID]; prev != nil {
		prev.stop()
	}
	c.typingSessions[chatID] = session
	c.typingMu.Unlock()

	parent := c.baseContext()
	go c.typingLoop(parent, roomID, session)

	var once sync.Once
	stop := func() {
		once.Do(func() {
			session.stop()
			c.typingMu.Lock()
			if current := c.typingSessions[chatID]; current == session {
				delete(c.typingSessions, chatID)
			}
			c.typingMu.Unlock()
			_, _ = c.client.UserTyping(context.Background(), roomID, false, 0)
		})
	}

	return stop, nil
}

// SendPlaceholder implements channels.PlaceholderCapable.
func (c *MatrixChannel) SendPlaceholder(ctx context.Context, chatID string) (string, error) {
	if !c.bc.Placeholder.Enabled {
		return "", nil
	}

	roomID := id.RoomID(strings.TrimSpace(chatID))
	if roomID == "" {
		return "", fmt.Errorf("matrix room ID is empty")
	}

	text := c.bc.Placeholder.GetRandomText()

	resp, err := c.client.SendMessageEvent(ctx, roomID, event.EventMessage, &event.MessageEventContent{
		MsgType: event.MsgNotice,
		Body:    text,
	})
	if err != nil {
		return "", err
	}

	return resp.EventID.String(), nil
}

// EditMessage implements channels.MessageEditor.
func (c *MatrixChannel) EditMessage(ctx context.Context, chatID string, messageID string, content string) error {
	roomID := id.RoomID(strings.TrimSpace(chatID))
	if roomID == "" {
		return fmt.Errorf("matrix room ID is empty")
	}
	if strings.TrimSpace(messageID) == "" {
		return fmt.Errorf("matrix message ID is empty")
	}

	editContent := c.messageContent(content)
	editContent.SetEdit(id.EventID(messageID))

	_, err := c.client.SendMessageEvent(ctx, roomID, event.EventMessage, editContent)
	return err
}

// DeleteMessage implements channels.MessageDeleter.
func (c *MatrixChannel) DeleteMessage(ctx context.Context, chatID string, messageID string) error {
	roomID := id.RoomID(strings.TrimSpace(chatID))
	if roomID == "" {
		return fmt.Errorf("matrix room ID is empty")
	}
	eventID := id.EventID(strings.TrimSpace(messageID))
	if eventID == "" {
		return fmt.Errorf("matrix message ID is empty")
	}

	_, err := c.client.RedactEvent(ctx, roomID, eventID)
	return err
}
