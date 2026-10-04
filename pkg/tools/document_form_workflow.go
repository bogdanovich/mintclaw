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
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

const (
	documentFormWorkflowSchemaVersion = "mintclaw.document_form_workflow.v1"
	documentFormQuestionTimeout       = time.Hour
	documentFormSummaryMaxRunes       = 512
	documentFormPlanMaxRunes          = 768
	documentFormCandidateLimit        = 8
	documentFormPlanningPageLimit     = 3
	documentFormEvidencePageLimit     = 2
	documentFormEvidenceTextLimit     = 4000
	documentFormEvidenceRenderEdge    = 1024
	documentFormEvidenceRenderDPI     = 72
)

type documentFormQuestionPresentation struct {
	question       string
	summary        string
	plan           string
	language       string
	blankActions   []interactions.ProtectedAnswerAction
	checkedLabel   string
	uncheckedLabel string
}

type safeDocumentFormJob struct {
	JobID                string                `json:"job_id"`
	State                document.FormJobState `json:"state"`
	Revision             int64                 `json:"revision"`
	FieldSchemaDigest    string                `json:"field_schema_digest"`
	AuditPolicyRevision  string                `json:"audit_policy_revision"`
	ReviewRevision       int64                 `json:"review_revision,omitempty"`
	ReviewDigest         string                `json:"review_digest,omitempty"`
	ApprovalRevision     int64                 `json:"approval_revision,omitempty"`
	OutputPolicyRevision string                `json:"output_policy_revision,omitempty"`
	OperationID          string                `json:"operation_id,omitempty"`
	ArtifactRef          string                `json:"artifact_ref,omitempty"`
	ArtifactDigest       string                `json:"artifact_digest,omitempty"`
	ExpiresAt            int64                 `json:"expires_at"`
	NeedsInitialPlan     bool                  `json:"needs_initial_plan"`
}

type safeDocumentFormField struct {
	FieldID        string                 `json:"field_id"`
	Label          string                 `json:"label"`
	Kind           document.FormFieldKind `json:"kind"`
	Required       bool                   `json:"required"`
	Page           int                    `json:"page,omitempty"`
	DateFormat     string                 `json:"date_format,omitempty"`
	MultiSelect    bool                   `json:"multi_select,omitempty"`
	Options        []string               `json:"options,omitempty"`
	Blocker        string                 `json:"blocker,omitempty"`
	Status         string                 `json:"status,omitempty"`
	LabelTruncated bool                   `json:"label_truncated,omitempty"`
}

type safeDocumentFormMapping struct {
	Revision              int64                   `json:"revision,omitempty"`
	ConfirmedFieldCount   int                     `json:"confirmed_field_count"`
	UnresolvedFieldCount  int                     `json:"unresolved_field_count"`
	ReadyForReview        bool                    `json:"ready_for_review"`
	WritableFieldCount    int                     `json:"writable_field_count"`
	CandidateFields       []safeDocumentFormField `json:"candidate_fields,omitempty"`
	PreservedFieldCount   int                     `json:"preserved_field_count"`
	ProvidedFieldCount    int                     `json:"provided_field_count"`
	OptionalBlankCount    int                     `json:"optional_blank_count"`
	MissingFieldCount     int                     `json:"missing_field_count"`
	ConflictingFieldCount int                     `json:"conflicting_field_count"`
	Window                safeDocumentFormWindow  `json:"field_window"`
}

type documentFormWindowSelection struct {
	pages    []int
	offset   int
	explicit bool
}

type safeDocumentFormWindow struct {
	Selected   bool  `json:"selected"`
	Pages      []int `json:"pages,omitempty"`
	Offset     int   `json:"offset"`
	Limit      int   `json:"limit"`
	Total      int   `json:"total"`
	Truncated  bool  `json:"truncated"`
	NextOffset *int  `json:"next_offset,omitempty"`
}

type safeDocumentFormReview struct {
	SchemaVersion        string                          `json:"schema_version"`
	JobID                string                          `json:"job_id"`
	State                document.FormJobState           `json:"state"`
	Revision             int64                           `json:"revision"`
	ReviewRevision       int64                           `json:"review_revision,omitempty"`
	FieldSchemaDigest    string                          `json:"field_schema_digest"`
	AuditPolicyRevision  string                          `json:"audit_policy_revision"`
	AuditModel           string                          `json:"audit_model,omitempty"`
	AssignmentDigest     string                          `json:"assignment_digest,omitempty"`
	ReviewDigest         string                          `json:"review_digest,omitempty"`
	RequestedAction      string                          `json:"requested_action"`
	WritableFieldCount   int                             `json:"writable_field_count"`
	ProvidedFieldCount   int                             `json:"provided_field_count"`
	BlankFieldCount      int                             `json:"blank_field_count"`
	ExistingFieldCount   int                             `json:"existing_field_count"`
	UnresolvedFieldCount int                             `json:"unresolved_field_count"`
	BlockerCount         int                             `json:"blocker_count"`
	Fields               []document.FormReviewField      `json:"fields,omitempty"`
	FieldsTruncated      bool                            `json:"fields_truncated,omitempty"`
	Blockers             []document.FormJobReviewBlocker `json:"blockers,omitempty"`
	BlockersTruncated    bool                            `json:"blockers_truncated,omitempty"`
	Ready                bool                            `json:"ready"`
}

type safeDocumentFormFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type safeDocumentFormResult struct {
	SchemaVersion     string                   `json:"schema_version"`
	Operation         string                   `json:"operation"`
	FormAction        string                   `json:"form_action"`
	SourceRef         string                   `json:"source_ref,omitempty"`
	FieldSchemaDigest string                   `json:"field_schema_digest,omitempty"`
	Job               *safeDocumentFormJob     `json:"job,omitempty"`
	Mapping           *safeDocumentFormMapping `json:"mapping,omitempty"`
	NextField         *safeDocumentFormField   `json:"next_field,omitempty"`
	Review            *safeDocumentFormReview  `json:"review,omitempty"`
	Commit            *safeDocumentFormCommit  `json:"commit,omitempty"`
	Failure           *safeDocumentFormFailure `json:"failure,omitempty"`
	Evidence          *safeDocumentReport      `json:"evidence,omitempty"`
}

type safeDocumentFormCommit struct {
	OperationID          string `json:"operation_id"`
	ArtifactRef          string `json:"artifact_ref,omitempty"`
	ArtifactDigest       string `json:"artifact_digest,omitempty"`
	SourceUnchanged      bool   `json:"source_unchanged"`
	StructuralAssertions int    `json:"structural_assertions,omitempty"`
	VisualAssertions     int    `json:"visual_assertions,omitempty"`
	CheckedFields        int    `json:"checked_fields,omitempty"`
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
	case "discover":
		return tool.discoverFormWorkflow(ctx, store, mediaOwner, args)
	case "start":
		return tool.startFormWorkflow(ctx, store, mediaOwner, owner, args)
	case "collect":
		return tool.collectFormWorkflow(ctx, store, mediaOwner, owner, args, "collect")
	case "continue":
		return tool.continueFormWorkflow(ctx, store, mediaOwner, owner, args)
	case "clarify", "back":
		return tool.navigateFormWorkflow(ctx, store, mediaOwner, owner, args, formAction)
	case "status":
		return tool.statusFormWorkflow(ctx, store, mediaOwner, owner, args)
	case "clarify_intent":
		return tool.clarifyFormIntent(ctx, store, mediaOwner, owner, args)
	case "evidence":
		return tool.formEvidence(ctx, store, mediaOwner, owner, args)
	case "correct":
		return tool.collectFormWorkflow(ctx, store, mediaOwner, owner, args, "correct")
	case "review":
		return tool.reviewFormWorkflow(ctx, store, mediaOwner, owner, args)
	case "commit":
		return tool.commitFormWorkflow(ctx, store, mediaOwner, owner, args)
	case "cancel":
		return tool.cancelFormWorkflow(ctx, store, owner, args)
	default:
		return documentFormToolFailure("invalid_input", "form workflow action is invalid")
	}
}

func (tool *DocumentTool) discoverFormWorkflow(
	ctx context.Context,
	store ownedDocumentMediaStore,
	mediaOwner media.MediaOwner,
	args map[string]any,
) *toolshared.ToolResult {
	ref, _, err := tool.resolveSource(ctx, "form", args)
	if err != nil {
		return documentFormToolFailure("source_not_authorized", "document source is unavailable for this authority")
	}
	schema, err := tool.formWorkflowSchema(ctx, store, ref, mediaOwner)
	if err != nil {
		return documentFormToolError(err)
	}
	discoveryDigest, err := documentFormDiscoveryDigest(schema.SourceSHA256, schema)
	if err != nil {
		return documentFormToolFailure("form_job_stale", "the form field schema is unavailable")
	}
	summary := documentFormDiscoverySummary(schema)
	return preserveDocumentToolVisibility(documentFormToolResult(safeDocumentFormResult{
		SchemaVersion: documentFormWorkflowSchemaVersion, Operation: "form", FormAction: "discover",
		SourceRef: ref, FieldSchemaDigest: discoveryDigest,
		Mapping: documentFormMappingWindow(summary, schema, "", documentFormWindowArgs(args)),
	}))
}

