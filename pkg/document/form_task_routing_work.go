package document

import (
	"context"
	"time"
)

// QuestionFormTaskRoute is conservative recovery, never switch authority.
// Exhausted/failed private interpretation retains the request for human choice.
func (store *FormJobStore) QuestionFormTaskRoute(
	ctx context.Context, reference string, revision int64, owner FormJobOwner,
) error {
	return store.updateFormTaskRoute(ctx, reference, owner, func(
		record *FormJobRecord, mode *formIntakeStoredMode, route *formTaskRouteStored,
	) (bool, error) {
		staleReady := route.State == FormTaskReady && formTaskGrantRevision(*route) != record.Revision
		if !formIntakeCapturesRoute(mode) || record.Revision != revision ||
			(route.State != FormTaskPending && route.State != FormTaskVerify && route.State != FormTaskQuestion &&
				(route.State != FormTaskCurrent || route.FieldReference != "") && !staleReady) || hasStagedFormIntake(mode) {
			return false, ErrFormJobConflict
		}
		if route.State == FormTaskQuestion {
			return false, nil
		}
		if staleReady {
			// Retain the payload, but require fresh consent at the current revision.
			route.ConsentAnswerID, route.Question = "", nil
		}
		route.State = FormTaskQuestion
		return true, nil
	})
}

func (store *FormJobStore) HoldFormTaskAnswer(
	ctx context.Context, reference string, owner FormJobOwner,
) error {
	return store.updateFormTaskAnswer(ctx, reference, owner, func(
		_ *FormJobRecord, _ *formIntakeStoredMode, route *formTaskRouteStored, answer *formTaskRouteAnswerStored,
	) (bool, error) {
		if answer.State == "handled" {
			return false, nil
		}
		if answer.State != "committed" || route.State != FormTaskQuestion {
			return false, ErrFormJobConflict
		}
		if answer.Text == "switch_task" || answer.Text == "keep_form" {
			// An expired choice is neither fresh authority nor a form fact.
			answer.Text = ""
		}
		answer.State, route.Question = "handled", nil
		return true, nil
	})
}

// FormTaskRouteWork returns only opaque references. Unknown scope replies are
// not implicitly converted into form facts.
func (store *FormJobStore) FormTaskRouteWork(
	ctx context.Context, jobID string, owner FormJobOwner,
) ([]string, error) {
	var result []string
	err := store.update(ctx, func(snapshot *formJobStoreDocument, _ time.Time) (bool, error) {
		record, key, err := store.authorizedRecord(snapshot, jobID, owner)
		if err != nil {
			return false, err
		}
		defer clear(key)
		source, err := readFormIntakeSource(record, key)
		if err != nil || source.Intake == nil {
			return false, ErrFormJobConflict
		}
		for _, route := range source.Intake.TaskRoutes {
			committed := false
			for _, answer := range route.Answers {
				if answer.State == "committed" {
					result = append(result, formTaskAnswerReference(jobID, route.ID, answer.ID))
					committed = true
				}
			}
			if !committed && formTaskRoutePending(route) {
				result = append(result, formTaskRoutePrefix+jobID+"."+route.ID)
			}
			for _, answer := range route.Answers {
				if answer.ForForm && answer.Text != "" && answer.FieldReference == "" && answer.State == "handled" {
					result = append(result, formTaskAnswerReference(jobID, route.ID, answer.ID))
				}
			}
		}
		return false, nil
	})
	return result, err
}
