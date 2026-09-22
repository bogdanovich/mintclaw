package tools

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

const documentDeliveryCommitTimeout = 2 * time.Second

var errUnsupportedDocumentDeliverySettlement = errors.New("unsupported document delivery settlement")

type documentDeliveryOutcome struct {
	writeState  document.WriteOperationState
	formOutcome document.FormDeliveryOutcome
	failureCode document.FailureCode
	terminal    bool
}

type documentDeliveryBinding struct {
	owner       document.Authority
	operationID string
	form        *document.FormDeliveryRequest
}

type documentDeliveryCoordinator struct {
	journal       func() (*document.WriteJournal, error)
	inspect       documentDeliveryInspector
	formJobs      *document.FormJobStore
	workspace     string
	retryAttempts int
}

func (tool *DocumentTool) documentDeliveries() documentDeliveryCoordinator {
	return documentDeliveryCoordinator{
		journal:       tool.documentWriteJournal,
		inspect:       tool.deliveryState,
		formJobs:      tool.formJobs,
		workspace:     tool.workspace,
		retryAttempts: 4,
	}
}

func directDocumentDeliveryBinding(
	owner document.Authority,
	operationID string,
) documentDeliveryBinding {
	return documentDeliveryBinding{owner: owner, operationID: operationID}
}

func formDocumentDeliveryBinding(
	owner document.Authority,
	record document.FormJobRecord,
) documentDeliveryBinding {
	request := document.FormDeliveryRequest{
		JobID:       record.JobID,
		OwnerDigest: record.OwnerDigest,
		OperationID: record.OperationID,
		ArtifactRef: record.ArtifactRef,
	}
	return documentDeliveryBinding{owner: owner, operationID: record.OperationID, form: &request}
}

func documentDeliveryOutcomeFromWriteState(
	state document.WriteOperationState,
) (documentDeliveryOutcome, error) {
	switch state {
	case document.WriteDeliveryPending:
		return documentDeliveryOutcome{writeState: state}, nil
	case document.WriteDelivered:
		return documentDeliveryOutcome{
			writeState: state, formOutcome: document.FormDeliveryDelivered, terminal: true,
		}, nil
	case document.WriteDeliveryFailed:
		return documentDeliveryOutcome{
			writeState: state, formOutcome: document.FormDeliveryDefinitelyFailed,
			failureCode: document.FailureDeliveryFailed, terminal: true,
		}, nil
	case document.WriteDeliveryAmbiguous:
		return documentDeliveryOutcome{
			writeState: state, formOutcome: document.FormDeliveryAmbiguous,
			failureCode: document.FailureDeliveryAmbiguous, terminal: true,
		}, nil
	default:
		return documentDeliveryOutcome{}, document.ErrWriteConflict
	}
}

func documentDeliveryOutcomeFromSettlement(
	status toolshared.DeliverySettlementStatus,
) (documentDeliveryOutcome, error) {
	switch status {
	case toolshared.DeliverySettlementDelivered:
		return documentDeliveryOutcomeFromWriteState(document.WriteDelivered)
	case toolshared.DeliverySettlementDefinitelyFailed:
		return documentDeliveryOutcomeFromWriteState(document.WriteDeliveryFailed)
	case toolshared.DeliverySettlementAmbiguous:
		return documentDeliveryOutcomeFromWriteState(document.WriteDeliveryAmbiguous)
	default:
		return documentDeliveryOutcome{}, errUnsupportedDocumentDeliverySettlement
	}
}

func documentDeliveryOutcomeFromOutbox(
	status outbox.Status,
	current document.WriteOperationState,
) (documentDeliveryOutcome, error) {
	switch status {
	case outbox.StatusPending, outbox.StatusAttempting:
		return documentDeliveryOutcomeFromWriteState(document.WriteDeliveryPending)
	case outbox.StatusDelivered:
		return documentDeliveryOutcomeFromWriteState(document.WriteDelivered)
	case outbox.StatusDefinitelyFailed:
		return documentDeliveryOutcomeFromWriteState(document.WriteDeliveryFailed)
	case outbox.StatusAmbiguous:
		return documentDeliveryOutcomeFromWriteState(document.WriteDeliveryAmbiguous)
	case outbox.StatusAbandoned:
		if outcome, err := documentDeliveryOutcomeFromWriteState(current); err == nil && outcome.terminal {
			return outcome, nil
		}
		return documentDeliveryOutcomeFromWriteState(document.WriteDeliveryFailed)
	default:
		return documentDeliveryOutcome{}, document.ErrWriteConflict
	}
}

