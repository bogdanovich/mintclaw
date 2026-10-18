package eval

import (
	"errors"
	"mime"
	"net/url"
	"slices"
	"strings"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
)

const maxPDFWireMessages = 16

var errPDFWire = errors.New("eval: PDF wire observation unavailable")

type pdfWirePart struct{ kind, contentType, path string }

type pdfWireMessage struct {
	session, agent, sessionKey, delivery string
	parts                                []pdfWirePart
	workspaceMatched, conflict           bool
}

// Only the sole client reader writes this bounded value-free projection. The
// conversation owner may inspect it only after Client.Close has joined readers.
type pdfWireObservation struct {
	workspace string
	messages  map[string]pdfWireMessage
	code      string
}

func (wire *pdfWireObservation) observe(message protocol.MintClawMessage) error {
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
	candidate := pdfWireMessage{
		session: message.SessionID, agent: event.AgentID, sessionKey: event.SessionKey, delivery: delivery,
		workspaceMatched: true,
	}
	for _, scope := range event.TraceScopes {
		candidate.workspaceMatched = candidate.workspaceMatched && scope.Complete() && scope.Workspace == wire.workspace
	}
	for _, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if !ok {
			return errPDFWire
		}
		var projected pdfWirePart
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
	if previous, found := wire.messages[event.MessageID]; found {
		if previous.session != candidate.session || previous.agent != candidate.agent ||
			previous.sessionKey != candidate.sessionKey || previous.delivery != candidate.delivery ||
			previous.workspaceMatched != candidate.workspaceMatched || !slices.Equal(previous.parts, candidate.parts) {
			previous.conflict = true
			wire.messages[event.MessageID] = previous
		}
	} else {
		if len(wire.messages) >= maxPDFWireMessages {
			wire.code = "wire_media_limit"
			return errPDFWire
		}
		if wire.messages == nil {
			wire.messages = make(map[string]pdfWireMessage)
		}
		wire.messages[event.MessageID] = candidate
	}
	wire.code = ""
	return nil
}

func (domain *pdfConversation) concludeWire(trial *Trial, complete bool) {
	if domain.wire == nil || trial.PDF == nil {
		return
	}
	wire := domain.wire
	defer clear(wire.messages)
	evidence := &PDFWireEvidence{
		Version: Version, Scope: "native_bound_client_media_v1", Code: "wire_media_unavailable",
		Complete: complete && wire.code == "", ObservedMessages: len(wire.messages),
	}
	trial.PDF.Wire = evidence
	if wire.code != "" {
		evidence.Code = wire.code
	}
	settled := trial.PDF.Settlement
	if settled == nil || len(settled.PlatformMessageIDs) != 1 || !strings.HasPrefix(settled.ArtifactRef, "media://") ||
		trial.SessionID == "" || trial.AgentID == "" || trial.SessionKey == "" {
		return
	}
	path := "/mintclaw/media/" + url.PathEscape(strings.TrimPrefix(settled.ArtifactRef, "media://"))
	for _, candidate := range wire.messages {
		if candidate.session != trial.SessionID || candidate.agent != trial.AgentID ||
			candidate.sessionKey != trial.SessionKey {
			continue
		}
		for _, part := range candidate.parts {
			if part.path == path {
				evidence.MatchingMessages++
				break
			}
		}
	}
	candidate, found := wire.messages[settled.PlatformMessageIDs[0]]
	code := ""
	switch {
	case evidence.MatchingMessages > 1:
		code = "multiple_wire_deliveries"
	case found && candidate.conflict:
		code = "wire_media_conflict"
	case found && candidate.session != "" && candidate.session != trial.SessionID ||
		found && candidate.agent != "" && candidate.agent != trial.AgentID ||
		found && candidate.sessionKey != "" && candidate.sessionKey != trial.SessionKey ||
		found && candidate.delivery != "" && candidate.delivery != settled.OutboxDeliveryID ||
		found && !candidate.workspaceMatched:
		code = "wire_media_binding_mismatch"
	case found && (len(candidate.parts) != 1 || candidate.parts[0].path != path ||
		candidate.parts[0].kind != "file" || candidate.parts[0].contentType != "application/pdf"):
		code = "wire_media_binding_mismatch"
	}
	if code != "" {
		evidence.Code = code
		failPDFArtifact(trial, Fail, code)
		return
	}
	if wire.code != "" {
		evidence.Code = wire.code
		return
	}
	if evidence.Complete && found && evidence.MatchingMessages == 1 && candidate.delivery == settled.OutboxDeliveryID &&
		candidate.session != "" && candidate.agent != "" && candidate.sessionKey != "" {
		evidence.Code, evidence.MessageID = "wire_media_observed", settled.PlatformMessageIDs[0]
	}
}
