package channels

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

// typingEntry wraps a typing stop function with a creation timestamp for TTL eviction.
type typingEntry struct {
	stop      func()
	createdAt time.Time
}

// reactionEntry wraps a reaction undo function with a creation timestamp for TTL eviction.
type reactionEntry struct {
	undo      func()
	createdAt time.Time
}

// placeholderEntry wraps a placeholder ID with a creation timestamp for TTL eviction.
type placeholderEntry struct {
	id        string
	createdAt time.Time
}

// StreamCoordinator owns transient UI state and final-stream suppression state.
// Channel adapters provide operations; the coordinator owns their lifecycle.
type StreamCoordinator struct {
	placeholders  sync.Map // "channel:chatID" -> placeholderEntry
	typingStops   sync.Map // "channel:chatID" -> typingEntry
	reactionUndos sync.Map // "channel:chatID" -> reactionEntry
	toolFeedback  *ToolFeedbackCoordinator

	streamActive              sync.Map // streamSuppressionKey -> true
	streamAuxiliaryTombstones sync.Map // streamSuppressionKey -> time.Time
}

func newStreamCoordinator() *StreamCoordinator {
	return &StreamCoordinator{}
}

type streamCoordinatorHost interface {
	deliveryChannel(name string) (Channel, bool)
	dismissToolFeedbackTargets(
		ctx context.Context,
		channelName string,
		ch Channel,
		chatID string,
		outboundCtx *bus.InboundContext,
		sessionKey string,
		traceScopes []runtimeevents.TraceScope,
	)
	streamSplitOnMarker() bool
	streamResponseFooterEnabled() bool
}

func (s *StreamCoordinator) getStreamer(
	ctx context.Context,
	host streamCoordinatorHost,
	channelName, chatID, sessionKey, requestID string,
	traceScope runtimeevents.TraceScope,
) (bus.Streamer, bool) {
	ch, exists := host.deliveryChannel(channelName)
	if !exists {
		return nil, false
	}

	streamingChannel, ok := ch.(StreamingCapable)
	if !ok {
		return nil, false
	}

	beginStream := func(beginCtx context.Context) (Streamer, error) {
		if scoped, scopedOK := ch.(ScopedStreamingCapable); scopedOK {
			return scoped.BeginStreamForScope(beginCtx, chatID, sessionKey, requestID, traceScope)
		}
		return streamingChannel.BeginStream(beginCtx, chatID)
	}
	streamer, err := beginStream(ctx)
	if err != nil {
		logger.DebugCF("channels", "Streaming unavailable, falling back to placeholder", map[string]any{
			"channel": channelName,
			"error":   err.Error(),
		})
		return nil, false
	}

	streamKey := streamSuppressionKey(channelName, chatID, sessionKey, traceScope)
	placeholderKey := channelName + ":" + chatID
	clearMarker := func() {
		s.consumeActive(streamKey)
	}
	onFinalize := func(finalizeCtx context.Context, finalContent string) {
		host.dismissToolFeedbackTargets(
			finalizeCtx,
			channelName,
			ch,
			chatID,
			&bus.InboundContext{Channel: channelName, ChatID: chatID},
			sessionKey,
			[]runtimeevents.TraceScope{traceScope},
		)
		if entry, loaded := s.takePlaceholder(placeholderKey); loaded && entry.id != "" {
			if deleter, deleteOK := ch.(MessageDeleter); deleteOK {
				_ = deleter.DeleteMessage(finalizeCtx, chatID, entry.id)
			} else if editor, editOK := ch.(MessageEditor); editOK {
				_ = editor.EditMessage(finalizeCtx, chatID, entry.id, finalContent)
			}
		}
		s.markFinalized(streamKey, time.Now())
	}

	footer := responseFooterStreamState{
		enabled: host.streamResponseFooterEnabled(),
		channel: channelName,
	}
	if host.streamSplitOnMarker() {
		return &splitMarkerStreamer{
			current:     streamer,
			reasoning:   reasoningStreamerFrom(streamer),
			begin:       beginStream,
			onFinalize:  onFinalize,
			clearMarker: clearMarker,
			footer:      footer,
		}, true
	}

	return &finalizeHookStreamer{
		Streamer:    streamer,
		clearMarker: clearMarker,
		onFinalize:  onFinalize,
		footer:      footer,
	}, true
}

func (s *StreamCoordinator) storePlaceholder(key string, entry placeholderEntry) {
	s.placeholders.Store(key, entry)
}

func (s *StreamCoordinator) takePlaceholder(key string) (placeholderEntry, bool) {
	value, loaded := s.placeholders.LoadAndDelete(key)
	entry, ok := value.(placeholderEntry)
	return entry, loaded && ok
}

func (s *StreamCoordinator) placeholderExists(key string) bool {
	_, ok := s.placeholders.Load(key)
	return ok
}

