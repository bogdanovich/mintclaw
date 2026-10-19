package document

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

// StartFormIntake attaches one bounded protected conversation to the existing
// job. It does not accept facts or change the immutable document binding.
func (store *FormJobStore) StartFormIntake(
	ctx context.Context,
	request FormIntakeStartRequest,
) (FormIntakeStartResult, error) {
	if !validFormIntakeJobID(request.JobID) || request.ExpectedRevision <= 0 ||
		!validFormIntakeKey(request.IdempotencyKey) || !validFormFieldsFacts(request.Schema) ||
		!validFormIntakeContext(request.Context) {
		return FormIntakeStartResult{}, ErrFormJobAnswerConflict
	}
	if _, err := formIntakeCandidates(request.Schema, request.Context.CandidateFieldIDs); err != nil {
		return FormIntakeStartResult{}, err
	}
	var result FormIntakeStartResult
	err := store.update(ctx, func(snapshot *formJobStoreDocument, now time.Time) (bool, error) {
		record, key, err := store.authorizedRecord(snapshot, request.JobID, request.Owner)
		if err != nil {
			return false, err
		}
		defer clear(key)
		if matchErr := formJobMatchesSchema(record.Public, request.Schema); matchErr != nil {
			return false, matchErr
		}
		if !formIntakeMutable(record.Public) || len(record.PendingEvents) != 0 {
			return false, ErrFormJobConflict
		}
		source, err := readFormIntakeSource(record, key)
		if err != nil {
			return false, err
		}
		digest, err := formJobJSONKeyedDigest(store.profileKey, struct {
			JobID    string
			Revision int64
			Context  FormIntakeContext
		}{record.Public.JobID, request.ExpectedRevision, request.Context})
		if err != nil {
			return false, err
		}
		id := "form_intake_" + keyedDigest(store.profileKey, "intake_mode", record.Public.JobID, request.IdempotencyKey)[:32]
		if source.Intake != nil {
			if source.Intake.ID != id || !constantTimeStringEqual(source.Intake.StartDigest, digest) {
				return false, ErrFormJobAnswerConflict
			}
			result = formIntakeStartResult(record, source.Intake, true)
			return false, nil
		}
		if record.Public.Revision != request.ExpectedRevision {
			return false, ErrFormJobConflict
		}
		if err = store.requireExclusiveFormIntake(
			snapshot,
			record.Public.JobID,
			record.Public.OwnerDigest,
		); err != nil {
			return false, err
		}
		payload := formIntakeBindingPayload{
			Version: FormIntakeVersion, JobID: record.Public.JobID, ModeID: id,
			SchemaDigest: record.Public.FieldSchemaDigest, ExpectedRevision: record.Public.Revision,
			Context: cloneFormIntakeContext(request.Context),
		}
		envelope, err := sealFormJobEnvelope(key, formJobEnvelope{
			Kind: formIntakeBindingKind, JobID: payload.JobID, EventID: id, Revision: payload.ExpectedRevision,
		}, payload, store.random)
		if err != nil {
			return false, err
		}
		encoded, err := json.Marshal(envelope)
		if err != nil {
			return false, ErrFormJobRecordCorrupt
		}
		source.Intake = &formIntakeStoredMode{
			Version: FormIntakeVersion, ID: id, SchemaDigest: payload.SchemaDigest,
			StartDigest: digest, StartRevision: payload.ExpectedRevision, State: FormIntakeActive,
			Context: payload.Context,
			InitialBinding: interactions.ProtectedAnswerBinding{
				Namespace: FormProtectedAnswerNamespace, Token: base64.RawURLEncoding.EncodeToString(encoded),
				Actions: []interactions.ProtectedAnswerAction{
					interactions.ProtectedAnswerActionClarify, interactions.ProtectedAnswerActionBack,
				},
			},
		}
		if err := store.writeFormIntakeSource(&record, key, source, now); err != nil {
			return false, err
		}
		snapshot.Records[record.Public.JobID] = record
		result = formIntakeStartResult(record, source.Intake, false)
		return true, nil
	})
	if err != nil {
		return FormIntakeStartResult{}, err
	}
	return result, nil
}

