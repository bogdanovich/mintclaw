package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/media"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

const (
	documentFormWorkflowSchemaVersion = "mintclaw.document_form_workflow.v1"
	documentFormQuestionTimeout       = time.Hour
)

type safeDocumentFormJob struct {
	JobID               string                `json:"job_id"`
	State               document.FormJobState `json:"state"`
	Revision            int64                 `json:"revision"`
	FieldSchemaDigest   string                `json:"field_schema_digest"`
	AuditPolicyRevision string                `json:"audit_policy_revision"`
	ReviewRevision      int64                 `json:"review_revision,omitempty"`
	ReviewDigest        string                `json:"review_digest,omitempty"`
	ExpiresAt           int64                 `json:"expires_at"`
}

type safeDocumentFormField struct {
	FieldID  string                 `json:"field_id"`
	Label    string                 `json:"label"`
	Kind     document.FormFieldKind `json:"kind"`
	Required bool                   `json:"required"`
}

type safeDocumentFormFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type safeDocumentFormResult struct {
	SchemaVersion string                          `json:"schema_version"`
	Operation     string                          `json:"operation"`
	FormAction    string                          `json:"form_action"`
	Job           *safeDocumentFormJob            `json:"job,omitempty"`
	Mapping       *document.FormJobMappingSummary `json:"mapping,omitempty"`
	NextField     *safeDocumentFormField          `json:"next_field,omitempty"`
	Review        *document.FormReview            `json:"review,omitempty"`
	Failure       *safeDocumentFormFailure        `json:"failure,omitempty"`
}

func (tool *DocumentTool) formWorkflow(
	ctx context.Context,
	store ownedDocumentMediaStore,
	mediaOwner media.MediaOwner,
	args map[string]any,
) *toolshared.ToolResult {
	if tool.formJobs == nil {
		return documentFormToolFailure(
			"protected_store_unavailable",
			"protected form workflow storage is unavailable",
		)
	}
	owner, err := documentFormOwner(ctx)
	if err != nil {
		return documentFormToolFailure("form_job_unauthorized", "form workflow authority is unavailable")
	}
	formAction := strings.ToLower(strings.TrimSpace(stringDocumentArg(args, "form_action")))
	switch formAction {
	case "start":
		return tool.startFormWorkflow(ctx, store, mediaOwner, owner, args)
	case "continue":
		return tool.continueFormWorkflow(ctx, store, mediaOwner, owner, args)
	case "status":
		return tool.statusFormWorkflow(ctx, store, mediaOwner, owner, args)
	case "correct":
		return tool.correctFormWorkflow(ctx, store, mediaOwner, owner, args)
	case "cancel":
		return tool.cancelFormWorkflow(ctx, store, owner, args)
	default:
		return documentFormToolFailure("invalid_input", "form workflow action is invalid")
	}
}

