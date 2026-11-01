package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func documentConversationIdentityForTurn(ts *turnState) (string, error) {
	if ts == nil || !documentConversationBound(ts) || strings.TrimSpace(ts.userMessage) == "" && len(ts.media) == 0 {
		return "", nil
	}
	inbound := ts.opts.Dispatch.InboundContext
	if inbound == nil {
		return "", fmt.Errorf("ordinary form conversation requires inbound identity")
	}
	return protectedAnswerIdempotencyKey("", bus.InboundMessage{
		Context: *inbound, SpoolID: ts.opts.DocumentIntakeSpoolID,
	})
}

func documentConversationBound(ts *turnState) bool {
	return ts != nil && (ts.opts.DocumentIntakeReference != "" ||
		ts.opts.Dispatch.InboundContext != nil && !ts.opts.Dispatch.InboundContext.DocumentIntake.IsZero())
}

// The existing canonical restore snapshot is the full owner history, not only
// the selected model tail. A summary may quote form conversation even after
// the original message leaves that tail. No second store or permanent taint
// registry is needed to retain this diagnostic boundary.
func (ts *turnState) documentConversationProtected() bool {
	if ts == nil {
		return false
	}
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return documentConversationBound(ts) ||
		hasDocumentConversation(ts.canonicalRestoreHistory) ||
		hasDocumentConversation(ts.persistedMessages) || hasDocumentConversation(ts.liveTurnMessages)
}

func (ts *turnState) diagnosticUserMessage() string {
	if ts.documentConversationProtected() {
		return protectedConversationDiagnosticReceipt
	}
	return ts.userMessage
}

func (ts *turnState) documentConversationEnvelope(identity string) (*providers.TurnEnvelope, error) {
	if identity == "" {
		return nil, nil
	}
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	for _, existing := range ts.canonicalRestoreHistory {
		if existing.Role != "user" || documentConversationID(existing) != identity {
			continue
		}
		if existing.Content != ts.userMessage {
			return nil, &turnAdmissionError{err: fmt.Errorf("ordinary conversation identity changed its body")}
		}
		return existing.TurnEnvelope.Clone(), nil
	}
	return nil, nil
}

func omitRepeatedConversationInput(history []providers.Message, identity string) []providers.Message {
	if identity == "" {
		return history
	}
	filtered := make([]providers.Message, 0, len(history))
	for _, message := range history {
		if message.Role != "user" || documentConversationID(message) != identity {
			filtered = append(filtered, message)
		}
	}
	return filtered
}

func containsConversationInput(history []providers.Message, identity string) bool {
	if identity == "" {
		return false
	}
	for _, message := range history {
		if message.Role == "user" && documentConversationID(message) == identity {
			return true
		}
	}
	return false
}

// Only ordinary task-scope answers use normal history here. Dedicated protected
// field, password and authentication sinks keep their existing opaque contract.
// The application service already holds the session claim and verified route;
// this append is conversation retention, never acceptance of native facts.
func preserveDocumentConversationReply(
	ctx context.Context, command answerInteractionCommand, record interactions.Record, identity string,
) error {
	if !document.IsFormTaskRouteQuestionBinding(record.ProtectedAnswer) ||
		strings.TrimSpace(command.Message.Content) == "" {
		return nil
	}
	if command.Agent == nil || command.Agent.Sessions == nil {
		return fmt.Errorf("ordinary form conversation history is unavailable")
	}
	message := providers.Message{
		Role: "user", Content: command.Message.Content, Media: append([]string(nil), command.Message.Media...),
		CreatedAt: inboundReceivedAt(command.Message),
		TurnEnvelope: &providers.TurnEnvelope{
			Version: providers.TurnEnvelopeVersion1, DocumentConversationID: identity,
		},
	}
	return persistFullSessionMessage(ctx, command.Agent.Sessions, command.Authorization.SessionKey, &message)
}
