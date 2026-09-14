package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

const documentDeliveryCommitTimeout = 2 * time.Second

func documentFillAssignmentsSchema() map[string]any {
	return map[string]any{
		"type":     "array",
		"minItems": 1,
		"maxItems": document.DefaultMaxFormFields,
		"description": "Explicit typed values keyed by opaque field_id values returned by fields. " +
			"Values are protected and omitted from durable history.",
		"items": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"field_id": map[string]any{"type": "string"},
				"value": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"type": map[string]any{
							"type": "string",
							"enum": []string{"text", "boolean", "choice", "choices"},
						},
						"text":    map[string]any{"type": "string"},
						"checked": map[string]any{"type": "boolean"},
						"choices": map[string]any{
							"type": "array", "minItems": 1,
							"items": map[string]any{"type": "string"},
						},
					},
					"required": []string{"type"},
				},
			},
			"required": []string{"field_id", "value"},
		},
	}
}

func documentAssignmentCount(value any) int {
	switch assignments := value.(type) {
	case []any:
		return len(assignments)
	case []document.FormFillAssignment:
		return len(assignments)
	default:
		return 0
	}
}

// documentDurableAssignmentProjection keeps the persisted assistant tool call
// schema-valid while removing every model-authored field identifier and value.
// Invalid or empty live input still receives one valid placeholder so the
// original in-memory call can reach normal tool validation without first
// leaking its malformed protected payload into durable history.
func documentDurableAssignmentProjection(value any) []any {
	count := documentAssignmentCount(value)
	if count < 1 {
		count = 1
	}
	if count > document.DefaultMaxFormFields {
		count = document.DefaultMaxFormFields
	}
	projected := make([]any, count)
	for index := range projected {
		projected[index] = map[string]any{
			"field_id": "redacted_field",
			"value": map[string]any{
				"type": "text",
				"text": "[redacted]",
			},
		}
	}
	return projected
}

func documentFillMapArg(value any) (document.FillMap, error) {
	if documentAssignmentCount(value) == 0 {
		return document.FillMap{}, errors.New("fill requires at least one typed assignment")
	}
	encoded, err := json.Marshal(struct {
		SchemaVersion string `json:"schema_version"`
		Assignments   any    `json:"assignments"`
	}{
		SchemaVersion: document.FillMapSchemaVersion,
		Assignments:   value,
	})
	if err != nil {
		return document.FillMap{}, errors.New("document fill assignments are invalid")
	}
	fill, err := document.DecodeFillMap(bytes.NewReader(encoded))
	if err != nil {
		return document.FillMap{}, err
	}
	return fill, nil
}

func (tool *DocumentTool) fields(
	ctx context.Context,
	store ownedDocumentMediaStore,
	ref string,
	owner media.MediaOwner,
) *toolshared.ToolResult {
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
	return documentToolReportResult(report)
}

