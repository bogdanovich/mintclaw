package tools

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/media"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

const documentFormTaskSchema = "document.form_task_routing.v1"

type safeDocumentFormTask struct {
	SchemaVersion string                      `json:"schema_version"`
	Operation     string                      `json:"operation"`
	FormAction    string                      `json:"form_action"`
	Reference     string                      `json:"reference"`
	State         document.FormTaskRouteState `json:"state"`
	NextReference string                      `json:"next_reference,omitempty"`
	HandoffFresh  bool                        `json:"handoff_fresh,omitempty"`
	Reason        string                      `json:"reason,omitempty"`
}

func (tool *DocumentTool) ProtectedFormTaskRoutingConfigured() bool {
	if !tool.ProtectedFormIntakeConfigured() {
		return false
	}
	_, ok := tool.formInterpreter.(document.FormTaskRouter)
	return ok
}

func (tool *DocumentTool) AdmitProtectedFormTask(
	ctx context.Context, guard bus.InboundDocumentIntakeGuard, identity, text string, refs []string,
) (string, error) {
	if !guard.Valid() || !tool.ProtectedFormTaskRoutingConfigured() || tool.formJobs == nil {
		return "", document.ErrFormJobConflict
	}
	owner, err := documentFormOwner(ctx)
	if err != nil {
		return "", document.ErrFormJobUnauthorized
	}
	store, mediaOwner, err := tool.executionAuthority(ctx)
	if err != nil {
		return "", document.ErrFormJobUnauthorized
	}
	for _, ref := range refs {
		opened, openErr := store.OpenOwned(ref, mediaOwner)
		if openErr != nil {
			return "", document.ErrFormJobUnauthorized
		}
		if closeErr := opened.Close(); closeErr != nil {
			return "", document.ErrFormJobStoreUnavailable
		}
	}
	record, err := tool.formJobs.Get(ctx, guard.JobID, owner)
	if err != nil {
		return "", err
	}
	admitted, err := tool.formJobs.BeginFormTaskRoute(ctx, document.FormTaskRouteRequest{
		JobID: guard.JobID, ModeID: guard.ModeID, ExpectedRevision: record.Revision, Owner: owner,
		MessageIdentity: identity, Text: text, MediaRefs: refs,
	})
	if err != nil {
		return "", err
	}
	return admitted.Reference, nil
}

