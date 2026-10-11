package eval

import (
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

func (domain *pdfConversation) confirmReview(prompt Event) (protocol.MintClawMessage, error) {
	if prompt.Kind != "final" || prompt.AgentID != domain.prepared.agent ||
		prompt.SessionKey != domain.owner.RouteSessionKey || domain.jobID == "" || domain.approvalPrompt != nil ||
		domain.prepared.item.Case.PDF.Output != "required" {
		return protocol.MintClawMessage{}, ErrPDFTrial
	}
	job, err := domain.prepared.observeExistingJob(domain.jobID, domain.owner)
	if err != nil {
		return protocol.MintClawMessage{}, err
	}
	if job.State != document.FormJobReviewReady || job.ReviewRevision != job.Revision ||
		!validDigest(job.ReviewDigest) || !validDigest(job.AssignmentDigest) || len(job.AuditBlockers) != 0 ||
		!domain.outputScopeMatches(job) {
		return protocol.MintClawMessage{}, ErrPDFTrial
	}
	now := time.Now()
	request := protocol.MintClawMessage{
		Type: protocol.TypeMessageSend, ID: uuid.NewString(), SessionID: domain.prepared.sessionID,
		Timestamp: now.UnixMilli(), Payload: map[string]any{
			protocol.PayloadKeyContent: "I confirm the reviewed data. Create and send the reviewed PDF.",
		},
	}
	domain.review, domain.confirmedAt, domain.confirmationRequestID = &job, now, request.ID
	return request, nil
}

func (domain *pdfConversation) outputScopeMatches(job document.FormJobRecord) bool {
	if domain.prepared.item.Case.PDF.Draft {
		return job.OutputScope == document.FormOutputEditableDraft &&
			job.OutputMode == document.FormOutputEditableAcroForm
	}
	return job.OutputScope == "" &&
		(job.OutputMode == "" || job.OutputMode == document.FormOutputEditableAcroForm)
}

func (domain *pdfConversation) approveReviewedWrite(prompt Event) (protocol.MintClawMessage, error) {
	if prompt.Kind != "approval" || domain.review == nil || domain.confirmationRequestID == "" ||
		domain.approvalPrompt != nil || domain.prepared.item.Case.PDF.Output != "required" {
		return protocol.MintClawMessage{}, ErrPDFTrial
	}
	record, err := domain.prepared.observePrompt(prompt)
	if err != nil {
		return protocol.MintClawMessage{}, err
	}
	origin := record.Origin.ExecutionContext
	if origin == nil || origin.MessageID != domain.confirmationRequestID ||
		origin.ClientSessionID != domain.prepared.sessionID || origin.Channel != record.Route.Channel ||
		origin.ChatID != record.Route.ChatID || origin.ChatType != record.Route.ChatType ||
		origin.SenderID != record.Route.SenderID || origin.Account != record.Route.AccountID ||
		origin.TopicID != record.Route.TopicID || origin.SpaceID != record.Route.SpaceID ||
		origin.SpaceType != record.Route.SpaceType {
		return protocol.MintClawMessage{}, ErrPDFTrial
	}
	job, err := domain.prepared.observeExistingJob(domain.jobID, domain.owner)
	if err != nil {
		return protocol.MintClawMessage{}, err
	}
	binding, err := job.CommitBinding()
	if err != nil || job.State != document.FormJobAwaitingApproval ||
		job.Revision != job.ApprovalRevision || !reviewMatchesBinding(*domain.review, binding) ||
		record.CreatedAt < domain.confirmedAt.UnixMilli() || job.UpdatedAt < domain.confirmedAt.UnixMilli() {
		return protocol.MintClawMessage{}, ErrPDFTrial
	}
	root, err := os.OpenRoot(domain.prepared.workspace)
	if err != nil {
		return protocol.MintClawMessage{}, ErrPDFTrial
	}
	key, err := readPDFNativeFile(root, "state/interaction_hmac.key", 32)
	_ = root.Close()
	defer clear(key)
	if err != nil {
		return protocol.MintClawMessage{}, ErrPDFTrial
	}
	matched, err := interactions.VerifyArgumentHash(key, binding.ApprovalArguments(), record.Origin.ArgumentHash)
	if err != nil || !matched {
		return protocol.MintClawMessage{}, ErrPDFTrial
	}
	domain.approvalPrompt, domain.operationID = &prompt, binding.OperationID
	return protocol.MintClawMessage{
		Type: protocol.TypeMessageSend, ID: uuid.NewString(), SessionID: domain.prepared.sessionID,
		Timestamp: time.Now().UnixMilli(), Payload: map[string]any{
			protocol.PayloadKeyContent:            "Allow once",
			protocol.PayloadKeyInteractionChoice:  string(bus.InboundInteractionChoiceAllowOnce),
			protocol.PayloadKeyInteractionShortID: prompt.InteractionShortID,
			protocol.PayloadKeyInteractionPrompt:  prompt.MessageID,
		},
	}, nil
}

// The native binding owns approval shape/digest. This comparison only requires
// that the reviewed fixture, scope and assignment have not changed since the
// separate user confirmation; it does not create or consume native authority.
func reviewMatchesBinding(review document.FormJobRecord, binding document.FormCommitBinding) bool {
	return binding.JobID == review.JobID && binding.OwnerDigest == review.OwnerDigest &&
		binding.ApprovalRevision == review.Revision+1 && binding.ReviewRevision == review.ReviewRevision &&
		binding.SourceDigest == review.SourceDigest && binding.FieldSchemaDigest == review.FieldSchemaDigest &&
		binding.BackendRevision == review.BackendRevision && binding.AuditPolicyRevision == review.AuditPolicyRevision &&
		binding.AssignmentDigest == review.AssignmentDigest && binding.ReviewDigest == review.ReviewDigest &&
		binding.OutputScope == review.OutputScope && binding.OutputMode == review.OutputMode &&
		binding.OutputPolicyRevision == review.FormOutputPolicy() && binding.ExpiresAt == review.ExpiresAt
}
