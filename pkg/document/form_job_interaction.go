package document

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

const (
	FormProtectedAnswerNamespace = "document.form.v1"
	maxFormProtectedBindingBytes = 16 * 1024
	protectedAnswerPendingGrace  = 30 * time.Second
)

type FormProtectedAnswerBindingRequest struct {
	JobID             string
	ExpectedRevision  int64
	Owner             FormJobOwner
	FieldID           string
	SupersedesEventID string
}

type formProtectedAnswerBindingPayload struct {
	JobID             string `json:"job_id"`
	ExpectedRevision  int64  `json:"expected_revision"`
	FieldID           string `json:"field_id"`
	SupersedesEventID string `json:"supersedes_event_id,omitempty"`
}

// NewProtectedAnswerBinding creates an authority-checked opaque token for one
// exact form field revision. The token contains no answer value.
func (store *FormJobStore) NewProtectedAnswerBinding(
	ctx context.Context,
	request FormProtectedAnswerBindingRequest,
) (interactions.ProtectedAnswerBinding, error) {
	if err := validateFormProtectedAnswerBindingRequest(request); err != nil {
		return interactions.ProtectedAnswerBinding{}, err
	}
	var token string
	err := store.update(ctx, func(document *formJobStoreDocument, _ time.Time) (bool, error) {
		record, jobKey, err := store.authorizedRecord(document, request.JobID, request.Owner)
		if err != nil {
			return false, err
		}
		defer clear(jobKey)
		if record.Public.State == FormJobExpired {
			return false, ErrFormJobExpired
		}
		if record.Public.State.terminal() {
			return false, ErrFormJobTerminal
		}
		if record.Public.Revision != request.ExpectedRevision {
			return false, ErrFormJobConflict
		}
		if request.SupersedesEventID != "" {
			current := slices.IndexFunc(record.Public.Fields, func(field FormJobFieldState) bool {
				return field.FieldID == request.FieldID
			})
			if current < 0 || record.Public.Fields[current].EventID != request.SupersedesEventID {
				return false, ErrFormJobConflict
			}
		}
		payload := formProtectedAnswerBindingPayload{
			JobID:             strings.TrimSpace(request.JobID),
			ExpectedRevision:  request.ExpectedRevision,
			FieldID:           strings.TrimSpace(request.FieldID),
			SupersedesEventID: strings.TrimSpace(request.SupersedesEventID),
		}
		envelope, err := sealFormJobEnvelope(jobKey, formJobEnvelope{
			Kind:     "answer_binding",
			JobID:    payload.JobID,
			FieldID:  payload.FieldID,
			Revision: payload.ExpectedRevision,
		}, payload, store.random)
		if err != nil {
			return false, fmt.Errorf("seal document protected answer binding: %w", err)
		}
		encoded, err := json.Marshal(envelope)
		if err != nil {
			return false, err
		}
		defer clear(encoded)
		token = base64.RawURLEncoding.EncodeToString(encoded)
		return false, nil
	})
	if err != nil {
		return interactions.ProtectedAnswerBinding{}, err
	}
	return interactions.ProtectedAnswerBinding{Namespace: FormProtectedAnswerNamespace, Token: token}, nil
}

func validateFormProtectedAnswerBindingRequest(request FormProtectedAnswerBindingRequest) error {
	if _, err := request.Owner.canonical(); err != nil {
		return err
	}
	if strings.TrimSpace(request.JobID) == "" || len(request.JobID) > maxFormJobIDLength ||
		request.ExpectedRevision <= 0 || strings.TrimSpace(request.FieldID) == "" ||
		len(request.FieldID) > maxFormJobFieldIDLength || !utf8.ValidString(request.FieldID) ||
		len(request.SupersedesEventID) > maxFormJobEventIDLength {
		return errors.New("document protected answer binding request is invalid")
	}
	return nil
}

type FormProtectedAnswerSink struct {
	store *FormJobStore
}

func NewFormProtectedAnswerSink(store *FormJobStore) (*FormProtectedAnswerSink, error) {
	if store == nil {
		return nil, ErrFormJobStoreUnavailable
	}
	return &FormProtectedAnswerSink{store: store}, nil
}

func (*FormProtectedAnswerSink) Namespace() string { return FormProtectedAnswerNamespace }

// FormJobStore returns the process-owned store shared with the document tool.
// The sink remains the lifetime owner.
func (sink *FormProtectedAnswerSink) FormJobStore() *FormJobStore {
	if sink == nil {
		return nil
	}
	return sink.store
}

func (sink *FormProtectedAnswerSink) Close() {
	if sink == nil || sink.store == nil {
		return
	}
	sink.store.Close()
}