func (tool *DocumentTool) startFormWorkflow(
	ctx context.Context,
	store ownedDocumentMediaStore,
	mediaOwner media.MediaOwner,
	owner document.FormJobOwner,
	args map[string]any,
) *toolshared.ToolResult {
	policyRevision, err := tool.formPolicy.Revision()
	if err != nil || tool.formAudit == nil {
		return documentFormToolFailure(
			"audit_unavailable",
			"a configured deliberative document audit model is required",
		)
	}
	ref, _, err := tool.resolveSource(ctx, "form", args)
	if err != nil {
		return documentFormToolFailure("source_not_authorized", "document source is unavailable for this authority")
	}
	schema, err := tool.formWorkflowSchema(ctx, store, ref, mediaOwner)
	if err != nil {
		return documentFormToolError(err)
	}
	startKey, err := documentFormStartKey(ctx, ref)
	if err != nil {
		return documentFormToolFailure("form_job_conflict", "form workflow identity is unavailable")
	}
	preparedSource, err := prepareDocumentFormSource(store, mediaOwner, ref, startKey)
	if err != nil {
		return documentFormToolFailure(
			"protected_store_unavailable",
			"the immutable form source could not be retained",
		)
	}
	schemaDigest, err := document.FormFieldSchemaDigest(schema)
	if err != nil {
		return documentFormToolFailure("form_job_stale", "the form field schema is unavailable")
	}
	backendRevision, err := document.FormFieldsBackendRevision(schema)
	if err != nil {
		return documentFormToolFailure("form_job_stale", "the form backend revision is unavailable")
	}
	record, err := tool.formJobs.Create(ctx, document.FormJobCreateRequest{
		Owner: owner, StartIdempotencyKey: startKey, SourceRef: preparedSource.ref,
		SourceDigest: schema.SourceSHA256, FieldSchemaDigest: schemaDigest,
		BackendRevision: backendRevision, AuditPolicyRevision: policyRevision,
	})
	if err != nil {
		_ = os.Remove(preparedSource.path)
		return documentFormToolError(err)
	}
	if err = preparedSource.register(store, mediaOwner, record); err != nil {
		_, _ = tool.formJobs.Delete(ctx, record.JobID, record.Revision, owner)
		_ = store.ReleaseAll(documentFormSourceScope(record.JobID))
		return documentFormToolFailure(
			"protected_store_unavailable",
			"the immutable form source could not be retained",
		)
	}
	return tool.driveFormWorkflow(ctx, owner, schema, record, "start", "")
}

func (tool *DocumentTool) continueFormWorkflow(
	ctx context.Context,
	store ownedDocumentMediaStore,
	mediaOwner media.MediaOwner,
	owner document.FormJobOwner,
	args map[string]any,
) *toolshared.ToolResult {
	jobID := strings.TrimSpace(stringDocumentArg(args, "job_id"))
	receiptReference := strings.TrimSpace(stringDocumentArg(args, "event_id"))
	eventID := ""
	if receiptReference != "" {
		receiptJobID, receiptEventID, parseErr := document.ParseFormProtectedAnswerReference(receiptReference)
		if parseErr != nil || (jobID != "" && jobID != receiptJobID) {
			return documentFormToolFailure("form_job_conflict", "the protected answer receipt is invalid")
		}
		jobID = receiptJobID
		eventID = receiptEventID
	}
	record, schema, err := tool.loadFormWorkflow(ctx, store, mediaOwner, owner, jobID)
	if err != nil {
		return documentFormToolError(err)
	}
	if eventID != "" {
		mapped, mapErr := tool.formJobs.MapFormEvent(ctx, document.FormEventMappingRequest{
			JobID: jobID, ExpectedRevision: record.Revision, Owner: owner, Schema: schema,
			SourceEventID: eventID, IdempotencyKey: "protected-receipt:" + eventID,
		})
		if mapErr != nil {
			return documentFormToolError(mapErr)
		}
		record = mapped.Job
	}
	return tool.driveFormWorkflow(ctx, owner, schema, record, "continue", "")
}

func (tool *DocumentTool) statusFormWorkflow(
	ctx context.Context,
	store ownedDocumentMediaStore,
	mediaOwner media.MediaOwner,
	owner document.FormJobOwner,
	args map[string]any,
) *toolshared.ToolResult {
	jobID := strings.TrimSpace(stringDocumentArg(args, "job_id"))
	record, schema, err := tool.loadFormWorkflow(ctx, store, mediaOwner, owner, jobID)
	if err != nil {
		return documentFormToolError(err)
	}
	projection := safeDocumentFormResult{
		SchemaVersion: documentFormWorkflowSchemaVersion,
		Operation:     "form",
		FormAction:    "status",
		Job:           safeDocumentFormJobProjection(record),
	}
	if record.AuditRevision != 0 {
		review, reviewErr := tool.formJobs.CurrentFormReview(ctx, jobID, owner, schema)
		if reviewErr != nil {
			return documentFormToolError(reviewErr)
		}
		projection.Review = &review
	} else {
		summary, summaryErr := tool.formJobs.FormMappingSummary(ctx, jobID, owner, schema)
		if summaryErr != nil {
			return documentFormToolError(summaryErr)
		}
		projection.Mapping = &summary
	}
	return documentFormToolResult(projection)
}