func (tool *DocumentTool) fill(
	ctx context.Context,
	store ownedDocumentMediaStore,
	ref string,
	owner media.MediaOwner,
	args map[string]any,
) *toolshared.ToolResult {
	idempotentStore, ok := store.(idempotentOwnedDocumentMediaStore)
	if !ok {
		return documentToolFailure(
			"fill",
			document.StateUnavailable,
			document.FailureArtifactRegistration,
			"durable document artifact registration is unavailable",
		)
	}
	fill, err := documentFillMapArg(args["assignments"])
	if err != nil {
		return documentToolFailure(
			"fill",
			document.StateFailed,
			document.FailureInvalidInput,
			"document fill assignments are invalid",
		).WithError(err)
	}
	operationID, _ := args["operation_id"].(string)
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		operationID = documentToolWriteOperationID(ctx)
	}
	snapshot, report := document.FillMedia(ctx, store, ref, owner, fill, document.FormWriteOptions{
		Acquire:     document.AcquireOptions{ScratchRoot: tool.scratchRoot},
		StateRoot:   tool.stateRoot,
		OperationID: operationID,
	})
	if snapshot != nil {
		defer func() { _ = snapshot.Close() }()
	}
	if report.State != document.StateSucceeded || snapshot == nil {
		return documentToolReportResult(report)
	}
	registeredRef, record, err := tool.registerFilledDocument(
		ctx,
		idempotentStore,
		owner,
		snapshot,
		report,
	)
	if err != nil {
		return documentToolFailure(
			"fill",
			document.StateFailed,
			document.FailureArtifactRegistration,
			"verified document could not be registered",
		).WithError(err)
	}
	if record.State == document.WriteDeliveryPending {
		record, err = tool.reconcileDocumentWriteDelivery(
			ctx,
			report.Input.Authority,
			report.OperationID,
			record,
		)
		if err != nil {
			return documentToolFailure(
				"fill",
				document.StateUncertain,
				document.FailureRecoveryUncertain,
				"document delivery recovery could not be reconciled",
			).WithError(err)
		}
	}
	result := documentToolReportResultWithDelivery(report, record, registeredRef)
	switch record.State {
	case document.WriteDelivered:
		return result
	case document.WriteDeliveryFailed:
		return documentToolFailure(
			"fill",
			document.StateFailed,
			document.FailureDeliveryFailed,
			"document delivery previously failed before remote acceptance",
		)
	case document.WriteDeliveryAmbiguous, document.WriteDeliveryPending:
		return documentToolFailure(
			"fill",
			document.StateUncertain,
			document.FailureDeliveryAmbiguous,
			"document delivery may already have reached the remote channel and will not be replayed blindly",
		)
	case document.WriteRegistered:
	default:
		return documentToolFailure(
			"fill",
			document.StateUncertain,
			document.FailureRecoveryUncertain,
			"document write recovery is uncertain",
		)
	}
	result.Media = []string{registeredRef}
	result.ForUser = "Filled and verified PDF."
	result.Deliverable = documentFormDeliverable(report, record, registeredRef)
	result.WithWriteAudit(toolshared.WriteAuditEntry{
		Kind:   "document",
		Target: registeredRef,
		Action: "fill",
		Tool:   "document",
		Metadata: map[string]string{
			"operation_id": report.OperationID,
			"sha256":       report.Write.OutputSHA256,
		},
	})
	result.WithDeliveryIntent(toolshared.DeliveryImmediateContinue)
	result.Delivery.Outbound = documentFormOutbound(report.Input.Authority, report.OperationID, record, registeredRef)
	result.Delivery.Commit = func(commitCtx context.Context) error {
		if err := tool.advanceDocumentWriteDelivery(
			commitCtx,
			report.Input.Authority,
			report.OperationID,
			document.WriteDeliveryPending,
			toolshared.ToolOutboundDeliveryID(commitCtx),
		); err != nil {
			return err
		}
		return updateDocumentToolDeliveryReport(
			result,
			report,
			record,
			registeredRef,
			document.WriteDeliveryPending,
		)
	}
	result.Delivery.Settle = func(settleCtx context.Context, settlement toolshared.DeliverySettlement) error {
		var target document.WriteOperationState
		switch settlement.Status {
		case toolshared.DeliverySettlementDelivered:
			target = document.WriteDelivered
		case toolshared.DeliverySettlementDefinitelyFailed:
			target = document.WriteDeliveryFailed
		case toolshared.DeliverySettlementAmbiguous:
			target = document.WriteDeliveryAmbiguous
		default:
			return errors.New("unsupported document delivery settlement")
		}
		if err := tool.advanceDocumentWriteDelivery(
			settleCtx,
			report.Input.Authority,
			report.OperationID,
			target,
			settlement.DeliveryID,
		); err != nil {
			return err
		}
		return updateDocumentToolDeliveryReport(result, report, record, registeredRef, target)
	}
	return result
}

