package bus

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
)

func TestPublishConsume(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	ctx := context.Background()

	msg := InboundMessage{
		Context: InboundContext{
			Channel:  "test",
			ChatID:   "chat1",
			ChatType: "direct",
			SenderID: "user1",
		},
		Content: "hello",
	}

	if err := mb.PublishInbound(ctx, msg); err != nil {
		t.Fatalf("PublishInbound failed: %v", err)
	}

	got, ok := <-mb.InboundChan()
	if !ok {
		t.Fatal("ConsumeInbound returned ok=false")
	}
	if got.Content != "hello" {
		t.Fatalf("expected content 'hello', got %q", got.Content)
	}
	if got.Context.Channel != "test" {
		t.Fatalf("expected context channel 'test', got %q", got.Context.Channel)
	}
	if got.Context.ChatID != "chat1" {
		t.Fatalf("expected context chat ID 'chat1', got %q", got.Context.ChatID)
	}
	if got.Context.SenderID != "user1" {
		t.Fatalf("expected context sender ID 'user1', got %q", got.Context.SenderID)
	}
}

func TestPublishInbound_NormalizesContext(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()
	optionIndex := 1

	msg := InboundMessage{
		Context: InboundContext{
			Channel:          "slack",
			Account:          "workspace-a",
			ChatID:           "C456/1712",
			ChatType:         "group",
			TopicID:          "1712",
			SpaceID:          "T001",
			SpaceType:        "team",
			SenderID:         "U123",
			MessageID:        "1712.01",
			ReplyToMessageID: "1700.01",
			Mentioned:        true,
			MediaGroup: InboundMediaGroup{
				ID:         " album-1 ",
				MessageIDs: []string{" 1 ", "2"},
			},
			Interaction: InboundInteractionProjection{
				Choice: InboundInteractionChoiceAllowOnce, Response: " Allow once ",
				ShortID: " abc12345 ", OptionIndex: &optionIndex,
			},
			Raw: map[string]string{
				legacyInboundInteractionResponseKey: "legacy response",
				"transport":                         "test",
			},
		},
		Content: "hello",
	}

	if err := mb.PublishInbound(context.Background(), msg); err != nil {
		t.Fatalf("PublishInbound failed: %v", err)
	}
	msg.Context.MediaGroup.MessageIDs[0] = "mutated"
	*msg.Context.Interaction.OptionIndex = 9

	got := <-mb.InboundChan()
	if got.Context.Channel != "slack" {
		t.Fatalf("expected context channel slack, got %q", got.Context.Channel)
	}
	if got.Context.Account != "workspace-a" {
		t.Fatalf("expected context account workspace-a, got %q", got.Context.Account)
	}
	if got.Context.ChatType != "group" {
		t.Fatalf("expected context chat type group, got %q", got.Context.ChatType)
	}
	if got.Context.TopicID != "1712" {
		t.Fatalf("expected topic 1712, got %q", got.Context.TopicID)
	}
	if got.Context.SpaceType != "team" || got.Context.SpaceID != "T001" {
		t.Fatalf("expected team space T001, got %q/%q", got.Context.SpaceType, got.Context.SpaceID)
	}
	if !got.Context.Mentioned {
		t.Fatal("expected mentioned=true in context")
	}
	if got.Context.ReplyToMessageID != "1700.01" {
		t.Fatalf("expected reply_to_message_id 1700.01, got %q", got.Context.ReplyToMessageID)
	}
	if got.Context.MediaGroup.ID != "album-1" ||
		!slices.Equal(got.Context.MediaGroup.MessageIDs, []string{"1", "2"}) {
		t.Fatalf("expected normalized media group, got %#v", got.Context.MediaGroup)
	}
	if got.Context.Interaction.Choice != InboundInteractionChoiceAllowOnce ||
		got.Context.Interaction.Response != "Allow once" || got.Context.Interaction.ShortID != "abc12345" ||
		got.Context.Interaction.OptionIndex == nil || *got.Context.Interaction.OptionIndex != 1 {
		t.Fatalf("expected normalized interaction projection, got %#v", got.Context.Interaction)
	}
	if len(got.Context.Raw) != 1 || got.Context.Raw["transport"] != "test" {
		t.Fatalf("expected typed interaction to replace legacy raw keys, got %#v", got.Context.Raw)
	}
	if got.Context.ActorID != "U123" {
		t.Fatalf("expected actor_id to default to sender U123, got %q", got.Context.ActorID)
	}
	if got.Context.SourceRef != "slack:C456/1712:1712.01" {
		t.Fatalf("expected source_ref slack:C456/1712:1712.01, got %q", got.Context.SourceRef)
	}
}

func TestNormalizeInboundContextMigratesMintClawClientSessionID(t *testing.T) {
	tests := []struct {
		name            string
		context         InboundContext
		wantSessionID   string
		wantRawSession  string
		wantRawMetadata string
	}{
		{
			name: "legacy mintclaw metadata",
			context: InboundContext{
				Channel: " MINTCLAW ",
				Raw: map[string]string{
					legacyInboundClientSessionIDKey: " legacy-session ",
					"transport":                     "websocket",
				},
			},
			wantSessionID:   "legacy-session",
			wantRawMetadata: "websocket",
		},
		{
			name: "typed provenance wins",
			context: InboundContext{
				Channel:         "mintclaw",
				ClientSessionID: " typed-session ",
				Raw:             map[string]string{legacyInboundClientSessionIDKey: "stale-session"},
			},
			wantSessionID: "typed-session",
		},
		{
			name: "other channel retains adapter metadata",
			context: InboundContext{
				Channel: "telegram",
				Raw:     map[string]string{legacyInboundClientSessionIDKey: "adapter-session"},
			},
			wantRawSession: "adapter-session",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := NormalizeInboundContext(test.context)
			if got.ClientSessionID != test.wantSessionID {
				t.Fatalf("ClientSessionID = %q, want %q", got.ClientSessionID, test.wantSessionID)
			}
			if got.Raw[legacyInboundClientSessionIDKey] != test.wantRawSession {
				t.Fatalf(
					"legacy raw session = %q, want %q",
					got.Raw[legacyInboundClientSessionIDKey],
					test.wantRawSession,
				)
			}
			if got.Raw["transport"] != test.wantRawMetadata {
				t.Fatalf("transport metadata = %q, want %q", got.Raw["transport"], test.wantRawMetadata)
			}
		})
	}
}

