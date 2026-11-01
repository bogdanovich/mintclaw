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
	Progress                document.FormIntakeProgress `json:"progress"`
	RequestRevision         int64                       `json:"request_revision,omitempty"`
	Question                string                      `json:"question,omitempty"`
	Summary                 string                      `json:"form_summary,omitempty"`
	Plan                    string                      `json:"collection_plan,omitempty"`
	AcceptedCount           int                         `json:"accepted_count"`
	AcceptedFields          []safeDocumentFormField     `json:"accepted_fields,omitempty"`
	AcceptedFieldsTruncated bool                        `json:"accepted_fields_truncated,omitempty"`
	acceptedIDs             []string
	AcknowledgedCount       int                                `json:"acknowledged_count"`
	ScopedCount             int                                `json:"scoped_count,omitempty"`
	Findings                []document.FormFieldMappingBlocker `json:"findings,omitempty"`
	FindingFields           []safeDocumentFormField            `json:"finding_fields,omitempty"`
	Intent                  document.FormIntakeDialogIntent    `json:"intent,omitempty"`
	FindingsTruncated       bool                               `json:"findings_truncated,omitempty"`
	Failure                 *safeDocumentFormFailure           `json:"failure,omitempty"`
	ReceivedInputState      string                             `json:"received_input_state,omitempty"`
	RetainedFindingCount    int                                `json:"retained_finding_count,omitempty"`
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
	progress, err := tool.formJobs.FormIntakeForJob(ctx, guard.JobID, owner)
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
	progress, err := tool.formJobs.FormIntakeForJob(ctx, record.JobID, owner)
	if err != nil || progress == nil || progress.JobID != record.JobID {
		return documentFormToolError(document.ErrFormJobConflict)
	}
	guard := bus.InboundDocumentIntakeGuard{JobID: progress.JobID, ModeID: progress.ModeID}
	if err = tool.ControlProtectedFormIntake(ctx, guard, action); err != nil {
		return documentFormToolError(err)
	}
	progress, err = tool.formJobs.FormIntakeForJob(ctx, record.JobID, owner)
	if err != nil || progress == nil {
		return documentFormToolError(document.ErrFormJobConflict)
	}
	record, err = tool.formJobs.Get(ctx, record.JobID, owner)
	if err != nil {
		return documentFormToolError(err)
	}
	if action == "resume" {
		work, workErr := tool.formJobs.FormTaskRouteWork(ctx, record.JobID, owner)
		if workErr != nil {
			return documentFormToolError(workErr)
		}
		for _, pending := range work {
			if _, _, parseErr := document.ParseFormTaskRouteReference(pending); parseErr == nil {
				view, readErr := tool.formJobs.ReadFormTaskRoute(ctx, pending, owner)
				if readErr != nil {
					return documentFormToolError(readErr)
				}
				// Resume the draft, not an unrelated unanswered scope question.
				// Committed replies and not-yet-classified messages still run.
				if view.State == document.FormTaskQuestion {
					continue
				}
			}
			return tool.nextFormTaskResult(ctx, owner, pending)
		}
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

func (tool *DocumentTool) BoundProtectedFormIntake(
	ctx context.Context, guard bus.InboundDocumentIntakeGuard,
) (*document.FormIntakeProgress, error) {
	if !guard.Valid() || tool == nil || tool.formJobs == nil {
		return nil, document.ErrFormJobConflict
	}
	owner, err := documentFormOwner(ctx)
	if err != nil {
		return nil, document.ErrFormJobUnauthorized
	}
	progress, err := tool.formJobs.FormIntakeForJob(ctx, guard.JobID, owner)
	if err != nil || progress == nil || progress.ModeID != guard.ModeID {
		return nil, document.ErrFormJobConflict
	}
	return progress, nil
}

// RetryProtectedFormIntake resolves an existing receipt for a typed same-route
// recovery control. It never admits input or resets the extraction budget.
func (tool *DocumentTool) RetryProtectedFormIntake(
	ctx context.Context, guard bus.InboundDocumentIntakeGuard,
) (string, error) {
	if !guard.Valid() || tool == nil || tool.readOnly || tool.formJobs == nil {
		return "", document.ErrFormJobConflict
	}
	owner, err := documentFormOwner(ctx)
	if err != nil {
		return "", document.ErrFormJobUnauthorized
	}
	progress, err := tool.formJobs.FindFormIntake(ctx, owner)
	if err != nil || progress == nil ||
		progress.JobID != guard.JobID || progress.ModeID != guard.ModeID {
		return "", document.ErrFormJobConflict
	}
	if tool.ProtectedFormTaskRoutingConfigured() {
		work, workErr := tool.formJobs.FormTaskRouteWork(ctx, guard.JobID, owner)
		if workErr != nil {
			return "", workErr
		}
		if guard.Reference != "" {
			if slices.Contains(work, guard.Reference) {
				return guard.Reference, nil
			}
		} else if len(work) > 0 {
			return work[0], nil
		}
	}
	if progress.State != document.FormIntakeActive {
		return "", document.ErrFormJobConflict
	}
	references, err := tool.formJobs.FormIntakeReferences(ctx, guard.JobID, guard.ModeID, owner)
	if err != nil || len(references) == 0 {
		return "", document.ErrFormJobConflict
	}
	if guard.Reference != "" {
		if !slices.Contains(references, guard.Reference) {
			return "", document.ErrFormJobConflict
		}
		return guard.Reference, nil
	}
	return references[0], nil
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
	// Browsed windows are presentation, not collection authority. Validate the
	// proposed group against the current native owner/schema/revision instead
	// of retaining another registry of fields seen in earlier windows.
	mapping, err := tool.formJobs.FormMappingSummary(ctx, record.JobID, owner, schema)
	if err != nil {
		return documentFormToolError(err)
	}
	if mapping.Revision != record.Revision {
		return documentFormToolError(document.ErrFormJobConflict)
	}
	for _, id := range ids {
		if !slices.ContainsFunc(mapping.Unresolved, func(field document.FormFieldMappingBlocker) bool {
			return field.FieldID == id
		}) {
			return documentFormToolFailure("invalid_input", "intake selected a resolved or unrelated field")
		}
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
	for _, field := range schema.Fields {
		if !slices.Contains(intake.acceptedIDs, field.ID) {
			continue
		}
		// Aggregate acceptance is not proof of a particular current field.
		// Only name a newly accepted ID that still has a strong native value.
		if !slices.ContainsFunc(record.Fields, func(state document.FormJobFieldState) bool {
			return state.FieldID == field.ID && state.State == document.FormValueConfirmed &&
				state.Source == document.FormValueSourceUser && state.Validation == document.FormValueValidationValid &&
				state.Confidence != document.FormValueConfidenceLow
		}) {
			continue
		}
		if len(intake.AcceptedFields) == document.MaxFormIntakeCandidates {
			intake.AcceptedFieldsTruncated = true
			break
		}
		intake.AcceptedFields = append(intake.AcceptedFields, *documentFormFieldProjection(field, ""))
	}
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
	var lastErr error
	for _, alias := range aliases {
		result, err := tool.formInterpreter.InterpretFormIntake(ctx, alias, view)
		if err == nil {
			return result, nil
		}
		lastErr = documentFormIntakeInterpreterError(err)
		if ctx.Err() != nil {
			break
		}
	}
	return document.FormIntakeInterpretation{}, errors.Join(document.ErrFormAuditUnavailable, lastErr)
}

func documentFormIntakeInterpreterError(err error) error {
	for _, safe := range []error{
		context.Canceled, context.DeadlineExceeded,
		document.ErrFormIntakeInvalidContext, document.ErrFormIntakeInvalidResponse,
		document.ErrFormIntakeModelMissing, document.ErrFormIntakeProviderAuth,
		document.ErrFormIntakeProviderBilling, document.ErrFormIntakeProviderRequest,
		document.ErrFormIntakeProviderLimited, document.ErrFormIntakeProviderTemp,
	} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	return document.ErrFormAuditUnavailable
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
		ReceivedInputState: "retained",
	}
	if input.Consumed {
		intake.ReceivedInputState = "accounted"
	}
	references, err := tool.formJobs.FormIntakeReferences(ctx, input.JobID, input.ModeID, owner)
	if err != nil {
		return documentFormToolError(err)
	}
	for _, pending := range references {
		result, processErr := tool.processFormIntakeInput(ctx, owner, schema, *progress, pending, reference)
		intake.AcceptedCount += result.accepted
		intake.acceptedIDs = append(intake.acceptedIDs, result.acceptedIDs...)
		intake.AcknowledgedCount += result.acknowledged
		intake.ScopedCount += result.scoped
		if pending == reference {
			intake.Intent = result.intent
		}
		if processErr != nil {
			intake.Failure = &safeDocumentFormFailure{
				Code:    "intake_pending",
				Message: "Some protected input remains pending. Accepted fields are preserved; retry or clarify without repeating them.",
				Stage:   result.failureStage,
				Reason:  documentFormIntakeFailureReason(processErr),
			}
			if errors.Is(processErr, document.ErrFormEditableOutputUnavailable) {
				intake.Failure = &safeDocumentFormFailure{
					Code:    "editable_output_unavailable",
					Message: "Verified editable output is unavailable for this job. Protected input and accepted fields are preserved; continue complete filling or cancel. No draft was produced.",
					Stage:   result.failureStage,
					Reason:  "editable_output_unavailable",
				}
			}
			// A retained older error must not suppress a later admitted control or
			// clarification. Pending input still fences review and output authority.
		}
	}
	progress, err = tool.formJobs.FindFormIntake(ctx, owner)
	if err != nil || progress == nil {
		return documentFormToolError(document.ErrFormJobConflict)
	}
	intake.Progress = *progress
	record, err := tool.formJobs.Get(ctx, input.JobID, owner)
	if err != nil {
		return documentFormToolError(err)
	}
	summary, err := tool.formJobs.FormMappingSummary(ctx, input.JobID, owner, schema)
	if err != nil {
		return documentFormToolError(err)
	}
	if summary.Revision != record.Revision {
		return documentFormToolError(document.ErrFormJobConflict)
	}
	// A later input may settle earlier findings in this same atomic ledger.
	// Project current native state, not the earlier intermediate pass results.
	for _, pending := range references {
		current, readErr := tool.formJobs.ReadFormIntakeInput(ctx, pending, owner)
		if readErr != nil {
			return documentFormToolError(readErr)
		}
		if pending == reference {
			intake.ReceivedInputState = "retained"
			if current.Consumed {
				intake.ReceivedInputState = "accounted"
			}
		}
		for _, finding := range current.Findings {
			intake.RetainedFindingCount++
			if slices.Contains(current.SupersededAdvisoryIDs, finding.FieldID) &&
				!documentFormIntakeActionableFinding(summary, finding) {
				continue
			}
			if len(intake.Findings) < document.MaxFormIntakeCandidates {
				intake.Findings = append(intake.Findings, finding)
			} else {
				intake.FindingsTruncated = true
			}
		}
	}
	if progress.PendingInputs == 0 {
		intake.Failure = nil
	}
	return tool.formIntakeResult(ctx, owner, schema, record, "continue", intake)
}

// Presentation only: an old advisory cannot make a confirmed value missing.
// Native input/findings, conflicts, budgets and the pending-output fence remain
// untouched, including when unrelated unaccounted text prevents settlement.
func documentFormIntakeActionableFinding(
	summary document.FormJobMappingSummary,
	finding document.FormFieldMappingBlocker,
) bool {
	if finding.Code != "field_ambiguous" && finding.Code != "field_confirmation_required" {
		return true
	}
	return !slices.Contains(summary.ConfirmedFieldIDs, finding.FieldID)
}

// Never project err.Error(): private provider/native wrappers can contain input.
// These closed categories explain a processing failure, not missing field data
// or permission to retry, discard protected input, change scope or commit.
func documentFormIntakeFailureReason(err error) string {
	switch {
	case errors.Is(err, document.ErrFormJobCapacityExceeded):
		return "capacity_exceeded"
	case errors.Is(err, document.ErrFormJobAnswerConflict):
		return "answer_rejected"
	case errors.Is(err, document.ErrFormJobConflict):
		return "state_changed"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "interrupted"
	case errors.Is(err, document.ErrFormIntakeInvalidContext):
		return "interpretation_context_invalid"
	case errors.Is(err, document.ErrFormIntakeInvalidResponse):
		return "interpretation_response_invalid"
	case errors.Is(err, document.ErrFormIntakeModelMissing):
		return "interpretation_model_unavailable"
	case errors.Is(err, document.ErrFormIntakeProviderAuth):
		return "provider_authentication"
	case errors.Is(err, document.ErrFormIntakeProviderBilling):
		return "provider_billing"
	case errors.Is(err, document.ErrFormIntakeProviderRequest):
		return "provider_request_invalid"
	case errors.Is(err, document.ErrFormIntakeProviderLimited):
		return "provider_rate_limited"
	case errors.Is(err, document.ErrFormIntakeProviderTemp):
		return "provider_temporary"
	case errors.Is(err, document.ErrFormAuditUnavailable):
		return "interpretation_unavailable"
	default:
		return "unavailable"
	}
}

type formIntakeProcessedInput struct {
	acceptedIDs  []string
	accepted     int
	acknowledged int
	scoped       int
	findings     []document.FormFieldMappingBlocker
	intent       document.FormIntakeDialogIntent
	failureStage string
}

func (tool *DocumentTool) processFormIntakeInput(
	ctx context.Context, owner document.FormJobOwner, schema document.FormFieldsFacts,
	progress document.FormIntakeProgress, reference, trigger string,
) (formIntakeProcessedInput, error) {
	var result formIntakeProcessedInput
	result.failureStage = "input"
	retained, retainedErr := tool.formJobs.ReadFormIntakeInput(ctx, reference, owner)
	if retainedErr != nil {
		return result, retainedErr
	}
	windows := formIntakeCandidateWindows(schema, progress.Context.CandidateFieldIDs)
	prioritizeFormIntakeFindingWindows(windows, retained.Findings)
	for _, ids := range windows {
		result.failureStage = "input"
		input, err := tool.formJobs.ReadFormIntakeInput(ctx, reference, owner)
		if err != nil {
			return result, err
		}
		if input.Consumed {
			result.intent = input.DialogIntent
			break
		}
		// Automatic continuation may revisit a known blocker with the new
		// related input, but must not spend the old budget on unchanged empty
		// windows. Explicit retries of this receipt still traverse all windows.
		if reference != trigger && slices.ContainsFunc(input.CompletedWindows, func(completed []string) bool {
			return slices.Equal(completed, ids)
		}) && !slices.ContainsFunc(input.Findings, func(finding document.FormFieldMappingBlocker) bool {
			return slices.Contains(ids, finding.FieldID)
		}) {
			continue
		}
		clarificationTrigger := ""
		for _, finding := range input.Findings {
			if slices.Contains(ids, finding.FieldID) {
				clarificationTrigger = trigger
				break
			}
		}
		// Busy-session retries share the enclosing turn, but retain their own
		// trusted ingress identity. Replaying that input reuses committed windows;
		// a new retry uses the same protected input and original reservation budget.
		processingIdentity := toolshared.ToolDocumentIntakeProcessingIdentity(ctx, trigger)
		if processingIdentity == "" {
			processingIdentity = toolshared.ToolExecutionID(ctx)
		}
		identityBytes, err := json.Marshal(
			[]any{clarificationTrigger, processingIdentity, ids, input.Context},
		)
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
		result.failureStage = "evidence"
		evidence, err := tool.privateFormIntakeEvidence(ctx, owner, input.JobID, schema, ids)
		if err != nil {
			return result, err
		}
		for attempt := 0; attempt < 2; attempt++ {
			result.failureStage = "reservation"
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
			view := document.FormIntakeInterpretationView{Pass: pass, Evidence: evidence}
			// Refresh after reserving the admission snapshot. A subsequent new
			// receipt will make native apply stale instead of hiding related input.
			relatedReferences, err := tool.formJobs.FormIntakeReferences(ctx, input.JobID, input.ModeID, owner)
			if err != nil {
				return result, err
			}
			relatedBytes := 0
			for index := len(relatedReferences) - 1; index >= 0; index-- {
				other := relatedReferences[index]
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
			view.OutputMode, view.OutputScope = record.OutputMode, record.OutputScope
			for _, id := range record.DeferredFieldIDs {
				if slices.Contains(ids, id) {
					view.DeferredFieldIDs = append(view.DeferredFieldIDs, id)
				}
			}
			for _, state := range record.Fields {
				if slices.Contains(ids, state.FieldID) {
					view.FieldStates = append(view.FieldStates, state)
				}
			}
			result.failureStage = "interpretation"
			proposal, err := tool.interpretFormIntake(ctx, view)
			if err != nil {
				return result, err
			}
			result.failureStage = "apply"
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
				Output:           proposal.Output,
				Exclusions:       proposal.Exclusions,
			})
			if errors.Is(err, document.ErrFormJobConflict) && attempt == 0 {
				continue
			}
			if err != nil {
				return result, err
			}
			result.accepted += len(applied.AcceptedFieldIDs)
			result.acceptedIDs = append(result.acceptedIDs, applied.AcceptedFieldIDs...)
			result.acknowledged += len(applied.AcknowledgedIDs)
			result.scoped += len(applied.ScopedFieldIDs)
			result.intent = proposal.Intent
			break
		}
	}
	result.failureStage = "input"
	input, err := tool.formJobs.ReadFormIntakeInput(ctx, reference, owner)
	if err != nil {
		return result, err
	}
	result.findings = input.Findings
	if reference != trigger && !input.Consumed && input.Passes >= document.MaxFormIntakeExtractionPasses {
		result.failureStage = "reservation"
		return result, document.ErrFormJobCapacityExceeded
	}
	result.failureStage = ""
	return result, nil
}

// A clarification must reach known blockers before unrelated traversal spends
// the remaining persisted budget. Reorder whole existing windows only: their
// candidate, page, reservation and apply bindings are unchanged.
func prioritizeFormIntakeFindingWindows(windows [][]string, findings []document.FormFieldMappingBlocker) {
	if len(findings) == 0 {
		return
	}
	slices.SortStableFunc(windows, func(first, second []string) int {
		firstFinding, secondFinding := false, false
		for _, finding := range findings {
			firstFinding = firstFinding || slices.Contains(first, finding.FieldID)
			secondFinding = secondFinding || slices.Contains(second, finding.FieldID)
		}
		if firstFinding == secondFinding {
			return 0
		}
		if firstFinding {
			return -1
		}
		return 1
	})
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
