package agent

import (
	"reflect"

	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

// verifiedPresentationRepair owns the already verified execution result across
// one model-only presentation call. Model instructions do not own this proof.
type verifiedPresentationRepair struct {
	original terminalContent
	outcome  *taskresult.Outcome
}

func newVerifiedPresentationRepair(
	original terminalContent,
	audits []toolshared.WriteAuditEntry,
	receipts []taskresult.Receipt,
	checklist []runtimeObjectiveItem,
) *verifiedPresentationRepair {
	_, outcome := extractObjectiveOutcomeWithReceipts(original.content, audits, receipts, true, checklist)
	if outcome.Status != taskresult.OutcomeSucceeded || outcome.UserSummary != "" {
		return nil
	}
	if _, exact := declaredExactJSONObjectiveOutput(outcome); exact {
		return nil
	}
	return &verifiedPresentationRepair{original: original, outcome: outcome}
}

func (repair *verifiedPresentationRepair) accept(
	candidate terminalContent,
	audits []toolshared.WriteAuditEntry,
	receipts []taskresult.Receipt,
	checklist []runtimeObjectiveItem,
) terminalContent {
	_, outcome := extractObjectiveOutcomeWithReceipts(candidate.content, audits, receipts, true, checklist)
	if outcome.UserSummary == "" {
		return repair.original
	}
	// Compare canonical verified fields, not raw JSON spelling or prose. Empty
	// receipt arrays and omitted arrays have the same validated representation.
	// A presentation call cannot change outputs, receipt ownership, or status.
	outcome.UserSummary = ""
	if !reflect.DeepEqual(repair.outcome, outcome) {
		return repair.original
	}
	candidate.protected = candidate.protected || repair.original.protected
	return candidate
}
