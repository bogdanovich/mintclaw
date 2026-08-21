package integrationtools

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

func TestMessageTool_Execute_Success(t *testing.T) {
	tool := NewMessageTool()

	ctx := WithToolContext(context.Background(), "test-channel", "test-chat-id")
	args := map[string]any{
		"content": "Hello, world!",
	}

	result := tool.Execute(ctx, args)

	if result.Delivery.Outbound == nil {
		t.Fatal("expected declarative outbound delivery")
	}
	if result.Delivery.Outbound.Channel != "test-channel" {
		t.Errorf("Expected channel 'test-channel', got '%s'", result.Delivery.Outbound.Channel)
	}
	if result.Delivery.Outbound.ChatID != "test-chat-id" {
		t.Errorf("Expected chatID 'test-chat-id', got '%s'", result.Delivery.Outbound.ChatID)
	}
	if result.Delivery.Outbound.Text != "Hello, world!" {
		t.Errorf("Expected content 'Hello, world!', got '%s'", result.Delivery.Outbound.Text)
	}
	if len(result.Delivery.Outbound.Media) != 0 {
		t.Fatalf("expected no media parts, got %d", len(result.Delivery.Outbound.Media))
	}

	if !result.Delivery.IsFinalHandled() {
		t.Fatalf("delivery result = %+v, want final_handled", result)
	}

	// - ForLLM contains send status description
	if result.ForLLM != "Message prepared for delivery to test-channel:test-chat-id" {
		t.Errorf("unexpected ForLLM status: %q", result.ForLLM)
	}

	// - ForUser is empty (user already received message directly)
	if result.ForUser != "" {
		t.Errorf("Expected ForUser to be empty, got '%s'", result.ForUser)
	}

	// - IsError should be false
	if result.IsError {
		t.Error("Expected IsError=false for successful send")
	}
}

func TestMessageTool_Execute_WithCustomChannel(t *testing.T) {
	tool := NewMessageTool()

	ctx := WithToolContext(context.Background(), "default-channel", "default-chat-id")
	args := map[string]any{
		"content": "Test message",
		"channel": "custom-channel",
		"chat_id": "custom-chat-id",
	}

	result := tool.Execute(ctx, args)

	// Verify custom channel/chatID were used instead of defaults
	if result.Delivery.Outbound == nil {
		t.Fatal("expected declarative outbound delivery")
	}
	if result.Delivery.Outbound.Channel != "custom-channel" {
		t.Errorf("Expected channel 'custom-channel', got '%s'", result.Delivery.Outbound.Channel)
	}
	if result.Delivery.Outbound.ChatID != "custom-chat-id" {
		t.Errorf("Expected chatID 'custom-chat-id', got '%s'", result.Delivery.Outbound.ChatID)
	}

	if !result.Delivery.IsFinalHandled() {
		t.Fatalf("delivery result = %+v, want final_handled", result)
	}
	if result.ForLLM != "Message prepared for delivery to custom-channel:custom-chat-id" {
		t.Errorf("unexpected ForLLM status: %q", result.ForLLM)
	}
}

func TestMessageTool_Execute_ImmediateContinue(t *testing.T) {
	tool := NewMessageTool()
	result := tool.Execute(
		WithToolContext(context.Background(), "test-channel", "test-chat-id"),
		map[string]any{
			"content":         "Still working",
			"delivery_intent": string(DeliveryImmediateContinue),
		},
	)

	if !result.Delivery.IsImmediate() || result.Delivery.IsFinalHandled() {
		t.Fatalf("delivery result = %+v, want immediate_continue", result)
	}
}

func TestMessageTool_Execute_MissingContent(t *testing.T) {
	tool := NewMessageTool()

	ctx := WithToolContext(context.Background(), "test-channel", "test-chat-id")
	args := map[string]any{} // content missing

	result := tool.Execute(ctx, args)

	// Verify error result for missing content/media
	if !result.IsError {
		t.Error("Expected IsError=true for missing content/media")
	}
	if result.ForLLM != "content or media is required" {
		t.Errorf("Expected ForLLM 'content or media is required', got '%s'", result.ForLLM)
	}
}