func (tool *DocumentTool) correctFormWorkflow(
	ctx context.Context,
	store ownedDocumentMediaStore,
	mediaOwner media.MediaOwner,
	owner document.FormJobOwner,
	args map[string]any,
) *toolshared.ToolResult {
	jobID := strings.TrimSpace(stringDocumentArg(args, "job_id"))
	fieldID := strings.TrimSpace(stringDocumentArg(args, "field_id"))
	record, schema, err := tool.loadFormWorkflow(ctx, store, mediaOwner, owner, jobID)
	if err != nil {
		return documentFormToolError(err)
	}
	fieldIndex := slices.IndexFunc(schema.Fields, func(field document.FormField) bool {
		return field.ID == fieldID && !field.ReadOnly
	})
	if fieldIndex < 0 {
		return documentFormToolFailure("field_unresolved", "the requested form field is unavailable")
	}
	return tool.formQuestionResult(ctx, owner, schema, record, fieldID, "correct")
}

func (tool *DocumentTool) cancelFormWorkflow(
	ctx context.Context,
	store ownedDocumentMediaStore,
	owner document.FormJobOwner,
	args map[string]any,
) *toolshared.ToolResult {
	jobID := strings.TrimSpace(stringDocumentArg(args, "job_id"))
	record, err := tool.formJobs.Get(ctx, jobID, owner)
	if err != nil {
		return documentFormToolError(err)
	}
	record, err = tool.formJobs.Cancel(ctx, jobID, record.Revision, owner)
	if err != nil {
		return documentFormToolError(err)
	}
	if err = store.ReleaseAll(documentFormSourceScope(record.JobID)); err != nil {
		return documentFormToolFailure(
			"protected_store_unavailable",
			"the canceled form source could not be removed",
		)
	}
	return documentFormToolResult(safeDocumentFormResult{
		SchemaVersion: documentFormWorkflowSchemaVersion,
		Operation:     "form",
		FormAction:    "cancel",
		Job:           safeDocumentFormJobProjection(record),
	})
}

func (tool *DocumentTool) driveFormWorkflow(
	ctx context.Context,
	owner document.FormJobOwner,
	schema document.FormFieldsFacts,
	record document.FormJobRecord,
	formAction string,
	_ string,
) *toolshared.ToolResult {
	if record.State == document.FormJobReviewReady {
		review, err := tool.formJobs.CurrentFormReview(ctx, record.JobID, owner, schema)
		if err != nil {
			return documentFormToolError(err)
		}
		return documentFormToolResult(safeDocumentFormResult{
			SchemaVersion: documentFormWorkflowSchemaVersion, Operation: "form", FormAction: formAction,
			Job: safeDocumentFormJobProjection(record), Review: &review,
		})
	}
	summary, err := tool.formJobs.FormMappingSummary(ctx, record.JobID, owner, schema)
	if err != nil {
		return documentFormToolError(err)
	}
	if !summary.ReadyForReview {
		return tool.formQuestionResult(ctx, owner, schema, record, summary.NextUnresolvedID, formAction)
	}
	result, err := tool.formJobs.ReviewFormJob(ctx, document.FormReviewRequest{
		JobID: record.JobID, ExpectedRevision: record.Revision, Owner: owner,
		Schema: schema, Policy: tool.formPolicy, Auditor: tool.formAudit,
	})
	if err != nil {
		return documentFormToolError(err)
	}
	if !result.Review.Ready && len(result.Review.Blockers) != 0 {
		return tool.formQuestionResult(
			ctx,
			owner,
			schema,
			result.Job,
			result.Review.Blockers[0].FieldID,
			formAction,
		)
	}
	return documentFormToolResult(safeDocumentFormResult{
		SchemaVersion: documentFormWorkflowSchemaVersion, Operation: "form", FormAction: formAction,
		Job: safeDocumentFormJobProjection(result.Job), Review: &result.Review,
	})
}

