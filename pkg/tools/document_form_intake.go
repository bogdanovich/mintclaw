package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/media"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type safeDocumentFormIntake struct {
	Progress          document.FormIntakeProgress        `json:"progress"`
	RequestRevision   int64                              `json:"request_revision,omitempty"`
	Question          string                             `json:"question,omitempty"`
	Summary           string                             `json:"form_summary,omitempty"`
	Plan              string                             `json:"collection_plan,omitempty"`
	AcceptedCount     int                                `json:"accepted_count"`
	AcknowledgedCount int                                `json:"acknowledged_count"`
	Findings          []document.FormFieldMappingBlocker `json:"findings,omitempty"`
	FindingFields     []safeDocumentFormField            `json:"finding_fields,omitempty"`
	Intent            document.FormIntakeDialogIntent    `json:"intent,omitempty"`
	FindingsTruncated bool                               `json:"findings_truncated,omitempty"`
	Failure           *safeDocumentFormFailure           `json:"failure,omitempty"`
}

// ProtectedFormIntakeConfigured gates fresh-message admission on the explicit
// composition option. A configured but unavailable store must fail closed.
func (tool *DocumentTool) ProtectedFormIntakeConfigured() bool {
	return tool != nil && !tool.readOnly && tool.formInterpreter != nil
}

// ControlProtectedFormIntake is a trusted ingress seam for explicit commands.
// Natural-language controls are interpreted privately and then use the same
// ordinary document actions. Neither route grants output approval.
func (tool *DocumentTool) ControlProtectedFormIntake(
	ctx context.Context, guard bus.InboundDocumentIntakeGuard, action string,
) error {
	if !guard.Valid() || !tool.ProtectedFormIntakeConfigured() || tool.formJobs == nil {
		return document.ErrFormJobConflict
	}
	owner, err := documentFormOwner(ctx)
	if err != nil {
		return document.ErrFormJobUnauthorized
	}
	progress, err := tool.formJobs.FindFormIntake(ctx, owner)
	if err != nil || progress == nil || progress.JobID != guard.JobID || progress.ModeID != guard.ModeID {
		return document.ErrFormJobConflict
	}
	if action == "cancel" {
		store, _, authorityErr := tool.executionAuthority(ctx)
		if authorityErr != nil {
			return document.ErrFormJobUnauthorized
		}
		result := tool.cancelFormWorkflow(ctx, store, owner, map[string]any{"job_id": guard.JobID})
		if result.IsError {
			return document.ErrFormJobStoreUnavailable
		}
		return nil
	}
	state := document.FormIntakePaused
	if action == "resume" {
		state = document.FormIntakeActive
	} else if action != "pause" {
		return document.ErrFormJobAnswerConflict
	}
	_, err = tool.formJobs.SetFormIntakeState(ctx, guard.JobID, guard.ModeID, progress.Revision, owner, state)
	return err
}

func (tool *DocumentTool) setFormIntakeWorkflowState(
	ctx context.Context, store ownedDocumentMediaStore, mediaOwner media.MediaOwner,
	owner document.FormJobOwner, args map[string]any, action string,
) *toolshared.ToolResult {
	record, schema, err := tool.loadFormWorkflow(ctx, store, mediaOwner, owner, stringDocumentArg(args, "job_id"))
	if err != nil {
		return documentFormToolError(err)
	}
	progress, err := tool.formJobs.FindFormIntake(ctx, owner)
	if err != nil || progress == nil || progress.JobID != record.JobID {
		return documentFormToolError(document.ErrFormJobConflict)
	}
	guard := bus.InboundDocumentIntakeGuard{JobID: progress.JobID, ModeID: progress.ModeID}
	if err = tool.ControlProtectedFormIntake(ctx, guard, action); err != nil {
		return documentFormToolError(err)
	}
	progress, err = tool.formJobs.FindFormIntake(ctx, owner)
	if err != nil || progress == nil {
		return documentFormToolError(document.ErrFormJobConflict)
	}
	record, err = tool.formJobs.Get(ctx, record.JobID, owner)
	if err != nil {
		return documentFormToolError(err)
	}
	return tool.formIntakeResult(ctx, owner, schema, record, action, safeDocumentFormIntake{Progress: *progress})
}

// FindProtectedFormIntake is trusted runtime admission, not a model tool. An
// unavailable store is an error, never proof that protected mode is absent.
func (tool *DocumentTool) FindProtectedFormIntake(ctx context.Context) (*document.FormIntakeProgress, error) {
	if tool == nil || tool.readOnly || tool.formJobs == nil {
		return nil, document.ErrFormJobStoreUnavailable
	}
	owner, err := documentFormOwner(ctx)
	if err != nil {
		return nil, document.ErrFormJobUnauthorized
	}
	return tool.formJobs.FindFormIntake(ctx, owner)
}

