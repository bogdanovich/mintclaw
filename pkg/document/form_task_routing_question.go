package document

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

const (
	formTaskQuestionBindingKind = "task_route_binding"
	formTaskAnswerPrefix        = "form_route_answer."
	MaxFormTaskQuestionAnswers  = 4
	// Free-text interpretation is bounded; reserve one final canonical choice
	// so exhaustion never disables the operator's buttons.
	maxFormTaskStoredAnswers = MaxFormTaskQuestionAnswers + 1
	MaxFormTaskAnswerBytes   = 8 * 1024
)

// IsFormTaskRouteQuestionBinding is a routing hint, not an authorization check.
// The protected sink still validates the sealed binding and current native state.
func IsFormTaskRouteQuestionBinding(binding *interactions.ProtectedAnswerBinding) bool {
	if binding == nil || binding.Namespace != FormProtectedAnswerNamespace {
		return false
	}
	envelope, err := decodeFormProtectedBindingEnvelope(binding.Token)
	return err == nil && envelope.Kind == formTaskQuestionBindingKind
}

type formTaskRouteQuestion struct {
	Round    int                                 `json:"round"`
	Revision int64                               `json:"revision"`
	Binding  interactions.ProtectedAnswerBinding `json:"binding"`
}

type formTaskQuestionPayload struct {
	JobID        string `json:"job_id"`
	ModeID       string `json:"mode_id"`
	RouteID      string `json:"route_id"`
	SchemaDigest string `json:"schema_digest"`
	Revision     int64  `json:"revision"`
	Round        int    `json:"round"`
}

type formTaskRouteAnswerStored struct {
	AdmissionOrder   int                           `json:"admission_order"`
	Context          FormIntakeContext             `json:"context"`
	ID               string                        `json:"id"`
	MessageDigest    string                        `json:"message_digest"`
	BodyDigest       string                        `json:"body_digest"`
	InteractionID    string                        `json:"interaction_id"`
	Round            int                           `json:"round"`
	Revision         int64                         `json:"revision"`
	CreatedAt        int64                         `json:"created_at"`
	CommittedAt      int64                         `json:"committed_at,omitempty"`
	State            string                        `json:"state"`
	Text             string                        `json:"text,omitempty"`
	FieldReference   string                        `json:"field_reference,omitempty"`
	ForForm          bool                          `json:"for_form,omitempty"`
	Choice           FormTaskRouteAction           `json:"choice,omitempty"`
	ChoiceKind       string                        `json:"choice_kind,omitempty"`
	Passes           int                           `json:"passes,omitempty"`
	ReservedRevision int64                         `json:"reserved_revision,omitempty"`
	Decisions        []formTaskRouteStoredDecision `json:"decisions,omitempty"`
}

// Answers remain inside the native encrypted source, including when their
// task-selection purpose is unclear. Ordinary history receives only a receipt.
type FormTaskRouteAnswer struct {
	Reference      string              `json:"reference"`
	State          string              `json:"state"`
	Text           string              `json:"-"`
	FieldReference string              `json:"field_reference,omitempty"`
	Choice         FormTaskRouteAction `json:"choice,omitempty"`
	Passes         int                 `json:"-"`
}

func (store *FormJobStore) ReadFormTaskRouteAnswer(
	ctx context.Context, reference string, owner FormJobOwner,
) (FormTaskRouteSnapshot, error) {
	jobID, routeID, answerID, err := ParseFormTaskRouteAnswerReference(reference)
	if err != nil {
		return FormTaskRouteSnapshot{}, err
	}
	var result FormTaskRouteSnapshot
	err = store.updateFormTaskRoute(ctx, formTaskRoutePrefix+jobID+"."+routeID, owner, func(
		record *FormJobRecord, mode *formIntakeStoredMode, route *formTaskRouteStored,
	) (bool, error) {
		for _, answer := range route.Answers {
			if answer.ID != answerID {
				continue
			}
			if answer.State == "staged" {
				return false, ErrFormJobConflict
			}
			result = formTaskRouteSnapshot(*record, mode, *route, false)
			result.Answer = &FormTaskRouteAnswer{
				Reference: reference, State: answer.State, Text: answer.Text,
				FieldReference: answer.FieldReference, Choice: answer.Choice,
				Passes: answer.Passes,
			}
			result.PreviousUnrelated = formTaskPreviousUnrelated(mode, answer.AdmissionOrder)
			return false, nil
		}
		return false, ErrFormJobNotFound
	})
	return result, err
}