func (tool *DocumentTool) formQuestionResult(
	ctx context.Context,
	owner document.FormJobOwner,
	schema document.FormFieldsFacts,
	record document.FormJobRecord,
	fieldID string,
	formAction string,
) *toolshared.ToolResult {
	fieldIndex := slices.IndexFunc(schema.Fields, func(field document.FormField) bool {
		return field.ID == fieldID && !field.ReadOnly
	})
	if fieldIndex < 0 {
		return documentFormToolFailure("field_unresolved", "the next form field is unavailable")
	}
	field := schema.Fields[fieldIndex]
	supersedes := ""
	if current := slices.IndexFunc(record.Fields, func(state document.FormJobFieldState) bool {
		return state.FieldID == fieldID
	}); current >= 0 {
		supersedes = record.Fields[current].EventID
	}
	binding, err := tool.formJobs.NewProtectedAnswerBinding(ctx, document.FormProtectedAnswerBindingRequest{
		JobID: record.JobID, ExpectedRevision: record.Revision, Owner: owner,
		FieldID: fieldID, SupersedesEventID: supersedes,
	})
	if err != nil {
		return documentFormToolError(err)
	}
	label := documentFormFieldLabel(field)
	question := interactions.Question{
		ID: "document_form_value", Header: "PDF form",
		Question: documentFormQuestionText(label, field), Options: documentFormQuestionOptions(field),
		MultiSelect: field.MultiSelect,
	}
	suspension := interactions.SuspensionRequest{
		Kind: interactions.KindQuestion, Questions: []interactions.Question{question},
		PromptSummary: "Provide or correct PDF form field: " + truncateDocumentFormText(label, 256),
		Timeout:       documentFormQuestionTimeout, ProtectedAnswer: &binding,
	}
	if validationErr := interactions.ValidateSuspensionRequest(suspension); validationErr != nil {
		return documentFormToolFailure("field_unresolved", "the form question could not be prepared")
	}
	summary, err := tool.formJobs.FormMappingSummary(ctx, record.JobID, owner, schema)
	if err != nil {
		return documentFormToolError(err)
	}
	projection := safeDocumentFormResult{
		SchemaVersion: documentFormWorkflowSchemaVersion, Operation: "form", FormAction: formAction,
		Job: safeDocumentFormJobProjection(record), Mapping: &summary,
		NextField: &safeDocumentFormField{
			FieldID: field.ID, Label: label, Kind: field.Kind, Required: field.Required,
		},
	}
	result := documentFormToolResult(projection)
	result.Control.Suspension = &suspension
	result.Delivery.Intent = toolshared.DeliverySilent
	return result
}

func (tool *DocumentTool) loadFormWorkflow(
	ctx context.Context,
	store ownedDocumentMediaStore,
	mediaOwner media.MediaOwner,
	owner document.FormJobOwner,
	jobID string,
) (document.FormJobRecord, document.FormFieldsFacts, error) {
	record, err := tool.formJobs.Get(ctx, jobID, owner)
	if err != nil {
		return document.FormJobRecord{}, document.FormFieldsFacts{}, err
	}
	ref, err := tool.formJobs.SourceRef(ctx, jobID, owner)
	if err != nil {
		return document.FormJobRecord{}, document.FormFieldsFacts{}, err
	}
	schema, err := tool.formWorkflowSchema(ctx, store, ref, mediaOwner)
	if err != nil {
		return document.FormJobRecord{}, document.FormFieldsFacts{}, err
	}
	return record, schema, nil
}

func (tool *DocumentTool) formWorkflowSchema(
	ctx context.Context,
	store ownedDocumentMediaStore,
	ref string,
	owner media.MediaOwner,
) (document.FormFieldsFacts, error) {
	if tool.formSchema != nil {
		return tool.formSchema(ctx, store, ref, owner)
	}
	snapshot, report := document.FieldsMedia(
		ctx,
		store,
		ref,
		owner,
		document.AcquireOptions{ScratchRoot: tool.scratchRoot},
	)
	if snapshot != nil {
		defer func() { _ = snapshot.Close() }()
	}
	if report.State != document.StateSucceeded || report.Fields == nil {
		if report.Failure != nil {
			return document.FormFieldsFacts{}, errors.New(string(report.Failure.Code))
		}
		return document.FormFieldsFacts{}, errors.New("form_unsupported")
	}
	return *report.Fields, nil
}