func (coordinator documentDeliveryCoordinator) commit(
	ctx context.Context,
	binding documentDeliveryBinding,
	outboxDeliveryID string,
) error {
	if binding.form != nil {
		if coordinator.formJobs == nil {
			return document.ErrFormJobStoreUnavailable
		}
		_, publish, err := coordinator.formJobs.AdmitFormDeliveryRecovery(ctx, *binding.form)
		if err != nil {
			return err
		}
		if !publish {
			return document.ErrFormJobTerminal
		}
	}
	pending, err := documentDeliveryOutcomeFromWriteState(document.WriteDeliveryPending)
	if err != nil {
		return err
	}
	return coordinator.transition(ctx, binding, pending, outboxDeliveryID)
}

func (coordinator documentDeliveryCoordinator) settle(
	ctx context.Context,
	binding documentDeliveryBinding,
	outcome documentDeliveryOutcome,
	outboxDeliveryID string,
) (*document.FormJobRecord, error) {
	if !outcome.terminal {
		return nil, document.ErrWriteConflict
	}
	if err := coordinator.transition(ctx, binding, outcome, outboxDeliveryID); err != nil {
		return nil, err
	}
	return coordinator.settleForm(ctx, binding, outcome)
}

func (coordinator documentDeliveryCoordinator) settleForm(
	ctx context.Context,
	binding documentDeliveryBinding,
	outcome documentDeliveryOutcome,
) (*document.FormJobRecord, error) {
	if binding.form == nil {
		return nil, nil
	}
	if coordinator.formJobs == nil {
		return nil, document.ErrFormJobStoreUnavailable
	}
	if !outcome.terminal || outcome.formOutcome == "" {
		return nil, document.ErrWriteConflict
	}
	request := *binding.form
	request.Outcome = outcome.formOutcome
	record, err := coordinator.formJobs.SettleFormDelivery(ctx, request)
	return &record, err
}

func (coordinator documentDeliveryCoordinator) transition(
	ctx context.Context,
	binding documentDeliveryBinding,
	outcome documentDeliveryOutcome,
	outboxDeliveryID string,
) error {
	journal, err := coordinator.journal()
	if err != nil {
		return err
	}
	outboxDeliveryID = strings.TrimSpace(outboxDeliveryID)
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), documentDeliveryCommitTimeout)
	defer cancel()
	attempts := coordinator.retryAttempts
	if attempts < 1 {
		attempts = 1
	}
	for range attempts {
		record, found, lookupErr := journal.Lookup(commitCtx, binding.operationID, binding.owner)
		if lookupErr != nil || !found {
			return errors.Join(lookupErr, document.ErrWriteConflict)
		}
		if outcome.writeState != document.WriteDeliveryPending && record.OutboxDeliveryID != outboxDeliveryID {
			return document.ErrWriteConflict
		}
		if documentDeliveryTransitionAlreadySatisfied(record.State, outcome.writeState) {
			if outboxDeliveryID != "" && record.OutboxDeliveryID != outboxDeliveryID {
				return document.ErrWriteConflict
			}
			return nil
		}
		transition := document.WriteTransition{
			ExpectedRevision: record.Revision,
			State:            outcome.writeState,
			FailureCode:      outcome.failureCode,
		}
		if outcome.writeState == document.WriteDeliveryPending {
			transition.OutboxDeliveryID = outboxDeliveryID
		}
		if _, _, err = journal.Transition(
			commitCtx,
			binding.operationID,
			binding.owner,
			transition,
		); err == nil {
			return nil
		} else if !errors.Is(err, document.ErrWriteConflict) {
			return err
		}
	}
	return document.ErrWriteConflict
}

