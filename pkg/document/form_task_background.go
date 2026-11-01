package document

import (
	"context"
	"math"
)

// Prior focus is derived from earlier settled receipts, not a second mutable
// task registry. It is private classifier context, never assignment authority.
func formTaskPreviousUnrelated(mode *formIntakeStoredMode, before int) bool {
	latest, unrelated := 0, false
	consider := func(order int, other bool) {
		if order > latest && order < before {
			latest, unrelated = order, other
		}
	}
	for index, input := range mode.Inputs {
		if input.State == formIntakeInputCommitted || input.State == formIntakeInputConsumed {
			consider(formIntakeAdmissionOrder(mode, index), false)
		}
	}
	for _, route := range mode.TaskRoutes {
		if route.State == FormTaskGranted {
			consider(route.AdmissionOrder, true)
		} else if route.FieldReference != "" {
			consider(route.AdmissionOrder, false)
		}
		for _, answer := range route.Answers {
			if answer.State != "handled" {
				continue
			}
			if answer.Choice == FormTaskIndependent && answer.Text == "" {
				consider(answer.AdmissionOrder, true)
			} else if answer.FieldReference != "" {
				consider(answer.AdmissionOrder, false)
			}
		}
	}
	return unrelated
}

// GrantFormTaskIndependentReply never releases the route's original request or
// media. Clearing this independently verified reply is the durable once-only
// boundary, just as clearing a granted root prevents task replay.
func (store *FormJobStore) GrantFormTaskIndependentReply(
	ctx context.Context, reference string, owner FormJobOwner,
) (FormTaskHandoff, error) {
	var result FormTaskHandoff
	err := store.updateFormTaskAnswer(ctx, reference, owner, func(
		record *FormJobRecord, mode *formIntakeStoredMode, route *formTaskRouteStored, answer *formTaskRouteAnswerStored,
	) (bool, error) {
		if answer.Choice != FormTaskIndependent || answer.State != "handled" || answer.ChoiceKind != "private" ||
			answer.ForForm || answer.FieldReference != "" {
			return false, ErrFormJobConflict
		}
		if answer.Text == "" {
			result.Progress, result.Reused = formIntakeProgress(*record, mode), true
			return false, nil
		}
		if !formIntakeCapturesRoute(mode) || route.State != FormTaskQuestion || route.ConsentAnswerID != "" ||
			answer.Revision != record.Revision || answer.ReservedRevision != record.Revision ||
			record.Revision == math.MaxInt64 || hasStagedFormIntake(mode) {
			return false, ErrFormJobConflict
		}
		result.Text, answer.Text = answer.Text, ""
		record.Revision++
		invalidateFormIntakeReview(record)
		result.Progress = formIntakeProgress(*record, mode)
		return true, nil
	})
	if err != nil {
		return FormTaskHandoff{}, err
	}
	return result, nil
}
