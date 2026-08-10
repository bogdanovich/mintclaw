package channels

import (
	"fmt"

	"github.com/bogdanovich/mintclaw/pkg/bus"
)

type interactionControlRestorer interface {
	RestoreInteractionControls(bus.OutboundMessage) error
}

// RestoreInteractionControls rebuilds channel-local controls from durable
// interaction state without sending another prompt.
func (m *Manager) RestoreInteractionControls(msg bus.OutboundMessage) error {
	channel, ok := m.GetChannel(msg.Channel)
	if !ok {
		return fmt.Errorf("channel %q is unavailable", msg.Channel)
	}
	restorer, ok := channel.(interactionControlRestorer)
	if !ok {
		return nil
	}
	return restorer.RestoreInteractionControls(msg)
}