func TestInboundPayloadJSONOwnsAddressingInContext(t *testing.T) {
	tests := map[string]any{
		"turn": InboundMessage{
			Context: InboundContext{
				Channel:   "telegram",
				ChatID:    "chat-1",
				SenderID:  "user-1",
				MessageID: "message-1",
			},
		},
		"observed": ObservedMessage{
			Context: InboundContext{
				Channel:   "telegram",
				ChatID:    "chat-1",
				SenderID:  "user-1",
				MessageID: "message-1",
			},
		},
	}

	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			var decoded map[string]any
			if err = json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			for _, field := range []string{"channel", "chat_id", "sender_id", "message_id"} {
				if _, ok := decoded[field]; ok {
					t.Fatalf("payload contains duplicate top-level %q: %s", field, encoded)
				}
			}
			contextPayload, ok := decoded["context"].(map[string]any)
			if !ok {
				t.Fatalf("context payload = %#v", decoded["context"])
			}
			if contextPayload["channel"] != "telegram" ||
				contextPayload["chat_id"] != "chat-1" ||
				contextPayload["sender_id"] != "user-1" ||
				contextPayload["message_id"] != "message-1" {
				t.Fatalf("unexpected context payload: %#v", contextPayload)
			}
			if _, exists := contextPayload["interaction"]; exists {
				t.Fatalf("zero interaction projection was serialized: %s", encoded)
			}
		})
	}
}

func TestPublishInbound_WithSpoolWritesAndAckRemovesEntry(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()
	spool, err := NewInboundSpool(t.TempDir())
	if err != nil {
		t.Fatalf("NewInboundSpool failed: %v", err)
	}
	mb.SetInboundSpool(spool)

	msg := InboundMessage{
		Context: InboundContext{
			Channel:  "telegram",
			ChatID:   "chat",
			SenderID: "user",
		},
		Content: "durable hello",
	}
	if err := mb.PublishInbound(context.Background(), msg); err != nil {
		t.Fatalf("PublishInbound failed: %v", err)
	}
	got := <-mb.InboundChan()
	if got.SpoolID == "" {
		t.Fatal("expected spooled inbound message to have SpoolID")
	}
	if got.Context.ReceivedAt.IsZero() {
		t.Fatal("expected received_at to be assigned before spooling")
	}
	if got.Content != "durable hello" {
		t.Fatalf("content = %q, want durable hello", got.Content)
	}
	if _, err := os.Stat(spool.processingPath(got.SpoolID)); err != nil {
		t.Fatalf("expected processing spool file: %v", err)
	}
	pending, err := spool.Pending(context.Background(), 0)
	if err != nil {
		t.Fatalf("Pending failed: %v", err)
	}
	if len(pending) != 1 || !pending[0].Context.ReceivedAt.Equal(got.Context.ReceivedAt) {
		t.Fatalf("pending received_at = %#v, want %v", pending, got.Context.ReceivedAt)
	}
	if err := mb.AckInbound(context.Background(), got); err != nil {
		t.Fatalf("AckInbound failed: %v", err)
	}
	if _, err := os.Stat(spool.processingPath(got.SpoolID)); !os.IsNotExist(err) {
		t.Fatalf("expected processing spool file removed, stat err=%v", err)
	}
}

