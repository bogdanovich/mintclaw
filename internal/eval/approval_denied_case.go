package eval

import (
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/internal/evalfixture"
)

type ApprovalDeniedCase struct{}

func (ApprovalDeniedCase) validate(item Case) error {
	if item.Suite != "general" || item.PDF != nil || item.Browser != nil || item.Steering != nil ||
		item.ReadRecovery != nil || item.NativeStop != nil || len(item.Requires) != 3 ||
		!containsAll(item.Requires, []string{"conversation", boundedCallsCapability, approvalToolCapability}) ||
		len(item.Fixtures) != 1 || item.Fixtures[0].Kind != "file" ||
		item.Fixtures[0].SHA256 != digest([]byte(evalfixture.ApprovalBefore)) ||
		!strings.Contains(item.Task, evalfixture.ApprovalPath) || item.Driver.Mode != "scripted" ||
		len(item.Driver.Facts) != 0 || len(item.Driver.Steps) != 1 ||
		item.Driver.Steps[0] != (Step{WaitFor: Expectation{Kind: "approval"}, Action: Action{Kind: "deny"}}) ||
		item.Limits != (Limits{MaxUserTurns: 2, MaxDriverCalls: 1, TurnTimeout: "2m", CaseTimeout: "5m"}) ||
		len(item.Checks) != 3 || !requiredRubric(item.Rubric) {
		return ErrInvalidCase
	}
	for _, kind := range []string{"approval_denied", "same_session", "last_outcome"} {
		if !slices.ContainsFunc(item.Checks, func(check Check) bool {
			return check.Kind == kind && (kind != "last_outcome" || check.Expected == "final")
		}) {
			return ErrInvalidCase
		}
	}
	return nil
}
