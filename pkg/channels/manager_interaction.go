package channels

import (
	"fmt"

	"github.com/bogdanovich/mintclaw/pkg/bus"
)

type interactionControlSyncer interface {
	SyncInteractionControls(bus.OutboundMessage) error
}

// SyncInteractionControls projects durable interaction control state into a
// channel without sending another message.
func (m *Manager) SyncInteractionControls(msg bus.OutboundMessage) error {
	channel, ok := m.GetChannel(msg.Channel)
	if !ok {
		return fmt.Errorf("channel %q is unavailable", msg.Channel)
	}
	syncer, ok := channel.(interactionControlSyncer)
	if !ok {
		return nil
	}
	return syncer.SyncInteractionControls(msg)
}