func TestReplayInboundMessagesReplaysCapturedUnackedMessage(t *testing.T) {
	dir := t.TempDir()
	first := NewMessageBus()
	spool, err := NewInboundSpool(dir)
	if err != nil {
		t.Fatalf("NewInboundSpool failed: %v", err)
	}
	first.SetInboundSpool(spool)
	receivedAt := time.Date(2026, 9, 6, 17, 30, 0, 0, time.FixedZone("test", -7*60*60))
	wantReceivedAt := receivedAt.UTC()
	optionIndex := 0
	if publishErr := first.PublishInbound(context.Background(), InboundMessage{
		Context: InboundContext{
			Channel:         "slack",
			ChatID:          "chat",
			TopicID:         "topic-a",
			SenderID:        "user",
			ClientSessionID: "frontend-session-1",
			ReceivedAt:      receivedAt,
			Relation: InboundMessageRelation{
				Kind:      InboundRelationAdjacentFollowupMedia,
				MediaOnly: true,
			},
			MediaGroup: InboundMediaGroup{
				ID:         "album-1",
				MessageIDs: []string{"message-1", "message-2"},
			},
			Interaction: InboundInteractionProjection{
				Unresolved: true, ShortID: "abc12345",
				OptionIndex: &optionIndex, ResponseMessageID: "message-2",
			},
		},
		Content:    "before restart",
		SessionKey: "agent:main:slack:chat:topic-a",
	}); publishErr != nil {
		t.Fatalf("PublishInbound failed: %v", publishErr)
	}
	published := <-first.InboundChan()
	if published.SpoolID == "" {
		t.Fatal("expected published message to have SpoolID")
	}
	first.Close()

	second := NewMessageBus()
	defer second.Close()
	secondSpool, err := NewInboundSpool(dir)
	if err != nil {
		t.Fatalf("NewInboundSpool(second) failed: %v", err)
	}
	second.SetInboundSpool(secondSpool)
	pending, err := second.PendingInboundSpool(context.Background())
	if err != nil {
		t.Fatalf("PendingInboundSpool failed: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending = %d, want 1", len(pending))
	}
	if err := second.ReplayInboundMessages(context.Background(), pending); err != nil {
		t.Fatalf("ReplayInboundMessages failed: %v", err)
	}
	got := <-second.InboundChan()
	if got.SpoolID != published.SpoolID {
		t.Fatalf("spool id = %q, want %q", got.SpoolID, published.SpoolID)
	}
	if got.Content != "before restart" {
		t.Fatalf("content = %q, want before restart", got.Content)
	}
	if got.Context.TopicID != "topic-a" {
		t.Fatalf("topic id = %q, want topic-a", got.Context.TopicID)
	}
	if got.Context.ClientSessionID != "frontend-session-1" {
		t.Fatalf("client session ID = %q, want frontend-session-1", got.Context.ClientSessionID)
	}
	if got.SessionKey != "agent:main:slack:chat:topic-a" {
		t.Fatalf("session key = %q, want topic session", got.SessionKey)
	}
	if !got.Context.ReceivedAt.Equal(wantReceivedAt) {
		t.Fatalf("received_at = %v, want %v", got.Context.ReceivedAt, wantReceivedAt)
	}
	if got.Context.Relation.Kind != InboundRelationAdjacentFollowupMedia || !got.Context.Relation.MediaOnly {
		t.Fatalf("relation = %#v, want durable adjacent media relation", got.Context.Relation)
	}
	if got.Context.MediaGroup.ID != "album-1" ||
		!slices.Equal(got.Context.MediaGroup.MessageIDs, []string{"message-1", "message-2"}) {
		t.Fatalf("media group = %#v, want durable album membership", got.Context.MediaGroup)
	}
	if !got.Context.Interaction.Unresolved ||
		got.Context.Interaction.ShortID != "abc12345" || got.Context.Interaction.OptionIndex == nil ||
		*got.Context.Interaction.OptionIndex != 0 || got.Context.Interaction.ResponseMessageID != "message-2" {
		t.Fatalf("interaction = %#v, want durable callback projection", got.Context.Interaction)
	}
	if err := second.AckInbound(context.Background(), got); err != nil {
		t.Fatalf("AckInbound failed: %v", err)
	}
	if _, err := os.Stat(secondSpool.processingPath(got.SpoolID)); !os.IsNotExist(err) {
		t.Fatalf("expected replayed processing file removed, stat err=%v", err)
	}
}

func TestPersistInboundContextUpdatesOnlyDurableFacts(t *testing.T) {
	spool, err := NewInboundSpool(t.TempDir())
	if err != nil {
		t.Fatalf("NewInboundSpool failed: %v", err)
	}
	mb := NewMessageBus()
	defer mb.Close()
	mb.SetInboundSpool(spool)

	if err = mb.PublishInbound(t.Context(), InboundMessage{
		Context: InboundContext{Channel: "telegram", ChatID: "chat", ChatType: "direct", SenderID: "user"},
		Content: "[media only]",
		Media:   []string{"media://image-1"},
	}); err != nil {
		t.Fatalf("PublishInbound failed: %v", err)
	}
	msg := <-mb.InboundChan()
	if err = mb.ReleaseInbound(t.Context(), msg, errors.New("retry before classification")); err != nil {
		t.Fatalf("ReleaseInbound failed: %v", err)
	}
	msg.Content = "derived transcript must not replace raw content"
	msg.Context.Relation = InboundMessageRelation{
		Kind:      InboundRelationAdjacentFollowupMedia,
		MediaOnly: true,
	}
	if err = mb.PersistInboundContext(t.Context(), msg); err != nil {
		t.Fatalf("PersistInboundContext failed: %v", err)
	}

	pending, err := spool.Pending(t.Context(), 0)
	if err != nil {
		t.Fatalf("Pending failed: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending = %d, want 1", len(pending))
	}
	if pending[0].Content != "[media only]" {
		t.Fatalf("content = %q, want original raw content", pending[0].Content)
	}
	if pending[0].Context.Relation != msg.Context.Relation {
		t.Fatalf("relation = %#v, want %#v", pending[0].Context.Relation, msg.Context.Relation)
	}
}

func TestPendingLegacySpoolRecordHydratesContext(t *testing.T) {
	spool, err := NewInboundSpool(t.TempDir())
	if err != nil {
		t.Fatalf("NewInboundSpool failed: %v", err)
	}
	receivedAt := time.Date(2026, 9, 6, 23, 45, 0, 0, time.UTC)
	record := spooledInboundRecord{
		Version:    inboundSpoolVersion,
		ID:         "legacy-received-at",
		ReceivedAt: receivedAt,
		Message: InboundMessage{
			Context: InboundContext{
				Channel: "mintclaw", ChatID: "mintclaw:legacy-session", SenderID: "user",
				Raw: map[string]string{
					legacyInboundClientSessionIDKey:              " legacy-session ",
					legacyInboundInteractionResponseErrorKey:     " unresolved callback option ",
					legacyInboundInteractionShortIDKey:           " abc12345 ",
					legacyInboundInteractionOptionIndexKey:       "0",
					legacyInboundInteractionResponseMessageIDKey: "message-2",
					"transport": "legacy",
				},
			},
			Content: "legacy",
		},
	}
	if err = spool.writeRecord(spool.processingPath(record.ID), record); err != nil {
		t.Fatalf("writeRecord failed: %v", err)
	}

	pending, err := spool.Pending(t.Context(), 0)
	if err != nil {
		t.Fatalf("Pending failed: %v", err)
	}
	if len(pending) != 1 || !pending[0].Context.ReceivedAt.Equal(receivedAt) {
		t.Fatalf("pending received_at = %#v, want %v", pending, receivedAt)
	}
	if pending[0].Context.MediaGroup.ID != "" || len(pending[0].Context.MediaGroup.MessageIDs) != 0 {
		t.Fatalf("legacy media group = %#v, want zero value", pending[0].Context.MediaGroup)
	}
	if pending[0].Context.ClientSessionID != "legacy-session" {
		t.Fatalf("legacy client session ID = %q, want legacy-session", pending[0].Context.ClientSessionID)
	}
	projection := pending[0].Context.Interaction
	if !projection.Unresolved || projection.ShortID != "abc12345" ||
		projection.OptionIndex == nil || *projection.OptionIndex != 0 || projection.ResponseMessageID != "message-2" {
		t.Fatalf("legacy interaction projection = %#v, want migrated callback facts", projection)
	}
	if len(pending[0].Context.Raw) != 1 || pending[0].Context.Raw["transport"] != "legacy" {
		t.Fatalf("legacy raw metadata = %#v, want interaction keys removed", pending[0].Context.Raw)
	}
}

