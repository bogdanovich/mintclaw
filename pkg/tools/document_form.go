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

	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/media"
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
	result.Delivery.Commit = func(commitCtx context.Context) error {
		return tool.advanceDocumentWriteDelivery(
			commitCtx,
			report.Input.Authority,
			report.OperationID,
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
		return tool.advanceDocumentWriteDelivery(
			settleCtx,
			report.Input.Authority,
			report.OperationID,
			target,
		)
	}
	return result
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
) error {
	journal, err := tool.documentWriteJournal()
	if err != nil {
		return err
	}
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), documentDeliveryCommitTimeout)
	defer cancel()
	for range 4 {
		record, found, lookupErr := journal.Lookup(commitCtx, operationID, owner)
		if lookupErr != nil || !found {
			return errors.Join(lookupErr, document.ErrWriteConflict)
		}
		if documentDeliveryTransitionAlreadySatisfied(record.State, target) {
			return nil
		}
		transition := document.WriteTransition{ExpectedRevision: record.Revision, State: target}
		switch target {
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
