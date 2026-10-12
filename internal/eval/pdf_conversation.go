package eval

import (
	"context"
	"errors"
	"strings"
	"time"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

// The callback inspects bytes in the client's sole reader before JSON maps can
// discard duplicate keys or response projection can discard auxiliary frames.
func inspectPDFWire(data []byte, facts map[string]string, filter *strings.Replacer) error {
	err := inspectNativeJSON(data, facts, filter)
	if err != nil && !errors.Is(err, ErrPrivateEvidence) {
		return ErrPDFTrial
	}
	return err
}

type pdfConversation struct {
	prepared              *pdfTrial
	jobID                 string
	owner                 document.FormJobOwner
	review                *document.FormJobRecord
	confirmedAt           time.Time
	confirmationRequestID string
	approvalPrompt        *Event
	approvalRecord        *interactions.Record
	approvalRequestID     string
	operationID           string
	artifacts             *pdfArtifactCollector
	censusOwner           *document.Authority
	ordinary              *questionConversation
}

func (domain *pdfConversation) initialTask() string { return domain.prepared.task }

func (domain *pdfConversation) request(
	ctx context.Context, action Action, prompt Event,
) (protocol.MintClawMessage, bool, error) {
	if action.Kind == "confirm" {
		request, err := domain.confirmReview(prompt)
		return request, true, err
	}
	if action.Kind == "allow_once" {
		request, err := domain.approveReviewedWrite(prompt)
		return request, true, err
	}
	domain.review = nil
	domain.confirmationRequestID = ""
	if action.Kind == "protected_answer" {
		request, err := domain.prepared.protectedRequest(prompt, action.Fact)
		return request, true, err
	}
	if prompt.Kind == "question" || prompt.Kind == "approval" {
		if err := domain.observe(ctx, prompt); err != nil {
			return protocol.MintClawMessage{}, false, err
		}
	}
	if action.Kind == "message" || action.Kind == "finish" {
		for _, line := range strings.Split(action.Text, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "/") {
				return protocol.MintClawMessage{}, false, ErrPDFTrial
			}
		}
	}
	if prompt.Kind == "question" && domain.ordinary != nil {
		return domain.ordinary.request(ctx, action, prompt)
	}
	// No other intent can grant permission or preserve an old confirmation.
	return protocol.MintClawMessage{}, false, nil
}

func (domain *pdfConversation) observe(ctx context.Context, prompt Event) error {
	if prompt.Kind != "question" && prompt.Kind != "approval" {
		return nil
	}
	// The wire prompt can precede the durable delivered/waiting transition.
	// Re-observe reads for one bounded grace period; never replay an input.
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		record, err := domain.prepared.observePrompt(prompt)
		if errors.Is(err, ErrPrivateEvidence) {
			return err
		}
		if err != nil && prompt.Kind == "question" {
			public, publicErr := domain.prepared.observePromptOrigin(prompt, "request_user_input")
			if publicErr == nil && public.ProtectedAnswer == nil {
				ordinary := &questionConversation{
					home: domain.prepared.home, workspace: domain.prepared.workspace,
					sessionID: domain.prepared.sessionID, agent: domain.prepared.agent,
					started: domain.prepared.started, filter: domain.prepared.filter,
					facts: domain.prepared.item.Case.Driver.Facts,
				}
				if ordinaryErr := ordinary.observe(ctx, prompt); ordinaryErr != nil {
					return ordinaryErr
				}
				domain.ordinary = ordinary
				return nil
			}
			if errors.Is(publicErr, ErrPrivateEvidence) {
				return publicErr
			}
		}
		if err == nil {
			domain.ordinary = nil
			if record.Kind == interactions.KindApproval {
				return nil
			}
			job, owner, jobErr := domain.prepared.observeProtectedJob(record)
			if jobErr != nil {
				return jobErr
			}
			if domain.jobID != "" && (domain.jobID != job.JobID || domain.owner != owner) {
				return ErrPDFTrial
			}
			domain.jobID, domain.owner = job.JobID, owner
			if writeOwner, ownerErr := pdfWriteOwner(domain.prepared.workspace, record); ownerErr == nil {
				domain.censusOwner = &writeOwner
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ErrPDFTrial
		case <-ticker.C:
		}
	}
}

func (domain *pdfConversation) observeCancellation(ctx context.Context, prompt, reply Event) error {
	if domain.ordinary == nil {
		return ErrPDFTrial
	}
	return domain.ordinary.observeCancellation(ctx, prompt, reply)
}

func (domain *pdfConversation) conclude(ctx context.Context, trial *Trial) {
	if domain.ordinary != nil {
		domain.ordinary.conclude(ctx, trial)
	}
	trial.PDF = &PDFTrialEvidence{
		Version: Version, SourceSHA256: domain.prepared.item.Case.Fixtures[0].SHA256,
		JobID: domain.jobID, State: "unknown", Cleanup: "owned_fixture_retained; native_settlement_unknown",
	}
	defer domain.concludeNativeCensus(ctx, trial)
	if domain.review != nil {
		trial.PDF.WriteIntent = &PDFWriteIntentEvidence{
			ReviewRevision: domain.review.ReviewRevision, ConfirmationRequestID: domain.confirmationRequestID,
			OperationID: domain.operationID,
		}
		if domain.approvalPrompt != nil {
			trial.PDF.WriteIntent.ApprovalInteractionID = domain.approvalPrompt.InteractionID
		}
	}
	if domain.jobID == "" {
		return
	}
	job, err := domain.prepared.observeExistingJob(domain.jobID, domain.owner)
	if errors.Is(err, ErrPrivateEvidence) {
		trial.Verdict, trial.Code = Fail, "private_native_evidence"
		trial.ExecutionVerdict, trial.ExecutionCode = Fail, trial.Code
		return
	}
	if err == nil && job.SourceDigest == trial.PDF.SourceSHA256 {
		trial.PDF.State = string(job.State)
	}
	if domain.approvalPrompt != nil {
		settlement, settledErr := domain.waitSettlement(ctx)
		if errors.Is(settledErr, ErrPrivateEvidence) {
			trial.Verdict, trial.Code = Fail, "private_native_evidence"
			trial.ExecutionVerdict, trial.ExecutionCode = Fail, trial.Code
			return
		}
		if settledErr == nil {
			operation := settlement.operation
			trial.PDF.Settlement = &PDFSettlementEvidence{
				ApprovalInteractionID: settlement.approval.ID, ApprovalRequestID: domain.approvalRequestID,
				ApprovalConsumedAt: settlement.approval.ApprovalConsumedAt,
				OperationID:        operation.OperationID, OperationRevision: operation.Revision,
				BackendGeneration: operation.BackendGeneration, OutboxDeliveryID: settlement.delivery.ID,
				PlatformMessageIDs: settlement.delivery.PlatformMessageIDs,
				ArtifactRef:        operation.ArtifactRef, ArtifactSHA256: operation.Artifact.SHA256,
				ArtifactSize: operation.Artifact.Size,
			}
			if domain.artifacts != nil {
				domain.artifacts.collect(ctx, domain.prepared, settlement, trial)
			}
		}
	}
	// A native job observation is not an artifact, privacy, no-write or cleanup
	// proof. Required domain checks remain unknown until those witnesses exist.
}