func (sink *FormProtectedAnswerSink) Accept(
	ctx context.Context,
	request interactions.ProtectedAnswerSinkRequest,
) (interactions.ProtectedAnswerReceipt, error) {
	if sink == nil || sink.store == nil || request.Binding.Namespace != FormProtectedAnswerNamespace ||
		strings.TrimSpace(request.InteractionID) == "" || strings.TrimSpace(request.IdempotencyKey) == "" {
		return interactions.ProtectedAnswerReceipt{}, ErrFormJobStoreUnavailable
	}
	owner, err := formJobOwnerFromProtectedRequest(request.Workspace, request.Route)
	if err != nil {
		return interactions.ProtectedAnswerReceipt{}, err
	}
	payload, err := sink.store.openProtectedAnswerBinding(ctx, request.Binding.Token, owner)
	if err != nil {
		return interactions.ProtectedAnswerReceipt{}, err
	}
	appendRequest := FormJobAppendValueRequest{
		JobID:             payload.JobID,
		ExpectedRevision:  payload.ExpectedRevision,
		Owner:             owner,
		FieldID:           payload.FieldID,
		IdempotencyKey:    request.IdempotencyKey,
		Source:            FormValueSourceUser,
		SupersedesEventID: payload.SupersedesEventID,
	}
	switch request.Intent {
	case interactions.ProtectedAnswerValue:
		appendRequest.Value = FormProtectedValue{Kind: ProtectedValueText, Text: request.Text}
		appendRequest.State = FormValueSupplied
	case interactions.ProtectedAnswerSkip:
		appendRequest.Value = FormProtectedValue{Kind: ProtectedValueBlank}
		appendRequest.State = FormValueBlanked
		appendRequest.BlankReason = FormBlankSkipped
	case interactions.ProtectedAnswerNotApplicable:
		appendRequest.Value = FormProtectedValue{Kind: ProtectedValueBlank}
		appendRequest.State = FormValueNotApplicable
		appendRequest.BlankReason = FormBlankNotApplicable
	default:
		return interactions.ProtectedAnswerReceipt{}, ErrFormJobAnswerConflict
	}
	_, event, err := sink.store.stageProtectedValue(ctx, appendRequest)
	if err != nil {
		return interactions.ProtectedAnswerReceipt{}, err
	}
	return interactions.ProtectedAnswerReceipt{
		Reference: event.EventID,
		State:     "stored",
	}, nil
}

func (sink *FormProtectedAnswerSink) Commit(
	ctx context.Context,
	request interactions.ProtectedAnswerCommitRequest,
) error {
	if sink == nil || sink.store == nil || request.Binding.Namespace != FormProtectedAnswerNamespace ||
		strings.TrimSpace(request.InteractionID) == "" || strings.TrimSpace(request.Receipt.Reference) == "" ||
		request.Receipt.State != "stored" {
		return ErrFormJobStoreUnavailable
	}
	owner, err := formJobOwnerFromProtectedRequest(request.Workspace, request.Route)
	if err != nil {
		return err
	}
	payload, err := sink.store.openProtectedAnswerBinding(ctx, request.Binding.Token, owner)
	if err != nil {
		return err
	}
	return sink.store.commitProtectedValue(ctx, payload, owner, request.Receipt.Reference)
}

func (sink *FormProtectedAnswerSink) Discard(
	ctx context.Context,
	request interactions.ProtectedAnswerDiscardRequest,
) error {
	if sink == nil || sink.store == nil || request.Binding.Namespace != FormProtectedAnswerNamespace ||
		strings.TrimSpace(request.InteractionID) == "" {
		return ErrFormJobStoreUnavailable
	}
	owner, err := formJobOwnerFromProtectedRequest(request.Workspace, request.Route)
	if err != nil {
		return err
	}
	payload, err := sink.store.openProtectedAnswerBinding(ctx, request.Binding.Token, owner)
	if err != nil {
		return err
	}
	eventID := ""
	if request.Receipt != nil {
		eventID = strings.TrimSpace(request.Receipt.Reference)
	}
	return sink.store.discardProtectedValue(ctx, payload, owner, eventID, request.Force)
}

