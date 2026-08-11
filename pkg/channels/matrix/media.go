package matrix

import (
	"context"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	_ "modernc.org/sqlite"

	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/media"
)

func (c *MatrixChannel) extractInboundContent(
	ctx context.Context,
	msgEvt *event.MessageEventContent,
	scope string,
) (string, []string, bool) {
	switch msgEvt.MsgType {
	case event.MsgText, event.MsgNotice:
		return msgEvt.Body, nil, true
	case event.MsgImage, event.MsgAudio, event.MsgVideo, event.MsgFile:
		return c.extractInboundMedia(ctx, msgEvt, scope)
	default:
		logger.DebugCF("matrix", "Ignoring unsupported matrix msgtype", map[string]any{
			"msgtype": msgEvt.MsgType,
		})
		return "", nil, false
	}
}

func (c *MatrixChannel) extractInboundMedia(
	ctx context.Context,
	msgEvt *event.MessageEventContent,
	scope string,
) (string, []string, bool) {
	mediaKind := matrixMediaKind(msgEvt.MsgType)
	label := matrixMediaLabel(msgEvt, mediaKind)
	content := fmt.Sprintf("[%s: %s]", mediaKind, label)
	if caption := strings.TrimSpace(msgEvt.GetCaption()); caption != "" {
		content = caption + "\n" + content
	}

	localPath, err := c.downloadMedia(ctx, msgEvt, mediaKind)
	if err != nil {
		logger.WarnCF("matrix", "Failed to download media; forwarding as text-only marker", map[string]any{
			"msgtype": msgEvt.MsgType,
			"error":   err.Error(),
		})
		return content, nil, true
	}

	filename := matrixMediaFilename(label, mediaKind, matrixContentType(msgEvt))
	ref := c.storeMedia(localPath, media.MediaMeta{
		Filename:    filename,
		ContentType: matrixContentType(msgEvt),
		Source:      "matrix",
	}, scope)
	return content, []string{ref}, true
}

func (c *MatrixChannel) storeMedia(localPath string, meta media.MediaMeta, scope string) string {
	if store := c.GetMediaStore(); store != nil {
		if meta.CleanupPolicy == "" {
			meta.CleanupPolicy = media.CleanupPolicyDeleteOnCleanup
		}
		ref, err := store.Store(localPath, meta, scope)
		if err == nil {
			return ref
		}
		logger.WarnCF("matrix", "Failed to store media in MediaStore, falling back to local path", map[string]any{
			"path":  localPath,
			"error": err.Error(),
		})
	}
	return localPath
}