func documentFormOutbound(
	owner document.Authority,
	operationID string,
	record document.WriteOperationRecord,
	artifactRef string,
) *toolshared.OutboundDelivery {
	return &toolshared.OutboundDelivery{
		Media: []bus.MediaPart{{
			Type: "file", Ref: artifactRef, Filename: "filled-document.pdf", ContentType: "application/pdf",
		}},
		Recovery: &bus.OutboundRecovery{
			Kind:             bus.OutboundRecoveryDocumentFill,
			MediaRef:         artifactRef,
			WorkspaceID:      owner.WorkspaceID,
			AgentID:          owner.AgentID,
			ActorID:          owner.ActorID,
			RouteID:          owner.RouteID,
			SessionID:        owner.SessionID,
			AuthorityKind:    owner.Kind,
			OperationID:      operationID,
			DomainDeliveryID: record.DeliveryID,
		},
	}
}

func updateDocumentToolDeliveryReport(
	result *toolshared.ToolResult,
	report document.Report,
	record document.WriteOperationRecord,
	artifactRef string,
	state document.WriteOperationState,
) error {
	record.State = state
	updated := documentToolReportResultWithDelivery(report, record, artifactRef)
	if updated.IsError {
		return errors.New("document delivery report could not be updated")
	}
	result.ForLLM = updated.ForLLM
	return nil
}

func (tool *DocumentTool) verifyFormWrite(
	ctx context.Context,
	store ownedDocumentMediaStore,
	ref string,
	owner media.MediaOwner,
	args map[string]any,
) *toolshared.ToolResult {
	operationID, _ := args["operation_id"].(string)
	snapshot, report := document.VerifyMedia(ctx, store, ref, owner, document.FormWriteOptions{
		Acquire:     document.AcquireOptions{ScratchRoot: tool.scratchRoot},
		StateRoot:   tool.stateRoot,
		OperationID: strings.TrimSpace(operationID),
	})
	if snapshot != nil {
		defer func() { _ = snapshot.Close() }()
	}
	return documentToolReportResult(report)
}

// resolveRegisteredWriteArtifact admits an output ref for reproducible verify
// calls only when the exact operation journal and current media owner bind it
// to the already verified form generation. This keeps a durable output usable
// across turns in the same routed session without treating arbitrary media refs
// from model history as current-input authority.
func (tool *DocumentTool) resolveRegisteredWriteArtifact(
	ctx context.Context,
	args map[string]any,
) (string, error) {
	ref, _ := args["source"].(string)
	ref = strings.TrimSpace(ref)
	operationID, _ := args["operation_id"].(string)
	operationID = strings.TrimSpace(operationID)
	if !strings.HasPrefix(ref, "media://") || operationID == "" {
		return "", errors.New("registered document reference is invalid")
	}
	store, owner, err := tool.executionAuthority(ctx)
	if err != nil {
		return "", err
	}
	journal, err := tool.documentWriteJournal()
	if err != nil {
		return "", err
	}
	writeOwner := document.Authority{
		Kind:        "inbound_media",
		WorkspaceID: owner.WorkspaceID,
		AgentID:     owner.AgentID,
		ActorID:     owner.ActorID,
		RouteID:     owner.RouteID,
		SessionID:   owner.SessionID,
	}
	record, found, err := journal.Lookup(ctx, operationID, writeOwner)
	if err != nil || !found || record.Artifact == nil || record.ArtifactRef != ref {
		return "", errors.Join(err, errors.New("registered document operation does not match"))
	}
	switch record.State {
	case document.WriteRegistered,
		document.WriteDeliveryPending,
		document.WriteDelivered,
		document.WriteDeliveryFailed,
		document.WriteDeliveryAmbiguous:
	default:
		return "", errors.New("registered document operation is not verifiable")
	}
	if err = verifyRegisteredDocument(store, owner, ref, document.Artifact{
		ContentType: "application/pdf",
		Size:        record.Artifact.Size,
		SHA256:      record.Artifact.SHA256,
	}); err != nil {
		return "", err
	}
	return ref, nil
}

func documentToolWriteOperationID(ctx context.Context) string {
	seed := strings.TrimSpace(toolshared.ToolExecutionID(ctx)) + "\x00" +
		strings.TrimSpace(toolshared.ToolCallID(ctx))
	if seed == "\x00" {
		return document.NewWriteOperationID()
	}
	digest := sha256.Sum256([]byte("mintclaw.document.agent-write.v1\x00" + seed))
	bytes := digest[:16]
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return "document_write_" + strings.ReplaceAll(uuid.Must(uuid.FromBytes(bytes)).String(), "-", "")
}

