package channels

import (
	"context"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
)

func trackedToolFeedbackMessageChatID(ch Channel, chatID string, outboundCtx *bus.InboundContext) string {
	if resolver, ok := ch.(toolFeedbackMessageTargetResolver); ok {
		if resolved := strings.TrimSpace(resolver.ToolFeedbackMessageChatID(chatID, outboundCtx)); resolved != "" {
			return resolved
		}
	}
	return resolveOutboundChatID(ch, chatID, outboundCtx)
}

func (m *Manager) cleanupDeliveryState(
	ctx context.Context,
	name string,
	chatID string,
	outboundCtx *bus.InboundContext,
	ch Channel,
	opts deliveryCleanupOptions,
) {
	cleanupChatIDs := candidateChatIDs(chatID, resolveOutboundChatID(ch, chatID, outboundCtx))

	if opts.StopTyping {
		for _, cleanupChatID := range cleanupChatIDs {
			if entry, loaded := m.streamCoordinator().takeTyping(name + ":" + cleanupChatID); loaded {
				entry.stop()
			}
		}
	}

	if opts.UndoReaction {
		for _, cleanupChatID := range cleanupChatIDs {
			if entry, loaded := m.streamCoordinator().takeReaction(name + ":" + cleanupChatID); loaded {
				entry.undo()
			}
		}
	}

	if opts.ClearStreamActive {
		for _, cleanupChatID := range cleanupChatIDs {
			streamKey := streamSuppressionKey(
				name, cleanupChatID, opts.SessionKey, primaryTraceScope(opts.TraceScopes),
			)
			m.streamCoordinator().clear(streamKey)
		}
	}

	if opts.DismissToolFeedback {
		m.dismissToolFeedbackTargets(
			ctx, name, ch, chatID, outboundCtx, opts.SessionKey, opts.TraceScopes,
		)
	}

	if opts.DeletePlaceholder {
		for _, cleanupChatID := range cleanupChatIDs {
			if entry, loaded := m.streamCoordinator().
				takePlaceholder(name + ":" + cleanupChatID); loaded &&
				entry.id != "" {
				if deleter, ok := ch.(MessageDeleter); ok {
					_ = deleter.DeleteMessage(ctx, cleanupChatID, entry.id)
				}
			}
		}
	}
}

func sessionScopedToolFeedbackMessageChatID(chatID, sessionKey string) string {
	chatID = strings.TrimSpace(chatID)
	sessionKey = strings.TrimSpace(sessionKey)
	if chatID == "" || sessionKey == "" {
		return chatID
	}
	return chatID + "#session:" + sessionKey
}

func toolFeedbackCoordinatorKey(channelName, trackedChatID string) string {
	channelName = strings.TrimSpace(channelName)
	trackedChatID = strings.TrimSpace(trackedChatID)
	if channelName == "" || trackedChatID == "" {
		return ""
	}
	return channelName + ":" + trackedChatID
}

func toolFeedbackTarget(
	channelName string,
	ch Channel,
	chatID string,
	outboundCtx *bus.InboundContext,
	sessionKey string,
	traceScope runtimeevents.TraceScope,
) (string, string) {
	deliveryChatID := trackedToolFeedbackMessageChatID(ch, chatID, outboundCtx)
	trackedChatID := sessionScopedToolFeedbackMessageChatID(deliveryChatID, sessionKey)
	key := toolFeedbackCoordinatorKey(channelName, trackedChatID)
	if strings.TrimSpace(sessionKey) == "" {
		key, _ = traceScopedDeliveryKey(key, traceScope)
	}
	return key, deliveryChatID
}

func toolFeedbackTargets(
	channelName string,
	ch Channel,
	chatID string,
	outboundCtx *bus.InboundContext,
	sessionKey string,
	traceScopes []runtimeevents.TraceScope,
) ([]string, bool) {
	base, _ := toolFeedbackTarget(
		channelName, ch, chatID, outboundCtx, sessionKey, runtimeevents.TraceScope{},
	)
	if strings.TrimSpace(sessionKey) != "" {
		return []string{base}, false
	}
	normalized, err := bus.NormalizeTraceScopes(traceScopes)
	if err != nil || len(normalized) == 0 {
		return []string{base}, false
	}
	keys := make([]string, 0, len(normalized))
	for _, traceScope := range normalized {
		key, _ := traceScopedDeliveryKey(base, traceScope)
		keys = append(keys, key)
	}
	return keys, true
}

