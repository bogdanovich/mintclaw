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

// InboundMetadataKeyInteractionResponseError marks a channel-projected
// interaction response that could not be resolved to a valid answer.
const InboundMetadataKeyInteractionResponseError = "interaction_response_error"

// InboundMetadataKeyInteractionOptionIndex preserves a channel-validated
// option callback until the durable prompt receipt can resolve its label.
const InboundMetadataKeyInteractionOptionIndex = "interaction_option_index"

// InboundMetadataKeyInteractionResponseMessageID carries the channel message
// that a final interaction response can safely reply to when the inbound event
// itself is not a message, such as a Telegram callback query.
const InboundMetadataKeyInteractionResponseMessageID = "interaction_response_message_id"

const (
	InboundInteractionChoiceAllowOnce = "allow_once"
	InboundInteractionChoiceDeny      = "deny"
	InboundInteractionChoiceCancel    = "cancel"
	InboundInteractionCancelLabel     = "⛔ Cancel turn"
)