// ApprovalArguments binds the form commit to trusted current job state. All
// other document actions preserve their ordinary canonical argument binding.
func (tool *DocumentTool) ApprovalArguments(
	ctx context.Context,
	args map[string]any,
) (map[string]any, error) {
	if strings.ToLower(strings.TrimSpace(stringDocumentArg(args, "action"))) != "form" ||
		strings.ToLower(strings.TrimSpace(stringDocumentArg(args, "form_action"))) != "commit" {
		cloned := make(map[string]any, len(args))
		for key, value := range args {
			cloned[key] = value
		}
		return cloned, nil
	}
	if err := validateDocumentActionOptions("form", args); err != nil {
		return nil, err
	}
	store, mediaOwner, err := tool.executionAuthority(ctx)
	if err != nil {
		return nil, newDocumentFormApprovalError(
			documentFormToolFailure("form_job_unauthorized", "document authority is unavailable"),
		)
	}
	owner, err := documentFormOwner(ctx)
	if err != nil {
		return nil, newDocumentFormApprovalError(
			documentFormToolFailure("form_job_unauthorized", "form workflow authority is unavailable"),
		)
	}
	_, _, _, binding, err := tool.prepareFormCommit(
		ctx,
		store,
		mediaOwner,
		owner,
		strings.TrimSpace(stringDocumentArg(args, "job_id")),
	)
	if err != nil {
		return nil, newDocumentFormApprovalError(documentFormToolError(err))
	}
	return documentFormApprovalArguments(binding), nil
}

func (tool *DocumentTool) commitFormWorkflow(
	ctx context.Context,
	store ownedDocumentMediaStore,
	mediaOwner media.MediaOwner,
	owner document.FormJobOwner,
	args map[string]any,
) *toolshared.ToolResult {
	jobID := strings.TrimSpace(stringDocumentArg(args, "job_id"))
	recorded, err := tool.formJobs.Get(ctx, jobID, owner)
	if err != nil {
		return documentFormToolError(err)
	}
	switch recorded.State {
	case document.FormJobDelivering:
		return tool.resumeFormDelivery(ctx, mediaOwner, recorded)
	case document.FormJobCompleted:
		return documentFormCommitResult(recorded, nil)
	case document.FormJobFailed, document.FormJobUncertain:
		return documentFormTerminalFailure(recorded)
	}
	record, schema, request, binding, err := tool.prepareFormCommit(
		ctx,
		store,
		mediaOwner,
		owner,
		jobID,
	)
	if err != nil {
		return documentFormToolError(err)
	}
	if record.State == document.FormJobAwaitingApproval {
		approved := toolshared.ToolApprovalBypass(ctx)
		if toolshared.ToolApprovalContinuation(ctx) {
			arguments, found := toolshared.ToolApprovalArguments(ctx)
			if !found || !equalDocumentFormApprovalArguments(
				arguments,
				documentFormApprovalArguments(binding),
			) {
				return documentFormToolFailure(
					"approval_stale",
					"the reviewed form approval is no longer current",
				)
			}
			approved = true
		}
		if !approved {
			return tool.formCommitApprovalResult(ctx, owner, schema, record)
		}
		request.ExpectedRevision = record.Revision
		record, err = tool.formJobs.BeginFormCommit(ctx, request, binding)
		if err != nil {
			return documentFormToolError(err)
		}
	}
	if record.State != document.FormJobCommitting {
		return documentFormToolFailure("approval_stale", "the reviewed form approval is no longer current")
	}
	request.ExpectedRevision = record.Revision
	materialized, fill, err := tool.formJobs.MaterializeFormCommit(ctx, request)
	if err != nil {
		return documentFormToolError(err)
	}
	defer clearDocumentFormFill(&fill)
	sourceRef, err := tool.formJobs.SourceRef(ctx, record.JobID, owner)
	if err != nil {
		return documentFormToolError(err)
	}
	idempotentStore, ok := store.(idempotentOwnedDocumentMediaStore)
	if !ok {
		return tool.failFormCommit(
			ctx,
			materialized,
			owner,
			true,
			"artifact_registration_failed",
			"verified document registration is unavailable",
		)
	}
	snapshot, report := document.FillMedia(ctx, store, sourceRef, mediaOwner, fill, document.FormWriteOptions{
		Acquire:   document.AcquireOptions{ScratchRoot: tool.scratchRoot},
		StateRoot: tool.stateRoot, OperationID: materialized.OperationID,
	})
	if snapshot != nil {
		defer func() { _ = snapshot.Close() }()
	}
	if report.State != document.StateSucceeded || snapshot == nil || report.Input == nil || report.Write == nil ||
		report.OperationID != materialized.OperationID || report.Input.SHA256 != materialized.SourceDigest {
		failureCode := "document_commit_failed"
		if report.Failure != nil && validDocumentFormFailureCode(string(report.Failure.Code)) {
			failureCode = string(report.Failure.Code)
		}
		return tool.failFormCommit(
			ctx,
			materialized,
			owner,
			report.State == document.StateUncertain,
			failureCode,
			"the reviewed PDF could not be filled and verified",
		)
	}
	registeredRef, writeRecord, err := tool.registerFilledDocument(
		ctx,
		idempotentStore,
		mediaOwner,
		snapshot,
		report,
	)
	if err != nil {
		return tool.failFormCommit(
			ctx,
			materialized,
			owner,
			true,
			"artifact_registration_failed",
			"the verified PDF could not be registered safely",
		)
	}
	record, err = tool.formJobs.RecordFormCommitArtifact(ctx, document.FormCommitArtifactRequest{
		JobID: materialized.JobID, ExpectedRevision: materialized.Revision, Owner: owner,
		OperationID: materialized.OperationID, ArtifactRef: registeredRef,
		ArtifactDigest: report.Write.OutputSHA256,
	})
	if err != nil {
		return documentFormToolError(err)
	}
	return tool.documentFormDeliveryResult(mediaOwner, record, writeRecord, report.Write)
}

func (tool *DocumentTool) resumeFormDelivery(
	ctx context.Context,
	mediaOwner media.MediaOwner,
	record document.FormJobRecord,
) *toolshared.ToolResult {
	journal, err := tool.documentWriteJournal()
	if err != nil {
		return documentFormToolError(err)
	}
	writeOwner := documentFormWriteAuthority(mediaOwner)
	writeRecord, found, err := journal.Lookup(ctx, record.OperationID, writeOwner)
	if err != nil || !found || writeRecord.Artifact == nil ||
		writeRecord.ArtifactRef != record.ArtifactRef || writeRecord.Artifact.SHA256 != record.ArtifactDigest {
		return documentFormToolFailure(
			"form_job_conflict",
			"the verified form delivery does not match its durable document operation",
		)
	}
	deliveries := tool.documentDeliveries()
	binding := formDocumentDeliveryBinding(writeOwner, record)
	if writeRecord.State == document.WriteDeliveryPending {
		writeRecord, err = deliveries.reconcile(ctx, binding, writeRecord)
		if err != nil {
			return documentFormToolError(err)
		}
	}
	switch writeRecord.State {
	case document.WriteRegistered:
		return tool.documentFormDeliveryResult(mediaOwner, record, writeRecord, nil)
	case document.WriteDeliveryPending:
		return documentFormCommitResult(record, nil)
	case document.WriteDelivered, document.WriteDeliveryFailed, document.WriteDeliveryAmbiguous:
		outcome, outcomeErr := documentDeliveryOutcomeFromWriteState(writeRecord.State)
		if outcomeErr != nil {
			return documentFormToolError(outcomeErr)
		}
		settled, settleErr := deliveries.settleForm(ctx, binding, outcome)
		if settleErr != nil {
			return documentFormToolError(settleErr)
		}
		if settled == nil {
			return documentFormToolError(document.ErrWriteConflict)
		}
		if settled.State == document.FormJobCompleted {
			return documentFormCommitResult(*settled, nil)
		}
		return documentFormTerminalFailure(*settled)
	default:
		return documentFormToolFailure(
			"form_job_conflict",
			"the verified form delivery is not in a recoverable state",
		)
	}
}

