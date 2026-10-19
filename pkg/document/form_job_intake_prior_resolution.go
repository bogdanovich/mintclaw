package document

import (
	"slices"
)

type formIntakeStoredResolution struct {
	InputID       string         `json:"input_id"`
	ThroughPasses int            `json:"through_passes"`
	Span          formIntakeSpan `json:"span,omitzero"`
	// Advisory settlement has no literal to account. Exactly one variant is set.
	GuidanceFieldID string `json:"guidance_field_id,omitempty"`
}

// An acknowledgement need not advance the field revision. A settlement still
// changes source accounting, so earlier unapplied reservations cannot use their
// stale interpretation snapshot. Completed exact replay is handled before this.
func formIntakeReservationSuperseded(mode *formIntakeStoredMode, inputID string, passIndex int) bool {
	for _, input := range mode.Inputs {
		for _, pass := range input.Reservations {
			if pass.Outcome == nil {
				continue
			}
			for _, prior := range pass.Outcome.PriorResolutions {
				if prior.InputID == inputID && passIndex < prior.ThroughPasses {
					return true
				}
			}
		}
	}
	return false
}

// This runs inside the current native apply's atomic update, after positive
// validation and before either accepted events or source accounting is published.
func (store *FormJobStore) resolvePriorFormIntakeLiterals(
	source *formJobSourceSecret, currentIndex int, record formJobStoredRecord, key []byte,
	request FormIntakeApplyRequest, selection formIntakeSelection, outcome *formIntakeStoredOutcome,
) error {
	type resolution struct {
		spans []formIntakeSpan
	}
	resolved := make(map[int]*resolution)
	var settlements []formIntakeStoredResolution
	for _, proposal := range request.Proposals {
		if len(proposal.Resolves) == 0 {
			continue
		}
		if proposal.Disposition != "" || !slices.Contains(selection.CurrentValueIDs, proposal.FieldID) {
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
			if err != nil || jobID != record.Public.JobID || index < 0 ||
				formIntakeAdmissionOrder(
					source.Intake,
					index,
				) >= formIntakeAdmissionOrder(
					source.Intake,
					currentIndex,
				) {
				return ErrFormJobAnswerConflict
			}
			input := source.Intake.Inputs[index]
			if input.State != formIntakeInputCommitted || len(input.Reservations) != prior.ExpectedPasses {
				return ErrFormJobConflict
			}
			findings := formIntakeCurrentModeFindings(source.Intake, input)
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
			settlements = append(settlements, formIntakeStoredResolution{
				InputID: input.ID, ThroughPasses: prior.ExpectedPasses, Span: span,
			})
		}
	}
	outcome.PriorResolutions = settlements
	// Bookkeeping never reserves another extraction or mutates an older outcome.
	for index := range source.Intake.Inputs {
		item := resolved[index]
		if item == nil {
			continue
		}
		input := &source.Intake.Inputs[index]
		if len(input.Accounted)+len(item.spans) > MaxFormIntakeAccountedSpans {
			return ErrFormJobCapacityExceeded
		}
		input.Accounted = appendFormIntakeSpans(input.Accounted, item.spans)
		consumed := len(formIntakeCurrentModeFindings(source.Intake, *input)) == 0 &&
			formIntakeTextAccounted(input.Text, input.Accounted) &&
			(!formIntakeHasScopedInstruction(*input) || formIntakeScopeCoverageComplete(*input, request.Schema))
		if consumed {
			input.State, input.Text = formIntakeInputConsumed, ""
		}
	}
	return nil
}