func (c *MatrixChannel) downloadMedia(
	ctx context.Context,
	msgEvt *event.MessageEventContent,
	mediaKind string,
) (string, error) {
	uri := matrixMediaURI(msgEvt)
	if uri == "" {
		return "", fmt.Errorf("empty matrix media URL")
	}
	parsed := uri.ParseOrIgnore()
	if parsed.IsEmpty() {
		return "", fmt.Errorf("invalid matrix media URL: %s", uri)
	}

	dlCtx := c.baseContext()
	if ctx != nil {
		dlCtx = ctx
	}
	reqCtx, cancel := context.WithTimeout(dlCtx, 20*time.Second)
	defer cancel()

	resp, err := c.client.Download(reqCtx, parsed)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	reader := resp.Body
	readerClose := func() error { return nil }

	// Encrypted attachments put URL in msgEvt.File and require client-side decryption.
	if msgEvt != nil && msgEvt.File != nil && msgEvt.URL == "" {
		if err = msgEvt.File.PrepareForDecryption(); err != nil {
			return "", fmt.Errorf("decrypt matrix media: %w", err)
		}
		decryptReader := msgEvt.File.DecryptStream(resp.Body)
		reader = decryptReader
		readerClose = decryptReader.Close
	}

	label := matrixMediaLabel(msgEvt, mediaKind)
	ext := matrixMediaExt(label, matrixContentType(msgEvt), mediaKind)
	mediaDir, err := matrixMediaTempDir()
	if err != nil {
		return "", fmt.Errorf("create matrix media directory: %w", err)
	}
	tmp, err := os.CreateTemp(mediaDir, "matrix-media-*"+ext)
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		_ = tmp.Close()
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	_, err = io.Copy(tmp, reader)
	if err != nil {
		return "", err
	}
	if err = readerClose(); err != nil {
		return "", fmt.Errorf("decrypt matrix media: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return "", err
	}

	cleanup = false
	return tmpPath, nil
}

func matrixContentType(msgEvt *event.MessageEventContent) string {
	if msgEvt != nil && msgEvt.Info != nil {
		return strings.TrimSpace(msgEvt.Info.MimeType)
	}
	return ""
}

func matrixMediaURI(msgEvt *event.MessageEventContent) id.ContentURIString {
	if msgEvt == nil {
		return ""
	}
	if msgEvt.URL != "" {
		return msgEvt.URL
	}
	if msgEvt.File != nil {
		return msgEvt.File.URL
	}
	return ""
}

func matrixMediaKind(msgType event.MessageType) string {
	switch msgType {
	case event.MsgAudio:
		return "audio"
	case event.MsgVideo:
		return "video"
	case event.MsgFile:
		return "file"
	default:
		return "image"
	}
}

func matrixOutboundMsgType(partType, filename, contentType string) event.MessageType {
	switch strings.ToLower(strings.TrimSpace(partType)) {
	case "image":
		return event.MsgImage
	case "audio", "voice":
		return event.MsgAudio
	case "video":
		return event.MsgVideo
	case "file", "document":
		return event.MsgFile
	}

	ct := strings.ToLower(strings.TrimSpace(contentType))
	switch {
	case strings.HasPrefix(ct, "image/"):
		return event.MsgImage
	case strings.HasPrefix(ct, "audio/"), ct == "application/ogg", ct == "application/x-ogg":
		return event.MsgAudio
	case strings.HasPrefix(ct, "video/"):
		return event.MsgVideo
	}

	switch strings.ToLower(strings.TrimSpace(filepath.Ext(filename))) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".svg":
		return event.MsgImage
	case ".mp3", ".wav", ".ogg", ".m4a", ".flac", ".aac", ".wma", ".opus":
		return event.MsgAudio
	case ".mp4", ".avi", ".mov", ".webm", ".mkv":
		return event.MsgVideo
	default:
		return event.MsgFile
	}
}

func matrixOutboundContent(
	caption, filename string,
	msgType event.MessageType,
	contentType string,
	size int64,
	uri id.ContentURIString,
) *event.MessageEventContent {
	body := strings.TrimSpace(caption)
	if body == "" {
		body = filename
	}
	if body == "" {
		body = matrixMediaKind(msgType)
	}

	info := &event.FileInfo{MimeType: strings.TrimSpace(contentType)}
	if size > 0 && size <= int64(int(^uint(0)>>1)) {
		info.Size = int(size)
	}

	content := &event.MessageEventContent{
		MsgType:  msgType,
		Body:     body,
		URL:      uri,
		FileName: filename,
		Info:     info,
	}
	return content
}

func matrixMediaLabel(msgEvt *event.MessageEventContent, fallback string) string {
	if msgEvt == nil {
		return fallback
	}
	if v := strings.TrimSpace(msgEvt.FileName); v != "" {
		return v
	}
	if v := strings.TrimSpace(msgEvt.Body); v != "" {
		return v
	}
	return fallback
}

func matrixMediaFilename(label, mediaKind, contentType string) string {
	filename := strings.TrimSpace(label)
	if filename == "" {
		filename = mediaKind
	}
	if filepath.Ext(filename) == "" {
		filename += matrixMediaExt("", contentType, mediaKind)
	}
	return filename
}

func matrixMediaExt(filename, contentType, mediaKind string) string {
	if ext := strings.TrimSpace(filepath.Ext(filename)); ext != "" {
		return ext
	}
	if contentType != "" {
		if exts, err := mime.ExtensionsByType(contentType); err == nil && len(exts) > 0 {
			return exts[0]
		}
	}
	switch mediaKind {
	case "audio":
		return ".ogg"
	case "video":
		return ".mp4"
	case "file":
		return ".bin"
	default:
		return ".jpg"
	}
}
