package document

import (
	"context"
	"errors"
	"path/filepath"
	"time"
)

const formWriteTerminalCommitTimeout = 2 * time.Second

type formWriteOutcome struct {
	State    State
	Record   WriteOperationRecord
	Facts    *FormWriteFacts
	Artifact *Artifact
	Failure  *Failure
}

type formWriteCoordinator struct {
	journal *WriteJournal
	store   *formWriteStore
	worker  FormWriterWorker
}

func newFormWriteCoordinator(root string, worker FormWriterWorker) (*formWriteCoordinator, error) {
	resolved, err := prepareWriteJournalRoot(root)
	if err != nil || worker == nil {
		return nil, ErrWriteJournalFailed
	}
	journal, err := NewWriteJournal(filepath.Join(resolved, "journal"))
	if err != nil {
		return nil, err
	}
	store, err := newFormWriteStore(filepath.Join(resolved, "generations"))
	if err != nil {
		return nil, err
	}
	return &formWriteCoordinator{journal: journal, store: store, worker: worker}, nil
}

func (coordinator *formWriteCoordinator) Fill(
	ctx context.Context,
	operationID string,
	owner Authority,
	snapshot *Snapshot,
	input DocumentRef,
	limits Limits,
	fill NormalizedFillRequest,
) formWriteOutcome {
	if coordinator == nil || coordinator.journal == nil || coordinator.store == nil || coordinator.worker == nil ||
		snapshot == nil || input.Authority != owner || input.SHA256 != fill.SourceSHA256 ||
		!validNormalizedFillRequest(fill) {
		return failedFormWriteOutcome(StateFailed, FailureWriteConflict)
	}
	record, _, err := coordinator.journal.Accept(ctx, operationID, owner, fill)
	if err != nil {
		return journalFormWriteOutcome(err)
	}
	release, err := acquireDocumentJournalFileLock(ctx, coordinator.store.executionLockPath(record.OperationID))
	if err != nil {
		return journalFormWriteOutcome(err)
	}
	defer release()
	record, found, err := coordinator.journal.Lookup(ctx, record.OperationID, owner)
	if err != nil || !found {
		return journalFormWriteOutcome(err)
	}
	generation, generationFound, err := coordinator.store.Load(record.OperationID)
	if err != nil {
		return coordinator.uncertain(ctx, owner, record)
	}

	for {
		switch record.State {
		case WriteAccepted:
			if generationFound {
				return coordinator.uncertain(ctx, owner, record)
			}
			record, err = coordinator.transition(ctx, owner, record, WriteTransition{State: WriteWriting})
			if err != nil {
				return journalFormWriteOutcome(err)
			}
		case WriteWriting:
			if !generationFound {
				result := coordinator.fillCandidate(ctx, record.OperationID, snapshot, input, limits, fill)
				if result.State != StateSucceeded {
					return coordinator.workerFailed(ctx, owner, record, result)
				}
				generation, err = coordinator.store.Commit(record.OperationID, snapshot, result)
				if err != nil {
					if errors.Is(err, ErrWriteConflict) || errors.Is(err, errFormWriteGenerationUncertain) {
						return coordinator.uncertain(ctx, owner, record)
					}
					return coordinator.failed(ctx, owner, record, FailureArtifactRegistration)
				}
				generationFound = true
			}
			artifact := WriteArtifactEvidence{SHA256: generation.Artifact.SHA256, Size: generation.Artifact.Size}
			record, err = coordinator.transition(ctx, owner, record, WriteTransition{
				State: WriteWritten, Artifact: &artifact,
			})
			if err != nil {
				return journalFormWriteOutcome(err)
			}
		case WriteWritten:
			if !generationMatchesRecord(generation, generationFound, record, false) {
				return coordinator.uncertain(ctx, owner, record)
			}
			record, err = coordinator.transition(ctx, owner, record, WriteTransition{State: WriteVerifying})
			if err != nil {
				return journalFormWriteOutcome(err)
			}
		case WriteVerifying:
			if !generationMatchesRecord(generation, generationFound, record, false) {
				return coordinator.uncertain(ctx, owner, record)
			}
			verification := formWriteVerificationEvidence(generation.Facts)
			record, err = coordinator.transition(ctx, owner, record, WriteTransition{
				State: WriteVerified, Verification: &verification,
			})
			if err != nil {
				return journalFormWriteOutcome(err)
			}
		case WriteVerified, WriteRegistered, WriteDeliveryPending, WriteDelivered,
			WriteDeliveryFailed, WriteDeliveryAmbiguous:
			if !generationMatchesRecord(generation, generationFound, record, true) {
				return coordinator.uncertain(ctx, owner, record)
			}
			if err = coordinator.store.Materialize(snapshot, generation); err != nil {
				if errors.Is(err, errFormWriteGenerationUncertain) {
					return coordinator.uncertain(ctx, owner, record)
				}
				return failedRecordedFormWriteOutcome(record, StateFailed, FailureArtifactRegistration)
			}
			facts := generation.Facts
			artifact := generation.Artifact
			return formWriteOutcome{
				State: StateSucceeded, Record: record, Facts: &facts, Artifact: &artifact,
			}
		case WriteCanceled:
			return failedRecordedFormWriteOutcome(record, StateCanceled, record.FailureCode)
		case WriteFailed:
			return failedRecordedFormWriteOutcome(
				record,
				recordedWriteFailureState(record.FailureCode),
				record.FailureCode,
			)
		case WriteUncertain:
			return failedRecordedFormWriteOutcome(record, StateUncertain, record.FailureCode)
		default:
			return coordinator.uncertain(ctx, owner, record)
		}
	}
}

