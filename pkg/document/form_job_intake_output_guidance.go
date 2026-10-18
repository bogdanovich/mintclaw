package document

import "slices"

// Empty scoped IDs are not proof of guidance. This only marks histories that
// may be explicitly reclassified by a new whole-utterance private interpretation.
func formIntakeOutputOnlyHistory(input formIntakeStoredInput) bool {
	if slices.ContainsFunc(input.Accounted, func(span formIntakeSpan) bool { return span.FieldID != "" }) {
		return false
	}
	scoped := false
	for _, pass := range input.Reservations {
		outcome := pass.Outcome
		if outcome == nil {
			continue
		}
		if len(outcome.AcceptedIDs)+len(outcome.AcknowledgedIDs)+len(outcome.ScopedIDs) != 0 {
			return false
		}
		for _, finding := range outcome.Findings {
			if finding.Code != "field_ambiguous" && finding.Code != "field_confirmation_required" {
				return false
			}
		}
		scoped = scoped || outcome.ScopedInstruction
	}
	return scoped
}

func reclassifyFormIntakeOutputGuidance(
	input formIntakeStoredInput, request FormIntakeApplyRequest,
) []formIntakeSpan {
	if !formIntakeOutputOnlyHistory(input) || request.Output != nil ||
		len(request.Proposals)+len(request.Findings) != 0 ||
		!formIntakeGuidanceOnlyOutcome(formIntakeStoredOutcome{Intent: request.Intent}) {
		return input.Accounted
	}
	spans, err := resolveFormIntakeRefs(input.Text, request.NonData, "")
	if err != nil || !formIntakeTextAccounted(input.Text, spans) {
		return input.Accounted
	}
	// No mutation of old outcomes or selected output scope. Only exact existing
	// spans become non-data; ordinary validation still checks this pass below.
	accounted := slices.Clone(input.Accounted)
	for index := range accounted {
		accounted[index].ScopedInstruction = false
	}
	return accounted
}