func (tool *DocumentTool) registerFilledDocument(
	ctx context.Context,
	store idempotentOwnedDocumentMediaStore,
	owner media.MediaOwner,
	snapshot documentArtifactSource,
	report document.Report,
) (string, document.WriteOperationRecord, error) {
	if report.Input == nil || report.Write == nil || len(report.Artifacts) != 1 || report.OperationID == "" {
		return "", document.WriteOperationRecord{}, errors.New("verified document report is incomplete")
	}
	journal, err := tool.documentWriteJournal()
	if err != nil {
		return "", document.WriteOperationRecord{}, err
	}
	record, found, err := journal.Lookup(ctx, report.OperationID, report.Input.Authority)
	if err != nil || !found {
		return "", document.WriteOperationRecord{}, errors.Join(err, document.ErrWriteConflict)
	}
	if record.ArtifactRef != "" {
		if err = verifyRegisteredDocument(store, owner, record.ArtifactRef, report.Artifacts[0]); err != nil {
			return "", document.WriteOperationRecord{}, err
		}
		return record.ArtifactRef, record, nil
	}
	if record.State != document.WriteVerified {
		return "", document.WriteOperationRecord{}, document.ErrWriteConflict
	}
	path, err := copyFilledDocumentArtifactToMediaTemp(snapshot, report.Artifacts[0])
	if err != nil {
		return "", document.WriteOperationRecord{}, err
	}
	ref, err := store.StoreIdempotentOwned(path, media.MediaMeta{
		Filename:      "filled-document.pdf",
		ContentType:   "application/pdf",
		Source:        "tool:document",
		CleanupPolicy: media.CleanupPolicyDeleteOnCleanup,
	}, "document-form-"+record.DeliveryID, record.DeliveryID, owner)
	if err != nil {
		_ = os.Remove(path)
		return "", document.WriteOperationRecord{}, err
	}
	if retainedPath, resolveErr := store.Resolve(ref); resolveErr == nil && retainedPath != path {
		_ = os.Remove(path)
	}
	record, _, err = journal.Transition(ctx, report.OperationID, report.Input.Authority, document.WriteTransition{
		ExpectedRevision: record.Revision,
		State:            document.WriteRegistered,
		ArtifactRef:      ref,
	})
	if err != nil {
		latest, latestFound, lookupErr := journal.Lookup(ctx, report.OperationID, report.Input.Authority)
		if lookupErr == nil && latestFound && latest.ArtifactRef == ref {
			return ref, latest, nil
		}
		return "", document.WriteOperationRecord{}, err
	}
	return ref, record, nil
}

func verifyRegisteredDocument(
	store ownedDocumentMediaStore,
	owner media.MediaOwner,
	ref string,
	artifact document.Artifact,
) error {
	opened, err := store.OpenOwned(ref, owner)
	if err != nil {
		return err
	}
	defer func() { _ = opened.Close() }()
	if opened.Meta.ContentType != "application/pdf" || opened.Identity.Size != artifact.Size ||
		opened.Identity.SHA256 != artifact.SHA256 {
		return errors.New("registered document does not match verified output")
	}
	return nil
}

func (tool *DocumentTool) documentWriteJournal() (*document.WriteJournal, error) {
	if strings.TrimSpace(tool.stateRoot) == "" {
		return nil, document.ErrWriteJournalFailed
	}
	return document.NewWriteJournal(filepath.Join(tool.stateRoot, "journal"))
}

