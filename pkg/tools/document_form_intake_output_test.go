package tools

import (
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/document"
)

func TestDocumentFormIntakeUnsupportedDraftRetainsJobAndAllowsLaterNaturalCancel(t *testing.T) {
	const instruction = "Make an editable partial draft PRIVATE_DRAFT_64cf"
	const cancel = "Cancel this form"
	model := &intakeWorkflowTestInterpreter{}
	tool, ctx, _, jobID, _ := intakeWorkflowFixture(t, model)
	model.interpret = func(view document.FormIntakeInterpretationView) (document.FormIntakeInterpretation, error) {
		if view.Pass.Input.Text == cancel {
			return document.FormIntakeInterpretation{
				Intent:  document.FormIntakeIntentCancel,
				NonData: []document.FormIntakeLiteral{{Text: cancel, Occurrence: 1}},
			}, nil
		}
		return document.FormIntakeInterpretation{
			Intent: document.FormIntakeIntentDraft,
			Output: &document.FormIntakeOutputInstruction{
				Mode:    document.FormIntakeOutputDraft,
				Literal: document.FormIntakeLiteral{Text: instruction, Occurrence: 1},
			},
		}, nil
	}
	progress, err := tool.FindProtectedFormIntake(ctx)
	if err != nil || progress == nil {
		t.Fatal(err)
	}
	guard := bus.InboundDocumentIntakeGuard{JobID: jobID, ModeID: progress.ModeID}
	reference, err := tool.AdmitProtectedFormIntake(ctx, guard, "unsupported-draft", instruction)
	if err != nil {
		t.Fatal(err)
	}
	draftReference := reference
	args, err := tool.ProtectedAnswerContinuationArguments(reference)
	if err != nil {
		t.Fatal(err)
	}
	refused := tool.Execute(ctx, args)
	projection := decodeWorkflowResult(t, refused.ForLLM)
	if refused.IsError || projection.Intake == nil || projection.Intake.Failure == nil ||
		projection.Intake.Failure.Code != "editable_output_unavailable" || projection.Intake.AcceptedCount != 0 ||
		projection.Intake.Progress.PendingInputs != 1 || strings.Contains(refused.ForLLM, instruction) {
		t.Fatalf("unproved editable output lost its job or did not explain the limitation: %s", refused.ForLLM)
	}
	owner, err := documentFormOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	input, err := tool.formJobs.ReadFormIntakeInput(t.Context(), reference, owner)
	if err != nil || input.Consumed || input.Text != instruction {
		t.Fatal("unsupported draft request was silently consumed")
	}
	reference, err = tool.AdmitProtectedFormIntake(ctx, guard, "natural-cancel-after-refusal", cancel)
	if err != nil {
		t.Fatal(err)
	}
	args, err = tool.ProtectedAnswerContinuationArguments(reference)
	if err != nil {
		t.Fatal(err)
	}
	continued := tool.Execute(ctx, args)
	projection = decodeWorkflowResult(t, continued.ForLLM)
	if continued.IsError || projection.Intake == nil || projection.Intake.Intent != document.FormIntakeIntentCancel ||
		projection.Intake.Progress.PendingInputs != 1 {
		t.Fatal("older retained draft error suppressed the newer operator cancellation")
	}
	followup, err := tool.ToolResultFollowup(continued)
	if err != nil || followup == nil || followup.ResponseOnly {
		t.Fatal("natural cancellation did not select the existing bound control")
	}
	cancelArgs := map[string]any{"action": "form", "form_action": "cancel", "job_id": jobID}
	if err = followup.ValidateArguments(cancelArgs); err != nil {
		t.Fatal(err)
	}
	canceled := tool.Execute(ctx, cancelArgs)
	if canceled.IsError {
		t.Fatal(canceled.ForLLM)
	}
	job, err := tool.formJobs.Get(t.Context(), jobID, owner)
	if err != nil || job.State != document.FormJobCanceled {
		t.Fatal("natural cancellation left the same job active or retained its protected source")
	}
	if _, err = tool.formJobs.ReadFormIntakeInput(t.Context(), draftReference, owner); err == nil {
		t.Fatal("natural cancellation retained protected utterance authority")
	}
	if active, findErr := tool.FindProtectedFormIntake(ctx); findErr != nil || active != nil {
		t.Fatal("canceled form still owns ordinary-message ingress")
	}
}
