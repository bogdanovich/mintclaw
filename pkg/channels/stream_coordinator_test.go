package channels

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
)

type streamCoordinatorTestHost struct {
	channel        Channel
	splitOnMarker  bool
	footerEnabled  bool
	dismissedCount atomic.Int32
}

func (h *streamCoordinatorTestHost) deliveryChannel(name string) (Channel, bool) {
	return h.channel, name == "test" && h.channel != nil
}

func (h *streamCoordinatorTestHost) dismissToolFeedbackTargets(
	context.Context,
	string,
	Channel,
	string,
	*bus.InboundContext,
	string,
	[]runtimeevents.TraceScope,
) {
	h.dismissedCount.Add(1)
}

func (h *streamCoordinatorTestHost) streamSplitOnMarker() bool {
	return h.splitOnMarker
}

func (h *streamCoordinatorTestHost) streamResponseFooterEnabled() bool {
	return h.footerEnabled
}

func TestStreamCoordinatorExpireInteractions(t *testing.T) {
	var expiredStops atomic.Int32
	var currentStops atomic.Int32
	state := StreamCoordinator{}
	now := time.Now()

	state.typingStops.Store("expired-typing", typingEntry{
		stop:      func() { expiredStops.Add(1) },
		createdAt: now.Add(-typingStopTTL - time.Second),
	})
	state.typingStops.Store("current-typing", typingEntry{
		stop:      func() { currentStops.Add(1) },
		createdAt: now,
	})
	state.reactionUndos.Store("expired-reaction", reactionEntry{
		undo:      func() { expiredStops.Add(1) },
		createdAt: now.Add(-typingStopTTL - time.Second),
	})
	state.reactionUndos.Store("current-reaction", reactionEntry{
		undo:      func() { currentStops.Add(1) },
		createdAt: now,
	})
	state.placeholders.Store("expired-placeholder", placeholderEntry{
		id:        "old",
		createdAt: now.Add(-placeholderTTL - time.Second),
	})
	state.placeholders.Store("current-placeholder", placeholderEntry{
		id:        "current",
		createdAt: now,
	})

	state.expireInteractions(now)

	if expiredStops.Load() != 2 {
		t.Fatalf("expired callbacks = %d, want 2", expiredStops.Load())
	}
	if currentStops.Load() != 0 {
		t.Fatalf("current callbacks = %d, want 0", currentStops.Load())
	}
	for _, key := range []string{"expired-typing", "expired-reaction", "expired-placeholder"} {
		if _, ok := stateEntry(&state, key); ok {
			t.Fatalf("expired entry %q was not removed", key)
		}
	}
	for _, key := range []string{"current-typing", "current-reaction", "current-placeholder"} {
		if _, ok := stateEntry(&state, key); !ok {
			t.Fatalf("current entry %q was removed", key)
		}
	}
}

func TestStreamCoordinatorOwnsToolFeedbackCoordinator(t *testing.T) {
	state := StreamCoordinator{}
	if state.hasToolFeedback() {
		t.Fatal("zero interaction state unexpectedly has tool feedback")
	}
	if terminals := state.beginToolFeedbackTerminals([]string{"test:chat-1"}, true, false); terminals != nil {
		t.Fatalf("zero-state terminals = %+v, want nil", terminals)
	}

	state.initializeToolFeedback(
		ToolFeedbackAnimatorConfig{AnimationInterval: time.Hour},
		false,
	)
	t.Cleanup(state.stopToolFeedback)
	if !state.hasToolFeedback() {
		t.Fatal("initialized interaction state has no tool feedback")
	}

	terminals := state.beginToolFeedbackTerminals([]string{"test:chat-1"}, true, false)
	if len(terminals) != 1 || terminals[0] == nil {
		t.Fatalf("beginToolFeedbackTerminals() = %+v", terminals)
	}
	state.completeToolFeedbackTerminals(context.Background(), terminals, false)
	state.configureToolFeedback(
		ToolFeedbackAnimatorConfig{AnimationInterval: time.Hour},
		true,
	)
	if !state.toolFeedback.separateMessages() {
		t.Fatal("configureToolFeedback() did not update separate-message mode")
	}
}

func TestStreamCoordinatorToolFeedbackFallback(t *testing.T) {
	state := StreamCoordinator{}
	messageIDs, err := state.deliverToolFeedback(
		context.Background(),
		"test:chat-1",
		"chat-1",
		"working",
		toolFeedbackOperations{},
		func(_ context.Context, content string) (toolFeedbackSendResult, error) {
			if content != "working" {
				t.Fatalf("fallback content = %q, want working", content)
			}
			return toolFeedbackSendResult{messageIDs: []string{"message-1"}}, nil
		},
	)
	if err != nil || len(messageIDs) != 1 || messageIDs[0] != "message-1" {
		t.Fatalf("fallback delivery = (%v, %v)", messageIDs, err)
	}
}

