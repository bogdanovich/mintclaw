package document

import (
	"context"
	"math"
	"strings"
)

const MaxFormTaskAnswerPasses = 2

type FormTaskAnswerDecision struct {
	Reference        string
	Owner            FormJobOwner
	ExpectedRevision int64
	Pass             int
	Action           FormTaskRouteAction
	Evidence         FormIntakeLiteral `json:"-"`
}

func (store *FormJobStore) updateFormTaskAnswer(
	ctx context.Context, reference string, owner FormJobOwner,
	mutate func(*FormJobRecord, *formIntakeStoredMode, *formTaskRouteStored, *formTaskRouteAnswerStored) (bool, error),
) error {
	jobID, routeID, answerID, err := ParseFormTaskRouteAnswerReference(reference)
	if err != nil {
		return err
	}
	return store.updateFormTaskRoute(ctx, formTaskRoutePrefix+jobID+"."+routeID, owner, func(
		record *FormJobRecord, mode *formIntakeStoredMode, route *formTaskRouteStored,
	) (bool, error) {
		for index := range route.Answers {
			if route.Answers[index].ID == answerID {
				return mutate(record, mode, route, &route.Answers[index])
			}
		}
		return false, ErrFormJobNotFound
	})
}

func (store *FormJobStore) ReserveFormTaskAnswerPass(
	ctx context.Context, reference string, owner FormJobOwner,
) (FormTaskRouteSnapshot, error) {
	err := store.updateFormTaskAnswer(ctx, reference, owner, func(
		record *FormJobRecord, mode *formIntakeStoredMode, route *formTaskRouteStored, answer *formTaskRouteAnswerStored,
	) (bool, error) {
		if !formIntakeCapturesRoute(mode) || route.State != FormTaskQuestion || route.Question == nil ||
			answer.Round != route.Question.Round || answer.State != "committed" || answer.Revision != record.Revision ||
			hasStagedFormIntake(mode) {
			return false, ErrFormJobConflict
		}
		if answer.Passes >= MaxFormTaskAnswerPasses {
			return false, ErrFormJobCapacityExceeded
		}
		answer.Passes++
		answer.ReservedRevision = record.Revision
		return true, nil
	})
	if err != nil {
		return FormTaskRouteSnapshot{}, err
	}
	return store.ReadFormTaskRouteAnswer(ctx, reference, owner)
}