// Only exact canonical choices from the protected question bypass private
// interpretation. Ordinary free text is never matched by a phrase heuristic.
func (store *FormJobStore) ApplyFormTaskRouteChoice(
	ctx context.Context, reference string, owner FormJobOwner,
) (FormTaskRouteSnapshot, error) {
	jobID, routeID, answerID, err := ParseFormTaskRouteAnswerReference(reference)
	if err != nil {
		return FormTaskRouteSnapshot{}, err
	}
	var result FormTaskRouteSnapshot
	err = store.updateFormTaskRoute(ctx, formTaskRoutePrefix+jobID+"."+routeID, owner, func(
		record *FormJobRecord, mode *formIntakeStoredMode, route *formTaskRouteStored,
	) (bool, error) {
		index := slices.IndexFunc(
			route.Answers,
			func(answer formTaskRouteAnswerStored) bool { return answer.ID == answerID },
		)
		if index < 0 {
			return false, ErrFormJobNotFound
		}
		answer := &route.Answers[index]
		if answer.State == "handled" && answer.Choice != "" {
			result = formTaskRouteSnapshot(*record, mode, *route, true)
			return false, nil
		}
		if answer.State != "committed" || !formIntakeCapturesRoute(mode) || route.State != FormTaskQuestion ||
			route.Question == nil || answer.Round != route.Question.Round || answer.Revision != record.Revision ||
			record.Revision == math.MaxInt64 || hasStagedFormIntake(mode) {
			return false, ErrFormJobConflict
		}
		switch answer.Text {
		case "switch_task":
			answer.Choice = FormTaskSwitch
			route.State, route.ConsentAnswerID = FormTaskReady, answer.ID
		case "keep_form":
			answer.Choice = FormTaskContinue
			route.State, route.ConsentAnswerID = FormTaskCurrent, answer.ID
			if strings.TrimSpace(route.Text) == "" {
				route.State, route.MediaRefs = FormTaskKept, nil
			}
			mode.State = FormIntakeActive
			record.Revision++
			invalidateFormIntakeReview(record)
			for index := range route.Answers {
				if route.Answers[index].ID != answer.ID && route.Answers[index].Text != "" &&
					route.Answers[index].Choice != FormTaskIndependent {
					route.Answers[index].ForForm = true
				}
			}
		default:
			return false, ErrFormJobAnswerConflict
		}
		answer.State, answer.Text, answer.ChoiceKind, route.Question = "handled", "", "canonical", nil
		result = formTaskRouteSnapshot(*record, mode, *route, false)
		return true, nil
	})
	return result, err
}

