package document

import "slices"

func validateFormIntakePriorResolutions(mode *formIntakeStoredMode) error {
	type identity struct {
		input      string
		start, end int
	}
	seen := make(map[identity]bool)
	for currentIndex, input := range mode.Inputs {
		for _, pass := range input.Reservations {
			if pass.Outcome == nil {
				continue
			}
			if len(pass.Outcome.PriorResolutions) > MaxFormIntakeCandidates {
				return ErrFormJobRecordCorrupt
			}
			for _, prior := range pass.Outcome.PriorResolutions {
				index := formIntakeInputIndex(mode, prior.InputID)
				span := prior.Span
				if index < 0 || index >= currentIndex || pass.ReceiptCount <= currentIndex ||
					prior.ThroughPasses < 1 || prior.ThroughPasses > len(mode.Inputs[index].Reservations) ||
					prior.ThroughPasses > MaxFormIntakeExtractionPasses || span.ScopedInstruction ||
					!slices.Contains(pass.CandidateIDs, span.FieldID) ||
					!slices.Contains(pass.Outcome.AcceptedIDs, span.FieldID) &&
						!slices.Contains(pass.Outcome.AcknowledgedIDs, span.FieldID) ||
					!slices.Contains(mode.Inputs[index].Accounted, span) {
					return ErrFormJobRecordCorrupt
				}
				key := identity{prior.InputID, span.Start, span.End}
				if seen[key] {
					return ErrFormJobRecordCorrupt
				}
				seen[key] = true
			}
		}
	}
	return nil
}