func toolFeedbackOperationsFor(ch Channel) toolFeedbackOperations {
	operations := toolFeedbackOperations{}
	if editor, ok := ch.(toolFeedbackMessageEditor); ok {
		operations.edit = editor.EditToolFeedbackMessage
	} else if editor, ok := ch.(MessageEditor); ok {
		operations.edit = editor.EditMessage
	}
	if deleter, ok := ch.(MessageDeleter); ok {
		operations.delete = deleter.DeleteMessage
	}
	return operations
}

func (m *Manager) beginToolFeedbackTerminals(
	channelName string,
	ch Channel,
	chatID string,
	outboundCtx *bus.InboundContext,
	sessionKey string,
	traceScopes []runtimeevents.TraceScope,
	transient bool,
) []*toolFeedbackTerminal {
	if m == nil || !m.streamCoordinator().hasToolFeedback() {
		return nil
	}
	keys, scoped := m.resolveToolFeedbackTargets(
		channelName, ch, chatID, outboundCtx, sessionKey, traceScopes,
	)
	return m.streamCoordinator().beginToolFeedbackTerminals(keys, scoped, transient)
}

func (m *Manager) completeToolFeedbackTerminals(
	ctx context.Context,
	terminals []*toolFeedbackTerminal,
	success bool,
) {
	m.streamCoordinator().completeToolFeedbackTerminals(ctx, terminals, success)
}

func (m *Manager) beginOutboundToolFeedbackTerminals(
	channelName string,
	ch Channel,
	msg bus.OutboundMessage,
) []*toolFeedbackTerminal {
	if m == nil || !m.streamCoordinator().hasToolFeedback() || outboundMessageIsToolFeedback(msg) ||
		!OutboundMessageDismissesTrackedToolFeedback(msg) {
		return nil
	}
	return m.beginToolFeedbackTerminals(
		channelName,
		ch,
		outboundMessageChatID(msg),
		&msg.Context,
		msg.SessionKey,
		msg.TraceScopes,
		bus.OutboundMetadataFromMessage(msg).IsInterim(),
	)
}

func (m *Manager) deliverToolFeedback(
	ctx context.Context,
	channelName string,
	ch Channel,
	msg bus.OutboundMessage,
	send func(context.Context, bus.OutboundMessage) ([]string, error),
) ([]string, error) {
	key, deliveryChatID := toolFeedbackTarget(
		channelName,
		ch,
		outboundMessageChatID(msg),
		&msg.Context,
		msg.SessionKey,
		primaryTraceScope(msg.TraceScopes),
	)
	content := prepareToolFeedbackMessageContent(ch, msg.Content)
	operations := toolFeedbackOperationsFor(ch)
	return m.streamCoordinator().deliverToolFeedback(
		ctx,
		key,
		deliveryChatID,
		content,
		operations,
		func(sendCtx context.Context, prepared string) (toolFeedbackSendResult, error) {
			sendMsg := msg
			sendMsg.Content = prepared
			if sender, ok := ch.(toolFeedbackMessageSender); ok {
				messageIDs, editable, err := sender.SendToolFeedbackMessage(sendCtx, sendMsg)
				return toolFeedbackSendResult{messageIDs: messageIDs, editable: editable}, err
			}
			messageIDs, err := send(sendCtx, sendMsg)
			return toolFeedbackSendResult{messageIDs: messageIDs, editable: operations.edit != nil}, err
		},
	)
}

// DismissToolFeedback clears tracked progress for one outbound identity.
func (m *Manager) DismissToolFeedback(ctx context.Context, target bus.OutboundMessage) {
	if m == nil || !m.streamCoordinator().hasToolFeedback() {
		return
	}
	channelName := outboundMessageChannel(target)
	ch, ok := m.GetChannel(channelName)
	if !ok {
		return
	}
	m.dismissToolFeedbackTargets(
		ctx,
		channelName,
		ch,
		outboundMessageChatID(target),
		&target.Context,
		target.SessionKey,
		target.TraceScopes,
	)
}

