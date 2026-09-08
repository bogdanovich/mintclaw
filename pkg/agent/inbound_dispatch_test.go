package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

func newInboundDispatchTestLoop(t *testing.T) (*AgentLoop, func()) {
	t.Helper()
	al, _, _, _, cleanup := newTestAgentLoop(t) //nolint:dogsled
	return al, cleanup
}

func TestBuildInboundMessageTurn_ConstructsDispatchEnvelope(t *testing.T) {
	al, cleanup := newInboundDispatchTestLoop(t)
	defer cleanup()
	mediaRef := installInboundMediaRef(t, al)

	msg := bus.NormalizeInboundMessage(bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:          "telegram",
			ChatID:           "-100123",
			ChatType:         "group",
			SenderID:         "telegram:42",
			MessageID:        "msg-1",
			ReplyToMessageID: "reply-1",
		},
		Sender: bus.SenderInfo{
			DisplayName: "Anton",
		},
		Content: "hello",
		Media:   []string{mediaRef},
	})

	turn, err := al.buildInboundMessageTurn(context.Background(), msg)
	if err != nil {
		t.Fatalf("buildInboundMessageTurn() error = %v", err)
	}
	defer turn.Cleanup()

	if turn.Agent == nil {
		t.Fatal("Agent is nil")
	}
	if turn.SessionKey == "" {
		t.Fatal("SessionKey is empty")
	}
	if turn.ScopeKey != turn.SessionKey {
		t.Fatalf("ScopeKey = %q, SessionKey = %q, want equal", turn.ScopeKey, turn.SessionKey)
	}
	if turn.Options.Dispatch.RouteSessionKey != turn.SessionKey {
		t.Fatalf(
			"Dispatch.RouteSessionKey = %q, want %q",
			turn.Options.Dispatch.RouteSessionKey,
			turn.SessionKey,
		)
	}
	if turn.Options.Dispatch.SessionKey != turn.SessionKey {
		t.Fatalf("Dispatch.SessionKey = %q, want %q", turn.Options.Dispatch.SessionKey, turn.SessionKey)
	}
	if turn.Options.Dispatch.Channel() != "telegram" || turn.Options.Dispatch.ChatID() != "-100123" {
		t.Fatalf(
			"dispatch addressing = (%q,%q), want (telegram,-100123)",
			turn.Options.Dispatch.Channel(),
			turn.Options.Dispatch.ChatID(),
		)
	}
	if turn.Options.Dispatch.MessageID() != "msg-1" ||
		turn.Options.Dispatch.ReplyToMessageID() != "reply-1" {
		t.Fatalf(
			"dispatch message ids = (%q,%q), want (msg-1,reply-1)",
			turn.Options.Dispatch.MessageID(),
			turn.Options.Dispatch.ReplyToMessageID(),
		)
	}
	if turn.Options.Dispatch.UserMessage != "hello" {
		t.Fatalf("Dispatch.UserMessage = %q, want hello", turn.Options.Dispatch.UserMessage)
	}
	if len(turn.Options.Dispatch.Media) != 1 || turn.Options.Dispatch.Media[0] != mediaRef {
		t.Fatalf("Dispatch.Media = %v, want [%s]", turn.Options.Dispatch.Media, mediaRef)
	}
	if turn.Options.Dispatch.SenderID() != "telegram:42" || turn.Options.SenderDisplayName != "Anton" {
		t.Fatalf(
			"sender fields = (%q,%q), want (telegram:42,Anton)",
			turn.Options.Dispatch.SenderID(),
			turn.Options.SenderDisplayName,
		)
	}
	if turn.Options.ModelBinding.RouteSessionKey != turn.Options.Dispatch.RouteSessionKey {
		t.Fatalf(
			"ModelBinding.RouteSessionKey = %q, want %q",
			turn.Options.ModelBinding.RouteSessionKey,
			turn.Options.Dispatch.RouteSessionKey,
		)
	}
	if turn.Options.ModelBinding.WorkspaceAgent != turn.Agent {
		t.Fatal("ModelBinding.WorkspaceAgent does not match routed agent")
	}
}