func (tool *DocumentTool) advanceDocumentWriteDelivery(
	ctx context.Context,
	owner document.Authority,
	operationID string,
	target document.WriteOperationState,
	outboxDeliveryID string,
) error {
	journal, err := tool.documentWriteJournal()
	if err != nil {
		return err
	}
	outboxDeliveryID = strings.TrimSpace(outboxDeliveryID)
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), documentDeliveryCommitTimeout)
	defer cancel()
	for range 4 {
		record, found, lookupErr := journal.Lookup(commitCtx, operationID, owner)
		if lookupErr != nil || !found {
			return errors.Join(lookupErr, document.ErrWriteConflict)
		}
		if target != document.WriteDeliveryPending && record.OutboxDeliveryID != outboxDeliveryID {
			return document.ErrWriteConflict
		}
		if documentDeliveryTransitionAlreadySatisfied(record.State, target) {
			if outboxDeliveryID != "" && record.OutboxDeliveryID != outboxDeliveryID {
				return document.ErrWriteConflict
			}
			return nil
		}
		transition := document.WriteTransition{ExpectedRevision: record.Revision, State: target}
		switch target {
		case document.WriteDeliveryPending:
			transition.OutboxDeliveryID = outboxDeliveryID
		case document.WriteDeliveryFailed:
			transition.FailureCode = document.FailureDeliveryFailed
		case document.WriteDeliveryAmbiguous:
			transition.FailureCode = document.FailureDeliveryAmbiguous
		}
		if _, _, err = journal.Transition(commitCtx, operationID, owner, transition); err == nil {
			return nil
		} else if !errors.Is(err, document.ErrWriteConflict) {
			return err
		}
	}
	return document.ErrWriteConflict
}

func (tool *DocumentTool) reconcileDocumentWriteDelivery(
	ctx context.Context,
	owner document.Authority,
	operationID string,
	record document.WriteOperationRecord,
) (document.WriteOperationRecord, error) {
	if record.State != document.WriteDeliveryPending || record.OutboxDeliveryID == "" || tool.deliveryState == nil {
		return record, nil
	}
	inspection, err := tool.deliveryState(record.OutboxDeliveryID)
	if err != nil {
		return document.WriteOperationRecord{}, err
	}
	intent := inspection.Intent
	if !tool.documentOutboxIntentMatches(record, owner, operationID, intent) {
		return document.WriteOperationRecord{}, document.ErrWriteConflict
	}
	target, terminal, err := documentWriteDeliveryTarget(inspection)
	if err != nil {
		return document.WriteOperationRecord{}, err
	}
	if !terminal {
		return record, nil
	}
	if err = tool.advanceDocumentWriteDelivery(
		ctx,
		owner,
		operationID,
		target,
		intent.ID,
	); err != nil {
		return document.WriteOperationRecord{}, err
	}
	journal, err := tool.documentWriteJournal()
	if err != nil {
		return document.WriteOperationRecord{}, err
	}
	reconciled, found, err := journal.Lookup(ctx, operationID, owner)
	if err != nil || !found {
		return document.WriteOperationRecord{}, errors.Join(err, document.ErrWriteConflict)
	}
	return reconciled, nil
}

func documentWriteDeliveryTarget(
	inspection outbox.DeliveryInspection,
) (document.WriteOperationState, bool, error) {
	intent := inspection.Intent
	switch intent.Status {
	case outbox.StatusDelivered:
		return document.WriteDelivered, true, nil
	case outbox.StatusAmbiguous:
		return document.WriteDeliveryAmbiguous, true, nil
	case outbox.StatusAbandoned:
		return document.WriteDeliveryFailed, true, nil
	case outbox.StatusDefinitelyFailed:
		return document.WriteDeliveryFailed, true, nil
	case outbox.StatusPending, outbox.StatusAttempting:
		return "", false, nil
	default:
		return "", false, document.ErrWriteConflict
	}
}