func (store *FormJobStore) stageProtectedValue(
	ctx context.Context,
	request FormJobAppendValueRequest,
) (FormJobRecord, FormJobValueEvent, error) {
	if err := validateFormJobAppendValueRequest(request); err != nil {
		return FormJobRecord{}, FormJobValueEvent{}, err
	}
	var result FormJobRecord
	var eventResult FormJobValueEvent
	err := store.update(ctx, func(document *formJobStoreDocument, now time.Time) (bool, error) {
		record, jobKey, err := store.authorizedRecord(document, request.JobID, request.Owner)
		if err != nil {
			return false, err
		}
		defer clear(jobKey)
		idempotencyDigest := keyedDigest(store.profileKey, "answer", record.Public.JobID, request.IdempotencyKey)
		eventID := "form_value_" + idempotencyDigest[:32]
		bindingDigest, err := formJobJSONKeyedDigest(store.profileKey, struct {
			FieldID           string
			Value             FormProtectedValue
			State             FormValueState
			Source            FormValueSource
			BlankReason       FormBlankReason
			ValidationCode    string
			SupersedesEventID string
		}{
			FieldID: request.FieldID, Value: request.Value, State: request.State, Source: request.Source,
			BlankReason: request.BlankReason, ValidationCode: request.ValidationCode,
			SupersedesEventID: request.SupersedesEventID,
		})
		if err != nil {
			return false, err
		}
		for _, envelopes := range [][]formJobEnvelope{record.Events, record.PendingEvents} {
			for _, envelope := range envelopes {
				if envelope.EventID != eventID {
					continue
				}
				if envelope.BindingDigest != bindingDigest {
					return false, ErrFormJobAnswerConflict
				}
				payload, payloadErr := openFormJobValuePayload(jobKey, envelope)
				if payloadErr != nil {
					return false, payloadErr
				}
				result = cloneFormJobRecord(record.Public)
				eventResult = publicFormJobValueEvent(payload)
				return false, nil
			}
		}
		if record.Public.State == FormJobExpired {
			return false, ErrFormJobExpired
		}
		if record.Public.State.terminal() {
			return false, ErrFormJobTerminal
		}
		if record.Public.State != FormJobPrepared && record.Public.State != FormJobCollecting &&
			record.Public.State != FormJobReviewReady {
			return false, ErrFormJobConflict
		}
		if request.ExpectedRevision != record.Public.Revision || len(record.PendingEvents) != 0 {
			return false, ErrFormJobConflict
		}
		if len(record.Events)+len(record.PendingEvents) >= store.maxEventsPerJob {
			return false, ErrFormJobCapacityExceeded
		}
		currentIndex := slices.IndexFunc(record.Public.Fields, func(field FormJobFieldState) bool {
			return field.FieldID == request.FieldID
		})
		if request.SupersedesEventID != "" {
			if currentIndex < 0 || record.Public.Fields[currentIndex].EventID != request.SupersedesEventID {
				return false, ErrFormJobConflict
			}
		} else if currentIndex >= 0 {
			return false, ErrFormJobConflict
		}
		payload := formJobValuePayload{
			EventID: eventID, FieldID: request.FieldID, Revision: record.Public.Revision + 1,
			Value: cloneProtectedValue(request.Value), State: request.State, Source: request.Source,
			BlankReason: request.BlankReason, ValidationCode: strings.TrimSpace(request.ValidationCode),
			SupersedesEventID: strings.TrimSpace(request.SupersedesEventID),
			IdempotencyDigest: idempotencyDigest, PreviousDigest: record.Public.LedgerDigest,
			CreatedAt: now.UnixMilli(),
		}
		envelope, err := sealFormJobEnvelope(jobKey, formJobEnvelope{
			Kind: "pending_value", JobID: record.Public.JobID, FieldID: request.FieldID,
			EventID: eventID, Revision: payload.Revision, BindingDigest: bindingDigest,
		}, payload, store.random)
		if err != nil {
			return false, fmt.Errorf("seal pending document form value: %w", err)
		}
		record.PendingEvents = append(record.PendingEvents, envelope)
		document.Records[record.Public.JobID] = record
		result = cloneFormJobRecord(record.Public)
		eventResult = publicFormJobValueEvent(payload)
		return true, nil
	})
	return result, eventResult, err
}