// FindFormIntake returns no utterance, PDF value, or transferable authority.
// Exact authenticated owner/route matching prevents topic or workspace bleed.
func (store *FormJobStore) FindFormIntake(ctx context.Context, owner FormJobOwner) (*FormIntakeProgress, error) {
	var result *FormIntakeProgress
	err := store.update(ctx, func(snapshot *formJobStoreDocument, _ time.Time) (bool, error) {
		digest, err := store.ownerDigest(owner)
		if err != nil {
			return false, err
		}
		for _, candidate := range snapshot.Records {
			if candidate.Public.State.terminal() || !constantTimeStringEqual(candidate.Public.OwnerDigest, digest) {
				continue
			}
			record, key, err := store.authorizedRecord(snapshot, candidate.Public.JobID, owner)
			if err != nil {
				return false, err
			}
			source, err := readFormIntakeSource(record, key)
			clear(key)
			if err != nil {
				return false, err
			}
			if !formIntakeCapturesRoute(source.Intake) {
				continue
			}
			if result != nil {
				return false, ErrFormJobConflict
			}
			progress := formIntakeProgress(record.Public, source.Intake)
			result = &progress
		}
		return false, nil
	})
	return result, err
}

// AdmitFormIntakeMessage atomically admits an authenticated standalone message
// to the document-owned mode. No synthetic interaction registry claim is used.
func (store *FormJobStore) AdmitFormIntakeMessage(
	ctx context.Context,
	request FormIntakeMessageRequest,
) (FormIntakeAdmission, error) {
	if !validFormIntakeJobID(request.JobID) || !validFormIntakeJobID(request.ModeID) ||
		request.ExpectedRevision <= 0 || !validFormFieldsFacts(request.Schema) ||
		!validFormIntakeKey(
			request.MessageIdentity,
		) || !validFormIntakeText(interactions.ProtectedAnswerValue, request.Text) {
		return FormIntakeAdmission{}, ErrFormJobAnswerConflict
	}
	return store.admitFormIntake(ctx, request.Owner, request.JobID, func(
		record formJobStoredRecord, mode *formIntakeStoredMode,
	) (formIntakeBindingPayload, error) {
		if err := formJobMatchesSchema(record.Public, request.Schema); err != nil {
			return formIntakeBindingPayload{}, err
		}
		if mode.ID != request.ModeID {
			return formIntakeBindingPayload{}, ErrFormJobConflict
		}
		return formIntakeBindingPayload{
			Version: FormIntakeVersion, JobID: record.Public.JobID, ModeID: mode.ID,
			SchemaDigest: mode.SchemaDigest, ExpectedRevision: request.ExpectedRevision, Context: mode.Context,
		}, nil
	}, request.MessageIdentity, interactions.ProtectedAnswerValue, request.Text, "", false, request.RoutingReference)
}

type formIntakeOriginResolver func(formJobStoredRecord, *formIntakeStoredMode) (formIntakeBindingPayload, error)