// ReconcileRecoveredDeliveryAdmission verifies that a recovered outbox intent
// still belongs to a pending document operation immediately before replay.
func (tool *DocumentTool) ReconcileRecoveredDeliveryAdmission(
	ctx context.Context,
	intent outbox.Intent,
) (bool, error) {
	owner, operationID, ok := recoveredDocumentDeliveryAuthority(intent)
	if !ok {
		return false, document.ErrWriteConflict
	}
	journal, err := tool.documentWriteJournal()
	if err != nil {
		return false, err
	}
	record, found, err := journal.Lookup(ctx, operationID, owner)
	if err != nil || !found {
		return false, errors.Join(err, document.ErrWriteConflict)
	}
	switch record.State {
	case document.WriteRegistered:
		if !tool.documentOutboxIntentMetadataMatches(record, owner, operationID, intent) ||
			record.OutboxDeliveryID != "" {
			return false, document.ErrWriteConflict
		}
		if err = tool.advanceDocumentWriteDelivery(
			ctx,
			owner,
			operationID,
			document.WriteDeliveryPending,
			intent.ID,
		); err != nil {
			return false, err
		}
		return true, nil
	case document.WriteDeliveryPending:
		if !tool.documentOutboxIntentMatches(record, owner, operationID, intent) {
			return false, document.ErrWriteConflict
		}
		return true, nil
	case document.WriteDelivered, document.WriteDeliveryFailed, document.WriteDeliveryAmbiguous:
		if !tool.documentOutboxIntentMatches(record, owner, operationID, intent) {
			return false, document.ErrWriteConflict
		}
		return false, nil
	default:
		return false, document.ErrWriteConflict
	}
}

// SettleRecoveredDelivery advances a pending document operation from the exact
// terminal outbox intent observed after gateway restart recovery.
func (tool *DocumentTool) SettleRecoveredDelivery(ctx context.Context, intent outbox.Intent) error {
	owner, operationID, ok := recoveredDocumentDeliveryAuthority(intent)
	if !ok {
		return document.ErrWriteConflict
	}
	journal, err := tool.documentWriteJournal()
	if err != nil {
		return err
	}
	record, found, err := journal.Lookup(ctx, operationID, owner)
	if err != nil || !found {
		return errors.Join(err, document.ErrWriteConflict)
	}
	if !tool.documentOutboxIntentMatches(record, owner, operationID, intent) {
		return document.ErrWriteConflict
	}
	target, terminal, err := documentWriteDeliveryTarget(outbox.DeliveryInspection{Intent: intent})
	if err != nil || !terminal {
		return err
	}
	return tool.advanceDocumentWriteDelivery(ctx, owner, operationID, target, intent.ID)
}

func recoveredDocumentDeliveryAuthority(intent outbox.Intent) (document.Authority, string, bool) {
	if intent.Media == nil || intent.Media.Recovery == nil ||
		intent.Media.Recovery.Kind != bus.OutboundRecoveryDocumentFill {
		return document.Authority{}, "", false
	}
	recovery := intent.Media.Recovery
	return document.Authority{
		Kind:        recovery.AuthorityKind,
		WorkspaceID: recovery.WorkspaceID,
		AgentID:     recovery.AgentID,
		ActorID:     recovery.ActorID,
		RouteID:     recovery.RouteID,
		SessionID:   recovery.SessionID,
	}, recovery.OperationID, true
}

func (tool *DocumentTool) documentOutboxIntentMatches(
	record document.WriteOperationRecord,
	owner document.Authority,
	operationID string,
	intent outbox.Intent,
) bool {
	return intent.ID == record.OutboxDeliveryID &&
		tool.documentOutboxIntentMetadataMatches(record, owner, operationID, intent)
}

func (tool *DocumentTool) documentOutboxIntentMetadataMatches(
	record document.WriteOperationRecord,
	owner document.Authority,
	operationID string,
	intent outbox.Intent,
) bool {
	if intent.Identity.Kind != outbox.KindMedia || intent.Media == nil ||
		(tool.workspace != "" && intent.OwnerWorkspace != tool.workspace) || len(intent.Media.Parts) != 1 {
		return false
	}
	recovery := intent.Media.Recovery
	if recovery == nil || recovery.Kind != bus.OutboundRecoveryDocumentFill ||
		recovery.MediaRef != record.ArtifactRef || recovery.OperationID != operationID ||
		recovery.DomainDeliveryID != record.DeliveryID || recovery.AuthorityKind != owner.Kind ||
		recovery.WorkspaceID != owner.WorkspaceID || recovery.AgentID != owner.AgentID ||
		recovery.ActorID != owner.ActorID || recovery.RouteID != owner.RouteID ||
		recovery.SessionID != owner.SessionID {
		return false
	}
	part := intent.Media.Parts[0]
	return part.Ref == record.ArtifactRef && part.Type == "file" &&
		part.ContentType == "application/pdf" && part.Filename == "filled-document.pdf"
}

