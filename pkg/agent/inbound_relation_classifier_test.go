package agent

import (
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

func TestAllowAdjacentMediaFollowupForChatType_OnlyDirect(t *testing.T) {
	for _, chatType := range []string{"", "group", "channel", "private"} {
		if allowAdjacentMediaFollowupForChatType(chatType) {
			t.Fatalf("allowAdjacentMediaFollowupForChatType(%q) = true, want false", chatType)
		}
	}
	if !allowAdjacentMediaFollowupForChatType("direct") {
		t.Fatal("allowAdjacentMediaFollowupForChatType(direct) = false, want true")
	}
}

func TestClassifyPromptCurrentMessageRelation_ReplyWinsForMediaOnly(t *testing.T) {
	got := classifyPromptCurrentMessageRelation(
		"[media only]",
		[]string{"media://image-1"},
		"reply-1",
		false,
		nil,
		time.Time{},
	)

	if got.Kind != InboundRelationReplyToMessage {
		t.Fatalf("Kind = %q, want %q", got.Kind, InboundRelationReplyToMessage)
	}
	if !got.MediaOnly {
		t.Fatal("MediaOnly = false, want true")
	}
}

func TestClassifyPromptCurrentMessageRelation_AdjacentMediaFollowup(t *testing.T) {
	now := time.Now()
	ts := now.Add(-time.Minute)

	got := classifyPromptCurrentMessageRelation(
		"[media only]",
		[]string{"media://image-1"},
		"",
		true,
		[]providers.Message{
			{Role: "user", Content: "Here is what I ate", CreatedAt: &ts},
		},
		now,
	)

	if got.Kind != InboundRelationAdjacentFollowupMedia {
		t.Fatalf("Kind = %q, want %q", got.Kind, InboundRelationAdjacentFollowupMedia)
	}
}

func TestClassifyPromptCurrentMessageRelation_AdjacentMediaFollowupRequiresExplicitAllow(t *testing.T) {
	now := time.Now()
	ts := now.Add(-time.Minute)

	got := classifyPromptCurrentMessageRelation(
		"[media only]",
		[]string{"media://image-1"},
		"",
		false,
		[]providers.Message{
			{Role: "user", Content: "Here is what I ate", CreatedAt: &ts},
		},
		now,
	)

	if got.Kind != InboundRelationStandalone {
		t.Fatalf("Kind = %q, want %q", got.Kind, InboundRelationStandalone)
	}
	if !got.MediaOnly {
		t.Fatal("MediaOnly = false, want true")
	}
}

func TestClassifyPromptCurrentMessageRelation_AdjacentMediaFollowupRequiresTimestamp(t *testing.T) {
	zero := time.Time{}
	for _, test := range []struct {
		name      string
		createdAt *time.Time
	}{
		{name: "missing"},
		{name: "zero", createdAt: &zero},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := classifyPromptCurrentMessageRelation(
				"[media only]",
				[]string{"media://image-1"},
				"",
				true,
				[]providers.Message{
					{Role: "user", Content: "Here is what I ate", CreatedAt: test.createdAt},
				},
				time.Now(),
			)

			if got.Kind != InboundRelationStandalone {
				t.Fatalf("Kind = %q, want %q", got.Kind, InboundRelationStandalone)
			}
			if !got.MediaOnly {
				t.Fatal("MediaOnly = false, want true")
			}
		})
	}
}

func TestClassifyPromptCurrentMessageRelation_StandaloneAfterAssistantReply(t *testing.T) {
	userTS := time.Now().Add(-time.Minute)
	assistantTS := time.Now().Add(-30 * time.Second)

	got := classifyPromptCurrentMessageRelation(
		"[media only]",
		[]string{"media://image-1"},
		"",
		true,
		[]providers.Message{
			{Role: "user", Content: "Here is what I ate", CreatedAt: &userTS},
			{Role: "assistant", Content: "Saved.", CreatedAt: &assistantTS},
		},
		time.Now(),
	)

	if got.Kind != InboundRelationStandalone {
		t.Fatalf("Kind = %q, want %q", got.Kind, InboundRelationStandalone)
	}
	if !got.MediaOnly {
		t.Fatal("MediaOnly = false, want true")
	}
}

func TestClassifyPromptCurrentMessageRelation_TextMessageStaysStandalone(t *testing.T) {
	got := classifyPromptCurrentMessageRelation(
		"this is plain text",
		[]string{"media://image-1"},
		"",
		false,
		nil,
		time.Time{},
	)

	if got.Kind != InboundRelationStandalone {
		t.Fatalf("Kind = %q, want %q", got.Kind, InboundRelationStandalone)
	}
	if got.MediaOnly {
		t.Fatal("MediaOnly = true, want false")
	}
}

func TestClassifyPromptCurrentMessageRelation_KnownAttachmentPlaceholderCountsAsMediaOnly(t *testing.T) {
	got := classifyPromptCurrentMessageRelation(
		"[image]",
		[]string{"media://image-1"},
		"",
		false,
		nil,
		time.Time{},
	)

	if got.Kind != InboundRelationStandalone {
		t.Fatalf("Kind = %q, want %q", got.Kind, InboundRelationStandalone)
	}
	if !got.MediaOnly {
		t.Fatal("MediaOnly = false, want true")
	}
}

func TestClassifyPromptCurrentMessageRelation_RejectsOutOfOrderEventTime(t *testing.T) {
	previousReceivedAt := time.Date(2026, 9, 6, 20, 0, 0, 0, time.UTC)
	currentReceivedAt := previousReceivedAt.Add(-time.Second)

	got := classifyPromptCurrentMessageRelation(
		"[media only]",
		[]string{"media://image-1"},
		"",
		true,
		[]providers.Message{{
			Role:      "user",
			Content:   "Later event",
			CreatedAt: &previousReceivedAt,
		}},
		currentReceivedAt,
	)

	if got.Kind != InboundRelationStandalone {
		t.Fatalf("Kind = %q, want %q", got.Kind, InboundRelationStandalone)
	}
}

func TestNormalizeDispatchInboundRelationUsesEventTimeInsteadOfProcessingTime(t *testing.T) {
	previousReceivedAt := time.Date(2026, 9, 6, 20, 0, 0, 0, time.UTC)
	currentReceivedAt := previousReceivedAt.Add(time.Minute)
	processingAt := currentReceivedAt.Add(30 * time.Minute)
	store := session.NewMemoryStore()
	store.AddFullMessage("session-1", providers.Message{
		Role:      "user",
		Content:   "Here is what I ate",
		CreatedAt: &previousReceivedAt,
	})
	agent := &AgentInstance{Sessions: store}
	dispatch := DispatchRequest{
		SessionKey:  "session-1",
		UserMessage: "[media only]",
		Media:       []string{"media://image-1"},
		InboundContext: &bus.InboundContext{
			Channel:    "telegram",
			ChatID:     "chat-1",
			ChatType:   "direct",
			SenderID:   "user-1",
			ReceivedAt: currentReceivedAt,
		},
	}

	got := normalizeDispatchInboundRelation(agent, dispatch, processingAt)

	if got.InboundContext.Relation.Kind != InboundRelationAdjacentFollowupMedia {
		t.Fatalf(
			"Kind = %q, want %q",
			got.InboundContext.Relation.Kind,
			InboundRelationAdjacentFollowupMedia,
		)
	}
	if !got.InboundContext.ReceivedAt.Equal(currentReceivedAt) {
		t.Fatalf("ReceivedAt = %v, want event time %v", got.InboundContext.ReceivedAt, currentReceivedAt)
	}
}