func (tool *DocumentTool) documentFormDeliveryResult(
	mediaOwner media.MediaOwner,
	formRecord document.FormJobRecord,
	writeRecord document.WriteOperationRecord,
	write *document.FormWriteFacts,
) *toolshared.ToolResult {
	result := documentFormCommitResult(formRecord, write)
	result.Media = []string{formRecord.ArtifactRef}
	result.ForUser = "Filled and verified PDF."
	result.Deliverable = documentFormWorkflowDeliverable(formRecord, writeRecord)
	result.WithWriteAudit(toolshared.WriteAuditEntry{
		Kind: "document", Target: formRecord.ArtifactRef, Action: "form_commit", Tool: "document",
		Metadata: map[string]string{
			"operation_id": formRecord.OperationID, "sha256": formRecord.ArtifactDigest,
		},
	})
	result.WithDeliveryIntent(toolshared.DeliveryImmediateContinue)
	writeOwner := documentFormWriteAuthority(mediaOwner)
	result.Delivery.Outbound = documentFormOutbound(
		writeOwner,
		formRecord.OperationID,
		writeRecord,
		formRecord.ArtifactRef,
		&formRecord,
	)
	deliveries := tool.documentDeliveries()
	binding := formDocumentDeliveryBinding(writeOwner, formRecord)
	result.Delivery.Commit = func(commitCtx context.Context) error {
		return deliveries.commit(commitCtx, binding, toolshared.ToolOutboundDeliveryID(commitCtx))
	}
	result.Delivery.Settle = func(
		settleCtx context.Context,
		settlement toolshared.DeliverySettlement,
	) error {
		outcome, err := documentDeliveryOutcomeFromSettlement(settlement.Status)
		if err != nil {
			return err
		}
		settled, err := deliveries.settle(settleCtx, binding, outcome, settlement.DeliveryID)
		if err != nil {
			return err
		}
		if settled == nil {
			return document.ErrWriteConflict
		}
		updated := documentFormCommitResult(*settled, nil)
		result.ForLLM = updated.ForLLM
		return nil
	}
	return result
}

func documentFormWriteAuthority(owner media.MediaOwner) document.Authority {
	return document.Authority{
		Kind: "inbound_media", WorkspaceID: owner.WorkspaceID, AgentID: owner.AgentID,
		ActorID: owner.ActorID, RouteID: owner.RouteID, SessionID: owner.SessionID,
	}
}

func documentFormWorkflowDeliverable(
	record document.FormJobRecord,
	writeRecord document.WriteOperationRecord,
) *taskresult.Deliverable {
	return &taskresult.Deliverable{
		Text: "Filled and verified PDF.",
		Artifacts: []taskresult.Artifact{{
			Ref: record.ArtifactRef, Kind: "file", Filename: "filled-document.pdf",
			ContentType: "application/pdf",
		}},
		Metadata: map[string]string{
			"operation": "form", "operation_id": record.OperationID,
			"document_delivery_id": writeRecord.DeliveryID, "output_sha256": record.ArtifactDigest,
		},
	}
}

func documentFormTerminalFailure(record document.FormJobRecord) *toolshared.ToolResult {
	code := strings.TrimSpace(record.FailureCode)
	if !validDocumentFormFailureCode(code) {
		code = "form_job_terminal"
	}
	message := "the form delivery is no longer active"
	if record.State == document.FormJobUncertain {
		message = "the form delivery outcome is uncertain and will not be replayed"
	}
	return documentFormToolFailure(code, message)
}

func (tool *DocumentTool) prepareFormCommit(
	ctx context.Context,
	store ownedDocumentMediaStore,
	mediaOwner media.MediaOwner,
	owner document.FormJobOwner,
	jobID string,
) (document.FormJobRecord, document.FormFieldsFacts, document.FormCommitRequest, document.FormCommitBinding, error) {
	record, schema, err := tool.loadFormWorkflow(ctx, store, mediaOwner, owner, jobID)
	if err != nil {
		return document.FormJobRecord{}, document.FormFieldsFacts{}, document.FormCommitRequest{},
			document.FormCommitBinding{}, err
	}
	policyRevision, err := tool.formPolicy.Revision()
	if err != nil || tool.formAudit == nil {
		return document.FormJobRecord{}, document.FormFieldsFacts{}, document.FormCommitRequest{},
			document.FormCommitBinding{}, document.ErrFormAuditUnavailable
	}
	request := document.FormCommitRequest{
		JobID: record.JobID, ExpectedRevision: record.Revision, Owner: owner, Schema: schema,
		AuditPolicyRevision:  policyRevision,
		OutputPolicyRevision: document.FormCommitOutputPolicyRevision,
	}
	var binding document.FormCommitBinding
	if record.State == document.FormJobReviewReady {
		record, binding, err = tool.formJobs.PrepareFormCommitApproval(ctx, request)
	} else {
		record, binding, err = tool.formJobs.CurrentFormCommitBinding(ctx, request)
	}
	return record, schema, request, binding, err
}

func (tool *DocumentTool) formCommitApprovalResult(
	ctx context.Context,
	owner document.FormJobOwner,
	schema document.FormFieldsFacts,
	record document.FormJobRecord,
) *toolshared.ToolResult {
	review, err := tool.formJobs.CurrentFormReview(ctx, record.JobID, owner, schema)
	if err != nil {
		return documentFormToolError(err)
	}
	result := documentFormToolResult(safeDocumentFormResult{
		SchemaVersion: documentFormWorkflowSchemaVersion, Operation: "form", FormAction: "commit",
		Job: safeDocumentFormJobProjection(record), Review: documentFormReviewProjection(review),
	})
	result.Control.Suspension = &interactions.SuspensionRequest{
		Kind:          interactions.KindApproval,
		PromptSummary: "Fill and verify the exact reviewed PDF form",
		Timeout:       documentFormQuestionTimeout,
	}
	result.Delivery.Intent = toolshared.DeliverySilent
	return result
}

func (tool *DocumentTool) failFormCommit(
	ctx context.Context,
	record document.FormJobRecord,
	owner document.FormJobOwner,
	uncertain bool,
	code string,
	message string,
) *toolshared.ToolResult {
	_, err := tool.formJobs.FailFormCommit(
		ctx,
		record.JobID,
		record.Revision,
		owner,
		uncertain,
		code,
	)
	if err != nil {
		return documentFormToolError(err)
	}
	return documentFormToolFailure(code, message)
}

