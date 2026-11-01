package eval

import (
	"context"
	"encoding/json"
	"testing"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
)

type finalWireTransport struct {
	*fakeConversationTransport
	wire *finalDeliveryWire
}

func (transport *finalWireTransport) Receive(ctx context.Context) (protocol.MintClawMessage, error) {
	message, err := transport.fakeConversationTransport.Receive(ctx)
	if err == nil {
		err = transport.wire.observe(message)
	}
	return message, err
}

type finalWireControlDomain struct{ *finalDeliveryConversation }

// Only the wire/conversation boundary is under test here. Real native
// receipts, settlement and frozen grading have separate native CLI controls.
func (*finalWireControlDomain) observe(context.Context, Event) error { return nil }

func TestFinalDeliveryWireSeesLateFirstFinalBeforeFollowupResponse(t *testing.T) {
	for _, kind := range []string{"positive", "late_final", "late_update"} {
		t.Run(kind, func(t *testing.T) {
			item := finalDeliveryDefinition(t).Case
			item.FinalDelivery.Streaming = false
			wire := &finalDeliveryWire{sessionID: "eval-test-session"}
			fake := newFakeConversation()
			transport := &finalWireTransport{fakeConversationTransport: fake, wire: wire}
			var first protocol.MintClawMessage
			fake.onSend = func(request protocol.MintClawMessage, number int) {
				response := testResponse(request, number)
				response.Type = protocol.TypeMessageCreate
				response.Payload[protocol.PayloadKeyContent] = "Sorted synthetic blocks."
				if number == 1 {
					first = response
				} else {
					if kind != "positive" {
						late := first
						if kind == "late_update" {
							late.Type = protocol.TypeMessageUpdate
							late.Payload = map[string]any{}
							for key, value := range first.Payload {
								late.Payload[key] = value
							}
							late.Payload[protocol.PayloadKeyFinal] = false
						}
						// This old request frame is read while waiting for request 2;
						// the shared loop intentionally filters it out of trial.Events.
						fake.events <- late
					}
					response.Payload[protocol.PayloadKeyContent] = item.FinalDelivery.Marker
				}
				fake.events <- response
			}
			domain := &finalDeliveryConversation{item: item, sessionID: wire.sessionID, wire: wire}
			data, _ := json.Marshal(item)
			trial := runDomainConversation(t.Context(), transport, LoadedCase{Case: item, Digest: digest(data)},
				wire.sessionID, "main", "/synthetic/eval", conversationRoles{}, &finalWireControlDomain{domain})
			domain.freezeWire(&trial, fake.Failure() == nil)
			if trial.ExecutionVerdict != Pass || len(trial.Events) != 4 || len(fake.sent) != 2 {
				t.Fatal("control did not cross the real follow-up filtering boundary", trial.ExecutionCode)
			}
			if _, code := finalDeliveryRevisions(trial, item, "/synthetic/eval"); code != "" {
				t.Fatal("old transcript-only oracle did not demonstrate its blind spot", code)
			}
			code := finalDeliveryWireCode(trial, item, "/synthetic/eval")
			if (code == "") != (kind == "positive") {
				t.Fatal("late old-request frame escaped the complete wire census", kind, code)
			}
		})
	}
}

func TestFinalDeliveryWireRefusesScopeLimitsAndIncompleteCapture(t *testing.T) {
	for _, kind := range []string{"session", "unowned_final", "frame_limit", "byte_limit", "request_limit", "incomplete"} {
		t.Run(kind, func(t *testing.T) {
			wire := &finalDeliveryWire{sessionID: "eval-test-session"}
			request := protocol.MintClawMessage{ID: "owned-request", SessionID: wire.sessionID}
			if wire.register(request) != nil {
				t.Fatal("register control")
			}
			response := testResponse(request, 1)
			switch kind {
			case "session":
				response.SessionID = "foreign-session"
			case "unowned_final":
				response.Payload[protocol.PayloadKeyRequestID] = "unowned-request"
			case "frame_limit":
				wire.count = 256
			case "byte_limit":
				wire.bytes = 2 * 1024 * 1024
			case "request_limit":
				request.ID = "another-request"
				if wire.register(request) != nil {
					t.Fatal("second registration")
				}
				request.ID = "excess-request"
				if wire.register(request) == nil {
					t.Fatal("third request accepted")
				}
			case "incomplete":
			}
			err := wire.observe(response)
			if kind != "incomplete" && err == nil {
				t.Fatal("wire guard did not refuse", kind)
			}
			if wire.snapshot(false).Complete {
				t.Fatal("partial/failed wire capture claimed complete")
			}
		})
	}
}
