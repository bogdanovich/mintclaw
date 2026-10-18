package document

import "slices"

// Reconcile only fully accounted advice against an existing, still-current
// native event. This is acknowledgement, never a proposed or copied value.
func (store *FormJobStore) acknowledgeAccountedFormIntakeGuidance(
	mode *formIntakeStoredMode, index int, schema FormFieldsFacts, record FormJobRecord,
	pass *formIntakeStoredPass,
) []string {
	input := &mode.Inputs[index]
	if pass.Outcome == nil || input.State != formIntakeInputCommitted ||
		!formIntakeTextAccounted(input.Text, input.Accounted) ||
		formIntakeHasScopedInstruction(*input) && !formIntakeScopeCoverageComplete(*input, schema) {
		return nil
	}
	var acknowledged []string
	for _, finding := range formIntakeCurrentModeFindings(mode, *input) {
		id := finding.FieldID
		if (finding.Code != "field_ambiguous" && finding.Code != "field_confirmation_required") ||
			!slices.Contains(pass.CandidateIDs, id) || slices.Contains(record.DeferredFieldIDs, id) ||
			slices.Contains(pass.Outcome.AcceptedIDs, id) || slices.Contains(pass.Outcome.AcknowledgedIDs, id) ||
			slices.Contains(pass.Outcome.ScopedIDs, id) || slices.Contains(pass.Outcome.ResolvedGuidanceIDs, id) ||
			slices.ContainsFunc(pass.Outcome.Findings, func(current FormFieldMappingBlocker) bool {
				return current.FieldID == id
			}) {
			continue
		}
		field, found := formIntakeConfirmedUserField(record, id)
		if !found || !store.formIntakeEventObservedAdmission(mode, index, field, 0) {
			continue
		}
		acknowledged = append(acknowledged, id)
	}
	pass.Outcome.AcknowledgedIDs = append(pass.Outcome.AcknowledgedIDs, acknowledged...)
	slices.Sort(pass.Outcome.AcknowledgedIDs)
	return acknowledged
}

func formIntakeConfirmedUserField(record FormJobRecord, id string) (FormJobFieldState, bool) {
	index := slices.IndexFunc(record.Fields, func(field FormJobFieldState) bool {
		return field.FieldID == id && field.Source == FormValueSourceUser &&
			field.Validation == FormValueValidationValid && field.Confidence != FormValueConfidenceLow &&
			(field.State == FormValueConfirmed || field.State == FormValueBlanked || field.State == FormValueNotApplicable)
	})
	if index < 0 {
		return FormJobFieldState{}, false
	}
	return record.Fields[index], true
}

func (store *FormJobStore) formIntakeEventObservedAdmission(
	mode *formIntakeStoredMode, index int, field FormJobFieldState, afterRevision int64,
) bool {
	for _, input := range mode.Inputs {
		for _, pass := range input.Reservations {
			if pass.Outcome == nil || pass.ReceiptCount <= index ||
				afterRevision > 0 && pass.Outcome.AppliedRevision <= afterRevision ||
				!slices.Contains(pass.Outcome.AcceptedIDs, field.FieldID) {
				continue
			}
			// Match the native identities from ApplyFormIntakePass and
			// appendCompositeToRecord. A historical acknowledgement alone cannot
			// prove provenance, and a later correction has a different event ID.
			batchID := "form_batch_" + keyedDigest(store.profileKey, "intake_apply", pass.ID)[:32]
			eventID := "form_value_" + keyedDigest(store.profileKey, "composite_value", batchID, field.FieldID)[:32]
			if field.EventID == eventID {
				return true
			}
		}
	}
	return false
}