func (store *FormJobStore) NewFormTaskRouteQuestion(
	ctx context.Context, reference string, owner FormJobOwner,
) (interactions.ProtectedAnswerBinding, error) {
	var result interactions.ProtectedAnswerBinding
	err := store.updateFormTaskRouteKey(ctx, reference, owner, func(
		record *FormJobRecord, mode *formIntakeStoredMode, route *formTaskRouteStored, key []byte,
	) (bool, error) {
		if route.State != FormTaskQuestion || !formIntakeCapturesRoute(mode) {
			return false, ErrFormJobConflict
		}
		if route.Question != nil && route.Question.Revision == record.Revision {
			result = route.Question.Binding
			return false, nil
		}
		if hasStagedFormIntake(mode) {
			return false, ErrFormJobConflict
		}
		if len(route.Answers) >= maxFormTaskStoredAnswers {
			return false, ErrFormJobCapacityExceeded
		}
		payload := formTaskQuestionPayload{
			JobID: record.JobID, ModeID: mode.ID, RouteID: route.ID,
			SchemaDigest: record.FieldSchemaDigest, Revision: record.Revision,
			Round: min(route.QuestionRound+1, maxFormTaskStoredAnswers),
		}
		envelope, err := sealFormJobEnvelope(key, formJobEnvelope{
			Kind: formTaskQuestionBindingKind, JobID: record.JobID, EventID: route.ID, Revision: record.Revision,
		}, payload, store.random)
		if err != nil {
			return false, err
		}
		encoded, err := json.Marshal(envelope)
		if err != nil {
			return false, ErrFormJobRecordCorrupt
		}
		result = interactions.ProtectedAnswerBinding{
			Namespace: FormProtectedAnswerNamespace, Token: base64.RawURLEncoding.EncodeToString(encoded),
		}
		route.QuestionRound = payload.Round
		route.Question = &formTaskRouteQuestion{Round: payload.Round, Revision: record.Revision, Binding: result}
		return true, nil
	})
	if err != nil {
		return interactions.ProtectedAnswerBinding{}, err
	}
	return result, nil
}

func openFormTaskQuestion(
	record FormJobRecord, mode *formIntakeStoredMode, route *formTaskRouteStored,
	key []byte, envelope formJobEnvelope,
) (formTaskQuestionPayload, error) {
	var payload formTaskQuestionPayload
	if envelope.Kind != formTaskQuestionBindingKind || openFormJobEnvelope(key, envelope, &payload) != nil ||
		payload.JobID != record.JobID || payload.JobID != envelope.JobID || payload.ModeID != mode.ID ||
		payload.RouteID != route.ID || payload.RouteID != envelope.EventID ||
		payload.SchemaDigest != record.FieldSchemaDigest || payload.Revision != envelope.Revision ||
		payload.Round < 1 || payload.Round > route.QuestionRound {
		return payload, ErrFormJobAnswerConflict
	}
	return payload, nil
}