func TestMessageTool_Execute_NoTargetChannel(t *testing.T) {
	tool := NewMessageTool()
	// No WithToolContext — channel/chatID are empty

	ctx := context.Background()
	args := map[string]any{
		"content": "Test message",
	}

	result := tool.Execute(ctx, args)

	// Verify error when no target channel specified
	if !result.IsError {
		t.Error("Expected IsError=true when no target channel")
	}
	if result.ForLLM != "No target channel/chat specified" {
		t.Errorf("Expected ForLLM 'No target channel/chat specified', got '%s'", result.ForLLM)
	}
}

func TestMessageTool_Execute_DeclarativeWithoutCallback(t *testing.T) {
	tool := NewMessageTool()

	ctx := WithToolContext(context.Background(), "test-channel", "test-chat-id")
	args := map[string]any{
		"content": "Test message",
	}

	result := tool.Execute(ctx, args)

	if result.IsError {
		t.Fatalf("expected declarative delivery without callback, got %s", result.ForLLM)
	}
	if result.Delivery.Outbound == nil || result.Delivery.Outbound.Text != "Test message" {
		t.Fatalf("unexpected outbound: %+v", result.Delivery.Outbound)
	}
}

func TestMessageTool_Name(t *testing.T) {
	tool := NewMessageTool()
	if tool.Name() != "message" {
		t.Errorf("Expected name 'message', got '%s'", tool.Name())
	}
}

func TestMessageTool_Description(t *testing.T) {
	tool := NewMessageTool()
	desc := tool.Description()
	if desc == "" {
		t.Error("Description should not be empty")
	}
}

func TestMessageTool_Parameters(t *testing.T) {
	tool := NewMessageTool()
	params := tool.Parameters()

	// Verify parameters structure
	typ, ok := params["type"].(string)
	if !ok || typ != "object" {
		t.Error("Expected type 'object'")
	}

	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatal("Expected properties to be a map")
	}

	// Check required properties
	required, ok := params["required"].([]string)
	if !ok || len(required) != 1 || required[0] != "content" {
		t.Fatal("Expected content-only required schema when local media is disabled")
	}

	// Check content property
	contentProp, ok := props["content"].(map[string]any)
	if !ok {
		t.Error("Expected 'content' property")
	}
	if contentProp["type"] != "string" {
		t.Error("Expected content type to be 'string'")
	}

	if _, hasMediaProp := props["media"]; hasMediaProp {
		t.Fatal("did not expect 'media' property when local media is disabled")
	}

	// Check channel property (optional)
	channelProp, ok := props["channel"].(map[string]any)
	if !ok {
		t.Error("Expected 'channel' property")
	}
	if channelProp["type"] != "string" {
		t.Error("Expected channel type to be 'string'")
	}

	// Check chat_id property (optional)
	chatIDProp, ok := props["chat_id"].(map[string]any)
	if !ok {
		t.Error("Expected 'chat_id' property")
	}
	if chatIDProp["type"] != "string" {
		t.Error("Expected chat_id type to be 'string'")
	}

	// Check reply_to_message_id property (optional)
	replyToProp, ok := props["reply_to_message_id"].(map[string]any)
	if !ok {
		t.Error("Expected 'reply_to_message_id' property")
	}
	if replyToProp["type"] != "string" {
		t.Error("Expected reply_to_message_id type to be 'string'")
	}

	deliveryIntent, ok := props["delivery_intent"].(map[string]any)
	if !ok {
		t.Fatal("Expected delivery_intent property")
	}
	wantIntents := []string{string(DeliveryImmediateContinue), string(DeliveryFinalHandled)}
	if got := deliveryIntent["enum"]; !reflect.DeepEqual(got, wantIntents) {
		t.Fatalf("delivery_intent enum = %#v, want %#v", got, wantIntents)
	}
}

func TestMessageTool_Parameters_WithLocalMediaEnabled(t *testing.T) {
	tool := NewMessageTool()
	tool.ConfigureLocalMedia(t.TempDir(), true, 1024*1024, nil)
	params := tool.Parameters()

	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatal("Expected properties to be a map")
	}
	mediaProp, ok := props["media"].(map[string]any)
	if !ok {
		t.Fatal("Expected 'media' property")
	}
	if mediaProp["type"] != "array" {
		t.Error("Expected media type to be 'array'")
	}
	anyOf, ok := params["anyOf"].([]map[string]any)
	if !ok || len(anyOf) != 2 {
		t.Fatal("Expected anyOf content/media requirement")
	}
	if _, ok := params["required"]; ok {
		t.Fatal("did not expect top-level required content when media is enabled")
	}
}

