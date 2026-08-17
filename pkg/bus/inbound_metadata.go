package bus

// InboundMetadataKeyInteractionChoice carries a channel-validated, bounded
// interaction choice separately from any quoted context added to Content.
const InboundMetadataKeyInteractionChoice = "interaction_choice"

// InboundMetadataKeyInteractionResponse carries the channel-authored text of
// a reply to an interaction prompt without any quoted-message decoration.
const InboundMetadataKeyInteractionResponse = "interaction_response"

// InboundMetadataKeyInteractionShortID binds a channel-projected response to
// the exact interaction prompt that produced its controls.
const InboundMetadataKeyInteractionShortID = "interaction_short_id"

const (
	InboundInteractionChoiceAllowOnce = "allow_once"
	InboundInteractionChoiceDeny      = "deny"
	InboundInteractionChoiceCancel    = "cancel"
	InboundInteractionCancelLabel     = "⛔ Cancel turn"
)
