package channels

import (
	"context"
	"fmt"

	"github.com/bogdanovich/mintclaw/pkg/bus"
)

// MediaSender is an optional interface for channels that can send
// media attachments (images, files, audio, video).
// Manager discovers channels implementing this interface via type
// assertion and routes OutboundMediaMessage to them.
type MediaSender interface {
	SendMedia(ctx context.Context, msg bus.OutboundMediaMessage) ([]string, error)
}

// MediaDeliverySender preserves typed transport metadata that the legacy
// MediaSender signature cannot represent.
type MediaDeliverySender interface {
	SendMediaResult(ctx context.Context, pending []bus.OutboundMediaMessage) DeliveryResult[bus.OutboundMediaMessage]
}

// MediaPreflighter validates channel-specific constraints before a durable
// outbound intent is admitted. Implementations must not perform remote writes.
type MediaPreflighter interface {
	PreflightMedia(ctx context.Context, msg bus.OutboundMediaMessage) error
}

// MediaConstraintError reports a deterministic channel policy rejection that
// callers can act on before attempting delivery.
type MediaConstraintError struct {
	Channel string
	Ref     string
	Size    int64
	MaxSize int64
}

func (e *MediaConstraintError) Error() string {
	return fmt.Sprintf(
		"%s media %q is %d bytes, exceeding the channel upload limit of %d bytes; reduce or transcode the file before sending",
		e.Channel,
		e.Ref,
		e.Size,
		e.MaxSize,
	)
}
