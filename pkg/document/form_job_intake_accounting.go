package document

import (
	"slices"
	"strings"
	"unicode/utf8"
)

func formIntakeCurrentFindings(input formIntakeStoredInput) []FormFieldMappingBlocker {
	current := make(map[string]string, MaxFormIntakeCandidates)
	for _, pass := range input.Reservations {
		if pass.Outcome == nil {
			continue
		}
		for _, ids := range [][]string{pass.Outcome.AcceptedIDs, pass.Outcome.AcknowledgedIDs} {
			for _, id := range ids {
				delete(current, id)
			}
		}
		for _, finding := range pass.Outcome.Findings {
			current[finding.FieldID] = finding.Code
		}
	}
	findings := make([]FormFieldMappingBlocker, 0, len(current))
	for fieldID, code := range current {
		findings = append(findings, FormFieldMappingBlocker{FieldID: fieldID, Code: code})
	}
	slices.SortFunc(findings, func(a, b FormFieldMappingBlocker) int { return strings.Compare(a.FieldID, b.FieldID) })
	return findings
}

func formIntakeAccountedLiterals(input formIntakeStoredInput) []FormIntakeAccountedLiteral {
	if input.State == formIntakeInputConsumed {
		return nil
	}
	result := make([]FormIntakeAccountedLiteral, 0, len(input.Accounted))
	for _, span := range input.Accounted {
		text := input.Text[span.Start:span.End]
		occurrence := formIntakeLiteralOccurrence(input.Text, span)
		result = append(result, FormIntakeAccountedLiteral{
			Literal: FormIntakeLiteral{Text: text, Occurrence: occurrence}, FieldID: span.FieldID,
		})
	}
	return result
}

func validateFormIntakeAccounting(record FormJobRecord, input formIntakeStoredInput) error {
	if len(input.Accounted) > MaxFormIntakeAccountedSpans || input.OriginalBytes < 0 ||
		input.OriginalBytes > MaxFormIntakeInputBytes ||
		(input.State == formIntakeInputStaged && (len(input.Accounted) != 0 || input.OriginalBytes != 0)) ||
		(input.State == formIntakeInputCommitted && input.OriginalBytes != 0 && input.OriginalBytes != len(input.Text)) {
		return ErrFormJobRecordCorrupt
	}
	for index, span := range input.Accounted {
		if span.Start < 0 || span.End <= span.Start || span.End > input.OriginalBytes ||
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
		!validDocumentDigest(outcome.Digest) || outcome.AppliedRevision <= pass.Revision ||
		outcome.AppliedRevision > record.Revision ||
		outcome.AppliedRevision != pass.Revision+int64(len(outcome.AcceptedIDs))+1 ||
		len(outcome.AcceptedIDs)+len(outcome.AcknowledgedIDs)+len(outcome.Findings) > len(pass.CandidateIDs) {
		return ErrFormJobRecordCorrupt
	}
	seen := make(map[string]bool, len(pass.CandidateIDs))
	for _, ids := range [][]string{outcome.AcceptedIDs, outcome.AcknowledgedIDs} {
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
