package document

import (
	"slices"
)

// This runs inside the current native apply's atomic update, after positive
// validation and before either accepted events or source accounting is published.
func (store *FormJobStore) resolvePriorFormIntakeLiterals(
	source *formJobSourceSecret, currentIndex int, record formJobStoredRecord, key []byte,
	request FormIntakeApplyRequest, selection formIntakeSelection, digest string,
) error {
	type resolution struct {
		spans []formIntakeSpan
		ids   []string
	}
	resolved := make(map[int]*resolution)
	for _, proposal := range request.Proposals {
		if len(proposal.Resolves) == 0 {
			continue
		}
		if proposal.Disposition != "" ||
			!slices.Contains(selection.AcceptedIDs, proposal.FieldID) &&
				!slices.Contains(selection.AcknowledgedIDs, proposal.FieldID) {
			return ErrFormJobAnswerConflict
		}
		value, found, err := currentFormIntakeValue(record, key, proposal.FieldID)
		if err != nil {
			return err
		}
		if !found || value.State != FormValueConfirmed || value.Validation != FormValueValidationValid {
			return ErrFormJobAnswerConflict
		}
		for _, prior := range proposal.Resolves {
			jobID, inputID, err := parseFormIntakeReceipt(prior.Reference)
			index := formIntakeInputIndex(source.Intake, inputID)
			if err != nil || jobID != record.Public.JobID || index < 0 || index >= currentIndex {
				return ErrFormJobAnswerConflict
			}
			input := source.Intake.Inputs[index]
			if input.State != formIntakeInputCommitted || len(input.Reservations) != prior.ExpectedPasses {
				return ErrFormJobConflict
			}
			findings := formIntakeCurrentFindings(input)
			findingIndex := slices.IndexFunc(findings, func(finding FormFieldMappingBlocker) bool {
				return finding.FieldID == proposal.FieldID &&
					(finding.Code == "field_invalid" || finding.Code == "field_conflicting")
			})
			if findingIndex < 0 {
				return ErrFormJobAnswerConflict
			}
			span, err := resolveFormIntakeLiteral(input.Text, prior.Literal)
			if err != nil {
				return err
			}
			span.FieldID = proposal.FieldID
			if slices.ContainsFunc(input.Accounted, func(prior formIntakeSpan) bool {
				return formIntakeSpansOverlap(span, prior)
			}) {
				return ErrFormJobAnswerConflict
			}
			item := resolved[index]
			if item == nil {
				item = &resolution{}
				resolved[index] = item
			}
			if slices.ContainsFunc(item.spans, func(prior formIntakeSpan) bool {
				return formIntakeSpansOverlap(span, prior)
			}) {
				return ErrFormJobAnswerConflict
			}
			item.spans = append(item.spans, span)
			if !slices.Contains(item.ids, proposal.FieldID) {
				item.ids = append(item.ids, proposal.FieldID)
			}
		}
	}
	// Use stable input order for deterministic native reservation identities.
	for index := 0; index < currentIndex; index++ {
		item := resolved[index]
		if item == nil {
			continue
		}
		input := &source.Intake.Inputs[index]
		if len(input.Reservations) >= MaxFormIntakeExtractionPasses ||
			len(input.Accounted)+len(item.spans) > MaxFormIntakeAccountedSpans {
			return ErrFormJobCapacityExceeded
		}
		slices.Sort(item.ids)
		id := keyedDigest(store.profileKey, "intake_prior_resolution", request.PassID, input.ID, digest)
		outcome := &formIntakeStoredOutcome{
			Digest: digest, AppliedRevision: record.Public.Revision, AcknowledgedIDs: item.ids,
			Intent: formIntakeInput(record.Public, source.Intake, *input).DialogIntent,
		}
		input.Reservations = append(input.Reservations, formIntakeStoredPass{
			ID: "form_pass_" + id[:32], KeyDigest: id, ReceiptCount: len(source.Intake.Inputs),
			Revision: record.Public.Revision, CandidateIDs: item.ids, Outcome: outcome,
		})
		input.Accounted = appendFormIntakeSpans(input.Accounted, item.spans)
		outcome.Consumed = len(formIntakeCurrentFindings(*input)) == 0 &&
			formIntakeTextAccounted(input.Text, input.Accounted) &&
			(!formIntakeHasScopedInstruction(*input) || formIntakeScopeCoverageComplete(*input, request.Schema))
		if outcome.Consumed {
			input.State, input.Text = formIntakeInputConsumed, ""
		}
	}
	return nil
}
