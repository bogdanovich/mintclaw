package document

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

func TestFormIntakeDelayedAdvisoryReconciliationUsesStillCurrentAcceptedEvents(t *testing.T) {
	store, options := newTestFormJobStore(t)
	schema := intakeTextSchema()
	schema.Fields = schema.Fields[:2]
	start := intakeApplyFixture(t, store, schema)
	mode, err := store.StartFormIntake(t.Context(), start)
	if err != nil {
		t.Fatal(err)
	}
	old, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.Progress.ModeID, mode.Job.Revision, "old-ambiguous", "PHONE Context",
	))
	if err != nil {
		t.Fatal(err)
	}
	first := reserveIntakeApply(t, store, start, old.Receipt, old.Job.Revision, []int{0, 1}, "uncertain")
	first.Findings = []FormFieldMappingBlocker{
		{FieldID: schema.Fields[0].ID, Code: "field_ambiguous"},
		{FieldID: schema.Fields[1].ID, Code: "field_ambiguous"},
	}
	pending, err := store.ApplyFormIntakePass(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.Progress.ModeID, pending.Job.Revision, "clarification", "Leave first blank. Phone belongs second",
	))
	if err != nil {
		t.Fatal(err)
	}
	clarification := reserveIntakeApply(
		t, store, start, current.Receipt, current.Job.Revision, []int{0, 1}, "blank-first",
	)
	blank := intakeProposal(schema, 0, "Leave first blank")
	blank.Disposition = FormIntakeIntentionallyBlank
	clarification.Proposals = []FormIntakeProposal{blank}
	clarification.Findings = []FormFieldMappingBlocker{{
		FieldID: schema.Fields[1].ID, Code: "field_confirmation_required",
	}}
	blanked, err := store.ApplyFormIntakePass(t.Context(), clarification)
	if err != nil || blanked.Progress.PendingInputs != 2 {
		t.Fatal("a decision erased incompletely accounted input")
	}
	// The related clarification permits the older source's exact value to be
	// assigned. The previously accepted blank is no longer proposed in this pass.
	resolve := reserveIntakeApply(t, store, start, old.Receipt, blanked.Job.Revision, []int{0, 1}, "assign-second")
	resolve.Proposals = []FormIntakeProposal{intakeProposal(schema, 1, "PHONE")}
	resolve.NonData = []FormIntakeLiteral{intakeLiteral("Context")}
	resolved, err := store.ApplyFormIntakePass(t.Context(), resolve)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, err = OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	finish := reserveIntakeApply(
		t, store, start, current.Receipt, resolved.Job.Revision, []int{0, 1}, "finish-role-prose",
	)
	finish.Intent = FormIntakeIntentClarify
	finish.NonData = []FormIntakeLiteral{intakeLiteral("Phone belongs second")}
	before, err := os.ReadFile(formJobStatePath(options))
	if err != nil {
		t.Fatal(err)
	}
	write := store.writeFile
	store.writeFile = func(string, []byte, os.FileMode) error { return errors.New("injected publication failure") }
	if failed, applyErr := store.ApplyFormIntakePass(t.Context(), finish); applyErr == nil || failed.Consumed {
		t.Fatal("failed publication acknowledged or consumed input")
	}
	store.writeFile = write
	after, err := os.ReadFile(formJobStatePath(options))
	if err != nil || string(before) != string(after) {
		t.Fatal("failed reconciliation changed the snapshot")
	}
	finished, err := store.ApplyFormIntakePass(t.Context(), finish)
	if err != nil || finished.Progress.PendingInputs != 0 || finished.Progress.RetainedBytes != 0 ||
		finished.Job.LedgerRevision != 2 {
		t.Fatalf("accepted values and fully accounted clarification left stale advice: pending=%d err=%v",
			finished.Progress.PendingInputs, err)
	}
	for _, ref := range []string{old.Receipt, current.Receipt} {
		input, readErr := store.ReadFormIntakeInput(t.Context(), ref, start.Owner)
		if readErr != nil || !input.Consumed || input.Text != "" || input.Passes != 2 {
			t.Fatal("reconciliation reset passes or retained consumed text")
		}
	}
	values, err := store.ReadValues(t.Context(), start.JobID, start.Owner, []string{
		schema.Fields[0].ID, schema.Fields[1].ID,
	})
	if err != nil || values[schema.Fields[0].ID].Value.Kind != ProtectedValueBlank ||
		values[schema.Fields[1].ID].Value.Text != "PHONE" {
		t.Fatal("advisory reconciliation changed a canonical value")
	}
	replay, err := store.ApplyFormIntakePass(t.Context(), finish)
	if err != nil || !replay.Reused || replay.Job.LedgerRevision != 2 || replay.Progress.PendingInputs != 0 {
		t.Fatal("completed replay changed reconciliation or values")
	}
	finish.NonData[0].Text = "changed replay"
	if _, err = store.ApplyFormIntakePass(t.Context(), finish); !errors.Is(err, ErrFormJobAnswerConflict) {
		t.Fatal("changed reconciliation replay was accepted")
	}
}

