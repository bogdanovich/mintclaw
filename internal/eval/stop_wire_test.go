package eval

import (
	"context"
	"testing"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
)

// This exercises transport correlation, never a native-stop semantic pass.
type stopWireDomain struct {
	selection  string
	actual     string
	command    string
	observeErr error
	observed   int
}

func (*stopWireDomain) initialTask() string { return "Synthetic held read." }
func (*stopWireDomain) request(context.Context, Action, Event) (protocol.MintClawMessage, bool, error) {
	return protocol.MintClawMessage{}, false, nil
}
func (*stopWireDomain) observe(context.Context, Event) error { return nil }
func (*stopWireDomain) conclude(context.Context, *Trial)     {}
func (domain *stopWireDomain) stopRequestID() string {
	if domain.selection != "" {
		return domain.selection
	}
	return domain.actual
}

func (domain *stopWireDomain) observeStop(context.Context, Event) error {
	domain.observed++
	return domain.observeErr
}

func (domain *stopWireDomain) afterSend(
	_ context.Context, _ protocol.MintClawMessage, number int, send func(string) (Event, error),
) error {
	if number != 1 {
		return nil
	}
	if _, err := send("Synthetic queued guidance."); err != nil {
		return err
	}
	input, err := send(domain.command)
	domain.actual = input.RequestID
	return err
}

func TestNativeStopWireSelectsOnlyActualStopAndRequiresObserver(t *testing.T) {
	for _, name := range []string{
		"positive_wire_only", "forged_id", "not_stop", "native_unknown", "native_private", "llm_reply", "wrong_route",
		"unadmitted", "no_driver",
	} {
		t.Run(name, func(t *testing.T) {
			item := nativeStopDefinition(t).Case
			domain := &stopWireDomain{command: "/stop"}
			want := Pass
			switch name {
			case "forged_id":
				domain.selection, want = "never-sent", Inconclusive
			case "not_stop":
				domain.command, want = "not a stop command", Inconclusive
			case "native_unknown":
				domain.observeErr, want = errNativeEvidence, Inconclusive
			case "native_private":
				domain.observeErr, want = ErrPrivateEvidence, Fail
			case "llm_reply", "no_driver":
				want = Inconclusive
			case "wrong_route":
				want = Fail
			case "unadmitted":
				item, want = testCase(), Inconclusive
			}
			transport := newFakeConversation()
			var primary protocol.MintClawMessage
			transport.onSend = func(request protocol.MintClawMessage, number int) {
				if number == 1 {
					primary = request
				}
				if number == 3 {
					transport.events <- testResponse(primary, 1)
					reply := testResponse(request, number)
					if name != "llm_reply" {
						delete(reply.Payload, protocol.PayloadKeyTraceScopes)
					}
					if name == "wrong_route" {
						reply.Payload[protocol.PayloadKeyAgentID] = "other"
					}
					transport.events <- reply
				}
				if number == 4 {
					reply := testResponse(request, number)
					reply.Payload[protocol.PayloadKeyContent] = item.NativeStop.ReuseMarker
					transport.events <- reply
				}
			}
			var adapter conversationDomain = domain
			if name == "no_driver" {
				adapter = nil
			}
			trial := runDomainConversation(t.Context(), transport, stopLoadedCase(t, item),
				"eval-stop-session", "main", "/synthetic/eval", conversationRoles{}, adapter)
			if trial.ExecutionVerdict != want || transport.closed != 1 || trial.Verdict == Pass {
				t.Fatal("wire control bypassed native evidence or correlation", trial.ExecutionVerdict, trial.Code)
			}
			if name == "positive_wire_only" && (len(transport.sent) != 4 || domain.observed != 1 ||
				trial.Events[3].Kind != "stopped" || trial.Events[3].RequestID != transport.sent[2].ID) {
				t.Fatal("stop ACK was bound to the original task instead of the actual control input", trial.Events)
			}
			if name == "no_driver" && len(transport.sent) != 0 {
				t.Fatal("missing stop driver reached target work")
			}
		})
	}
}