func (store *FormJobStore) commitProtectedValue(
	ctx context.Context,
	binding formProtectedAnswerBindingPayload,
	owner FormJobOwner,
	eventID string,
) error {
	return store.update(ctx, func(document *formJobStoreDocument, now time.Time) (bool, error) {
		record, jobKey, err := store.authorizedRecord(document, binding.JobID, owner)
		if err != nil {
			return false, err
		}
		defer clear(jobKey)
		for _, envelope := range record.Events {
			if envelope.EventID != eventID {
				continue
			}
			payload, payloadErr := openFormJobValuePayload(jobKey, envelope)
			if payloadErr != nil || !protectedValueMatchesBinding(payload, binding) {
				return false, ErrFormJobAnswerConflict
			}
			return false, nil
		}
		pendingIndex := slices.IndexFunc(record.PendingEvents, func(envelope formJobEnvelope) bool {
			return envelope.EventID == eventID
		})
		if pendingIndex < 0 || record.Public.Revision != binding.ExpectedRevision {
			return false, ErrFormJobConflict
		}
		pending := record.PendingEvents[pendingIndex]
		payload, err := openFormJobValuePayload(jobKey, pending)
		if err != nil || !protectedValueMatchesBinding(payload, binding) ||
			payload.PreviousDigest != record.Public.LedgerDigest {
			return false, ErrFormJobAnswerConflict
		}
		envelope, err := sealFormJobEnvelope(jobKey, formJobEnvelope{
			Kind: "value", JobID: record.Public.JobID, FieldID: payload.FieldID,
			EventID: payload.EventID, Revision: payload.Revision, BindingDigest: pending.BindingDigest,
		}, payload, store.random)
		if err != nil {
			return false, fmt.Errorf("commit protected document form value: %w", err)
		}
		record.PendingEvents = slices.Delete(record.PendingEvents, pendingIndex, pendingIndex+1)
		record.Events = append(record.Events, envelope)
		record.Public.State = FormJobCollecting
		record.Public.Revision = payload.Revision
		record.Public.LedgerRevision++
		record.Public.LedgerDigest, err = formJobJSONDigest(envelope)
		if err != nil {
			return false, err
		}
		record.Public.UpdatedAt = now.UnixMilli()
		fieldState := FormJobFieldState{
			FieldID: payload.FieldID, EventID: payload.EventID, ValueKind: payload.Value.Kind,
			State: payload.State, Source: payload.Source, BlankReason: payload.BlankReason,
			ValidationCode: payload.ValidationCode, UpdatedAt: now.UnixMilli(),
		}
		currentIndex := slices.IndexFunc(record.Public.Fields, func(field FormJobFieldState) bool {
			return field.FieldID == payload.FieldID
		})
		if currentIndex >= 0 {
			record.Public.Fields[currentIndex] = fieldState
		} else {
			record.Public.Fields = append(record.Public.Fields, fieldState)
			slices.SortFunc(record.Public.Fields, func(left, right FormJobFieldState) int {
				return cmp.Compare(left.FieldID, right.FieldID)
			})
		}
		document.Records[record.Public.JobID] = record
		return true, nil
	})
}

func (store *FormJobStore) discardProtectedValue(
	ctx context.Context,
	binding formProtectedAnswerBindingPayload,
	owner FormJobOwner,
	eventID string,
	force bool,
) error {
	return store.update(ctx, func(document *formJobStoreDocument, now time.Time) (bool, error) {
		record, jobKey, err := store.authorizedRecord(document, binding.JobID, owner)
		if err != nil {
			return false, err
		}
		defer clear(jobKey)
		pendingIndex := slices.IndexFunc(record.PendingEvents, func(envelope formJobEnvelope) bool {
			return envelope.FieldID == binding.FieldID && envelope.Revision == binding.ExpectedRevision+1 &&
				(eventID == "" || envelope.EventID == eventID)
		})
		if pendingIndex < 0 {
			return false, nil
		}
		payload, err := openFormJobValuePayload(jobKey, record.PendingEvents[pendingIndex])
		if err != nil || !protectedValueMatchesBinding(payload, binding) {
			return false, ErrFormJobAnswerConflict
		}
		if !force && now.Sub(time.UnixMilli(payload.CreatedAt)) < protectedAnswerPendingGrace {
			return false, nil
		}
		record.PendingEvents = slices.Delete(record.PendingEvents, pendingIndex, pendingIndex+1)
		document.Records[record.Public.JobID] = record
		return true, nil
	})
}

func protectedValueMatchesBinding(payload formJobValuePayload, binding formProtectedAnswerBindingPayload) bool {
	return payload.FieldID == binding.FieldID && payload.Revision == binding.ExpectedRevision+1 &&
		payload.SupersedesEventID == binding.SupersedesEventID
}

