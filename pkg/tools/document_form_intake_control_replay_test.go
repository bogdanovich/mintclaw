package tools

import (
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/document"
)

func TestDocumentFormIntakeConsumedControlReplayRestoresFollowupAfterRestart(t *testing.T) {
	for _, intent := range []document.FormIntakeDialogIntent{document.FormIntakeIntentPause, document.FormIntakeIntentCancel} {
		t.Run(string(intent), func(t *testing.T) {
			model := &intakeWorkflowTestInterpreter{}
			tool, ctx, _, jobID, options := intakeWorkflowFixture(t, model)
			model.interpret = func(view document.FormIntakeInterpretationView) (document.FormIntakeInterpretation, error) {
				return document.FormIntakeInterpretation{
					Intent:  intent,
					NonData: []document.FormIntakeLiteral{{Text: view.Pass.Input.Text, Occurrence: 1}},
				}, nil
			}
			progress, err := tool.FindProtectedFormIntake(ctx)
			if err != nil || progress == nil {
				t.Fatal(err)
			}
			ref, err := tool.AdmitProtectedFormIntake(
				ctx,
				bus.InboundDocumentIntakeGuard{
					JobID:  jobID,
					ModeID: progress.ModeID,
				},
				"control-before-crash",
				string(intent),
			)
			if err != nil {
				t.Fatal(err)
			}
			args, err := tool.ProtectedAnswerContinuationArguments(ref)
			if err != nil {
				t.Fatal(err)
			}
			initial := tool.Execute(ctx, args)
			first := decodeWorkflowResult(t, initial.ForLLM)
			if initial.IsError || first.Intake == nil || first.Intake.Intent != intent ||
				first.Intake.Progress.PendingInputs != 0 || model.intakeCalls != 1 {
				t.Fatal("initial control did not commit its private intent")
			}
			// Stop before executing the native-control followup. Only the existing
			// encrypted outcome, not a new model interpretation, may restore it.
			tool.formJobs.Close()
			tool.formJobs, err = document.OpenFormJobStore(options)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(tool.formJobs.Close)
			for range 2 {
				replayed := tool.Execute(ctx, args)
				projection := decodeWorkflowResult(t, replayed.ForLLM)
				if replayed.IsError || projection.Intake == nil || projection.Intake.Intent != intent ||
					projection.Intake.Progress.PendingInputs != 0 || model.intakeCalls != 1 {
					t.Fatal("consumed receipt lost its exact intent or called the private model again")
				}
				followup, err := tool.ProtectedAnswerContinuationFollowup(replayed)
				if err != nil || followup == nil || followup.ResponseOnly {
					t.Fatal("replay did not restore the required native control")
				}
				if err = followup.ValidateArguments(map[string]any{
					"action": "form", "form_action": string(intent), "job_id": jobID,
				}); err != nil {
					t.Fatal(err)
				}
				if err = followup.ValidateArguments(map[string]any{
					"action": "form", "form_action": "commit", "job_id": jobID,
				}); err == nil {
					t.Fatal("replayed control acquired unrelated commit authority")
				}
			}
		})
	}
}
