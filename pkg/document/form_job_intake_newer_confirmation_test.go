package document

import (
	"errors"
	"testing"
)

func TestFormIntakeNewerConfirmationSettlesOnlyFullyAccountedOlderUncertainty(t *testing.T) {
	for _, test := range []struct {
		name                    string
		blank, extra, unrelated bool
	}{
		{name: "confirmed"},
		{name: "explicit blank", blank: true},
		{name: "unaccounted other facts", extra: true},
		{name: "unrelated field", unrelated: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, options := newTestFormJobStore(t)
			schema := intakeTextSchema()
			schema.Fields = schema.Fields[:2]
			start := intakeApplyFixture(t, store, schema)
			mode, err := store.StartFormIntake(t.Context(), start)
			if err != nil {
				t.Fatal(err)
			}
			text := "I am Casey Mint."
			if test.extra {
				text += " Other unhandled facts remain."
			}
			old, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
				start, mode.Progress.ModeID, mode.Job.Revision, "old-uncertainty", text,
			))
			if err != nil {
				t.Fatal(err)
			}
			first := reserveIntakeApply(t, store, start, old.Receipt, old.Job.Revision, []int{0, 1}, "uncertain")
			first.Proposals = []FormIntakeProposal{intakeProposal(schema, 0, "Casey Mint")}
			first.Findings = []FormFieldMappingBlocker{{FieldID: schema.Fields[1].ID, Code: "field_ambiguous"}}
			accepted, err := store.ApplyFormIntakePass(t.Context(), first)
			if err != nil {
				t.Fatal(err)
			}
			prose := reserveIntakeApply(t, store, start, old.Receipt, accepted.Job.Revision, []int{0, 1}, "prose")
			prose.Intent, prose.NonData = FormIntakeIntentFacts, []FormIntakeLiteral{intakeLiteral("I am")}
			pending, err := store.ApplyFormIntakePass(t.Context(), prose)
			if err != nil || pending.Progress.PendingInputs != 1 {
				t.Fatal("older uncertainty was not retained")
			}
			currentText := "Robin Mint"
			if test.blank {
				currentText = "Leave the second entry blank"
			}
			if test.unrelated {
				currentText = "Casey Mint"
			}
			current, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
				start, mode.Progress.ModeID, pending.Job.Revision, "new-confirmation", currentText,
			))
			if err != nil {
				t.Fatal(err)
			}
			request := reserveIntakeApply(
				t,
				store,
				start,
				current.Receipt,
				current.Job.Revision,
				[]int{0, 1},
				"confirm",
			)
			field := 1
			if test.unrelated {
				field = 0
			}
			request.Proposals = []FormIntakeProposal{intakeProposal(schema, field, currentText)}
			if test.blank {
				request.Proposals[0].Disposition = FormIntakeIntentionallyBlank
			}
			result, err := store.ApplyFormIntakePass(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			expected := 0
			if test.extra || test.unrelated {
				expected = 1
			}
			if result.Progress.PendingInputs != expected {
				t.Fatalf("new canonical confirmation did not reconcile older accounting: pending=%d want=%d",
					result.Progress.PendingInputs, expected)
			}
			input, err := store.ReadFormIntakeInput(t.Context(), old.Receipt, start.Owner)
			if err != nil || input.Consumed != (expected == 0) || input.Passes != 2 {
				t.Fatal("reconciliation changed extraction counts or erased unrelated pending facts")
			}
			store.Close()
			store, err = OpenFormJobStore(options)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(store.Close)
			input, err = store.ReadFormIntakeInput(t.Context(), old.Receipt, start.Owner)
			if err != nil || input.Consumed != (expected == 0) || input.Passes != 2 {
				t.Fatal("restart lost the older receipt lifecycle or extraction count")
			}
			replayed, err := store.ApplyFormIntakePass(t.Context(), request)
			if err != nil || !replayed.Reused || replayed.Progress.PendingInputs != expected ||
				replayed.Job.LedgerRevision != result.Job.LedgerRevision {
				t.Fatal("exact replay changed fields or restored old uncertainty")
			}
			request.Proposals[0].Literals[0].Text = "A changed replay"
			if _, err = store.ApplyFormIntakePass(t.Context(), request); !errors.Is(err, ErrFormJobAnswerConflict) {
				t.Fatal("changed replay was accepted")
			}
		})
	}
}