func TestBuildInboundMessageTurnRejectsOpaqueMediaWithoutStore(t *testing.T) {
	al, cleanup := newInboundDispatchTestLoop(t)
	defer cleanup()

	_, err := al.buildInboundMessageTurn(t.Context(), bus.InboundMessage{
		Context: bus.InboundContext{
			Channel: "telegram", ChatID: "chat-1", ChatType: "direct", SenderID: "telegram:42",
		},
		Content: "[media only]",
		Media:   []string{"media://unbound"},
	})
	if err == nil || !strings.Contains(err.Error(), "media store is unavailable") {
		t.Fatalf("buildInboundMessageTurn() error = %v, want unavailable media store", err)
	}
}

func TestBindInboundMediaOwnerAllowsMissingStoreWithoutOpaqueMedia(t *testing.T) {
	if err := bindInboundMediaOwnerForTarget(nil, nil, bus.InboundMessage{
		Media: []string{"https://example.invalid/document.pdf"},
	}); err != nil {
		t.Fatalf("non-opaque media admission error = %v", err)
	}
}

func installInboundMediaRef(t *testing.T, al *AgentLoop) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "inbound.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.7\ninbound\n%%EOF\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := media.NewFileMediaStore()
	ref, err := store.Store(path, media.MediaMeta{Filename: "inbound.pdf"}, "inbound")
	if err != nil {
		t.Fatal(err)
	}
	al.SetMediaStore(store)
	return ref
}

func TestBuildInboundMessageTurnIgnoresLegacySessionKey(t *testing.T) {
	al, cleanup := newInboundDispatchTestLoop(t)
	defer cleanup()

	legacySessionKey := "agent:main:manual-session"
	msg := bus.NormalizeInboundMessage(bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  "telegram",
			ChatID:   "chat-1",
			ChatType: "private",
			SenderID: "telegram:42",
		},
		Content:    "hello",
		SessionKey: legacySessionKey,
	})

	turn, err := al.buildInboundMessageTurn(context.Background(), msg)
	if err != nil {
		t.Fatalf("buildInboundMessageTurn() error = %v", err)
	}
	defer turn.Cleanup()

	if turn.SessionKey == legacySessionKey || !session.IsOpaqueSessionKey(turn.SessionKey) {
		t.Fatalf("SessionKey = %q, want current opaque route key", turn.SessionKey)
	}
	if turn.Options.Dispatch.SessionKey != turn.Options.Dispatch.RouteSessionKey {
		t.Fatalf(
			"Dispatch.SessionKey = %q, route = %q",
			turn.Options.Dispatch.SessionKey,
			turn.Options.Dispatch.RouteSessionKey,
		)
	}
}

func TestBuildInboundMessageTurnPreservesCurrentExplicitSessionKey(t *testing.T) {
	al, cleanup := newInboundDispatchTestLoop(t)
	defer cleanup()

	explicitSessionKey := session.BuildOpaqueSessionKey("explicit|session=manual")
	msg := bus.NormalizeInboundMessage(bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  "telegram",
			ChatID:   "chat-1",
			ChatType: "direct",
			SenderID: "telegram:42",
		},
		Content:    "hello",
		SessionKey: explicitSessionKey,
	})

	turn, err := al.buildInboundMessageTurn(context.Background(), msg)
	if err != nil {
		t.Fatalf("buildInboundMessageTurn() error = %v", err)
	}
	defer turn.Cleanup()

	if turn.SessionKey != explicitSessionKey {
		t.Fatalf("SessionKey = %q, want %q", turn.SessionKey, explicitSessionKey)
	}
	if turn.Options.Dispatch.SessionKey != explicitSessionKey {
		t.Fatalf("Dispatch.SessionKey = %q, want %q", turn.Options.Dispatch.SessionKey, explicitSessionKey)
	}
	if turn.Options.Dispatch.RouteSessionKey == explicitSessionKey {
		t.Fatalf("RouteSessionKey = explicit key %q, want routed session key", explicitSessionKey)
	}
}

