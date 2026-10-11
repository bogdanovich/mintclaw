package tools

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/document"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestDocumentFormIntakeRetryAtCapacityPreservesReceiptsAndBudgetAcrossRestart(t *testing.T) {
	model := &intakeWorkflowTestInterpreter{}
	tool, ctx, schema, jobID, options := intakeWorkflowFixture(t, model)
	progress, err := tool.FindProtectedFormIntake(ctx)
	if err != nil || progress == nil {
		t.Fatal("intake unavailable")
	}
	guard := bus.InboundDocumentIntakeGuard{JobID: jobID, ModeID: progress.ModeID}
	var first string
	for index := 0; index < document.MaxFormIntakePendingInputs; index++ {
		ref, admitErr := tool.AdmitProtectedFormIntake(ctx, guard,
			fmt.Sprintf("pending-%d", index), "PRIVATE_RETAINED_FACT")
		if admitErr != nil {
			t.Fatal("pending input could not be admitted")
		}
		if index == 0 {
			first = ref
		}
	}
	if _, err = tool.AdmitProtectedFormIntake(ctx, guard, "extra", "Retry processing"); err == nil {
		t.Fatal("fixture did not fill native pending capacity")
	}
	owner, err := documentFormOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before, err := tool.FindProtectedFormIntake(ctx)
	if err != nil || before == nil {
		t.Fatal("pending progress unavailable")
	}
	if _, err = tool.formJobs.ReserveFormIntakePass(ctx, document.FormIntakePassRequest{
		Reference: first, ExpectedRevision: before.Revision, Owner: owner, Schema: schema,
		CandidateIDs: []string{schema.Fields[0].ID}, IdempotencyKey: "prior-extraction",
	}); err != nil {
		t.Fatal(err)
	}
	inputBefore, err := tool.formJobs.ReadFormIntakeInput(ctx, first, owner)
	if err != nil || inputBefore.Passes != 1 {
		t.Fatal("prior extraction reservation unavailable")
	}
	tool.formJobs.Close()
	tool.formJobs, err = document.OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tool.formJobs.Close)
	for range 2 {
		ref, retryErr := tool.RetryProtectedFormIntake(ctx, guard)
		if retryErr != nil || ref != first || model.intakeCalls != 0 {
			t.Fatal("retry admitted a fresh input or interpreted during receipt selection")
		}
		guard.Reference = ref
	}
	after, err := tool.FindProtectedFormIntake(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("retry changed accepted state or retained-input accounting")
	}
	inputAfter, err := tool.formJobs.ReadFormIntakeInput(ctx, first, owner)
	if err != nil || !reflect.DeepEqual(inputBefore, inputAfter) {
		t.Fatal("retry changed protected input or reset its extraction budget")
	}
	for _, invalid := range []bus.InboundDocumentIntakeGuard{
		{JobID: "form_job_" + strings.Repeat("f", 32), ModeID: guard.ModeID},
		{JobID: guard.JobID, ModeID: "form_intake_" + strings.Repeat("f", 32)},
		{
			JobID: guard.JobID, ModeID: guard.ModeID,
			Reference: "form_intake." + guard.JobID + ".form_input_" + strings.Repeat("f", 32),
		},
	} {
		if _, err = tool.RetryProtectedFormIntake(ctx, invalid); err == nil {
			t.Fatal("retry changed a mismatched job, mode or persisted receipt")
		}
	}
	otherRoute := toolshared.WithToolRouteSessionKey(ctx, "unrelated-route")
	if _, err = tool.RetryProtectedFormIntake(otherRoute, guard); err == nil {
		t.Fatal("another owner route obtained a pending receipt")
	}
	if err = tool.ControlProtectedFormIntake(ctx, guard, "pause"); err != nil {
		t.Fatal(err)
	}
	if _, err = tool.RetryProtectedFormIntake(ctx, guard); err == nil {
		t.Fatal("retry implicitly resumed a paused mode")
	}
}

func TestDocumentFormIntakeRetryWithoutPendingInputRefuses(t *testing.T) {
	tool, ctx, _, jobID, _ := intakeWorkflowFixture(t, &intakeWorkflowTestInterpreter{})
	progress, err := tool.FindProtectedFormIntake(ctx)
	if err != nil || progress == nil {
		t.Fatal("intake unavailable")
	}
	if _, err = tool.RetryProtectedFormIntake(ctx,
		bus.InboundDocumentIntakeGuard{JobID: jobID, ModeID: progress.ModeID}); err == nil {
		t.Fatal("retry manufactured an input for an empty mode")
	}
}

func TestDocumentFormIntakePendingGuidanceDoesNotClaimReviewOrRequestReceivedFacts(t *testing.T) {
	projection := safeDocumentFormResult{
		Job:     &safeDocumentFormJob{JobID: "form_job_" + strings.Repeat("a", 32)},
		Mapping: &safeDocumentFormMapping{ReadyForReview: true},
		Intake:  &safeDocumentFormIntake{Progress: document.FormIntakeProgress{PendingInputs: 1}},
	}
	followup, err := documentFormIntakeFollowup(projection, false)
	if err != nil || followup == nil || !followup.ResponseOnly ||
		!strings.Contains(followup.Instruction, "Pending inputs still block review and output") ||
		!strings.Contains(followup.Instruction, "/retry_form") ||
		!strings.Contains(followup.Instruction, "without requesting received facts again") {
		t.Fatal("pending input invited a fake review checkpoint or another data message")
	}
}