func (store *FormJobStore) admitFormIntake(
	ctx context.Context, owner FormJobOwner, jobID string, originResolver formIntakeOriginResolver,
	identity string, intent interactions.ProtectedAnswerIntent, text, interactionID string, staged bool,
	routingReference ...string,
) (FormIntakeAdmission, error) {
	var result FormIntakeAdmission
	err := store.update(ctx, func(snapshot *formJobStoreDocument, now time.Time) (bool, error) {
		record, key, err := store.authorizedRecord(snapshot, jobID, owner)
		if err != nil {
			return false, err
		}
		defer clear(key)
		if !formIntakeMutable(record.Public) {
			return false, ErrFormJobConflict
		}
		source, err := readFormIntakeSource(record, key)
		if err != nil || source.Intake == nil {
			return false, ErrFormJobConflict
		}
		mode := source.Intake
		if len(routingReference) > 1 {
			return false, ErrFormJobAnswerConflict
		}
		routed := ""
		if len(routingReference) == 1 {
			routed = routingReference[0]
		}
		if err = store.validateRoutedFormAdmission(
			record.Public,
			mode,
			routed,
			identity,
			text,
			intent,
			staged,
		); err != nil {
			return false, err
		}
		origin, err := originResolver(record, mode)
		if err != nil {
			return false, err
		}
		admissionContext, admissionOrder := mode.Context, 0
		if len(mode.TaskRoutes) > 0 {
			admissionOrder = nextFormIntakeAdmissionOrder(mode)
		}
		if routed != "" {
			admissionContext, admissionOrder, err = routedFormIntakeOrigin(record.Public.JobID, mode, routed)
			if err != nil {
				return false, err
			}
			origin.Context = cloneFormIntakeContext(admissionContext)
		}
		messageDigest := keyedDigest(store.profileKey, "intake_message", record.Public.JobID, mode.ID, identity)
		for _, input := range mode.Inputs {
			if !constantTimeStringEqual(input.MessageDigest, messageDigest) {
				continue
			}
			if !staged && input.State == formIntakeInputStaged {
				return false, ErrFormJobConflict
			}
			// Transport replay uses the stored interpretation origin, never a new
			// revision's context. Explicit prompt claims must also match that origin.
			if staged && (input.InteractionID != interactionID || !equalFormIntakeOrigin(input.Origin, origin)) {
				return false, ErrFormJobAnswerConflict
			}
			digest, digestErr := store.formIntakeBodyDigest(input.Origin, intent, text)
			if digestErr != nil || !constantTimeStringEqual(digest, input.BodyDigest) {
				return false, ErrFormJobAnswerConflict
			}
			result = formIntakeAdmission(record, mode, input.ID, true)
			return false, nil
		}
		if mode.State != FormIntakeActive || origin.ExpectedRevision != record.Public.Revision ||
			len(record.PendingEvents) != 0 || record.Public.Revision == math.MaxInt64 ||
			!equalFormIntakeOrigin(origin, formIntakeBindingPayload{
				Version: FormIntakeVersion, JobID: record.Public.JobID, ModeID: mode.ID,
				SchemaDigest: mode.SchemaDigest, ExpectedRevision: record.Public.Revision, Context: admissionContext,
			}) {
			return false, ErrFormJobConflict
		}
		progress := formIntakeProgress(record.Public, mode)
		if len(mode.Inputs) >= MaxFormIntakeReceipts || progress.PendingInputs >= MaxFormIntakePendingInputs ||
			progress.RetainedBytes+len(text) > MaxFormIntakeRetainedBytes {
			return false, ErrFormJobCapacityExceeded
		}
		for _, input := range mode.Inputs {
			// A registry question remains a one-answer claim even when admission
			// no longer advances the assignment revision. Exact transport replay
			// was handled above; a new message cannot reuse the same claim.
			if input.State == formIntakeInputStaged || staged && (input.InteractionID == interactionID ||
				input.InteractionID != "" && equalFormIntakeOrigin(input.Origin, origin)) {
				return false, ErrFormJobConflict
			}
		}
		bodyDigest, err := store.formIntakeBodyDigest(origin, intent, text)
		if err != nil {
			return false, err
		}
		input := formIntakeStoredInput{
			AdmissionOrder: admissionOrder,
			ID:             "form_input_" + messageDigest[:32], MessageDigest: messageDigest, BodyDigest: bodyDigest,
			InteractionID: interactionID, State: formIntakeInputStaged, Intent: intent, Text: text,
			Origin: origin, CreatedAt: now.UnixMilli(),
		}
		if !staged {
			input.State, input.CommittedAt = formIntakeInputCommitted, now.UnixMilli()
		}
		mode.Inputs = append(mode.Inputs, input)
		if routed != "" {
			linkRoutedFormInput(record.Public.JobID, mode, routed, input.ID)
		}
		if err := store.writeFormIntakeSource(&record, key, source, now); err != nil {
			return false, err
		}
		snapshot.Records[record.Public.JobID] = record
		result = formIntakeAdmission(record, mode, input.ID, false)
		return true, nil
	})
	if err != nil {
		return FormIntakeAdmission{}, err
	}
	return result, nil
}

// ReadFormIntakeInput is only for the protected extractor. Staged inputs cannot
// be interpreted before the interaction registry's durable owner claim.
func (store *FormJobStore) ReadFormIntakeInput(
	ctx context.Context, reference string, owner FormJobOwner,
) (FormIntakeInput, error) {
	jobID, inputID, err := parseFormIntakeReceipt(reference)
	if err != nil {
		return FormIntakeInput{}, err
	}
	var result FormIntakeInput
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
		if index < 0 || source.Intake.Inputs[index].State == formIntakeInputStaged {
			return false, ErrFormJobConflict
		}
		result = store.formIntakeInput(record.Public, source.Intake, source.Intake.Inputs[index])
		return false, nil
	})
	if err != nil {
		return FormIntakeInput{}, err
	}
	return result, nil
}

