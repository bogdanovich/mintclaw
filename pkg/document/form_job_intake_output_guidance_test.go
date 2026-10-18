package document

import (
	"errors"
	"testing"
)

func TestFormIntakeOutputOnlyHistoryCanBecomeWholeGuidance(t *testing.T) {
	store, options, start, mode, review := reviewedIntakeFixture(t)
	const text = "Finish the reviewed PDF without changing any values"
	admitted, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.ModeID, review.Job.Revision, "output-only-finish", text,
	))
	if err != nil {
		t.Fatal(err)
	}
	first := reserveIntakeApply(t, store, start, admitted.Receipt, admitted.Job.Revision, []int{0}, "old-output")
	first.Intent = FormIntakeIntentClarify
	first.Output = &FormIntakeOutputInstruction{Mode: FormIntakeOutputComplete, Literal: intakeLiteral(text)}
	first.Findings = []FormFieldMappingBlocker{
		{FieldID: start.Schema.Fields[0].ID, Code: "field_confirmation_required"},
	}
	uncertain, err := store.ApplyFormIntakePass(t.Context(), first)
	if err != nil || uncertain.Consumed || uncertain.Progress.PendingInputs != 1 {
		t.Fatalf("output-only uncertainty was not retained: %v", err)
	}
	private, err := store.ReadFormIntakeInput(t.Context(), admitted.Receipt, start.Owner)
	if err != nil || len(private.Accounted) != 1 || !private.Accounted[0].ReclassifiableOutput {
		t.Fatal("native eligibility was not exposed in the protected view")
	}
	second := reserveIntakeApply(t, store, start, admitted.Receipt, uncertain.Job.Revision, []int{0}, "whole-guidance")
	second.Intent, second.NonData = FormIntakeIntentFinish, []FormIntakeLiteral{intakeLiteral(text)}
	settled, err := store.ApplyFormIntakePass(t.Context(), second)
	if err != nil || !settled.Consumed || settled.Progress.PendingInputs != 0 ||
		settled.Job.Revision != review.Job.Revision || settled.Job.LedgerRevision != review.Job.LedgerRevision ||
		settled.Job.ReviewDigest != review.Job.ReviewDigest || settled.Job.ApprovalDigest != "" ||
		settled.Job.OutputScope != review.Job.OutputScope {
		t.Fatalf("whole guidance did not settle output-only bookkeeping without mutation: %v", err)
	}
	store.Close()
	store, err = OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	input, err := store.ReadFormIntakeInput(t.Context(), admitted.Receipt, start.Owner)
	if err != nil || !input.Consumed || input.Text != "" || len(input.Findings) != 0 {
		t.Fatal("restart resurrected output-only guidance")
	}
	replayed, err := store.ApplyFormIntakePass(t.Context(), second)
	if err != nil || !replayed.Reused || !replayed.Consumed || replayed.Job.Revision != review.Job.Revision {
		t.Fatal("resolved output-only guidance did not replay exactly")
	}
	second.Intent = FormIntakeIntentCancel
	if _, err = store.ApplyFormIntakePass(t.Context(), second); !errors.Is(err, ErrFormJobAnswerConflict) {
		t.Fatal("changed replay acquired control authority")
	}
}

func TestFormIntakeOutputOnlyReclassificationCannotHideFieldHistoryOrPartialGuidance(t *testing.T) {
	const text = "Finish this PDF with extra data"
	for _, name := range []string{
		"partial coverage", "facts intent", "current proposal", "current finding", "current output",
		"accepted history", "acknowledged history", "field disposition history", "positive evidence",
		"native invalid", "native conflict", "native unsupported", "native unresolved",
	} {
		t.Run(name, func(t *testing.T) {
			input := formIntakeStoredInput{
				Text: text, Accounted: []formIntakeSpan{{Start: 0, End: len(text), ScopedInstruction: true}},
				Reservations: []formIntakeStoredPass{{Outcome: &formIntakeStoredOutcome{ScopedInstruction: true}}},
			}
			request := FormIntakeApplyRequest{
				Intent: FormIntakeIntentFinish, NonData: []FormIntakeLiteral{intakeLiteral(text)},
			}
			history := input.Reservations[0].Outcome
			switch name {
			case "partial coverage":
				request.NonData = []FormIntakeLiteral{intakeLiteral("Finish this PDF")}
			case "facts intent":
				request.Intent = FormIntakeIntentFacts
			case "current proposal":
				request.Proposals = []FormIntakeProposal{{FieldID: "field_a"}}
			case "current finding":
				request.Findings = []FormFieldMappingBlocker{{FieldID: "field_a", Code: "field_ambiguous"}}
			case "current output":
				request.Output = &FormIntakeOutputInstruction{Mode: FormIntakeOutputComplete}
			case "accepted history":
				history.AcceptedIDs = []string{"field_a"}
			case "acknowledged history":
				history.AcknowledgedIDs = []string{"field_a"}
			case "field disposition history":
				history.ScopedIDs = []string{"field_a"}
			case "positive evidence":
				input.Accounted = append(input.Accounted, formIntakeSpan{Start: 0, End: 6, FieldID: "field_a"})
			default:
				codes := map[string]string{
					"native invalid": "field_invalid", "native conflict": "field_conflicting",
					"native unsupported": "field_unsupported", "native unresolved": "field_unresolved",
				}
				history.Findings = []FormFieldMappingBlocker{{FieldID: "field_a", Code: codes[name]}}
			}
			accounted := reclassifyFormIntakeOutputGuidance(input, request)
			if !accounted[0].ScopedInstruction || !input.Accounted[0].ScopedInstruction {
				t.Fatal("unsafe reclassification erased scoped history")
			}
		})
	}
}
