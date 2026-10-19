package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type taskWorkflowInterpreter struct {
	*intakeWorkflowTestInterpreter
	route func(document.FormTaskRouteSnapshot) (document.FormTaskRouteInterpretation, error)
	calls int
}

func TestDocumentFormTaskBurstSuspendsAndResumesRetainedDraftWithoutReplayingTask(t *testing.T) {
	tool, ctx, owner, guard, model := taskWorkflowFixture(t, document.FormTaskSwitch)
	var refs []string
	for index := range document.MaxFormTaskRoutingPending {
		ref, err := tool.AdmitProtectedFormTask(ctx, guard, fmt.Sprintf("burst-%d", index), "PRIVATE_PENDING_TEXT", nil)
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, ref)
	}
	if _, err := tool.AdmitProtectedFormTask(
		ctx,
		guard,
		"over-capacity",
		"NOT_ADMITTED",
		nil,
	); !errors.Is(
		err,
		document.ErrFormJobCapacityExceeded,
	) {
		t.Fatal("burst admission was not bounded")
	}
	first := executeTaskRoute(tool, ctx, refs[0])
	if first.IsError || first.ContextText == "" {
		t.Fatal("initial new task was not released")
	}
	retained := executeTaskRoute(tool, ctx, refs[1])
	if retained.IsError || retained.ContextText != "" ||
		!strings.Contains(retained.ForLLM, "retained_for_suspended_draft") {
		t.Fatal("later guarded message escaped into the new task")
	}
	resumed := tool.Execute(ctx, map[string]any{"action": "form", "form_action": "resume", "job_id": guard.JobID})
	follow, err := tool.ToolResultFollowup(resumed)
	if resumed.IsError || err != nil || follow == nil || follow.ValidateArguments == nil ||
		follow.ValidateArguments(map[string]any{
			"action": "form", "form_action": "route", "answer_ref": refs[1],
		}) != nil {
		t.Fatal("resume did not prioritize retained native messages")
	}
	model.route = func(document.FormTaskRouteSnapshot) (document.FormTaskRouteInterpretation, error) {
		return document.FormTaskRouteInterpretation{Action: document.FormTaskContinue}, nil
	}
	for _, ref := range refs[1:] {
		result := executeTaskRoute(tool, ctx, ref)
		if result.IsError || result.ContextText != "" {
			t.Fatal("resume replayed the new task or lost a retained input")
		}
		view, err := tool.formJobs.ReadFormTaskRoute(ctx, ref, owner)
		if err != nil || view.FieldReference == "" {
			t.Fatal("retained old-form message was not promoted")
		}
	}
	if model.calls != 2+len(refs)-1 {
		t.Fatal("a granted task was classified again")
	}
}

func (model *taskWorkflowInterpreter) InterpretFormTaskRoute(
	_ context.Context, _ string, view document.FormTaskRouteSnapshot,
) (document.FormTaskRouteInterpretation, error) {
	model.calls++
	return model.route(view)
}

func taskWorkflowFixture(t *testing.T, action document.FormTaskRouteAction) (
	*DocumentTool, context.Context, document.FormJobOwner, bus.InboundDocumentIntakeGuard, *taskWorkflowInterpreter,
) {
	t.Helper()
	base := &intakeWorkflowTestInterpreter{interpret: func(document.FormIntakeInterpretationView) (
		document.FormIntakeInterpretation, error,
	) {
		return document.FormIntakeInterpretation{Intent: document.FormIntakeIntentFacts}, nil
	}}
	tool, ctx, _, jobID, _ := intakeWorkflowFixture(t, base)
	model := &taskWorkflowInterpreter{intakeWorkflowTestInterpreter: base}
	model.route = func(view document.FormTaskRouteSnapshot) (document.FormTaskRouteInterpretation, error) {
		result := document.FormTaskRouteInterpretation{Action: action}
		if action == document.FormTaskSwitch {
			result.Evidence = document.FormIntakeLiteral{Text: strings.TrimSpace(view.Text), Occurrence: 1}
		}
		return result, nil
	}
	tool.formInterpreter = model
	owner := mustDocumentFormOwner(t, ctx)
	progress, err := tool.formJobs.FormIntakeForJob(ctx, jobID, owner)
	if err != nil || progress == nil {
		t.Fatal(err)
	}
	return tool, ctx, owner, bus.InboundDocumentIntakeGuard{JobID: jobID, ModeID: progress.ModeID}, model
}

