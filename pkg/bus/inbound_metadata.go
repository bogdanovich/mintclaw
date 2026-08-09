package bus

// InboundMetadataKeyInteractionChoice carries a channel-validated, bounded
// interaction choice separately from any quoted context added to Content.
const InboundMetadataKeyInteractionChoice = "interaction_choice"

// InboundMetadataKeyInteractionResponse carries the channel-authored text of
// a reply to an interaction prompt without any quoted-message decoration.
const InboundMetadataKeyInteractionResponse = "interaction_response"

const (
	InboundInteractionChoiceAllowOnce = "allow_once"
	InboundInteractionChoiceDeny      = "deny"
	InboundInteractionChoiceCancel    = "cancel"
	InboundInteractionCancelLabel     = "⛔ Cancel turn"
)
