package document

import (
	"context"
	"math"
	"slices"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

func openFormIntakeBinding(
	record formJobStoredRecord, key []byte, envelope formJobEnvelope, mode *formIntakeStoredMode,
) (formIntakeBindingPayload, error) {
	var payload formIntakeBindingPayload
	if mode == nil || envelope.Kind != formIntakeBindingKind || openFormJobEnvelope(key, envelope, &payload) != nil ||
		payload.Version != FormIntakeVersion || payload.JobID != record.Public.JobID || payload.JobID != envelope.JobID ||
		payload.ModeID != mode.ID || payload.ModeID != envelope.EventID || payload.SchemaDigest != record.Public.FieldSchemaDigest ||
		payload.ExpectedRevision != envelope.Revision || !validFormIntakeContext(payload.Context) {
		return payload, ErrFormJobRecordCorrupt
	}
	return payload, nil
}

func (sink *FormProtectedAnswerSink) acceptFormIntake(
	ctx context.Context, request interactions.ProtectedAnswerSinkRequest, envelope formJobEnvelope,
) (interactions.ProtectedAnswerReceipt, error) {
	if !validFormIntakeKey(request.MessageIdentity) || !validFormIntakeKey(request.InteractionID) ||
		!validFormIntakeText(request.Intent, request.Text) {
		return interactions.ProtectedAnswerReceipt{}, ErrFormJobAnswerConflict
	}
	owner, err := formJobOwnerFromProtectedRequest(request.Workspace, request.Route)
	if err != nil {
		return interactions.ProtectedAnswerReceipt{}, err
	}
	admitted, err := sink.store.admitFormIntake(ctx, owner, envelope.JobID, func(
		record formJobStoredRecord, mode *formIntakeStoredMode,
	) (formIntakeBindingPayload, error) {
		key, keyErr := unwrapFormJobKey(sink.store.profileKey, *record.WrappedKey)
		if keyErr != nil {
			return formIntakeBindingPayload{}, keyErr
		}
		defer clear(key)
		return openFormIntakeBinding(record, key, envelope, mode)
	}, request.MessageIdentity, request.Intent, request.Text, request.InteractionID, true)
	if err != nil {
		return interactions.ProtectedAnswerReceipt{}, err
	}
	return interactions.ProtectedAnswerReceipt{Reference: admitted.Receipt, State: "stored"}, nil
}

func (sink *FormProtectedAnswerSink) commitFormIntake(
	ctx context.Context, request interactions.ProtectedAnswerCommitRequest, envelope formJobEnvelope,
) error {
	owner, err := formJobOwnerFromProtectedRequest(request.Workspace, request.Route)
	if err != nil {
		return err
	}
	jobID, inputID, err := parseFormIntakeReceipt(request.Receipt.Reference)
	if err != nil || jobID != envelope.JobID {
		return ErrFormJobAnswerConflict
	}
	return sink.store.update(ctx, func(snapshot *formJobStoreDocument, now time.Time) (bool, error) {
		record, key, err := sink.store.authorizedRecord(snapshot, jobID, owner)
		if err != nil {
			return false, err
		}
		defer clear(key)
		source, err := readFormIntakeSource(record, key)
		if err != nil {
			return false, err
		}
		origin, err := openFormIntakeBinding(record, key, envelope, source.Intake)
		if err != nil {
			return false, err
		}
		index := formIntakeInputIndex(source.Intake, inputID)
		if index < 0 {
			return false, ErrFormJobConflict
		}
		input := &source.Intake.Inputs[index]
		if input.InteractionID != request.InteractionID || !equalFormIntakeOrigin(input.Origin, origin) {
			return false, ErrFormJobAnswerConflict
		}
		if input.State != formIntakeInputStaged {
			return false, nil
		}
		if !formIntakeMutable(record.Public) || source.Intake.State != FormIntakeActive ||
			record.Public.Revision != origin.ExpectedRevision || record.Public.Revision == math.MaxInt64 {
			return false, ErrFormJobConflict
		}
		input.State, input.CommittedAt = formIntakeInputCommitted, now.UnixMilli()
		record.Public.Revision++
		invalidateFormIntakeReview(&record.Public)
		if err := sink.store.writeFormIntakeSource(&record, key, source, now); err != nil {
			return false, err
		}
		snapshot.Records[record.Public.JobID] = record
		return true, nil
	})
}

func (sink *FormProtectedAnswerSink) discardFormIntake(
	ctx context.Context, request interactions.ProtectedAnswerDiscardRequest, envelope formJobEnvelope,
) error {
	owner, err := formJobOwnerFromProtectedRequest(request.Workspace, request.Route)
	if err != nil {
		return err
	}
	inputID := ""
	if request.Receipt != nil {
		jobID, id, err := parseFormIntakeReceipt(request.Receipt.Reference)
		if err != nil || jobID != envelope.JobID || request.Receipt.State != "stored" {
			return ErrFormJobAnswerConflict
		}
		inputID = id
	}
	return sink.store.update(ctx, func(snapshot *formJobStoreDocument, now time.Time) (bool, error) {
		record, key, err := sink.store.authorizedRecord(snapshot, envelope.JobID, owner)
		if err != nil {
			return false, err
		}
		defer clear(key)
		source, err := readFormIntakeSource(record, key)
		if err != nil {
			return false, err
		}
		origin, err := openFormIntakeBinding(record, key, envelope, source.Intake)
		if err != nil {
			return false, err
		}
		for index, input := range source.Intake.Inputs {
			if input.InteractionID != request.InteractionID || inputID != "" && input.ID != inputID {
				continue
			}
			if !equalFormIntakeOrigin(input.Origin, origin) {
				return false, ErrFormJobAnswerConflict
			}
			if input.State != formIntakeInputStaged ||
				!request.Force && now.Sub(time.UnixMilli(input.CreatedAt)) < protectedAnswerPendingGrace {
				return false, nil
			}
			source.Intake.Inputs = slices.Delete(source.Intake.Inputs, index, index+1)
			if err := sink.store.writeFormIntakeSource(&record, key, source, now); err != nil {
				return false, err
			}
			snapshot.Records[record.Public.JobID] = record
			return true, nil
		}
		return false, nil
	})
}

func (sink *FormProtectedAnswerSink) cancelFormIntake(
	ctx context.Context, request interactions.ProtectedAnswerCancelRequest, envelope formJobEnvelope,
) error {
	owner, err := formJobOwnerFromProtectedRequest(request.Workspace, request.Route)
	if err != nil {
		return err
	}
	return sink.store.update(ctx, func(snapshot *formJobStoreDocument, now time.Time) (bool, error) {
		record, key, err := sink.store.authorizedRecord(snapshot, envelope.JobID, owner)
		if err != nil {
			return false, err
		}
		defer clear(key)
		if record.Public.State == FormJobCanceled {
			return false, nil
		}
		if !formIntakeMutable(record.Public) {
			return false, ErrFormJobConflict
		}
		source, err := readFormIntakeSource(record, key)
		if err != nil {
			return false, err
		}
		if _, err := openFormIntakeBinding(record, key, envelope, source.Intake); err != nil {
			return false, err
		}
		sink.store.eraseStoredRecord(&record, FormJobCanceled, "", now)
		snapshot.Records[record.Public.JobID] = record
		return true, nil
	})
}

// SetFormIntakeState changes conversation lifecycle, never assignment authority.
// Pausing retains pending private text; closing is allowed only when settled.
func (store *FormJobStore) SetFormIntakeState(
	ctx context.Context, jobID, modeID string, revision int64, owner FormJobOwner, state FormIntakeState,
) (FormIntakeProgress, error) {
	if !validFormIntakeJobID(jobID) || !validFormIntakeJobID(modeID) || revision <= 0 ||
		(state != FormIntakeActive && state != FormIntakePaused && state != FormIntakeClosed) {
		return FormIntakeProgress{}, ErrFormJobAnswerConflict
	}
	var result FormIntakeProgress
	err := store.update(ctx, func(snapshot *formJobStoreDocument, now time.Time) (bool, error) {
		record, key, err := store.authorizedRecord(snapshot, jobID, owner)
		if err != nil {
			return false, err
		}
		defer clear(key)
		source, err := readFormIntakeSource(record, key)
		if err != nil {
			return false, err
		}
		if source.Intake == nil || source.Intake.ID != modeID || source.Intake.State == FormIntakeClosed ||
			!formIntakeMutable(
				record.Public,
			) || record.Public.Revision != revision || record.Public.Revision == math.MaxInt64 {
			return false, ErrFormJobConflict
		}
		for _, input := range source.Intake.Inputs {
			if input.State == formIntakeInputStaged ||
				state == FormIntakeClosed && input.State != formIntakeInputConsumed {
				return false, ErrFormJobConflict
			}
		}
		if source.Intake.State == state {
			result = formIntakeProgress(record.Public, source.Intake)
			return false, nil
		}
		source.Intake.State = state
		record.Public.Revision++
		invalidateFormIntakeReview(&record.Public)
		if err := store.writeFormIntakeSource(&record, key, source, now); err != nil {
			return false, err
		}
		snapshot.Records[record.Public.JobID] = record
		result = formIntakeProgress(record.Public, source.Intake)
		return true, nil
	})
	if err != nil {
		return FormIntakeProgress{}, err
	}
	return result, nil
}

// Guard legacy field staging as well: two unrelated admission protocols must
// not compete for the same expected job revision.
func rejectLegacyFormAdmissionDuringIntake(record formJobStoredRecord, key []byte) error {
	if record.Public.State.terminal() {
		return nil
	}
	source, err := readFormIntakeSource(record, key)
	if err != nil {
		return err
	}
	if source.Intake != nil && source.Intake.State != FormIntakeClosed {
		return ErrFormJobConflict
	}
	return nil
}
