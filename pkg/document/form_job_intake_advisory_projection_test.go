package document

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestFormIntakeAdvisoryProjectionRequiresExactStrictlyLaterCurrentDecision(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	for _, name := range []string{
		"later", "earlier", "same revision", "later finding", "ack only", "different event",
		"other field", "low confidence", "native conflict", "not a user decision", "before admission",
	} {
		t.Run(name, func(t *testing.T) {
			const id = "field_a"
			old := formIntakeStoredInput{
				ID: "old", State: formIntakeInputCommitted, Text: "unaccounted", OriginalBytes: 11,
				Reservations: []formIntakeStoredPass{{
					ID: "old-pass", ReceiptCount: 1,
					Outcome: &formIntakeStoredOutcome{
						AppliedRevision: 2,
						Findings:        []FormFieldMappingBlocker{{FieldID: id, Code: "field_ambiguous"}},
					},
				}},
			}
			decision := formIntakeStoredPass{
				ID: "accepted-pass", ReceiptCount: 2,
				Outcome: &formIntakeStoredOutcome{AppliedRevision: 3, AcceptedIDs: []string{id}},
			}
			batchID := "form_batch_" + keyedDigest(store.profileKey, "intake_apply", decision.ID)[:32]
			field := FormJobFieldState{
				FieldID: id, Source: FormValueSourceUser, State: FormValueConfirmed,
				Validation: FormValueValidationValid, Confidence: FormValueConfidenceExact,
				EventID: "form_value_" + keyedDigest(store.profileKey, "composite_value", batchID, id)[:32],
			}
			switch name {
			case "earlier":
				decision.Outcome.AppliedRevision = 1
			case "same revision":
				decision.Outcome.AppliedRevision = 2
			case "later finding":
				old.Reservations[0].Outcome.AppliedRevision = 4
			case "ack only":
				decision.Outcome.AcceptedIDs, decision.Outcome.AcknowledgedIDs = nil, []string{id}
			case "different event":
				field.EventID = "another-native-event"
			case "other field":
				decision.Outcome.AcceptedIDs = []string{"other_field"}
			case "low confidence":
				field.Confidence = FormValueConfidenceLow
			case "native conflict":
				old.Reservations[0].Outcome.Findings[0].Code = "field_conflicting"
			case "not a user decision":
				field.Source = FormValueSourceDocument
			case "before admission":
				decision.ReceiptCount = 0
			}
			mode := &formIntakeStoredMode{ID: "mode", Inputs: []formIntakeStoredInput{old, {
				ID: "new", Reservations: []formIntakeStoredPass{decision},
			}}}
			before, err := json.Marshal(mode)
			if err != nil {
				t.Fatal(err)
			}
			input := store.formIntakeInput(FormJobRecord{Fields: []FormJobFieldState{field}}, mode, old)
			if slices.Contains(input.SupersededAdvisoryIDs, id) != (name == "later") ||
				len(input.Findings) != 1 || input.Text != "unaccounted" || input.Consumed {
				t.Fatal("advisory projection hid a real question or altered protected accounting")
			}
			after, err := json.Marshal(mode)
			if err != nil || string(before) != string(after) {
				t.Fatal("presentation evidence mutated native state")
			}
		})
	}
}