// PauseToolFeedback stops animation while retaining the editable progress
// carrier for a later turn in the same logical session.
func (m *Manager) PauseToolFeedback(ctx context.Context, target bus.OutboundMessage) {
	if m == nil || !m.streamCoordinator().hasToolFeedback() {
		return
	}
	channelName := outboundMessageChannel(target)
	ch, ok := m.GetChannel(channelName)
	if !ok {
		return
	}
	keys, scoped := m.resolveToolFeedbackTargets(
		channelName,
		ch,
		outboundMessageChatID(target),
		&target.Context,
		target.SessionKey,
		target.TraceScopes,
	)
	m.streamCoordinator().pauseToolFeedback(ctx, keys, scoped)
}

func (m *Manager) dismissToolFeedbackTargets(
	ctx context.Context,
	channelName string,
	ch Channel,
	chatID string,
	outboundCtx *bus.InboundContext,
	sessionKey string,
	traceScopes []runtimeevents.TraceScope,
) {
	keys, scoped := m.resolveToolFeedbackTargets(
		channelName, ch, chatID, outboundCtx, sessionKey, traceScopes,
	)
	m.streamCoordinator().dismissToolFeedback(ctx, keys, scoped)
}

func (m *Manager) resolveToolFeedbackTargets(
	channelName string,
	ch Channel,
	chatID string,
	outboundCtx *bus.InboundContext,
	sessionKey string,
	traceScopes []runtimeevents.TraceScope,
) ([]string, bool) {
	keys, scoped := toolFeedbackTargets(
		channelName, ch, chatID, outboundCtx, sessionKey, traceScopes,
	)
	if !scoped && len(keys) == 1 {
		if key, ok := m.streamCoordinator().singleActiveScopedToolFeedbackKey(keys[0]); ok {
			return []string{key}, true
		}
	}
	return keys, scoped
}

func prepareToolFeedbackMessageContent(ch Channel, content string) string {
	prepared := strings.TrimSpace(content)
	if prepared == "" {
		return ""
	}
	if preparer, ok := ch.(toolFeedbackMessageContentPreparer); ok {
		if candidate := strings.TrimSpace(preparer.PrepareToolFeedbackMessageContent(prepared)); candidate != "" {
			return candidate
		}
	}
	return prepared
}

// RecordPlaceholder registers a placeholder message for later editing.
// Implements PlaceholderRecorder.
func (m *Manager) RecordPlaceholder(channel, chatID, placeholderID string) {
	key := channel + ":" + chatID
	m.streamCoordinator().storePlaceholder(key, placeholderEntry{id: placeholderID, createdAt: time.Now()})
}

// SendPlaceholder sends a "Thinking…" placeholder for the given channel/chatID
// and records it for later editing. Returns true if a placeholder was sent.
func (m *Manager) SendPlaceholder(ctx context.Context, channel, chatID string) bool {
	ch, ok := m.lifecycle.channel(channel)
	if !ok {
		return false
	}
	pc, ok := ch.(PlaceholderCapable)
	if !ok {
		return false
	}
	phID, err := pc.SendPlaceholder(ctx, chatID)
	if err != nil || phID == "" {
		return false
	}
	m.RecordPlaceholder(channel, chatID, phID)
	return true
}

// RecordTypingStop registers a typing stop function for later invocation.
// Implements PlaceholderRecorder.
func (m *Manager) RecordTypingStop(channel, chatID string, stop func()) {
	key := channel + ":" + chatID
	entry := typingEntry{stop: stop, createdAt: time.Now()}
	if previous, loaded := m.streamCoordinator().swapTyping(key, entry); loaded && previous.stop != nil {
		previous.stop()
	}
}

// InvokeTypingStop invokes the registered typing stop function for the given channel and chatID.
// It is safe to call even when no typing indicator is active (no-op).
// Used by the agent loop to stop typing when processing completes (success, error, or panic),
// regardless of whether an outbound message is published.
func (m *Manager) InvokeTypingStop(channel, chatID string) {
	key := channel + ":" + chatID
	if entry, loaded := m.streamCoordinator().takeTyping(key); loaded {
		entry.stop()
	}
}

// RecordReactionUndo registers a reaction undo function for later invocation.
// Implements PlaceholderRecorder.
func (m *Manager) RecordReactionUndo(channel, chatID string, undo func()) {
	key := channel + ":" + chatID
	m.streamCoordinator().storeReaction(key, reactionEntry{undo: undo, createdAt: time.Now()})
}