func TestBuildInboundMessageTurn_UsesSessionOverrideAsEffectiveSession(t *testing.T) {
	al, cleanup := newInboundDispatchTestLoop(t)
	defer cleanup()

	msg := bus.NormalizeInboundMessage(bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  "telegram",
			ChatID:   "chat-1",
			ChatType: "private",
			SenderID: "telegram:42",
		},
		Content: "hello",
	})

	route, _, err := al.resolveMessageRoute(msg)
	if err != nil {
		t.Fatalf("resolveMessageRoute() error = %v", err)
	}
	allocation := al.allocateRouteSession(route, msg)
	overrideKey := session.BuildMainSessionKey("manual")
	if setErr := al.setSessionOverride(allocation.SessionKey, overrideKey); setErr != nil {
		t.Fatalf("setSessionOverride() error = %v", setErr)
	}

	turn, err := al.buildInboundMessageTurn(context.Background(), msg)
	if err != nil {
		t.Fatalf("buildInboundMessageTurn() error = %v", err)
	}
	defer turn.Cleanup()

	if turn.Options.Dispatch.RouteSessionKey != allocation.SessionKey {
		t.Fatalf(
			"RouteSessionKey = %q, want %q",
			turn.Options.Dispatch.RouteSessionKey,
			allocation.SessionKey,
		)
	}
	if turn.SessionKey != overrideKey {
		t.Fatalf("SessionKey = %q, want override %q", turn.SessionKey, overrideKey)
	}
}

func TestBuildInboundMessageTurn_PreparesInboundMessage(t *testing.T) {
	al, cleanup := newInboundDispatchTestLoop(t)
	defer cleanup()

	msg := bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  "telegram",
			ChatID:   "chat-1",
			ChatType: "private",
			SenderID: "telegram:42",
		},
		Content: "hello",
	}

	turn, err := al.buildInboundMessageTurn(context.Background(), msg)
	if err != nil {
		t.Fatalf("buildInboundMessageTurn() error = %v", err)
	}
	defer turn.Cleanup()

	if turn.Message.Context.Channel != "telegram" || turn.Message.Context.ChatID != "chat-1" {
		t.Fatalf(
			"prepared context = (%q,%q), want (telegram,chat-1)",
			turn.Message.Context.Channel,
			turn.Message.Context.ChatID,
		)
	}
	if turn.Options.Dispatch.Channel() != "telegram" || turn.Options.Dispatch.ChatID() != "chat-1" {
		t.Fatalf(
			"dispatch addressing = (%q,%q), want prepared context",
			turn.Options.Dispatch.Channel(),
			turn.Options.Dispatch.ChatID(),
		)
	}
	if turn.Message.Context.ReceivedAt.IsZero() {
		t.Fatal("prepared message is missing received_at")
	}
	if turn.Options.Dispatch.InboundRelation().Kind != bus.InboundRelationStandalone {
		t.Fatalf("relation = %#v, want standalone", turn.Options.Dispatch.InboundRelation())
	}
}

func TestBuildInboundMessageTurnPersistsEventTimeRelationForReplay(t *testing.T) {
	al, _, msgBus, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	mediaRef := installInboundMediaRef(t, al)
	spool, err := bus.NewInboundSpool(t.TempDir())
	if err != nil {
		t.Fatalf("NewInboundSpool failed: %v", err)
	}
	msgBus.SetInboundSpool(spool)

	previousReceivedAt := time.Date(2026, 9, 6, 20, 0, 0, 0, time.UTC)
	currentReceivedAt := previousReceivedAt.Add(time.Minute)
	msg := bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:    "telegram",
			ChatID:     "chat-1",
			ChatType:   "direct",
			SenderID:   "telegram:42",
			ReceivedAt: currentReceivedAt,
		},
		Content: "[media only]",
		Media:   []string{mediaRef},
	}
	target, err := al.resolveInboundDispatchTarget(msg)
	if err != nil {
		t.Fatalf("resolveInboundDispatchTarget() error = %v", err)
	}
	target.Agent.Sessions.AddFullMessage(target.SessionKey, providers.Message{
		Role:      "user",
		Content:   "Here is what I ate",
		CreatedAt: &previousReceivedAt,
	})
	if err = msgBus.PublishInbound(t.Context(), msg); err != nil {
		t.Fatalf("PublishInbound failed: %v", err)
	}
	spooled := <-msgBus.InboundChan()

	turn, err := al.buildInboundMessageTurn(t.Context(), spooled)
	if err != nil {
		t.Fatalf("buildInboundMessageTurn() error = %v", err)
	}
	defer turn.Cleanup()
	if turn.Options.Dispatch.InboundRelation().Kind != bus.InboundRelationAdjacentFollowupMedia {
		t.Fatalf("turn relation = %#v, want adjacent media follow-up", turn.Options.Dispatch.InboundRelation())
	}

	pending, err := msgBus.PendingInboundSpool(t.Context())
	if err != nil {
		t.Fatalf("PendingInboundSpool failed: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending = %d, want 1", len(pending))
	}
	if !pending[0].Context.ReceivedAt.Equal(currentReceivedAt) ||
		pending[0].Context.Relation.Kind != bus.InboundRelationAdjacentFollowupMedia {
		t.Fatalf("persisted facts = %#v, want event time and adjacent media relation", pending[0].Context)
	}
}