type preparedDocumentFormSource struct {
	path string
	ref  string
	key  string
}

func prepareDocumentFormSource(
	store ownedDocumentMediaStore,
	owner media.MediaOwner,
	ref string,
	startKey string,
) (preparedDocumentFormSource, error) {
	if _, ok := store.(idempotentOwnedDocumentMediaStore); !ok {
		return preparedDocumentFormSource{}, errors.New("durable owned media registration is unavailable")
	}
	source, err := store.OpenOwned(ref, owner)
	if err != nil {
		return preparedDocumentFormSource{}, err
	}
	defer func() { _ = source.Close() }()
	if (source.Meta.ContentType != "application/pdf" && source.Meta.ContentType != "application/octet-stream") ||
		source.Identity.Size <= 0 ||
		source.Identity.Size > document.DefaultMaxInputBytes || len(source.Identity.SHA256) != sha256.Size*2 {
		return preparedDocumentFormSource{}, errors.New("document form source descriptor is invalid")
	}
	if mkdirErr := os.MkdirAll(media.TempDir(), 0o700); mkdirErr != nil {
		return preparedDocumentFormSource{}, mkdirErr
	}
	output, err := os.CreateTemp(media.TempDir(), ".document-form-source-*.pdf")
	if err != nil {
		return preparedDocumentFormSource{}, err
	}
	path := output.Name()
	remove := true
	defer func() {
		_ = output.Close()
		if remove {
			_ = os.Remove(path)
		}
	}()
	if err = output.Chmod(0o600); err != nil {
		return preparedDocumentFormSource{}, err
	}
	hash := sha256.New()
	written, err := io.Copy(
		io.MultiWriter(output, hash),
		io.LimitReader(source.File, source.Identity.Size+1),
	)
	if err != nil || written != source.Identity.Size || hex.EncodeToString(hash.Sum(nil)) != source.Identity.SHA256 {
		return preparedDocumentFormSource{}, errors.New("document form source bytes changed during retention")
	}
	var trailing [1]byte
	if count, readErr := source.File.Read(trailing[:]); count != 0 ||
		(readErr != nil && !errors.Is(readErr, io.EOF)) {
		return preparedDocumentFormSource{}, errors.New("document form source exceeds its immutable descriptor")
	}
	if err = output.Sync(); err != nil {
		return preparedDocumentFormSource{}, err
	}
	if err = output.Close(); err != nil {
		return preparedDocumentFormSource{}, err
	}
	digest := sha256.Sum256([]byte("mintclaw.document-form-source.v1\x00" + startKey))
	identity := hex.EncodeToString(digest[:16])
	key := "document-form-source-" + identity
	retained, err := media.IdempotentRef(key)
	if err != nil {
		return preparedDocumentFormSource{}, err
	}
	remove = false
	return preparedDocumentFormSource{path: path, ref: retained, key: key}, nil
}

func (prepared preparedDocumentFormSource) register(
	store ownedDocumentMediaStore,
	owner media.MediaOwner,
	record document.FormJobRecord,
) error {
	defer func() { _ = os.Remove(prepared.path) }()
	idempotent, ok := store.(idempotentOwnedDocumentMediaStore)
	if !ok || prepared.path == "" || prepared.ref == "" || prepared.key == "" || record.ExpiresAt <= 0 {
		return errors.New("durable owned media registration is unavailable")
	}
	retained, err := idempotent.StoreIdempotentOwned(prepared.path, media.MediaMeta{
		Filename: "form-source.pdf", ContentType: "application/pdf", Source: "tool:document-form",
		CleanupPolicy: media.CleanupPolicyDeleteOnCleanup,
		RetainUntil:   time.UnixMilli(record.ExpiresAt).UTC(),
	}, documentFormSourceScope(record.JobID), prepared.key, owner)
	if err != nil || retained != prepared.ref {
		return errors.Join(err, errors.New("document form source identity changed during retention"))
	}
	registeredPath, resolveErr := store.Resolve(retained)
	if resolveErr == nil && registeredPath == prepared.path {
		// The non-persistent store owns this exact temporary path. Do not remove
		// it here; ReleaseAll or age cleanup owns its lifecycle.
		prepared.path = ""
	}
	return nil
}

