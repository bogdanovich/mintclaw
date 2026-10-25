package document

import (
	"maps"
	"slices"
	"strings"
	"unicode/utf8"
)

func formIntakeCurrentFindings(input formIntakeStoredInput) []FormFieldMappingBlocker {
	return formIntakeFindingsThrough(input, nil, nil)
}

func formIntakeCurrentModeFindings(mode *formIntakeStoredMode, input formIntakeStoredInput) []FormFieldMappingBlocker {
	through := make(map[string]int)
	guidanceThrough := make(map[string]int)
	if mode != nil {
		for _, other := range mode.Inputs {
			for _, pass := range other.Reservations {
				if pass.Outcome == nil {
					continue
				}
				for _, prior := range pass.Outcome.PriorResolutions {
					if prior.InputID != input.ID {
						continue
					}
					target, fieldID := through, prior.Span.FieldID
					if prior.GuidanceFieldID != "" {
						target, fieldID = guidanceThrough, prior.GuidanceFieldID
					}
					if prior.ThroughPasses > target[fieldID] {
						target[fieldID] = prior.ThroughPasses
					}
				}
			}
		}
	}
	return formIntakeFindingsThrough(input, through, guidanceThrough)
}

func formIntakeFindingsThrough(
	input formIntakeStoredInput, through, guidanceThrough map[string]int,
) []FormFieldMappingBlocker {
	if input.State == formIntakeInputConsumed {
		return nil
	}
	// A completed outcome can be reserved after the exclusion pass but applied
	// before it. Settle advice through the native apply-time cutoff, not merely
	// by folding outcomes in reservation order. Never mutate caller-owned maps.
	guidanceThrough = maps.Clone(guidanceThrough)
	for _, pass := range input.Reservations {
		if pass.Outcome == nil {
			continue
		}
		for _, id := range pass.Outcome.ExcludedCandidateIDs {
			if guidanceThrough == nil {
				guidanceThrough = make(map[string]int)
			}
			guidanceThrough[id] = max(guidanceThrough[id], pass.Outcome.ExclusionThroughPasses)
		}
	}
	current := make(map[string]string, MaxFormIntakeCandidates)
	for passIndex, pass := range input.Reservations {
		if pass.Outcome == nil {
			continue
		}
		for _, ids := range [][]string{pass.Outcome.AcceptedIDs, pass.Outcome.AcknowledgedIDs} {
			for _, id := range ids {
				delete(current, id)
			}
		}
		for _, finding := range pass.Outcome.Findings {
			if passIndex < through[finding.FieldID] || passIndex < guidanceThrough[finding.FieldID] &&
				(finding.Code == "field_ambiguous" || finding.Code == "field_confirmation_required") {
				continue
			}
			prior := current[finding.FieldID]
			if prior != "" && prior != "field_ambiguous" && prior != "field_confirmation_required" &&
				(finding.Code == "field_ambiguous" || finding.Code == "field_confirmation_required") {
				// Advice cannot replace a native blocker. Only accepted or
				// acknowledged evidence above resolves that field's history.
				continue
			}
			current[finding.FieldID] = finding.Code
		}
		for _, id := range append(slices.Clone(pass.Outcome.ResolvedGuidanceIDs), pass.Outcome.ExcludedCandidateIDs...) {
			// Only advisory uncertainty in this exact window may settle.
			// Native invalid/conflicting facts still require their own resolution.
			if current[id] == "field_ambiguous" || current[id] == "field_confirmation_required" {
				delete(current, id)
			}
		}
	}
	findings := make([]FormFieldMappingBlocker, 0, len(current))
	for fieldID, code := range current {
		findings = append(findings, FormFieldMappingBlocker{FieldID: fieldID, Code: code})
	}
	slices.SortFunc(findings, func(a, b FormFieldMappingBlocker) int { return strings.Compare(a.FieldID, b.FieldID) })
	return findings
}

// Resolution is derived once at native apply, not retroactively inferred from
// later accounting. It persists with the existing encrypted pass outcome.
func formIntakeResolvedGuidanceIDs(input formIntakeStoredInput, pass formIntakeStoredPass) []string {
	if pass.Outcome == nil || !formIntakeGuidanceOnlyOutcome(*pass.Outcome) ||
		!formIntakeTextAccounted(input.Text, input.Accounted) ||
		slices.ContainsFunc(input.Accounted, func(span formIntakeSpan) bool {
			return span.FieldID != "" || span.ScopedInstruction
		}) {
		return nil
	}
	var ids []string
	for _, finding := range formIntakeCurrentFindings(input) {
		if slices.Contains(pass.CandidateIDs, finding.FieldID) &&
			(finding.Code == "field_ambiguous" || finding.Code == "field_confirmation_required") {
			ids = append(ids, finding.FieldID)
		}
	}
	return ids
}

func formIntakeGuidanceOnlyOutcome(outcome formIntakeStoredOutcome) bool {
	if outcome.ScopedInstruction || len(outcome.ExcludedCandidateIDs) != 0 ||
		len(outcome.AcceptedIDs)+len(outcome.AcknowledgedIDs)+len(outcome.ScopedIDs)+len(outcome.Findings) != 0 {
		return false
	}
	switch outcome.Intent {
	case FormIntakeIntentClarify, FormIntakeIntentBack, FormIntakeIntentPause, FormIntakeIntentResume,
		FormIntakeIntentCancel, FormIntakeIntentReview, FormIntakeIntentFinish:
		return true
	default:
		return false
	}
}