func (sink *FormProtectedAnswerSink) acceptFormTaskQuestion(
	ctx context.Context, request interactions.ProtectedAnswerSinkRequest, envelope formJobEnvelope,
) (interactions.ProtectedAnswerReceipt, error) {
	if request.Intent != interactions.ProtectedAnswerValue || !validFormIntakeKey(request.MessageIdentity) ||
		!validFormIntakeKey(request.InteractionID) || strings.TrimSpace(request.Text) == "" ||
		!utf8.ValidString(request.Text) || len(request.Text) > MaxFormTaskAnswerBytes {
		return interactions.ProtectedAnswerReceipt{}, ErrFormJobAnswerConflict
	}
	owner, err := FormJobOwnerFromProtectedRoute(request.Workspace, request.Route)
	if err != nil {
		return interactions.ProtectedAnswerReceipt{}, err
	}
	reference := formTaskRoutePrefix + envelope.JobID + "." + envelope.EventID
	var result interactions.ProtectedAnswerReceipt
	err = sink.store.updateFormTaskRouteKey(ctx, reference, owner, func(
		record *FormJobRecord, mode *formIntakeStoredMode, route *formTaskRouteStored, key []byte,
	) (bool, error) {
		payload, openErr := openFormTaskQuestion(*record, mode, route, key, envelope)
		if openErr != nil {
			return false, openErr
		}
		identity := keyedDigest(
			sink.store.profileKey,
			"form_task_answer",
			record.JobID,
			route.ID,
			request.MessageIdentity,
		)
		body := keyedDigest(sink.store.profileKey, "form_task_answer_body", request.Text)
		for _, answer := range route.Answers {
			if answer.MessageDigest != identity {
				continue
			}
			if answer.BodyDigest != body || answer.Round != payload.Round ||
				answer.InteractionID != request.InteractionID {
				return false, ErrFormJobAnswerConflict
			}
			result = interactions.ProtectedAnswerReceipt{
				Reference: formTaskAnswerReference(record.JobID, route.ID, answer.ID),
				State:     "stored",
			}
			return false, nil
		}
		if !formIntakeCapturesRoute(mode) || route.State != FormTaskQuestion || route.Question == nil ||
			payload.Round != route.Question.Round || record.Revision != payload.Revision ||
			route.Question.Binding.Token != request.Binding.Token ||
			slices.ContainsFunc(route.Answers, func(answer formTaskRouteAnswerStored) bool {
				return answer.State == "staged" || answer.State == "committed"
			}) {
			return false, ErrFormJobConflict
		}
		canonical := request.Text == "switch_task" || request.Text == "keep_form"
		if len(route.Answers) >= maxFormTaskStoredAnswers ||
			(len(route.Answers) >= MaxFormTaskQuestionAnswers && !canonical) ||
			(!canonical && formIntakeProgress(*record, mode).TaskBytes+len(request.Text) > MaxFormTaskRoutingRetainedBytes) {
			return false, ErrFormJobCapacityExceeded
		}
		answer := formTaskRouteAnswerStored{
			AdmissionOrder: nextFormIntakeAdmissionOrder(mode), Context: cloneFormIntakeContext(route.Context),
			ID: "form_task_answer_" + identity[:32], MessageDigest: identity, BodyDigest: body,
			InteractionID: request.InteractionID, Round: payload.Round, Revision: payload.Revision,
			CreatedAt: sink.store.now().UnixMilli(), State: "staged", Text: request.Text,
		}
		route.Answers = append(route.Answers, answer)
		result = interactions.ProtectedAnswerReceipt{
			Reference: formTaskAnswerReference(record.JobID, route.ID, answer.ID),
			State:     "stored",
		}
		return true, nil
	})
	if err != nil {
		return interactions.ProtectedAnswerReceipt{}, err
	}
	return result, nil
}

// Canonical protocol choices consume the separate bounded answer slots, not
// the private-text allowance. A full retained request must still be answerable.
func formTaskAnswerTextBytes(text string) int {
	if text == "switch_task" || text == "keep_form" {
		return 0
	}
	return len(text)
}

func formTaskAnswerReference(jobID, routeID, answerID string) string {
	return formTaskAnswerPrefix + jobID + "." + routeID + "." + answerID
}

func (sink *FormProtectedAnswerSink) updateFormTaskQuestion(
	ctx context.Context, envelope formJobEnvelope, workspace string, protectedRoute interactions.Route,
	mutate func(*FormJobRecord, *formIntakeStoredMode, *formTaskRouteStored, formTaskQuestionPayload) (bool, error),
) error {
	owner, err := FormJobOwnerFromProtectedRoute(workspace, protectedRoute)
	if err != nil {
		return err
	}
	return sink.store.updateFormTaskRouteKey(
		ctx,
		formTaskRoutePrefix+envelope.JobID+"."+envelope.EventID,
		owner,
		func(record *FormJobRecord, mode *formIntakeStoredMode, route *formTaskRouteStored, key []byte) (bool, error) {
			payload, err := openFormTaskQuestion(*record, mode, route, key, envelope)
			if err != nil {
				return false, err
			}
			return mutate(record, mode, route, payload)
		},
	)
}