func (coordinator *formWriteCoordinator) fillCandidate(
	ctx context.Context,
	operationID string,
	snapshot *Snapshot,
	input DocumentRef,
	limits Limits,
	fill NormalizedFillRequest,
) WorkerResult {
	result := coordinator.worker.FillCandidate(ctx, snapshot, input, limits, operationID, fill)
	request := newWorkerOperationRequest(input, limits, workerOperationFillCandidate)
	request.OperationID = operationID
	request.Fill = &fill
	if result.State == StateSucceeded && (result.SchemaVersion != WorkerResultSchemaVersion ||
		result.OperationID != operationID || result.Input == nil || *result.Input != request.Input ||
		result.Failure != nil || !validWorkerSuccessPayload(request, result)) {
		return workerFailure(
			operationID,
			StateFailed,
			FailureWorkerProtocol,
			"document worker returned an invalid response",
		)
	}
	if result.State != StateSucceeded && !validFormWriteWorkerFailure(result.State, result.Failure) {
		return workerFailure(
			operationID,
			StateFailed,
			FailureWorkerProtocol,
			"document worker returned an invalid response",
		)
	}
	return result
}

func validFormWriteWorkerFailure(state State, failure *Failure) bool {
	if !validWorkerFailure(state, failure) {
		return false
	}
	return state == StateCanceled || validWriteTerminalFailure(failure.Code)
}

func (coordinator *formWriteCoordinator) workerFailed(
	ctx context.Context,
	owner Authority,
	record WriteOperationRecord,
	result WorkerResult,
) formWriteOutcome {
	failure := safeWorkerFailure(result)
	if result.State == StateCanceled {
		return coordinator.transitionTerminal(ctx, owner, record, WriteCanceled, failure, result.State)
	}
	return coordinator.transitionTerminal(ctx, owner, record, WriteFailed, failure, result.State)
}

func (coordinator *formWriteCoordinator) failed(
	ctx context.Context,
	owner Authority,
	record WriteOperationRecord,
	code FailureCode,
) formWriteOutcome {
	failure := safeFormWriteFailure(code)
	return coordinator.transitionTerminal(ctx, owner, record, WriteFailed, failure, StateFailed)
}

func (coordinator *formWriteCoordinator) uncertain(
	ctx context.Context,
	owner Authority,
	record WriteOperationRecord,
) formWriteOutcome {
	failure := safeFormWriteFailure(FailureRecoveryUncertain)
	return coordinator.transitionTerminal(ctx, owner, record, WriteUncertain, failure, StateUncertain)
}

func (coordinator *formWriteCoordinator) transitionTerminal(
	ctx context.Context,
	owner Authority,
	record WriteOperationRecord,
	state WriteOperationState,
	failure Failure,
	reportState State,
) formWriteOutcome {
	// Once an operation is accepted, its terminal result must survive the
	// request context that canceled the worker. Keep this final journal write
	// bounded so a stuck journal cannot indefinitely retain the caller.
	commitContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), formWriteTerminalCommitTimeout)
	defer cancel()
	next, err := coordinator.transition(commitContext, owner, record, WriteTransition{
		State: state, FailureCode: failure.Code,
	})
	if err != nil {
		return journalFormWriteOutcome(err)
	}
	return formWriteOutcome{State: reportState, Record: next, Failure: &failure}
}

func (coordinator *formWriteCoordinator) transition(
	ctx context.Context,
	owner Authority,
	record WriteOperationRecord,
	transition WriteTransition,
) (WriteOperationRecord, error) {
	transition.ExpectedRevision = record.Revision
	next, _, err := coordinator.journal.Transition(ctx, record.OperationID, owner, transition)
	return next, err
}

