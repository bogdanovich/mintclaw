package document

import "slices"

// A later natively accepted decision can settle advisory uncertainty, not an
// unaccounted fact or a native validation error. This only erases already fully
// accounted older text; it never derives, copies or changes a field value.
func consumeOlderFormIntakeGuidance(
	mode *formIntakeStoredMode, currentIndex int, schema FormFieldsFacts,
	record FormJobRecord, currentValueIDs []string, outcome *formIntakeStoredOutcome,
) {
	for index := 0; index < currentIndex; index++ {
		input := &mode.Inputs[index]
		if input.State != formIntakeInputCommitted || !formIntakeTextAccounted(input.Text, input.Accounted) ||
			formIntakeHasScopedInstruction(*input) && !formIntakeScopeCoverageComplete(*input, schema) {
			continue
		}
		findings := formIntakeCurrentModeFindings(mode, *input)
		if len(findings) == 0 {
			continue
		}
		for _, finding := range findings {
			field := slices.IndexFunc(record.Fields, func(field FormJobFieldState) bool {
				return field.FieldID == finding.FieldID && field.Source == FormValueSourceUser &&
					field.Validation == FormValueValidationValid && field.Confidence != FormValueConfidenceLow &&
					(field.State == FormValueConfirmed || field.State == FormValueBlanked ||
						field.State == FormValueNotApplicable)
			})
			if (finding.Code != "field_ambiguous" && finding.Code != "field_confirmation_required") || field < 0 ||
				!slices.Contains(currentValueIDs, finding.FieldID) {
				continue
			}
			outcome.PriorResolutions = append(outcome.PriorResolutions, formIntakeStoredResolution{
				InputID: input.ID, ThroughPasses: len(input.Reservations), GuidanceFieldID: finding.FieldID,
			})
		}
		if len(formIntakeCurrentModeFindings(mode, *input)) == 0 {
			input.State, input.Text = formIntakeInputConsumed, ""
		}
	}
}