func (sink *FormProtectedAnswerSink) commitFormTaskQuestion(
	ctx context.Context, request interactions.ProtectedAnswerCommitRequest, envelope formJobEnvelope,
) error {
	jobID, routeID, answerID, err := ParseFormTaskRouteAnswerReference(request.Receipt.Reference)
	if err != nil || jobID != envelope.JobID || routeID != envelope.EventID {
		return ErrFormJobAnswerConflict
	}
	return sink.updateFormTaskQuestion(ctx, envelope, request.Workspace, request.Route, func(
		record *FormJobRecord, mode *formIntakeStoredMode, route *formTaskRouteStored, payload formTaskQuestionPayload,
	) (bool, error) {
		index := slices.IndexFunc(
			route.Answers,
			func(answer formTaskRouteAnswerStored) bool { return answer.ID == answerID },
		)
		if index < 0 || route.Answers[index].InteractionID != request.InteractionID ||
			route.Answers[index].Round != payload.Round {
			return false, ErrFormJobAnswerConflict
		}
		answer := &route.Answers[index]
		if answer.State != "staged" {
			return false, nil
		}
		if !formIntakeCapturesRoute(mode) || route.State != FormTaskQuestion || route.Question == nil ||
			payload.Round != route.Question.Round || record.Revision != payload.Revision ||
			route.Question.Binding.Token != request.Binding.Token {
			return false, ErrFormJobConflict
		}
		answer.State, answer.CommittedAt = "committed", sink.store.now().UnixMilli()
		return true, nil
	})
}

func (sink *FormProtectedAnswerSink) discardFormTaskQuestion(
	ctx context.Context, request interactions.ProtectedAnswerDiscardRequest, envelope formJobEnvelope,
) error {
	answerID := ""
	if request.Receipt != nil {
		jobID, routeID, id, err := ParseFormTaskRouteAnswerReference(request.Receipt.Reference)
		if err != nil || jobID != envelope.JobID || routeID != envelope.EventID || request.Receipt.State != "stored" {
			return ErrFormJobAnswerConflict
		}
		answerID = id
	}
	return sink.updateFormTaskQuestion(ctx, envelope, request.Workspace, request.Route, func(
		_ *FormJobRecord, _ *formIntakeStoredMode, route *formTaskRouteStored, payload formTaskQuestionPayload,
	) (bool, error) {
		for index, answer := range route.Answers {
			if answer.InteractionID != request.InteractionID || answerID != "" && answer.ID != answerID {
				continue
			}
			if answer.Round != payload.Round {
				return false, ErrFormJobAnswerConflict
			}
			if answer.State != "staged" ||
				!request.Force && sink.store.now().Sub(time.UnixMilli(answer.CreatedAt)) < protectedAnswerPendingGrace {
				return false, nil
			}
			route.Answers = slices.Delete(route.Answers, index, index+1)
			return true, nil
		}
		return false, nil
	})
}

func (sink *FormProtectedAnswerSink) cancelFormTaskQuestion(
	ctx context.Context, request interactions.ProtectedAnswerCancelRequest, envelope formJobEnvelope,
) error {
	return sink.updateFormTaskQuestion(ctx, envelope, request.Workspace, request.Route, func(
		record *FormJobRecord, mode *formIntakeStoredMode, route *formTaskRouteStored, payload formTaskQuestionPayload,
	) (bool, error) {
		if route.State == FormTaskKept {
			return false, nil
		}
		if !formIntakeCapturesRoute(mode) || route.State != FormTaskQuestion || route.Question == nil ||
			payload.Round != route.Question.Round || record.Revision != payload.Revision || record.Revision == math.MaxInt64 ||
			route.Question.Binding.Token != request.Binding.Token {
			return false, ErrFormJobConflict
		}
		// Cancel only this new task. Retain prior committed free-text replies.
		route.State, route.Text, route.MediaRefs, route.Question = FormTaskKept, "", nil, nil
		route.Answers = slices.DeleteFunc(route.Answers, func(answer formTaskRouteAnswerStored) bool {
			return answer.State == "staged" && answer.InteractionID == request.InteractionID
		})
		for index := range route.Answers {
			if !route.Answers[index].ForForm {
				route.Answers[index].Text = ""
			}
		}
		record.Revision++
		invalidateFormIntakeReview(record)
		return true, nil
	})
}