func (sink *FormProtectedAnswerSink) Cancel(
	ctx context.Context,
	request interactions.ProtectedAnswerCancelRequest,
) error {
	if sink == nil || sink.store == nil || request.Binding.Namespace != FormProtectedAnswerNamespace ||
		strings.TrimSpace(request.InteractionID) == "" || strings.TrimSpace(request.IdempotencyKey) == "" {
		return ErrFormJobStoreUnavailable
	}
	owner, err := formJobOwnerFromProtectedRequest(request.Workspace, request.Route)
	if err != nil {
		return err
	}
	envelope, err := decodeFormProtectedAnswerBinding(request.Binding.Token)
	if err != nil {
		return err
	}
	public, err := sink.store.Get(ctx, envelope.JobID, owner)
	if err != nil {
		return err
	}
	if public.State == FormJobCanceled {
		if public.Revision == envelope.Revision+1 || public.Revision == envelope.Revision+2 {
			return nil
		}
		return ErrFormJobTerminal
	}
	payload, err := sink.store.openProtectedAnswerBinding(ctx, request.Binding.Token, owner)
	if err != nil {
		return err
	}
	cancelRevision := payload.ExpectedRevision
	if public.Revision == payload.ExpectedRevision+1 {
		fieldIndex := slices.IndexFunc(public.Fields, func(field FormJobFieldState) bool {
			return field.FieldID == payload.FieldID
		})
		if fieldIndex < 0 || (payload.SupersedesEventID != "" &&
			public.Fields[fieldIndex].EventID == payload.SupersedesEventID) {
			return ErrFormJobConflict
		}
		cancelRevision = public.Revision
	} else if public.Revision != payload.ExpectedRevision {
		return ErrFormJobConflict
	}
	_, err = sink.store.Cancel(ctx, payload.JobID, cancelRevision, owner)
	return err
}

func (store *FormJobStore) openProtectedAnswerBinding(
	ctx context.Context,
	token string,
	owner FormJobOwner,
) (formProtectedAnswerBindingPayload, error) {
	var payload formProtectedAnswerBindingPayload
	envelope, err := decodeFormProtectedAnswerBinding(token)
	if err != nil {
		return payload, err
	}
	err = store.update(ctx, func(document *formJobStoreDocument, _ time.Time) (bool, error) {
		record, jobKey, recordErr := store.authorizedRecord(document, envelope.JobID, owner)
		if recordErr != nil {
			return false, recordErr
		}
		defer clear(jobKey)
		if openErr := openFormJobEnvelope(jobKey, envelope, &payload); openErr != nil {
			return false, openErr
		}
		if payload.JobID != envelope.JobID || payload.FieldID != envelope.FieldID ||
			payload.ExpectedRevision != envelope.Revision || validateFormProtectedAnswerBindingRequest(
			FormProtectedAnswerBindingRequest{
				JobID: payload.JobID, ExpectedRevision: payload.ExpectedRevision, Owner: owner,
				FieldID: payload.FieldID, SupersedesEventID: payload.SupersedesEventID,
			},
		) != nil {
			return false, ErrFormJobRecordCorrupt
		}
		if record.Public.State == FormJobExpired {
			return false, ErrFormJobExpired
		}
		if record.Public.State.terminal() {
			return false, ErrFormJobTerminal
		}
		return false, nil
	})
	return payload, err
}

func decodeFormProtectedAnswerBinding(token string) (formJobEnvelope, error) {
	var envelope formJobEnvelope
	if strings.TrimSpace(token) != token || token == "" || len(token) > maxFormProtectedBindingBytes*2 {
		return envelope, ErrFormJobRecordCorrupt
	}
	encoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(encoded) > maxFormProtectedBindingBytes {
		return envelope, ErrFormJobRecordCorrupt
	}
	defer clear(encoded)
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return envelope, ErrFormJobRecordCorrupt
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return envelope, ErrFormJobRecordCorrupt
	}
	if envelope.Kind != "answer_binding" || strings.TrimSpace(envelope.JobID) == "" ||
		strings.TrimSpace(envelope.FieldID) == "" || envelope.Revision <= 0 {
		return envelope, ErrFormJobRecordCorrupt
	}
	return envelope, nil
}

func formJobOwnerFromProtectedRequest(workspace string, route interactions.Route) (FormJobOwner, error) {
	routeSessionKey := strings.TrimSpace(route.RouteSessionKey)
	if routeSessionKey == "" {
		routeSessionKey = strings.TrimSpace(route.SessionKey)
	}
	owner := FormJobOwner{
		AgentID:         route.AgentID,
		WorkspaceID:     workspace,
		RouteSessionKey: routeSessionKey,
		Channel:         route.Channel,
		AccountID:       route.AccountID,
		ChatID:          route.ChatID,
		ChatType:        route.ChatType,
		SenderID:        route.SenderID,
		TopicID:         route.TopicID,
		SpaceID:         route.SpaceID,
		SpaceType:       route.SpaceType,
	}
	if _, err := owner.canonical(); err != nil {
		return FormJobOwner{}, err
	}
	return owner, nil
}