func documentDeliveryTransitionAlreadySatisfied(
	current document.WriteOperationState,
	target document.WriteOperationState,
) bool {
	if current == target {
		return true
	}
	return target == document.WriteDeliveryPending &&
		(current == document.WriteDelivered || current == document.WriteDeliveryFailed ||
			current == document.WriteDeliveryAmbiguous)
}

func copyFilledDocumentArtifactToMediaTemp(
	snapshot documentArtifactSource,
	artifact document.Artifact,
) (string, error) {
	if artifact.Kind != "filled_pdf_candidate" || artifact.ContentType != "application/pdf" ||
		artifact.Size <= 0 || artifact.Size > document.DefaultMaxArtifactBytes || len(artifact.Pages) == 0 {
		return "", errors.New("filled document artifact descriptor is invalid")
	}
	input, err := snapshot.OpenArtifact(artifact.Ref)
	if err != nil {
		return "", err
	}
	defer func() { _ = input.Close() }()
	if err = os.MkdirAll(media.TempDir(), 0o700); err != nil {
		return "", err
	}
	output, err := os.CreateTemp(media.TempDir(), ".document-filled-*.pdf")
	if err != nil {
		return "", err
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
		return "", err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(output, hash), io.LimitReader(input, artifact.Size+1))
	if copyErr != nil || written != artifact.Size || hex.EncodeToString(hash.Sum(nil)) != artifact.SHA256 {
		return "", errors.New("filled document bytes do not match the verified descriptor")
	}
	var trailing [1]byte
	if count, readErr := input.Read(trailing[:]); count != 0 || (readErr != nil && !errors.Is(readErr, io.EOF)) {
		return "", errors.New("filled document exceeds the verified descriptor")
	}
	if err = output.Sync(); err != nil {
		return "", err
	}
	if err = output.Close(); err != nil {
		return "", err
	}
	remove = false
	return path, nil
}

func documentToolReportResultWithDelivery(
	report document.Report,
	record document.WriteOperationRecord,
	artifactRef string,
) *toolshared.ToolResult {
	projection := safeDocumentReportFromReport(report)
	projection.Delivery = &safeDocumentDelivery{
		State: record.State, DeliveryID: record.DeliveryID, ArtifactRef: artifactRef,
	}
	if len(projection.Artifacts) == 1 {
		projection.Artifacts[0].Ref = artifactRef
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		return documentToolFailure(
			report.Operation,
			document.StateFailed,
			document.FailureInternal,
			"document report could not be encoded",
		).WithError(err)
	}
	return &toolshared.ToolResult{ForLLM: string(encoded)}
}

func documentFormDeliverable(
	report document.Report,
	record document.WriteOperationRecord,
	ref string,
) *taskresult.Deliverable {
	return &taskresult.Deliverable{
		Text: "Filled and verified PDF.",
		Artifacts: []taskresult.Artifact{{
			Ref: ref, Kind: "file", Filename: "filled-document.pdf", ContentType: "application/pdf",
		}},
		Metadata: map[string]string{
			"operation":            "fill",
			"operation_id":         report.OperationID,
			"document_delivery_id": record.DeliveryID,
			"source_sha256":        report.Write.SourceSHA256,
			"request_sha256":       report.Write.RequestSHA256,
			"output_sha256":        report.Write.OutputSHA256,
		},
	}
}

func safeDocumentReportFromReport(report document.Report) safeDocumentReport {
	result := documentToolReportResult(report)
	var projection safeDocumentReport
	if result.IsError || json.Unmarshal([]byte(result.ForLLM), &projection) != nil {
		return safeDocumentReport{
			SchemaVersion: document.ReportSchemaVersion,
			OperationID:   report.OperationID,
			Operation:     report.Operation,
			State:         document.StateFailed,
			Failure: &document.Failure{
				Code: document.FailureInternal, Message: "document report could not be projected",
			},
		}
	}
	return projection
}