// AdmitProtectedFormIntake runs only after the coordinator persists the exact
// document guard. Source/schema loading uses the existing immutable media owner.
func (tool *DocumentTool) AdmitProtectedFormIntake(
	ctx context.Context, guard bus.InboundDocumentIntakeGuard, identity, text string,
) (string, error) {
	if !guard.Valid() || tool == nil || tool.readOnly || tool.formJobs == nil || tool.formInterpreter == nil {
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
	record, schema, err := tool.loadFormWorkflow(ctx, store, mediaOwner, owner, guard.JobID)
	if err != nil {
		return "", err
	}
	admission, err := tool.formJobs.AdmitFormIntakeMessage(ctx, document.FormIntakeMessageRequest{
		JobID: guard.JobID, ModeID: guard.ModeID, ExpectedRevision: record.Revision,
		Owner: owner, Schema: schema, MessageIdentity: identity, Text: text,
	})
	if err != nil {
		return "", err
	}
	return admission.Receipt, nil
}

func (tool *DocumentTool) collectFormIntake(
	ctx context.Context, owner document.FormJobOwner, schema document.FormFieldsFacts,
	record document.FormJobRecord, args map[string]any, summary, plan string,
) *toolshared.ToolResult {
	if tool.formInterpreter == nil {
		return documentFormToolFailure(
			"protected_interpretation_unavailable",
			"protected free-text interpretation is unavailable",
		)
	}
	ids, err := documentFormIntakeCandidateIDs(args["candidate_field_ids"])
	if err != nil {
		return documentFormToolFailure("invalid_input", "intake requires a bounded candidate field selection")
	}
	context := document.FormIntakeContext{
		CandidateFieldIDs: ids, FocusFieldID: stringDocumentArg(args, "field_id"),
		RoleContext: strings.TrimSpace(stringDocumentArg(args, "role_context")),
		Question:    strings.TrimSpace(stringDocumentArg(args, "question")),
		Language:    stringDocumentArg(args, "interaction_language"),
	}
	current, err := tool.formJobs.FindFormIntake(ctx, owner)
	if err != nil {
		return documentFormToolError(err)
	}
	startKey, err := documentFormStartKey(ctx, record.JobID)
	if err != nil {
		return documentFormToolFailure("invalid_input", "intake execution identity is unavailable")
	}
	request := document.FormIntakeStartRequest{
		JobID: record.JobID, ExpectedRevision: record.Revision, Owner: owner, Schema: schema,
		Context: context, IdempotencyKey: "intake:" + startKey,
	}
	var progress document.FormIntakeProgress
	if current == nil {
		if summary == "" || plan == "" {
			return documentFormToolFailure(
				"agent_plan_required",
				"free-text intake requires a grounded summary and grouped needs",
			)
		}
		started, startErr := tool.formJobs.StartFormIntake(ctx, request)
		if startErr != nil {
			return documentFormToolError(startErr)
		}
		progress, record = started.Progress, started.Job
	} else {
		if current.JobID != record.JobID {
			return documentFormToolFailure("form_job_conflict", "another form intake owns this exact route")
		}
		progress, err = tool.formJobs.UpdateFormIntakeContext(ctx, request, current.ModeID)
		if err != nil {
			return documentFormToolError(err)
		}
		record, err = tool.formJobs.Get(ctx, record.JobID, owner)
		if err != nil {
			return documentFormToolError(err)
		}
	}
	return tool.formIntakeResult(ctx, owner, schema, record, "collect", safeDocumentFormIntake{
		Progress: progress, Question: context.Question, Summary: summary, Plan: plan,
	})
}

func documentFormIntakeCandidateIDs(raw any) ([]string, error) {
	var ids []string
	switch values := raw.(type) {
	case []string:
		ids = slices.Clone(values)
	case []any:
		for _, value := range values {
			id, ok := value.(string)
			if !ok {
				return nil, errors.New("invalid intake candidate")
			}
			ids = append(ids, id)
		}
	default:
		return nil, errors.New("invalid intake candidates")
	}
	if len(ids) == 0 || len(ids) > document.MaxFormIntakeCandidates {
		return nil, errors.New("invalid intake candidate count")
	}
	for index, id := range ids {
		if id == "" || strings.TrimSpace(id) != id || slices.Contains(ids[:index], id) {
			return nil, errors.New("invalid intake candidate")
		}
	}
	return ids, nil
}

func (tool *DocumentTool) formIntakeResult(
	ctx context.Context, owner document.FormJobOwner, schema document.FormFieldsFacts,
	record document.FormJobRecord, action string, intake safeDocumentFormIntake,
) *toolshared.ToolResult {
	result := tool.formProgressResult(ctx, owner, schema, record, action, "")
	if result.IsError {
		return result
	}
	var projection safeDocumentFormResult
	if err := json.Unmarshal([]byte(result.ForLLM), &projection); err != nil {
		return documentFormToolFailure("protected_store_unavailable", "form intake progress is unavailable")
	}
	projection.Intake = &intake
	for _, finding := range intake.Findings {
		if len(intake.FindingFields) == document.MaxFormIntakeCandidates {
			break
		}
		for _, field := range schema.Fields {
			if field.ID == finding.FieldID {
				intake.FindingFields = append(intake.FindingFields, *documentFormFieldProjection(field, finding.Code))
				break
			}
		}
	}
	return preserveDocumentToolVisibility(documentFormToolResult(projection))
}

func (tool *DocumentTool) interpretFormIntake(
	ctx context.Context, view document.FormIntakeInterpretationView,
) (document.FormIntakeInterpretation, error) {
	if tool.formInterpreter == nil {
		return document.FormIntakeInterpretation{}, document.ErrFormAuditUnavailable
	}
	if _, err := tool.formPolicy.Revision(); err != nil {
		return document.FormIntakeInterpretation{}, document.ErrFormAuditUnavailable
	}
	aliases := append([]string{tool.formPolicy.PrimaryModel}, tool.formPolicy.EquivalentFallbacks...)
	for _, alias := range aliases {
		result, err := tool.formInterpreter.InterpretFormIntake(ctx, alias, view)
		if err == nil {
			return result, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return document.FormIntakeInterpretation{}, document.ErrFormAuditUnavailable
}

func (tool *DocumentTool) continueFormIntake(
	ctx context.Context, store ownedDocumentMediaStore, mediaOwner media.MediaOwner,
	owner document.FormJobOwner, reference string,
) *toolshared.ToolResult {
	input, err := tool.formJobs.ReadFormIntakeInput(ctx, reference, owner)
	if err != nil {
		return documentFormToolError(err)
	}
	_, schema, err := tool.loadFormWorkflow(ctx, store, mediaOwner, owner, input.JobID)
	if err != nil {
		return documentFormToolError(err)
	}
	progress, err := tool.formJobs.FindFormIntake(ctx, owner)
	if err != nil || progress == nil || progress.JobID != input.JobID || progress.ModeID != input.ModeID {
		return documentFormToolFailure("form_job_conflict", "the protected intake receipt is unavailable")
	}
	intake := safeDocumentFormIntake{
		Progress: *progress, RequestRevision: input.AdmittedRevision, Intent: input.DialogIntent,
	}
	references, err := tool.formJobs.FormIntakeReferences(ctx, input.JobID, input.ModeID, owner)
	if err != nil {
		return documentFormToolError(err)
	}
	for _, pending := range references {
		result, processErr := tool.processFormIntakeInput(ctx, owner, schema, *progress, pending, reference, references)
		intake.AcceptedCount += result.accepted
		intake.AcknowledgedCount += result.acknowledged
		if pending == reference {
			intake.Intent = result.intent
		}
		intake.Findings = append(intake.Findings, result.findings...)
		if processErr != nil {
			intake.Failure = &safeDocumentFormFailure{
				Code:    "intake_pending",
				Message: "Some protected input remains pending. Accepted fields are preserved; retry or clarify without repeating them.",
			}
			break
		}
	}
	progress, err = tool.formJobs.FindFormIntake(ctx, owner)
	if err != nil || progress == nil {
		return documentFormToolError(document.ErrFormJobConflict)
	}
	intake.Progress = *progress
	if len(intake.Findings) > document.MaxFormIntakeCandidates {
		intake.FindingsTruncated = true
		intake.Findings = intake.Findings[:document.MaxFormIntakeCandidates]
	}
	record, err := tool.formJobs.Get(ctx, input.JobID, owner)
	if err != nil {
		return documentFormToolError(err)
	}
	return tool.formIntakeResult(ctx, owner, schema, record, "continue", intake)
}

type formIntakeProcessedInput struct {
	accepted     int
	acknowledged int
	findings     []document.FormFieldMappingBlocker
	intent       document.FormIntakeDialogIntent
}

func (tool *DocumentTool) processFormIntakeInput(
	ctx context.Context, owner document.FormJobOwner, schema document.FormFieldsFacts,
	progress document.FormIntakeProgress, reference, trigger string, references []string,
) (formIntakeProcessedInput, error) {
	var result formIntakeProcessedInput
	windows := formIntakeCandidateWindows(schema, progress.Context.CandidateFieldIDs)
	for _, ids := range windows {
		input, err := tool.formJobs.ReadFormIntakeInput(ctx, reference, owner)
		if err != nil {
			return result, err
		}
		if input.Consumed {
			result.intent = input.DialogIntent
			break
		}
		clarificationTrigger := ""
		for _, finding := range input.Findings {
			if slices.Contains(ids, finding.FieldID) {
				clarificationTrigger = trigger
				break
			}
		}
		identityBytes, err := json.Marshal([]any{clarificationTrigger, ids, input.Context})
		if err != nil {
			return result, document.ErrFormJobConflict
		}
		digest := sha256.Sum256(identityBytes)
		passKey := "dialog:" + hex.EncodeToString(digest[:])
		old, err := tool.formJobs.FormIntakePassOutcome(ctx, reference, passKey, owner)
		if err != nil {
			return result, err
		}
		if old != nil {
			result.intent = old.Intent
			continue
		}
		evidence, err := tool.privateFormIntakeEvidence(ctx, owner, input.JobID, schema, ids)
		if err != nil {
			return result, err
		}
		for attempt := 0; attempt < 2; attempt++ {
			input, err = tool.formJobs.ReadFormIntakeInput(ctx, reference, owner)
			if err != nil {
				return result, err
			}
			pass, err := tool.formJobs.ReserveFormIntakePass(ctx, document.FormIntakePassRequest{
				Reference: reference, ExpectedRevision: input.Revision, Owner: owner, Schema: schema,
				CandidateIDs: ids, IdempotencyKey: passKey, RetryStale: true,
			})
			if errors.Is(err, document.ErrFormJobConflict) && attempt == 0 {
				continue
			}
			if err != nil {
				return result, err
			}
			if pass.Outcome != nil {
				result.intent = pass.Outcome.Intent
				break
			}
			pass.Input.Context = progress.Context
			view := document.FormIntakeInterpretationView{Pass: pass, Evidence: evidence}
			relatedBytes := 0
			for index := len(references) - 1; index >= 0; index-- {
				other := references[index]
				if other == reference {
					continue
				}
				related, readErr := tool.formJobs.ReadFormIntakeInput(ctx, other, owner)
				if readErr != nil {
					return result, readErr
				}
				if !related.Consumed {
					if relatedBytes+len(related.Text) > document.MaxFormIntakeInputBytes {
						view.RelatedInputsPartial = true
						continue
					}
					relatedBytes += len(related.Text)
					view.RelatedInputs = append(view.RelatedInputs, related)
				}
			}
			record, err := tool.formJobs.Get(ctx, input.JobID, owner)
			if err != nil {
				return result, err
			}
			for _, state := range record.Fields {
				if slices.Contains(ids, state.FieldID) {
					view.FieldStates = append(view.FieldStates, state)
				}
			}
			proposal, err := tool.interpretFormIntake(ctx, view)
			if err != nil {
				return result, err
			}
			applied, err := tool.formJobs.ApplyFormIntakePass(ctx, document.FormIntakeApplyRequest{
				Reference:        reference,
				PassID:           pass.ID,
				ExpectedRevision: pass.Revision,
				Owner:            owner,
				Schema:           schema,
				Proposals:        proposal.Proposals,
				NonData:          proposal.NonData,
				Findings:         proposal.Findings,
				Intent:           proposal.Intent,
			})
			if errors.Is(err, document.ErrFormJobConflict) && attempt == 0 {
				continue
			}
			if err != nil {
				return result, err
			}
			result.accepted += len(applied.AcceptedFieldIDs)
			result.acknowledged += len(applied.AcknowledgedIDs)
			result.intent = proposal.Intent
			break
		}
	}
	input, err := tool.formJobs.ReadFormIntakeInput(ctx, reference, owner)
	if err != nil {
		return result, err
	}
	result.findings = input.Findings
	return result, nil
}

// Windows are structural batches, not a semantic planner. Hash-sorted field
// IDs do not follow page order, so group by page to keep private evidence bounded.
func formIntakeCandidateWindows(schema document.FormFieldsFacts, initial []string) [][]string {
	fields := make([]document.FormField, 0, len(schema.Fields))
	for _, field := range schema.Fields {
		if !field.ReadOnly {
			fields = append(fields, field)
		}
	}
	slices.SortStableFunc(fields, func(a, b document.FormField) int {
		return a.Widgets[0].Page - b.Widgets[0].Page
	})
	var windows [][]string
	appendFields := func(selected bool) {
		var ids []string
		var pages []int
		for _, field := range fields {
			if slices.Contains(initial, field.ID) != selected {
				continue
			}
			page := field.Widgets[0].Page
			if len(ids) == document.MaxFormIntakeCandidates ||
				len(pages) == documentFormEvidencePageLimit && !slices.Contains(pages, page) {
				windows = append(windows, ids)
				ids, pages = nil, nil
			}
			ids = append(ids, field.ID)
			if !slices.Contains(pages, page) {
				pages = append(pages, page)
			}
		}
		if len(ids) > 0 {
			windows = append(windows, ids)
		}
	}
	appendFields(true)
	appendFields(false)
	return windows
}