func (coordinator documentDeliveryCoordinator) reconcile(
	ctx context.Context,
	binding documentDeliveryBinding,
	record document.WriteOperationRecord,
) (document.WriteOperationRecord, error) {
	if record.State != document.WriteDeliveryPending || record.OutboxDeliveryID == "" || coordinator.inspect == nil {
		return record, nil
	}
	inspection, err := coordinator.inspect(record.OutboxDeliveryID)
	if err != nil {
		return document.WriteOperationRecord{}, err
	}
	intent := inspection.Intent
	if !coordinator.intentMatches(record, binding, intent) {
		return document.WriteOperationRecord{}, document.ErrWriteConflict
	}
	outcome, err := documentDeliveryOutcomeFromOutbox(intent.Status, record.State)
	if err != nil {
		return document.WriteOperationRecord{}, err
	}
	if !outcome.terminal {
		return record, nil
	}
	if err = coordinator.transition(ctx, binding, outcome, intent.ID); err != nil {
		return document.WriteOperationRecord{}, err
	}
	journal, err := coordinator.journal()
	if err != nil {
		return document.WriteOperationRecord{}, err
	}
	reconciled, found, err := journal.Lookup(ctx, binding.operationID, binding.owner)
	if err != nil || !found {
		return document.WriteOperationRecord{}, errors.Join(err, document.ErrWriteConflict)
	}
	return reconciled, nil
}

func (coordinator documentDeliveryCoordinator) reconcileRecoveredAdmission(
	ctx context.Context,
	intent outbox.Intent,
) (bool, error) {
	binding, ok := recoveredDocumentDeliveryBinding(intent)
	if !ok {
		return false, document.ErrWriteConflict
	}
	journal, err := coordinator.journal()
	if err != nil {
		return false, err
	}
	record, found, err := journal.Lookup(ctx, binding.operationID, binding.owner)
	if err != nil || !found {
		return false, errors.Join(err, document.ErrWriteConflict)
	}
	if !coordinator.intentMetadataMatches(record, binding, intent) {
		return false, document.ErrWriteConflict
	}
	formPublish := true
	var formRecord document.FormJobRecord
	if binding.form != nil {
		if coordinator.formJobs == nil {
			return false, document.ErrFormJobStoreUnavailable
		}
		formRecord, formPublish, err = coordinator.formJobs.AdmitFormDeliveryRecovery(ctx, *binding.form)
		if err != nil {
			return false, err
		}
	}
	switch record.State {
	case document.WriteRegistered:
		if record.OutboxDeliveryID != "" {
			return false, document.ErrWriteConflict
		}
		pending, outcomeErr := documentDeliveryOutcomeFromWriteState(document.WriteDeliveryPending)
		if outcomeErr != nil {
			return false, outcomeErr
		}
		if err = coordinator.transition(ctx, binding, pending, intent.ID); err != nil {
			return false, err
		}
		return formPublish, nil
	case document.WriteDeliveryPending:
		if !coordinator.intentMatches(record, binding, intent) {
			return false, document.ErrWriteConflict
		}
		return formPublish, nil
	case document.WriteDelivered, document.WriteDeliveryFailed, document.WriteDeliveryAmbiguous:
		if !coordinator.intentMatches(record, binding, intent) {
			return false, document.ErrWriteConflict
		}
		if binding.form != nil {
			binding.form = formDeliveryRequestFromRecord(formRecord)
			outcome, outcomeErr := documentDeliveryOutcomeFromWriteState(record.State)
			if outcomeErr != nil {
				return false, outcomeErr
			}
			if _, err = coordinator.settleForm(ctx, binding, outcome); err != nil {
				return false, err
			}
		}
		return false, nil
	default:
		return false, document.ErrWriteConflict
	}
}

func (coordinator documentDeliveryCoordinator) settleRecovered(
	ctx context.Context,
	intent outbox.Intent,
) error {
	binding, ok := recoveredDocumentDeliveryBinding(intent)
	if !ok {
		return document.ErrWriteConflict
	}
	journal, err := coordinator.journal()
	if err != nil {
		return err
	}
	record, found, err := journal.Lookup(ctx, binding.operationID, binding.owner)
	if err != nil || !found {
		return errors.Join(err, document.ErrWriteConflict)
	}
	if !coordinator.intentMatches(record, binding, intent) {
		return document.ErrWriteConflict
	}
	outcome, err := documentDeliveryOutcomeFromOutbox(intent.Status, record.State)
	if err != nil || !outcome.terminal {
		return err
	}
	_, err = coordinator.settle(ctx, binding, outcome, intent.ID)
	return err
}

