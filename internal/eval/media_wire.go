package eval

import (
	"errors"
	"mime"
	"net/url"
	"slices"
	"strings"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/events"
)

const (
	maxPDFWireMessages = 16
	maxPDFWireVariants = 16
)

var errPDFWire = errors.New("eval: PDF wire observation unavailable")

type mediaWirePart struct{ kind, contentType, path string }

type mediaWireMessage struct {
	session, agent, sessionKey, delivery string
	parts                                []mediaWirePart
	workspaceMatched                     bool
	traces                               []events.TraceScope
}

// Only the sole client reader writes this bounded value-free projection. The
// conversation owner may inspect it only after Client.Close has joined readers.
type mediaWireObservation struct {
	workspace string
	messages  map[string][]mediaWireMessage
	code      string
}

func (wire *mediaWireObservation) observe(message protocol.MintClawMessage) error {
	raw, present := message.Payload["attachments"]
	if !present {
		return nil
	}
	parts, ok := raw.([]any)
	if ok && len(parts) == 0 {
		return nil
	}
	wire.code = "wire_media_unavailable"
	if message.Type != protocol.TypeMessageCreate || !ok || len(parts) == 0 || len(parts) > 16 {
		return errPDFWire
	}
	event, err := responseEvent(message, 0)
	if err != nil || !boundedIdentity(event.MessageID, 128) ||
		message.SessionID != "" && !boundedIdentity(message.SessionID, 128) ||
		event.AgentID != "" && !boundedIdentity(event.AgentID, 128) ||
		event.SessionKey != "" && !boundedIdentity(event.SessionKey, 512) {
		return errPDFWire
	}
	delivery, ok := message.Payload[protocol.PayloadKeyDeliveryID].(string)
	if message.Payload[protocol.PayloadKeyDeliveryID] != nil && (!ok || !boundedIdentity(delivery, 256)) {
		return errPDFWire
	}
	candidate := mediaWireMessage{
		session: message.SessionID, agent: event.AgentID, sessionKey: event.SessionKey, delivery: delivery,
		workspaceMatched: true,
		traces:           event.TraceScopes,
	}
	for _, scope := range event.TraceScopes {
		candidate.workspaceMatched = candidate.workspaceMatched && scope.Complete() && scope.Workspace == wire.workspace
	}
	for _, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if !ok {
			return errPDFWire
		}
		var projected mediaWirePart
		for _, field := range []struct {
			name    string
			value   *string
			maximum int
		}{
			{"type", &projected.kind, 64}, {"content_type", &projected.contentType, 256}, {"url", &projected.path, 512},
		} {
			value, valid := part[field.name].(string)
			if !valid || !boundedText(value, field.maximum) {
				return errPDFWire
			}
			*field.value = value
		}
		mediaType, _, parseErr := mime.ParseMediaType(projected.contentType)
		if parseErr != nil {
			return errPDFWire
		}
		projected.contentType = mediaType
		candidate.parts = append(candidate.parts, projected)
	}
	previous := wire.messages[event.MessageID]
	if slices.ContainsFunc(previous, func(value mediaWireMessage) bool {
		return value.session == candidate.session && value.agent == candidate.agent &&
			value.sessionKey == candidate.sessionKey && value.delivery == candidate.delivery &&
			value.workspaceMatched == candidate.workspaceMatched && slices.Equal(value.parts, candidate.parts) &&
			slices.Equal(value.traces, candidate.traces)
	}) {
		wire.code = ""
		return nil
	}
	if len(previous) >= maxPDFWireVariants || len(previous) == 0 && len(wire.messages) >= maxPDFWireMessages {
		wire.code = "wire_media_limit"
		return errPDFWire
	}
	if wire.messages == nil {
		wire.messages = make(map[string][]mediaWireMessage)
	}
	// A conflicting repeat cannot erase an earlier artifact or route. Count
	// distinct logical IDs below, but inspect every retained variant of each ID.
	wire.messages[event.MessageID] = append(previous, candidate)
	wire.code = ""
	return nil
}

func (wire *mediaWireObservation) bind(
	trial Trial, messageID, deliveryID, mediaRef, contentType string, complete bool,
) *MediaWireEvidence {
	evidence := &MediaWireEvidence{
		Version: Version, Scope: "native_bound_client_media_v1", Code: "wire_media_unavailable",
		Complete: complete && wire.code == "", ObservedMessages: len(wire.messages),
	}
	if wire.code != "" {
		evidence.Code = wire.code
	}
	if !strings.HasPrefix(mediaRef, "media://") ||
		trial.SessionID == "" || trial.AgentID == "" || trial.SessionKey == "" {
		return evidence
	}
	path := "/mintclaw/media/" + url.PathEscape(strings.TrimPrefix(mediaRef, "media://"))
	for _, variants := range wire.messages {
		if slices.ContainsFunc(variants, func(candidate mediaWireMessage) bool {
			if candidate.session != trial.SessionID || candidate.agent != trial.AgentID ||
				candidate.sessionKey != trial.SessionKey {
				return false
			}
			return slices.ContainsFunc(candidate.parts, func(part mediaWirePart) bool { return part.path == path })
		}) {
			evidence.MatchingMessages++
		}
	}
	variants, found := wire.messages[messageID]
	var candidate mediaWireMessage
	if found {
		candidate = variants[0]
	}
	code := ""
	switch {
	case evidence.MatchingMessages > 1:
		code = "multiple_wire_deliveries"
	case len(variants) > 1:
		code = "wire_media_conflict"
	case found && candidate.session != "" && candidate.session != trial.SessionID ||
		found && candidate.agent != "" && candidate.agent != trial.AgentID ||
		found && candidate.sessionKey != "" && candidate.sessionKey != trial.SessionKey ||
		found && candidate.delivery != "" && candidate.delivery != deliveryID ||
		found && !candidate.workspaceMatched:
		code = "wire_media_binding_mismatch"
	case found && (len(candidate.parts) != 1 || candidate.parts[0].path != path ||
		candidate.parts[0].kind != "file" || candidate.parts[0].contentType != contentType):
		code = "wire_media_binding_mismatch"
	}
	if code != "" {
		evidence.Code = code
		return evidence
	}
	if wire.code != "" {
		evidence.Code = wire.code
		return evidence
	}
	if evidence.Complete && found && evidence.MatchingMessages == 1 && candidate.delivery == deliveryID &&
		candidate.session != "" && candidate.agent != "" && candidate.sessionKey != "" {
		evidence.Code, evidence.MessageID = "wire_media_observed", messageID
	}
	return evidence
}