func TestStreamCoordinatorExpireStreams(t *testing.T) {
	state := StreamCoordinator{}
	now := time.Now()
	state.streamActive.Store("active", true)
	state.streamAuxiliaryTombstones.Store(
		"expired",
		now.Add(-streamAuxiliaryTombstoneTTL-time.Second),
	)
	state.streamAuxiliaryTombstones.Store("current", now)
	state.streamAuxiliaryTombstones.Store("malformed", "not-a-time")

	state.expireStreams(now)

	if _, ok := state.streamActive.Load("active"); !ok {
		t.Fatal("stream activity must not be TTL-evicted with tombstones")
	}
	if _, ok := state.streamAuxiliaryTombstones.Load("current"); !ok {
		t.Fatal("current tombstone was removed")
	}
	for _, key := range []string{"expired", "malformed"} {
		if _, ok := state.streamAuxiliaryTombstones.Load(key); ok {
			t.Fatalf("stale tombstone %q was not removed", key)
		}
	}
}

func TestStreamCoordinatorFinalizationLifecycle(t *testing.T) {
	state := StreamCoordinator{}
	now := time.Now()
	traceScope := runtimeevents.NewTraceScope("/workspace/main", "turn-1")
	key := streamSuppressionKey("test", "chat-1", "session-1", traceScope)

	state.markFinalized(key, now)

	activeKey, active := state.activeKey("test", "chat-1", "session-1", traceScope)
	if !active || activeKey != key {
		t.Fatalf("activeKey() = (%q, %v), want (%q, true)", activeKey, active, key)
	}
	if !state.activeForChat("test", "chat-1") {
		t.Fatal("activeForChat() = false, want true")
	}
	if !state.tombstoneActiveForMessage("test", "chat-1", "session-1", traceScope, now) {
		t.Fatal("tombstoneActiveForMessage() = false, want true")
	}
	if !state.consumeActive(key) {
		t.Fatal("consumeActive() = false, want true")
	}
	if _, active := state.activeKey("test", "chat-1", "session-1", traceScope); active {
		t.Fatal("activeKey() remained active after consume")
	}
	if !state.tombstoneActive(key, now) {
		t.Fatal("consumeActive() must preserve the auxiliary tombstone")
	}

	state.clearTombstone(key)
	if state.tombstoneActive(key, now) {
		t.Fatal("clearTombstone() left tombstone active")
	}
}

func TestStreamCoordinatorOwnsStreamerFinalization(t *testing.T) {
	state := newStreamCoordinator()
	underlying := &mockStreamer{}
	host := &streamCoordinatorTestHost{
		channel: &mockStreamingChannel{streamer: underlying},
	}
	traceScope := runtimeevents.NewTraceScope("/workspace/main", "turn-1")

	streamer, ok := state.getStreamer(
		context.Background(), host, "test", "chat-1", "session-1", "request-1", traceScope,
	)
	if !ok {
		t.Fatal("getStreamer() unavailable")
	}
	if err := streamer.Finalize(context.Background(), "done"); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}

	key := streamSuppressionKey("test", "chat-1", "session-1", traceScope)
	if !state.active(key) || !state.tombstoneExists(key) {
		t.Fatal("finalization did not record active stream and auxiliary tombstone")
	}
	if host.dismissedCount.Load() != 1 {
		t.Fatalf("tool feedback dismissals = %d, want 1", host.dismissedCount.Load())
	}
}

func TestStreamCoordinatorScopedFallbackAndClear(t *testing.T) {
	state := StreamCoordinator{}
	now := time.Now()
	traceScope := runtimeevents.NewTraceScope("/workspace/main", "turn-1")
	key := streamSuppressionKey("test", "chat-1", "", traceScope)
	state.markFinalized(key, now)

	activeKey, active := state.activeKey("test", "chat-1", "", runtimeevents.TraceScope{})
	if !active || activeKey != key {
		t.Fatalf("unscoped activeKey() = (%q, %v), want (%q, true)", activeKey, active, key)
	}
	if !state.tombstoneActiveForMessage(
		"test",
		"chat-1",
		"",
		runtimeevents.TraceScope{},
		now,
	) {
		t.Fatal("unscoped tombstone fallback did not find the only scoped turn")
	}

	state.clear(key)
	if state.activeForChat("test", "chat-1") || state.tombstoneActive(key, now) {
		t.Fatal("clear() left stream suppression state")
	}
}

func stateEntry(state *StreamCoordinator, key string) (any, bool) {
	if value, ok := state.typingStops.Load(key); ok {
		return value, true
	}
	if value, ok := state.reactionUndos.Load(key); ok {
		return value, true
	}
	return state.placeholders.Load(key)
}