func TestMessageBusPublishesRuntimeFailureAndCloseEvents(t *testing.T) {
	eventBus := runtimeevents.NewBus()
	defer func() {
		if err := eventBus.Close(); err != nil {
			t.Errorf("event bus close failed: %v", err)
		}
	}()

	_, eventsCh, err := eventBus.Channel().OfKind(
		runtimeevents.KindBusPublishFailed,
		runtimeevents.KindBusCloseStarted,
		runtimeevents.KindBusCloseDrained,
		runtimeevents.KindBusCloseCompleted,
	).SubscribeChan(t.Context(), runtimeevents.SubscribeOptions{Name: "bus-events", Buffer: 4})
	if err != nil {
		t.Fatalf("SubscribeChan failed: %v", err)
	}

	mb := NewMessageBus()
	mb.SetEventPublisher(eventBus)

	if err := mb.PublishInbound(context.Background(), InboundMessage{}); err == nil {
		t.Fatal("expected PublishInbound to fail")
	}
	failed := receiveBusRuntimeEvent(t, eventsCh)
	if failed.Kind != runtimeevents.KindBusPublishFailed ||
		failed.Source.Name != "inbound" ||
		failed.Severity != runtimeevents.SeverityError {
		t.Fatalf("publish failed event = %+v", failed)
	}
	if failed.Attrs["stream"] != "inbound" || failed.Attrs["error"] == "" {
		t.Fatalf("publish failed attrs = %#v, want stream and error", failed.Attrs)
	}

	if err := mb.PublishOutbound(context.Background(), OutboundMessage{
		Context: NewOutboundContext("telegram", "chat-1", ""),
		Content: "queued",
	}); err != nil {
		t.Fatalf("PublishOutbound failed: %v", err)
	}
	mb.Close()

	seen := map[runtimeevents.Kind]bool{}
	var drainedAttrs map[string]any
	for range 3 {
		evt := receiveBusRuntimeEvent(t, eventsCh)
		seen[evt.Kind] = true
		if evt.Kind == runtimeevents.KindBusCloseDrained {
			drainedAttrs = evt.Attrs
		}
	}
	for _, kind := range []runtimeevents.Kind{
		runtimeevents.KindBusCloseStarted,
		runtimeevents.KindBusCloseDrained,
		runtimeevents.KindBusCloseCompleted,
	} {
		if !seen[kind] {
			t.Fatalf("missing %s event, seen=%v", kind, seen)
		}
	}
	if drainedAttrs["drained"] != 1 {
		t.Fatalf("bus close drained attrs = %#v, want drained count", drainedAttrs)
	}
}

func receiveBusRuntimeEvent(t *testing.T, ch <-chan runtimeevents.Event) runtimeevents.Event {
	t.Helper()

	select {
	case evt, ok := <-ch:
		if !ok {
			t.Fatal("runtime event channel closed before expected event")
		}
		return evt
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for runtime event")
		return runtimeevents.Event{}
	}
}

func TestPublishOutboundSubscribe(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	ctx := context.Background()

	msg := OutboundMessage{
		Context: InboundContext{
			Channel: "telegram",
			ChatID:  "123",
		},
		Content: "world",
	}

	if err := mb.PublishOutbound(ctx, msg); err != nil {
		t.Fatalf("PublishOutbound failed: %v", err)
	}

	got, ok := <-mb.OutboundChan()
	if !ok {
		t.Fatal("SubscribeOutbound returned ok=false")
	}
	if got.Content != "world" {
		t.Fatalf("expected content 'world', got %q", got.Content)
	}
	if got.Context.Channel != "telegram" || got.Context.ChatID != "123" {
		t.Fatalf("expected normalized outbound context, got %+v", got.Context)
	}
}

func TestPublishOutbound_MirrorsContextToLegacyFields(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	msg := OutboundMessage{
		Context: InboundContext{
			Channel:          "telegram",
			ChatID:           "chat-42",
			ReplyToMessageID: "msg-9",
		},
		AgentID:    "main",
		SessionKey: "sk_v1_123",
		Scope: &OutboundScope{
			Version:    1,
			AgentID:    "main",
			Channel:    "telegram",
			Account:    "bot-a",
			Dimensions: []string{"chat", "sender"},
			Values: map[string]string{
				"chat":   "direct:chat-42",
				"sender": "user-1",
			},
		},
		Content: "reply",
	}

	if err := mb.PublishOutbound(context.Background(), msg); err != nil {
		t.Fatalf("PublishOutbound failed: %v", err)
	}

	got := <-mb.OutboundChan()
	if got.Channel != "telegram" {
		t.Fatalf("expected legacy channel telegram, got %q", got.Channel)
	}
	if got.ChatID != "chat-42" {
		t.Fatalf("expected legacy chat ID chat-42, got %q", got.ChatID)
	}
	if got.ReplyToMessageID != "msg-9" {
		t.Fatalf("expected mirrored reply_to_message_id msg-9, got %q", got.ReplyToMessageID)
	}
	if got.AgentID != "main" || got.SessionKey != "sk_v1_123" {
		t.Fatalf("unexpected outbound turn metadata: agent=%q session=%q", got.AgentID, got.SessionKey)
	}
	if got.Scope == nil || got.Scope.AgentID != "main" || got.Scope.Values["chat"] != "direct:chat-42" {
		t.Fatalf("unexpected outbound scope: %+v", got.Scope)
	}
	if got.Context.Channel != "telegram" || got.Context.ChatID != "chat-42" {
		t.Fatalf("unexpected outbound context: %+v", got.Context)
	}
}