func ParseFormTaskRouteAnswerReference(reference string) (string, string, string, error) {
	parts := strings.Split(reference, ".")
	if len(parts) != 4 || parts[0] != strings.TrimSuffix(formTaskAnswerPrefix, ".") ||
		!validFormTaskRoutingID(parts[1], "form_job_") || !validFormTaskRoutingID(parts[2], "form_route_") ||
		!validFormTaskRoutingID(parts[3], "form_task_answer_") {
		return "", "", "", ErrFormJobAnswerConflict
	}
	return parts[1], parts[2], parts[3], nil
}

func validateStoredFormTaskQuestion(record FormJobRecord, mode *formIntakeStoredMode, route formTaskRouteStored) error {
	if route.QuestionRound < 0 || route.QuestionRound > maxFormTaskStoredAnswers ||
		len(route.Answers) > maxFormTaskStoredAnswers {
		return ErrFormJobRecordCorrupt
	}
	if question := route.Question; question != nil {
		if question.Round != route.QuestionRound || question.Round < 1 || question.Revision < route.Revision ||
			question.Revision > record.Revision || question.Binding.Namespace != FormProtectedAnswerNamespace ||
			len(question.Binding.Actions) != 0 {
			return ErrFormJobRecordCorrupt
		}
		envelope, err := decodeFormProtectedBindingEnvelope(question.Binding.Token)
		if err != nil || envelope.Kind != formTaskQuestionBindingKind || envelope.JobID != record.JobID ||
			envelope.EventID != route.ID || envelope.Revision != question.Revision {
			return ErrFormJobRecordCorrupt
		}
	}
	seen := make(map[string]bool, len(route.Answers))
	for _, answer := range route.Answers {
		if answer.ID != "form_task_answer_"+prefixFormIntakeDigest(answer.MessageDigest) || seen[answer.ID] ||
			!validDocumentDigest(answer.MessageDigest) || !validDocumentDigest(answer.BodyDigest) ||
			!validFormIntakeKey(answer.InteractionID) || answer.Round < 1 || answer.Round > route.QuestionRound ||
			answer.Revision < mode.StartRevision || answer.Revision > record.Revision || answer.CreatedAt <= 0 ||
			!utf8.ValidString(answer.Text) || len(answer.Text) > MaxFormTaskAnswerBytes {
			return ErrFormJobRecordCorrupt
		}
		seen[answer.ID] = true
		if answer.Choice != "" && (answer.State != "handled" ||
			(answer.Choice != FormTaskSwitch && answer.Choice != FormTaskContinue && answer.Choice != FormTaskIndependent)) {
			return ErrFormJobRecordCorrupt
		}
		if answer.Choice == FormTaskIndependent && (answer.ForForm || answer.FieldReference != "" ||
			answer.ChoiceKind != "private" || route.ConsentAnswerID == answer.ID) {
			return ErrFormJobRecordCorrupt
		}
		if err := validateFormTaskAnswerDecision(record, answer); err != nil {
			return err
		}
		if answer.FieldReference != "" {
			jobID, inputID, err := parseFormIntakeReceipt(answer.FieldReference)
			if err != nil || jobID != record.JobID || formIntakeInputIndex(mode, inputID) < 0 ||
				!answer.ForForm || answer.State != "handled" || answer.Text != "" {
				return ErrFormJobRecordCorrupt
			}
		}
		switch answer.State {
		case "staged":
			if answer.CommittedAt != 0 || answer.Text == "" || !formIntakeCapturesRoute(mode) ||
				answer.Revision != record.Revision || route.State != FormTaskQuestion || route.Question == nil ||
				answer.Round != route.Question.Round {
				return ErrFormJobRecordCorrupt
			}
		case "committed", "handled":
			if answer.CommittedAt < answer.CreatedAt {
				return ErrFormJobRecordCorrupt
			}
		default:
			return ErrFormJobRecordCorrupt
		}
	}
	return nil
}
