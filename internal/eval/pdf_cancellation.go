package eval

import (
	"context"
	"reflect"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

type pdfCancellationObservation struct {
	prompt    Event
	waiting   interactions.Record
	job       document.FormJobRecord
	sentAfter time.Time
	proof     *PDFCancellationEvidence
}

func (domain *pdfConversation) prepareProtectedCancellation(prompt Event) error {
	if domain.protectedCancel != nil || domain.jobID == "" {
		return ErrPDFTrial
	}
	record, err := domain.prepared.observePrompt(prompt)
	if err != nil {
		return err
	}
	origin := record.Origin.ExecutionContext
	if record.Kind != interactions.KindQuestion || record.ProtectedAnswer == nil ||
		record.ProtectedAnswer.Namespace != document.FormProtectedAnswerNamespace || origin == nil ||
		origin.MessageID != prompt.RequestID || !validID(prompt.RequestID) || !validID(record.Origin.TurnID) {
		return ErrPDFTrial
	}
	matchedScope := false
	for _, scope := range prompt.TraceScopes {
		matchedScope = matchedScope || scope.Complete() && scope.Workspace == domain.prepared.workspace &&
			scope.TurnID == record.Origin.TurnID
	}
	job, owner, err := domain.prepared.observeProtectedJob(record)
	if err != nil {
		return err
	}
	if !matchedScope || owner != domain.owner || job.JobID != domain.jobID {
		return ErrPDFTrial
	}
	domain.protectedCancel = &pdfCancellationObservation{
		prompt: prompt, waiting: record, job: job, sentAfter: time.Now(),
	}
	return nil
}

func (domain *pdfConversation) observeProtectedCancellation(_ context.Context, prompt, reply Event) error {
	observation := domain.protectedCancel
	if observation == nil || observation.proof != nil || prompt.MessageID != observation.prompt.MessageID ||
		prompt.InteractionID != observation.waiting.ID || prompt.InteractionShortID != observation.waiting.ShortID ||
		reply.AgentID != domain.prepared.agent || reply.SessionKey != observation.waiting.Route.SessionKey ||
		!boundedIdentity(reply.MessageID, 128) || !validID(reply.RequestID) ||
		reply.Controls != bus.OutboundInteractionControlsRemove || reply.InteractionKind != "question" ||
		reply.InteractionID != "" && reply.InteractionID != observation.waiting.ID {
		return ErrPDFTrial
	}
	current, err := domain.prepared.observeInteraction(observation.waiting.ID)
	if err != nil {
		return err
	}
	before := observation.waiting
	now := time.Now().UnixMilli()
	if current.ShortID != before.ShortID || current.CreatedAt != before.CreatedAt || current.Route != before.Route ||
		current.PromptDeliveryID != before.PromptDeliveryID || current.Kind != before.Kind || current.Answer != nil ||
		!reflect.DeepEqual(current.Origin, before.Origin) || !reflect.DeepEqual(current.Questions, before.Questions) ||
		!reflect.DeepEqual(current.ProtectedAnswer, before.ProtectedAnswer) ||
		current.Status != interactions.StatusCancelled || current.FailureCode != "session_control_stop" ||
		current.Outcome != "" || current.Revision <= before.Revision ||
		current.ResolvedAt < observation.sentAfter.UnixMilli() || current.ResolvedAt > now {
		return ErrPDFTrial
	}
	job, err := domain.prepared.observeExistingJob(domain.jobID, domain.owner)
	if err != nil {
		return err
	}
	if job.State != document.FormJobCanceled || job.Revision <= observation.job.Revision ||
		job.OwnerDigest != observation.job.OwnerDigest || job.CreatedAt != observation.job.CreatedAt ||
		job.FieldSchemaDigest != observation.job.FieldSchemaDigest ||
		job.TerminalAt < observation.sentAfter.UnixMilli() || job.TerminalAt > now ||
		len(job.Fields) != 0 || job.LedgerDigest != "" || job.AssignmentDigest != "" || job.ReviewDigest != "" {
		return ErrPDFTrial
	}
	observation.proof = &PDFCancellationEvidence{
		Question: QuestionCancellationEvidence{
			Version: Version, InteractionID: current.ID, ShortID: current.ShortID,
			PromptMessageID: prompt.MessageID, PromptRequestID: prompt.RequestID,
			PromptDeliveryID: current.PromptDeliveryID, CancelRequestID: reply.RequestID,
			ReplyMessageID: reply.MessageID, AgentID: current.Route.AgentID,
			SessionKey: current.Route.SessionKey, Workspace: domain.prepared.workspace,
			OriginTurnID: current.Origin.TurnID, WaitingRevision: before.Revision,
			TerminalRevision: current.Revision, ResolvedAt: current.ResolvedAt,
		},
		JobWaitingRevision: observation.job.Revision, JobTerminalRevision: job.Revision, JobTerminalAt: job.TerminalAt,
	}
	return nil
}