func TestNormalizeOutboundTraceScopes(t *testing.T) {
	scopes := []runtimeevents.TraceScope{
		runtimeevents.NewTraceScope("/workspace/main", "turn-1"),
		{Workspace: " /workspace/main ", TurnID: " turn-2 "},
		runtimeevents.NewTraceScope("/workspace/main", "turn-1"),
		{Workspace: "/workspace/main"},
	}
	want := []runtimeevents.TraceScope{
		runtimeevents.NewTraceScope("/workspace/main", "turn-1"),
		runtimeevents.NewTraceScope("/workspace/main", "turn-2"),
	}

	text, err := NormalizeOutboundMessage(OutboundMessage{TraceScopes: scopes})
	if err != nil {
		t.Fatalf("normalize text: %v", err)
	}
	if !slices.Equal(text.TraceScopes, want) {
		t.Fatalf("text trace scopes = %+v, want %+v", text.TraceScopes, want)
	}
	media, err := NormalizeOutboundMediaMessage(OutboundMediaMessage{TraceScopes: scopes})
	if err != nil {
		t.Fatalf("normalize media: %v", err)
	}
	if !slices.Equal(media.TraceScopes, want) {
		t.Fatalf("media trace scopes = %+v, want %+v", media.TraceScopes, want)
	}

	crossWorkspace := []runtimeevents.TraceScope{
		runtimeevents.NewTraceScope("/workspace/a", "turn-1"),
		runtimeevents.NewTraceScope("/workspace/b", "turn-2"),
	}
	if _, err := NormalizeTraceScopes(crossWorkspace); !errors.Is(err, ErrMixedTraceScopeWorkspaces) {
		t.Fatalf("cross-workspace trace scopes error = %v", err)
	}
}

func TestNormalizeOutboundTraceSettlementRequiresCompleteScope(t *testing.T) {
	valid := []runtimeevents.TraceScope{
		runtimeevents.NewTraceScope("/workspace/main", "turn-1"),
	}

	text, err := NormalizeOutboundMessage(OutboundMessage{
		TraceScopes: valid, TraceSettlement: true,
	})
	if err != nil || !text.TraceSettlement {
		t.Fatalf("valid text settlement = (%v, %v), want true and nil", text.TraceSettlement, err)
	}
	media, err := NormalizeOutboundMediaMessage(OutboundMediaMessage{
		TraceScopes: valid, TraceSettlement: true,
	})
	if err != nil || !media.TraceSettlement {
		t.Fatalf("valid media settlement = (%v, %v), want true and nil", media.TraceSettlement, err)
	}

	text, err = NormalizeOutboundMessage(OutboundMessage{TraceSettlement: true})
	if err != nil || text.TraceSettlement {
		t.Fatalf("unscoped text settlement = (%v, %v), want false and nil", text.TraceSettlement, err)
	}
	media, err = NormalizeOutboundMediaMessage(OutboundMediaMessage{TraceSettlement: true})
	if err != nil || media.TraceSettlement {
		t.Fatalf("unscoped media settlement = (%v, %v), want false and nil", media.TraceSettlement, err)
	}
}

func TestNormalizeOutboundMediaMessageValidatesRecoveryPrerequisite(t *testing.T) {
	recovery := &OutboundRecovery{
		Kind:        OutboundRecoveryBrowserScreenshot,
		ArtifactRef: "transfer-artifact://opaque", MediaRef: "media://screenshot",
		WorkspaceID: "workspace_1", AgentID: "browser", ActorID: "actor_1",
		RouteID: "route_1", SessionID: "session_1", ToolCallID: "call_1",
	}
	message, err := NormalizeOutboundMediaMessage(OutboundMediaMessage{
		Parts: []MediaPart{{Type: "image", Ref: recovery.MediaRef}}, Recovery: recovery,
	})
	if err != nil || message.Recovery == nil || message.Recovery == recovery {
		t.Fatalf("NormalizeOutboundMediaMessage() = %+v, %v", message, err)
	}
	recovery.MediaRef = "media://different"
	if message.Recovery.MediaRef != "media://screenshot" {
		t.Fatal("normalized recovery prerequisite aliases caller memory")
	}
	if _, err = NormalizeOutboundMediaMessage(OutboundMediaMessage{
		Parts: []MediaPart{{Type: "image", Ref: "media://screenshot"}}, Recovery: recovery,
	}); err == nil {
		t.Fatal("mismatched recovery media was accepted")
	}
}

func TestPublishOutboundRejectsMixedTraceScopeWorkspaces(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()
	scopes := []runtimeevents.TraceScope{
		runtimeevents.NewTraceScope("/workspace/a", "turn-1"),
		runtimeevents.NewTraceScope("/workspace/b", "turn-2"),
	}

	err := mb.PublishOutbound(context.Background(), OutboundMessage{
		Context: NewOutboundContext("telegram", "chat-1", ""),
		Content: "reply", TraceScopes: scopes,
	})
	if !errors.Is(err, ErrMixedTraceScopeWorkspaces) {
		t.Fatalf("PublishOutbound error = %v", err)
	}
	err = mb.PublishOutboundMedia(context.Background(), OutboundMediaMessage{
		Context: NewOutboundContext("telegram", "chat-1", ""),
		Parts:   []MediaPart{{Type: "image", Ref: "media://test"}}, TraceScopes: scopes,
	})
	if !errors.Is(err, ErrMixedTraceScopeWorkspaces) {
		t.Fatalf("PublishOutboundMedia error = %v", err)
	}

	select {
	case msg := <-mb.OutboundChan():
		t.Fatalf("invalid text outbound was delivered: %+v", msg)
	default:
	}
	select {
	case msg := <-mb.OutboundMediaChan():
		t.Fatalf("invalid media outbound was delivered: %+v", msg)
	default:
	}
}

