package document

import (
	"slices"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

// Dispositions require exact operator instruction evidence. They are not
// generated field values and cannot relax source-required constraints.
type FormIntakeDisposition string

const (
	FormIntakeIntentionallyBlank FormIntakeDisposition = "intentionally_blank"
	FormIntakeNotApplicable      FormIntakeDisposition = "not_applicable"
)

func (disposition FormIntakeDisposition) valid() bool {
	return disposition == "" || disposition == FormIntakeIntentionallyBlank || disposition == FormIntakeNotApplicable
}

func selectFormIntakeDisposition(
	selection *formIntakeSelection, record formJobStoredRecord, key []byte, item formIntakeResolvedProposal,
) error {
	if item.Field.Required {
		selection.Findings = append(
			selection.Findings,
			FormFieldMappingBlocker{FieldID: item.Field.ID, Code: "field_invalid"},
		)
		return nil
	}
	state, reason := FormValueBlanked, FormBlankIntentional
	if item.Proposal.Disposition == FormIntakeNotApplicable {
		state, reason = FormValueNotApplicable, FormBlankNotApplicable
	}
	current, found, err := currentFormIntakeValue(record, key, item.Field.ID)
	if err != nil {
		return err
	}
	proposal := item.Proposal
	if found && current.State == state && current.Value.Kind == ProtectedValueBlank &&
		current.Validation == FormValueValidationValid &&
		(proposal.SupersedesEventID == "" || proposal.SupersedesEventID == current.EventID) && !proposal.ReplaceExisting {
		selection.AcknowledgedIDs = append(selection.AcknowledgedIDs, item.Field.ID)
	} else {
		if (found || item.Field.HasValue) && len(proposal.CorrectionEvidence) == 0 ||
			found && proposal.SupersedesEventID != current.EventID ||
			!found && proposal.SupersedesEventID != "" ||
			!found && item.Field.HasValue && !proposal.ReplaceExisting ||
			!found && !item.Field.HasValue && proposal.ReplaceExisting {
			selection.Findings = append(selection.Findings,
				FormFieldMappingBlocker{FieldID: item.Field.ID, Code: "field_conflicting"})
			return nil
		}
		selection.Payloads = append(selection.Payloads, formJobValuePayload{
			FieldID: item.Field.ID, Value: FormProtectedValue{Kind: ProtectedValueBlank},
			State: state, Source: FormValueSourceUser, Confidence: FormValueConfidenceExact,
			Validation: FormValueValidationValid, BlankReason: reason,
			SupersedesEventID: proposal.SupersedesEventID,
		})
		selection.AcceptedIDs = append(selection.AcceptedIDs, item.Field.ID)
	}
	selection.Accounted = appendFormIntakeSpans(selection.Accounted, item.Spans)
	selection.Accounted = appendFormIntakeSpans(selection.Accounted, item.Correction)
	return nil
}

func formIntakeHasScopedInstruction(input formIntakeStoredInput) bool {
	return slices.ContainsFunc(input.Reservations, func(pass formIntakeStoredPass) bool {
		return pass.Outcome != nil && pass.Outcome.ScopedInstruction
	})
}

// Coverage is derived from the existing reservation list, not a scan cursor or
// an unbounded secondary planner. A scoped clause must survive every view.
func formIntakeScopeCoverageComplete(input formIntakeStoredInput, schema FormFieldsFacts) bool {
	if input.Intent != interactions.ProtectedAnswerValue {
		return true
	}
	for _, field := range schema.Fields {
		if field.ReadOnly {
			continue
		}
		if !slices.ContainsFunc(input.Reservations, func(pass formIntakeStoredPass) bool {
			return pass.Outcome != nil && slices.Contains(pass.CandidateIDs, field.ID)
		}) {
			return false
		}
	}
	return true
}
