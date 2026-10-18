package agent

import "github.com/bogdanovich/mintclaw/pkg/bus"

// Keep the source's execution metadata in the private runtime queue, outside
// provider messages and canonical history. Clone reference-backed values so
// callers cannot change the admitted routing or reply metadata later.
func steeringContextForSender(inbound *bus.InboundContext, senderID string) *bus.InboundContext {
	cloned := cloneInboundContext(inbound)
	if cloned == nil {
		return nil
	}
	if senderID != "" && senderID != cloned.SenderID {
		cloned.SenderID = senderID
		cloned.ActorID = senderID
	}
	return cloned
}

func sameSteeringDeliveryScope(first, second *bus.InboundContext) bool {
	return first.Channel == second.Channel && first.Account == second.Account &&
		first.ChatID == second.ChatID && first.ChatType == second.ChatType && first.TopicID == second.TopicID &&
		first.SpaceID == second.SpaceID && first.SpaceType == second.SpaceType &&
		first.ClientSessionID == second.ClientSessionID
}

func sameSteeringAuthority(first, second *bus.InboundContext) bool {
	return sameSteeringDeliveryScope(first, second) && first.SenderID == second.SenderID &&
		first.ActorID == second.ActorID
}

func (sq *steeringQueue) nextInboundContext(scope runtimeSessionScope) *bus.InboundContext {
	if sq == nil {
		return nil
	}
	sq.mu.Lock()
	defer sq.mu.Unlock()
	for _, entry := range sq.queues[scope] {
		if entry.inbound != nil {
			return cloneInboundContext(entry.inbound)
		}
	}
	return nil
}