func TestPublishOutbound_PreservesExplicitReplyToMessageID(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	msg := OutboundMessage{
		Context: InboundContext{
			Channel: "telegram",
			ChatID:  "chat-42",
		},
		ReplyToMessageID: "msg-9",
		Content:          "reply",
	}

	if err := mb.PublishOutbound(context.Background(), msg); err != nil {
		t.Fatalf("PublishOutbound failed: %v", err)
	}

	got := <-mb.OutboundChan()
	if got.ReplyToMessageID != "msg-9" {
		t.Fatalf("expected mirrored reply_to_message_id msg-9, got %q", got.ReplyToMessageID)
	}
	if got.Context.ReplyToMessageID != "msg-9" {
		t.Fatalf("expected context reply_to_message_id msg-9, got %q", got.Context.ReplyToMessageID)
	}
}

func TestPublishOutbound_PreservesExplicitReplyToMessageIDWhenContextReplyIsBlank(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	msg := OutboundMessage{
		Context: InboundContext{
			Channel:          "telegram",
			ChatID:           "chat-42",
			ReplyToMessageID: "   ",
		},
		ReplyToMessageID: "msg-9",
		Content:          "reply",
	}

	if err := mb.PublishOutbound(context.Background(), msg); err != nil {
		t.Fatalf("PublishOutbound failed: %v", err)
	}

	got := <-mb.OutboundChan()
	if got.ReplyToMessageID != "msg-9" {
		t.Fatalf("expected mirrored reply_to_message_id msg-9, got %q", got.ReplyToMessageID)
	}
	if got.Context.ReplyToMessageID != "msg-9" {
		t.Fatalf("expected context reply_to_message_id msg-9, got %q", got.Context.ReplyToMessageID)
	}
}

func TestPublishOutboundMedia_MirrorsContextToLegacyFields(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	msg := OutboundMediaMessage{
		Context: InboundContext{
			Channel: "slack",
			ChatID:  "C001",
		},
		AgentID:    "support",
		SessionKey: "sk_v1_media",
		Scope: &OutboundScope{
			Version:    1,
			AgentID:    "support",
			Channel:    "slack",
			Dimensions: []string{"chat"},
			Values: map[string]string{
				"chat": "channel:c001",
			},
		},
		Parts: []MediaPart{{Type: "image", Ref: "media://1"}},
	}

	if err := mb.PublishOutboundMedia(context.Background(), msg); err != nil {
		t.Fatalf("PublishOutboundMedia failed: %v", err)
	}

	got := <-mb.OutboundMediaChan()
	if got.Channel != "slack" {
		t.Fatalf("expected legacy channel slack, got %q", got.Channel)
	}
	if got.ChatID != "C001" {
		t.Fatalf("expected legacy chat ID C001, got %q", got.ChatID)
	}
	if got.AgentID != "support" || got.SessionKey != "sk_v1_media" {
		t.Fatalf("unexpected outbound media turn metadata: agent=%q session=%q", got.AgentID, got.SessionKey)
	}
	if got.Scope == nil || got.Scope.Values["chat"] != "channel:c001" {
		t.Fatalf("unexpected outbound media scope: %+v", got.Scope)
	}
	if got.Context.Channel != "slack" || got.Context.ChatID != "C001" {
		t.Fatalf("unexpected outbound media context: %+v", got.Context)
	}
}

func TestPublishAudioChunkSubscribe(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	chunk := AudioChunk{
		SessionID: "voice-1",
		SpeakerID: "speaker-1",
		ChatID:    "chat-1",
		Channel:   "discord",
		Sequence:  7,
		Format:    "opus",
		Data:      []byte{0x01, 0x02},
	}

	if err := mb.PublishAudioChunk(context.Background(), chunk); err != nil {
		t.Fatalf("PublishAudioChunk failed: %v", err)
	}

	got, ok := <-mb.AudioChunksChan()
	if !ok {
		t.Fatal("AudioChunksChan returned ok=false")
	}
	if got.SessionID != "voice-1" || got.Sequence != 7 {
		t.Fatalf("unexpected audio chunk: %+v", got)
	}
}

