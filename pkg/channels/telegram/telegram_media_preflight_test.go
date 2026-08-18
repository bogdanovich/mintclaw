package telegram

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/media"
)

func TestTelegramMediaPreflightAcceptsFileAtLimit(t *testing.T) {
	channel, msg := telegramPreflightFixture(t, telegramMediaUploadMaxBytes)
	if err := channel.PreflightMedia(context.Background(), msg); err != nil {
		t.Fatalf("PreflightMedia() error = %v", err)
	}
}

func TestTelegramMediaPreflightRejectsOversizedFile(t *testing.T) {
	channel, msg := telegramPreflightFixture(t, 132_186_801)
	err := channel.PreflightMedia(context.Background(), msg)
	var constraint *channels.MediaConstraintError
	if !errors.As(err, &constraint) {
		t.Fatalf("PreflightMedia() error = %v, want MediaConstraintError", err)
	}
	if constraint.Size != 132_186_801 || constraint.MaxSize != telegramMediaUploadMaxBytes {
		t.Fatalf("constraint = %+v", constraint)
	}
}

func telegramPreflightFixture(
	t *testing.T,
	size int64,
) (*TelegramChannel, bus.OutboundMediaMessage) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "video.mp4")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	if err = file.Truncate(size); err != nil {
		_ = file.Close()
		t.Fatalf("truncate fixture: %v", err)
	}
	if err = file.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}
	store := media.NewFileMediaStore()
	ref, err := store.Store(
		path,
		media.MediaMeta{Filename: "video.mp4", ContentType: "video/mp4"},
		"preflight-test",
	)
	if err != nil {
		t.Fatalf("store fixture: %v", err)
	}
	channel := &TelegramChannel{
		BaseChannel: channels.NewBaseChannel("telegram", nil, nil, nil),
	}
	channel.SetMediaStore(store)
	return channel, bus.OutboundMediaMessage{
		Channel: "telegram",
		ChatID:  "123",
		Parts: []bus.MediaPart{{
			Type:     "video",
			Ref:      ref,
			Filename: "video.mp4",
		}},
	}
}
