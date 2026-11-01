package bus_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
)

func TestRecoveryOutboxRemainsStrictSchemaCompatible(t *testing.T) {
	schema, err := jsonschema.For[outbox.Intent](nil)
	if err != nil {
		t.Fatalf("outbox schema inference failed: %v", err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	primary := bus.OutboundRecovery{
		Kind:        bus.OutboundRecoveryBrowserScreenshot,
		ArtifactRef: "transfer-artifact://first",
		MediaRef:    "media://first",
		WorkspaceID: "workspace",
		AgentID:     "browser",
		ActorID:     "actor",
		RouteID:     "route",
		SessionID:   "closed-session",
		ToolCallID:  "capture-first",
	}
	second := primary
	second.ArtifactRef, second.MediaRef, second.ToolCallID = "transfer-artifact://second", "media://second", "capture-second"
	for _, batch := range []bool{false, true} {
		message := bus.OutboundMediaMessage{
			Parts: []bus.MediaPart{{Type: "image", Ref: primary.MediaRef}}, Recovery: &primary,
		}
		if batch {
			message.Parts = append(message.Parts, bus.MediaPart{Type: "image", Ref: second.MediaRef})
			message.AdditionalRecovery = []bus.OutboundRecovery{second}
		}
		intent, err := outbox.NewMediaIntent(t.TempDir(), outbox.Identity{
			SourceID: "schema-test", Channel: "telegram", ChatID: "chat", SessionKey: "route",
		}, message, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(intent)
		if err != nil {
			t.Fatal(err)
		}
		var value any
		if err = json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		if err = resolved.Validate(value); err != nil {
			t.Fatalf("strict outbox validation failed (batch=%v): %v", batch, err)
		}
	}
}
