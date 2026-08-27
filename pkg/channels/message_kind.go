package channels

import "github.com/bogdanovich/mintclaw/pkg/bus"

// OutboundMessageDismissesTrackedToolFeedback reports whether the outgoing
// message is terminal user-facing content that should clear any previously
// tracked tool-feedback carrier after a fresh send.
func OutboundMessageDismissesTrackedToolFeedback(msg bus.OutboundMessage) bool {
	metadata := msg.Metadata
	if metadata.IsToolFeedback() || metadata.IsThought() || metadata.IsToolCalls() {
		return false
	}
	if metadata.IsApprovalPrompt() || metadata.IsQuestionPrompt() || metadata.RemovesInteractionControls() {
		return false
	}
	return true
}