// ReserveFormIntakePass persists the extraction budget before calling a model.
// Retry never resets that budget or exposes the full schema as one candidate view.
func (store *FormJobStore) ReserveFormIntakePass(
	ctx context.Context, request FormIntakePassRequest,
) (FormIntakePass, error) {
	jobID, inputID, err := parseFormIntakeReceipt(request.Reference)
	if err != nil || request.ExpectedRevision <= 0 || !validFormIntakeKey(request.IdempotencyKey) ||
		!validFormFieldsFacts(request.Schema) {
		return FormIntakePass{}, ErrFormJobAnswerConflict
	}
	fields, err := formIntakeCandidates(request.Schema, request.CandidateIDs)
	if err != nil {
		return FormIntakePass{}, err
	}
	var result FormIntakePass
	err = store.update(ctx, func(snapshot *formJobStoreDocument, now time.Time) (bool, error) {
		record, key, authErr := store.authorizedRecord(snapshot, jobID, request.Owner)
		if authErr != nil {
			return false, authErr
		}
		defer clear(key)
		if matchErr := formJobMatchesSchema(record.Public, request.Schema); matchErr != nil {
			return false, matchErr
		}
		source, sourceErr := readFormIntakeSource(record, key)
		if sourceErr != nil {
			return false, sourceErr
		}
		index := formIntakeInputIndex(source.Intake, inputID)
		if index < 0 || source.Intake.State != FormIntakeActive || !formIntakeMutable(record.Public) ||
			record.Public.Revision != request.ExpectedRevision || source.Intake.Inputs[index].State != formIntakeInputCommitted {
			return false, ErrFormJobConflict
		}
		if stageErr := requireNoStagedFormIntake(record, key); stageErr != nil {
			return false, stageErr
		}
		input := &source.Intake.Inputs[index]
		digest := keyedDigest(store.profileKey, "intake_pass", input.ID, request.IdempotencyKey)
		group := ""
		if request.RetryStale {
			group = digest
			for _, prior := range input.Reservations {
				if !constantTimeStringEqual(prior.GroupDigest, group) {
					continue
				}
				if !slices.Equal(prior.CandidateIDs, request.CandidateIDs) {
					return false, ErrFormJobAnswerConflict
				}
				if prior.Outcome != nil {
					outcome := formIntakeApplyResult(record.Public, source.Intake, prior.Outcome, true)
					result = FormIntakePass{
						ID: prior.ID, Revision: prior.Revision,
						CandidateIDs: slices.Clone(prior.CandidateIDs), Reused: true, Outcome: &outcome,
					}
					return false, nil
				}
			}
			digest = keyedDigest(
				store.profileKey,
				"intake_retry",
				group,
				strconv.FormatInt(request.ExpectedRevision, 10),
				strconv.Itoa(len(source.Intake.Inputs)),
			)
		}
		reservationIndex := slices.IndexFunc(input.Reservations, func(pass formIntakeStoredPass) bool {
			return constantTimeStringEqual(pass.KeyDigest, digest)
		})
		reused := reservationIndex >= 0
		var reservation formIntakeStoredPass
		if reused {
			reservation = input.Reservations[reservationIndex]
			if reservation.Revision != request.ExpectedRevision ||
				!slices.Equal(reservation.CandidateIDs, request.CandidateIDs) {
				return false, ErrFormJobAnswerConflict
			}
			if reservation.Outcome == nil && (reservation.ReceiptCount != len(source.Intake.Inputs) ||
				formIntakeReservationSuperseded(source.Intake, input.ID, reservationIndex)) {
				return false, ErrFormJobConflict
			}
		} else {
			if len(input.Reservations) >= MaxFormIntakeExtractionPasses {
				return false, ErrFormJobCapacityExceeded
			}
			reservation = formIntakeStoredPass{
				ID:           "form_pass_" + digest[:32],
				KeyDigest:    digest,
				GroupDigest:  group,
				ReceiptCount: len(source.Intake.Inputs),
				Revision:     request.ExpectedRevision,
				CandidateIDs: slices.Clone(request.CandidateIDs),
			}
			input.Reservations = append(input.Reservations, reservation)
			if saveErr := store.writeFormIntakeSource(&record, key, source, now); saveErr != nil {
				return false, saveErr
			}
			snapshot.Records[record.Public.JobID] = record
		}
		result = FormIntakePass{
			Input: store.formIntakeInput(record.Public, source.Intake, *input), Fields: fields,
			ID: reservation.ID, Revision: reservation.Revision,
			CandidateIDs: slices.Clone(reservation.CandidateIDs), Reused: reused,
		}
		// Ordinary retries see the current collection question. Routed input
		// was already bound before delayed promotion: its original role and
		// question remain the interpretation authority, not the later prompt.
		if !isRoutedFormIntakeInput(record.Public.JobID, source.Intake, input.ID) {
			result.Input.Context = cloneFormIntakeContext(source.Intake.Context)
		}
		return !reused, nil
	})
	if err != nil {
		return FormIntakePass{}, err
	}
	return result, nil
}

