package document

import (
	"context"
	"errors"
	"strings"
	"time"
)

type FormDeliveryOutcome string

const (
	FormDeliveryDelivered        FormDeliveryOutcome = "delivered"
	FormDeliveryDefinitelyFailed FormDeliveryOutcome = "definitely_failed"
	FormDeliveryAmbiguous        FormDeliveryOutcome = "ambiguous"

	formJobExpiredDuringCommitFailure = "form_job_expired_during_commit"
)

type FormDeliveryRequest struct {
	JobID       string
	OwnerDigest string
	OperationID string
	ArtifactRef string
	Outcome     FormDeliveryOutcome
}

// AdmitFormDeliveryRecovery checks whether one recovered outbox intent still
// owns an active PDF3 delivery. It accepts only value-free keyed authority.
func (store *FormJobStore) AdmitFormDeliveryRecovery(
	ctx context.Context,
	request FormDeliveryRequest,
) (FormJobRecord, bool, error) {
	if err := validateFormDeliveryRequest(request, false); err != nil {
		return FormJobRecord{}, false, err
	}
	var result FormJobRecord
	publish := false
	err := store.update(ctx, func(document *formJobStoreDocument, _ time.Time) (bool, error) {
		record, found := document.Records[request.JobID]
		if !found {
			return false, ErrFormJobNotFound
		}
		if !storedFormDeliveryMatches(record.Public, request) {
			return false, ErrFormJobUnauthorized
		}
		result = cloneFormJobRecord(record.Public)
		switch record.Public.State {
		case FormJobDelivering:
			publish = true
		case FormJobCompleted, FormJobFailed, FormJobUncertain:
			publish = false
		default:
			return false, ErrFormJobConflict
		}
		return false, nil
	})
	return result, publish, err
}

// SettleFormDelivery closes one delivering job from the exact terminal PDF2
// journal/outbox receipt. Repeated identical settlement is idempotent.
func (store *FormJobStore) SettleFormDelivery(
	ctx context.Context,
	request FormDeliveryRequest,
) (FormJobRecord, error) {
	if err := validateFormDeliveryRequest(request, true); err != nil {
		return FormJobRecord{}, err
	}
	var result FormJobRecord
	err := store.update(ctx, func(document *formJobStoreDocument, now time.Time) (bool, error) {
		record, found := document.Records[request.JobID]
		if !found {
			return false, ErrFormJobNotFound
		}
		if !storedFormDeliveryMatches(record.Public, request) {
			return false, ErrFormJobUnauthorized
		}
		expiredDuringDelivery := record.Public.State == FormJobUncertain &&
			record.Public.FailureCode == formJobExpiredDuringCommitFailure
		if record.Public.State.terminal() && !expiredDuringDelivery {
			if !terminalFormDeliveryMatchesOutcome(record.Public, request.Outcome) {
				return false, ErrFormJobConflict
			}
			result = cloneFormJobRecord(record.Public)
			return false, nil
		}
		if record.Public.State != FormJobDelivering && !expiredDuringDelivery {
			return false, ErrFormJobConflict
		}
		if err := store.settleStoredFormDelivery(&record, request.Outcome, now); err != nil {
			return false, err
		}
		document.Records[request.JobID] = record
		result = cloneFormJobRecord(record.Public)
		return true, nil
	})
	return result, err
}

func (store *FormJobStore) settleStoredFormDelivery(
	record *formJobStoredRecord,
	outcome FormDeliveryOutcome,
	now time.Time,
) error {
	switch outcome {
	case FormDeliveryDelivered:
		store.completeStoredFormCommit(record, now)
	case FormDeliveryDefinitelyFailed:
		store.terminalizeStoredFormCommit(
			record,
			FormJobFailed,
			string(FailureDeliveryFailed),
			now,
		)
	case FormDeliveryAmbiguous:
		store.terminalizeStoredFormCommit(
			record,
			FormJobUncertain,
			string(FailureDeliveryAmbiguous),
			now,
		)
	default:
		return errors.New("document form delivery outcome is invalid")
	}
	return nil
}

func validateFormDeliveryRequest(request FormDeliveryRequest, requireOutcome bool) error {
	if strings.TrimSpace(request.JobID) != request.JobID ||
		strings.TrimSpace(request.OwnerDigest) != request.OwnerDigest ||
		strings.TrimSpace(request.OperationID) != request.OperationID ||
		strings.TrimSpace(request.ArtifactRef) != request.ArtifactRef ||
		request.JobID == "" || len(request.JobID) > maxFormJobIDLength ||
		!validDocumentDigest(request.OwnerDigest) || !validWriteOperationID(request.OperationID) ||
		!validDurableArtifactRef(request.ArtifactRef) {
		return errors.New("document form delivery identity is invalid")
	}
	if !requireOutcome && request.Outcome != "" {
		return errors.New("document form delivery admission has an outcome")
	}
	if requireOutcome {
		switch request.Outcome {
		case FormDeliveryDelivered, FormDeliveryDefinitelyFailed, FormDeliveryAmbiguous:
		default:
			return errors.New("document form delivery outcome is invalid")
		}
	}
	return nil
}

func storedFormDeliveryMatches(record FormJobRecord, request FormDeliveryRequest) bool {
	return constantTimeStringEqual(record.OwnerDigest, request.OwnerDigest) &&
		record.OperationID == request.OperationID && record.ArtifactRef == request.ArtifactRef
}

func terminalFormDeliveryMatchesOutcome(record FormJobRecord, outcome FormDeliveryOutcome) bool {
	switch record.State {
	case FormJobCompleted:
		return outcome == FormDeliveryDelivered
	case FormJobFailed:
		return outcome == FormDeliveryDefinitelyFailed && record.FailureCode == string(FailureDeliveryFailed)
	case FormJobUncertain:
		return outcome == FormDeliveryAmbiguous && record.FailureCode == string(FailureDeliveryAmbiguous)
	default:
		return false
	}
}

func (store *FormJobStore) completeStoredFormCommit(record *formJobStoredRecord, now time.Time) {
	clearStoredFormProtectedMaterial(record)
	record.Public.State = FormJobCompleted
	record.Public.Revision++
	record.Public.UpdatedAt = now.UnixMilli()
	record.Public.TerminalAt = now.UnixMilli()
	record.Public.CleanupAfter = now.Add(store.terminalRetention).UnixMilli()
	record.Public.FailureCode = ""
}

func (store *FormJobStore) expireStoredFormDelivery(record *formJobStoredRecord, now time.Time) {
	clearStoredFormProtectedMaterial(record)
	record.Public.State = FormJobUncertain
	record.Public.Revision++
	record.Public.UpdatedAt = now.UnixMilli()
	record.Public.TerminalAt = now.UnixMilli()
	record.Public.CleanupAfter = now.Add(store.terminalRetention).UnixMilli()
	record.Public.FailureCode = formJobExpiredDuringCommitFailure
}
