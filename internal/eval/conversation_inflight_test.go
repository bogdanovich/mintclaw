package eval

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
)

type testInFlightDomain struct {
	texts []string
}

func (*testInFlightDomain) initialTask() string { return "Synthetic in-flight task" }
func (*testInFlightDomain) request(context.Context, Action, Event) (protocol.MintClawMessage, bool, error) {
	return protocol.MintClawMessage{}, false, nil
}
func (*testInFlightDomain) observe(context.Context, Event) error { return nil }
func (*testInFlightDomain) conclude(context.Context, *Trial)     {}
func (domain *testInFlightDomain) afterSend(
	_ context.Context, _ protocol.MintClawMessage, number int, send func(string) (Event, error),
) error {
	if number == 1 {
		for _, text := range domain.texts {
			// Deliberately ignore errors: the flow must keep refusals sticky.
			_, _ = send(text)
		}
	}
	return nil
}

func TestConversationInFlightSenderPrecedesFinalAndCannotBypassLimits(t *testing.T) {
	for _, test := range []struct {
		name   string
		texts  []string
		limit  int
		filter *strings.Replacer
		want   Verdict
		sent   int
	}{
		{name: "in_flight", texts: []string{"correction-b"}, limit: 3, want: Pass, sent: 3},
		{name: "two_inputs", texts: []string{"correction-b", "correction-c"}, limit: 4, want: Pass, sent: 4},
		{name: "case_limit", texts: []string{"correction-b", "correction-c"}, limit: 2, want: Inconclusive, sent: 2},
		{name: "hook_limit", texts: []string{"b", "c", "d", "e"}, limit: 5, want: Inconclusive, sent: 3},
		{name: "oversized", texts: []string{strings.Repeat("x", MaxInputBytes+1)}, limit: 2, want: Inconclusive, sent: 1},
		{
			name: "private", texts: []string{"secret-guidance", "later"}, limit: 3,
			filter: strings.NewReplacer("secret-guidance", "[REDACTED]"), want: Fail, sent: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := newFakeConversation()
			var first protocol.MintClawMessage
			transport.onSend = func(request protocol.MintClawMessage, number int) {
				switch number {
				case 1:
					first = request
				case 2:
					// A terminal response becomes available only after guidance.
					transport.events <- testResponse(first, 1)
				default:
					transport.events <- testResponse(request, number)
				}
			}
			item := testCase()
			item.Rubric = nil
			item.Limits.MaxUserTurns = test.limit
			data, err := json.Marshal(item)
			if err != nil {
				t.Fatal(err)
			}
			trial := runDomainConversation(t.Context(), transport,
				LoadedCase{Case: item, Digest: digest(data)}, "eval-test-session", "main", "/synthetic/eval",
				conversationRoles{filter: test.filter}, &testInFlightDomain{texts: test.texts})
			if trial.Verdict != test.want || len(transport.sent) != test.sent || transport.closed != 1 {
				t.Fatal("in-flight boundary or sticky limit changed", trial.Verdict, trial.Code, len(transport.sent))
			}
			inputs, identities := 0, map[string]bool{}
			for _, event := range trial.Events {
				if event.Kind != "input" {
					continue
				}
				if event.Turn < 1 || event.Turn > 2 || identities[event.RequestID] || !validID(event.RequestID) {
					t.Fatal("input event lost actual wire identity", event)
				}
				identities[event.RequestID] = true
				inputs++
			}
			if inputs != test.sent {
				t.Fatal("transcript contains synthetic sends or dropped successful sends")
			}
		})
	}
}