func readFormIntakeSource(record formJobStoredRecord, key []byte) (formJobSourceSecret, error) {
	var source formJobSourceSecret
	if record.Source == nil || openFormJobEnvelope(key, *record.Source, &source) != nil {
		return source, ErrFormJobRecordCorrupt
	}
	return source, nil
}

func (store *FormJobStore) writeFormIntakeSource(
	record *formJobStoredRecord, key []byte, source formJobSourceSecret, now time.Time,
) error {
	if err := validateStoredFormIntake(record.Public, source.Intake); err != nil {
		return err
	}
	envelope, err := sealFormJobEnvelope(key, *record.Source, source, store.random)
	if err != nil {
		return err
	}
	// Decryption constructed new slices; publication never aliases the loaded
	// source envelope, including on unrelated expiry saves after an error.
	record.Source = &envelope
	record.Public.UpdatedAt = now.UnixMilli()
	return nil
}

func (store *FormJobStore) formIntakeBodyDigest(
	origin formIntakeBindingPayload, intent interactions.ProtectedAnswerIntent, text string,
) (string, error) {
	return formJobJSONKeyedDigest(store.profileKey, struct {
		Origin formIntakeBindingPayload
		Intent interactions.ProtectedAnswerIntent
		Text   string
	}{origin, intent, text})
}

func equalFormIntakeOrigin(a, b formIntakeBindingPayload) bool {
	left, errA := json.Marshal(a)
	right, errB := json.Marshal(b)
	return errA == nil && errB == nil && constantTimeStringEqual(string(left), string(right))
}

func formIntakeMutable(record FormJobRecord) bool {
	return record.State == FormJobPrepared || record.State == FormJobCollecting ||
		record.State == FormJobReviewReady || record.State == FormJobAwaitingApproval
}

func invalidateFormIntakeReview(record *FormJobRecord) {
	clearFormJobReviewProjection(record)
	clearFormJobCommitProjection(record)
	record.State = FormJobCollecting
}

func formIntakeStartResult(record formJobStoredRecord, mode *formIntakeStoredMode, reused bool) FormIntakeStartResult {
	return FormIntakeStartResult{
		Job: cloneFormJobRecord(record.Public), Progress: formIntakeProgress(record.Public, mode),
		Binding: mode.InitialBinding, Reused: reused,
	}
}

func formIntakeProgress(record FormJobRecord, mode *formIntakeStoredMode) FormIntakeProgress {
	progress := FormIntakeProgress{
		JobID:      record.JobID,
		ModeID:     mode.ID,
		Revision:   record.Revision,
		State:      mode.State,
		InputCount: len(mode.Inputs),
		Context:    cloneFormIntakeContext(mode.Context),
	}
	for _, input := range mode.Inputs {
		if input.State != formIntakeInputConsumed {
			progress.PendingInputs++
			progress.RetainedBytes += len(input.Text)
		}
	}
	for _, route := range mode.TaskRoutes {
		progress.TaskBytes += len(route.Text)
		if formTaskRoutePending(route) {
			progress.PendingTasks++
		}
		for _, answer := range route.Answers {
			progress.TaskBytes += formTaskAnswerTextBytes(answer.Text)
			if answer.ForForm && answer.Text != "" {
				progress.PendingTasks++
			}
		}
	}
	return progress
}

func formIntakeAdmission(
	record formJobStoredRecord,
	mode *formIntakeStoredMode,
	id string,
	reused bool,
) FormIntakeAdmission {
	return FormIntakeAdmission{
		Job: cloneFormJobRecord(record.Public), Progress: formIntakeProgress(record.Public, mode),
		Receipt: formIntakeReceiptPrefix + record.Public.JobID + "." + id, Reused: reused,
	}
}

