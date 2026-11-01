package tools

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/document"
)

func TestDocumentFormCompleteMappingWithPendingTaskStaysConversational(t *testing.T) {
	for _, test := range []struct {
		name         string
		ready        bool
		pendingTasks int
		responseOnly bool
	}{
		{name: "complete fields with unresolved task", ready: true, pendingTasks: 1, responseOnly: true},
		{name: "settled complete fields can review", ready: true},
		{name: "missing fields can still be planned", pendingTasks: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			projection := safeDocumentFormResult{
				FormAction: "continue", Job: &safeDocumentFormJob{JobID: "same-job"},
				Mapping: &safeDocumentFormMapping{ReadyForReview: test.ready},
				Intake: &safeDocumentFormIntake{Progress: document.FormIntakeProgress{
					PendingTasks: test.pendingTasks,
				}},
			}
			followup, err := documentFormIntakeFollowup(projection, false)
			if err != nil || followup == nil || followup.ResponseOnly != test.responseOnly {
				t.Fatal("field completeness crossed the pending-task conversation boundary")
			}
			if test.responseOnly {
				if !strings.Contains(followup.Instruction, "pending_tasks") ||
					!strings.Contains(followup.Instruction, "Do not reopen") ||
					!strings.Contains(followup.Instruction, "review") {
					t.Fatal("response-only progress lost its task and review fences")
				}
				return
			}
			review := map[string]any{"action": "form", "form_action": "review", "job_id": "same-job"}
			if followup.ValidateArguments == nil ||
				(followup.ValidateArguments(review) == nil) != test.ready {
				t.Fatal("settled review or missing-field planning changed authority")
			}
			if !test.ready && followup.ValidateArguments(map[string]any{
				"action": "form", "form_action": "status", "job_id": "same-job", "pages": []any{1},
			}) != nil {
				t.Fatal("an unresolved unrelated task blocked bounded form planning")
			}
		})
	}
}

func TestDocumentFormCompleteDraftReturnPreservesPendingRequestAndReviewFence(t *testing.T) {
	tool, ctx, owner, guard, model := taskWorkflowFixture(t, document.FormTaskAsk)
	values := make(map[string]string, 13)
	var paragraph []string
	for index := 0; index < 13; index++ {
		id, value := fmt.Sprintf("field_%064x", index+1), fmt.Sprintf("PRIVATE_LITERAL_%02d", index)
		values[id] = value
		paragraph = append(paragraph, value)
	}
	model.interpret = func(view document.FormIntakeInterpretationView) (document.FormIntakeInterpretation, error) {
		if view.Pass.Input.Text == "Return to the saved form and describe its status." {
			return document.FormIntakeInterpretation{
				Intent:  document.FormIntakeIntentReview,
				NonData: []document.FormIntakeLiteral{{Text: view.Pass.Input.Text, Occurrence: 1}},
			}, nil
		}
		result := document.FormIntakeInterpretation{Intent: document.FormIntakeIntentFacts}
		for _, field := range view.Pass.Fields {
			result.Proposals = append(result.Proposals, document.FormIntakeProposal{
				FieldID: field.ID, Literals: []document.FormIntakeLiteral{{Text: values[field.ID], Occurrence: 1}},
			})
		}
		return result, nil
	}
	input, err := tool.AdmitProtectedFormIntake(ctx, guard, "all-facts", strings.Join(paragraph, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	filled := tool.Execute(ctx, map[string]any{"action": "form", "form_action": "continue", "answer_ref": input})
	complete := decodeWorkflowResult(t, filled.ForLLM)
	if filled.IsError || complete.Mapping == nil || !complete.Mapping.ReadyForReview ||
		complete.Intake == nil || complete.Intake.Progress.PendingInputs != 0 {
		t.Fatal("fixture did not completely resolve the native form")
	}
	pending, err := tool.AdmitProtectedFormTask(ctx, guard, "new-purpose", "PRIVATE_UNRESOLVED_REQUEST", nil)
	if err != nil {
		t.Fatal(err)
	}
	question := executeTaskRoute(tool, ctx, pending)
	if question.IsError || question.Control.Suspension == nil {
		t.Fatal("fixture did not preserve a genuine task-scope question")
	}
	before, err := tool.formJobs.ReadFormTaskRoute(ctx, pending, owner)
	if err != nil {
		t.Fatal(err)
	}
	draftBefore, err := tool.formJobs.Get(ctx, guard.JobID, owner)
	if err != nil {
		t.Fatal(err)
	}
	model.route = func(document.FormTaskRouteSnapshot) (document.FormTaskRouteInterpretation, error) {
		return document.FormTaskRouteInterpretation{Action: document.FormTaskContinue}, nil
	}
	ref, err := tool.AdmitProtectedFormTask(
		ctx,
		guard,
		"return",
		"Return to the saved form and describe its status.",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	returned := executeTaskRoute(tool, ctx, ref)
	projection := decodeWorkflowResult(t, returned.ForLLM)
	followup, err := tool.ProtectedAnswerContinuationFollowup(returned)
	if returned.IsError || err != nil || followup == nil || !followup.ResponseOnly ||
		projection.Mapping == nil || !projection.Mapping.ReadyForReview || projection.Intake == nil ||
		projection.Intake.Progress.PendingTasks != 1 || projection.Intake.Progress.PendingInputs != 0 {
		t.Fatal("return invited a review that native pending-task authority must reject")
	}
	review := tool.Execute(ctx, map[string]any{"action": "form", "form_action": "review", "job_id": guard.JobID})
	if !review.IsError {
		t.Fatal("conversational return bypassed the native review/output fence")
	}
	after, err := tool.formJobs.ReadFormTaskRoute(ctx, pending, owner)
	if err != nil || before.State != document.FormTaskQuestion || after.State != before.State ||
		after.Text != before.Text || after.GrantRevision != before.GrantRevision || after.Passes != before.Passes {
		t.Fatal("form return erased, granted or executed the original request")
	}
	draftAfter, err := tool.formJobs.Get(ctx, guard.JobID, owner)
	if err != nil || !reflect.DeepEqual(draftAfter.Fields, draftBefore.Fields) ||
		draftAfter.SourceDigest != draftBefore.SourceDigest || draftAfter.LedgerRevision != draftBefore.LedgerRevision ||
		draftAfter.State != document.FormJobCollecting || draftAfter.ApprovalRevision != 0 ||
		draftAfter.OperationID != "" || draftAfter.ArtifactRef != "" {
		t.Fatal("conversational return changed accepted fields or output authority")
	}
}
