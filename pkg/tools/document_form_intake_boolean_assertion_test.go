package tools

import (
	"slices"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/document"
)

func TestDocumentFormIntakeCarriesVerifiedTypedCheckboxThroughNativeApply(t *testing.T) {
	model := &intakeWorkflowTestInterpreter{}
	tool, ctx, schema, jobID, _ := intakeWorkflowFixture(t, model, func(schema *document.FormFieldsFacts) {
		schema.Fields[0].Kind = document.FormFieldCheckbox
	})
	fieldID, quote := schema.Fields[0].ID, "I want postal delivery"
	model.interpret = func(view document.FormIntakeInterpretationView) (document.FormIntakeInterpretation, error) {
		result := document.FormIntakeInterpretation{Intent: document.FormIntakeIntentFacts}
		if slices.Contains(view.Pass.CandidateIDs, fieldID) {
			value := true
			result.Proposals = []document.FormIntakeProposal{{
				FieldID: fieldID, BooleanAssertion: &value,
				Literals: []document.FormIntakeLiteral{{Text: quote, Occurrence: 1}},
			}}
		}
		return result, nil
	}
	progress, err := tool.FindProtectedFormIntake(ctx)
	if err != nil || progress == nil {
		t.Fatal(err)
	}
	ref, err := tool.AdmitProtectedFormIntake(ctx, bus.InboundDocumentIntakeGuard{
		JobID: jobID, ModeID: progress.ModeID,
	}, "typed-boolean", quote)
	if err != nil {
		t.Fatal(err)
	}
	result := decodeWorkflowResult(t, tool.Execute(ctx, map[string]any{
		"action": "form", "form_action": "continue", "answer_ref": ref,
	}).ForLLM)
	if result.Intake == nil || result.Intake.AcceptedCount != 1 || result.Intake.Progress.PendingInputs != 0 ||
		result.Intake.Failure != nil || len(result.Intake.Findings) != 0 {
		t.Fatal("verified assertion was not propagated through the real protected tool/store boundary")
	}
	owner, err := documentFormOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	values, err := tool.formJobs.ReadValues(t.Context(), jobID, owner, []string{fieldID})
	if err != nil || values[fieldID].Value.Boolean == nil || !*values[fieldID].Value.Boolean {
		t.Fatal("the native tool path stored a whole claim as text instead of a checkbox")
	}
}