func TestPublishAudioChunk_BackpressureDropPublishesRuntimeEvent(t *testing.T) {
	eventBus := runtimeevents.NewBus()
	defer func() {
		if err := eventBus.Close(); err != nil {
			t.Errorf("event bus close failed: %v", err)
		}
	}()

	_, eventsCh, err := eventBus.Channel().OfKind(runtimeevents.KindBusMessageDropped).SubscribeChan(
		t.Context(),
		runtimeevents.SubscribeOptions{Name: "bus-drop-events", Buffer: 1},
	)
	if err != nil {
		t.Fatalf("SubscribeChan failed: %v", err)
	}

	mb := NewMessageBus()
	defer mb.Close()
	mb.SetEventPublisher(eventBus)

	for i := range defaultBusBufferSize * 4 {
		if pubErr := mb.PublishAudioChunk(context.Background(), AudioChunk{
			SessionID: "voice-1",
			SpeakerID: "speaker-1",
			ChatID:    "chat-1",
			Channel:   "discord",
			Sequence:  uint64(i),
			Format:    "opus",
			Data:      []byte{0x01},
		}); pubErr != nil {
			t.Fatalf("fill failed at %d: %v", i, pubErr)
		}
	}

	err = mb.PublishAudioChunk(context.Background(), AudioChunk{
		SessionID: "voice-1",
		SpeakerID: "speaker-1",
		ChatID:    "chat-1",
		Channel:   "discord",
		Sequence:  999,
		Format:    "opus",
		Data:      []byte{0x01},
	})
	if !errors.Is(err, ErrBusBackpressure) {
		t.Fatalf("PublishAudioChunk() error = %v, want %v", err, ErrBusBackpressure)
	}

	evt := receiveBusRuntimeEvent(t, eventsCh)
	if evt.Kind != runtimeevents.KindBusMessageDropped ||
		evt.Source.Name != "audio_chunk" ||
		evt.Severity != runtimeevents.SeverityWarn {
		t.Fatalf("drop event = %+v", evt)
	}
	if evt.Scope.Channel != "discord" || evt.Scope.ChatID != "chat-1" {
		t.Fatalf("drop event scope = %+v", evt.Scope)
	}
	if evt.Attrs["stream"] != "audio_chunk" ||
		evt.Attrs["reason"] != "queue_full_timeout" ||
		evt.Attrs["wait_ms"] != defaultAudioPublishTimeout.Milliseconds() ||
		evt.Attrs["queue_depth"] != defaultBusBufferSize*4 ||
		evt.Attrs["queue_capacity"] != defaultBusBufferSize*4 ||
		evt.Attrs["dropped_total"] != uint64(1) {
		t.Fatalf("drop event attrs = %#v", evt.Attrs)
	}

	stats := mb.Stats()
	if stats.AudioChunks.DroppedTotal != 1 {
		t.Fatalf("AudioChunks dropped = %d, want 1", stats.AudioChunks.DroppedTotal)
	}
	if stats.AudioChunks.Depth != defaultBusBufferSize*4 {
		t.Fatalf("AudioChunks depth = %d, want %d", stats.AudioChunks.Depth, defaultBusBufferSize*4)
	}
	wantWaitMS := defaultAudioPublishTimeout.Milliseconds()
	if stats.AudioChunks.LastDropWaitMillis != wantWaitMS {
		t.Fatalf("AudioChunks last wait ms = %d, want %d", stats.AudioChunks.LastDropWaitMillis, wantWaitMS)
	}
}

func TestPublishVoiceControlSubscribe(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	ctrl := VoiceControl{
		SessionID: "voice-1",
		ChatID:    "chat-1",
		Type:      "command",
		Action:    "start",
	}

	if err := mb.PublishVoiceControl(context.Background(), ctrl); err != nil {
		t.Fatalf("PublishVoiceControl failed: %v", err)
	}

	got, ok := <-mb.VoiceControlsChan()
	if !ok {
		t.Fatal("VoiceControlsChan returned ok=false")
	}
	if got.Type != "command" || got.Action != "start" {
		t.Fatalf("unexpected voice control: %+v", got)
	}
}

func TestNewOutboundContext_NormalizesReplyAddress(t *testing.T) {
	ctx := NewOutboundContext(" telegram ", " chat-42 ", " msg-9 ")
	if ctx.Channel != "telegram" {
		t.Fatalf("expected channel telegram, got %q", ctx.Channel)
	}
	if ctx.ChatID != "chat-42" {
		t.Fatalf("expected chat_id chat-42, got %q", ctx.ChatID)
	}
	if ctx.ReplyToMessageID != "msg-9" {
		t.Fatalf("expected reply_to_message_id msg-9, got %q", ctx.ReplyToMessageID)
	}
}