func TestFormIntakeDelayedAdvisoryReconciliationRequiresEveryNativeGate(t *testing.T) {
	for _, name := range []string{
		"accepted", "confirmation advice", "blank", "not applicable", "unaccounted", "missing scope coverage",
		"native invalid", "native conflict", "unsupported", "new advice", "outside view", "deferred",
		"missing canonical field", "superseded event", "empty event", "low confidence", "not user",
		"pending validation", "unconfirmed", "before admission", "legacy unbound pass", "uncommitted pass",
		"historical acknowledgement", "different accepted field", "staged input", "existing acknowledgement",
	} {
		t.Run(name, func(t *testing.T) {
			store := &FormJobStore{profileKey: []byte("private-test-profile-key")}
			schema := intakeTextSchema()
			schema.Fields = schema.Fields[:3]
			a, b, c := schema.Fields[0].ID, schema.Fields[1].ID, schema.Fields[2].ID
			proof := formIntakeStoredPass{
				ID: "form_pass_" + strings.Repeat("a", 32), ReceiptCount: 2, CandidateIDs: []string{b},
				Outcome: &formIntakeStoredOutcome{AcceptedIDs: []string{b}},
			}
			batchID := "form_batch_" + keyedDigest(store.profileKey, "intake_apply", proof.ID)[:32]
			eventID := "form_value_" + keyedDigest(store.profileKey, "composite_value", batchID, b)[:32]
			record := FormJobRecord{Fields: []FormJobFieldState{{
				FieldID: b, EventID: eventID, Source: FormValueSourceUser, State: FormValueConfirmed,
				Confidence: FormValueConfidenceExact, Validation: FormValueValidationValid,
			}}}
			input := formIntakeStoredInput{
				ID: "current", State: formIntakeInputCommitted, Text: "Context",
				Intent:    interactions.ProtectedAnswerValue,
				Accounted: []formIntakeSpan{{Start: 0, End: 7}},
				Reservations: []formIntakeStoredPass{
					{CandidateIDs: []string{a, b, c}, ReceiptCount: 2, Outcome: &formIntakeStoredOutcome{
						Findings: []FormFieldMappingBlocker{{FieldID: b, Code: "field_ambiguous"}},
					}},
					{CandidateIDs: []string{a, b, c}, ReceiptCount: 2, Outcome: &formIntakeStoredOutcome{
						Intent: FormIntakeIntentFacts,
					}},
				},
			}
			switch name {
			case "confirmation advice":
				input.Reservations[0].Outcome.Findings[0].Code = "field_confirmation_required"
			case "blank":
				record.Fields[0].State = FormValueBlanked
			case "not applicable":
				record.Fields[0].State = FormValueNotApplicable
			case "unaccounted":
				input.Text += " more facts"
			case "missing scope coverage":
				input.Accounted[0].ScopedInstruction = true
				input.Reservations[0].Outcome.ScopedInstruction = true
				input.Reservations[0].CandidateIDs, input.Reservations[1].CandidateIDs = []string{b}, []string{b}
			case "native invalid":
				input.Reservations[0].Outcome.Findings[0].Code = "field_invalid"
			case "native conflict":
				input.Reservations[0].Outcome.Findings[0].Code = "field_conflicting"
			case "unsupported":
				input.Reservations[0].Outcome.Findings[0].Code = "field_unsupported"
			case "new advice":
				input.Reservations[1].Outcome.Findings = []FormFieldMappingBlocker{{
					FieldID: b, Code: "field_ambiguous",
				}}
			case "outside view":
				input.Reservations[1].CandidateIDs = []string{a}
			case "deferred":
				record.DeferredFieldIDs = []string{b}
			case "missing canonical field":
				record.Fields = nil
			case "superseded event":
				record.Fields[0].EventID = "form_value_" + strings.Repeat("b", 32)
			case "empty event":
				record.Fields[0].EventID = ""
			case "low confidence":
				record.Fields[0].Confidence = FormValueConfidenceLow
			case "not user":
				record.Fields[0].Source = FormValueSource("model")
			case "pending validation":
				record.Fields[0].Validation = FormValueValidationPending
			case "unconfirmed":
				record.Fields[0].State = FormValueSupplied
			case "before admission":
				proof.ReceiptCount = 1
			case "legacy unbound pass":
				proof.ReceiptCount = 0
			case "uncommitted pass":
				proof.Outcome = nil
			case "historical acknowledgement":
				proof.Outcome.AcceptedIDs, proof.Outcome.AcknowledgedIDs = nil, []string{b}
			case "different accepted field":
				proof.Outcome.AcceptedIDs = []string{a}
			case "staged input":
				input.State = formIntakeInputStaged
			case "existing acknowledgement":
				input.Reservations[1].Outcome.AcknowledgedIDs = []string{b}
			}
			mode := &formIntakeStoredMode{Inputs: []formIntakeStoredInput{
				{ID: "source", State: formIntakeInputConsumed, Reservations: []formIntakeStoredPass{proof}}, input,
			}}
			prior, err := json.Marshal([]formIntakeStoredPass{proof, input.Reservations[0]})
			if err != nil {
				t.Fatal(err)
			}
			pass := &mode.Inputs[1].Reservations[1]
			previousIDs := slices.Clone(pass.Outcome.AcknowledgedIDs)
			ids := store.acknowledgeAccountedFormIntakeGuidance(mode, 1, schema, record, pass)
			want := name == "accepted" || name == "confirmation advice" || name == "blank" || name == "not applicable"
			if (len(ids) == 1) != want || len(ids) > 1 || want && ids[0] != b ||
				!want && !slices.Equal(pass.Outcome.AcknowledgedIDs, previousIDs) {
				t.Fatal("delayed reconciliation bypassed a native gate or duplicated an acknowledgement")
			}
			after, err := json.Marshal([]formIntakeStoredPass{
				mode.Inputs[0].Reservations[0], mode.Inputs[1].Reservations[0],
			})
			if err != nil || string(prior) != string(after) {
				t.Fatal("native reconciliation changed an older outcome")
			}
		})
	}
}
