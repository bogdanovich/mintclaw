package channels

import (
	"context"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

// preSend handles typing stop, reaction undo, and placeholder editing before sending a message.
// Returns the delivered message IDs and true when delivery completed before a normal Send.
func (m *Manager) preSend(ctx context.Context, name string, msg bus.OutboundMessage, ch Channel) ([]string, bool) {
	chatID := outboundMessageChatID(msg)
	key := name + ":" + chatID
	traceScope := primaryTraceScope(msg.TraceScopes)
	streamKey := streamSuppressionKey(name, chatID, msg.SessionKey, traceScope)
	activeStreamKey, streamActive := m.streamCoordinator().activeKey(name, chatID, msg.SessionKey, traceScope)

	m.cleanupDeliveryState(ctx, name, chatID, &msg.Context, ch, deliveryCleanupOptions{
		StopTyping:   true,
		UndoReaction: true,
	})

	isToolFeedback := outboundMessageIsToolFeedback(msg)
	isToolCalls := outboundMessageIsToolCalls(msg)
	isAuxiliaryMessage := outboundMessageHasAuxiliaryKind(msg)
	isFinalMessage := outboundMessageIsFinal(msg)
	// 3. If a stream already finalized this chat, stale auxiliary messages must
	// be dropped without consuming the final-response marker. Streaming
	// finalization bypasses the worker queue, so older queued feedback/thoughts
	// can arrive before the normal final outbound message that cleans up the
	// marker and placeholder.
	// Tool calls must reach the UI, and the queued final must consume the active
	// marker after the streamed copy has already been delivered.
	if isAuxiliaryMessage && !isToolCalls && !isFinalMessage {
		if streamActive {
			return nil, true
		}
		if m.streamCoordinator().tombstoneActiveForMessage(
			name, chatID, msg.SessionKey, traceScope,
			time.Now(),
		) {
			return nil, true
		}
	}

	// 4. If a stream already finalized this turn, skip only the duplicate final
	// outbound. Earlier queued visible messages must still be delivered.
	if isFinalMessage {
		if streamActive {
			if !m.streamCoordinator().consumeActive(activeStreamKey) {
				streamActive = false
			} else {
				if entry, loaded := m.streamCoordinator().takePlaceholder(key); loaded && entry.id != "" {
					// Prefer deleting the placeholder (cleaner UX than editing to same content)
					if deleter, ok := ch.(MessageDeleter); ok {
						_ = deleter.DeleteMessage(ctx, chatID, entry.id) // best effort
					} else if editor, ok := ch.(MessageEditor); ok {
						if payloadEditor, ok := ch.(MessageEditorWithPayload); ok {
							_ = payloadEditor.EditMessageWithPayload(
								ctx,
								chatID,
								entry.id,
								outboundMessageEditPayload(msg, msg.Content),
							)
						} else {
							_ = editor.EditMessage(ctx, chatID, entry.id, msg.Content) // fallback
						}
					}
				}
				if m.streamCoordinator().hasToolFeedback() {
					keys, _ := toolFeedbackTargets(
						name, ch, chatID, &msg.Context, msg.SessionKey, msg.TraceScopes,
					)
					m.streamCoordinator().releaseToolFeedbackTerminals(keys)
				}
				return nil, true
			}
		}
	}

	if streamActive {
		return nil, false
	}
	if m.streamCoordinator().activeForChat(name, chatID) {
		return nil, false
	}

	if !isAuxiliaryMessage {
		m.streamCoordinator().clearTombstone(streamKey)
	}

	// 5. Try editing placeholder
	if entry, loaded := m.streamCoordinator().takePlaceholder(key); loaded && entry.id != "" {
		logger.InfoCF("channels", "Evaluating placeholder edit bypass",
			map[string]any{
				"channel":          name,
				"chat_id":          chatID,
				"placeholder_id":   entry.id,
				"message_kind":     bus.OutboundMetadataFromMessage(msg).MessageKind,
				"is_tool_feedback": isToolFeedback,
				"bypass":           outboundMessageBypassesPlaceholderEdit(msg),
			})
		if isToolFeedback {
			if deleter, ok := ch.(MessageDeleter); ok {
				_ = deleter.DeleteMessage(ctx, chatID, entry.id) // best effort
			}
			return nil, false
		}
		if outboundMessageBypassesPlaceholderEdit(msg) {
			if deleter, ok := ch.(MessageDeleter); ok {
				_ = deleter.DeleteMessage(ctx, chatID, entry.id) // best effort
			}
			return nil, false
		}
		if editor, ok := ch.(MessageEditor); ok {
			content := msg.Content
			err := func() error {
				if payloadEditor, ok := ch.(MessageEditorWithPayload); ok {
					return payloadEditor.EditMessageWithPayload(
						ctx,
						chatID,
						entry.id,
						outboundMessageEditPayload(msg, content),
					)
				}
				return editor.EditMessage(ctx, chatID, entry.id, content)
			}()
			if err == nil {
				return []string{entry.id}, true
			}
			// edit failed → fall through to normal Send
		}
	}

	return nil, false
}

// preSendMedia handles typing stop, reaction undo, and placeholder cleanup
// before sending media attachments. Unlike preSend for text messages, media
// delivery never edits the placeholder because there is no text payload to
// replace it with; it only attempts to delete the placeholder when possible.
func (m *Manager) preSendMedia(ctx context.Context, name string, msg bus.OutboundMediaMessage, ch Channel) {
	chatID := outboundMediaChatID(msg)

	m.cleanupDeliveryState(ctx, name, chatID, &msg.Context, ch, deliveryCleanupOptions{
		StopTyping:        true,
		UndoReaction:      true,
		ClearStreamActive: true,
		DeletePlaceholder: true,
		SessionKey:        msg.SessionKey,
		TraceScopes:       msg.TraceScopes,
	})
}