func (store *FormJobStore) DecideFormTaskAnswer(
	ctx context.Context, request FormTaskAnswerDecision,
) (FormTaskRouteSnapshot, error) {
	if request.Pass < 1 || request.Pass > MaxFormTaskAnswerPasses || request.ExpectedRevision <= 0 ||
		(request.Action != FormTaskContinue && request.Action != FormTaskAsk && request.Action != FormTaskSwitch &&
			request.Action != FormTaskKeep) {
		return FormTaskRouteSnapshot{}, ErrFormJobAnswerConflict
	}
	digest, err := formJobJSONKeyedDigest(store.profileKey, struct {
		Action     FormTaskRouteAction
		Evidence   string
		Occurrence int
	}{request.Action, request.Evidence.Text, request.Evidence.Occurrence})
	if err != nil {
		return FormTaskRouteSnapshot{}, err
	}
	err = store.updateFormTaskAnswer(ctx, request.Reference, request.Owner, func(
		record *FormJobRecord, mode *formIntakeStoredMode, route *formTaskRouteStored, answer *formTaskRouteAnswerStored,
	) (bool, error) {
		for _, previous := range answer.Decisions {
			if previous.Pass != request.Pass {
				continue
			}
			if previous.Digest != digest {
				return false, ErrFormJobAnswerConflict
			}
			return false, nil
		}
		if !formIntakeCapturesRoute(mode) || route.State != FormTaskQuestion || route.Question == nil ||
			answer.Round != route.Question.Round || answer.State != "committed" ||
			answer.Passes != request.Pass || answer.ReservedRevision != record.Revision ||
			record.Revision != request.ExpectedRevision || hasStagedFormIntake(mode) {
			return false, ErrFormJobConflict
		}
		if request.Action == FormTaskSwitch || request.Action == FormTaskKeep {
			if request.Evidence.Text == "" || request.Evidence.Text != strings.TrimSpace(answer.Text) ||
				request.Evidence.Occurrence != 1 {
				return false, ErrFormJobAnswerConflict
			}
			if _, literalErr := resolveFormIntakeLiteral(answer.Text, request.Evidence); literalErr != nil {
				return false, literalErr
			}
		}
		answer.Decisions = append(answer.Decisions, formTaskRouteStoredDecision{
			Pass: request.Pass, Revision: record.Revision, Action: request.Action, Digest: digest,
		})
		if request.Action == FormTaskContinue {
			answer.ForForm = true
		}
		if request.Action == FormTaskSwitch || request.Action == FormTaskKeep {
			if len(answer.Decisions) == 1 && answer.Passes < MaxFormTaskAnswerPasses {
				return true, nil
			}
			if len(answer.Decisions) == 2 && answer.Decisions[0].Action == request.Action &&
				answer.Decisions[0].Revision == record.Revision {
				if request.Action == FormTaskKeep && record.Revision == math.MaxInt64 {
					return false, ErrFormJobConflict
				}
				answer.ChoiceKind, route.ConsentAnswerID = "private", answer.ID
				if request.Action == FormTaskSwitch {
					answer.Choice, route.State = FormTaskSwitch, FormTaskReady
				} else {
					answer.Choice, route.State, mode.State = FormTaskContinue, FormTaskCurrent, FormIntakeActive
					if strings.TrimSpace(route.Text) == "" {
						route.State, route.MediaRefs = FormTaskKept, nil
					}
					record.Revision++
					invalidateFormIntakeReview(record)
					for index := range route.Answers {
						if route.Answers[index].Text != "" {
							route.Answers[index].ForForm = true
						}
					}
				}
			}
		}
		answer.State, route.Question = "handled", nil
		return true, nil
	})
	if err != nil {
		return FormTaskRouteSnapshot{}, err
	}
	return store.ReadFormTaskRouteAnswer(ctx, request.Reference, request.Owner)
}

func validateFormTaskAnswerDecision(record FormJobRecord, answer formTaskRouteAnswerStored) error {
	if answer.Passes < 0 || answer.Passes > MaxFormTaskAnswerPasses || len(answer.Decisions) > answer.Passes ||
		(answer.Passes == 0 && answer.ReservedRevision != 0) ||
		(answer.Passes > 0 && (answer.ReservedRevision < answer.Revision || answer.ReservedRevision > record.Revision)) {
		return ErrFormJobRecordCorrupt
	}
	lastPass := 0
	for _, decision := range answer.Decisions {
		if decision.Pass <= lastPass || decision.Pass > answer.Passes || decision.Revision < answer.Revision ||
			decision.Revision > record.Revision || !validDocumentDigest(decision.Digest) ||
			(decision.Action != FormTaskContinue && decision.Action != FormTaskAsk && decision.Action != FormTaskSwitch &&
				decision.Action != FormTaskKeep) {
			return ErrFormJobRecordCorrupt
		}
		lastPass = decision.Pass
	}
	switch answer.ChoiceKind {
	case "":
		if answer.Choice != "" {
			return ErrFormJobRecordCorrupt
		}
	case "canonical":
		if answer.Choice == "" || answer.Text != "" {
			return ErrFormJobRecordCorrupt
		}
	case "private":
		want := FormTaskSwitch
		if answer.Choice == FormTaskContinue {
			want = FormTaskKeep
		}
		if answer.Choice == "" || len(answer.Decisions) != 2 || answer.Decisions[0].Action != want ||
			answer.Decisions[1].Action != want || answer.Decisions[0].Revision != answer.Decisions[1].Revision {
			return ErrFormJobRecordCorrupt
		}
	default:
		return ErrFormJobRecordCorrupt
	}
	return nil
}
