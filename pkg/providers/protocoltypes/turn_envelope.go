package protocoltypes

import (
	"strings"
)

const (
	turnEnvelopeOpenTag  = `<mintclaw_turn_context version="1">`
	turnEnvelopeCloseTag = "</mintclaw_turn_context>"
)

// ProjectTurnEnvelope returns the provider-visible representation of a
// canonical message. Version 1 is intentionally rendered as one deterministic
// compact text carrier appended to the owning root-user message: canonical content
// remains suitable for search and presentation while replay sends the exact
// admitted context without changing conversation message order.
func ProjectTurnEnvelope(message Message) Message {
	envelope := message.TurnEnvelope
	message.TurnEnvelope = nil
	if envelope == nil {
		return message
	}
	if envelope.Version != TurnEnvelopeVersion1 {
		return message
	}

	parts := make([]string, 0, len(envelope.Parts))
	for _, part := range envelope.Parts {
		if strings.TrimSpace(part.Content) != "" {
			parts = append(parts, part.Content)
		}
	}
	if len(parts) == 0 {
		return message
	}
	carrier := turnEnvelopeOpenTag + "\n" + strings.Join(parts, "\n\n---\n\n") + "\n" + turnEnvelopeCloseTag
	if strings.TrimSpace(message.Content) == "" {
		message.Content = carrier
	} else {
		message.Content += "\n\n" + carrier
	}
	return message
}
