package document

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func booleanAssertionFixture(t *testing.T, quote string, kind FormFieldKind) (
	*FormJobStore, FormJobStoreOptions, FormIntakeStartRequest, FormIntakeApplyRequest,
) {
	t.Helper()
	store, options := newTestFormJobStore(t)
	schema := intakeTextSchema()
	schema.Fields[0].Kind = kind
	start := intakeApplyFixture(t, store, schema)
	mode, err := store.StartFormIntake(t.Context(), start)
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.Progress.ModeID, mode.Job.Revision, "boolean-claim", quote,
	))
	if err != nil {
		t.Fatal(err)
	}
	request := reserveIntakeApply(t, store, start, input.Receipt, input.Job.Revision, []int{0}, "boolean-claim")
	value := true
	request.Proposals = []FormIntakeProposal{intakeProposal(schema, 0, quote)}
	request.Proposals[0].BooleanAssertion = &value
	return store, options, start, request
}

func TestFormIntakeBooleanAssertionPreservesTypedValueProvenanceAndReplay(t *testing.T) {
	for _, value := range []bool{true, false} {
		quote := "I want postal delivery"
		if !value {
			quote = "I do not want postal delivery"
		}
		store, options, start, request := booleanAssertionFixture(t, quote, FormFieldCheckbox)
		*request.Proposals[0].BooleanAssertion = value
		result, err := store.ApplyFormIntakePass(t.Context(), request)
		if err != nil || !result.Consumed || result.Job.LedgerRevision != 1 || len(result.AcceptedFieldIDs) != 1 {
			t.Fatalf("source-quoted checkbox assertion did not commit once: %v", err)
		}
		values, err := store.ReadValues(t.Context(), start.JobID, start.Owner, result.AcceptedFieldIDs)
		if err != nil || values[start.Schema.Fields[0].ID].Value.Boolean == nil ||
			*values[start.Schema.Fields[0].ID].Value.Boolean != value {
			t.Fatal("typed checkbox value was replaced with its evidence text")
		}
		public, err := json.Marshal(request.Proposals[0])
		if err != nil || strings.Contains(string(public), "postal") || strings.Contains(string(public), "Boolean") {
			t.Fatal("private boolean assertion escaped ordinary JSON")
		}
		store.Close()
		store, err = OpenFormJobStore(options)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(store.Close)
		replayed, err := store.ApplyFormIntakePass(t.Context(), request)
		if err != nil || !replayed.Reused || replayed.Job.LedgerRevision != 1 || replayed.Progress.PendingInputs != 0 {
			t.Fatal("restart lost the exact assertion outcome or appended another event")
		}
		*request.Proposals[0].BooleanAssertion = !value
		if _, err = store.ApplyFormIntakePass(t.Context(), request); !errors.Is(err, ErrFormJobAnswerConflict) {
			t.Fatal("changing only the boolean value replayed as identical")
		}
		values, err = store.ReadValues(t.Context(), start.JobID, start.Owner, result.AcceptedFieldIDs)
		if err != nil || *values[start.Schema.Fields[0].ID].Value.Boolean != value {
			t.Fatal("caller mutation changed the persisted canonical boolean")
		}
	}
}

func TestFormIntakeBooleanAssertionRejectsInvalidNativeBindingsAtomically(t *testing.T) {
	for _, name := range []string{
		"text field", "disposition", "several quotes", "missing quote", "inverted token", "unoffered field", "prose overlap",
	} {
		t.Run(name, func(t *testing.T) {
			kind, quote := FormFieldCheckbox, "I want postal delivery"
			if name == "text field" {
				kind = FormFieldText
			}
			if name == "inverted token" {
				quote = "No"
			}
			store, _, start, request := booleanAssertionFixture(t, quote, kind)
			switch name {
			case "disposition":
				request.Proposals[0].Disposition = FormIntakeIntentionallyBlank
			case "several quotes":
				request.Proposals[0].Literals = append(request.Proposals[0].Literals, intakeLiteral(quote))
			case "missing quote":
				request.Proposals[0].Literals[0].Text = "Not provided by this input"
			case "unoffered field":
				request.Proposals[0].FieldID = start.Schema.Fields[1].ID
			case "prose overlap":
				request.NonData = []FormIntakeLiteral{intakeLiteral(quote)}
			}
			if _, err := store.ApplyFormIntakePass(t.Context(), request); !errors.Is(err, ErrFormJobAnswerConflict) {
				t.Fatalf("invalid assertion binding was not rejected: %v", err)
			}
			record, err := store.Get(t.Context(), start.JobID, start.Owner)
			input, readErr := store.ReadFormIntakeInput(t.Context(), request.Reference, start.Owner)
			if err != nil || readErr != nil || record.LedgerRevision != 0 || input.Consumed || input.Text != quote {
				t.Fatal("invalid assertion published a value or erased protected evidence")
			}
		})
	}
}

func TestFormIntakeBooleanAssertionStillRequiresExplicitCorrectionAuthority(t *testing.T) {
	store, _, start, first := booleanAssertionFixture(t, "I want postal delivery", FormFieldCheckbox)
	accepted, err := store.ApplyFormIntakePass(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	mode, err := store.FindFormIntake(t.Context(), start.Owner)
	if err != nil || mode == nil {
		t.Fatal(err)
	}
	current, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.ModeID, accepted.Job.Revision, "correct-delivery", "Correction: I do not want postal delivery",
	))
	if err != nil {
		t.Fatal(err)
	}
	request := reserveIntakeApply(t, store, start, current.Receipt, current.Job.Revision, []int{0}, "without-authority")
	value := false
	request.Proposals = []FormIntakeProposal{intakeProposal(start.Schema, 0, "I do not want postal delivery")}
	request.Proposals[0].BooleanAssertion = &value
	blocked, err := store.ApplyFormIntakePass(t.Context(), request)
	if err != nil || blocked.Job.LedgerRevision != 1 || len(blocked.Findings) != 1 ||
		blocked.Findings[0].Code != "field_conflicting" || blocked.Consumed {
		t.Fatal("typed assertion replaced an accepted checkbox without ordinary correction authority")
	}
	fresh := reserveIntakeApply(t, store, start, current.Receipt, blocked.Job.Revision, []int{0}, "with-authority")
	fresh.Proposals = request.Proposals
	fresh.Proposals[0].CorrectionEvidence = []FormIntakeLiteral{intakeLiteral("Correction:")}
	fresh.Proposals[0].SupersedesEventID = accepted.Job.Fields[0].EventID
	corrected, err := store.ApplyFormIntakePass(t.Context(), fresh)
	if err != nil || !corrected.Consumed || corrected.Job.LedgerRevision != 2 || corrected.Progress.PendingInputs != 0 {
		t.Fatalf("explicit source-bound correction failed: %v", err)
	}
}
