package eval

import (
	"os"
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
	operation, err := domain.prepared.observeWriteOperation(domain.operationID, owner)
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
	delivery, err := domain.prepared.observeOutbox(operation.OutboxDeliveryID)
	if err != nil {
		return pdfSettlement{}, err
	}
	if !domain.settledDeliveryMatches(job, operation, owner, delivery) {
		return pdfSettlement{}, ErrPDFTrial
	}
	return pdfSettlement{job: job, approval: approval, operation: operation, delivery: delivery}, nil
}

func (domain *pdfConversation) settledApprovalMatches(record interactions.Record, terminalAt int64) bool {
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
) (document.WriteOperationRecord, error) {
	if !validID(id) {
		return document.WriteOperationRecord{}, ErrPDFTrial
	}
	root, err := os.OpenRoot(trial.home)
	if err != nil {
		return document.WriteOperationRecord{}, ErrPDFTrial
	}
	defer func() { _ = root.Close() }()
	data, err := readPDFNativeFile(root, "state/document-writes/journal/"+id+".json",
		document.DefaultMaxWriteJournalRecord)
	if err != nil {
		return document.WriteOperationRecord{}, err
	}
	defer clear(data)
	if err = trial.scanNativeSurface(data); err != nil {
		return document.WriteOperationRecord{}, err
	}
	record, err := document.InspectWriteOperationSnapshot(data, id, owner)
	if err != nil || record.CreatedAt.Before(trial.started) {
		return document.WriteOperationRecord{}, ErrPDFTrial
	}
	return record, nil
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
