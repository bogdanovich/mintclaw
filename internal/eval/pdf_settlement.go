package eval

import (
	"os"
	"reflect"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
)

// These receipts establish native settlement, not PDF contents or whole-runtime
// privacy. Only the independent artifact and raw-surface witnesses can do that.
type pdfSettlement struct {
	job       document.FormJobRecord
	approval  interactions.Record
	operation document.WriteOperationRecord
	delivery  outbox.Intent
	snapshot  pdfOutputSnapshot
}

// Fingerprint the same raw bytes admitted by the native decoders, not a
// later read or re-serialization that could hide a changed terminal receipt.
type pdfOutputSnapshot struct {
	operationID, operationSHA256 string
	deliveryID, deliverySHA256   string
}

func (snapshot pdfOutputSnapshot) matches(inventory *pdfNativeInventory, evidence *PDFSettlementEvidence) bool {
	if inventory == nil || evidence == nil || !validID(snapshot.operationID) || !validID(snapshot.deliveryID) ||
		!validDigest(snapshot.operationSHA256) || !validDigest(snapshot.deliverySHA256) ||
		snapshot.operationID != evidence.OperationID || snapshot.deliveryID != evidence.OutboxDeliveryID {
		return false
	}
	write, delivery := false, false
	for _, file := range inventory.files {
		if file.kind == "write" && file.id == snapshot.operationID {
			write = digest(file.data) == snapshot.operationSHA256
		}
		if file.kind == "outbox" && file.id == snapshot.deliveryID {
			delivery = digest(file.data) == snapshot.deliverySHA256
		}
	}
	return write && delivery
}

func (domain *pdfConversation) observeSettlement() (pdfSettlement, error) {
	if domain.review == nil || domain.approvalRecord == nil || domain.approvalPrompt == nil ||
		domain.approvalRequestID == "" || domain.operationID == "" || domain.confirmedAt.Before(domain.prepared.started) {
		return pdfSettlement{}, ErrPDFTrial
	}
	job, err := domain.prepared.observeExistingJob(domain.jobID, domain.owner)
	if err != nil {
		return pdfSettlement{}, err
	}
	binding, err := job.CommitBinding()
	if err != nil || job.State != document.FormJobCompleted || job.OperationID != domain.operationID ||
		job.TerminalAt < domain.confirmedAt.UnixMilli() || job.TerminalAt > time.Now().UnixMilli() ||
		!reviewMatchesBinding(*domain.review, binding) {
		return pdfSettlement{}, ErrPDFTrial
	}
	approval, err := domain.prepared.observeInteraction(domain.approvalPrompt.InteractionID)
	if err != nil {
		return pdfSettlement{}, err
	}
	if !domain.settledApprovalMatches(approval, job.TerminalAt) {
		return pdfSettlement{}, ErrPDFTrial
	}
	if err = domain.prepared.verifyApprovalBinding(binding, approval.Origin.ArgumentHash); err != nil {
		return pdfSettlement{}, err
	}
	if err = domain.prepared.observePromptDelivery(approval, *domain.approvalPrompt); err != nil {
		return pdfSettlement{}, err
	}
	owner, err := pdfWriteOwner(domain.prepared.workspace, approval)
	if err != nil {
		return pdfSettlement{}, err
	}
	operation, operationSHA256, err := domain.prepared.observeWriteOperation(domain.operationID, owner)
	if err != nil {
		return pdfSettlement{}, err
	}
	if operation.State != document.WriteDelivered || operation.Artifact == nil || operation.Verification == nil ||
		operation.SourceSHA256 != job.SourceDigest || operation.ArtifactRef != job.ArtifactRef ||
		operation.Artifact.SHA256 != job.ArtifactDigest || operation.Artifact.Size > maxPDFBytes ||
		operation.CreatedAt.Before(time.UnixMilli(approval.ApprovalConsumedAt)) ||
		operation.UpdatedAt.After(time.Now()) {
		return pdfSettlement{}, ErrPDFTrial
	}
	delivery, deliverySHA256, err := domain.prepared.observeOutboxSnapshot(operation.OutboxDeliveryID)
	if err != nil {
		return pdfSettlement{}, err
	}
	if !domain.settledDeliveryMatches(job, operation, owner, delivery) {
		return pdfSettlement{}, ErrPDFTrial
	}
	return pdfSettlement{
		job: job, approval: approval, operation: operation, delivery: delivery,
		snapshot: pdfOutputSnapshot{
			operationID: operation.OperationID, operationSHA256: operationSHA256,
			deliveryID: delivery.ID, deliverySHA256: deliverySHA256,
		},
	}, nil
}

