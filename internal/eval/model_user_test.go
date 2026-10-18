package eval

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
)

func TestUserDecisionRetainsOnlyValidatedActionKind(t *testing.T) {
	for _, test := range []struct {
		name, output, kind, code string
	}{
		{"message", `{"kind":"message","text":"ordinary-message-canary"}`, "message", "user_action_received"},
		{"clarify", `{"kind":"clarify"}`, "clarify", "user_action_received"},
		{"protected", `{"kind":"protected_answer","fact":"test-fact"}`, "protected_answer", "user_action_received"},
		{"stop", `{"kind":"stop"}`, "stop", "user_action_received"},
		{"unknown", `{"kind":"unknown-action-canary"}`, "", "invalid_user_action"},
		{"private", `{"kind":"message","text":"private-fact-canary"}`, "", "user_call_failed"},
		{"credential", `{"kind":"message","text":"private-credential-canary"}`, "", "user_call_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			item := rolePromptCase()
			item.Driver.Facts = map[string]string{"test-fact": "private-fact-canary"}
			roles, user, _ := adaptiveRoles(test.output, "")
			roles.user.filter = strings.NewReplacer("private-credential-canary", "[withheld]")
			decision := nextModelAction(t.Context(), roles.user, item, rolePromptEvents(), "question", 3)
			if decision.Code != test.code || decision.Call == nil || decision.Call.ActionKind != test.kind ||
				user.calls != 1 {
				t.Fatal("decision kind was lost, fabricated, or retained before validation", decision.Code)
			}
			data, err := json.Marshal(decision.Call)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{
				"ordinary-message-canary", "private-fact-canary", "private-credential-canary", "unknown-action-canary",
				"test-fact", `"text"`, `"fact"`,
			} {
				if bytes.Contains(data, []byte(forbidden)) {
					t.Fatal("decision metadata retained text, a fact handle, or unvalidated model output")
				}
			}
			if bytes.Contains(data, []byte("action_kind")) != (test.kind != "") {
				t.Fatal("missing decision kind was serialized as a known action")
			}
		})
	}
}

func TestAdaptiveRejectedPDFOrdinaryNavigationRetainsDecisionKind(t *testing.T) {
	for _, kind := range []string{"clarify", "back", "skip", "not_applicable"} {
		t.Run(kind, func(t *testing.T) {
			prepared := newPDFTrialFixture(t)
			defer func() { _ = prepared.release(true) }()
			prepared.item.Case.Rubric = nil
			ordinary := &questionConversation{
				home: prepared.home, workspace: prepared.workspace, sessionID: prepared.sessionID,
				agent: prepared.agent, started: prepared.started,
			}
			transport := newFakeConversation()
			transport.onSend = func(request protocol.MintClawMessage, number int) {
				if number != 1 {
					t.Fatal("refused ordinary navigation reached the target or was replayed")
				}
				_, _, response := ordinaryQuestionFixture(t, ordinary, request, false)
				response.Payload[protocol.PayloadKeyAgentID] = prepared.agent
				transport.events <- response
			}
			output, err := json.Marshal(Action{Kind: kind})
			if err != nil {
				t.Fatal(err)
			}
			roles, user, judge := adaptiveRoles(string(output), "")
			domain := &pdfConversation{prepared: prepared}
			trial := runDomainConversation(t.Context(), transport, prepared.item, prepared.sessionID,
				prepared.agent, prepared.workspace, roles, domain)
			if trial.Verdict != Inconclusive || trial.Code != "domain_input_not_admitted" || trial.EvidenceEligible ||
				len(transport.sent) != 1 || len(trial.DriverCalls) != 1 || trial.DriverCalls[0].ActionKind != kind ||
				user.calls != 1 || judge.calls != 0 {
				t.Fatal("refused decision kind was lost or navigation authority changed", trial.Code)
			}
		})
	}
}
