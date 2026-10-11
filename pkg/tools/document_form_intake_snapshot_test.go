package tools

import (
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/document"
)

func TestDocumentFormIntakeRelatedAdmissionDuringInterpretationRetriesFreshSnapshot(t *testing.T) {
	model := &intakeWorkflowTestInterpreter{}
	tool, ctx, schema, jobID, _ := intakeWorkflowFixture(t, model)
	progress, err := tool.FindProtectedFormIntake(ctx)
	if err != nil || progress == nil {
		t.Fatal(err)
	}
	guard := bus.InboundDocumentIntakeGuard{JobID: jobID, ModeID: progress.ModeID}
	first, err := tool.AdmitProtectedFormIntake(ctx, guard, "before-private-call", "Casey Mint")
	if err != nil {
		t.Fatal(err)
	}
	var second string
	sawRelated := false
	model.interpret = func(view document.FormIntakeInterpretationView) (document.FormIntakeInterpretation, error) {
		switch model.intakeCalls {
		case 1:
			second, err = tool.AdmitProtectedFormIntake(
				ctx,
				guard,
				"during-private-call",
				"These facts belong to the recipient.",
			)
			if err != nil {
				t.Fatal(err)
			}
		case 2:
			for _, input := range view.RelatedInputs {
				if input.Reference == second {
					sawRelated = true
				}
			}
		}
		return document.FormIntakeInterpretation{
			Intent: document.FormIntakeIntentFacts,
			Proposals: []document.FormIntakeProposal{
				{
					FieldID:  schema.Fields[0].ID,
					Literals: []document.FormIntakeLiteral{{Text: "Casey Mint", Occurrence: 1}},
				},
			},
		}, nil
	}
	args, err := tool.ProtectedAnswerContinuationArguments(first)
	if err != nil {
		t.Fatal(err)
	}
	result := tool.Execute(ctx, args)
	projection := decodeWorkflowResult(t, result.ForLLM)
	if result.IsError || projection.Intake == nil || projection.Intake.Failure != nil ||
		projection.Intake.AcceptedCount != 1 || projection.Intake.Progress.PendingInputs != 1 ||
		model.intakeCalls != 2 || !sawRelated {
		t.Fatal("admission race applied stale interpretation or failed to refresh related input")
	}
}