func (domain *pdfConversation) settledApprovalMatches(record interactions.Record, terminalAt int64) bool {
	if domain.approvalRecord == nil || domain.approvalRecord.Origin.ExecutionContext == nil ||
		record.Origin.ExecutionContext == nil ||
		!reflect.DeepEqual(record.Origin.ExecutionContext, domain.approvalRecord.Origin.ExecutionContext) {
		return false
	}
	return record.Kind == interactions.KindApproval && record.Status == interactions.StatusResolved &&
		record.Outcome == interactions.OutcomeAllowed && record.ApprovalConsumedAt >= domain.confirmedAt.UnixMilli() &&
		record.ApprovalConsumedAt <= terminalAt && record.ResolvedAt >= record.ApprovalConsumedAt &&
		record.ResolvedAt <= time.Now().UnixMilli() &&
		record.ApprovalConsumedAt < record.ExpiresAt && record.ExpiresAt == domain.approvalRecord.ExpiresAt &&
		record.CreatedAt == domain.approvalRecord.CreatedAt &&
		record.Answer != nil && record.Answer.MessageID == domain.approvalRequestID &&
		record.Answer.ReceivedAt >= domain.confirmedAt.UnixMilli() &&
		record.Answer.ReceivedAt <= record.ApprovalConsumedAt &&
		record.Answer.Text == string(bus.InboundInteractionChoiceAllowOnce) && record.Answer.Choice == "" &&
		!record.Answer.Superseded &&
		record.Answer.ResponseMessageID == domain.approvalPrompt.MessageID &&
		record.Route == domain.approvalRecord.Route && record.Origin.ToolName == "document" &&
		record.Origin.ToolCallID == domain.approvalRecord.Origin.ToolCallID &&
		record.Origin.TurnID == domain.approvalRecord.Origin.TurnID &&
		record.Origin.ArgumentHash == domain.approvalRecord.Origin.ArgumentHash && domain.confirmationMatches(record)
}

func pdfWriteOwner(workspace string, record interactions.Record) (document.Authority, error) {
	if record.Origin.ExecutionContext == nil {
		return document.Authority{}, ErrPDFTrial
	}
	actor := record.Origin.ExecutionContext.ActorID
	if actor == "" {
		actor = record.Route.SenderID
	}
	routeSession := record.Route.RouteSessionKey
	if routeSession == "" {
		routeSession = record.Route.SessionKey
	}
	owner, err := media.NewMediaOwner(workspace, record.Route.AgentID, actor,
		routeSession, record.Route.SessionKey,
		record.Route.Channel, record.Route.ChatID, record.Route.TopicID)
	if err != nil {
		return document.Authority{}, ErrPDFTrial
	}
	return document.Authority{
		Kind: "inbound_media", WorkspaceID: owner.WorkspaceID, AgentID: owner.AgentID,
		ActorID: owner.ActorID, RouteID: owner.RouteID, SessionID: owner.SessionID,
	}, nil
}

func (trial *pdfTrial) observeWriteOperation(
	id string,
	owner document.Authority,
) (document.WriteOperationRecord, string, error) {
	if !validID(id) {
		return document.WriteOperationRecord{}, "", ErrPDFTrial
	}
	root, err := os.OpenRoot(trial.home)
	if err != nil {
		return document.WriteOperationRecord{}, "", ErrPDFTrial
	}
	defer func() { _ = root.Close() }()
	data, err := readPDFNativeFile(root, "state/document-writes/journal/"+id+".json",
		document.DefaultMaxWriteJournalRecord)
	if err != nil {
		return document.WriteOperationRecord{}, "", err
	}
	defer clear(data)
	if err = trial.scanNativeSurface(data); err != nil {
		return document.WriteOperationRecord{}, "", err
	}
	record, err := document.InspectWriteOperationSnapshot(data, id, owner)
	if err != nil || record.CreatedAt.Before(trial.started) {
		return document.WriteOperationRecord{}, "", ErrPDFTrial
	}
	return record, digest(data), nil
}

func (domain *pdfConversation) settledDeliveryMatches(
	job document.FormJobRecord, operation document.WriteOperationRecord, owner document.Authority, intent outbox.Intent,
) bool {
	if intent.Status != outbox.StatusDelivered || !intent.RecoverySettled || intent.TranscriptProjectionPending() ||
		intent.OwnerWorkspace != domain.prepared.workspace || intent.Identity.Kind != outbox.KindMedia ||
		intent.Identity.Channel != domain.owner.Channel || intent.Identity.ChatID != domain.owner.ChatID ||
		intent.Identity.SessionKey != domain.owner.RouteSessionKey || len(intent.PlatformMessageIDs) != 1 ||
		intent.Media == nil || intent.CreatedAt.Before(operation.CreatedAt) || intent.UpdatedAt.After(time.Now()) {
		return false
	}
	message, recovery := intent.Media, intent.Media.Recovery
	return message.AgentID == domain.owner.AgentID && message.SessionKey == domain.owner.RouteSessionKey &&
		message.DeliveryID == intent.ID && len(message.Parts) == 1 && message.Parts[0].Ref == job.ArtifactRef &&
		message.Parts[0].ContentType == "application/pdf" && recovery != nil &&
		recovery.Kind == bus.OutboundRecoveryDocumentFill && recovery.MediaRef == job.ArtifactRef &&
		recovery.OperationID == operation.OperationID && recovery.DomainDeliveryID == operation.DeliveryID &&
		recovery.DomainJobID == job.JobID && recovery.DomainOwnerDigest == job.OwnerDigest &&
		recovery.AuthorityKind == owner.Kind && recovery.WorkspaceID == owner.WorkspaceID &&
		recovery.AgentID == owner.AgentID && recovery.ActorID == owner.ActorID &&
		recovery.RouteID == owner.RouteID && recovery.SessionID == owner.SessionID
}
