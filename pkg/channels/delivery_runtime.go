package channels

import (
	"context"
	"sync"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
)

type deliveryRuntimeHost interface {
	deliveryChannel(name string) (Channel, bool)
	deliveryTextSource() <-chan bus.OutboundMessage
	deliveryMediaSource() <-chan bus.OutboundMediaMessage
	deliverySplitOnMarker() bool
	decorateOutboundResponseFooter(msg bus.OutboundMessage) bus.OutboundMessage
	finalizedStreamActiveForMessage(channelName string, msg bus.OutboundMessage) bool
	beginDurableOutbound(deliveryID string) (bool, error)
	persistDurableOutbound(deliveryID string, outcome OutboundDeliveryOutcome) error
	persistDurableRejection(deliveryID string, cause error) error
	publishChannelEvent(
		kind runtimeevents.Kind,
		channel string,
		scope runtimeevents.Scope,
		severity runtimeevents.Severity,
		payload any,
	)
	publishOutboundSent(name string, msg bus.OutboundMessage, messageIDs []string)
	publishOutboundQueued(name string, msg bus.OutboundMessage)
	publishOutboundFailed(name string, msg bus.OutboundMessage, err error, retrying bool)
	publishOutboundMediaSent(name string, msg bus.OutboundMediaMessage, messageIDs []string)
	publishOutboundMediaQueued(name string, msg bus.OutboundMediaMessage)
	publishOutboundMediaFailed(name string, msg bus.OutboundMediaMessage, err error)
	beginOutboundToolFeedbackTerminals(
		channelName string,
		ch Channel,
		msg bus.OutboundMessage,
	) []*toolFeedbackTerminal
	beginToolFeedbackTerminals(
		channelName string,
		ch Channel,
		chatID string,
		outboundCtx *bus.InboundContext,
		sessionKey string,
		traceScopes []runtimeevents.TraceScope,
		transient bool,
	) []*toolFeedbackTerminal
	completeToolFeedbackTerminals(
		ctx context.Context,
		terminals []*toolFeedbackTerminal,
		success bool,
	)
	deliveryToolFeedbackEnabled() bool
	deliverToolFeedback(
		ctx context.Context,
		channelName string,
		ch Channel,
		msg bus.OutboundMessage,
		send func(context.Context, bus.OutboundMessage) ([]string, error),
	) ([]string, error)
	preSend(ctx context.Context, name string, msg bus.OutboundMessage, ch Channel) ([]string, bool)
	preSendMedia(ctx context.Context, name string, msg bus.OutboundMediaMessage, ch Channel)
}

// DeliveryRuntime owns outbound delivery registration and dispatcher lifetime.
// ChannelLifecycle serializes registrations with channel lifecycle transitions.
type DeliveryRuntime struct {
	mu             sync.RWMutex
	owners         map[string]*deliveryOwner
	dispatchCtx    context.Context
	dispatchCancel context.CancelFunc
	host           deliveryRuntimeHost
}

func (r *DeliveryRuntime) bindHost(host deliveryRuntimeHost) {
	r.host = host
}

func newDeliveryRuntime() *DeliveryRuntime {
	return &DeliveryRuntime{
		owners: make(map[string]*deliveryOwner),
	}
}

func (r *DeliveryRuntime) ensureInitialized() {
	if r.owners == nil {
		r.owners = make(map[string]*deliveryOwner)
	}
}

func (r *DeliveryRuntime) install(owner *deliveryOwner) {
	if owner == nil || owner.name == "" || owner.Worker() == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureInitialized()
	r.owners[owner.name] = owner
}

func (r *DeliveryRuntime) workerCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	count := 0
	for _, owner := range r.owners {
		if owner.active() {
			count++
		}
	}
	return count
}

func (r *DeliveryRuntime) hasActiveWorker(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	owner := r.owners[name]
	return owner.active()
}

func (r *DeliveryRuntime) snapshot() []*deliveryOwner {
	r.mu.RLock()
	defer r.mu.RUnlock()
	targets := make([]*deliveryOwner, 0, len(r.owners))
	for _, owner := range r.owners {
		targets = append(targets, owner)
	}
	return targets
}

func (r *DeliveryRuntime) owner(name string) *deliveryOwner {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.owners[name]
}

func (r *DeliveryRuntime) removeIfMatches(name string, owner *deliveryOwner) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if owner != nil && r.owners[name] == owner {
		delete(r.owners, name)
	}
}

func (r *DeliveryRuntime) startDispatcher(parent context.Context) context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dispatchCancel != nil {
		r.dispatchCancel()
	}
	dispatchCtx, cancel := context.WithCancel(parent)
	r.dispatchCtx = dispatchCtx
	r.dispatchCancel = cancel
	return dispatchCtx
}

func (r *DeliveryRuntime) ensureDispatcher(parent context.Context) (context.Context, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dispatchCtx != nil && r.dispatchCancel != nil {
		select {
		case <-r.dispatchCtx.Done():
			r.dispatchCancel()
		default:
			return r.dispatchCtx, false
		}
	}
	dispatchCtx, cancel := context.WithCancel(parent)
	r.dispatchCtx = dispatchCtx
	r.dispatchCancel = cancel
	return dispatchCtx, true
}

func (r *DeliveryRuntime) stopDispatcher() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dispatchCancel == nil {
		return
	}
	r.dispatchCancel()
	r.dispatchCtx = nil
	r.dispatchCancel = nil
}

func (r *DeliveryRuntime) dispatcherRunning() bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.dispatchCancel != nil
}
