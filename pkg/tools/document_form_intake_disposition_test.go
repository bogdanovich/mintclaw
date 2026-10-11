package tools

import (
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/document"
)

// This qualifies the protected tool bridge, not a real PDF or channel ingress.
func TestDocumentFormIntakeSharedSectionDispositionThroughOrdinaryMessageBridge(t *testing.T) {
	const instruction = "The representative section does not apply PRIVATE_SECTION_09dc"
	model := &intakeWorkflowTestInterpreter{}
	tool, ctx, schema, jobID, _ := intakeWorkflowFixture(t, model, func(schema *document.FormFieldsFacts) {
		for index := range schema.Fields {
			schema.Fields[index].Required = false
			schema.Fields[index].Name = "Representative detail"
		}
	})
	model.interpret = func(view document.FormIntakeInterpretationView) (document.FormIntakeInterpretation, error) {
		result := document.FormIntakeInterpretation{Intent: document.FormIntakeIntentFacts}
		for _, field := range view.Pass.Fields {
			result.Proposals = append(result.Proposals, document.FormIntakeProposal{
				FieldID: field.ID, Disposition: document.FormIntakeNotApplicable,
				Literals: []document.FormIntakeLiteral{{Text: instruction, Occurrence: 1}},
			})
		}
		return result, nil
	}
	progress, err := tool.FindProtectedFormIntake(ctx)
	if err != nil || progress == nil {
		t.Fatal(err)
	}
	reference, err := tool.AdmitProtectedFormIntake(ctx, bus.InboundDocumentIntakeGuard{
		JobID: jobID, ModeID: progress.ModeID,
	}, "ordinary-section-disposition", instruction)
	if err != nil {
		t.Fatal(err)
	}
	args, err := tool.ProtectedAnswerContinuationArguments(reference)
	if err != nil {
		t.Fatal(err)
	}
	continued := tool.Execute(ctx, args)
	projection := decodeWorkflowResult(t, continued.ForLLM)
	if continued.IsError || projection.Intake == nil || projection.Intake.Failure != nil ||
		projection.Intake.AcceptedCount != len(schema.Fields) || projection.Intake.Progress.PendingInputs != 0 ||
		model.intakeCalls != 2 {
		t.Fatalf("shared instruction did not cross the eight-field window: %s", continued.ForLLM)
	}
	if strings.Contains(continued.ForLLM, instruction) || strings.Contains(continued.ContextText, instruction) {
		t.Fatal("private instruction escaped the safe tool projection")
	}
	owner, err := documentFormOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	job, err := tool.formJobs.Get(t.Context(), jobID, owner)
	if err != nil || len(job.Fields) != len(schema.Fields) {
		t.Fatal("disposition ledger did not contain the complete section")
	}
	for _, field := range job.Fields {
		if field.State != document.FormValueNotApplicable || field.ValueKind != document.ProtectedValueBlank {
			t.Fatal("scoped instruction was treated as a supplied field value")
		}
	}
	replayed := tool.Execute(ctx, args)
	if replayed.IsError || model.intakeCalls != 2 {
		t.Fatal("consumed instruction replay called the private model again")
	}
}