func (store *FormJobStore) formIntakeInput(
	record FormJobRecord,
	mode *formIntakeStoredMode,
	input formIntakeStoredInput,
) FormIntakeInput {
	var dialogIntent FormIntakeDialogIntent
	var completedWindows [][]string
	for _, pass := range input.Reservations {
		if pass.Outcome != nil {
			completedWindows = append(completedWindows, slices.Clone(pass.CandidateIDs))
		}
	}
	for index := len(input.Reservations) - 1; index >= 0; index-- {
		if outcome := input.Reservations[index].Outcome; outcome != nil {
			dialogIntent = outcome.Intent
			break
		}
	}
	findings := formIntakeCurrentModeFindings(mode, input)
	var supersededAdvisoryIDs []string
	for _, finding := range findings {
		if finding.Code != "field_ambiguous" && finding.Code != "field_confirmation_required" {
			continue
		}
		var lastAdviceRevision int64
		for _, pass := range input.Reservations {
			if pass.Outcome != nil &&
				slices.ContainsFunc(pass.Outcome.Findings, func(prior FormFieldMappingBlocker) bool {
					return prior.FieldID == finding.FieldID
				}) {
				lastAdviceRevision = max(lastAdviceRevision, pass.Outcome.AppliedRevision)
			}
		}
		field, found := formIntakeConfirmedUserField(record, finding.FieldID)
		if found && lastAdviceRevision > 0 && store.formIntakeEventObservedAdmission(
			mode, formIntakeInputIndex(mode, input.ID), field, lastAdviceRevision,
		) {
			supersededAdvisoryIDs = append(supersededAdvisoryIDs, finding.FieldID)
		}
	}
	return FormIntakeInput{
		AdmissionOrder:        formIntakeAdmissionOrder(mode, formIntakeInputIndex(mode, input.ID)),
		JobID:                 record.JobID,
		ModeID:                mode.ID,
		Reference:             formIntakeReceiptPrefix + record.JobID + "." + input.ID,
		Revision:              record.Revision,
		AdmittedRevision:      input.Origin.ExpectedRevision,
		DialogIntent:          dialogIntent,
		Passes:                len(input.Reservations),
		Consumed:              input.State == formIntakeInputConsumed,
		Intent:                input.Intent,
		Text:                  input.Text,
		Context:               cloneFormIntakeContext(input.Origin.Context),
		Accounted:             formIntakeAccountedLiterals(input),
		Findings:              findings,
		CompletedWindows:      completedWindows,
		SupersededAdvisoryIDs: supersededAdvisoryIDs,
	}
}

func formIntakeInputIndex(mode *formIntakeStoredMode, id string) int {
	if mode == nil {
		return -1
	}
	return slices.IndexFunc(mode.Inputs, func(input formIntakeStoredInput) bool { return input.ID == id })
}

func parseFormIntakeReceipt(reference string) (string, string, error) {
	if len(reference) > interactions.MaxProtectedReference || !strings.HasPrefix(reference, formIntakeReceiptPrefix) {
		return "", "", ErrFormJobAnswerConflict
	}
	parts := strings.Split(strings.TrimPrefix(reference, formIntakeReceiptPrefix), ".")
	if len(parts) != 2 || !validFormIntakeJobID(parts[0]) || !validFormIntakeJobID(parts[1]) ||
		!strings.HasPrefix(parts[1], "form_input_") {
		return "", "", ErrFormJobAnswerConflict
	}
	return parts[0], parts[1], nil
}

func formIntakeCandidates(schema FormFieldsFacts, ids []string) ([]FormField, error) {
	if len(ids) == 0 || len(ids) > MaxFormIntakeCandidates {
		return nil, ErrFormJobCapacityExceeded
	}
	fields := make([]FormField, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		index := slices.IndexFunc(schema.Fields, func(field FormField) bool { return field.ID == id })
		if index < 0 || seen[id] || schema.Fields[index].ReadOnly {
			return nil, ErrFormJobAnswerConflict
		}
		seen[id] = true
		fields = append(fields, cloneFormField(schema.Fields[index]))
	}
	return fields, nil
}

func cloneFormIntakeContext(value FormIntakeContext) FormIntakeContext {
	value.CandidateFieldIDs = slices.Clone(value.CandidateFieldIDs)
	return value
}

