package tools

import (
	"slices"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/document"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestDocumentFormIntakeValidCorrectionSettlesEarlierInvalidEvidence(t *testing.T) {
	model := &intakeWorkflowTestInterpreter{}
	tool, ctx, schema, jobID, _ := intakeWorkflowFixture(t, model, func(schema *document.FormFieldsFacts) {
		schema.Fields[0].MaxLength = 4
	})
	target := schema.Fields[0].ID
	const original, correction = "My code is OVERLONG.", "Correction: my code is MINT."
	model.interpret = func(view document.FormIntakeInterpretationView) (document.FormIntakeInterpretation, error) {
		literal, prose := "OVERLONG", "My code is"
		if view.Pass.Input.Text == correction {
			literal, prose = "MINT", "Correction: my code is"
		}
		result := document.FormIntakeInterpretation{
			Intent:  document.FormIntakeIntentFacts,
			NonData: []document.FormIntakeLiteral{{Text: prose, Occurrence: 1}},
		}
		if !slices.Contains(view.Pass.CandidateIDs, target) {
			return result, nil
		}
		proposal := document.FormIntakeProposal{
			FieldID: target, Literals: []document.FormIntakeLiteral{{Text: literal, Occurrence: 1}},
		}
		if view.Pass.Input.Text == correction {
			for _, prior := range view.RelatedInputs {
				if prior.Text == original {
					proposal.Resolves = []document.FormIntakePriorLiteral{{
						Reference: prior.Reference, ExpectedPasses: prior.Passes,
						Literal: document.FormIntakeLiteral{Text: "OVERLONG", Occurrence: 1},
					}}
				}
			}
		}
		result.Proposals = []document.FormIntakeProposal{proposal}
		return result, nil
	}
	progress, err := tool.FindProtectedFormIntake(ctx)
	if err != nil || progress == nil {
		t.Fatal("intake unavailable")
	}
	guard := bus.InboundDocumentIntakeGuard{JobID: jobID, ModeID: progress.ModeID}
	first, err := tool.AdmitProtectedFormIntake(ctx, guard, "invalid-first", original)
	if err != nil {
		t.Fatal(err)
	}
	invalid := decodeWorkflowResult(t, tool.Execute(ctx, map[string]any{
		"action": "form", "form_action": "continue", "answer_ref": first,
	}).ForLLM)
	if invalid.Intake == nil || invalid.Intake.Progress.PendingInputs != 1 || len(invalid.Intake.Findings) != 1 {
		t.Fatal("fixture did not retain invalid input")
	}
	answer, err := tool.AdmitProtectedFormIntake(ctx, guard, "valid-correction", correction)
	if err != nil {
		t.Fatal(err)
	}
	ctx = toolshared.WithToolExecutionIdentity(ctx, "workspace", "valid-correction-turn")
	valid := decodeWorkflowResult(t, tool.Execute(ctx, map[string]any{
		"action": "form", "form_action": "continue", "answer_ref": answer,
	}).ForLLM)
	owner, err := documentFormOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	record, err := tool.formJobs.Get(ctx, jobID, owner)
	if err != nil || record.LedgerRevision != 1 || valid.Intake == nil || valid.Intake.AcceptedCount != 1 ||
		valid.Intake.Failure != nil || valid.Intake.Progress.PendingInputs != 0 || len(valid.Intake.Findings) != 0 {
		t.Fatal("valid correction was not accepted once with current, settled findings")
	}
}