func documentFormSourceScope(jobID string) string {
	return "document-form-source-" + strings.TrimSpace(jobID)
}

func documentFormStartKey(ctx context.Context, ref string) (string, error) {
	executionID := strings.TrimSpace(toolshared.ToolExecutionID(ctx))
	callID := strings.TrimSpace(toolshared.ToolCallID(ctx))
	if executionID == "" || callID == "" || strings.TrimSpace(ref) == "" {
		return "", errors.New("document form start identity is unavailable")
	}
	digest := sha256.Sum256([]byte(strings.Join([]string{
		"mintclaw.document-form-start.v1", executionID, callID, ref,
	}, "\x00")))
	return "form-start-" + hex.EncodeToString(digest[:]), nil
}

func documentFormOwner(ctx context.Context) (document.FormJobOwner, error) {
	inbound := toolshared.ToolInboundContext(ctx)
	routeSession := strings.TrimSpace(toolshared.ToolRouteSessionKey(ctx))
	if routeSession == "" {
		routeSession = strings.TrimSpace(toolshared.ToolSessionKey(ctx))
	}
	senderID := strings.TrimSpace(inbound.SenderID)
	if senderID == "" {
		senderID = strings.TrimSpace(inbound.ActorID)
	}
	owner := document.FormJobOwner{
		AgentID: toolshared.ToolAgentID(ctx), WorkspaceID: toolshared.ToolWorkspace(ctx),
		RouteSessionKey: routeSession, Channel: inbound.Channel, AccountID: inbound.Account,
		ChatID: inbound.ChatID, ChatType: inbound.ChatType, SenderID: senderID,
		TopicID: inbound.TopicID, SpaceID: inbound.SpaceID, SpaceType: inbound.SpaceType,
	}
	if strings.TrimSpace(owner.AgentID) == "" || strings.TrimSpace(owner.WorkspaceID) == "" ||
		strings.TrimSpace(owner.RouteSessionKey) == "" || strings.TrimSpace(owner.Channel) == "" ||
		strings.TrimSpace(owner.ChatID) == "" || strings.TrimSpace(owner.SenderID) == "" {
		return document.FormJobOwner{}, errors.New("document form owner is incomplete")
	}
	return owner, nil
}

func documentFormQuestionText(label string, field document.FormField) string {
	question := fmt.Sprintf("Provide %s for the PDF form. You may reply with free text.", label)
	if field.DateFormat != "" {
		question = fmt.Sprintf("Provide %s for the PDF form using %s. You may reply with free text.",
			label, field.DateFormat)
	}
	if !field.Required {
		question += " You may also skip it or mark it not applicable."
	}
	return truncateDocumentFormText(question, interactions.MaxQuestionLength)
}

func documentFormQuestionOptions(field document.FormField) []interactions.Option {
	switch field.Kind {
	case document.FormFieldCheckbox:
		return []interactions.Option{
			{Label: "Yes", Description: "Set this checkbox."},
			{Label: "No", Description: "Leave this checkbox unset."},
		}
	case document.FormFieldRadio, document.FormFieldCombo, document.FormFieldList:
		if len(field.Options) < 2 || len(field.Options) > interactions.MaxOptions {
			return nil
		}
		options := make([]interactions.Option, 0, len(field.Options))
		for _, option := range field.Options {
			label := strings.TrimSpace(option.Display)
			if label == "" {
				label = strings.TrimSpace(option.Export)
			}
			if label == "" || len(label) > interactions.MaxOptionLabelLength || !utf8.ValidString(label) {
				return nil
			}
			options = append(options, interactions.Option{
				Label: label, Description: "Use this form choice.",
			})
		}
		return options
	default:
		if field.Required {
			return nil
		}
		return []interactions.Option{
			{Label: interactions.ProtectedAnswerSkipLabel, Description: "Leave this optional field blank."},
			{
				Label:       interactions.ProtectedAnswerNotApplicableLabel,
				Description: "Mark this optional field as not applicable.",
			},
		}
	}
}

