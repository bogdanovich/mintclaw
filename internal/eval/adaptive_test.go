package eval

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func adaptiveRoles(userOutput, judgeOutput string) (conversationRoles, *roleTestProvider, *roleTestProvider) {
	user := &roleTestProvider{
		mediated: true,
		response: &providers.LLMResponse{Content: userOutput, FinishReason: "stop"},
	}
	judge := &roleTestProvider{
		mediated: true,
		response: &providers.LLMResponse{Content: judgeOutput, FinishReason: "stop"},
	}
	return conversationRoles{
		user:  &ModelRole{provider: user, identity: RoleIdentity{Role: "user"}},
		judge: &ModelRole{provider: judge, identity: RoleIdentity{Role: "judge"}},
	}, user, judge
}

const adaptiveJudgePass = `{"version":1,"criteria":[{"id":"coherent","verdict":"pass",` +
	`"reason":"The visible choices agree.","evidence_ids":["event-000002"]}]}`

func runAdaptiveTest(t *testing.T, transport *fakeConversationTransport, item Case, roles conversationRoles) Trial {
	t.Helper()
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	return runConversation(t.Context(), transport, LoadedCase{Case: item, Digest: digest(data)},
		"eval-adaptive-session", "main", "/synthetic/eval", roles)
}

func TestAdaptiveConversationUsesExistingTurnEngineAndSeparateJudge(t *testing.T) {
	for _, wording := range []string{"Please give the final answer.", "That covers it; confirm the result."} {
		transport := newFakeConversation()
		transport.onSend = func(request protocol.MintClawMessage, number int) {
			transport.events <- testResponse(request, number)
		}
		data, _ := json.Marshal(Action{Kind: "finish", Text: wording})
		roles, user, judge := adaptiveRoles(string(data), adaptiveJudgePass)
		item := rolePromptCase()
		item.Checks = append(item.Checks, Check{ID: "session", Kind: "same_session"})
		trial := runAdaptiveTest(t, transport, item, roles)
		if trial.Verdict != Pass || trial.ExecutionVerdict != Pass || !trial.EvidenceEligible ||
			len(transport.sent) != 2 || transport.closed != 1 || user.calls != 1 || judge.calls != 1 ||
			len(trial.DriverCalls) != 1 || trial.Judge == nil || trial.Judge.Call == nil ||
			transport.sent[0].SessionID != transport.sent[1].SessionID ||
			transport.sent[0].ID == transport.sent[1].ID || transport.sent[1].Payload[protocol.PayloadKeyContent] != wording {
			t.Fatal("adaptive flow replayed input, changed session, lost roles, or failed", trial.Verdict, trial.Code)
		}
	}
}

func TestAdaptiveStopLimitsAndMalformedActionsCannotPass(t *testing.T) {
	for _, test := range []struct {
		output, code string
		sends, calls int
	}{
		{`{"kind":"stop"}`, "driver_stopped", 1, 1},
		{`{"kind":"allow_once"}`, "invalid_user_action", 1, 1},
		{`{"kind":"message","text":"Another followup."}`, "user_turn_limit", 3, 2},
	} {
		transport := newFakeConversation()
		transport.onSend = func(request protocol.MintClawMessage, number int) {
			transport.events <- testResponse(request, number)
		}
		roles, user, judge := adaptiveRoles(test.output, adaptiveJudgePass)
		item := rolePromptCase()
		item.Limits.MaxUserTurns = 3
		trial := runAdaptiveTest(t, transport, item, roles)
		if trial.Verdict == Pass || trial.Code != test.code || len(transport.sent) != test.sends ||
			user.calls != test.calls || judge.calls != 0 || trial.EvidenceEligible || transport.closed != 1 {
			t.Fatal("stop/limits/invalid action became success or reached judge", trial.Verdict, trial.Code)
		}
	}
}

func TestAdaptiveMandatoryFailureSurvivesJudgePassOrError(t *testing.T) {
	for _, judgeError := range []bool{false, true} {
		transport := newFakeConversation()
		transport.onSend = func(request protocol.MintClawMessage, number int) {
			transport.events <- testResponse(request, number)
		}
		roles, _, judge := adaptiveRoles(`{"kind":"finish","text":"Give the final answer."}`, adaptiveJudgePass)
		if judgeError {
			judge.err = errors.New("private provider detail")
		}
		item := rolePromptCase()
		item.Checks = append(item.Checks, Check{ID: "missing", Kind: "response_contains", Expected: "not-present"})
		trial := runAdaptiveTest(t, transport, item, roles)
		if trial.Verdict != Fail || !trial.EvidenceEligible || trial.Judge == nil || judge.calls != 1 {
			t.Fatal("judge overwrote an independently verified failure", trial.Verdict, trial.Code)
		}
	}
}

func TestAdaptiveUnverifiedRouteNeverReachesModelRoles(t *testing.T) {
	transport := newFakeConversation()
	transport.onSend = func(request protocol.MintClawMessage, number int) {
		response := testResponse(request, number)
		response.Payload[protocol.PayloadKeyAgentID] = "wrong-agent"
		transport.events <- response
	}
	roles, user, judge := adaptiveRoles(`{"kind":"finish","text":"Done."}`, adaptiveJudgePass)
	trial := runAdaptiveTest(t, transport, rolePromptCase(), roles)
	if trial.Verdict != Fail || trial.EvidenceEligible || user.calls != 0 || judge.calls != 0 {
		t.Fatal("unverified route evidence was sent to model roles")
	}
}

func TestAdaptiveRejectsOversizedDriverContextBeforeCall(t *testing.T) {
	transport := newFakeConversation()
	transport.onSend = func(request protocol.MintClawMessage, number int) {
		response := testResponse(request, number)
		response.Payload[protocol.PayloadKeyContent] = strings.Repeat("x", MaxInputBytes)
		transport.events <- response
	}
	roles, user, judge := adaptiveRoles(`{"kind":"finish","text":"Done."}`, adaptiveJudgePass)
	trial := runAdaptiveTest(t, transport, rolePromptCase(), roles)
	if trial.Verdict == Pass || user.calls != 0 || judge.calls != 0 || len(transport.sent) != 1 {
		t.Fatal("unbounded context reached a model")
	}
}