func (s *StreamCoordinator) swapTyping(key string, entry typingEntry) (typingEntry, bool) {
	value, loaded := s.typingStops.Swap(key, entry)
	previous, ok := value.(typingEntry)
	return previous, loaded && ok
}

func (s *StreamCoordinator) takeTyping(key string) (typingEntry, bool) {
	value, loaded := s.typingStops.LoadAndDelete(key)
	entry, ok := value.(typingEntry)
	return entry, loaded && ok
}

func (s *StreamCoordinator) typingExists(key string) bool {
	_, ok := s.typingStops.Load(key)
	return ok
}

func (s *StreamCoordinator) storeReaction(key string, entry reactionEntry) {
	s.reactionUndos.Store(key, entry)
}

func (s *StreamCoordinator) takeReaction(key string) (reactionEntry, bool) {
	value, loaded := s.reactionUndos.LoadAndDelete(key)
	entry, ok := value.(reactionEntry)
	return entry, loaded && ok
}

func (s *StreamCoordinator) markActive(key string) {
	s.streamActive.Store(key, true)
}

func (s *StreamCoordinator) active(key string) bool {
	_, ok := s.streamActive.Load(key)
	return ok
}

func (s *StreamCoordinator) storeTombstone(key string, createdAt time.Time) {
	s.streamAuxiliaryTombstones.Store(key, createdAt)
}

func (s *StreamCoordinator) tombstoneExists(key string) bool {
	_, ok := s.streamAuxiliaryTombstones.Load(key)
	return ok
}

func (s *StreamCoordinator) activeToolFeedbackCount() int {
	if !s.hasToolFeedback() {
		return 0
	}
	return s.toolFeedback.ActiveCount()
}

func (s *StreamCoordinator) initializeToolFeedback(
	config ToolFeedbackAnimatorConfig,
	separateMessages bool,
) {
	s.toolFeedback = NewToolFeedbackCoordinator(config, separateMessages)
}

func (s *StreamCoordinator) hasToolFeedback() bool {
	return s != nil && s.toolFeedback != nil
}

func (s *StreamCoordinator) beginToolFeedbackTerminals(
	keys []string,
	scoped bool,
	transient bool,
) []*toolFeedbackTerminal {
	if !s.hasToolFeedback() {
		return nil
	}
	terminals := make([]*toolFeedbackTerminal, 0, len(keys))
	for _, key := range keys {
		if scoped && !transient {
			terminals = append(terminals, s.toolFeedback.BeginTerminal(key))
		} else {
			terminals = append(terminals, s.toolFeedback.BeginTransientTerminal(key))
		}
	}
	return terminals
}

func (s *StreamCoordinator) completeToolFeedbackTerminals(
	ctx context.Context,
	terminals []*toolFeedbackTerminal,
	success bool,
) {
	if !s.hasToolFeedback() {
		return
	}
	for _, terminal := range terminals {
		s.toolFeedback.CompleteTerminal(ctx, terminal, success)
	}
}

func (s *StreamCoordinator) deliverToolFeedback(
	ctx context.Context,
	key, chatID, content string,
	operations toolFeedbackOperations,
	send func(context.Context, string) (toolFeedbackSendResult, error),
) ([]string, error) {
	if !s.hasToolFeedback() {
		result, err := send(ctx, content)
		return result.messageIDs, err
	}
	return s.toolFeedback.deliver(ctx, key, chatID, content, operations, send)
}

func (s *StreamCoordinator) dismissToolFeedback(
	ctx context.Context,
	keys []string,
	scoped bool,
) {
	if !s.hasToolFeedback() {
		return
	}
	for _, key := range keys {
		if scoped {
			s.toolFeedback.Dismiss(ctx, key)
		} else {
			s.toolFeedback.DismissTransient(ctx, key)
		}
	}
}

func (s *StreamCoordinator) pauseToolFeedback(
	_ context.Context,
	keys []string,
	_ bool,
) {
	if !s.hasToolFeedback() {
		return
	}
	for _, key := range keys {
		s.toolFeedback.Pause(key)
	}
}

func (s *StreamCoordinator) singleActiveScopedToolFeedbackKey(base string) (string, bool) {
	if !s.hasToolFeedback() {
		return "", false
	}
	return s.toolFeedback.singleActiveScopedKey(base)
}

func (s *StreamCoordinator) releaseToolFeedbackTerminals(keys []string) {
	if !s.hasToolFeedback() {
		return
	}
	for _, key := range keys {
		s.toolFeedback.ReleaseTerminal(key)
	}
}

func (s *StreamCoordinator) stopToolFeedback() {
	if s.hasToolFeedback() {
		s.toolFeedback.StopAll()
	}
}

func (s *StreamCoordinator) retireToolFeedbackChannel(ctx context.Context, channel string) {
	if s.hasToolFeedback() {
		s.toolFeedback.RetireChannel(ctx, channel)
	}
}

func (s *StreamCoordinator) configureToolFeedback(
	config ToolFeedbackAnimatorConfig,
	separateMessages bool,
) {
	if s.hasToolFeedback() {
		s.toolFeedback.Configure(config, separateMessages)
	}
}

