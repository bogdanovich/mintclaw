package document

import "slices"

func validateFormIntakePriorResolutions(mode *formIntakeStoredMode) error {
	type identity struct {
		input      string
		start, end int
		guidance   string
		through    int
	}
	seen := make(map[identity]bool)
	for currentIndex, input := range mode.Inputs {
		for _, pass := range input.Reservations {
			if pass.Outcome == nil {
				continue
			}
			// Each current field may settle each earlier pending input, in addition
			// to the separately capped explicit literal resolutions. Model bounds do
			// not change; only native metadata spans multiple older inputs.
			if len(pass.Outcome.PriorResolutions) > MaxFormIntakeCandidates*MaxFormIntakePendingInputs {
				return ErrFormJobRecordCorrupt
			}
			literals := 0
			for _, prior := range pass.Outcome.PriorResolutions {
				index := formIntakeInputIndex(mode, prior.InputID)
				span := prior.Span
				if index < 0 || pass.ReceiptCount <= index || pass.ReceiptCount <= currentIndex ||
					formIntakeAdmissionOrder(mode, index) >= formIntakeAdmissionOrder(mode, currentIndex) ||
					prior.ThroughPasses < 1 || prior.ThroughPasses > len(mode.Inputs[index].Reservations) ||
					prior.ThroughPasses > MaxFormIntakeExtractionPasses {
					return ErrFormJobRecordCorrupt
				}
				fieldID := span.FieldID
				key := identity{input: prior.InputID, start: span.Start, end: span.End}
				if prior.GuidanceFieldID != "" {
					fieldID = prior.GuidanceFieldID
					old := mode.Inputs[index]
					if span != (formIntakeSpan{}) || old.State != formIntakeInputConsumed &&
						!formIntakeTextAccounted(old.Text, old.Accounted) {
						return ErrFormJobRecordCorrupt
					}
					key.guidance, key.through = fieldID, prior.ThroughPasses
				} else {
					literals++
					if literals > MaxFormIntakeCandidates || span.FieldID == "" || span.ScopedInstruction ||
						!slices.Contains(mode.Inputs[index].Accounted, span) {
						return ErrFormJobRecordCorrupt
					}
				}
				if !slices.Contains(pass.CandidateIDs, fieldID) ||
					!slices.Contains(pass.Outcome.AcceptedIDs, fieldID) &&
						!slices.Contains(pass.Outcome.AcknowledgedIDs, fieldID) {
					return ErrFormJobRecordCorrupt
				}
				if seen[key] {
					return ErrFormJobRecordCorrupt
				}
				seen[key] = true
			}
		}
	}
	return nil
}
