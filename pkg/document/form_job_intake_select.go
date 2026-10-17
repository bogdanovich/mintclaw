package document

import (
	"reflect"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

type formIntakeSelection struct {
	Payloads          []formJobValuePayload
	Accounted         []formIntakeSpan
	AcceptedIDs       []string
	AcknowledgedIDs   []string
	Findings          []FormFieldMappingBlocker
	ScopedInstruction bool
	ScopedIDs         []string
	DeferredIDs       []string
	IncludedIDs       []string
}

type formIntakeResolvedProposal struct {
	Proposal   FormIntakeProposal
	Field      FormField
	Spans      []formIntakeSpan
	Correction []formIntakeSpan
}

func selectFormIntakeProposals(
	record formJobStoredRecord, key []byte, request FormIntakeApplyRequest,
	input formIntakeStoredInput, candidateIDs []string,
) (formIntakeSelection, error) {
	result := formIntakeSelection{Accounted: slices.Clone(input.Accounted)}
	if input.Intent != interactions.ProtectedAnswerValue {
		if len(request.Proposals) != 0 || len(request.NonData) != 0 || len(request.Findings) != 0 ||
			request.Output != nil {
			return result, ErrFormJobAnswerConflict
		}
		return result, nil
	}
	input.Accounted = reclassifyFormIntakeOutputGuidance(input, request)
	result.Accounted = slices.Clone(input.Accounted)
	if request.Output != nil {
		span, err := resolveFormIntakeLiteral(input.Text, request.Output.Literal)
		if err != nil {
			return result, err
		}
		span.ScopedInstruction = true
		for _, prior := range input.Accounted {
			if formIntakeSpansOverlap(span, prior) && span != prior {
				return result, ErrFormJobAnswerConflict
			}
		}
		input.Accounted = appendFormIntakeSpans(slices.Clone(input.Accounted), []formIntakeSpan{span})
		result.Accounted = slices.Clone(input.Accounted)
		result.ScopedInstruction = true
	}
	resolved, err := resolveFormIntakeProposals(request, input.Text, candidateIDs)
	if err != nil {
		return result, err
	}
	seen := make(map[string]bool, len(candidateIDs))
	for _, proposal := range request.Proposals {
		seen[proposal.FieldID] = true
	}
	for _, finding := range request.Findings {
		if seen[finding.FieldID] || !slices.Contains(candidateIDs, finding.FieldID) ||
			!slices.Contains(
				[]string{"field_ambiguous", "field_conflicting", "field_confirmation_required"},
				finding.Code,
			) {
			return result, ErrFormJobAnswerConflict
		}
		seen[finding.FieldID] = true
		result.Findings = append(result.Findings, finding)
	}
	nonData, err := resolveFormIntakeRefs(input.Text, request.NonData, "")
	if err != nil {
		return result, err
	}
	// Do not let a prose classification hide even an invalid/ambiguous fact.
	for _, item := range resolved {
		if err := validateFormIntakeNonData(item.Correction, resolved, input.Accounted); err != nil {
			return result, err
		}
	}
	if err := validateFormIntakeNonData(nonData, resolved, input.Accounted); err != nil {
		return result, err
	}
	for _, item := range resolved {
		if item.Proposal.Disposition != "" {
			result.ScopedInstruction = true
		}
		if formIntakeProposalAmbiguous(item, resolved, input.Accounted) {
			result.Findings = append(
				result.Findings,
				FormFieldMappingBlocker{FieldID: item.Field.ID, Code: "field_ambiguous"},
			)
			continue
		}
		if item.Proposal.Disposition != "" {
			if err := selectFormIntakeDisposition(&result, record, key, item); err != nil {
				return result, err
			}
			continue
		}
		assignment := formIntakeAssignment(item)
		value, validation, code := deterministicFormValue(item.Field, assignment.Value)
		if assignment.Value.validate() != nil || validation != FormValueValidationValid {
			if code == "" {
				code = "field_invalid"
			}
			result.Findings = append(result.Findings, FormFieldMappingBlocker{FieldID: item.Field.ID, Code: code})
			continue
		}
		current, found, readErr := currentFormIntakeValue(record, key, item.Field.ID)
		if readErr != nil {
			return result, readErr
		}
		oldEvidence := formIntakeEvidenceAlreadyAccepted(item.Spans, input.Accounted)
		alreadyConfirmed := found && current.State == FormValueConfirmed &&
			current.Confidence != FormValueConfidenceLow && current.Validation == FormValueValidationValid &&
			reflect.DeepEqual(current.Value, value)
		if oldEvidence || alreadyConfirmed && (assignment.SupersedesEventID == "" ||
			assignment.SupersedesEventID == current.EventID) && !assignment.ReplaceExisting {
			result.AcknowledgedIDs = append(result.AcknowledgedIDs, item.Field.ID)
			result.Accounted = appendFormIntakeSpans(result.Accounted, item.Spans)
			result.Accounted = appendFormIntakeSpans(result.Accounted, item.Correction)
			continue
		}
		if (found || item.Field.HasValue) && len(item.Correction) == 0 {
			result.Findings = append(
				result.Findings,
				FormFieldMappingBlocker{FieldID: item.Field.ID, Code: "field_conflicting"},
			)
			continue
		}
		if !found && !item.Field.HasValue && (assignment.ReplaceExisting || assignment.SupersedesEventID != "") {
			result.Findings = append(
				result.Findings,
				FormFieldMappingBlocker{FieldID: item.Field.ID, Code: "field_conflicting"},
			)
			continue
		}
		payloads, findings := prepareFormCompositeValues(record, request.Schema, []FormCompositeAssignment{assignment})
		if len(findings) != 0 {
			result.Findings = append(result.Findings, findings...)
			continue
		}
		result.Payloads = append(result.Payloads, payloads...)
		result.AcceptedIDs = append(result.AcceptedIDs, item.Field.ID)
		result.Accounted = appendFormIntakeSpans(result.Accounted, item.Spans)
		result.Accounted = appendFormIntakeSpans(result.Accounted, item.Correction)
	}
	// An uncertainty finding has no trustworthy literal attribution. Retain
	// prose too until clarification rather than making an ambiguous fact
	// permanently unrecoverable through a conflicting non-data classification.
	if len(request.Findings) == 0 {
		result.Accounted = appendFormIntakeSpans(result.Accounted, nonData)
	}
	// The shared composite chain remains canonical regardless of model order.
	slices.SortFunc(
		result.Payloads,
		func(a, b formJobValuePayload) int { return strings.Compare(a.FieldID, b.FieldID) },
	)
	slices.Sort(result.AcceptedIDs)
	slices.Sort(result.AcknowledgedIDs)
	slices.Sort(result.ScopedIDs)
	return result, nil
}

func resolveFormIntakeProposals(
	request FormIntakeApplyRequest, text string, candidateIDs []string,
) ([]formIntakeResolvedProposal, error) {
	result := make([]formIntakeResolvedProposal, 0, len(request.Proposals))
	seen := make(map[string]bool, len(request.Proposals))
	for _, item := range request.Proposals {
		index := slices.IndexFunc(request.Schema.Fields, func(field FormField) bool { return field.ID == item.FieldID })
		if index < 0 || seen[item.FieldID] || !slices.Contains(candidateIDs, item.FieldID) ||
			request.Schema.Fields[index].ReadOnly {
			return nil, ErrFormJobAnswerConflict
		}
		seen[item.FieldID] = true
		field := request.Schema.Fields[index]
		if !item.Disposition.valid() ||
			(len(item.Literals) != 1 && (item.Disposition != "" || field.Kind != FormFieldList || !field.MultiSelect)) {
			return nil, ErrFormJobAnswerConflict
		}
		spans, err := resolveFormIntakeRefs(text, item.Literals, item.FieldID)
		if err != nil {
			return nil, err
		}
		if item.Disposition != "" {
			for index := range spans {
				spans[index].FieldID = ""
				spans[index].ScopedInstruction = true
			}
		}
		correction, err := resolveFormIntakeRefs(text, item.CorrectionEvidence, "")
		if err != nil {
			return nil, err
		}
		if item.Disposition != "" {
			// The same exact blanking instruction can also prove correction.
			// Keep its scoped accounting once, rather than duplicating it as prose
			// that would block reuse by another field in the instructed section.
			correction = slices.DeleteFunc(correction, func(candidate formIntakeSpan) bool {
				return slices.ContainsFunc(spans, func(instruction formIntakeSpan) bool {
					return instruction.Start == candidate.Start && instruction.End == candidate.End
				})
			})
		}
		result = append(result, formIntakeResolvedProposal{item, field, spans, correction})
	}
	return result, nil
}

func resolveFormIntakeRefs(text string, refs []FormIntakeLiteral, fieldID string) ([]formIntakeSpan, error) {
	result := make([]formIntakeSpan, 0, len(refs))
	for _, ref := range refs {
		span, err := resolveFormIntakeLiteral(text, ref)
		if err != nil {
			return nil, err
		}
		span.FieldID = fieldID
		for _, prior := range result {
			if formIntakeSpansOverlap(span, prior) {
				return nil, ErrFormJobAnswerConflict
			}
		}
		result = append(result, span)
	}
	return result, nil
}

func validateFormIntakeNonData(
	spans []formIntakeSpan, proposed []formIntakeResolvedProposal, accounted []formIntakeSpan,
) error {
	for _, span := range spans {
		for _, prior := range accounted {
			if (prior.FieldID != "" || prior.ScopedInstruction) && formIntakeSpansOverlap(span, prior) {
				return ErrFormJobAnswerConflict
			}
		}
		for _, item := range proposed {
			for _, fact := range item.Spans {
				if formIntakeSpansOverlap(span, fact) {
					return ErrFormJobAnswerConflict
				}
			}
		}
	}
	return nil
}

func formIntakeProposalAmbiguous(
	item formIntakeResolvedProposal, proposed []formIntakeResolvedProposal, accounted []formIntakeSpan,
) bool {
	for _, span := range item.Spans {
		for _, prior := range accounted {
			if formIntakeSpansOverlap(span, prior) && span != prior {
				return true
			}
		}
		for _, other := range proposed {
			if item.Field.ID == other.Field.ID {
				continue
			}
			for _, fact := range other.Spans {
				if formIntakeSpansOverlap(span, fact) && (!span.ScopedInstruction || span != fact) {
					return true
				}
			}
		}
	}
	return false
}

func formIntakeAssignment(item formIntakeResolvedProposal) FormCompositeAssignment {
	value := FormProtectedValue{Kind: ProtectedValueText, Text: item.Proposal.Literals[0].Text}
	if item.Field.Kind == FormFieldList && item.Field.MultiSelect {
		value = FormProtectedValue{Kind: ProtectedValueChoices}
		for _, ref := range item.Proposal.Literals {
			value.Choices = append(value.Choices, ref.Text)
		}
	}
	return FormCompositeAssignment{
		FieldID: item.Field.ID, Value: value,
		SupersedesEventID: item.Proposal.SupersedesEventID, ReplaceExisting: item.Proposal.ReplaceExisting,
	}
}

func currentFormIntakeValue(
	record formJobStoredRecord, key []byte, fieldID string,
) (formJobValuePayload, bool, error) {
	index := slices.IndexFunc(
		record.Public.Fields,
		func(state FormJobFieldState) bool { return state.FieldID == fieldID },
	)
	if index < 0 {
		return formJobValuePayload{}, false, nil
	}
	eventID := record.Public.Fields[index].EventID
	for _, envelope := range record.Events {
		if envelope.EventID == eventID {
			payload, err := openFormJobValuePayload(key, envelope)
			return payload, true, err
		}
	}
	return formJobValuePayload{}, false, ErrFormJobRecordCorrupt
}

func formIntakeEvidenceAlreadyAccepted(spans, accounted []formIntakeSpan) bool {
	for _, span := range spans {
		if !slices.Contains(accounted, span) {
			return false
		}
	}
	return len(spans) != 0
}

func appendFormIntakeSpans(accounted, added []formIntakeSpan) []formIntakeSpan {
	for _, span := range added {
		if !slices.Contains(accounted, span) {
			accounted = append(accounted, span)
		}
	}
	return accounted
}
