package channels

import (
	"context"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/media"
)

func NewManager(
	cfg *config.Config,
	messageBus *bus.MessageBus,
	store media.MediaStore,
	opts ...ManagerOption,
) (*Manager, error) {
	m := &Manager{
		bus:       messageBus,
		lifecycle: newChannelLifecycle(cfg, store),
		delivery:  newDeliveryRuntime(),
		stream:    newStreamCoordinator(),
	}
	m.delivery.bindHost(m)
	if cfg != nil {
		m.streamCoordinator().initializeToolFeedback(
			ToolFeedbackAnimatorConfig{
				AnimationInterval: cfg.Agents.Defaults.GetToolFeedbackAnimationInterval(),
				MinEditInterval:   cfg.Agents.Defaults.GetToolFeedbackEditMinInterval(),
			},
			cfg.Agents.Defaults.IsToolFeedbackSeparateMessagesEnabled(),
		)
	}
	for _, opt := range opts {
		if opt != nil {
			opt(m)
		}
	}

	// Register as streaming delegate so the agent loop can obtain streamers
	messageBus.SetStreamDelegate(m)

	if err := m.lifecycle.initChannels(m, &cfg.Channels); err != nil {
		return nil, err
	}

	// Store initial config hashes for all channels
	m.lifecycle.setInitialHashes(toChannelHashes(cfg))

	return m, nil
}

func (m *Manager) deliveryRuntime() *DeliveryRuntime {
	if m.delivery == nil {
		m.delivery = newDeliveryRuntime()
	}
	if m.delivery.host == nil {
		m.delivery.bindHost(m)
	}
	return m.delivery
}

func (m *Manager) streamCoordinator() *StreamCoordinator {
	if m.stream == nil {
		m.stream = newStreamCoordinator()
	}
	return m.stream
}

func (m *Manager) deliveryChannel(name string) (Channel, bool) {
	return m.lifecycle.channel(name)
}

func (m *Manager) deliveryTextSource() <-chan bus.OutboundMessage {
	return m.bus.OutboundChan()
}

func (m *Manager) deliveryMediaSource() <-chan bus.OutboundMediaMessage {
	return m.bus.OutboundMediaChan()
}

func (m *Manager) deliverySplitOnMarker() bool {
	return m.lifecycle.splitOnMarker()
}

func (m *Manager) deliveryToolFeedbackEnabled() bool {
	return m.streamCoordinator().hasToolFeedback()
}

func (m *Manager) lifecycleBus() *bus.MessageBus {
	return m.bus
}

func (m *Manager) lifecyclePlaceholderRecorder() PlaceholderRecorder {
	return m
}

// SetMediaStore updates the store used by the manager and every channel that
// accepts media store injection. Gateway reload creates a fresh store, so
// keeping existing channels on the same store as the agent is required for
// inbound media refs to remain resolvable after reload.
func (m *Manager) SetMediaStore(store media.MediaStore) {
	m.lifecycle.setMediaStore(store)
}

func (l *ChannelLifecycle) installDeliveryOwnerLocked(
	ctx context.Context,
	delivery *DeliveryRuntime,
	name string,
	channel Channel,
	channelType string,
) *deliveryOwner {
	owner := newDeliveryOwner(name, channel, channelType)
	delivery.install(owner)
	owner.StartDelivery(ctx, delivery)
	return owner
}