func recoveredDocumentDeliveryBinding(intent outbox.Intent) (documentDeliveryBinding, bool) {
	owner, operationID, ok := recoveredDocumentDeliveryAuthority(intent)
	if !ok {
		return documentDeliveryBinding{}, false
	}
	binding := directDocumentDeliveryBinding(owner, operationID)
	request, formRecovery, err := recoveredFormDeliveryRequest(intent)
	if err != nil {
		return documentDeliveryBinding{}, false
	}
	if formRecovery {
		binding.form = &request
	}
	return binding, true
}

func recoveredFormDeliveryRequest(intent outbox.Intent) (document.FormDeliveryRequest, bool, error) {
	if intent.Media == nil || intent.Media.Recovery == nil {
		return document.FormDeliveryRequest{}, false, nil
	}
	recovery := intent.Media.Recovery
	if recovery.DomainJobID == "" && recovery.DomainOwnerDigest == "" {
		return document.FormDeliveryRequest{}, false, nil
	}
	if recovery.DomainJobID == "" || recovery.DomainOwnerDigest == "" {
		return document.FormDeliveryRequest{}, false, document.ErrWriteConflict
	}
	return document.FormDeliveryRequest{
		JobID: recovery.DomainJobID, OwnerDigest: recovery.DomainOwnerDigest,
		OperationID: recovery.OperationID, ArtifactRef: recovery.MediaRef,
	}, true, nil
}

func formDeliveryRequestFromRecord(record document.FormJobRecord) *document.FormDeliveryRequest {
	request := document.FormDeliveryRequest{
		JobID: record.JobID, OwnerDigest: record.OwnerDigest,
		OperationID: record.OperationID, ArtifactRef: record.ArtifactRef,
	}
	return &request
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

func (coordinator documentDeliveryCoordinator) intentMatches(
	record document.WriteOperationRecord,
	binding documentDeliveryBinding,
	intent outbox.Intent,
) bool {
	return intent.ID == record.OutboxDeliveryID && coordinator.intentMetadataMatches(record, binding, intent)
}

func (coordinator documentDeliveryCoordinator) intentMetadataMatches(
	record document.WriteOperationRecord,
	binding documentDeliveryBinding,
	intent outbox.Intent,
) bool {
	if intent.Identity.Kind != outbox.KindMedia || intent.Media == nil ||
		(coordinator.workspace != "" && intent.OwnerWorkspace != coordinator.workspace) || len(intent.Media.Parts) != 1 {
		return false
	}
	recovery := intent.Media.Recovery
	if recovery == nil || recovery.Kind != bus.OutboundRecoveryDocumentFill ||
		recovery.MediaRef != record.ArtifactRef || recovery.OperationID != binding.operationID ||
		recovery.DomainDeliveryID != record.DeliveryID || recovery.AuthorityKind != binding.owner.Kind ||
		recovery.WorkspaceID != binding.owner.WorkspaceID || recovery.AgentID != binding.owner.AgentID ||
		recovery.ActorID != binding.owner.ActorID || recovery.RouteID != binding.owner.RouteID ||
		recovery.SessionID != binding.owner.SessionID || !documentDeliveryFormIdentityMatches(recovery, binding.form) {
		return false
	}
	part := intent.Media.Parts[0]
	return part.Ref == record.ArtifactRef && part.Type == "file" &&
		part.ContentType == "application/pdf" && part.Filename == "filled-document.pdf"
}

func documentDeliveryFormIdentityMatches(
	recovery *bus.OutboundRecovery,
	request *document.FormDeliveryRequest,
) bool {
	if request == nil {
		return recovery.DomainJobID == "" && recovery.DomainOwnerDigest == ""
	}
	return recovery.DomainJobID == request.JobID && recovery.DomainOwnerDigest == request.OwnerDigest
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

// ReconcileRecoveredDeliveryAdmission verifies that a recovered outbox intent
// still belongs to a pending document operation immediately before replay.
func (tool *DocumentTool) ReconcileRecoveredDeliveryAdmission(
	ctx context.Context,
	intent outbox.Intent,
) (bool, error) {
	return tool.documentDeliveries().reconcileRecoveredAdmission(ctx, intent)
}

// SettleRecoveredDelivery advances a pending document operation from the exact
// terminal outbox intent observed after gateway restart recovery.
func (tool *DocumentTool) SettleRecoveredDelivery(ctx context.Context, intent outbox.Intent) error {
	return tool.documentDeliveries().settleRecovered(ctx, intent)
}