func (s *StreamCoordinator) expireInteractions(now time.Time) {
	s.typingStops.Range(func(key, value any) bool {
		if entry, ok := value.(typingEntry); ok && now.Sub(entry.createdAt) > typingStopTTL {
			if _, loaded := s.typingStops.LoadAndDelete(key); loaded {
				entry.stop()
			}
		}
		return true
	})
	s.reactionUndos.Range(func(key, value any) bool {
		if entry, ok := value.(reactionEntry); ok && now.Sub(entry.createdAt) > typingStopTTL {
			if _, loaded := s.reactionUndos.LoadAndDelete(key); loaded {
				entry.undo()
			}
		}
		return true
	})
	s.placeholders.Range(func(key, value any) bool {
		if entry, ok := value.(placeholderEntry); ok && now.Sub(entry.createdAt) > placeholderTTL {
			s.placeholders.Delete(key)
		}
		return true
	})
}

func (s *StreamCoordinator) activeKey(
	channel, chatID, sessionKey string,
	traceScope runtimeevents.TraceScope,
) (string, bool) {
	key := streamSuppressionKey(channel, chatID, sessionKey, traceScope)
	if _, active := s.streamActive.Load(key); active {
		return key, true
	}
	if traceScope.Complete() {
		return "", false
	}
	return singleScopedStreamStateKey(
		&s.streamActive,
		streamSuppressionBaseKey(channel, chatID, sessionKey),
		nil,
	)
}

func (s *StreamCoordinator) consumeActive(key string) bool {
	_, loaded := s.streamActive.LoadAndDelete(key)
	return loaded
}

func (s *StreamCoordinator) clear(key string) {
	s.streamActive.Delete(key)
	s.streamAuxiliaryTombstones.Delete(key)
}

func (s *StreamCoordinator) markFinalized(key string, now time.Time) {
	s.streamActive.Store(key, true)
	s.streamAuxiliaryTombstones.Store(key, now)
}

func (s *StreamCoordinator) clearTombstone(key string) {
	s.streamAuxiliaryTombstones.Delete(key)
}

func (s *StreamCoordinator) tombstoneActive(key string, now time.Time) bool {
	value, ok := s.streamAuxiliaryTombstones.Load(key)
	if !ok {
		return false
	}
	createdAt, ok := value.(time.Time)
	if !ok || now.Sub(createdAt) > streamAuxiliaryTombstoneTTL {
		s.streamAuxiliaryTombstones.Delete(key)
		return false
	}
	return true
}

func (s *StreamCoordinator) tombstoneActiveForMessage(
	channel, chatID, sessionKey string,
	traceScope runtimeevents.TraceScope,
	now time.Time,
) bool {
	key := streamSuppressionKey(channel, chatID, sessionKey, traceScope)
	if s.tombstoneActive(key, now) {
		return true
	}
	if traceScope.Complete() {
		return false
	}
	key, ok := singleScopedStreamStateKey(
		&s.streamAuxiliaryTombstones,
		streamSuppressionBaseKey(channel, chatID, sessionKey),
		func(key string, value any) bool {
			createdAt, ok := value.(time.Time)
			if !ok || now.Sub(createdAt) > streamAuxiliaryTombstoneTTL {
				s.streamAuxiliaryTombstones.Delete(key)
				return false
			}
			return true
		},
	)
	return ok && s.tombstoneActive(key, now)
}

func (s *StreamCoordinator) activeForChat(channel, chatID string) bool {
	chatKey := streamSuppressionBaseKey(channel, chatID, "")
	found := false
	s.streamActive.Range(func(key, _ any) bool {
		keyString, ok := key.(string)
		if !ok {
			return true
		}
		if keyString == chatKey || strings.HasPrefix(keyString, chatKey+":") ||
			strings.HasPrefix(keyString, chatKey+"\x00turn\x00") {
			found = true
			return false
		}
		return true
	})
	return found
}

func (s *StreamCoordinator) expireStreams(now time.Time) {
	s.streamAuxiliaryTombstones.Range(func(key, value any) bool {
		if createdAt, ok := value.(time.Time); !ok || now.Sub(createdAt) > streamAuxiliaryTombstoneTTL {
			s.streamAuxiliaryTombstones.Delete(key)
		}
		return true
	})
}

func singleScopedStreamStateKey(
	state *sync.Map,
	baseKey string,
	valid func(string, any) bool,
) (string, bool) {
	prefix := baseKey + "\x00turn\x00"
	matched := ""
	ambiguous := false
	state.Range(func(key, value any) bool {
		keyString, ok := key.(string)
		if !ok || !strings.HasPrefix(keyString, prefix) {
			return true
		}
		if valid != nil && !valid(keyString, value) {
			return true
		}
		if matched != "" {
			ambiguous = true
			return false
		}
		matched = keyString
		return true
	})
	return matched, matched != "" && !ambiguous
}