func (tool *DocumentTool) interpretFormTask(
	ctx context.Context, view document.FormTaskRouteSnapshot,
) (document.FormTaskRouteInterpretation, error) {
	router, ok := tool.formInterpreter.(document.FormTaskRouter)
	if !ok {
		return document.FormTaskRouteInterpretation{}, document.ErrFormAuditUnavailable
	}
	if _, err := tool.formPolicy.Revision(); err != nil {
		return document.FormTaskRouteInterpretation{}, document.ErrFormAuditUnavailable
	}
	for _, alias := range append([]string{tool.formPolicy.PrimaryModel}, tool.formPolicy.EquivalentFallbacks...) {
		result, err := router.InterpretFormTaskRoute(ctx, alias, view)
		if err == nil {
			return result, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return document.FormTaskRouteInterpretation{}, document.ErrFormAuditUnavailable
}

func (tool *DocumentTool) routeFormTaskWorkflow(
	ctx context.Context, store ownedDocumentMediaStore, mediaOwner media.MediaOwner,
	owner document.FormJobOwner, args map[string]any,
) *toolshared.ToolResult {
	reference := strings.TrimSpace(stringDocumentArg(args, "answer_ref"))
	var view document.FormTaskRouteSnapshot
	var err error
	if _, _, _, answerErr := document.ParseFormTaskRouteAnswerReference(reference); answerErr == nil {
		view, err = tool.formJobs.ReadFormTaskRouteAnswer(ctx, reference, owner)
		if err == nil && view.Answer.State == "committed" {
			if view.Answer.Text == "switch_task" || view.Answer.Text == "keep_form" {
				view, err = tool.formJobs.ApplyFormTaskRouteChoice(ctx, reference, owner)
			} else {
				for attempt := 0; attempt < document.MaxFormTaskAnswerPasses; attempt++ {
					reserved, reserveErr := tool.formJobs.ReserveFormTaskAnswerPass(ctx, reference, owner)
					if reserveErr != nil {
						err = reserveErr
						break
					}
					proposal, interpretationErr := tool.interpretFormTask(ctx, reserved)
					if interpretationErr != nil {
						err = interpretationErr
						break
					}
					view, err = tool.formJobs.DecideFormTaskAnswer(ctx, document.FormTaskAnswerDecision{
						Reference: reference, Owner: owner, ExpectedRevision: reserved.Revision,
						Pass: reserved.Answer.Passes, Action: proposal.Action, Evidence: proposal.Evidence,
					})
					if err != nil || view.Answer.State == "handled" {
						break
					}
				}
			}
			if err != nil {
				if holdErr := tool.formJobs.HoldFormTaskAnswer(ctx, reference, owner); holdErr != nil {
					return documentFormToolError(holdErr)
				}
				view, err = tool.formJobs.ReadFormTaskRouteAnswer(ctx, reference, owner)
			}
		}
	} else {
		view, err = tool.formJobs.ReadFormTaskRoute(ctx, reference, owner)
	}
	if err != nil {
		return documentFormToolError(err)
	}
	progress, err := tool.formJobs.FormIntakeForJob(ctx, view.JobID, owner)
	if err != nil || progress == nil {
		return documentFormToolError(document.ErrFormJobConflict)
	}
	if progress.State == document.FormIntakeSuspended {
		if view.State == document.FormTaskGranted {
			return documentFormTaskResult(view, "handoff_uncertain")
		}
		return documentFormTaskResult(view, "retained_for_suspended_draft")
	}
	for attempts := 0; attempts < document.MaxFormTaskRoutingPasses &&
		(view.State == document.FormTaskPending || view.State == document.FormTaskVerify); attempts++ {
		reserved, reserveErr := tool.formJobs.ReserveFormTaskRoute(ctx, view.Reference, owner)
		if reserveErr != nil {
			err = reserveErr
			break
		}
		proposal, interpretationErr := tool.interpretFormTask(ctx, reserved)
		if interpretationErr != nil {
			err = interpretationErr
			break
		}
		view, err = tool.formJobs.DecideFormTaskRoute(ctx, document.FormTaskRouteDecision{
			Reference: view.Reference, Owner: owner, ExpectedRevision: reserved.Revision,
			Pass: reserved.Passes, Action: proposal.Action, Evidence: proposal.Evidence,
		})
		if err != nil {
			break
		}
	}
	if err != nil {
		// A failed/stale decision may return no snapshot. Recover only the
		// original opaque request; this is question authority, never a grant.
		view, err = tool.formJobs.ReadFormTaskRoute(ctx, reference, owner)
		if err != nil {
			return documentFormToolError(err)
		}
		if questionErr := tool.formJobs.QuestionFormTaskRoute(
			ctx,
			view.Reference,
			view.Revision,
			owner,
		); questionErr != nil {
			return documentFormToolError(questionErr)
		}
		view, err = tool.formJobs.ReadFormTaskRoute(ctx, view.Reference, owner)
		if err != nil {
			return documentFormToolError(err)
		}
	}
	if view.State == document.FormTaskReady {
		// Protected continuation starts with one native routing call. Keep the
		// entire newly authorized request within the existing live-text ceiling.
		wanted := view.HandoffTextBytes + 256
		if wanted > toolshared.ToolLiveContextTextAllowance(ctx) {
			return documentFormTaskResult(view, "handoff_context_capacity")
		}
		requiresVision := false
		for _, ref := range view.MediaRefs {
			opened, openErr := store.OpenOwned(ref, mediaOwner)
			if openErr != nil {
				return documentFormToolError(document.ErrFormJobUnauthorized)
			}
			requiresVision = requiresVision || strings.HasPrefix(opened.Meta.ContentType, "image/")
			if closeErr := opened.Close(); closeErr != nil {
				return documentFormToolError(document.ErrFormJobStoreUnavailable)
			}
		}
		handoff, handoffErr := tool.formJobs.GrantFormTaskHandoff(ctx, view.Reference, owner)
		if handoffErr != nil {
			return documentFormToolError(handoffErr)
		}
		view.State = document.FormTaskGranted
		result := documentFormTaskResult(view, "handoff_uncertain")
		if !handoff.Reused {
			result = documentFormTaskResult(view, "")
			var projection safeDocumentFormTask
			_ = json.Unmarshal([]byte(result.ForLLM), &projection)
			projection.HandoffFresh = true
			encoded, _ := json.Marshal(projection)
			result.ForLLM = string(encoded)
			result.ContextText = "The operator selected a new task. The previous PDF draft is preserved. " +
				"Follow only this newly authorized request (and its attached media); ask its purpose if unstated:\n" + handoff.Text
			result.ContextMedia = handoff.Media
			result.ContextMediaRequiresVision = requiresVision
		}
		return result
	}
	if progress.State == document.FormIntakePaused {
		if view.State == document.FormTaskCurrent && view.FieldReference == "" {
			if err = tool.formJobs.QuestionFormTaskRoute(ctx, view.Reference, view.Revision, owner); err != nil {
				return documentFormToolError(err)
			}
			view, err = tool.formJobs.ReadFormTaskRoute(ctx, view.Reference, owner)
			if err != nil {
				return documentFormToolError(err)
			}
		}
		if view.State == document.FormTaskQuestion {
			return tool.formTaskQuestionResult(ctx, owner, view)
		}
		return documentFormTaskResult(view, "draft_paused")
	}
	var latest *toolshared.ToolResult
	if view.State == document.FormTaskCurrent {
		if view.FieldReference == "" {
			latest = tool.promoteFormTaskText(
				ctx,
				store,
				mediaOwner,
				owner,
				view.JobID,
				view.ModeID,
				view.Reference,
				view.Text,
			)
		} else {
			latest = tool.continueFormIntake(ctx, store, mediaOwner, owner, view.FieldReference)
		}
		if latest.IsError {
			return latest
		}
	}
	work, err := tool.formJobs.FormTaskRouteWork(ctx, view.JobID, owner)
	if err != nil {
		return documentFormToolError(err)
	}
	for _, pending := range work {
		job, routeID, _, parseErr := document.ParseFormTaskRouteAnswerReference(pending)
		if parseErr != nil || "form_route."+job+"."+routeID != view.Reference {
			continue
		}
		answerView, readErr := tool.formJobs.ReadFormTaskRouteAnswer(ctx, pending, owner)
		if readErr != nil {
			return documentFormToolError(readErr)
		}
		if answerView.Answer.State == "committed" {
			return tool.nextFormTaskResult(ctx, owner, pending)
		}
		latest = tool.promoteFormTaskText(
			ctx,
			store,
			mediaOwner,
			owner,
			view.JobID,
			view.ModeID,
			pending,
			answerView.Answer.Text,
		)
		if latest.IsError {
			return latest
		}
	}
	if view.State == document.FormTaskQuestion {
		return tool.formTaskQuestionResult(ctx, owner, view)
	}
	work, err = tool.formJobs.FormTaskRouteWork(ctx, view.JobID, owner)
	if err != nil {
		return documentFormToolError(err)
	}
	for _, pending := range work {
		if pending != view.Reference {
			result := documentFormTaskResult(view, "")
			var projection safeDocumentFormTask
			_ = json.Unmarshal([]byte(result.ForLLM), &projection)
			projection.NextReference = pending
			encoded, _ := json.Marshal(projection)
			result.ForLLM = string(encoded)
			return result
		}
	}
	if latest != nil {
		return latest
	}
	if view.State == document.FormTaskGranted {
		return documentFormTaskResult(view, "handoff_uncertain")
	}
	record, schema, loadErr := tool.loadFormWorkflow(ctx, store, mediaOwner, owner, view.JobID)
	if loadErr != nil {
		return documentFormToolError(loadErr)
	}
	progress, err = tool.formJobs.FormIntakeForJob(ctx, view.JobID, owner)
	if err != nil || progress == nil {
		return documentFormToolError(document.ErrFormJobConflict)
	}
	return tool.formIntakeResult(ctx, owner, schema, record, "continue", safeDocumentFormIntake{Progress: *progress})
}

func (tool *DocumentTool) nextFormTaskResult(
	ctx context.Context, owner document.FormJobOwner, reference string,
) *toolshared.ToolResult {
	root := reference
	if jobID, routeID, _, err := document.ParseFormTaskRouteAnswerReference(reference); err == nil {
		root = "form_route." + jobID + "." + routeID
	}
	view, err := tool.formJobs.ReadFormTaskRoute(ctx, root, owner)
	if err != nil {
		return documentFormToolError(err)
	}
	encoded, _ := json.Marshal(safeDocumentFormTask{
		SchemaVersion: documentFormTaskSchema, Operation: "form", FormAction: "route",
		Reference: root, State: view.State, NextReference: reference,
	})
	return &toolshared.ToolResult{ForLLM: string(encoded)}
}

func (tool *DocumentTool) promoteFormTaskText(
	ctx context.Context, store ownedDocumentMediaStore, mediaOwner media.MediaOwner, owner document.FormJobOwner,
	jobID, modeID, reference, text string,
) *toolshared.ToolResult {
	record, schema, err := tool.loadFormWorkflow(ctx, store, mediaOwner, owner, jobID)
	if err != nil {
		return documentFormToolError(err)
	}
	admitted, err := tool.formJobs.AdmitFormIntakeMessage(ctx, document.FormIntakeMessageRequest{
		JobID: jobID, ModeID: modeID, ExpectedRevision: record.Revision, Owner: owner, Schema: schema,
		MessageIdentity: reference, RoutingReference: reference, Text: text,
	})
	if err != nil {
		return documentFormToolError(err)
	}
	return tool.continueFormIntake(ctx, store, mediaOwner, owner, admitted.Receipt)
}

func documentFormTaskResult(view document.FormTaskRouteSnapshot, reason string) *toolshared.ToolResult {
	encoded, _ := json.Marshal(safeDocumentFormTask{
		SchemaVersion: documentFormTaskSchema, Operation: "form", FormAction: "route", Reference: view.Reference,
		State: view.State, Reason: reason,
	})
	return &toolshared.ToolResult{ForLLM: string(encoded)}
}

func (tool *DocumentTool) formTaskQuestionResult(
	ctx context.Context, owner document.FormJobOwner, view document.FormTaskRouteSnapshot,
) *toolshared.ToolResult {
	binding, err := tool.formJobs.NewFormTaskRouteQuestion(ctx, view.Reference, owner)
	if err != nil {
		return documentFormToolError(err)
	}
	question := interactions.Question{
		ID: "document_task_scope", Header: "Task or form?",
		Introduction: "The previous draft and accepted data are preserved. You may answer freely or use the choices.",
		Question: "Is this a new task, or should we continue the previous form without using this new attachment? " +
			"Accompanying text stays protected when continuing the form.",
		Options: []interactions.Option{
			{Label: "New task; preserve draft", Value: "switch_task"},
			{Label: "Continue the previous form", Value: "keep_form"},
		},
	}
	if view.Context.Language == "ru" {
		question.Header = "Задача или форма?"
		question.Introduction = "Прежний черновик и принятые данные сохранены. Можно ответить текстом или выбрать вариант."
		question.Question = "Это новая задача или продолжаем прежнюю форму без использования нового вложения? " +
			"Текст рядом с вложением останется в защищённом заполнении формы."
		question.Options[0].Label, question.Options[1].Label = "Новая задача; сохранить черновик", "Продолжить прежнюю форму"
	}
	request := interactions.SuspensionRequest{
		Kind: interactions.KindQuestion, Questions: []interactions.Question{question}, ProtectedAnswer: &binding,
		PromptSummary:  "Choose whether a protected PDF draft should give way to a new task",
		PromptLanguage: view.Context.Language, Timeout: documentFormQuestionTimeout,
	}
	if err = interactions.ValidateSuspensionRequest(request); err != nil {
		return documentFormToolError(document.ErrFormJobConflict)
	}
	result := documentFormTaskResult(view, "")
	result.Control.Suspension = &request
	return result
}

func documentFormTaskFollowup(result *toolshared.ToolResult) (*toolshared.ToolOnlyFollowup, bool, error) {
	var projection safeDocumentFormTask
	decoded := json.Unmarshal([]byte(result.ForLLM), &projection) == nil
	if !decoded || projection.SchemaVersion != documentFormTaskSchema {
		return nil, false, nil
	}
	if projection.HandoffFresh && projection.State == document.FormTaskGranted && result.ContextText != "" {
		return nil, true, nil
	}
	if projection.NextReference != "" {
		expected := map[string]any{"action": "form", "form_action": "route", "answer_ref": projection.NextReference}
		return &toolshared.ToolOnlyFollowup{
			Instruction: "Consume the next admitted protected message through document: " + projection.NextReference,
			ValidateArguments: func(args map[string]any) error {
				if !reflect.DeepEqual(args, expected) {
					return errors.New("protected task continuation differs")
				}
				return nil
			},
		}, true, nil
	}
	return &toolshared.ToolOnlyFollowup{
		ResponseOnly: true,
		Instruction: "Explain the native retained/uncertain state truthfully and briefly in the user's language. " +
			"The old draft is preserved. Do not claim the new task ran, request repeated facts or replay its effects. " +
			"For an uncertain handoff say it cannot be confirmed; retained input can use /retry_form without repeating data.",
	}, true, nil
}
