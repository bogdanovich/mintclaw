package document

import (
	"errors"
	"testing"
)

func TestFormIntakeInterpretationFindingsPersistUntilThatFieldIsResolved(t *testing.T) {
	store, options := newTestFormJobStore(t)
	request := intakeApplyFixture(t, store, intakeTextSchema())
	started, err := store.StartFormIntake(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	admission, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		request, started.Progress.ModeID, started.Job.Revision, "uncertain-person", "Casey Mint",
	))
	if err != nil {
		t.Fatal(err)
	}
	first := reserveIntakeApply(t, store, request, admission.Receipt, admission.Job.Revision, []int{0}, "uncertain")
	first.Findings = []FormFieldMappingBlocker{{FieldID: request.Schema.Fields[0].ID, Code: "field_ambiguous"}}
	first.NonData = []FormIntakeLiteral{intakeLiteral("Casey Mint")}
	result, err := store.ApplyFormIntakePass(t.Context(), first)
	if err != nil || result.Consumed || len(result.AcceptedFieldIDs) != 0 || len(result.Findings) != 1 {
		t.Fatal("uncertainty was acknowledged as consumed or assigned")
	}
	store.Close()
	store, err = OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	input, err := store.ReadFormIntakeInput(t.Context(), admission.Receipt, request.Owner)
	if err != nil || input.Text != "Casey Mint" || len(input.Findings) != 1 {
		t.Fatal("restart lost protected uncertainty")
	}
	other := reserveIntakeApply(t, store, request, admission.Receipt, input.Revision, []int{8}, "other-window")
	result, err = store.ApplyFormIntakePass(t.Context(), other)
	if err != nil || result.Consumed {
		t.Fatal("another field window cleared unresolved uncertainty")
	}
	input, err = store.ReadFormIntakeInput(t.Context(), admission.Receipt, request.Owner)
	if err != nil || len(input.Findings) != 1 {
		t.Fatal("unrelated pass discarded the targeted finding")
	}
	resolved := reserveIntakeApply(t, store, request, admission.Receipt, input.Revision, []int{0}, "resolved-person")
	resolved.Proposals = []FormIntakeProposal{intakeProposal(request.Schema, 0, "Casey Mint")}
	result, err = store.ApplyFormIntakePass(t.Context(), resolved)
	if err != nil || !result.Consumed || len(result.AcceptedFieldIDs) != 1 {
		t.Fatal("literal-backed resolution did not clear that finding")
	}
	input, err = store.ReadFormIntakeInput(t.Context(), admission.Receipt, request.Owner)
	if err != nil || !input.Consumed || input.Text != "" || len(input.Findings) != 0 {
		t.Fatal("resolved input retained raw data or uncertainty")
	}
	first.Findings[0].Code = "field_conflicting"
	if _, err = store.ApplyFormIntakePass(t.Context(), first); !errors.Is(err, ErrFormJobAnswerConflict) {
		t.Fatal("changed interpretation finding replayed as the original outcome")
	}
}

func TestFormIntakeInterpretationFindingsRejectUnknownDuplicateAndAssignedFields(t *testing.T) {
	for _, change := range []func(*FormIntakeApplyRequest){
		func(request *FormIntakeApplyRequest) {
			request.Findings = []FormFieldMappingBlocker{{FieldID: request.Schema.Fields[8].ID, Code: "field_ambiguous"}}
		},
		func(request *FormIntakeApplyRequest) {
			request.Findings = []FormFieldMappingBlocker{{FieldID: request.Schema.Fields[0].ID, Code: "invalid-code"}}
		},
		func(request *FormIntakeApplyRequest) {
			finding := FormFieldMappingBlocker{FieldID: request.Schema.Fields[0].ID, Code: "field_ambiguous"}
			request.Findings = []FormFieldMappingBlocker{finding, finding}
		},
		func(request *FormIntakeApplyRequest) {
			request.Findings = []FormFieldMappingBlocker{{FieldID: request.Schema.Fields[0].ID, Code: "field_ambiguous"}}
			request.Proposals = []FormIntakeProposal{intakeProposal(request.Schema, 0, "Casey Mint")}
		},
	} {
		store, _ := newTestFormJobStore(t)
		start := intakeApplyFixture(t, store, intakeTextSchema())
		started, err := store.StartFormIntake(t.Context(), start)
		if err != nil {
			t.Fatal(err)
		}
		admission, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
			start, started.Progress.ModeID, started.Job.Revision, "bad-finding", "Casey Mint",
		))
		if err != nil {
			t.Fatal(err)
		}
		request := reserveIntakeApply(t, store, start, admission.Receipt, admission.Job.Revision, []int{0}, "bad-pass")
		change(&request)
		if _, err = store.ApplyFormIntakePass(t.Context(), request); !errors.Is(err, ErrFormJobAnswerConflict) {
			t.Fatal("invalid interpretation finding accepted")
		}
		current, err := store.Get(t.Context(), start.JobID, start.Owner)
		if err != nil || current.Revision != admission.Job.Revision || len(current.Fields) != 0 {
			t.Fatal("rejected finding mutated the job")
		}
	}
}
