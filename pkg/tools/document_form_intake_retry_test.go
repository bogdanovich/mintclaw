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

func TestDocumentFormIntakeRetryRevisitsIncompleteAccountingWithoutReapplyingFacts(t *testing.T) {
	model := &intakeWorkflowTestInterpreter{}
	tool, ctx, schema, jobID, _ := intakeWorkflowFixture(t, model)
	model.interpret = func(view document.FormIntakeInterpretationView) (document.FormIntakeInterpretation, error) {
		result := document.FormIntakeInterpretation{Intent: document.FormIntakeIntentFacts}
		if model.intakeCalls == 1 {
			result.Proposals = []document.FormIntakeProposal{{
				FieldID:  schema.Fields[0].ID,
				Literals: []document.FormIntakeLiteral{{Text: "Casey", Occurrence: 1}},
			}}
		} else if model.intakeCalls > 2 {
			result.NonData = []document.FormIntakeLiteral{{Text: "I am", Occurrence: 1}}
		}
		return result, nil
	}
	progress, err := tool.FindProtectedFormIntake(ctx)
	if err != nil || progress == nil {
		t.Fatal("intake unavailable")
	}
	guard := bus.InboundDocumentIntakeGuard{JobID: jobID, ModeID: progress.ModeID}
	ref, err := tool.AdmitProtectedFormIntake(ctx, guard, "accounting-retry", "I am Casey")
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"action": "form", "form_action": "continue", "answer_ref": ref}
	first := tool.Execute(ctx, args)
	initial := decodeWorkflowResult(t, first.ForLLM)
	if first.IsError || initial.Intake == nil || initial.Intake.Failure != nil ||
		initial.Intake.AcceptedCount != 1 || initial.Intake.Progress.PendingInputs != 1 || model.intakeCalls != 2 {
		t.Fatal("fixture did not retain incomplete accounting after accepting one fact")
	}
	replayed := tool.Execute(ctx, args)
	if replayed.IsError || model.intakeCalls != 2 {
		t.Fatal("same-execution replay reinterpreted committed windows")
	}
	owner, err := documentFormOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before, err := tool.formJobs.Get(ctx, jobID, owner)
	if err != nil {
		t.Fatal(err)
	}
	guard.Reference = ref
	resolved, err := tool.RetryProtectedFormIntake(ctx, guard)
	if err != nil || resolved != ref {
		t.Fatal("retry did not preserve its original receipt")
	}
	retryCtx := toolshared.WithToolExecutionIdentity(ctx, "workspace", "accounting-explicit-retry")
	second := tool.Execute(retryCtx, args)
	finished := decodeWorkflowResult(t, second.ForLLM)
	if second.IsError || finished.Intake == nil || finished.Intake.Failure != nil ||
		finished.Intake.Progress.PendingInputs != 0 || finished.Intake.AcceptedCount != 0 || model.intakeCalls != 3 {
		t.Fatal("retry reused completed windows forever instead of processing newly accounted input")
	}
	after, err := tool.formJobs.Get(ctx, jobID, owner)
	if err != nil || after.Revision != before.Revision || after.LedgerRevision != before.LedgerRevision ||
		!reflect.DeepEqual(before.Fields, after.Fields) {
		t.Fatal("accounting retry reapplied or changed an accepted fact")
	}
	input, err := tool.formJobs.ReadFormIntakeInput(ctx, ref, owner)
	if err != nil || !input.Consumed || input.Passes != 3 || input.Text != "" {
		t.Fatal("accounting retry lost retention cleanup or reset its bounded budget")
	}
}

func TestDocumentFormIntakeRetryCanFinishAnEmptyOutcome(t *testing.T) {
	model := &intakeWorkflowTestInterpreter{}
	tool, ctx, _, jobID, _ := intakeWorkflowFixture(t, model)
	model.interpret = func(view document.FormIntakeInterpretationView) (document.FormIntakeInterpretation, error) {
		result := document.FormIntakeInterpretation{Intent: document.FormIntakeIntentClarify}
		if model.intakeCalls > 2 {
			result.NonData = []document.FormIntakeLiteral{{Text: view.Pass.Input.Text, Occurrence: 1}}
		}
		return result, nil
	}
	progress, err := tool.FindProtectedFormIntake(ctx)
	if err != nil || progress == nil {
		t.Fatal("intake unavailable")
	}
	ref, err := tool.AdmitProtectedFormIntake(ctx,
		bus.InboundDocumentIntakeGuard{JobID: jobID, ModeID: progress.ModeID}, "empty-outcome", "Explain the options")
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"action": "form", "form_action": "continue", "answer_ref": ref}
	first := decodeWorkflowResult(t, tool.Execute(ctx, args).ForLLM)
	if first.Intake == nil || first.Intake.Failure != nil || first.Intake.Progress.PendingInputs != 1 ||
		first.Intake.AcceptedCount != 0 || model.intakeCalls != 2 {
		t.Fatal("fixture did not retain an empty committed outcome")
	}
	retryCtx := toolshared.WithToolExecutionIdentity(ctx, "workspace", "empty-outcome-retry")
	second := decodeWorkflowResult(t, tool.Execute(retryCtx, args).ForLLM)
	if second.Intake == nil || second.Intake.Failure != nil || second.Intake.Progress.PendingInputs != 0 ||
		second.Intake.AcceptedCount != 0 || model.intakeCalls != 3 {
		t.Fatal("fresh retry could not complete accounting without an earlier accepted fact")
	}
}