func TestPendingRelationRootDoesNotClassifyItselfAsFollowup(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	receivedAt := time.Date(2026, 9, 7, 6, 59, 0, 0, time.UTC)
	msg := bus.InboundMessage{
		SpoolID: "spool-root",
		Context: bus.InboundContext{
			Channel:    "telegram",
			ChatID:     "chat-1",
			ChatType:   "direct",
			SenderID:   "telegram:42",
			MessageID:  "root-1",
			ReceivedAt: receivedAt,
		},
		Content: "[media only]",
		Media:   []string{"media://image-1"},
	}
	target, err := al.resolveInboundDispatchTarget(msg)
	if err != nil {
		t.Fatalf("resolveInboundDispatchTarget() error = %v", err)
	}
	prepared, err := al.prepareInboundMessageForTarget(
		t.Context(),
		msg,
		targetWithInboundRelationRoot(target, msg),
	)
	if err != nil {
		t.Fatalf("prepareInboundMessageForTarget() error = %v", err)
	}
	if prepared.Context.Relation.Kind != bus.InboundRelationStandalone ||
		!prepared.Context.Relation.MediaOnly {
		t.Fatalf("root relation = %#v, want standalone media", prepared.Context.Relation)
	}
}

func TestPendingRelationRootStopsAfterCanonicalRootAppears(t *testing.T) {
	rootAt := time.Date(2026, 9, 7, 7, 0, 0, 0, time.UTC)
	assistantAt := rootAt.Add(30 * time.Second)
	followAt := rootAt.Add(time.Minute)
	target := &inboundDispatchTarget{
		relationRoot: &inboundRelationRoot{SpoolID: "spool-root", ReceivedAt: rootAt},
	}
	history := []providers.Message{
		{Role: "user", Content: "Here is what I ate", CreatedAt: &rootAt, RootTurnStart: true},
		{Role: "assistant", Content: "Saved.", CreatedAt: &assistantAt},
	}
	withRoot := historyWithPendingRelationRoot(history, target, bus.InboundMessage{
		SpoolID: "spool-followup",
		Context: bus.InboundContext{
			ReceivedAt: followAt,
		},
	})
	if len(withRoot) != len(history) {
		t.Fatalf("history length = %d, want canonical history length %d", len(withRoot), len(history))
	}
	relation := classifyPromptCurrentMessageRelation(
		"[media only]",
		[]string{"media://image-1"},
		"",
		true,
		withRoot,
		followAt,
	)
	if relation.Kind != bus.InboundRelationStandalone {
		t.Fatalf("relation after assistant = %#v, want standalone", relation)
	}
}