func documentFormFieldLabel(field document.FormField) string {
	label := strings.TrimSpace(field.AlternateName)
	if label == "" {
		label = strings.TrimSpace(field.Name)
	}
	if label == "" {
		label = field.ID
	}
	return truncateDocumentFormText(label, 256)
}

func truncateDocumentFormText(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if len(value) <= maximum {
		return value
	}
	value = value[:maximum]
	for value != "" && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return strings.TrimSpace(value)
}

func safeDocumentFormJobProjection(record document.FormJobRecord) *safeDocumentFormJob {
	return &safeDocumentFormJob{
		JobID: record.JobID, State: record.State, Revision: record.Revision,
		FieldSchemaDigest: record.FieldSchemaDigest, AuditPolicyRevision: record.AuditPolicyRevision,
		ReviewRevision: record.ReviewRevision, ReviewDigest: record.ReviewDigest, ExpiresAt: record.ExpiresAt,
	}
}

func documentFormToolResult(projection safeDocumentFormResult) *toolshared.ToolResult {
	encoded, err := json.Marshal(projection)
	if err != nil {
		return documentFormToolFailure("internal_failure", "form workflow result could not be encoded")
	}
	return &toolshared.ToolResult{ForLLM: string(encoded)}
}

func documentFormToolError(err error) *toolshared.ToolResult {
	code, message := "internal_failure", "form workflow could not continue"
	switch {
	case errors.Is(err, document.ErrFormAuditUnavailable):
		code, message = "audit_unavailable", "the configured document audit role is unavailable"
	case errors.Is(err, document.ErrFormAuditPolicyChanged):
		code, message = "audit_policy_changed", "the document audit policy changed; start a new form job"
	case errors.Is(err, document.ErrFormReviewStale), errors.Is(err, document.ErrFormJobStale):
		code, message = "review_stale", "the form source, schema, or review changed"
	case errors.Is(err, document.ErrFormJobNotFound):
		code, message = "form_job_not_found", "the form job was not found"
	case errors.Is(err, document.ErrFormJobUnauthorized):
		code, message = "form_job_unauthorized", "the form job belongs to another authority"
	case errors.Is(err, document.ErrFormJobConflict), errors.Is(err, document.ErrFormJobAnswerConflict):
		code, message = "form_job_conflict", "the form job changed; inspect its current status"
	case errors.Is(err, document.ErrFormJobExpired):
		code, message = "form_job_expired", "the form job expired"
	case errors.Is(err, document.ErrFormJobTerminal):
		code, message = "form_job_terminal", "the form job is no longer active"
	case errors.Is(err, document.ErrFormJobStoreUnavailable), errors.Is(err, document.ErrFormJobKeyUnavailable):
		code, message = "protected_store_unavailable", "protected form workflow storage is unavailable"
	case err != nil && validDocumentFormFailureCode(err.Error()):
		code, message = err.Error(), "the document form backend refused this workflow step"
	}
	return documentFormToolFailure(code, message)
}

func validDocumentFormFailureCode(code string) bool {
	if code == "" || len(code) > 64 {
		return false
	}
	for index, char := range code {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && (char != '_' || index == 0) {
			return false
		}
	}
	return true
}

func documentFormToolFailure(code, message string) *toolshared.ToolResult {
	encoded, _ := json.Marshal(safeDocumentFormResult{
		SchemaVersion: documentFormWorkflowSchemaVersion,
		Operation:     "form",
		Failure:       &safeDocumentFormFailure{Code: code, Message: message},
	})
	return &toolshared.ToolResult{ForLLM: string(encoded), IsError: true}
}
