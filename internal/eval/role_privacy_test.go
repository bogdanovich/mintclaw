package eval

import (
	"errors"
	"strings"
	"testing"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func TestRoleCredentialBoundaryRefusesBeforeCallsAndWithholdsOutput(t *testing.T) {
	filter := strings.NewReplacer("synthetic-role-credential", "[redacted]")
	for _, text := range []string{
		"synthetic-role-credential",
		`{"key":"synthetic\u002drole-credential"}`,
		"Key: synthetic\\u002drole-credential\nOrdinary prose follows.",
	} {
		provider := &roleTestProvider{mediated: true, response: &providers.LLMResponse{
			Content: text, FinishReason: "stop",
			Usage: &providers.UsageInfo{PromptTokens: 10252, CompletionTokens: 20, TotalTokens: 10272},
		}}
		role := &ModelRole{provider: provider, filter: filter, identity: RoleIdentity{Role: "judge"}}
		output, call, err := role.Call(t.Context(), "test-v1", "Public system.", text)
		if !errors.Is(err, ErrPrivateEvidence) || output != "" || call.Attempted || provider.calls != 0 {
			t.Fatal("credential-bearing input reached provider")
		}
		output, call, err = role.Call(t.Context(), "test-v1", "Public system.", "Public input.")
		if !errors.Is(err, ErrPrivateEvidence) || output != "" || !call.Attempted || provider.calls != 1 ||
			call.Code != "private_output" || call.Usage == nil || call.Usage.PromptTokens != 10252 {
			t.Fatal("private output was retained or usage was lost")
		}
	}
}

func TestPrivateJudgeOutputCannotBecomeEligibleForRegrade(t *testing.T) {
	transport := newFakeConversation()
	transport.onSend = func(request protocol.MintClawMessage, number int) {
		transport.events <- testResponse(request, number)
	}
	roles, _, judge := adaptiveRoles(`{"kind":"finish","text":"Give the final answer."}`,
		strings.ReplaceAll(adaptiveJudgePass, "The visible choices agree.", "synthetic-role-credential"))
	roles.judge.filter = strings.NewReplacer("synthetic-role-credential", "[redacted]")
	trial := runAdaptiveTest(t, transport, rolePromptCase(), roles)
	if trial.Verdict != Fail || trial.ExecutionVerdict != Fail || trial.EvidenceEligible ||
		trial.Judge == nil || trial.Judge.Code != "private_judge_output" || judge.calls != 1 ||
		len(trial.Judge.Findings.Criteria) != 0 {
		t.Fatal("privacy failure was eligible for a new passing assessment")
	}
}