func TestDocumentFormIntakeFreshRetriesDoNotResetThePassBudget(t *testing.T) {
	model := &intakeWorkflowTestInterpreter{}
	tool, ctx, _, jobID, _ := intakeWorkflowFixture(t, model)
	model.interpret = func(document.FormIntakeInterpretationView) (document.FormIntakeInterpretation, error) {
		return document.FormIntakeInterpretation{Intent: document.FormIntakeIntentFacts}, nil
	}
	progress, err := tool.FindProtectedFormIntake(ctx)
	if err != nil || progress == nil {
		t.Fatal("intake unavailable")
	}
	ref, err := tool.AdmitProtectedFormIntake(ctx,
		bus.InboundDocumentIntakeGuard{JobID: jobID, ModeID: progress.ModeID}, "bounded-retry", "PRIVATE_PENDING_INPUT")
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"action": "form", "form_action": "continue", "answer_ref": ref}
	for index := 0; index < document.MaxFormIntakeExtractionPasses/2; index++ {
		retryCtx := toolshared.WithToolExecutionIdentity(ctx, "workspace", fmt.Sprintf("bounded-retry-%d", index))
		projection := decodeWorkflowResult(t, tool.Execute(retryCtx, args).ForLLM)
		if projection.Intake == nil || projection.Intake.Failure != nil {
			t.Fatal("fresh retry failed before the existing pass budget was exhausted")
		}
	}
	retryCtx := toolshared.WithToolExecutionIdentity(ctx, "workspace", "exhausted-retry")
	projection := decodeWorkflowResult(t, tool.Execute(retryCtx, args).ForLLM)
	if projection.Intake == nil || projection.Intake.Failure == nil ||
		projection.Intake.Failure.Stage != "reservation" || projection.Intake.Progress.PendingInputs != 1 ||
		projection.Intake.AcceptedCount != 0 || model.intakeCalls != document.MaxFormIntakeExtractionPasses {
		t.Fatal("fresh executions bypassed the original persisted extraction budget")
	}
}

func TestDocumentFormIntakeBusyRetryUsesInboundIdentityAndReplaysExactly(t *testing.T) {
	model := &intakeWorkflowTestInterpreter{}
	tool, ctx, _, jobID, _ := intakeWorkflowFixture(t, model)
	model.interpret = func(view document.FormIntakeInterpretationView) (document.FormIntakeInterpretation, error) {
		result := document.FormIntakeInterpretation{Intent: document.FormIntakeIntentClarify}
		if model.intakeCalls > 2 {
			result.NonData = []document.FormIntakeLiteral{{Text: view.Pass.Input.Text, Occurrence: 1}}
		}
		return result, nil
	}
	progress, err := tool.FindProtectedFormIntake(ctx)
	if err != nil || progress == nil {
		t.Fatal("intake unavailable")
	}
	ref, err := tool.AdmitProtectedFormIntake(ctx,
		bus.InboundDocumentIntakeGuard{JobID: jobID, ModeID: progress.ModeID}, "busy-retry", "Explain the options")
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"action": "form", "form_action": "continue", "answer_ref": ref}
	initialCtx := toolshared.WithToolDocumentIntakeProcessingIdentity(ctx, ref, "initial-inbound")
	initial := decodeWorkflowResult(t, tool.Execute(initialCtx, args).ForLLM)
	if initial.Intake == nil || initial.Intake.Progress.PendingInputs != 1 || model.intakeCalls != 2 {
		t.Fatal("fixture did not retain incomplete windows")
	}
	if tool.Execute(initialCtx, args).IsError || model.intakeCalls != 2 {
		t.Fatal("same inbound replay reinterpreted committed windows")
	}
	recoveredCtx := toolshared.WithToolExecutionIdentity(
		initialCtx,
		toolshared.ToolWorkspace(initialCtx),
		"recovered-turn",
	)
	if tool.Execute(recoveredCtx, args).IsError || model.intakeCalls != 2 {
		t.Fatal("transport replay changed processing identity after turn recovery")
	}
	retryCtx := toolshared.WithToolDocumentIntakeProcessingIdentity(ctx, ref, "retry-inbound")
	if toolshared.ToolExecutionID(retryCtx) != toolshared.ToolExecutionID(initialCtx) {
		t.Fatal("busy retry changed global execution identity")
	}
	retried := decodeWorkflowResult(t, tool.Execute(retryCtx, args).ForLLM)
	if retried.Intake == nil || retried.Intake.Failure != nil || retried.Intake.Progress.PendingInputs != 0 ||
		retried.Intake.AcceptedCount != 0 || model.intakeCalls != 3 {
		t.Fatal("same-turn retry skipped incomplete accounting instead of progressing")
	}
	if tool.Execute(retryCtx, args).IsError || model.intakeCalls != 3 {
		t.Fatal("retry replay created another processing attempt")
	}
	owner, err := documentFormOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	input, err := tool.formJobs.ReadFormIntakeInput(ctx, ref, owner)
	if err != nil || input.Passes != 3 || !input.Consumed || input.Text != "" {
		t.Fatal("busy retry reset its budget or retained consumed private text")
	}
}
