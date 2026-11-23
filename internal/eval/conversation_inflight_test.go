package eval

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
)

type testInFlightDomain struct {
	texts []string
	tool  *activeToolFixture
}

func (*testInFlightDomain) initialTask() string { return "Synthetic in-flight task" }
func (*testInFlightDomain) request(context.Context, Action, Event) (protocol.MintClawMessage, bool, error) {
	return protocol.MintClawMessage{}, false, nil
}
func (*testInFlightDomain) observe(context.Context, Event) error { return nil }
func (*testInFlightDomain) conclude(context.Context, *Trial)     {}
func (domain *testInFlightDomain) afterSend(
	ctx context.Context, request protocol.MintClawMessage, number int, controls inFlightControls,
) error {
	if number == 1 {
		if domain.tool != nil {
			args := activeToolArgs{TrialID: request.SessionID, Slot: 1}
			if err := controls.waitEntered(ctx, domain.tool, args); err != nil {
				return err
			}
		}
		for _, text := range domain.texts {
			// Deliberately ignore errors: the flow must keep refusals sticky.
			_, _ = controls.send(text)
		}
	}
	return nil
}

func TestConversationInFlightRejectsTerminalBeforeToolEntry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		code   string
		kind   string
		events int
	}{
		{"early final", "terminal_before_active_tool", protocol.TypeMessageUpdate, 3},
		{"protocol refusal", "protocol_rejected", protocol.TypeError, 2},
		{"private frame", "private_wire_evidence", protocol.TypeMessageUpdate, 2},
		{"frame limit", "event_limit", "", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const session = "eval-test-session"
			tool := &activeToolFixture{
				barriers: make(map[activeToolArgs]*activeToolBarrier), closing: make(chan struct{}),
			}
			if err := tool.register(session, session, 1); err != nil {
				t.Fatal(err)
			}
			transport := newFakeConversation()
			transport.onSend = func(request protocol.MintClawMessage, _ int) {
				foreign := testResponse(request, 1)
				foreign.SessionID = "unrelated-session"
				transport.events <- foreign
				stale := testResponse(request, 1)
				stale.Payload[protocol.PayloadKeyRequestID] = "unrelated-request"
				transport.events <- stale
				partial := testResponse(request, 1)
				partial.Payload[protocol.PayloadKeyFinal] = false
				transport.events <- partial
				if tc.kind == "" {
					for range 256 {
						transport.events <- stale
					}
					return
				}
				terminal := testResponse(request, 1)
				terminal.Type = tc.kind
				if tc.name == "private frame" {
					terminal.Payload[protocol.PayloadKeyContent] = "private-test-marker"
				}
				transport.events <- terminal
			}
			item := testCase()
			item.Rubric = nil
			data, err := json.Marshal(item)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			trial := runDomainConversation(ctx, transport, LoadedCase{Case: item, Digest: digest(data)},
				session, "main", "/synthetic/eval",
				conversationRoles{filter: strings.NewReplacer("private-test-marker", "[REDACTED]")},
				&testInFlightDomain{texts: []string{"must not be sent"}, tool: tool},
			)
			if trial.ExecutionCode != tc.code || trial.ExecutionVerdict == Pass || trial.EvidenceEligible ||
				len(trial.Events) != tc.events || len(transport.sent) != 1 || transport.closed != 1 {
				t.Fatalf(
					"unexpected early exit: %s/%s, events=%d sent=%d closed=%d",
					trial.ExecutionVerdict,
					trial.ExecutionCode,
					len(trial.Events),
					len(transport.sent),
					transport.closed,
				)
			}
			if tc.name == "early final" {
				reply := trial.Events[2]
				if reply.Kind != "early_terminal" || reply.RequestID != transport.sent[0].ID ||
					reply.WireType != protocol.TypeMessageUpdate || reply.MessageID == "" ||
					reply.Content != "" || reply.Usage != nil || lastOutcome(trial.Events).ID != "" {
					t.Fatal("the correlated terminal reply was not retained as diagnostic metadata")
				}
				assertComparableEarlyReply(t, trial.Events)
			}
		})
	}
}

func assertComparableEarlyReply(t *testing.T, transcript []Event) {
	t.Helper()
	report := comparisonFixture(t, Pass, Inconclusive)
	trial := comparisonTrial(t, report, 1)
	trial.Events = transcript
	report.NativeModels = &NativeModelObservation{
		Scope: nativeModelScope, Code: "unavailable", Reason: "no_scopes", AllCalls: "unknown",
		Totals: summarizeNativeModels(nil),
	}
	writeAdmissionJSON(t, filepath.Join(report.ReportDir, report.Trials[1].File), trial)
	writeAdmissionJSON(t, filepath.Join(report.ReportDir, "run.json"), report)
	comparison, err := Compare(report.ReportDir, report.ReportDir)
	if err != nil || len(comparison.Cases) != 1 || comparison.Cases[0].Current.Coverage[Inconclusive] != 1 ||
		comparison.Cases[0].Current.Coverage[Pass] != 1 {
		t.Fatal("a trace-bearing early wire reply invalidated the saved report or changed coverage", err)
	}
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
