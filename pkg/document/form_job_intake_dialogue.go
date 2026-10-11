package document

import (
	"context"
	"math"
	"slices"
	"time"
)

// FormIntakePassOutcome reads a committed pass without another private model
// call or extraction reservation at the newer job revision.
func (store *FormJobStore) FormIntakePassOutcome(
	ctx context.Context, reference, idempotencyKey string, owner FormJobOwner,
) (*FormIntakeApplyResult, error) {
	jobID, inputID, err := parseFormIntakeReceipt(reference)
	if err != nil || !validFormIntakeKey(idempotencyKey) {
		return nil, ErrFormJobAnswerConflict
	}
	var result *FormIntakeApplyResult
	err = store.update(ctx, func(snapshot *formJobStoreDocument, _ time.Time) (bool, error) {
		record, key, authErr := store.authorizedRecord(snapshot, jobID, owner)
		if authErr != nil {
			return false, authErr
		}
		defer clear(key)
		source, sourceErr := readFormIntakeSource(record, key)
		if sourceErr != nil {
			return false, sourceErr
		}
		index := formIntakeInputIndex(source.Intake, inputID)
		if index < 0 {
			return false, ErrFormJobNotFound
		}
		input := source.Intake.Inputs[index]
		digest := keyedDigest(store.profileKey, "intake_pass", input.ID, idempotencyKey)
		passIndex := slices.IndexFunc(input.Reservations, func(pass formIntakeStoredPass) bool {
			return constantTimeStringEqual(pass.KeyDigest, digest) ||
				constantTimeStringEqual(pass.GroupDigest, digest) && pass.Outcome != nil
		})
		if passIndex >= 0 && input.Reservations[passIndex].Outcome != nil {
			outcome := formIntakeApplyResult(record.Public, source.Intake, input.Reservations[passIndex].Outcome, true)
			result = &outcome
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ParseFormIntakeReference validates an opaque reference without reading values
// or granting authority. Every protected read still authenticates its owner.
func ParseFormIntakeReference(reference string) (string, string, error) {
	return parseFormIntakeReceipt(reference)
}

// FormIntakeReferences returns at most the existing pending-input capacity.
// Callers must separately perform protected reads; no raw input is projected.
func (store *FormJobStore) FormIntakeReferences(
	ctx context.Context, jobID, modeID string, owner FormJobOwner,
) ([]string, error) {
	var references []string
	err := store.update(ctx, func(snapshot *formJobStoreDocument, _ time.Time) (bool, error) {
		record, key, err := store.authorizedRecord(snapshot, jobID, owner)
		if err != nil {
			return false, err
		}
		defer clear(key)
		source, err := readFormIntakeSource(record, key)
		if err != nil || source.Intake == nil || source.Intake.ID != modeID || source.Intake.State == FormIntakeClosed {
			return false, ErrFormJobConflict
		}
		for _, input := range source.Intake.Inputs {
			if input.State == formIntakeInputCommitted {
				references = append(references, formIntakeReceiptPrefix+jobID+"."+input.ID)
			}
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return references, nil
}

// UpdateFormIntakeContext changes only the agent-owned bounded question/role
// plan. Every admitted input retains its immutable origin; revisions invalidate
// prior output authority. It never supplies, changes or confirms a PDF value.
func (store *FormJobStore) UpdateFormIntakeContext(
	ctx context.Context, request FormIntakeStartRequest, modeID string,
) (FormIntakeProgress, error) {
	if !validFormIntakeJobID(request.JobID) || !validFormIntakeJobID(modeID) ||
		request.ExpectedRevision <= 0 || !validFormFieldsFacts(request.Schema) || !validFormIntakeContext(request.Context) {
		return FormIntakeProgress{}, ErrFormJobAnswerConflict
	}
	if _, err := formIntakeCandidates(request.Schema, request.Context.CandidateFieldIDs); err != nil {
		return FormIntakeProgress{}, err
	}
	var result FormIntakeProgress
	err := store.update(ctx, func(snapshot *formJobStoreDocument, now time.Time) (bool, error) {
		record, key, err := store.authorizedRecord(snapshot, request.JobID, request.Owner)
		if err != nil {
			return false, err
		}
		defer clear(key)
		if err = formJobMatchesSchema(record.Public, request.Schema); err != nil {
			return false, err
		}
		source, err := readFormIntakeSource(record, key)
		if err != nil || source.Intake == nil || source.Intake.ID != modeID ||
			source.Intake.State == FormIntakeClosed || !formIntakeMutable(record.Public) ||
			len(record.PendingEvents) != 0 || record.Public.Revision != request.ExpectedRevision ||
			record.Public.Revision == math.MaxInt64 {
			return false, ErrFormJobConflict
		}
		if err = requireNoStagedFormIntake(record, key); err != nil {
			return false, err
		}
		if equalFormIntakeOrigin(formIntakeBindingPayload{Context: source.Intake.Context},
			formIntakeBindingPayload{Context: request.Context}) {
			result = formIntakeProgress(record.Public, source.Intake)
			return false, nil
		}
		source.Intake.Context = cloneFormIntakeContext(request.Context)
		record.Public.Revision++
		invalidateFormIntakeReview(&record.Public)
		if err = store.writeFormIntakeSource(&record, key, source, now); err != nil {
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
