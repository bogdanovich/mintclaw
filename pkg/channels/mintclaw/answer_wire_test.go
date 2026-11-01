package mintclaw

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestOrdinaryAnswerWireRejectsInvalidUTF8BeforeJSONRepair(t *testing.T) {
	for _, test := range []struct {
		name    string
		content []byte
		valid   bool
	}{
		{"invalid-byte", []byte{0xff}, false},
		{"truncated-rune", []byte{0xd0}, false},
		{"literal-replacement-rune", []byte("\ufffd"), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			messageBus := bus.NewMessageBus()
			settings := &config.MintClawSettings{}
			settings.SetToken("test-token")
			channel, err := NewMintClawChannel(&config.Channel{
				Type: config.ChannelMintClaw, Enabled: true, AllowFrom: []string{"mintclaw-user"},
			}, settings, messageBus)
			if err != nil {
				t.Fatal(err)
			}
			if err = channel.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = channel.Stop(context.Background()) })
			server := httptest.NewServer(http.HandlerFunc(channel.handleWebSocket))
			t.Cleanup(server.Close)
			conn, response, err := websocket.DefaultDialer.Dial(
				"ws"+strings.TrimPrefix(server.URL, "http")+"?session_id=wire-session",
				http.Header{"Authorization": {"Bearer test-token"}},
			)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			t.Cleanup(func() { _ = conn.Close() })
			raw, err := json.Marshal(MintClawMessage{
				Type: TypeMessageSend, ID: "wire-answer", SessionID: "wire-session",
				Payload: map[string]any{
					PayloadKeyContent: "wire-content-marker", PayloadKeyInteractionChoice: InteractionChoiceAnswer,
					PayloadKeyInteractionShortID: "question-short", PayloadKeyInteractionPrompt: "delivered-prompt",
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			// Write raw bytes; WriteJSON would repair the invalid fixture first.
			raw = bytes.Replace(raw, []byte("wire-content-marker"), test.content, 1)
			if err = conn.WriteMessage(websocket.TextMessage, raw); err != nil {
				t.Fatal(err)
			}
			if err = conn.WriteJSON(MintClawMessage{Type: TypePing, ID: "wire-barrier"}); err != nil {
				t.Fatal(err)
			}
			if err = conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
			var reply MintClawMessage
			if err = conn.ReadJSON(&reply); err != nil {
				t.Fatal(err)
			}
			if !test.valid {
				if reply.Type != TypeError || reply.Payload["code"] != "invalid_message" {
					t.Fatal("invalid UTF-8 answer was repaired/admitted instead of rejected", reply.Type)
				}
				if err = conn.ReadJSON(&reply); err != nil {
					t.Fatal(err)
				}
			}
			if reply.Type != TypePong || reply.ID != "wire-barrier" {
				t.Fatal("wire barrier did not settle after the answer")
			}
			select {
			case inbound := <-messageBus.InboundChan():
				if !test.valid || inbound.Content != string(test.content) ||
					inbound.Context.Interaction.Response != string(test.content) ||
					inbound.Context.Interaction.ResponseMessageID != "delivered-prompt" {
					t.Fatal("wire answer was altered or invalid input reached the native bus")
				}
			default:
				if test.valid {
					t.Fatal("valid replacement rune was rejected as a malformed encoding")
				}
			}
		})
	}
}