func TestPublishInbound_ContextCancel(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	// Fill the buffer
	ctx := context.Background()
	for i := range defaultBusBufferSize {
		if err := mb.PublishInbound(ctx, InboundMessage{
			Context: InboundContext{
				Channel:  "test",
				ChatID:   "chat-fill",
				ChatType: "direct",
				SenderID: "user-fill",
			},
			Content: "fill",
		}); err != nil {
			t.Fatalf("fill failed at %d: %v", i, err)
		}
	}

	// Now buffer is full; publish with a canceled context
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()

	err := mb.PublishInbound(cancelCtx, InboundMessage{
		Context: InboundContext{
			Channel:  "test",
			ChatID:   "chat-overflow",
			ChatType: "direct",
			SenderID: "user-overflow",
		},
		Content: "overflow",
	})
	if err == nil {
		t.Fatal("expected error from canceled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestPublishInbound_BusClosed(t *testing.T) {
	mb := NewMessageBus()
	mb.Close()

	err := mb.PublishInbound(context.Background(), InboundMessage{
		Context: InboundContext{
			Channel:  "test",
			ChatID:   "chat1",
			ChatType: "direct",
			SenderID: "user1",
		},
		Content: "test",
	})
	if !errors.Is(err, ErrBusClosed) {
		t.Fatalf("expected ErrBusClosed, got %v", err)
	}
}

func TestPublishOutbound_BusClosed(t *testing.T) {
	mb := NewMessageBus()
	mb.Close()

	err := mb.PublishOutbound(context.Background(), OutboundMessage{
		Context: InboundContext{
			Channel: "test",
			ChatID:  "chat1",
		},
		Content: "test",
	})
	if !errors.Is(err, ErrBusClosed) {
		t.Fatalf("expected ErrBusClosed, got %v", err)
	}
}

func TestConsumeInbound_ContextCancel(t *testing.T) {
	mb := NewMessageBus()

	defer mb.Close()

	for i := range defaultBusBufferSize {
		if err := mb.PublishInbound(context.Background(), InboundMessage{
			Context: InboundContext{
				Channel:  "test",
				ChatID:   "chat-fill",
				ChatType: "direct",
				SenderID: "user-fill",
			},
			Content: "fill",
		}); err != nil {
			t.Fatalf("fill failed at %d: %v", i, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = mb.PublishInbound(ctx, InboundMessage{
		Context: InboundContext{
			Channel:  "test",
			ChatID:   "chat-cancel",
			ChatType: "direct",
			SenderID: "user-cancel",
		},
		Content: "ContextCancel",
	})

	select {
	case <-ctx.Done():
		t.Log("context canceled, as expected")

	case msg, ok := <-mb.InboundChan():
		if !ok {
			t.Fatal("expected ok=false when context is canceled")
		}
		if msg.Content == "ContextCancel" {
			t.Fatalf("expected content 'ContextCancel', got %q", msg.Content)
		}
	}
}

func TestConsumeInbound_BusClosed(t *testing.T) {
	mb := NewMessageBus()

	timer := time.AfterFunc(100*time.Millisecond, func() {
		mb.Close()
	})

	select {
	case <-timer.C:
		t.Log("context canceled, as expected")

	case _, ok := <-mb.InboundChan():
		if ok {
			t.Fatal("expected ok=false when context is canceled")
		}
	}
}

func TestSubscribeOutbound_BusClosed(t *testing.T) {
	mb := NewMessageBus()
	mb.Close()

	_, ok := <-mb.OutboundChan()
	if ok {
		t.Fatal("expected ok=false when bus is closed")
	}
}

func TestConcurrentPublishClose(t *testing.T) {
	mb := NewMessageBus()
	ctx := context.Background()

	const numGoroutines = 100
	var wg sync.WaitGroup
	wg.Add(numGoroutines + 1)

	// Spawn many goroutines trying to publish
	for range numGoroutines {
		go func() {
			defer wg.Done()
			// Use a short timeout context so we don't block forever after close
			publishCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
			defer cancel()
			// Errors are expected; we just must not panic or deadlock
			_ = mb.PublishInbound(publishCtx, InboundMessage{Content: "concurrent"})
		}()
	}

	// Close from another goroutine
	go func() {
		defer wg.Done()
		time.Sleep(5 * time.Millisecond)
		mb.Close()
	}()

	// Must complete without deadlock
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// success
	case <-time.After(5 * time.Second):
		t.Fatal("test timed out - possible deadlock")
	}
}

func TestPublishInbound_FullBuffer(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	ctx := context.Background()

	// Fill the buffer
	for i := range defaultBusBufferSize {
		if err := mb.PublishInbound(ctx, InboundMessage{
			Context: InboundContext{
				Channel:  "test",
				ChatID:   "chat-fill",
				ChatType: "direct",
				SenderID: "user-fill",
			},
			Content: "fill",
		}); err != nil {
			t.Fatalf("fill failed at %d: %v", i, err)
		}
	}

	// Buffer is full; publish with short timeout
	timeoutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	err := mb.PublishInbound(timeoutCtx, InboundMessage{
		Context: InboundContext{
			Channel:  "test",
			ChatID:   "chat-overflow",
			ChatType: "direct",
			SenderID: "user-overflow",
		},
		Content: "overflow",
	})
	if err == nil {
		t.Fatal("expected error when buffer is full and context times out")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}
}

// TestPublishInbound_FullBufferUsesBusBackpressureBudget exercises the generic
// publish() backpressure path directly (with a short 20ms timeout) rather than
// going through PublishInbound(). This avoids waiting for a long context timeout
// and keeps the test fast. Context validation and public-API wiring are covered
// by TestPublishInbound_FullBuffer and TestPublishInbound_ContextCancel.
func TestPublishInbound_FullBufferUsesBusBackpressureBudget(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	ch := make(chan InboundMessage, 1)
	ch <- InboundMessage{Content: "fill"}

	scope := runtimeevents.Scope{Channel: "test", ChatID: "chat-overflow"}
	err := publish(context.Background(), mb, ch, InboundMessage{Content: "overflow"}, publishPolicy{
		stream:  "inbound",
		timeout: 20 * time.Millisecond,
	}, &mb.inboundStats, scope)
	if !errors.Is(err, ErrBusBackpressure) {
		t.Fatalf("publish() error = %v, want %v", err, ErrBusBackpressure)
	}

	stats := mb.Stats()
	if stats.Inbound.DroppedTotal != 1 {
		t.Fatalf("Inbound dropped = %d, want 1", stats.Inbound.DroppedTotal)
	}
	if stats.Inbound.LastDropWaitMillis != 20 {
		t.Fatalf("Inbound last wait ms = %d, want 20", stats.Inbound.LastDropWaitMillis)
	}
}

func TestMessageBusHealthCheckIncludesQueueDepthAndDrops(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	ok, msg := mb.HealthCheck()
	if !ok {
		t.Fatal("HealthCheck should remain ok for backpressure telemetry")
	}
	if msg == "" {
		t.Fatal("HealthCheck message should not be empty")
	}

	for i := range cap(mb.audioChunks) {
		if err := mb.PublishAudioChunk(context.Background(), AudioChunk{
			Channel:  "discord",
			ChatID:   "voice-room",
			Sequence: uint64(i),
			Data:     []byte("fill"),
		}); err != nil {
			t.Fatalf("fill audio buffer at %d: %v", i, err)
		}
	}
	_ = mb.PublishAudioChunk(context.Background(), AudioChunk{
		Channel:  "discord",
		ChatID:   "voice-room",
		Sequence: 999,
		Data:     []byte("overflow"),
	})

	stats := mb.Stats()
	if stats.AudioChunks.Depth != cap(mb.audioChunks) {
		t.Fatalf("audio depth = %d, want %d", stats.AudioChunks.Depth, cap(mb.audioChunks))
	}
	if stats.AudioChunks.DroppedTotal != 1 {
		t.Fatalf("audio dropped = %d, want 1", stats.AudioChunks.DroppedTotal)
	}
}

func TestCloseIdempotent(t *testing.T) {
	mb := NewMessageBus()

	// Multiple Close calls must not panic
	mb.Close()
	mb.Close()
	mb.Close()

	// After close, publish should return ErrBusClosed
	err := mb.PublishInbound(context.Background(), InboundMessage{
		Context: InboundContext{
			Channel:  "test",
			ChatID:   "chat1",
			ChatType: "direct",
			SenderID: "user1",
		},
		Content: "test",
	})
	if !errors.Is(err, ErrBusClosed) {
		t.Fatalf("expected ErrBusClosed after multiple closes, got %v", err)
	}
}
