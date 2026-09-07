package bus

// These keys are read only while normalizing durable spool records written
// before inbound interaction projections became typed. New adapters must write
// InboundContext.Interaction instead. Remove this compatibility reader after
// pre-F3 inbound spools have drained from supported deployments.
const (
	legacyInboundInteractionChoiceKey            = "interaction_choice"
	legacyInboundInteractionResponseKey          = "interaction_response"
	legacyInboundInteractionResponseCandidateKey = "interaction_response_candidate"
	legacyInboundInteractionShortIDKey           = "interaction_short_id"
	legacyInboundInteractionResponseErrorKey     = "interaction_response_error"
	legacyInboundInteractionOptionIndexKey       = "interaction_option_index"
	legacyInboundInteractionResponseMessageIDKey = "interaction_response_message_id"
)

const InboundInteractionCancelLabel = "⛔ Cancel turn"
