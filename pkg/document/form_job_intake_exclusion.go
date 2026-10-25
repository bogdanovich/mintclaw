package document

import (
	"slices"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

// Exclusion settles only an earlier candidate association, never a value,
// disposition, native validation blocker or later interpretation. Semantic
// verification is private; native code owns exact identity, chronology and apply.
func validateFormIntakeCandidateExclusions(
	mode *formIntakeStoredMode, index int, pass formIntakeStoredPass, request FormIntakeApplyRequest,
) ([]string, error) {
	jobID, _, err := parseFormIntakeReceipt(request.Reference)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, item := range request.Exclusions {
		if mode.Inputs[index].Intent != interactions.ProtectedAnswerValue || !item.Verified ||
			!slices.Contains(pass.CandidateIDs, item.FieldID) || slices.Contains(ids, item.FieldID) ||
			slices.ContainsFunc(
				request.Proposals,
				func(p FormIntakeProposal) bool { return p.FieldID == item.FieldID },
			) ||
			slices.ContainsFunc(
				request.Findings,
				func(f FormFieldMappingBlocker) bool { return f.FieldID == item.FieldID },
			) {
			return nil, ErrFormJobAnswerConflict
		}
		field := slices.IndexFunc(request.Schema.Fields, func(field FormField) bool { return field.ID == item.FieldID })
		if field < 0 || request.Schema.Fields[field].ReadOnly || !slices.ContainsFunc(
			formIntakeCurrentModeFindings(mode, mode.Inputs[index]), func(f FormFieldMappingBlocker) bool {
				return f.FieldID == item.FieldID &&
					(f.Code == "field_ambiguous" || f.Code == "field_confirmation_required")
			},
		) {
			return nil, ErrFormJobAnswerConflict
		}
		job, inputID, err := parseFormIntakeReceipt(item.EvidenceReference)
		other := formIntakeInputIndex(mode, inputID)
		if err != nil || job != jobID || other < 0 || other >= pass.ReceiptCount ||
			formIntakeAdmissionOrder(mode, other) <= formIntakeAdmissionOrder(mode, index) {
			return nil, ErrFormJobAnswerConflict
		}
		proof := mode.Inputs[other]
		if proof.State != formIntakeInputCommitted || proof.Intent != interactions.ProtectedAnswerValue ||
			len(proof.Reservations) != item.ExpectedPasses {
			return nil, ErrFormJobConflict
		}
		if _, err = resolveFormIntakeLiteral(proof.Text, item.Evidence); err != nil {
			return nil, err
		}
		ids = append(ids, item.FieldID)
	}
	slices.Sort(ids)
	return ids, nil
}
