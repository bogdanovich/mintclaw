package mintclaw

import (
	"context"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
)

func TestSendTextFinalUsageUsesNativeMetadata(t *testing.T) {
	for _, test := range []struct {
		name     string
		kind     string
		outbound string
		input    int
		output   int
		want     bool
	}{
		{"final", bus.OutboundMessageKindFinalReply, bus.OutboundKindFinal, 10252, 20, true},
		{"input-only", bus.OutboundMessageKindFinalReply, bus.OutboundKindFinal, 10252, 0, true},
		{"output-only", bus.OutboundMessageKindFinalReply, bus.OutboundKindFinal, 0, 20, true},
		{"unknown", bus.OutboundMessageKindFinalReply, bus.OutboundKindFinal, 0, 0, false},
		{"thought", bus.OutboundMessageKindThought, "", 10252, 20, false},
		{"tool-calls", bus.OutboundMessageKindToolCalls, "", 10252, 20, false},
		{"interim", "", bus.OutboundKindInterim, 10252, 20, false},
		{"interim-final-kind", bus.OutboundMessageKindFinalReply, bus.OutboundKindInterim, 10252, 20, false},
		{"unmarked-final-kind", bus.OutboundMessageKindFinalReply, "", 10252, 20, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ch := newTestMintClawChannel(t)
			if err := ch.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = ch.Stop(context.Background()) }()
			connection, received, cleanup := newTestMintClawWebSocket(t)
			defer cleanup()
			ch.addConnForTest(&mintclawConn{id: "usage-connection", conn: connection, sessionID: "usage-session"})
			message := bus.OutboundMessage{
				ChatID: "mintclaw:usage-session", Content: "synthetic response",
				Context: bus.InboundContext{MessageID: "usage-request"},
				Metadata: bus.OutboundMetadata{
					MessageKind: test.kind, OutboundKind: test.outbound,
					UsageInputTokens: test.input, UsageOutputTokens: test.output,
				},
			}
			if _, err := ch.sendText(t.Context(), message); err != nil {
				t.Fatal(err)
			}
			select {
			case response := <-received:
				raw, present := response.Payload[PayloadKeyUsage]
				if present != test.want {
					t.Fatalf("usage present = %t, want %t", present, test.want)
				}
				if !test.want {
					return
				}
				usage, ok := raw.(map[string]any)
				if !ok || usage["input_tokens"] != float64(test.input) ||
					usage["output_tokens"] != float64(test.output) ||
					usage["total_tokens"] != float64(test.input+test.output) ||
					response.Payload[PayloadKeyFinal] != true ||
					response.Payload[PayloadKeyRequestID] != "usage-request" {
					t.Fatalf("native final lost usage or correlation: %#v", response.Payload)
				}
			case <-time.After(time.Second):
				t.Fatal("expected native final delivery")
			}
		})
	}
}

func TestSetTurnUsagePayload(t *testing.T) {
	t.Run("populates usage block when counts present", func(t *testing.T) {
		payload := map[string]any{PayloadKeyContent: "hi"}
		setTurnUsagePayload(payload, 1234, 567)

		raw, ok := payload[PayloadKeyUsage]
		if !ok {
			t.Fatalf("expected %q key in payload", PayloadKeyUsage)
		}
		usage, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("usage block is not a map: %T", raw)
		}
		if usage["input_tokens"] != 1234 {
			t.Errorf("input_tokens = %v, want 1234", usage["input_tokens"])
		}
		if usage["output_tokens"] != 567 {
			t.Errorf("output_tokens = %v, want 567", usage["output_tokens"])
		}
		if usage["total_tokens"] != 1801 {
			t.Errorf("total_tokens = %v, want 1801", usage["total_tokens"])
		}
	})

	t.Run("omits usage block when both counts zero", func(t *testing.T) {
		payload := map[string]any{PayloadKeyContent: "hi"}
		setTurnUsagePayload(payload, 0, 0)
		if _, ok := payload[PayloadKeyUsage]; ok {
			t.Errorf("expected no %q key when counts are zero", PayloadKeyUsage)
		}
	})
}

// newCaptureStreamer returns a streamer whose broadcasts are captured into the
// returned map pointer, so tests need no live websocket.
func newCaptureStreamer() (*mintclawStreamer, *map[string]any) {
	var last map[string]any
	ch := &MintClawChannel{}
	ch.broadcastFn = func(_ context.Context, chatID string, msg MintClawMessage) error {
		last = msg.Payload
		return nil
	}
	s := &mintclawStreamer{channel: ch, chatID: "c1"}
	return s, &last
}

func TestStreamerEmitsUsageOnFinalize(t *testing.T) {
	s, last := newCaptureStreamer()
	s.SetTurnUsage(100, 40)

	// sendLocked with empty messageID takes the create branch, which attaches
	// usage from the streamer's stored counts.
	s.mu.Lock()
	err := s.sendLocked(context.Background(), "answer", nil)
	s.mu.Unlock()
	if err != nil {
		t.Fatalf("sendLocked: %v", err)
	}
	if _, ok := (*last)[PayloadKeyUsage]; !ok {
		t.Fatalf("expected usage in payload, got %+v", *last)
	}
}