func TestFormIntakeNewerGuidanceRequiresEveryNativeGate(t *testing.T) {
	for _, name := range []string{
		"acknowledged", "accepted", "confirmation advice", "native invalid", "native conflict", "unsupported",
		"unaccounted", "other uncertainty", "deferred", "no canonical field", "invalid canonical field",
		"low confidence", "not a user decision", "unconfirmed", "missing scope coverage", "not newer",
	} {
		t.Run(name, func(t *testing.T) {
			schema := intakeTextSchema()
			schema.Fields = schema.Fields[:2]
			a, b := schema.Fields[0].ID, schema.Fields[1].ID
			old := formIntakeStoredInput{
				ID: "old", State: formIntakeInputCommitted, Text: "data", OriginalBytes: 4,
				Accounted: []formIntakeSpan{{Start: 0, End: 4, FieldID: a}},
				Reservations: []formIntakeStoredPass{{CandidateIDs: []string{a, b}, Outcome: &formIntakeStoredOutcome{
					Findings: []FormFieldMappingBlocker{{FieldID: b, Code: "field_ambiguous"}},
				}}},
			}
			outcome := formIntakeStoredOutcome{AcknowledgedIDs: []string{b}}
			record := FormJobRecord{Fields: []FormJobFieldState{{
				FieldID: b, Source: FormValueSourceUser, State: FormValueConfirmed,
				Confidence: FormValueConfidenceExact, Validation: FormValueValidationValid,
			}}}
			index := 1
			switch name {
			case "accepted":
				outcome.AcknowledgedIDs = nil
				outcome.AcceptedIDs = []string{b}
			case "confirmation advice":
				old.Reservations[0].Outcome.Findings[0].Code = "field_confirmation_required"
			case "native invalid":
				old.Reservations[0].Outcome.Findings[0].Code = "field_invalid"
			case "native conflict":
				old.Reservations[0].Outcome.Findings[0].Code = "field_conflicting"
			case "unsupported":
				old.Reservations[0].Outcome.Findings[0].Code = "field_unsupported"
			case "unaccounted":
				old.Text += " other facts"
			case "other uncertainty":
				old.Reservations[0].Outcome.Findings = append(old.Reservations[0].Outcome.Findings,
					FormFieldMappingBlocker{FieldID: a, Code: "field_ambiguous"})
			case "deferred":
				outcome.AcknowledgedIDs = nil
				outcome.ScopedIDs = []string{b}
			case "no canonical field":
				record.Fields = nil
			case "invalid canonical field":
				record.Fields[0].Validation = FormValueValidationInvalid
			case "low confidence":
				record.Fields[0].Confidence = FormValueConfidenceLow
			case "not a user decision":
				record.Fields[0].Source = FormValueSource("model")
			case "unconfirmed":
				record.Fields[0].State = FormValueState("unconfirmed")
			case "missing scope coverage":
				old.Accounted = []formIntakeSpan{{Start: 0, End: 4, ScopedInstruction: true}}
				old.Reservations[0].CandidateIDs = []string{a}
				old.Reservations[0].Outcome.ScopedInstruction = true
				old.Intent = "value"
			case "not newer":
				index = 0
			}
			mode := &formIntakeStoredMode{Inputs: []formIntakeStoredInput{old, {ID: "current", Text: "later"}}}
			consumeOlderFormIntakeGuidance(mode, index, schema, record, outcome)
			want := name == "acknowledged" || name == "accepted" || name == "confirmation advice"
			got := mode.Inputs[0]
			if (got.State == formIntakeInputConsumed) != want || want && got.Text != "" || !want && got.Text != old.Text ||
				len(got.Reservations) != 1 ||
				len(got.Reservations[0].Outcome.Findings) != len(old.Reservations[0].Outcome.Findings) {
				t.Fatal("advisory settlement bypassed a native gate or mutated an old outcome")
			}
		})
	}
}
