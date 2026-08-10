// MintClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package channels

import (
	"context"
	"errors"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
)

const (
	defaultChannelQueueSize = 16
	defaultRateLimit        = 10 // default 10 msg/s
	maxRetries              = 3
	rateLimitDelay          = 1 * time.Second
	baseBackoff             = 500 * time.Millisecond
	maxBackoff              = 8 * time.Second

	janitorInterval = 10 * time.Second
	typingStopTTL   = 5 * time.Minute
	placeholderTTL  = 10 * time.Minute

	streamAuxiliaryTombstoneTTL = 30 * time.Second
)

var errDeliveryClosed = errors.New("channel delivery is closed")

// channelRateConfig maps channel name to per-second rate limit.
var channelRateConfig = map[string]float64{
	"telegram": 20,
	"discord":  1,
	"slack":    1,
	"matrix":   2,
	"line":     10,
	"qq":       5,
	"irc":      2,
}

type channelWorker struct {
	ch         Channel
	queue      chan bus.OutboundMessage
	mediaQueue chan bus.OutboundMediaMessage
	done       chan struct{}
	mediaDone  chan struct{}
	limiter    *rate.Limiter
}

// deliveryOwner is the first narrow ownership boundary around outbound delivery.
// It intentionally owns only Channel+worker enqueue state today. A later
// channelSlot abstraction can wrap this with lifecycle/visibility state for
// safe reload swaps.
type deliveryOwner struct {
	name             string
	ch               Channel
	worker           *channelWorker
	mu               sync.Mutex
	closed           bool
	closedCh         chan struct{}
	closeDone        chan struct{}
	enqueueWG        sync.WaitGroup
	inflightEnqueues int
}

type Manager struct {
	bus            *bus.MessageBus
	runtimeEvents  runtimeevents.Bus
	lifecycle      *ChannelLifecycle
	delivery       *DeliveryRuntime
	stream         *StreamCoordinator
	outboundOutbox *outbox.Coordinator
}

type mediaStoreSetter interface {
	SetMediaStore(s media.MediaStore)
}

// ManagerOption configures a channel Manager.
type ManagerOption func(*Manager)

// WithRuntimeEvents injects the runtime event bus used for channel observations.
func WithRuntimeEvents(eventBus runtimeevents.Bus) ManagerOption {
	return func(m *Manager) {
		m.runtimeEvents = eventBus
	}
}

// WithOutboundOutbox enables durable channel outcomes for messages carrying a delivery ID.
func WithOutboundOutbox(coordinator *outbox.Coordinator) ManagerOption {
	return func(m *Manager) {
		m.outboundOutbox = coordinator
	}
}

// ChannelLifecyclePayload describes channel lifecycle runtime events.
type ChannelLifecyclePayload struct {
	Type  string `json:"type,omitempty"`
	Error string `json:"error,omitempty"`
}

// ChannelOutboundPayload describes channel outbound message runtime events.
type ChannelOutboundPayload struct {
	DeliveryID       string                     `json:"delivery_id,omitempty"`
	TraceScopes      []runtimeevents.TraceScope `json:"trace_scopes,omitempty"`
	TraceSettlement  bool                       `json:"trace_settlement,omitempty"`
	Media            bool                       `json:"media,omitempty"`
	ContentLen       int                        `json:"content_len,omitempty"`
	MessageIDs       []string                   `json:"message_ids,omitempty"`
	ReplyToMessageID string                     `json:"reply_to_message_id,omitempty"`
	Error            string                     `json:"error,omitempty"`
	Retries          int                        `json:"retries,omitempty"`
}

type outcomePublication uint8

const (
	publishNoOutcome outcomePublication = iota
	publishDefinitiveOutcome
	publishSuccessOnly
)

func (mode outcomePublication) success() bool {
	return mode == publishDefinitiveOutcome || mode == publishSuccessOnly
}

func (mode outcomePublication) failure(ambiguous bool) bool {
	return mode == publishDefinitiveOutcome || (mode == publishSuccessOnly && ambiguous)
}

type outboundTargetResolver interface {
	ResolveOutboundChatID(chatID string, outboundCtx *bus.InboundContext) string
}

type toolFeedbackMessageTargetResolver interface {
	ToolFeedbackMessageChatID(chatID string, outboundCtx *bus.InboundContext) string
}

type toolFeedbackMessageContentPreparer interface {
	PrepareToolFeedbackMessageContent(content string) string
}

type toolFeedbackMessageEditor interface {
	EditToolFeedbackMessage(ctx context.Context, chatID, messageID, content string) error
}

type toolFeedbackMessageSender interface {
	SendToolFeedbackMessage(ctx context.Context, msg bus.OutboundMessage) ([]string, bool, error)
}

type deliveryCleanupOptions struct {
	StopTyping          bool
	UndoReaction        bool
	ClearStreamActive   bool
	DismissToolFeedback bool
	DeletePlaceholder   bool
	SessionKey          string
	TraceScopes         []runtimeevents.TraceScope
}