func TestMessageTool_Execute_WithMediaDisabled(t *testing.T) {
	tool := NewMessageTool()

	ctx := WithToolContext(context.Background(), "telegram", "-1001")
	result := tool.Execute(ctx, map[string]any{
		"media": []any{
			map[string]any{"path": "photo.jpg"},
		},
	})
	if !result.IsError {
		t.Fatal("expected error when message media is disabled")
	}
	if result.ForLLM != "message media attachments are disabled; enable tools.message.media_enabled to send local media through message" {
		t.Fatalf("unexpected error: %q", result.ForLLM)
	}
}

func TestMessageTool_Execute_WithReplyToMessageID(t *testing.T) {
	tool := NewMessageTool()

	ctx := WithToolContext(context.Background(), "test-channel", "test-chat-id")
	args := map[string]any{
		"content":             "Reply test",
		"reply_to_message_id": "msg-123",
	}

	result := tool.Execute(ctx, args)
	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.ForLLM)
	}
	if result.Delivery.Outbound == nil || result.Delivery.Outbound.ReplyToMessageID != "msg-123" {
		t.Fatalf("unexpected reply_to_message_id in outbound: %+v", result.Delivery.Outbound)
	}
}

func TestMessageTool_Execute_TracksSentTargetForTurnSuppression(t *testing.T) {
	tool := NewMessageTool()

	ctx := WithToolContext(context.Background(), "test-channel", "test-chat-id")
	ctx = WithToolSessionContext(ctx, "main", "sk_v1_tool", &session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    "main",
		Channel:    "telegram",
		Dimensions: []string{"chat"},
		Values: map[string]string{
			"chat": "direct:test-chat-id",
		},
	})

	result := tool.Execute(ctx, map[string]any{"content": "Hello, world!"})
	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.ForLLM)
	}
	if tool.HasSentTo("sk_v1_tool", "test-channel", "test-chat-id") {
		t.Fatal("prepared delivery must not be tracked as sent")
	}
	result.Delivery.Confirm()
	if !tool.HasSentTo("sk_v1_tool", "test-channel", "test-chat-id") {
		t.Fatal("expected sent target tracking for final-response suppression")
	}
}

func TestMessageTool_Execute_WithMedia(t *testing.T) {
	tool := NewMessageTool()
	store := media.NewFileMediaStore()
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "photo.jpg")
	if err := os.WriteFile(imgPath, []byte("fake image bytes"), 0o644); err != nil {
		t.Fatalf("write image: %v", err)
	}
	tool.ConfigureLocalMedia(dir, true, 1024*1024, []*regexp.Regexp{})
	tool.SetMediaStore(store)

	ctx := WithToolContext(context.Background(), "telegram", "-1001")
	result := tool.Execute(ctx, map[string]any{
		"content": "Caption text",
		"media": []any{
			map[string]any{
				"path": imgPath,
			},
		},
	})
	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.ForLLM)
	}
	if result.Delivery.Outbound == nil {
		t.Fatal("expected declarative outbound delivery")
	}
	if result.Delivery.Outbound.Text != "Caption text" {
		t.Fatalf("content = %q, want Caption text", result.Delivery.Outbound.Text)
	}
	if len(result.Delivery.Outbound.Media) != 1 {
		t.Fatalf("expected 1 media part, got %d", len(result.Delivery.Outbound.Media))
	}
	if result.Delivery.Outbound.Media[0].Caption != "Caption text" {
		t.Fatalf("first part caption = %q, want Caption text", result.Delivery.Outbound.Media[0].Caption)
	}
	if result.Delivery.Outbound.Media[0].Ref == "" {
		t.Fatal("expected media ref to be populated")
	}
	if result.Delivery.Outbound.Media[0].Type == "" {
		t.Fatal("expected media type to be inferred")
	}
}