func validFormIntakeJobID(id string) bool {
	return len(id) <= maxFormJobIDLength && safeFormJobCodePattern.MatchString(id)
}

func validFormIntakeKey(key string) bool {
	return strings.TrimSpace(key) != "" && len(key) <= maxFormJobIdempotencyLength && utf8.ValidString(key)
}

func validFormIntakeText(intent interactions.ProtectedAnswerIntent, text string) bool {
	if intent == interactions.ProtectedAnswerClarify || intent == interactions.ProtectedAnswerBack {
		return text == ""
	}
	return intent == interactions.ProtectedAnswerValue && strings.TrimSpace(text) != "" &&
		len(text) <= MaxFormIntakeInputBytes && utf8.ValidString(text)
}

func validFormIntakeContext(value FormIntakeContext) bool {
	if len(value.CandidateFieldIDs) == 0 || len(value.CandidateFieldIDs) > MaxFormIntakeCandidates ||
		strings.TrimSpace(value.Question) == "" || !utf8.ValidString(value.Question) ||
		utf8.RuneCountInString(value.Question) > interactions.MaxQuestionLength ||
		!utf8.ValidString(
			value.RoleContext,
		) || utf8.RuneCountInString(value.RoleContext) > maxFormIntakeRoleContextRunes {
		return false
	}
	if value.Language != "" {
		canonical, err := interactions.CanonicalPromptLanguage(value.Language)
		if err != nil || canonical != value.Language {
			return false
		}
	}
	seen := make(map[string]bool, len(value.CandidateFieldIDs))
	for _, id := range value.CandidateFieldIDs {
		if !opaqueFieldID.MatchString(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return value.FocusFieldID == "" || seen[value.FocusFieldID]
}

func validateStoredFormIntake(record FormJobRecord, mode *formIntakeStoredMode) error {
	if mode == nil {
		return nil
	}
	if mode.Version != FormIntakeVersion || !validFormIntakeJobID(mode.ID) ||
		!strings.HasPrefix(mode.ID, "form_intake_") || mode.SchemaDigest != record.FieldSchemaDigest ||
		!validDocumentDigest(mode.StartDigest) || mode.StartRevision <= 0 || mode.StartRevision > record.Revision ||
		(mode.State != FormIntakeActive && mode.State != FormIntakePaused && mode.State != FormIntakeClosed &&
			mode.State != FormIntakeSuspended) ||
		!validFormIntakeContext(mode.Context) || mode.InitialBinding.Namespace != FormProtectedAnswerNamespace ||
		mode.InitialBinding.Token == "" || len(mode.InitialBinding.Token) > 2*maxFormProtectedBindingBytes ||
		!slices.Equal(mode.InitialBinding.Actions, []interactions.ProtectedAnswerAction{
			interactions.ProtectedAnswerActionClarify, interactions.ProtectedAnswerActionBack,
		}) || len(mode.Inputs) > MaxFormIntakeReceipts {
		return ErrFormJobRecordCorrupt
	}
	if err := validateStoredFormTaskRoutes(record, mode); err != nil {
		return err
	}
	if err := validateFormIntakeAdmissionOrders(mode); err != nil {
		return err
	}
	envelope, err := decodeFormProtectedBindingEnvelope(mode.InitialBinding.Token)
	if err != nil || envelope.Kind != formIntakeBindingKind || envelope.JobID != record.JobID ||
		envelope.EventID != mode.ID || envelope.Revision != mode.StartRevision {
		return ErrFormJobRecordCorrupt
	}
	seen := make(map[string]bool, len(mode.Inputs))
	staged := 0
	for _, input := range mode.Inputs {
		if input.ID != "form_input_"+prefixFormIntakeDigest(input.MessageDigest) || seen[input.ID] ||
			!validDocumentDigest(
				input.MessageDigest,
			) || !validDocumentDigest(input.BodyDigest) || input.CreatedAt <= 0 ||
			input.Origin.Version != FormIntakeVersion || input.Origin.JobID != record.JobID || input.Origin.ModeID != mode.ID ||
			input.Origin.SchemaDigest != mode.SchemaDigest || input.Origin.ExpectedRevision < mode.StartRevision ||
			input.Origin.ExpectedRevision > record.Revision || !validFormIntakeContext(input.Origin.Context) ||
			len(input.Reservations) > MaxFormIntakeExtractionPasses ||
			(input.Intent != interactions.ProtectedAnswerValue && input.Intent != interactions.ProtectedAnswerClarify &&
				input.Intent != interactions.ProtectedAnswerBack) {
			return ErrFormJobRecordCorrupt
		}
		seen[input.ID] = true
		if err := validateFormIntakeAccounting(record, input); err != nil {
			return err
		}
		switch input.State {
		case formIntakeInputStaged:
			staged++
			if input.InteractionID == "" || input.CommittedAt != 0 || len(input.Reservations) != 0 ||
				input.Origin.ExpectedRevision != record.Revision || mode.State != FormIntakeActive {
				return ErrFormJobRecordCorrupt
			}
		case formIntakeInputCommitted, formIntakeInputConsumed:
			if input.CommittedAt < input.CreatedAt || input.State == formIntakeInputConsumed && input.Text != "" {
				return ErrFormJobRecordCorrupt
			}
		default:
			return ErrFormJobRecordCorrupt
		}
		if input.State != formIntakeInputConsumed && !validFormIntakeText(input.Intent, input.Text) {
			return ErrFormJobRecordCorrupt
		}
		seenReservations := make(map[string]bool, len(input.Reservations))
		for _, reservation := range input.Reservations {
			if !validDocumentDigest(reservation.KeyDigest) ||
				reservation.GroupDigest != "" && !validDocumentDigest(reservation.GroupDigest) ||
				reservation.ReceiptCount < 0 || reservation.ReceiptCount > len(mode.Inputs) ||
				reservation.ID != "form_pass_"+prefixFormIntakeDigest(reservation.KeyDigest) ||
				seenReservations[reservation.ID] || reservation.Revision <= 0 || reservation.Revision > record.Revision ||
				!validFormIntakeCandidateIDs(reservation.CandidateIDs) {
				return ErrFormJobRecordCorrupt
			}
			seenReservations[reservation.ID] = true
			if err := validateFormIntakeOutcome(record, reservation); err != nil {
				return err
			}
		}
	}
	progress := formIntakeProgress(record, mode)
	for _, route := range mode.TaskRoutes {
		for _, answer := range route.Answers {
			if answer.State == "staged" {
				staged++
			}
		}
	}
	if err := validateFormIntakePriorResolutions(mode); err != nil {
		return err
	}
	if staged > 1 || progress.PendingInputs > MaxFormIntakePendingInputs ||
		progress.RetainedBytes > MaxFormIntakeRetainedBytes ||
		mode.State == FormIntakeClosed && progress.PendingInputs != 0 {
		return ErrFormJobRecordCorrupt
	}
	return nil
}

func validFormIntakeCandidateIDs(ids []string) bool {
	return validFormIntakeContext(FormIntakeContext{CandidateFieldIDs: ids, Question: "Candidate view"})
}

func prefixFormIntakeDigest(digest string) string {
	if len(digest) < 32 {
		return ""
	}
	return digest[:32]
}

// requireSettledFormIntake is also used at native review/final gates, not just
// by conversational code. Uninterpreted or unclaimed input is never lost by finalizing.
func requireSettledFormIntake(record formJobStoredRecord, key []byte) error {
	if record.Public.State.terminal() {
		return nil
	}
	if len(record.PendingEvents) != 0 {
		return ErrFormJobConflict
	}
	source, err := readFormIntakeSource(record, key)
	if err != nil {
		return err
	}
	if source.Intake != nil {
		progress := formIntakeProgress(record.Public, source.Intake)
		if progress.PendingInputs != 0 || progress.PendingTasks != 0 {
			return ErrFormJobConflict
		}
	}
	return nil
}

func requireNoStagedFormIntake(record formJobStoredRecord, key []byte) error {
	source, err := readFormIntakeSource(record, key)
	if err != nil {
		return err
	}
	if hasStagedFormIntake(source.Intake) {
		return ErrFormJobConflict
	}
	return nil
}

func hasStagedFormIntake(mode *formIntakeStoredMode) bool {
	if mode == nil {
		return false
	}
	for _, input := range mode.Inputs {
		if input.State == formIntakeInputStaged {
			return true
		}
	}
	for _, route := range mode.TaskRoutes {
		for _, answer := range route.Answers {
			if answer.State == "staged" {
				return true
			}
		}
	}
	return false
}