func (tool *DocumentTool) startFormWorkflow(
	ctx context.Context,
	store ownedDocumentMediaStore,
	mediaOwner media.MediaOwner,
	owner document.FormJobOwner,
	args map[string]any,
) *toolshared.ToolResult {
	suppliedSchemaDigest := strings.TrimSpace(stringDocumentArg(args, "field_schema_digest"))
	if suppliedSchemaDigest == "" {
		return documentFormToolFailure(
			"field_discovery_required",
			"call form discover for the exact source and pass its field_schema_digest to form start",
		)
	}
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
	window := documentFormWindowArgs(args)
	if window.explicit {
		summary := documentFormDiscoverySummary(schema)
		view := documentFormMappingWindow(summary, schema, "", window)
		if !summary.ReadyForReview && len(view.CandidateFields) == 0 {
			return documentFormToolFailure("invalid_input", "the selected field window is empty; browse another window")
		}
	}
	preparedSource, err := prepareDocumentFormSource(store, mediaOwner, ref, startKey, schema.SourceSHA256)
	if err != nil {
		logDocumentFormSourceRetentionFailure("prepare", err)
		return documentFormToolFailure(
			"protected_store_unavailable",
			"the immutable form source could not be retained",
		)
	}
	discoveryDigest, err := documentFormDiscoveryDigest(schema.SourceSHA256, schema)
	if err != nil {
		return documentFormToolFailure("form_job_stale", "the form field schema is unavailable")
	}
	if suppliedSchemaDigest != discoveryDigest {
		return documentFormToolFailure(
			"form_job_stale",
			"the field schema digest does not match the exact current document; call form discover again",
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
		logDocumentFormSourceRetentionFailure("register", err)
		_, _ = tool.formJobs.Delete(ctx, record.JobID, record.Revision, owner)
		_ = store.ReleaseAll(documentFormSourceScope(record.JobID))
		return documentFormToolFailure(
			"protected_store_unavailable",
			"the immutable form source could not be retained",
		)
	}
	return tool.formProgressWindowResult(ctx, owner, schema, record, "start", "", window)
}

func (tool *DocumentTool) continueFormWorkflow(
	ctx context.Context,
	store ownedDocumentMediaStore,
	mediaOwner media.MediaOwner,
	owner document.FormJobOwner,
	args map[string]any,
) *toolshared.ToolResult {
	jobID := strings.TrimSpace(stringDocumentArg(args, "job_id"))
	receiptReference := strings.TrimSpace(stringDocumentArg(args, "answer_ref"))
	if receiptReference == "" {
		// event_id was advertised by PDFI1. Accept it only for persisted calls and
		// rolling-upgrade recovery; new model schemas expose answer_ref alone.
		receiptReference = strings.TrimSpace(stringDocumentArg(args, "event_id"))
	}
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
	recentlyCompletedFieldID := ""
	if eventID != "" {
		mapped, mapErr := tool.formJobs.MapFormEvent(ctx, document.FormEventMappingRequest{
			JobID: jobID, ExpectedRevision: record.Revision, Owner: owner, Schema: schema,
			SourceEventID: eventID, IdempotencyKey: "protected-receipt:" + eventID,
		})
		if mapErr != nil {
			return documentFormToolError(mapErr)
		}
		record = mapped.Job
		recentlyCompletedFieldID = mapped.Field.FieldID
	}
	return tool.formProgressResult(ctx, owner, schema, record, "continue", recentlyCompletedFieldID)
}

func (tool *DocumentTool) navigateFormWorkflow(
	ctx context.Context,
	store ownedDocumentMediaStore,
	mediaOwner media.MediaOwner,
	owner document.FormJobOwner,
	args map[string]any,
	formAction string,
) *toolshared.ToolResult {
	reference := strings.TrimSpace(stringDocumentArg(args, "navigation_ref"))
	parts, err := document.ParseFormProtectedNavigationReference(reference)
	if err != nil || string(parts.Action) != formAction {
		return documentFormToolFailure("form_job_conflict", "the protected navigation receipt is invalid")
	}
	record, schema, err := tool.loadFormWorkflow(ctx, store, mediaOwner, owner, parts.JobID)
	if err != nil {
		return documentFormToolError(err)
	}
	candidateFieldIDs := make([]string, 0, len(schema.Fields))
	for _, field := range schema.Fields {
		if !field.ReadOnly {
			candidateFieldIDs = append(candidateFieldIDs, field.ID)
		}
	}
	action, fieldID, err := tool.formJobs.ConsumeFormProtectedNavigationReference(
		ctx,
		reference,
		owner,
		candidateFieldIDs,
		toolshared.ToolExecutionID(ctx),
		toolshared.ToolCallID(ctx),
	)
	if err != nil || string(action) != formAction {
		return documentFormToolFailure("form_job_conflict", "the protected navigation receipt is invalid")
	}
	controls, err := tool.formJobs.QuestionControls(ctx, document.FormProtectedAnswerBindingRequest{
		JobID: record.JobID, ExpectedRevision: record.Revision, Owner: owner, FieldID: fieldID,
	})
	if err != nil {
		return documentFormToolError(err)
	}
	return tool.formQuestionResult(ctx, owner, schema, record, fieldID, formAction, documentFormQuestionPresentation{
		language: controls.Language, blankActions: controls.BlankActions,
		question: controls.Question, checkedLabel: controls.CheckedLabel, uncheckedLabel: controls.UncheckedLabel,
	})
}

func (tool *DocumentTool) clarifyFormIntent(
	ctx context.Context,
	store ownedDocumentMediaStore,
	mediaOwner media.MediaOwner,
	owner document.FormJobOwner,
	args map[string]any,
) *toolshared.ToolResult {
	record, schema, err := tool.loadFormWorkflow(ctx, store, mediaOwner, owner, stringDocumentArg(args, "job_id"))
	if err != nil {
		return documentFormToolError(err)
	}
	if record.State != document.FormJobPrepared || len(record.Fields) != 0 {
		return documentFormToolFailure("form_job_conflict", "intent clarification is only available before collection")
	}
	return tool.formProgressResult(ctx, owner, schema, record, "clarify_intent", "")
}

func (tool *DocumentTool) formEvidence(
	ctx context.Context,
	store ownedDocumentMediaStore,
	mediaOwner media.MediaOwner,
	owner document.FormJobOwner,
	args map[string]any,
) *toolshared.ToolResult {
	jobID := stringDocumentArg(args, "job_id")
	record, schema, err := tool.loadFormWorkflow(ctx, store, mediaOwner, owner, jobID)
	if err != nil {
		return documentFormToolError(err)
	}
	progress := tool.formProgressWindowResult(ctx, owner, schema, record, "evidence", "", documentFormWindowArgs(args))
	if progress.IsError {
		return progress
	}
	ref, err := tool.formJobs.SourceRef(ctx, jobID, owner)
	if err != nil {
		return documentFormToolError(err)
	}
	readArgs := map[string]any{"pages": args["pages"], "max_characters": documentFormEvidenceTextLimit}
	var read *toolshared.ToolResult
	if stringDocumentArg(args, "evidence_mode") == "render" {
		if !toolshared.ToolDocumentVisionAvailable(ctx) {
			return documentToolFailure("render", document.StateUnavailable, document.FailureVisionUnavailable,
				"the selected model route has no configured image-input path")
		}
		readArgs = map[string]any{
			"pages":         args["pages"],
			"max_dimension": documentFormEvidenceRenderEdge,
			"dpi":           documentFormEvidenceRenderDPI,
		}
		read = tool.render(ctx, store, ref, mediaOwner, readArgs)
	} else {
		read = tool.extract(ctx, store, ref, mediaOwner, readArgs)
	}
	if read.IsError {
		return read
	}
	var projection safeDocumentFormResult
	var evidence safeDocumentReport
	if json.Unmarshal([]byte(progress.ForLLM), &projection) != nil ||
		json.Unmarshal([]byte(read.ForLLM), &evidence) != nil || evidence.Source == nil ||
		evidence.Source.SHA256 != record.SourceDigest {
		return documentFormToolFailure(
			"form_job_stale",
			"the planning evidence does not match the immutable form source",
		)
	}
	projection.Evidence = &evidence
	result := documentFormToolResult(projection)
	result.ContextText, result.ContextMedia = read.ContextText, read.ContextMedia
	return result
}

func (tool *DocumentTool) statusFormWorkflow(
	ctx context.Context,
	store ownedDocumentMediaStore,
	mediaOwner media.MediaOwner,
	owner document.FormJobOwner,
	args map[string]any,
) *toolshared.ToolResult {
	jobID := strings.TrimSpace(stringDocumentArg(args, "job_id"))
	recorded, err := tool.formJobs.Get(ctx, jobID, owner)
	if err != nil {
		return documentFormToolError(err)
	}
	if documentFormTerminalState(recorded.State) {
		return documentFormTerminalStatusResult(recorded)
	}
	record, schema, err := tool.loadFormWorkflow(ctx, store, mediaOwner, owner, jobID)
	if err != nil {
		latest, latestErr := tool.formJobs.Get(ctx, jobID, owner)
		if latestErr == nil && documentFormTerminalState(latest.State) {
			return documentFormTerminalStatusResult(latest)
		}
		return documentFormToolError(err)
	}
	projection := safeDocumentFormResult{
		SchemaVersion: documentFormWorkflowSchemaVersion,
		Operation:     "form",
		FormAction:    "status",
		Job:           safeDocumentFormJobProjection(record),
	}
	window := documentFormWindowArgs(args)
	if record.AuditRevision != 0 && !window.explicit {
		review, reviewErr := tool.formJobs.CurrentFormReview(ctx, jobID, owner, schema)
		if reviewErr != nil {
			return documentFormToolError(reviewErr)
		}
		projection.Review = documentFormReviewProjection(review)
	} else {
		summary, summaryErr := tool.formJobs.FormMappingSummary(ctx, jobID, owner, schema)
		if summaryErr != nil {
			return documentFormToolError(summaryErr)
		}
		projection.Mapping = documentFormMappingWindow(summary, schema, "", window)
	}
	return documentFormToolResult(projection)
}

func documentFormTerminalStatusResult(record document.FormJobRecord) *toolshared.ToolResult {
	projection := safeDocumentFormResult{
		SchemaVersion: documentFormWorkflowSchemaVersion,
		Operation:     "form",
		FormAction:    "status",
		Job:           safeDocumentFormJobProjection(record),
	}
	if record.State == document.FormJobCompleted {
		projection.Commit = safeDocumentFormCommitProjection(record, nil)
	}
	return documentFormToolResult(projection)
}

func documentFormTerminalState(state document.FormJobState) bool {
	switch state {
	case document.FormJobCompleted, document.FormJobCanceled, document.FormJobExpired,
		document.FormJobDeleted, document.FormJobFailed, document.FormJobUncertain:
		return true
	default:
		return false
	}
}

func (tool *DocumentTool) collectFormWorkflow(
	ctx context.Context,
	store ownedDocumentMediaStore,
	mediaOwner media.MediaOwner,
	owner document.FormJobOwner,
	args map[string]any,
	formAction string,
) *toolshared.ToolResult {
	jobID := strings.TrimSpace(stringDocumentArg(args, "job_id"))
	fieldID := strings.TrimSpace(stringDocumentArg(args, "field_id"))
	record, schema, err := tool.loadFormWorkflow(ctx, store, mediaOwner, owner, jobID)
	if err != nil {
		return documentFormToolError(err)
	}
	formSummary := strings.TrimSpace(stringDocumentArg(args, "form_summary"))
	collectionPlan := strings.TrimSpace(stringDocumentArg(args, "collection_plan"))
	firstQuestion := len(record.Fields) == 0
	if firstQuestion && (formSummary == "" || collectionPlan == "") {
		return documentFormToolFailure(
			"agent_plan_required",
			"the first protected question requires an agent-authored form_summary and collection_plan",
		)
	}
	if !firstQuestion && (formSummary != "" || collectionPlan != "") {
		return documentFormToolFailure(
			"invalid_input",
			"form_summary and collection_plan apply only to the first protected question",
		)
	}
	fieldIndex := slices.IndexFunc(schema.Fields, func(field document.FormField) bool {
		return field.ID == fieldID && !field.ReadOnly
	})
	if fieldIndex < 0 {
		return documentFormToolFailure("field_unresolved", "the requested form field is unavailable")
	}
	if schema.Fields[fieldIndex].Kind == document.FormFieldCheckbox &&
		(strings.TrimSpace(stringDocumentArg(args, "checked_label")) == "" ||
			strings.TrimSpace(stringDocumentArg(args, "unchecked_label")) == "") {
		return documentFormToolFailure(
			"checkbox_labels_required",
			"checkbox questions require checked_label and unchecked_label matching their meanings",
		)
	}
	language := ""
	if rawLanguage := stringDocumentArg(args, "interaction_language"); rawLanguage != "" {
		language, err = interactions.CanonicalPromptLanguage(rawLanguage)
		if err != nil {
			return documentFormToolFailure("invalid_input", "the question language is invalid")
		}
	}
	blankActions := []interactions.ProtectedAnswerAction(nil)
	if raw, present := args["blank_actions"]; present {
		blankActions, err = documentFormBlankActions(raw)
		if err != nil {
			return documentFormToolFailure("invalid_input", "the question blank actions are invalid")
		}
	}
	return tool.formQuestionResult(
		ctx,
		owner,
		schema,
		record,
		fieldID,
		formAction,
		documentFormQuestionPresentation{
			question: strings.TrimSpace(stringDocumentArg(args, "question")),
			summary:  formSummary, plan: collectionPlan, language: language, blankActions: blankActions,
			checkedLabel:   strings.TrimSpace(stringDocumentArg(args, "checked_label")),
			uncheckedLabel: strings.TrimSpace(stringDocumentArg(args, "unchecked_label")),
		},
	)
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

func (tool *DocumentTool) formProgressResult(
	ctx context.Context,
	owner document.FormJobOwner,
	schema document.FormFieldsFacts,
	record document.FormJobRecord,
	formAction string,
	recentlyCompletedFieldID string,
) *toolshared.ToolResult {
	return tool.formProgressWindowResult(ctx, owner, schema, record, formAction, recentlyCompletedFieldID,
		documentFormWindowSelection{})
}

func (tool *DocumentTool) formProgressWindowResult(
	ctx context.Context,
	owner document.FormJobOwner,
	schema document.FormFieldsFacts,
	record document.FormJobRecord,
	formAction string,
	recentlyCompletedFieldID string,
	window documentFormWindowSelection,
) *toolshared.ToolResult {
	if record.State == document.FormJobReviewReady {
		review, err := tool.formJobs.CurrentFormReview(ctx, record.JobID, owner, schema)
		if err != nil {
			return documentFormToolError(err)
		}
		return preserveDocumentToolVisibility(documentFormToolResult(safeDocumentFormResult{
			SchemaVersion: documentFormWorkflowSchemaVersion, Operation: "form", FormAction: formAction,
			Job: safeDocumentFormJobProjection(record), Review: documentFormReviewProjection(review),
		}))
	}
	summary, err := tool.formJobs.FormMappingSummary(ctx, record.JobID, owner, schema)
	if err != nil {
		return documentFormToolError(err)
	}
	return preserveDocumentToolVisibility(documentFormToolResult(safeDocumentFormResult{
		SchemaVersion: documentFormWorkflowSchemaVersion, Operation: "form", FormAction: formAction,
		Job: safeDocumentFormJobProjection(record),
		Mapping: documentFormMappingWindow(
			summary,
			schema,
			recentlyCompletedFieldID,
			window,
		),
	}))
}

func (tool *DocumentTool) reviewFormWorkflow(
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
	if record.State == document.FormJobReviewReady {
		return tool.formProgressResult(ctx, owner, schema, record, "review", "")
	}
	summary, err := tool.formJobs.FormMappingSummary(ctx, record.JobID, owner, schema)
	if err != nil {
		return documentFormToolError(err)
	}
	if !summary.ReadyForReview {
		return preserveDocumentToolVisibility(documentFormToolResult(safeDocumentFormResult{
			SchemaVersion: documentFormWorkflowSchemaVersion, Operation: "form", FormAction: "review",
			Job: safeDocumentFormJobProjection(record), Mapping: documentFormMappingProjection(summary, schema),
		}))
	}
	result, err := tool.formJobs.ReviewFormJob(ctx, document.FormReviewRequest{
		JobID: record.JobID, ExpectedRevision: record.Revision, Owner: owner,
		Schema: schema, Policy: tool.formPolicy, Auditor: tool.formAudit,
	})
	if err != nil {
		return documentFormToolError(err)
	}
	return preserveDocumentToolVisibility(documentFormToolResult(safeDocumentFormResult{
		SchemaVersion: documentFormWorkflowSchemaVersion, Operation: "form", FormAction: "review",
		Job: safeDocumentFormJobProjection(result.Job), Review: documentFormReviewProjection(result.Review),
	}))
}

func preserveDocumentToolVisibility(result *toolshared.ToolResult) *toolshared.ToolResult {
	if result != nil && !result.IsError {
		result.Control.PreserveToolVisibility = true
	}
	return result
}

func (tool *DocumentTool) formQuestionResult(
	ctx context.Context,
	owner document.FormJobOwner,
	schema document.FormFieldsFacts,
	record document.FormJobRecord,
	fieldID string,
	formAction string,
	presentation documentFormQuestionPresentation,
) *toolshared.ToolResult {
	fieldIndex := slices.IndexFunc(schema.Fields, func(field document.FormField) bool {
		return field.ID == fieldID && !field.ReadOnly
	})
	if fieldIndex < 0 {
		return documentFormToolFailure("field_unresolved", "the next form field is unavailable")
	}
	field := schema.Fields[fieldIndex]
	if field.Required && len(presentation.blankActions) > 0 {
		return documentFormToolFailure("invalid_input", "required form fields cannot offer blank actions")
	}
	options, err := documentFormQuestionOptions(field, presentation.checkedLabel, presentation.uncheckedLabel)
	if err != nil {
		return documentFormToolFailure("invalid_input", "the form checkbox labels are invalid")
	}
	supersedes := ""
	if current := slices.IndexFunc(record.Fields, func(state document.FormJobFieldState) bool {
		return state.FieldID == fieldID
	}); current >= 0 {
		supersedes = record.Fields[current].EventID
	}
	binding, err := tool.formJobs.NewProtectedAnswerBinding(ctx, document.FormProtectedAnswerBindingRequest{
		JobID: record.JobID, ExpectedRevision: record.Revision, Owner: owner,
		FieldID: fieldID, SupersedesEventID: supersedes,
		QuestionControls: &document.FormQuestionControls{
			Language:       presentation.language,
			BlankActions:   presentation.blankActions,
			Question:       presentation.question,
			CheckedLabel:   presentation.checkedLabel,
			UncheckedLabel: presentation.uncheckedLabel,
		},
	})
	if err != nil {
		return documentFormToolError(err)
	}
	binding.Actions = []interactions.ProtectedAnswerAction{
		interactions.ProtectedAnswerActionClarify,
	}
	if _, backErr := document.FormProtectedNavigationTarget(
		record,
		fieldID,
		supersedes,
		interactions.ProtectedAnswerBack,
	); backErr == nil {
		binding.Actions = append(binding.Actions, interactions.ProtectedAnswerActionBack)
	}
	binding.Actions = append(binding.Actions, presentation.blankActions...)
	label := documentFormFieldLabel(field)
	question := interactions.Question{
		ID:           "document_form_value",
		Header:       interactions.PromptText(presentation.language, interactions.PromptFormHeader),
		Introduction: strings.TrimSpace(strings.Join([]string{presentation.summary, presentation.plan}, "\n\n")),
		Question:     documentFormQuestionText(presentation.question, label, field),
		Options:      options,
		MultiSelect:  field.MultiSelect,
	}
	if formAction == "clarify" || formAction == "back" {
		question.Introduction = interactions.PromptText(presentation.language, interactions.PromptFormAnswerHint)
	}
	suspension := interactions.SuspensionRequest{
		Kind: interactions.KindQuestion, Questions: []interactions.Question{question},
		PromptSummary: "Provide or correct PDF form field: " + truncateDocumentFormText(label, 256),
		Timeout:       documentFormQuestionTimeout, ProtectedAnswer: &binding,
		PromptLanguage: presentation.language,
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
		Job: safeDocumentFormJobProjection(record), Mapping: documentFormMappingProjection(summary, schema),
		NextField: documentFormFieldProjection(field, ""),
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

type documentFormSourceRetentionError struct {
	phase string
	err   error
}

func (failure *documentFormSourceRetentionError) Error() string {
	return "immutable document source retention failed"
}

func (failure *documentFormSourceRetentionError) Unwrap() error {
	return failure.err
}

func documentFormSourceRetentionFailure(phase string, err error) error {
	return &documentFormSourceRetentionError{phase: phase, err: err}
}

func logDocumentFormSourceRetentionFailure(operation string, err error) {
	phase := "unknown"
	var failure *documentFormSourceRetentionError
	if errors.As(err, &failure) && failure.phase != "" {
		phase = failure.phase
	}
	logger.ErrorCF("document", "Immutable document source retention failed", map[string]any{
		"code":      "protected_store_unavailable",
		"operation": operation,
		"phase":     phase,
	})
}

func prepareDocumentFormSource(
	store ownedDocumentMediaStore,
	owner media.MediaOwner,
	ref string,
	startKey string,
	inspectedSHA256 string,
) (preparedDocumentFormSource, error) {
	if _, ok := store.(idempotentOwnedDocumentMediaStore); !ok {
		return preparedDocumentFormSource{}, documentFormSourceRetentionFailure(
			"store_capability",
			errors.New("durable owned media registration is unavailable"),
		)
	}
	source, err := store.OpenOwned(ref, owner)
	if err != nil {
		return preparedDocumentFormSource{}, documentFormSourceRetentionFailure("source_open", err)
	}
	defer func() { _ = source.Close() }()
	if source.Identity.Size <= 0 || source.Identity.Size > document.DefaultMaxInputBytes ||
		!validDocumentSHA256(source.Identity.SHA256) || !validDocumentSHA256(inspectedSHA256) ||
		!strings.EqualFold(source.Identity.SHA256, inspectedSHA256) {
		return preparedDocumentFormSource{}, documentFormSourceRetentionFailure(
			"inspected_identity",
			errors.New("document form source identity does not match inspected bytes"),
		)
	}
	if mkdirErr := os.MkdirAll(media.TempDir(), 0o700); mkdirErr != nil {
		return preparedDocumentFormSource{}, documentFormSourceRetentionFailure("temp_dir", mkdirErr)
	}
	output, err := os.CreateTemp(media.TempDir(), ".document-form-source-*.pdf")
	if err != nil {
		return preparedDocumentFormSource{}, documentFormSourceRetentionFailure("temp_create", err)
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
		return preparedDocumentFormSource{}, documentFormSourceRetentionFailure("temp_mode", err)
	}
	hash := sha256.New()
	written, err := io.Copy(
		io.MultiWriter(output, hash),
		io.LimitReader(source.File, source.Identity.Size+1),
	)
	if err != nil || written != source.Identity.Size ||
		hex.EncodeToString(hash.Sum(nil)) != strings.ToLower(source.Identity.SHA256) {
		return preparedDocumentFormSource{}, documentFormSourceRetentionFailure(
			"source_copy",
			errors.Join(err, errors.New("document form source bytes changed during retention")),
		)
	}
	var trailing [1]byte
	if count, readErr := source.File.Read(trailing[:]); count != 0 ||
		(readErr != nil && !errors.Is(readErr, io.EOF)) {
		return preparedDocumentFormSource{}, documentFormSourceRetentionFailure(
			"source_copy",
			errors.New("document form source exceeds its immutable descriptor"),
		)
	}
	if err = output.Sync(); err != nil {
		return preparedDocumentFormSource{}, documentFormSourceRetentionFailure("temp_sync", err)
	}
	if err = output.Close(); err != nil {
		return preparedDocumentFormSource{}, documentFormSourceRetentionFailure("temp_close", err)
	}
	digest := sha256.Sum256([]byte("mintclaw.document-form-source.v1\x00" + startKey))
	identity := hex.EncodeToString(digest[:16])
	key := "document-form-source-" + identity
	retained, err := media.IdempotentRef(key)
	if err != nil {
		return preparedDocumentFormSource{}, documentFormSourceRetentionFailure("retained_ref", err)
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
		return documentFormSourceRetentionFailure(
			"store_capability",
			errors.New("durable owned media registration is unavailable"),
		)
	}
	retained, err := idempotent.StoreIdempotentOwned(prepared.path, media.MediaMeta{
		Filename: "form-source.pdf", ContentType: "application/pdf", Source: "tool:document-form",
		CleanupPolicy: media.CleanupPolicyDeleteOnCleanup,
		RetainUntil:   time.UnixMilli(record.ExpiresAt).UTC(),
	}, documentFormSourceScope(record.JobID), prepared.key, owner)
	if err != nil || retained != prepared.ref {
		return documentFormSourceRetentionFailure(
			"retained_identity",
			errors.Join(err, errors.New("document form source identity changed during retention")),
		)
	}
	registeredPath, resolveErr := store.Resolve(retained)
	if resolveErr == nil && registeredPath == prepared.path {
		// The non-persistent store owns this exact temporary path. Do not remove
		// it here; ReleaseAll or age cleanup owns its lifecycle.
		prepared.path = ""
	}
	return nil
}

func validDocumentSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
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

func documentFormQuestionText(
	agentQuestion string,
	label string,
	field document.FormField,
) string {
	agentQuestion = strings.TrimSpace(agentQuestion)
	if agentQuestion != "" {
		return agentQuestion
	}
	question := fmt.Sprintf("Provide %s for the PDF form. You may reply with free text.", label)
	if field.DateFormat != "" {
		question = fmt.Sprintf("Provide %s for the PDF form using %s. You may reply with free text.",
			label, field.DateFormat)
	}
	return question
}

func documentFormBlankActions(raw any) ([]interactions.ProtectedAnswerAction, error) {
	values, ok := raw.([]any)
	if typed, typedOK := raw.([]string); typedOK {
		values = make([]any, len(typed))
		for i, value := range typed {
			values[i] = value
		}
		ok = true
	}
	if !ok || len(values) > 2 {
		return nil, errors.New("blank_actions must contain only distinct skip and not_applicable actions")
	}
	actions := make([]interactions.ProtectedAnswerAction, 0, len(values))
	for _, rawValue := range values {
		value, valid := rawValue.(string)
		action := interactions.ProtectedAnswerAction(value)
		if !valid || (action != interactions.ProtectedAnswerActionSkip &&
			action != interactions.ProtectedAnswerActionNotApplicable) || slices.Contains(actions, action) {
			return nil, errors.New("blank_actions must contain only distinct skip and not_applicable actions")
		}
		actions = append(actions, action)
	}
	return actions, nil
}

func documentFormQuestionOptions(
	field document.FormField,
	checkedLabel string,
	uncheckedLabel string,
) ([]interactions.Option, error) {
	checkedLabel = strings.TrimSpace(checkedLabel)
	uncheckedLabel = strings.TrimSpace(uncheckedLabel)
	if field.Kind != document.FormFieldCheckbox && (checkedLabel != "" || uncheckedLabel != "") {
		return nil, errors.New("checkbox labels require a checkbox field")
	}
	switch field.Kind {
	case document.FormFieldCheckbox:
		if checkedLabel == "" && uncheckedLabel == "" {
			checkedLabel, uncheckedLabel = "Yes", "No"
		}
		if checkedLabel == "" || uncheckedLabel == "" ||
			!utf8.ValidString(checkedLabel) || !utf8.ValidString(uncheckedLabel) ||
			utf8.RuneCountInString(checkedLabel) > interactions.MaxOptionLabelLength ||
			utf8.RuneCountInString(uncheckedLabel) > interactions.MaxOptionLabelLength ||
			strings.EqualFold(checkedLabel, uncheckedLabel) {
			return nil, errors.New("invalid checkbox labels")
		}
		return []interactions.Option{
			{Label: checkedLabel, Value: "true"},
			{Label: uncheckedLabel, Value: "false"},
		}, nil
	case document.FormFieldRadio, document.FormFieldCombo, document.FormFieldList:
		if len(field.Options) < 2 || len(field.Options) > interactions.MaxOptions {
			return nil, nil
		}
		options := make([]interactions.Option, 0, len(field.Options))
		for _, option := range field.Options {
			label := strings.TrimSpace(option.Display)
			if label == "" {
				label = strings.TrimSpace(option.Export)
			}
			if label == "" || len(label) > interactions.MaxOptionLabelLength || !utf8.ValidString(label) {
				return nil, nil
			}
			options = append(options, interactions.Option{
				Label: label, Description: "Use this form choice.",
			})
		}
		return options, nil
	default:
		return nil, nil
	}
}

func documentFormMappingProjection(
	summary document.FormJobMappingSummary,
	schema document.FormFieldsFacts,
) *safeDocumentFormMapping {
	return documentFormMappingProjectionExcludingConfirmed(summary, schema, "")
}

func documentFormMappingProjectionExcludingConfirmed(
	summary document.FormJobMappingSummary,
	schema document.FormFieldsFacts,
	excludedConfirmedFieldID string,
) *safeDocumentFormMapping {
	return documentFormMappingWindow(summary, schema, excludedConfirmedFieldID, documentFormWindowSelection{})
}

func documentFormMappingWindow(
	summary document.FormJobMappingSummary,
	schema document.FormFieldsFacts,
	excludedConfirmedFieldID string,
	window documentFormWindowSelection,
) *safeDocumentFormMapping {
	projection := &safeDocumentFormMapping{
		Revision: summary.Revision, ConfirmedFieldCount: len(summary.ConfirmedFieldIDs),
		UnresolvedFieldCount: len(summary.Unresolved), ReadyForReview: summary.ReadyForReview,
		WritableFieldCount: summary.WritableFieldCount,
		Window: safeDocumentFormWindow{
			Pages: append([]int(nil), window.pages...), Offset: window.offset, Limit: documentFormCandidateLimit,
			Selected: window.explicit,
		},
	}
	blockers := make(map[string]string, len(summary.Unresolved))
	for _, blocker := range summary.Unresolved {
		blockers[blocker.FieldID] = blocker.Code
	}
	confirmed := make(map[string]struct{}, len(summary.ConfirmedFieldIDs))
	for _, fieldID := range summary.ConfirmedFieldIDs {
		confirmed[fieldID] = struct{}{}
	}
	statuses := make(map[string]string, len(summary.FieldProgress))
	for _, field := range summary.FieldProgress {
		statuses[field.FieldID] = field.Status
	}
	type candidate struct {
		field safeDocumentFormField
		page  int
		index int
	}
	unresolvedCandidates := make([]candidate, 0, len(blockers))
	confirmedCandidates := make([]candidate, 0, len(confirmed))
	for index, field := range schema.Fields {
		blocker, unresolved := blockers[field.ID]
		_, isConfirmed := confirmed[field.ID]
		if field.ReadOnly || (!unresolved && !isConfirmed) {
			continue
		}
		status := statuses[field.ID]
		if unresolved {
			status = blocker
			if status == "field_unresolved" {
				status = "missing"
			}
		} else if status == "" {
			status = "confirmed"
			if field.HasValue {
				status = "preserved"
			}
		}
		switch status {
		case "preserved":
			projection.PreservedFieldCount++
		case "confirmed":
			projection.ProvidedFieldCount++
		case "optional_blank":
			projection.OptionalBlankCount++
		case "missing":
			projection.MissingFieldCount++
		case "field_conflicting", "field_ambiguous":
			projection.ConflictingFieldCount++
		}
		if isConfirmed && !unresolved && field.ID == excludedConfirmedFieldID {
			continue
		}
		if len(window.pages) != 0 && !slices.ContainsFunc(field.Widgets, func(widget document.FormFieldWidget) bool {
			return slices.Contains(window.pages, widget.Page)
		}) {
			continue
		}
		page := documentFormFieldPage(field)
		if len(window.pages) != 0 {
			page = 0
			for _, widget := range field.Widgets {
				if slices.Contains(window.pages, widget.Page) && (page == 0 || widget.Page < page) {
					page = widget.Page
				}
			}
		}
		entry := candidate{
			field: *documentFormFieldProjection(field, blocker), page: page, index: index,
		}
		entry.field.Status = status
		entry.field.Page = page
		if unresolved {
			unresolvedCandidates = append(unresolvedCandidates, entry)
			continue
		}
		confirmedCandidates = append(confirmedCandidates, entry)
	}
	compareCandidates := func(left, right candidate) int {
		switch {
		case left.page == 0 && right.page != 0:
			return 1
		case left.page != 0 && right.page == 0:
			return -1
		case left.page < right.page:
			return -1
		case left.page > right.page:
			return 1
		case left.index < right.index:
			return -1
		case left.index > right.index:
			return 1
		default:
			return 0
		}
	}
	slices.SortStableFunc(unresolvedCandidates, compareCandidates)
	slices.SortStableFunc(confirmedCandidates, compareCandidates)
	selected := make([]candidate, 0, len(unresolvedCandidates)+len(confirmedCandidates))
	selectedIndexes := make(map[int]struct{}, cap(selected))
	selectedKinds := make(map[document.FormFieldKind]struct{}, documentFormCandidateLimit)
	appendCandidates := func(candidates []candidate) {
		start := len(selected)
		for _, entry := range candidates {
			if _, ok := selectedKinds[entry.field.Kind]; ok {
				continue
			}
			selected = append(selected, entry)
			selectedIndexes[entry.index] = struct{}{}
			selectedKinds[entry.field.Kind] = struct{}{}
		}
		for _, entry := range candidates {
			if _, ok := selectedIndexes[entry.index]; ok {
				continue
			}
			selected = append(selected, entry)
			selectedIndexes[entry.index] = struct{}{}
			selectedKinds[entry.field.Kind] = struct{}{}
		}
		for offset := start; offset < len(selected); offset += documentFormCandidateLimit {
			end := min(offset+documentFormCandidateLimit, len(selected))
			slices.SortStableFunc(selected[offset:end], compareCandidates)
		}
	}
	appendCandidates(unresolvedCandidates)
	appendCandidates(confirmedCandidates)
	projection.Window.Total = len(selected)
	if (summary.ReadyForReview || len(summary.Unresolved) == 0) && !window.explicit {
		projection.Window.Truncated = len(selected) != 0
		return projection
	}
	start := min(window.offset, len(selected))
	end := min(start+documentFormCandidateLimit, len(selected))
	projection.Window.Truncated = start != 0 || end < len(selected)
	if end < len(selected) {
		nextOffset := end
		projection.Window.NextOffset = &nextOffset
	}
	selected = selected[start:end]
	projection.CandidateFields = make([]safeDocumentFormField, 0, len(selected))
	for _, candidate := range selected {
		projection.CandidateFields = append(projection.CandidateFields, candidate.field)
	}
	return projection
}

func documentFormWindowArgs(args map[string]any) documentFormWindowSelection {
	pages, pagesPresent := args["pages"]
	offset, offsetPresent := args["field_offset"]
	selection := documentFormWindowSelection{explicit: pagesPresent || offsetPresent}
	if pagesPresent {
		selection.pages, _ = documentPagesArg(pages)
	}
	if offsetPresent {
		selection.offset, _ = documentIntArg(offset)
	}
	return selection
}

func documentFormReviewProjection(review document.FormReview) *safeDocumentFormReview {
	projection := &safeDocumentFormReview{
		SchemaVersion: review.SchemaVersion, JobID: review.JobID, State: review.State,
		Revision: review.Revision, ReviewRevision: review.ReviewRevision,
		FieldSchemaDigest: review.FieldSchemaDigest, AuditPolicyRevision: review.AuditPolicyRevision,
		AuditModel: review.AuditModel, AssignmentDigest: review.AssignmentDigest,
		ReviewDigest: review.ReviewDigest, RequestedAction: review.RequestedAction,
		WritableFieldCount: len(review.Fields), BlockerCount: len(review.Blockers), Ready: review.Ready,
	}
	blockers := make(map[string]string, len(review.Blockers))
	for _, blocker := range review.Blockers {
		blockers[blocker.FieldID] = blocker.Code
		if len(projection.Blockers) < documentFormCandidateLimit {
			projection.Blockers = append(projection.Blockers, blocker)
		}
	}
	projection.BlockersTruncated = len(review.Blockers) > len(projection.Blockers)

	fields := append([]document.FormReviewField(nil), review.Fields...)
	for index := range fields {
		field := &fields[index]
		field.Label = truncateDocumentFormText(field.Label, 256)
		switch field.Summary {
		case "provided":
			projection.ProvidedFieldCount++
		case "blank":
			projection.BlankFieldCount++
		case "existing":
			projection.ExistingFieldCount++
		case "unresolved":
			projection.UnresolvedFieldCount++
		}
	}
	slices.SortStableFunc(fields, func(left, right document.FormReviewField) int {
		leftPriority := documentFormReviewFieldPriority(left, blockers[left.FieldID])
		rightPriority := documentFormReviewFieldPriority(right, blockers[right.FieldID])
		switch {
		case leftPriority < rightPriority:
			return -1
		case leftPriority > rightPriority:
			return 1
		default:
			return 0
		}
	})
	if len(fields) > documentFormCandidateLimit {
		fields = fields[:documentFormCandidateLimit]
	}
	projection.Fields = fields
	projection.FieldsTruncated = len(review.Fields) > len(projection.Fields)
	return projection
}

func documentFormReviewFieldPriority(field document.FormReviewField, blocker string) int {
	if blocker != "" {
		return 0
	}
	if field.Summary == "unresolved" ||
		field.Validation != "" && field.Validation != document.FormValueValidationValid ||
		field.State == document.FormValueAmbiguous || field.State == document.FormValueConflicting ||
		field.State == document.FormValueInvalid || field.State == document.FormValueModelSuggested {
		return 1
	}
	if field.Source == document.FormValueSourceUser || field.Summary == "provided" || field.Summary == "blank" {
		return 2
	}
	return 3
}

func documentFormDiscoverySummary(schema document.FormFieldsFacts) document.FormJobMappingSummary {
	summary := document.FormJobMappingSummary{}
	for _, field := range schema.Fields {
		if field.ReadOnly {
			continue
		}
		summary.WritableFieldCount++
		if field.HasValue {
			summary.ConfirmedFieldIDs = append(summary.ConfirmedFieldIDs, field.ID)
			summary.FieldProgress = append(summary.FieldProgress, document.FormFieldProgress{
				FieldID: field.ID, Status: "preserved",
			})
			continue
		}
		summary.FieldProgress = append(summary.FieldProgress, document.FormFieldProgress{
			FieldID: field.ID, Status: "missing",
		})
		summary.Unresolved = append(summary.Unresolved, document.FormFieldMappingBlocker{
			FieldID: field.ID, Code: "field_unresolved",
		})
	}
	summary.ReadyForReview = len(summary.Unresolved) == 0
	return summary
}

func documentFormFieldProjection(field document.FormField, blocker string) *safeDocumentFormField {
	projection := &safeDocumentFormField{
		FieldID: field.ID, Label: documentFormFieldLabel(field), Kind: field.Kind,
		Required: field.Required, Page: documentFormFieldPage(field), DateFormat: field.DateFormat,
		MultiSelect: field.MultiSelect, Blocker: blocker,
	}
	label := strings.TrimSpace(field.AlternateName)
	if label == "" {
		label = strings.TrimSpace(field.Name)
	}
	projection.LabelTruncated = utf8.RuneCountInString(label) > 256
	if len(field.Options) >= 2 && len(field.Options) <= interactions.MaxOptions {
		projection.Options = make([]string, 0, len(field.Options))
		for _, option := range field.Options {
			label := strings.TrimSpace(option.Display)
			if label == "" {
				label = strings.TrimSpace(option.Export)
			}
			if label == "" || !utf8.ValidString(label) ||
				utf8.RuneCountInString(label) > interactions.MaxOptionLabelLength {
				projection.Options = nil
				break
			}
			projection.Options = append(projection.Options, label)
		}
	}
	return projection
}

func documentFormFieldPage(field document.FormField) int {
	page := 0
	for _, widget := range field.Widgets {
		if widget.Page <= 0 || (page != 0 && widget.Page >= page) {
			continue
		}
		page = widget.Page
	}
	return page
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
	if utf8.RuneCountInString(value) <= maximum {
		return value
	}
	return strings.TrimSpace(string([]rune(value)[:maximum]))
}

func safeDocumentFormJobProjection(record document.FormJobRecord) *safeDocumentFormJob {
	return &safeDocumentFormJob{
		JobID: record.JobID, State: record.State, Revision: record.Revision,
		FieldSchemaDigest: record.FieldSchemaDigest, AuditPolicyRevision: record.AuditPolicyRevision,
		ReviewRevision: record.ReviewRevision, ReviewDigest: record.ReviewDigest,
		ApprovalRevision: record.ApprovalRevision, OutputPolicyRevision: record.OutputPolicyRevision,
		OperationID: record.OperationID, ArtifactRef: record.ArtifactRef,
		ArtifactDigest: record.ArtifactDigest, ExpiresAt: record.ExpiresAt,
		NeedsInitialPlan: len(record.Fields) == 0 && !documentFormTerminalState(record.State),
	}
}

func documentFormCommitResult(
	record document.FormJobRecord,
	write *document.FormWriteFacts,
) *toolshared.ToolResult {
	return documentFormToolResult(safeDocumentFormResult{
		SchemaVersion: documentFormWorkflowSchemaVersion, Operation: "form", FormAction: "commit",
		Job: safeDocumentFormJobProjection(record), Commit: safeDocumentFormCommitProjection(record, write),
	})
}

func safeDocumentFormCommitProjection(
	record document.FormJobRecord,
	write *document.FormWriteFacts,
) *safeDocumentFormCommit {
	commit := &safeDocumentFormCommit{
		OperationID: record.OperationID, ArtifactRef: record.ArtifactRef,
		ArtifactDigest: record.ArtifactDigest, SourceUnchanged: record.ArtifactDigest != "",
	}
	if write != nil {
		commit.SourceUnchanged = write.SourceSHA256 == record.SourceDigest
		commit.StructuralAssertions = write.StructuralAssertions
		commit.VisualAssertions = write.VisualAssertions
		commit.CheckedFields = write.CheckedFields
	}
	return commit
}

func documentFormApprovalArguments(binding document.FormCommitBinding) map[string]any {
	return map[string]any{
		"schema_version":         "mintclaw.document_form_approval.v1",
		"job_id":                 binding.JobID,
		"owner_digest":           binding.OwnerDigest,
		"approval_revision":      binding.ApprovalRevision,
		"review_revision":        binding.ReviewRevision,
		"source_digest":          binding.SourceDigest,
		"field_schema_digest":    binding.FieldSchemaDigest,
		"backend_revision":       binding.BackendRevision,
		"audit_policy_revision":  binding.AuditPolicyRevision,
		"assignment_digest":      binding.AssignmentDigest,
		"review_digest":          binding.ReviewDigest,
		"output_policy_revision": binding.OutputPolicyRevision,
		"operation_id":           binding.OperationID,
		"expires_at":             binding.ExpiresAt,
	}
}

func equalDocumentFormApprovalArguments(left, right map[string]any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func clearDocumentFormFill(fill *document.FillMap) {
	if fill == nil {
		return
	}
	for index := range fill.Assignments {
		fill.Assignments[index].FieldID = ""
		if fill.Assignments[index].Value.Text != nil {
			*fill.Assignments[index].Value.Text = ""
		}
		if fill.Assignments[index].Value.Checked != nil {
			*fill.Assignments[index].Value.Checked = false
		}
		clear(fill.Assignments[index].Value.Choices)
		fill.Assignments[index].Value.Choices = nil
	}
	clear(fill.Assignments)
	fill.Assignments = nil
	fill.SchemaVersion = ""
}

type documentFormApprovalError struct {
	result *toolshared.ToolResult
}

func newDocumentFormApprovalError(result *toolshared.ToolResult) error {
	return &documentFormApprovalError{result: result}
}

func (err *documentFormApprovalError) Error() string {
	return "document form approval authority is unavailable"
}

func (err *documentFormApprovalError) SafeApprovalDenialResult() *toolshared.ToolResult {
	if err == nil {
		return nil
	}
	return err.result
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
	case errors.Is(err, document.ErrFormApprovalStale):
		code, message = "approval_stale", "the reviewed form approval is no longer current"
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