func executeTaskRoute(tool *DocumentTool, ctx context.Context, reference string) *toolshared.ToolResult {
	return tool.Execute(ctx, map[string]any{"action": "form", "form_action": "route", "answer_ref": reference})
}

func TestDocumentFormTaskSwitchBudgetDraftPreservationAndNoReplay(t *testing.T) {
	tool, ctx, owner, guard, model := taskWorkflowFixture(t, document.FormTaskSwitch)
	store, mediaOwner, err := tool.executionAuthority(ctx)
	if err != nil {
		t.Fatal(err)
	}
	record, schema, err := tool.loadFormWorkflow(ctx, store, mediaOwner, owner, guard.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = tool.formJobs.AppendValue(ctx, document.FormJobAppendValueRequest{
		JobID:            guard.JobID,
		ExpectedRevision: record.Revision,
		Owner:            owner,
		FieldID:          schema.Fields[0].ID,
		IdempotencyKey:   "accepted-before-switch",
		State:            document.FormValueSupplied,
		Source:           document.FormValueSourceUser,
		Value: document.FormProtectedValue{
			Kind: document.ProtectedValueText,
			Text: "PRIVATE_OLD_FORM_VALUE",
		},
	}); err != nil {
		t.Fatal(err)
	}
	before, err := tool.formJobs.Get(ctx, guard.JobID, owner)
	if err != nil {
		t.Fatal(err)
	}
	const request = "Explain pipelines instead. NEW_TASK_ONLY"
	ref, err := tool.AdmitProtectedFormTask(ctx, guard, "natural-new-task", request, nil)
	if err != nil {
		t.Fatal(err)
	}
	limited := executeTaskRoute(tool, toolshared.WithToolLiveContextTextAllowance(ctx, 0), ref)
	if limited.IsError || limited.ContextText != "" || !strings.Contains(limited.ForLLM, "handoff_context_capacity") {
		t.Fatal("insufficient context granted or truncated a task")
	}
	progress, err := tool.formJobs.FindFormIntake(ctx, owner)
	if err != nil || progress == nil || progress.State != document.FormIntakeActive {
		t.Fatal("budget refusal suspended the draft")
	}
	result := executeTaskRoute(tool, ctx, ref)
	if result.IsError || !strings.Contains(result.ContextText, request) ||
		strings.Contains(result.ContextText, "PRIVATE_OLD_FORM_VALUE") ||
		strings.Contains(result.ForLLM, request) ||
		model.calls != 2 {
		t.Fatal("whole new task did not reach live-only context after two reserved passes")
	}
	if follow, err := tool.ProtectedAnswerContinuationFollowup(result); err != nil || follow != nil {
		t.Fatal("handoff remained tool-fenced")
	}
	after, err := tool.formJobs.Get(ctx, guard.JobID, owner)
	if err != nil || after.LedgerRevision != before.LedgerRevision || after.SourceDigest != before.SourceDigest ||
		after.State != document.FormJobCollecting || len(after.Fields) != len(before.Fields) {
		t.Fatal("task switch changed the old form's fields/source/lifecycle")
	}
	replay := executeTaskRoute(tool, ctx, ref)
	if replay.IsError || replay.ContextText != "" || len(replay.ContextMedia) != 0 || model.calls != 2 {
		t.Fatal("routing replay repeated the task or private interpretation")
	}
	if follow, err := tool.ProtectedAnswerContinuationFollowup(
		replay,
	); err != nil || follow == nil ||
		!follow.ResponseOnly {
		t.Fatal("uncertain replay did not remain response-only")
	}
}

func TestDocumentFormTaskBudgetRefusalThenResumeRefreshesReadyConsent(t *testing.T) {
	tool, ctx, owner, guard, model := taskWorkflowFixture(t, document.FormTaskSwitch)
	ref, err := tool.AdmitProtectedFormTask(ctx, guard, "deferred-switch", "PRIVATE_NEW_TASK", nil)
	if err != nil {
		t.Fatal(err)
	}
	limited := executeTaskRoute(tool, toolshared.WithToolLiveContextTextAllowance(ctx, 0), ref)
	if limited.IsError || !strings.Contains(limited.ForLLM, "handoff_context_capacity") {
		t.Fatal("fixture did not retain ready handoff after context refusal")
	}
	for _, action := range []string{"pause", "resume"} {
		result := tool.Execute(ctx, map[string]any{"action": "form", "form_action": action, "job_id": guard.JobID})
		if result.IsError {
			t.Fatal("normal lifecycle transition failed")
		}
	}
	result := executeTaskRoute(tool, ctx, ref)
	if result.IsError || result.ContextText != "" || result.Control.Suspension == nil || model.calls != 2 {
		t.Fatal("stale ready handoff was lost, released, or privately reclassified instead of asking fresh consent")
	}
	view, err := tool.formJobs.ReadFormTaskRoute(ctx, ref, owner)
	if err != nil || view.State != document.FormTaskQuestion || view.Text != "PRIVATE_NEW_TASK" {
		t.Fatal("fresh protected question lost the original retained request")
	}
}

func TestDocumentFormTaskSameFormClarificationsRemainProtected(t *testing.T) {
	tool, ctx, owner, guard, model := taskWorkflowFixture(t, document.FormTaskContinue)
	for index, text := range []string{
		"Я заёмщик Casey Mint. Контакт на случай чрезвычайной ситуации — Robin Mint. Телефон 202-555-0147; пока не указываю, чей он.",
		"Предыдущий телефон принадлежит только заёмщику. Телефон контактного лица пока не указан. Город заёмщика Mintford.",
	} {
		ref, err := tool.AdmitProtectedFormTask(
			ctx,
			guard,
			[]string{"mixed-facts", "phone-owner-clarification"}[index],
			text,
			nil,
		)
		if err != nil {
			t.Fatal(err)
		}
		result := executeTaskRoute(tool, ctx, ref)
		if result.IsError || result.ContextText != "" || result.Control.Suspension != nil {
			t.Fatal("same-form facts switched tasks or triggered a scope question")
		}
		for _, private := range []string{"Casey Mint", "Robin Mint", "202-555-0147", "Mintford"} {
			if strings.Contains(result.ForLLM, private) {
				t.Fatal("private facts entered ordinary model context")
			}
		}
		view, err := tool.formJobs.ReadFormTaskRoute(ctx, ref, owner)
		if err != nil || view.FieldReference == "" || view.Text != "" {
			t.Fatal("facts not atomically linked into existing protected field intake")
		}
		input, err := tool.formJobs.ReadFormIntakeInput(ctx, view.FieldReference, owner)
		if err != nil || input.Text != text || input.AdmissionOrder != index+1 {
			t.Fatal("clarification lost text or admission order")
		}
	}
	if model.calls != 2 {
		t.Fatal("same-form messages spent switch-confirmation passes")
	}
}

func TestDocumentFormTaskPrivateFailureAsksScopeWithFreeTextAndButtons(t *testing.T) {
	tool, ctx, owner, guard, model := taskWorkflowFixture(t, document.FormTaskAsk)
	model.route = func(document.FormTaskRouteSnapshot) (document.FormTaskRouteInterpretation, error) {
		return document.FormTaskRouteInterpretation{}, errors.New("PRIVATE_MODEL_FAILURE")
	}
	source, err := tool.formJobs.SourceRef(ctx, guard.JobID, owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tool.AdmitProtectedFormTask(
		ctx,
		guard,
		"unowned-media",
		"PRIVATE_REQUEST",
		[]string{"media://unowned"},
	); err == nil {
		t.Fatal("unauthorized media was admitted for task switching")
	}
	ref, err := tool.AdmitProtectedFormTask(
		ctx,
		guard,
		"ambiguous-new-input",
		"PRIVATE_AMBIGUOUS_REQUEST",
		[]string{source},
	)
	if err != nil {
		t.Fatal(err)
	}
	result := executeTaskRoute(tool, ctx, ref)
	if result.IsError || result.Control.Suspension == nil || strings.Contains(result.ForLLM, "PRIVATE_") {
		t.Fatal("failed private interpretation lost input or leaked its cause")
	}
	question := result.Control.Suspension
	if len(question.Questions) != 1 || len(question.Questions[0].Options) != 2 || question.ProtectedAnswer == nil {
		t.Fatal("scope clarification lacks native choices/free answer binding")
	}
	view, err := tool.formJobs.ReadFormTaskRoute(ctx, ref, owner)
	if err != nil || view.Text != "PRIVATE_AMBIGUOUS_REQUEST" || len(view.MediaRefs) != 1 ||
		view.MediaRefs[0] != source {
		t.Fatal("scope question discarded original input")
	}
	sink, err := document.NewFormProtectedAnswerSink(tool.formJobs)
	if err != nil {
		t.Fatal(err)
	}
	answer := interactions.ProtectedAnswerSinkRequest{
		Binding: *question.ProtectedAnswer, Workspace: owner.WorkspaceID,
		Route: interactions.Route{
			AgentID:         owner.AgentID,
			Channel:         owner.Channel,
			AccountID:       owner.AccountID,
			ChatID:          owner.ChatID,
			ChatType:        owner.ChatType,
			SenderID:        owner.SenderID,
			TopicID:         owner.TopicID,
			SessionKey:      owner.RouteSessionKey,
			RouteSessionKey: owner.RouteSessionKey,
			SpaceID:         owner.SpaceID,
			SpaceType:       owner.SpaceType,
		},
		InteractionID: "scope-choice", IdempotencyKey: "scope-choice", MessageIdentity: "scope-choice",
		Intent: interactions.ProtectedAnswerValue, Text: "switch_task",
	}
	receipt, err := sink.Accept(ctx, answer)
	if err != nil {
		t.Fatal(err)
	}
	if err = sink.Commit(ctx, interactions.ProtectedAnswerCommitRequest{
		Binding: answer.Binding, Workspace: answer.Workspace, Route: answer.Route,
		InteractionID: answer.InteractionID, Receipt: receipt,
	}); err != nil {
		t.Fatal(err)
	}
	// A crash after the native claim must recover that exact committed reply,
	// not issue a second question that can never accept an answer.
	recovered := executeTaskRoute(tool, ctx, ref)
	follow, err := tool.ProtectedAnswerContinuationFollowup(recovered)
	if err != nil || follow == nil || follow.ValidateArguments == nil || follow.ValidateArguments(map[string]any{
		"action": "form", "form_action": "route", "answer_ref": receipt.Reference,
	}) != nil {
		t.Fatal("committed question reply was not recovered by its opaque receipt")
	}
	result = executeTaskRoute(tool, ctx, receipt.Reference)
	if result.IsError || !strings.Contains(result.ContextText, "PRIVATE_AMBIGUOUS_REQUEST") || model.calls != 1 {
		t.Fatal("canonical operator choice did not resolve private-model unavailability")
	}
	if len(result.ContextMedia) != 1 || result.ContextMedia[0] != source || result.ContextMediaRequiresVision {
		t.Fatal("bound PDF was lost, substituted or misclassified as an image")
	}
	var projection safeDocumentFormTask
	if json.Unmarshal([]byte(result.ForLLM), &projection) != nil || !projection.HandoffFresh {
		t.Fatal("fresh handoff metadata missing")
	}
}