func TestProcessMessagePersistsInboundReceivedAtAsRootTimestamp(t *testing.T) {
	al, cleanup := newInboundDispatchTestLoop(t)
	defer cleanup()
	receivedAt := time.Date(2026, 9, 6, 21, 15, 0, 0, time.UTC)
	msg := bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:    "telegram",
			ChatID:     "chat-1",
			ChatType:   "direct",
			SenderID:   "telegram:42",
			ReceivedAt: receivedAt,
		},
		Content: "remember event time",
	}
	target, err := al.resolveInboundDispatchTarget(msg)
	if err != nil {
		t.Fatalf("resolveInboundDispatchTarget() error = %v", err)
	}

	if _, err = al.processMessage(t.Context(), msg); err != nil {
		t.Fatalf("processMessage() error = %v", err)
	}
	history := target.Agent.Sessions.GetHistory(target.SessionKey)
	if len(history) == 0 || !history[0].RootTurnStart || history[0].CreatedAt == nil {
		t.Fatalf("root history = %#v, want timestamped root user message", history)
	}
	if !history[0].CreatedAt.Equal(receivedAt) {
		t.Fatalf("root CreatedAt = %v, want %v", history[0].CreatedAt, receivedAt)
	}
}

func TestBuildInboundMessageTurn_RotatesEpochWithoutChangingRouteScope(t *testing.T) {
	al, cfg, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	cfg.Session.Lifecycle = &config.SessionLifecycleConfig{
		Strategy: "calendar",
		Period:   "day",
		Timezone: "America/Los_Angeles",
	}
	msg := bus.NormalizeInboundMessage(bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  "telegram",
			ChatID:   "chat-1",
			ChatType: "direct",
			SenderID: "telegram:42",
		},
		Content: "hello",
	})

	al.sessionNow = func() time.Time {
		return time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	}
	first, err := al.buildInboundMessageTurn(context.Background(), msg)
	if err != nil {
		t.Fatalf("first buildInboundMessageTurn() error = %v", err)
	}
	defer first.Cleanup()

	al.sessionNow = func() time.Time {
		return time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	}
	second, err := al.buildInboundMessageTurn(context.Background(), msg)
	if err != nil {
		t.Fatalf("second buildInboundMessageTurn() error = %v", err)
	}
	defer second.Cleanup()

	if first.Options.Dispatch.RouteSessionKey != second.Options.Dispatch.RouteSessionKey {
		t.Fatal("stable route scope changed across lifecycle epochs")
	}
	if first.SessionKey == second.SessionKey {
		t.Fatal("effective session did not rotate across calendar days")
	}
	if first.Options.Dispatch.SessionScope.Epoch == nil || second.Options.Dispatch.SessionScope.Epoch == nil {
		t.Fatal("session epoch provenance is missing")
	}
}

func TestBuildInboundMessageTurn_SessionResetDoesNotCrossEpoch(t *testing.T) {
	al, cfg, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	cfg.Session.Lifecycle = &config.SessionLifecycleConfig{
		Strategy: "calendar",
		Period:   "day",
		Timezone: "UTC",
	}
	msg := bus.NormalizeInboundMessage(bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  "telegram",
			ChatID:   "chat-1",
			ChatType: "direct",
			SenderID: "telegram:42",
		},
		Content: "hello",
	})

	al.sessionNow = func() time.Time {
		return time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	}
	firstTarget, err := al.resolveInboundDispatchTarget(msg)
	if err != nil {
		t.Fatalf("resolveInboundDispatchTarget() error = %v", err)
	}
	resetKey := session.BuildMainSessionKey("reset")
	if setErr := al.setSessionOverride(firstTarget.Allocation.SessionKey, resetKey); setErr != nil {
		t.Fatalf("setSessionOverride() error = %v", setErr)
	}
	withReset, err := al.resolveInboundDispatchTarget(msg)
	if err != nil {
		t.Fatalf("resolveInboundDispatchTarget() error = %v", err)
	}
	if withReset.SessionKey != resetKey {
		t.Fatalf("session key = %q, want reset key %q", withReset.SessionKey, resetKey)
	}

	al.sessionNow = func() time.Time {
		return time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	}
	nextDay, err := al.resolveInboundDispatchTarget(msg)
	if err != nil {
		t.Fatalf("resolveInboundDispatchTarget() error = %v", err)
	}
	if nextDay.SessionKey == resetKey {
		t.Fatal("manual session reset leaked into the next lifecycle epoch")
	}
}
