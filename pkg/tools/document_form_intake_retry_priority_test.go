package tools

import (
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/document"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestDocumentFormIntakeFindingPriorityPreservesWholeWindowsAndStableOrder(t *testing.T) {
	windows := [][]string{{"a", "b"}, {"c"}, {"d", "e"}, {"f"}, {"g"}}
	before := slices.Clone(windows)
	prioritizeFormIntakeFindingWindows(windows, nil)
	if !reflect.DeepEqual(before, windows) {
		t.Fatal("fresh input reordered structural windows")
	}
	prioritizeFormIntakeFindingWindows(windows, []document.FormFieldMappingBlocker{
		{FieldID: "e", Code: "field_invalid"},
		{FieldID: "c", Code: "field_confirmation_required"},
		{FieldID: "unrelated", Code: "field_ambiguous"},
	})
	if !reflect.DeepEqual(windows, [][]string{{"c"}, {"d", "e"}, {"a", "b"}, {"f"}, {"g"}}) {
		t.Fatal("priority changed candidate groups or their stable relative order")
	}
}

func TestDocumentFormIntakeClarificationUsesLastReservationForItsFindingAcrossRestart(t *testing.T) {
	model := &intakeWorkflowTestInterpreter{}
	tool, ctx, schema, jobID, options := intakeWorkflowFixture(t, model, func(schema *document.FormFieldsFacts) {
		for index := len(schema.Fields); index < 193; index++ {
			schema.Fields = append(schema.Fields, document.FormField{
				ID: fmt.Sprintf("field_%064x", index+1), Name: fmt.Sprintf("Generic detail %d", index),
				Kind: document.FormFieldText, Required: true,
				Widgets: []document.FormFieldWidget{{
					ID: fmt.Sprintf("widget_%064x", index+1), Page: index/8 + 1, Ordinal: 1,
				}},
			})
		}
	})
	target := schema.Fields[len(schema.Fields)-1].ID
	const clarification = "The last person is the borrower."
	model.interpret = func(view document.FormIntakeInterpretationView) (document.FormIntakeInterpretation, error) {
		if view.Pass.Input.Text == clarification {
			return document.FormIntakeInterpretation{
				Intent:  document.FormIntakeIntentClarify,
				NonData: []document.FormIntakeLiteral{{Text: clarification, Occurrence: 1}},
			}, nil
		}
		result := document.FormIntakeInterpretation{
			Intent:  document.FormIntakeIntentFacts,
			NonData: []document.FormIntakeLiteral{{Text: "My name is", Occurrence: 1}},
		}
		if !slices.Contains(view.Pass.CandidateIDs, target) {
			return result, nil
		}
		if len(view.RelatedInputs) == 0 {
			result.Findings = []document.FormFieldMappingBlocker{{FieldID: target, Code: "field_confirmation_required"}}
		} else {
			result.Proposals = []document.FormIntakeProposal{{
				FieldID: target, Literals: []document.FormIntakeLiteral{{Text: "PRIVATE_LITERAL", Occurrence: 1}},
			}}
		}
		return result, nil
	}
	progress, err := tool.FindProtectedFormIntake(ctx)
	if err != nil || progress == nil {
		t.Fatal("intake unavailable")
	}
	guard := bus.InboundDocumentIntakeGuard{JobID: jobID, ModeID: progress.ModeID}
	ref, err := tool.AdmitProtectedFormIntake(ctx, guard, "original-large-message", "My name is PRIVATE_LITERAL")
	if err != nil {
		t.Fatal(err)
	}
	first := decodeWorkflowResult(t, tool.Execute(ctx,
		map[string]any{"action": "form", "form_action": "continue", "answer_ref": ref}).ForLLM)
	if first.Intake == nil || first.Intake.Failure != nil || first.Intake.Progress.PendingInputs != 1 ||
		first.Intake.AcceptedCount != 0 || len(first.Intake.Findings) != 1 || model.intakeCalls < 20 {
		t.Fatal("large form did not retain the original ambiguous literal")
	}
	owner, err := documentFormOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	input, err := tool.formJobs.ReadFormIntakeInput(ctx, ref, owner)
	if err != nil {
		t.Fatal(err)
	}
	for index := input.Passes; index < document.MaxFormIntakeExtractionPasses-1; index++ {
		if _, err = tool.formJobs.ReserveFormIntakePass(ctx, document.FormIntakePassRequest{
			Reference: ref, ExpectedRevision: input.Revision, Owner: owner, Schema: schema,
			CandidateIDs: []string{schema.Fields[0].ID}, IdempotencyKey: fmt.Sprintf("prior-attempt-%d", index),
		}); err != nil {
			t.Fatal(err)
		}
	}
	tool.formJobs.Close()
	tool.formJobs, err = document.OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tool.formJobs.Close)
	answer, err := tool.AdmitProtectedFormIntake(ctx, guard, "role-clarification", clarification)
	if err != nil {
		t.Fatal(err)
	}
	beforeCalls := model.intakeCalls
	retryCtx := toolshared.WithToolExecutionIdentity(ctx, "workspace", "clarification-after-restart")
	args := map[string]any{"action": "form", "form_action": "continue", "answer_ref": answer}
	result := tool.Execute(retryCtx, args)
	finished := decodeWorkflowResult(t, result.ForLLM)
	if result.IsError || finished.Intake == nil || finished.Intake.Failure != nil ||
		finished.Intake.AcceptedCount != 1 || finished.Intake.Progress.PendingInputs != 0 ||
		model.intakeCalls != beforeCalls+2 {
		t.Fatal("clarification exhausted the original budget on unrelated windows")
	}
	for _, reference := range []string{ref, answer} {
		stored, readErr := tool.formJobs.ReadFormIntakeInput(ctx, reference, owner)
		if readErr != nil || !stored.Consumed || stored.Text != "" ||
			(reference == ref && stored.Passes != document.MaxFormIntakeExtractionPasses) {
			t.Fatal("clarification lost cleanup or reset the original budget")
		}
	}
	replayed := decodeWorkflowResult(t, tool.Execute(retryCtx, args).ForLLM)
	record, err := tool.formJobs.Get(ctx, jobID, owner)
	if err != nil || record.LedgerRevision != 1 || replayed.Intake == nil ||
		replayed.Intake.AcceptedCount != 0 || model.intakeCalls != beforeCalls+2 {
		t.Fatal("replay repeated interpretation or the original field event")
	}
}
