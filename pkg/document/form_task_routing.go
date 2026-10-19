package document

import (
	"context"
	"encoding/hex"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

const (
	MaxFormTaskRoutingPasses        = 4
	MaxFormTaskRoutingMedia         = 10
	MaxFormTaskRoutingRetainedBytes = 64 * 1024
	MaxFormTaskRoutingPending       = 4
	formTaskRoutePrefix             = "form_route."
)

type FormTaskRouteAction string

const (
	FormTaskContinue FormTaskRouteAction = "continue_form"
	FormTaskAsk      FormTaskRouteAction = "ask"
	FormTaskSwitch   FormTaskRouteAction = "switch_task"
	FormTaskKeep     FormTaskRouteAction = "keep_form"
)

type FormTaskRouteState string

const (
	FormTaskPending  FormTaskRouteState = "pending"
	FormTaskVerify   FormTaskRouteState = "verify"
	FormTaskQuestion FormTaskRouteState = "question"
	FormTaskCurrent  FormTaskRouteState = "current"
	FormTaskReady    FormTaskRouteState = "ready"
	FormTaskGranted  FormTaskRouteState = "granted"
	FormTaskKept     FormTaskRouteState = "kept"
)

// A route request is admitted by trusted ingress, never through model arguments.
// Raw input/media are confined to the existing encrypted form source.
type FormTaskRouteRequest struct {
	JobID            string
	ModeID           string
	ExpectedRevision int64
	Owner            FormJobOwner
	MessageIdentity  string
	Text             string   `json:"-"`
	MediaRefs        []string `json:"-"`
}

// FormTaskRouteSnapshot is a bounded private interpretation view. Its JSON
// projection contains only native routing metadata, never the user request.
type FormTaskRouteSnapshot struct {
	HandoffTextBytes int                  `json:"-"`
	Reference        string               `json:"reference"`
	JobID            string               `json:"job_id"`
	ModeID           string               `json:"mode_id"`
	Revision         int64                `json:"revision"`
	AdmittedRevision int64                `json:"-"`
	GrantRevision    int64                `json:"-"`
	State            FormTaskRouteState   `json:"state"`
	Passes           int                  `json:"passes"`
	FieldReference   string               `json:"field_reference,omitempty"`
	Answer           *FormTaskRouteAnswer `json:"-"`
	Reused           bool                 `json:"reused,omitempty"`
	Text             string               `json:"-"`
	MediaRefs        []string             `json:"-"`
	Context          FormIntakeContext    `json:"-"`
}

type FormTaskRouteDecision struct {
	Reference        string
	ExpectedRevision int64
	Owner            FormJobOwner
	Pass             int
	Action           FormTaskRouteAction
	// Switching requires the whole current request as evidence. A second,
	// separately reserved agreeing private pass is required before any grant.
	Evidence FormIntakeLiteral `json:"-"`
}

type FormTaskHandoff struct {
	Progress FormIntakeProgress `json:"progress"`
	Reused   bool               `json:"reused"`
	Text     string             `json:"-"`
	Media    []string           `json:"-"`
}

type formTaskRouteStoredDecision struct {
	Pass     int                 `json:"pass"`
	Revision int64               `json:"revision"`
	Action   FormTaskRouteAction `json:"action"`
	Digest   string              `json:"digest"`
}

type formTaskRouteStored struct {
	AdmissionOrder   int                           `json:"admission_order"`
	Context          FormIntakeContext             `json:"context"`
	ID               string                        `json:"id"`
	MessageDigest    string                        `json:"message_digest"`
	BodyDigest       string                        `json:"body_digest"`
	Revision         int64                         `json:"revision"`
	CreatedAt        int64                         `json:"created_at"`
	State            FormTaskRouteState            `json:"state"`
	Text             string                        `json:"text,omitempty"`
	MediaRefs        []string                      `json:"media_refs,omitempty"`
	Passes           int                           `json:"passes,omitempty"`
	ReservedRevision int64                         `json:"reserved_revision,omitempty"`
	Decisions        []formTaskRouteStoredDecision `json:"decisions,omitempty"`
	FieldReference   string                        `json:"field_reference,omitempty"`
	QuestionRound    int                           `json:"question_round,omitempty"`
	Question         *formTaskRouteQuestion        `json:"question,omitempty"`
	Answers          []formTaskRouteAnswerStored   `json:"answers,omitempty"`
	ConsentAnswerID  string                        `json:"consent_answer_id,omitempty"`
}

func (store *FormJobStore) BeginFormTaskRoute(
	ctx context.Context, request FormTaskRouteRequest,
) (FormTaskRouteSnapshot, error) {
	if !validFormIntakeJobID(request.JobID) || !validFormIntakeJobID(request.ModeID) ||
		request.ExpectedRevision <= 0 || !validFormIntakeKey(request.MessageIdentity) ||
		!validFormTaskRouteBody(request.Text, request.MediaRefs) {
		return FormTaskRouteSnapshot{}, ErrFormJobAnswerConflict
	}
	var result FormTaskRouteSnapshot
	err := store.update(ctx, func(snapshot *formJobStoreDocument, now time.Time) (bool, error) {
		record, key, err := store.authorizedRecord(snapshot, request.JobID, request.Owner)
		if err != nil {
			return false, err
		}
		defer clear(key)
		source, err := readFormIntakeSource(record, key)
		if err != nil || source.Intake == nil || source.Intake.ID != request.ModeID {
			return false, ErrFormJobConflict
		}
		mode := source.Intake
		identity := keyedDigest(store.profileKey, "form_task_route", request.JobID, mode.ID, request.MessageIdentity)
		body, err := store.formTaskRouteBodyDigest(request.Text, request.MediaRefs)
		if err != nil {
			return false, err
		}
		for _, route := range mode.TaskRoutes {
			if route.MessageDigest != identity {
				continue
			}
			if !constantTimeStringEqual(route.BodyDigest, body) {
				return false, ErrFormJobAnswerConflict
			}
			result = formTaskRouteSnapshot(record.Public, mode, route, true)
			return false, nil
		}
		if !formIntakeMutable(record.Public) || !formIntakeCapturesRoute(mode) ||
			record.Public.Revision != request.ExpectedRevision || len(record.PendingEvents) != 0 {
			return false, ErrFormJobConflict
		}
		if len(mode.TaskRoutes) >= MaxFormIntakeReceipts ||
			formIntakeProgress(record.Public, mode).TaskBytes+len(request.Text) > MaxFormTaskRoutingRetainedBytes {
			return false, ErrFormJobCapacityExceeded
		}
		pending := 0
		for _, route := range mode.TaskRoutes {
			if formTaskRoutePending(route) {
				pending++
			}
		}
		if pending >= MaxFormTaskRoutingPending {
			return false, ErrFormJobCapacityExceeded
		}
		route := formTaskRouteStored{
			AdmissionOrder: nextFormIntakeAdmissionOrder(mode), Context: cloneFormIntakeContext(mode.Context),
			ID: "form_route_" + identity[:32], MessageDigest: identity, BodyDigest: body,
			Revision: record.Public.Revision, CreatedAt: now.UnixMilli(), State: FormTaskPending,
			Text: request.Text, MediaRefs: slices.Clone(request.MediaRefs),
		}
		mode.TaskRoutes = append(mode.TaskRoutes, route)
		if err = store.writeFormIntakeSource(&record, key, source, now); err != nil {
			return false, err
		}
		snapshot.Records[record.Public.JobID] = record
		result = formTaskRouteSnapshot(record.Public, mode, route, false)
		return true, nil
	})
	return result, err
}

func (store *FormJobStore) ReadFormTaskRoute(
	ctx context.Context, reference string, owner FormJobOwner,
) (FormTaskRouteSnapshot, error) {
	var result FormTaskRouteSnapshot
	err := store.updateFormTaskRoute(ctx, reference, owner, func(
		record *FormJobRecord, mode *formIntakeStoredMode, route *formTaskRouteStored,
	) (bool, error) {
		result = formTaskRouteSnapshot(*record, mode, *route, false)
		return false, nil
	})
	return result, err
}

func (store *FormJobStore) ReserveFormTaskRoute(
	ctx context.Context, reference string, owner FormJobOwner,
) (FormTaskRouteSnapshot, error) {
	var result FormTaskRouteSnapshot
	err := store.updateFormTaskRoute(ctx, reference, owner, func(
		record *FormJobRecord, mode *formIntakeStoredMode, route *formTaskRouteStored,
	) (bool, error) {
		if !formIntakeCapturesRoute(mode) ||
			(route.State != FormTaskPending && route.State != FormTaskVerify) {
			return false, ErrFormJobConflict
		}
		if route.Passes >= MaxFormTaskRoutingPasses {
			return false, ErrFormJobCapacityExceeded
		}
		route.Passes++
		route.ReservedRevision = record.Revision
		result = formTaskRouteSnapshot(*record, mode, *route, false)
		return true, nil
	})
	return result, err
}

func (store *FormJobStore) DecideFormTaskRoute(
	ctx context.Context, request FormTaskRouteDecision,
) (FormTaskRouteSnapshot, error) {
	if request.ExpectedRevision <= 0 || request.Pass <= 0 || request.Pass > MaxFormTaskRoutingPasses ||
		(request.Action != FormTaskContinue && request.Action != FormTaskAsk && request.Action != FormTaskSwitch) {
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
	var result FormTaskRouteSnapshot
	err = store.updateFormTaskRoute(ctx, request.Reference, request.Owner, func(
		record *FormJobRecord, mode *formIntakeStoredMode, route *formTaskRouteStored,
	) (bool, error) {
		for _, previous := range route.Decisions {
			if previous.Pass != request.Pass {
				continue
			}
			if previous.Digest != digest {
				return false, ErrFormJobAnswerConflict
			}
			result = formTaskRouteSnapshot(*record, mode, *route, true)
			return false, nil
		}
		if !formIntakeCapturesRoute(mode) || record.Revision != request.ExpectedRevision ||
			record.Revision != route.ReservedRevision || request.Pass != route.Passes ||
			(route.State != FormTaskPending && route.State != FormTaskVerify) {
			return false, ErrFormJobConflict
		}
		if request.Action == FormTaskSwitch {
			if request.Evidence.Occurrence != 1 || request.Evidence.Text == "" ||
				request.Evidence.Text != strings.TrimSpace(route.Text) {
				return false, ErrFormJobAnswerConflict
			}
			if _, err = resolveFormIntakeLiteral(route.Text, request.Evidence); err != nil {
				return false, err
			}
		}
		state := FormTaskQuestion
		switch request.Action {
		case FormTaskContinue:
			if len(route.MediaRefs) == 0 && route.State != FormTaskVerify && mode.State == FormIntakeActive {
				state = FormTaskCurrent
			}
		case FormTaskSwitch:
			state = FormTaskVerify
			if len(route.Decisions) > 0 && route.State == FormTaskVerify {
				state = FormTaskQuestion
				previous := route.Decisions[len(route.Decisions)-1]
				if previous.Action == FormTaskSwitch && previous.Revision == record.Revision {
					state = FormTaskReady
				}
			}
		}
		route.Decisions = append(route.Decisions, formTaskRouteStoredDecision{
			Pass: request.Pass, Revision: record.Revision, Action: request.Action, Digest: digest,
		})
		route.State = state
		result = formTaskRouteSnapshot(*record, mode, *route, false)
		return true, nil
	})
	return result, err
}

// GrantFormTaskHandoff releases only this new request, once, while preserving
// the old form and values. A crash after grant is uncertain, not permission to
// release it again or repeat another task's external effects.
func (store *FormJobStore) GrantFormTaskHandoff(
	ctx context.Context, reference string, owner FormJobOwner,
) (FormTaskHandoff, error) {
	var result FormTaskHandoff
	err := store.updateFormTaskRoute(ctx, reference, owner, func(
		record *FormJobRecord, mode *formIntakeStoredMode, route *formTaskRouteStored,
	) (bool, error) {
		if route.State == FormTaskGranted {
			result.Progress, result.Reused = formIntakeProgress(*record, mode), true
			return false, nil
		}
		if route.State != FormTaskReady || !formIntakeCapturesRoute(mode) ||
			record.Revision != formTaskGrantRevision(*route) ||
			record.Revision == math.MaxInt64 {
			return false, ErrFormJobConflict
		}
		if hasStagedFormIntake(mode) {
			return false, ErrFormJobConflict
		}
		result.Text, result.Media = route.Text, slices.Clone(route.MediaRefs)
		for index := range route.Answers {
			answer := &route.Answers[index]
			if answer.ID == route.ConsentAnswerID && answer.ChoiceKind == "private" && answer.Choice == FormTaskSwitch {
				result.Text = strings.TrimSpace(result.Text + "\n\n" + answer.Text)
				answer.Text = ""
			}
		}
		route.State, route.Text, route.MediaRefs = FormTaskGranted, "", nil
		mode.State = FormIntakeSuspended
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

func (store *FormJobStore) updateFormTaskRoute(
	ctx context.Context, reference string, owner FormJobOwner,
	mutate func(*FormJobRecord, *formIntakeStoredMode, *formTaskRouteStored) (bool, error),
) error {
	return store.updateFormTaskRouteKey(ctx, reference, owner, func(
		record *FormJobRecord, mode *formIntakeStoredMode, route *formTaskRouteStored, _ []byte,
	) (bool, error) {
		return mutate(record, mode, route)
	})
}

func (store *FormJobStore) updateFormTaskRouteKey(
	ctx context.Context, reference string, owner FormJobOwner,
	mutate func(*FormJobRecord, *formIntakeStoredMode, *formTaskRouteStored, []byte) (bool, error),
) error {
	jobID, routeID, err := ParseFormTaskRouteReference(reference)
	if err != nil {
		return err
	}
	return store.update(ctx, func(snapshot *formJobStoreDocument, now time.Time) (bool, error) {
		record, key, err := store.authorizedRecord(snapshot, jobID, owner)
		if err != nil {
			return false, err
		}
		defer clear(key)
		source, err := readFormIntakeSource(record, key)
		if err != nil || source.Intake == nil || !formIntakeMutable(record.Public) || len(record.PendingEvents) != 0 {
			return false, ErrFormJobConflict
		}
		index := slices.IndexFunc(
			source.Intake.TaskRoutes,
			func(route formTaskRouteStored) bool { return route.ID == routeID },
		)
		if index < 0 {
			return false, ErrFormJobNotFound
		}
		changed, err := mutate(&record.Public, source.Intake, &source.Intake.TaskRoutes[index], key)
		if err != nil || !changed {
			return false, err
		}
		if err = store.writeFormIntakeSource(&record, key, source, now); err != nil {
			return false, err
		}
		snapshot.Records[jobID] = record
		return true, nil
	})
}

func ParseFormTaskRouteReference(reference string) (string, string, error) {
	parts := strings.Split(reference, ".")
	if len(parts) != 3 || parts[0] != strings.TrimSuffix(formTaskRoutePrefix, ".") ||
		!validFormTaskRoutingID(parts[1], "form_job_") || !validFormTaskRoutingID(parts[2], "form_route_") {
		return "", "", ErrFormJobAnswerConflict
	}
	return parts[1], parts[2], nil
}

func validFormTaskRoutingID(id, prefix string) bool {
	if !strings.HasPrefix(id, prefix) || len(id) != len(prefix)+32 || strings.ToLower(id) != id {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(id, prefix))
	return err == nil
}

func formTaskRouteSnapshot(
	record FormJobRecord, mode *formIntakeStoredMode, route formTaskRouteStored, reused bool,
) FormTaskRouteSnapshot {
	handoffBytes := len(route.Text)
	for _, answer := range route.Answers {
		if answer.ID == route.ConsentAnswerID && answer.ChoiceKind == "private" && answer.Choice == FormTaskSwitch {
			handoffBytes += 2 + len(answer.Text)
		}
	}
	return FormTaskRouteSnapshot{
		HandoffTextBytes: handoffBytes,
		GrantRevision:    formTaskGrantRevision(route),
		Reference:        formTaskRoutePrefix + record.JobID + "." + route.ID,
		JobID:            record.JobID, ModeID: mode.ID, Revision: record.Revision, AdmittedRevision: route.Revision,
		State: route.State, Passes: route.Passes, FieldReference: route.FieldReference, Reused: reused,
		Text: route.Text, MediaRefs: slices.Clone(route.MediaRefs), Context: cloneFormIntakeContext(route.Context),
	}
}

func (store *FormJobStore) formTaskRouteBodyDigest(text string, refs []string) (string, error) {
	if len(refs) == 0 {
		refs = nil
	}
	return formJobJSONKeyedDigest(store.profileKey, struct {
		Text  string
		Media []string
	}{text, refs})
}

func validFormTaskRouteBody(text string, refs []string) bool {
	if !utf8.ValidString(text) || len(text) > MaxFormIntakeInputBytes ||
		(strings.TrimSpace(text) == "" && len(refs) == 0) || len(refs) > MaxFormTaskRoutingMedia {
		return false
	}
	for index, ref := range refs {
		if !strings.HasPrefix(ref, "media://") || strings.TrimSpace(ref) != ref ||
			len(ref) > 1024 || !utf8.ValidString(ref) || slices.Contains(refs[:index], ref) {
			return false
		}
	}
	return true
}

func formTaskRoutePending(route formTaskRouteStored) bool {
	return route.State != FormTaskGranted && route.State != FormTaskKept &&
		(route.State != FormTaskCurrent || route.FieldReference == "")
}

func validateStoredFormTaskRoutes(record FormJobRecord, mode *formIntakeStoredMode) error {
	if len(mode.TaskRoutes) > MaxFormIntakeReceipts {
		return ErrFormJobRecordCorrupt
	}
	seen := make(map[string]bool, len(mode.TaskRoutes))
	pending := 0
	for _, route := range mode.TaskRoutes {
		if route.ID != "form_route_"+prefixFormIntakeDigest(route.MessageDigest) || seen[route.ID] ||
			!validDocumentDigest(route.MessageDigest) || !validDocumentDigest(route.BodyDigest) ||
			route.Revision < mode.StartRevision || route.Revision > record.Revision || route.CreatedAt <= 0 ||
			route.Passes < 0 || route.Passes > MaxFormTaskRoutingPasses || len(route.Decisions) > route.Passes ||
			(route.Passes == 0 && route.ReservedRevision != 0) ||
			(route.Passes > 0 && (route.ReservedRevision < route.Revision || route.ReservedRevision > record.Revision)) {
			return ErrFormJobRecordCorrupt
		}
		seen[route.ID] = true
		switch route.State {
		case FormTaskPending, FormTaskVerify, FormTaskQuestion, FormTaskCurrent, FormTaskReady:
			if route.FieldReference == "" && !validFormTaskRouteBody(route.Text, route.MediaRefs) {
				return ErrFormJobRecordCorrupt
			}
		case FormTaskGranted, FormTaskKept:
			if route.Text != "" || len(route.MediaRefs) != 0 {
				return ErrFormJobRecordCorrupt
			}
		default:
			return ErrFormJobRecordCorrupt
		}
		if route.FieldReference != "" {
			jobID, inputID, err := parseFormIntakeReceipt(route.FieldReference)
			inputIndex := formIntakeInputIndex(mode, inputID)
			if err != nil || jobID != record.JobID || inputIndex < 0 ||
				route.State != FormTaskCurrent || route.Text != "" || len(route.MediaRefs) != 0 {
				return ErrFormJobRecordCorrupt
			}
		}
		if formTaskRoutePending(route) {
			pending++
		}
		previous := 0
		for _, decision := range route.Decisions {
			if decision.Pass <= previous || decision.Pass > route.Passes || !validDocumentDigest(decision.Digest) ||
				decision.Revision < route.Revision || decision.Revision > record.Revision ||
				(decision.Action != FormTaskContinue && decision.Action != FormTaskAsk && decision.Action != FormTaskSwitch) {
				return ErrFormJobRecordCorrupt
			}
			previous = decision.Pass
		}
		if !validFormTaskRouteDecisionState(route) {
			return ErrFormJobRecordCorrupt
		}
		if err := validateStoredFormTaskQuestion(record, mode, route); err != nil {
			return err
		}
	}
	progress := formIntakeProgress(record, mode)
	if pending > MaxFormTaskRoutingPending || progress.TaskBytes > MaxFormTaskRoutingRetainedBytes ||
		mode.State == FormIntakeClosed && pending != 0 {
		return ErrFormJobRecordCorrupt
	}
	return nil
}

func validFormTaskRouteDecisionState(route formTaskRouteStored) bool {
	if route.ConsentAnswerID != "" {
		switch formTaskRouteConsent(route) {
		case FormTaskSwitch:
			return route.State == FormTaskReady || route.State == FormTaskGranted
		case FormTaskContinue:
			return route.State == FormTaskCurrent || route.State == FormTaskKept
		default:
			return false
		}
	}
	if len(route.Decisions) == 0 {
		return route.State == FormTaskPending || route.State == FormTaskQuestion || route.State == FormTaskKept
	}
	last := route.Decisions[len(route.Decisions)-1]
	if last.Pass == route.Passes && last.Revision != route.ReservedRevision {
		return false
	}
	switch route.State {
	case FormTaskVerify:
		return len(route.Decisions) == 1 && last.Action == FormTaskSwitch
	case FormTaskCurrent:
		return len(route.Decisions) == 1 && last.Action == FormTaskContinue && len(route.MediaRefs) == 0
	case FormTaskReady, FormTaskGranted:
		return len(route.Decisions) == 2 && last.Action == FormTaskSwitch &&
			route.Decisions[0].Action == FormTaskSwitch && route.Decisions[0].Revision == last.Revision
	case FormTaskQuestion, FormTaskKept:
		return len(route.Decisions) <= 2
	default:
		return false
	}
}

func formTaskRouteConsent(route formTaskRouteStored) FormTaskRouteAction {
	for _, answer := range route.Answers {
		if answer.ID == route.ConsentAnswerID && answer.State == "handled" {
			return answer.Choice
		}
	}
	return ""
}

func formTaskGrantRevision(route formTaskRouteStored) int64 {
	if route.ConsentAnswerID != "" {
		for _, answer := range route.Answers {
			if answer.ID == route.ConsentAnswerID && answer.Choice == FormTaskSwitch && answer.State == "handled" {
				return answer.Revision
			}
		}
		return 0
	}
	if len(route.Decisions) == 0 {
		return 0
	}
	return route.Decisions[len(route.Decisions)-1].Revision
}

func formIntakeCapturesRoute(mode *formIntakeStoredMode) bool {
	return mode != nil && (mode.State == FormIntakeActive || mode.State == FormIntakePaused)
}

func (store *FormJobStore) requireExclusiveFormIntake(
	snapshot *formJobStoreDocument, jobID, ownerDigest string,
) error {
	for id, record := range snapshot.Records {
		if id == jobID || record.Public.State.terminal() || record.Public.OwnerDigest != ownerDigest {
			continue
		}
		key, err := unwrapFormJobKey(store.profileKey, *record.WrappedKey)
		if err != nil {
			return err
		}
		source, err := readFormIntakeSource(record, key)
		clear(key)
		if err != nil {
			return err
		}
		if formIntakeCapturesRoute(source.Intake) {
			return ErrFormJobConflict
		}
	}
	return nil
}

func (store *FormJobStore) FormIntakeForJob(
	ctx context.Context, jobID string, owner FormJobOwner,
) (*FormIntakeProgress, error) {
	var result *FormIntakeProgress
	err := store.update(ctx, func(snapshot *formJobStoreDocument, _ time.Time) (bool, error) {
		record, key, err := store.authorizedRecord(snapshot, jobID, owner)
		if err != nil {
			return false, err
		}
		defer clear(key)
		source, err := readFormIntakeSource(record, key)
		if err != nil {
			return false, err
		}
		if source.Intake != nil && source.Intake.State != FormIntakeClosed {
			progress := formIntakeProgress(record.Public, source.Intake)
			result = &progress
		}
		return false, nil
	})
	return result, err
}

func (store *FormJobStore) validateRoutedFormAdmission(
	record FormJobRecord, mode *formIntakeStoredMode, reference, identity, text string,
	intent interactions.ProtectedAnswerIntent, staged bool,
) error {
	if reference == "" {
		return nil
	}
	jobID, routeID, err := ParseFormTaskRouteReference(reference)
	answerID := ""
	if err != nil {
		jobID, routeID, answerID, err = ParseFormTaskRouteAnswerReference(reference)
	}
	if err != nil || jobID != record.JobID || identity != reference || staged ||
		intent != interactions.ProtectedAnswerValue {
		return ErrFormJobAnswerConflict
	}
	for _, route := range mode.TaskRoutes {
		if route.ID != routeID {
			continue
		}
		if answerID != "" {
			for _, answer := range route.Answers {
				if answer.ID != answerID {
					continue
				}
				if answer.FieldReference != "" {
					return store.validateRoutedFieldReplay(
						record.JobID,
						mode,
						answer.FieldReference,
						identity,
						text,
						intent,
					)
				}
				if answer.State == "handled" && answer.ForForm && answer.Text == text {
					return nil
				}
				return ErrFormJobAnswerConflict
			}
			return ErrFormJobAnswerConflict
		}
		if route.State != FormTaskCurrent {
			continue
		}
		if route.FieldReference != "" {
			return store.validateRoutedFieldReplay(record.JobID, mode, route.FieldReference, identity, text, intent)
		}
		if len(route.MediaRefs) > 0 && formTaskRouteConsent(route) != FormTaskContinue {
			return ErrFormJobAnswerConflict
		}
		digest, digestErr := store.formTaskRouteBodyDigest(text, route.MediaRefs)
		if digestErr == nil && constantTimeStringEqual(route.BodyDigest, digest) {
			return nil
		}
		return ErrFormJobAnswerConflict
	}
	return ErrFormJobAnswerConflict
}

func (store *FormJobStore) validateRoutedFieldReplay(
	jobID string,
	mode *formIntakeStoredMode,
	reference, identity, text string,
	intent interactions.ProtectedAnswerIntent,
) error {
	job, inputID, parseErr := parseFormIntakeReceipt(reference)
	index := formIntakeInputIndex(mode, inputID)
	if parseErr != nil || job != jobID || index < 0 {
		return ErrFormJobAnswerConflict
	}
	input := mode.Inputs[index]
	digest, digestErr := store.formIntakeBodyDigest(input.Origin, intent, text)
	expected := keyedDigest(store.profileKey, "intake_message", jobID, mode.ID, identity)
	if digestErr == nil && constantTimeStringEqual(input.BodyDigest, digest) &&
		constantTimeStringEqual(input.MessageDigest, expected) {
		return nil
	}
	return ErrFormJobAnswerConflict
}

func linkRoutedFormInput(jobID string, mode *formIntakeStoredMode, reference, inputID string) {
	for index := range mode.TaskRoutes {
		route := &mode.TaskRoutes[index]
		for answerIndex := range route.Answers {
			answer := &route.Answers[answerIndex]
			if formTaskAnswerReference(jobID, route.ID, answer.ID) == reference {
				answer.FieldReference = formIntakeReceiptPrefix + jobID + "." + inputID
				answer.Text = ""
				return
			}
		}
		if formTaskRoutePrefix+jobID+"."+route.ID == reference {
			route.FieldReference = formIntakeReceiptPrefix + jobID + "." + inputID
			route.Text = ""
			route.MediaRefs = nil
			return
		}
	}
}
