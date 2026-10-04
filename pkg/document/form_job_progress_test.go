package document

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestFormMappingProgressDistinguishesFactsWithoutValues(t *testing.T) {
	for _, status := range []string{"missing", "preserved", "confirmed", "optional_blank", "field_conflicting"} {
		t.Run(status, func(t *testing.T) {
			store, options := newTestFormJobStore(t)
			owner := testFormJobOwner()
			schema := *successfulTestFormFields()
			schema.Fields[0].HasValue = status == "preserved"
			record := createMappedFormJob(t, store, owner, schema)
			if status != "missing" && status != "preserved" {
				value := FormProtectedValue{Kind: ProtectedValueText, Text: "PDFI3_PRIVATE_VALUE_SENTINEL"}
				state, validation, blankReason := FormValueConfirmed, FormValueValidationValid, FormBlankNone
				if status == "optional_blank" {
					value = FormProtectedValue{Kind: ProtectedValueBlank}
					state, blankReason = FormValueBlanked, FormBlankIntentional
				}
				if status == "field_conflicting" {
					state, validation = FormValueConflicting, FormValueValidationConflicting
				}
				var err error
				record, _, err = store.AppendValue(t.Context(), FormJobAppendValueRequest{
					JobID: record.JobID, ExpectedRevision: record.Revision, Owner: owner,
					FieldID: schema.Fields[0].ID, IdempotencyKey: "progress-answer", Value: value,
					State: state, Source: FormValueSourceUser, Confidence: FormValueConfidenceConfirmed,
					Validation: validation, BlankReason: blankReason,
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			summary, err := store.FormMappingSummary(t.Context(), record.JobID, owner, schema)
			want := []FormFieldProgress{{FieldID: schema.Fields[0].ID, Status: status}}
			if err != nil || !slices.Equal(summary.FieldProgress, want) {
				t.Fatalf("progress = %#v, %v, want %#v", summary.FieldProgress, err, want)
			}
			encoded, err := json.Marshal(summary)
			if err != nil || strings.Contains(string(encoded), "PDFI3_PRIVATE_VALUE_SENTINEL") {
				t.Fatalf("progress exposed protected values: %s, %v", encoded, err)
			}
			wrongOwner := owner
			wrongOwner.RouteSessionKey = "another-session"
			if _, err := store.FormMappingSummary(
				t.Context(),
				record.JobID,
				wrongOwner,
				schema,
			); !errors.Is(
				err,
				ErrFormJobUnauthorized,
			) {
				t.Fatalf("cross-owner progress error = %v", err)
			}
			store.Close()
			reopened, err := OpenFormJobStore(options)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(reopened.Close)
			restarted, err := reopened.FormMappingSummary(t.Context(), record.JobID, owner, schema)
			if err != nil || !slices.Equal(restarted.FieldProgress, want) || restarted.Revision != record.Revision {
				t.Fatalf("restarted progress = %#v, %v", restarted, err)
			}
		})
	}
}