func formIntakeAccountedLiterals(input formIntakeStoredInput) []FormIntakeAccountedLiteral {
	if input.State == formIntakeInputConsumed {
		return nil
	}
	result := make([]FormIntakeAccountedLiteral, 0, len(input.Accounted))
	outputOnly := formIntakeOutputOnlyHistory(input)
	for _, span := range input.Accounted {
		text := input.Text[span.Start:span.End]
		occurrence := formIntakeLiteralOccurrence(input.Text, span)
		result = append(result, FormIntakeAccountedLiteral{
			Literal: FormIntakeLiteral{Text: text, Occurrence: occurrence}, FieldID: span.FieldID,
			ScopedInstruction:    span.ScopedInstruction,
			ReclassifiableOutput: outputOnly && span.ScopedInstruction,
		})
	}
	return result
}

func validateFormIntakeAccounting(record FormJobRecord, input formIntakeStoredInput) error {
	for index, pass := range input.Reservations {
		if outcome := pass.Outcome; outcome != nil {
			cutoff := outcome.ExclusionThroughPasses
			if len(outcome.ExcludedCandidateIDs) == 0 && cutoff != 0 ||
				len(outcome.ExcludedCandidateIDs) != 0 && (cutoff < index+1 || cutoff > len(input.Reservations) ||
					cutoff > MaxFormIntakeExtractionPasses) {
				return ErrFormJobRecordCorrupt
			}
		}
	}
	if len(input.Accounted) > MaxFormIntakeAccountedSpans || input.OriginalBytes < 0 ||
		input.OriginalBytes > MaxFormIntakeInputBytes ||
		(input.State == formIntakeInputStaged && (len(input.Accounted) != 0 || input.OriginalBytes != 0)) ||
		(input.State == formIntakeInputCommitted && input.OriginalBytes != 0 && input.OriginalBytes != len(input.Text)) {
		return ErrFormJobRecordCorrupt
	}
	for index, span := range input.Accounted {
		if span.Start < 0 || span.End <= span.Start || span.End > input.OriginalBytes ||
			span.ScopedInstruction && span.FieldID != "" ||
			span.FieldID != "" && !slices.ContainsFunc(record.Fields, func(field FormJobFieldState) bool {
				return field.FieldID == span.FieldID
			}) {
			return ErrFormJobRecordCorrupt
		}
		if input.State != formIntakeInputConsumed && (!utf8.ValidString(input.Text[span.Start:span.End]) ||
			!formIntakeLiteralTokenBoundaries(input.Text, span.Start, span.End)) {
			return ErrFormJobRecordCorrupt
		}
		for _, prior := range input.Accounted[:index] {
			if span == prior || formIntakeSpansOverlap(span, prior) && (span.FieldID != "" || prior.FieldID != "") {
				return ErrFormJobRecordCorrupt
			}
		}
	}
	return nil
}

func validateFormIntakeOutcome(record FormJobRecord, pass formIntakeStoredPass) error {
	outcome := pass.Outcome
	if outcome == nil {
		return nil
	}
	if outcome.Intent != "" && !outcome.Intent.Valid() ||
		!validDocumentDigest(outcome.Digest) || outcome.AppliedRevision < pass.Revision ||
		outcome.AppliedRevision > record.Revision ||
		// Older outcomes charged a final accounting revision. Both exact shapes
		// remain readable; new writes charge only committed field events.
		(outcome.AppliedRevision != pass.Revision+int64(len(outcome.AcceptedIDs)) &&
			outcome.AppliedRevision != pass.Revision+int64(len(outcome.AcceptedIDs))+1) ||
		len(
			outcome.AcceptedIDs,
		)+len(
			outcome.AcknowledgedIDs,
		)+len(
			outcome.Findings,
		)+len(
			outcome.ScopedIDs,
		)+len(
			outcome.ResolvedGuidanceIDs,
		)+len(
			outcome.ExcludedCandidateIDs,
		) > len(
			pass.CandidateIDs,
		) {
		return ErrFormJobRecordCorrupt
	}
	seen := make(map[string]bool, len(pass.CandidateIDs))
	if len(outcome.ResolvedGuidanceIDs) != 0 && !formIntakeGuidanceOnlyOutcome(*outcome) {
		return ErrFormJobRecordCorrupt
	}
	for _, ids := range [][]string{
		outcome.AcceptedIDs, outcome.AcknowledgedIDs, outcome.ScopedIDs, outcome.ResolvedGuidanceIDs,
		outcome.ExcludedCandidateIDs,
	} {
		if !slices.IsSorted(ids) {
			return ErrFormJobRecordCorrupt
		}
		for _, id := range ids {
			if seen[id] || !slices.Contains(pass.CandidateIDs, id) {
				return ErrFormJobRecordCorrupt
			}
			seen[id] = true
		}
	}
	for _, finding := range outcome.Findings {
		if seen[finding.FieldID] || !slices.Contains(pass.CandidateIDs, finding.FieldID) {
			return ErrFormJobRecordCorrupt
		}
		switch finding.Code {
		case "field_ambiguous",
			"field_conflicting",
			"field_confirmation_required",
			"field_invalid",
			"field_unsupported",
			"field_unresolved":
		default:
			return ErrFormJobRecordCorrupt
		}
		seen[finding.FieldID] = true
	}
	return nil
}