func generationMatchesRecord(
	generation formWriteGeneration,
	found bool,
	record WriteOperationRecord,
	requireVerification bool,
) bool {
	if !found || generation.OperationID != record.OperationID || generation.SourceSHA256 != record.SourceSHA256 ||
		generation.RequestSHA256 != record.RequestSHA256 || record.Artifact == nil ||
		record.Artifact.SHA256 != generation.Artifact.SHA256 || record.Artifact.Size != generation.Artifact.Size {
		return false
	}
	if !requireVerification {
		return true
	}
	verification := formWriteVerificationEvidence(generation.Facts)
	return record.Verification != nil && *record.Verification == verification
}

func failedRecordedFormWriteOutcome(
	record WriteOperationRecord,
	state State,
	code FailureCode,
) formWriteOutcome {
	failure := safeFormWriteFailure(code)
	return formWriteOutcome{State: state, Record: record, Failure: &failure}
}

func recordedWriteFailureState(code FailureCode) State {
	switch code {
	case FailureBackendUnavailable, FailureWorkerUnavailable, FailureUnsupportedPlatform:
		return StateUnavailable
	case FailurePasswordRequired, FailureFormNotPresent, FailureAppearanceUnavailable, FailureUnsupportedFeature,
		FailureFormUnsupported, FailureFieldUnsupported:
		return StateUnsupported
	default:
		return StateFailed
	}
}

func journalFormWriteOutcome(err error) formWriteOutcome {
	code := WriteJournalFailureCode(err)
	state := StateFailed
	switch code {
	case FailureCanceled:
		state = StateCanceled
	case FailureRecoveryUncertain:
		state = StateUncertain
	}
	failure := safeFormWriteFailure(code)
	return formWriteOutcome{State: state, Failure: &failure}
}

func failedFormWriteOutcome(state State, code FailureCode) formWriteOutcome {
	failure := safeFormWriteFailure(code)
	return formWriteOutcome{State: state, Failure: &failure}
}

func safeFormWriteFailure(code FailureCode) Failure {
	messages := map[FailureCode]string{
		FailureCanceled:               "document form write was canceled",
		FailureWriteConflict:          "document form write conflicts with durable state",
		FailureWriteFailed:            "document form candidate could not be written",
		FailureJournalFailed:          "document form write journal is unavailable",
		FailureRecoveryUncertain:      "document form write recovery is uncertain",
		FailureArtifactRegistration:   "document form candidate could not be retained",
		FailureBackendUnavailable:     "document form backend is unavailable",
		FailureWorkerUnavailable:      "document worker executable is unavailable",
		FailureUnsupportedPlatform:    "document worker is initially admitted only on linux/amd64",
		FailureWorkerProtocol:         "document worker returned an invalid response",
		FailureWorkerCrashed:          "document worker terminated unexpectedly",
		FailureWorkerOutputLimit:      "document worker exceeded its output limit",
		FailureWorkerTimeout:          "document worker exceeded its runtime limit",
		FailureWorkerInputMismatch:    "immutable snapshot identity did not match the admitted input",
		FailureMalformedPDF:           "PDF structure is malformed or unsupported",
		FailurePasswordRequired:       "document inspection requires a protected password input",
		FailureInspectionLimit:        "document exceeds an inspection limit",
		FailureRenderLimit:            "document rendering exceeded a limit",
		FailureArtifactInvalid:        "document worker artifact validation failed",
		FailureLimitExceeded:          "document operation exceeded a limit",
		FailureUnsupportedFeature:     "document feature is not supported",
		FailureAppearanceUnavailable:  "document form appearance is unavailable",
		FailureFormNotPresent:         "PDF has no AcroForm fields",
		FailureFormUnsupported:        "PDF form is not supported",
		FailureFieldUnsupported:       "PDF contains unsupported form fields",
		FailureFieldNotFound:          "PDF form field was not found",
		FailureFieldAmbiguous:         "PDF form field is ambiguous",
		FailureFieldReadOnly:          "PDF form field is read-only",
		FailureFieldValueInvalid:      "PDF form field value is invalid",
		FailureChoiceInvalid:          "PDF form choice is invalid",
		FailureAppearanceStale:        "document form appearance is stale",
		FailureContentClipped:         "document form content is clipped",
		FailureVerificationStructural: "document form candidate failed structural verification",
		FailureVerificationVisual:     "document form candidate failed visual verification",
		FailureInternal:               "document form write failed",
	}
	message := messages[code]
	if message == "" {
		message = "document form write failed"
	}
	return Failure{Code: code, Message: message}
}
