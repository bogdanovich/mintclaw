package document

import (
	"bytes"
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

const (
	FormProtectedAnswerNamespace         = "document.form.v1"
	formProtectedReceiptPrefix           = "form_answer."
	formProtectedNavigationReceiptPrefix = "form_navigation."
	maxFormProtectedBindingBytes         = 16 * 1024
	protectedAnswerPendingGrace          = 30 * time.Second
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
	if request.Intent == interactions.ProtectedAnswerClarify || request.Intent == interactions.ProtectedAnswerBack {
		reference, referenceErr := sink.store.issueFormProtectedNavigationReference(
			ctx,
			request.Intent,
			payload.JobID,
			payload.FieldID,
			payload.SupersedesEventID,
			payload.ExpectedRevision,
			owner,
			request.InteractionID,
			request.IdempotencyKey,
		)
		if referenceErr != nil {
			return interactions.ProtectedAnswerReceipt{}, referenceErr
		}
		return interactions.ProtectedAnswerReceipt{Reference: reference, State: "stored"}, nil
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
	reference, err := FormProtectedAnswerReference(payload.JobID, event.EventID)
	if err != nil {
		return interactions.ProtectedAnswerReceipt{}, err
	}
	return interactions.ProtectedAnswerReceipt{
		Reference: reference,
		State:     "stored",
	}, nil
}

// FormProtectedAnswerReference binds the safe public job identity to one
// protected value event. The interaction continuation can pass this opaque
// reference back to the document tool without recovering either identity from
// model memory or persisting the protected answer.
func FormProtectedAnswerReference(jobID, eventID string) (string, error) {
	jobID = strings.TrimSpace(jobID)
	eventID = strings.TrimSpace(eventID)
	if !safeFormJobCodePattern.MatchString(jobID) || !safeFormJobCodePattern.MatchString(eventID) ||
		len(jobID) > maxFormJobIDLength || len(eventID) > maxFormJobEventIDLength {
		return "", errors.New("document protected answer reference is invalid")
	}
	reference := formProtectedReceiptPrefix + jobID + "." + eventID
	if len(reference) > interactions.MaxProtectedReference {
		return "", errors.New("document protected answer reference is invalid")
	}
	return reference, nil
}

// ParseFormProtectedAnswerReference validates and separates the opaque receipt
// returned by FormProtectedAnswerReference.
func ParseFormProtectedAnswerReference(reference string) (string, string, error) {
	reference = strings.TrimSpace(reference)
	if !strings.HasPrefix(reference, formProtectedReceiptPrefix) ||
		len(reference) > interactions.MaxProtectedReference {
		return "", "", errors.New("document protected answer reference is invalid")
	}
	parts := strings.Split(strings.TrimPrefix(reference, formProtectedReceiptPrefix), ".")
	if len(parts) != 2 {
		return "", "", errors.New("document protected answer reference is invalid")
	}
	jobID, eventID := parts[0], parts[1]
	canonical, err := FormProtectedAnswerReference(jobID, eventID)
	if err != nil || canonical != reference {
		return "", "", errors.New("document protected answer reference is invalid")
	}
	return jobID, eventID, nil
}

// FormProtectedNavigationReferenceParts is the safe, value-free projection of
// one authenticated protected-form navigation receipt.
type FormProtectedNavigationReferenceParts struct {
	Action      interactions.ProtectedAnswerIntent
	JobID       string
	FieldDigest string
	Revision    int64
	IssueID     string
	mac         string
}

func (store *FormJobStore) newFormProtectedNavigationReference(
	action interactions.ProtectedAnswerIntent,
	jobID string,
	fieldID string,
	revision int64,
	issueID string,
) (string, error) {
	jobID = strings.TrimSpace(jobID)
	fieldID = strings.TrimSpace(fieldID)
	issueID = strings.TrimSpace(issueID)
	if store == nil || len(store.profileKey) == 0 ||
		(action != interactions.ProtectedAnswerClarify && action != interactions.ProtectedAnswerBack) ||
		!safeFormJobCodePattern.MatchString(jobID) || len(jobID) > maxFormJobIDLength ||
		fieldID == "" || len(fieldID) > maxFormJobFieldIDLength || !utf8.ValidString(fieldID) || revision <= 0 ||
		!validLowerHex(issueID, formJobNavigationIssueIDLength) {
		return "", errors.New("document protected navigation reference is invalid")
	}
	revisionText := strconv.FormatInt(revision, 10)
	fieldDigest := digestBytes([]byte(fieldID))
	mac := keyedDigest(
		store.profileKey,
		"protected_navigation",
		string(action),
		jobID,
		fieldID,
		revisionText,
		issueID,
	)
	reference := strings.Join([]string{
		strings.TrimSuffix(formProtectedNavigationReceiptPrefix, "."),
		string(action),
		jobID,
		fieldDigest,
		revisionText,
		issueID,
		mac,
	}, ".")
	if len(reference) > interactions.MaxProtectedReference {
		return "", errors.New("document protected navigation reference is invalid")
	}
	return reference, nil
}

func (store *FormJobStore) issueFormProtectedNavigationReference(
	ctx context.Context,
	action interactions.ProtectedAnswerIntent,
	jobID string,
	currentFieldID string,
	supersedesEventID string,
	revision int64,
	owner FormJobOwner,
	interactionID string,
	idempotencyKey string,
) (string, error) {
	interactionID = strings.TrimSpace(interactionID)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if interactionID == "" || idempotencyKey == "" {
		return "", ErrFormJobAnswerConflict
	}
	issueID := keyedDigest(
		store.profileKey,
		"protected_navigation_issue",
		jobID,
		interactionID,
		idempotencyKey,
	)[:formJobNavigationIssueIDLength]
	interactionDigest := keyedDigest(store.profileKey, "protected_navigation_interaction", jobID, interactionID)
	reference := ""
	err := store.update(ctx, func(document *formJobStoreDocument, now time.Time) (bool, error) {
		record, err := store.authorizedPublicRecord(document, jobID, owner)
		if err != nil {
			return false, err
		}
		if record.Public.State.terminal() || record.Public.Revision != revision {
			return false, ErrFormJobAnswerConflict
		}
		beforePrune := len(record.NavigationReceipts)
		record.NavigationReceipts = slices.DeleteFunc(
			record.NavigationReceipts,
			func(receipt formJobNavigationReceipt) bool { return receipt.Revision < revision },
		)
		pruned := len(record.NavigationReceipts) != beforePrune
		fieldID, targetErr := FormProtectedNavigationTarget(
			cloneStoredFormJobPublic(record),
			currentFieldID,
			supersedesEventID,
			action,
		)
		if targetErr != nil {
			return false, ErrFormJobAnswerConflict
		}
		reference, err = store.newFormProtectedNavigationReference(action, jobID, fieldID, revision, issueID)
		if err != nil {
			return false, err
		}
		referenceDigest := digestBytes([]byte(reference))
		fieldDigest := digestBytes([]byte(fieldID))
		for _, existing := range record.NavigationReceipts {
			if existing.ReferenceDigest == referenceDigest {
				if existing.IssueID != issueID || existing.Action != action ||
					existing.FieldDigest != fieldDigest || existing.Revision != revision ||
					existing.InteractionDigest != interactionDigest {
					return false, ErrFormJobAnswerConflict
				}
				if pruned {
					document.Records[jobID] = record
				}
				return pruned, nil
			}
			if existing.IssueID == issueID {
				return false, ErrFormJobAnswerConflict
			}
		}
		if len(record.NavigationReceipts) >= maxFormJobNavigationReceipts {
			return false, ErrFormJobCapacityExceeded
		}
		record.NavigationReceipts = append(record.NavigationReceipts, formJobNavigationReceipt{
			ReferenceDigest:   referenceDigest,
			IssueID:           issueID,
			Action:            action,
			FieldDigest:       fieldDigest,
			Revision:          revision,
			InteractionDigest: interactionDigest,
			IssuedAt:          now.UnixMilli(),
		})
		document.Records[jobID] = record
		return true, nil
	})
	if err != nil {
		return "", err
	}
	return reference, nil
}

// ParseFormProtectedNavigationReference validates the bounded public shape of
// a navigation receipt. Authenticity is checked by ResolveFormProtectedNavigationReference.
func ParseFormProtectedNavigationReference(reference string) (FormProtectedNavigationReferenceParts, error) {
	reference = strings.TrimSpace(reference)
	invalid := func() (FormProtectedNavigationReferenceParts, error) {
		return FormProtectedNavigationReferenceParts{}, errors.New("document protected navigation reference is invalid")
	}
	if !strings.HasPrefix(reference, formProtectedNavigationReceiptPrefix) ||
		len(reference) > interactions.MaxProtectedReference {
		return invalid()
	}
	parts := strings.Split(strings.TrimPrefix(reference, formProtectedNavigationReceiptPrefix), ".")
	if len(parts) != 6 {
		return invalid()
	}
	action := interactions.ProtectedAnswerIntent(parts[0])
	if action != interactions.ProtectedAnswerClarify && action != interactions.ProtectedAnswerBack {
		return invalid()
	}
	jobID, fieldDigest, revisionText, issueID, mac := parts[1], parts[2], parts[3], parts[4], parts[5]
	if !safeFormJobCodePattern.MatchString(jobID) || len(jobID) > maxFormJobIDLength ||
		len(fieldDigest) != sha256.Size*2 || !validLowerHex(issueID, formJobNavigationIssueIDLength) ||
		len(mac) != sha256.Size*2 {
		return invalid()
	}
	if _, err := hex.DecodeString(fieldDigest); err != nil {
		return invalid()
	}
	if _, err := hex.DecodeString(mac); err != nil {
		return invalid()
	}
	revision, err := strconv.ParseInt(revisionText, 10, 64)
	if err != nil || revision <= 0 || strconv.FormatInt(revision, 10) != revisionText {
		return invalid()
	}
	return FormProtectedNavigationReferenceParts{
		Action: action, JobID: jobID, FieldDigest: fieldDigest, Revision: revision, IssueID: issueID, mac: mac,
	}, nil
}

// ResolveFormProtectedNavigationReference authenticates a receipt and maps
// its field digest back to exactly one candidate from the current form schema.
func (store *FormJobStore) ResolveFormProtectedNavigationReference(
	reference string,
	record FormJobRecord,
	candidateFieldIDs []string,
) (interactions.ProtectedAnswerIntent, string, error) {
	parts, err := ParseFormProtectedNavigationReference(reference)
	if err != nil || store == nil || record.JobID != parts.JobID || record.Revision != parts.Revision {
		return "", "", ErrFormJobAnswerConflict
	}
	matchedFieldID := ""
	for _, candidate := range candidateFieldIDs {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || digestBytes([]byte(candidate)) != parts.FieldDigest {
			continue
		}
		if matchedFieldID != "" && matchedFieldID != candidate {
			return "", "", ErrFormJobAnswerConflict
		}
		matchedFieldID = candidate
	}
	if matchedFieldID == "" {
		return "", "", ErrFormJobAnswerConflict
	}
	expected, err := store.newFormProtectedNavigationReference(
		parts.Action,
		parts.JobID,
		matchedFieldID,
		parts.Revision,
		parts.IssueID,
	)
	if err != nil || !hmac.Equal([]byte(expected), []byte(reference)) {
		return "", "", ErrFormJobAnswerConflict
	}
	return parts.Action, matchedFieldID, nil
}

// FormProtectedNavigationTarget deterministically selects the field affected
// by a trusted navigation action without consulting protected values.
func FormProtectedNavigationTarget(
	record FormJobRecord,
	currentFieldID string,
	supersedesEventID string,
	action interactions.ProtectedAnswerIntent,
) (string, error) {
	currentFieldID = strings.TrimSpace(currentFieldID)
	if currentFieldID == "" {
		return "", ErrFormJobAnswerConflict
	}
	if action == interactions.ProtectedAnswerClarify {
		return currentFieldID, nil
	}
	if action != interactions.ProtectedAnswerBack {
		return "", ErrFormJobAnswerConflict
	}
	bestFieldID := ""
	bestRevision := int64(0)
	for _, field := range record.Fields {
		if strings.TrimSpace(supersedesEventID) != "" && field.FieldID == currentFieldID {
			continue
		}
		if field.Revision <= 0 {
			return "", ErrFormJobAnswerConflict
		}
		if bestFieldID == "" || field.Revision > bestRevision {
			bestFieldID = field.FieldID
			bestRevision = field.Revision
			continue
		}
		if field.Revision == bestRevision && field.FieldID != bestFieldID {
			return "", ErrFormJobAnswerConflict
		}
	}
	if bestFieldID == "" {
		return "", ErrFormJobAnswerConflict
	}
	return bestFieldID, nil
}

func (store *FormJobStore) commitFormProtectedNavigationReceipt(
	ctx context.Context,
	reference string,
	payload formProtectedAnswerBindingPayload,
	owner FormJobOwner,
	interactionID string,
) error {
	interactionID = strings.TrimSpace(interactionID)
	if interactionID == "" {
		return ErrFormJobAnswerConflict
	}
	return store.update(ctx, func(document *formJobStoreDocument, now time.Time) (bool, error) {
		record, err := store.authorizedPublicRecord(document, payload.JobID, owner)
		if err != nil {
			return false, err
		}
		public := cloneStoredFormJobPublic(record)
		parts, err := ParseFormProtectedNavigationReference(reference)
		if err != nil || parts.JobID != payload.JobID || parts.Revision != payload.ExpectedRevision ||
			public.Revision != payload.ExpectedRevision {
			return false, ErrFormJobAnswerConflict
		}
		targetFieldID, err := FormProtectedNavigationTarget(
			public,
			payload.FieldID,
			payload.SupersedesEventID,
			parts.Action,
		)
		if err != nil {
			return false, err
		}
		_, resolvedFieldID, err := store.ResolveFormProtectedNavigationReference(
			reference,
			public,
			[]string{targetFieldID},
		)
		if err != nil || resolvedFieldID != targetFieldID {
			return false, ErrFormJobAnswerConflict
		}
		index := storedFormNavigationReceiptIndex(record.NavigationReceipts, reference)
		if index < 0 {
			return false, ErrFormJobAnswerConflict
		}
		stored := &record.NavigationReceipts[index]
		interactionDigest := keyedDigest(
			store.profileKey,
			"protected_navigation_interaction",
			payload.JobID,
			interactionID,
		)
		if stored.InteractionDigest != interactionDigest || stored.IssueID != parts.IssueID ||
			stored.Action != parts.Action || stored.FieldDigest != parts.FieldDigest ||
			stored.Revision != parts.Revision {
			return false, ErrFormJobAnswerConflict
		}
		if stored.CommittedAt != 0 {
			return false, nil
		}
		stored.CommittedAt = now.UnixMilli()
		document.Records[payload.JobID] = record
		return true, nil
	})
}

// ConsumeFormProtectedNavigationReference consumes one committed navigation
// authority for one stable provider tool call. A restart of that same call is
// idempotent; every different call, including one in the same turn, fails closed.
func (store *FormJobStore) ConsumeFormProtectedNavigationReference(
	ctx context.Context,
	reference string,
	owner FormJobOwner,
	candidateFieldIDs []string,
	consumerExecutionID string,
	consumerToolCallID string,
) (interactions.ProtectedAnswerIntent, string, error) {
	parts, err := ParseFormProtectedNavigationReference(reference)
	consumerExecutionID = strings.TrimSpace(consumerExecutionID)
	consumerToolCallID = strings.TrimSpace(consumerToolCallID)
	if err != nil || consumerExecutionID == "" || consumerToolCallID == "" {
		return "", "", ErrFormJobAnswerConflict
	}
	var resolvedAction interactions.ProtectedAnswerIntent
	var resolvedFieldID string
	err = store.update(ctx, func(document *formJobStoreDocument, now time.Time) (bool, error) {
		record, recordErr := store.authorizedPublicRecord(document, parts.JobID, owner)
		if recordErr != nil {
			return false, recordErr
		}
		public := cloneStoredFormJobPublic(record)
		action, fieldID, resolveErr := store.ResolveFormProtectedNavigationReference(
			reference,
			public,
			candidateFieldIDs,
		)
		if resolveErr != nil {
			return false, resolveErr
		}
		index := storedFormNavigationReceiptIndex(record.NavigationReceipts, reference)
		if index < 0 {
			return false, ErrFormJobAnswerConflict
		}
		stored := &record.NavigationReceipts[index]
		if stored.CommittedAt == 0 || stored.IssueID != parts.IssueID || stored.Action != action ||
			stored.FieldDigest != parts.FieldDigest || stored.Revision != parts.Revision {
			return false, ErrFormJobAnswerConflict
		}
		ownerDigest, ownerErr := store.ownerDigest(owner)
		if ownerErr != nil {
			return false, ownerErr
		}
		consumerDigest := keyedDigest(
			store.profileKey,
			"protected_navigation_consumer",
			parts.JobID,
			parts.IssueID,
			ownerDigest,
			consumerExecutionID,
			consumerToolCallID,
		)
		if stored.ConsumerDigest != "" {
			if stored.ConsumerDigest != consumerDigest {
				return false, ErrFormJobAnswerConflict
			}
			resolvedAction = action
			resolvedFieldID = fieldID
			return false, nil
		}
		stored.ConsumerDigest = consumerDigest
		stored.ConsumedAt = now.UnixMilli()
		document.Records[parts.JobID] = record
		resolvedAction = action
		resolvedFieldID = fieldID
		return true, nil
	})
	return resolvedAction, resolvedFieldID, err
}

func (store *FormJobStore) discardFormProtectedNavigationReceipt(
	ctx context.Context,
	reference string,
	payload formProtectedAnswerBindingPayload,
	owner FormJobOwner,
	interactionID string,
) error {
	parts, err := ParseFormProtectedNavigationReference(reference)
	interactionID = strings.TrimSpace(interactionID)
	if err != nil || interactionID == "" || parts.JobID != payload.JobID {
		return ErrFormJobAnswerConflict
	}
	return store.update(ctx, func(document *formJobStoreDocument, _ time.Time) (bool, error) {
		record, err := store.authorizedPublicRecord(document, payload.JobID, owner)
		if err != nil {
			return false, err
		}
		index := storedFormNavigationReceiptIndex(record.NavigationReceipts, reference)
		if index < 0 {
			return false, nil
		}
		interactionDigest := keyedDigest(
			store.profileKey,
			"protected_navigation_interaction",
			payload.JobID,
			interactionID,
		)
		if record.NavigationReceipts[index].InteractionDigest != interactionDigest ||
			record.NavigationReceipts[index].ConsumerDigest != "" {
			return false, ErrFormJobAnswerConflict
		}
		record.NavigationReceipts = slices.Delete(record.NavigationReceipts, index, index+1)
		document.Records[payload.JobID] = record
		return true, nil
	})
}

func storedFormNavigationReceiptIndex(receipts []formJobNavigationReceipt, reference string) int {
	referenceDigest := digestBytes([]byte(strings.TrimSpace(reference)))
	return slices.IndexFunc(receipts, func(receipt formJobNavigationReceipt) bool {
		return receipt.ReferenceDigest == referenceDigest
	})
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
	if strings.HasPrefix(request.Receipt.Reference, formProtectedNavigationReceiptPrefix) {
		return sink.store.commitFormProtectedNavigationReceipt(
			ctx,
			request.Receipt.Reference,
			payload,
			owner,
			request.InteractionID,
		)
	}
	jobID, eventID, err := ParseFormProtectedAnswerReference(request.Receipt.Reference)
	if err != nil || jobID != payload.JobID {
		return ErrFormJobAnswerConflict
	}
	return sink.store.commitProtectedValue(ctx, payload, owner, eventID)
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
		if strings.HasPrefix(request.Receipt.Reference, formProtectedNavigationReceiptPrefix) {
			return sink.store.discardFormProtectedNavigationReceipt(
				ctx,
				request.Receipt.Reference,
				payload,
				owner,
				request.InteractionID,
			)
		}
		jobID, parsedEventID, parseErr := ParseFormProtectedAnswerReference(request.Receipt.Reference)
		if parseErr != nil || jobID != payload.JobID {
			return ErrFormJobAnswerConflict
		}
		eventID = parsedEventID
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
			Confidence        FormValueConfidence `json:"Confidence,omitempty"`
			Validation        FormValueValidation `json:"Validation,omitempty"`
			BlankReason       FormBlankReason
			ValidationCode    string
			SupersedesEventID string
		}{
			FieldID: request.FieldID, Value: request.Value, State: request.State, Source: request.Source,
			Confidence: request.Confidence, Validation: request.Validation,
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
			Confidence: request.Confidence, Validation: request.Validation,
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
		clearFormJobReviewProjection(&record.Public)
		record.Public.Revision = payload.Revision
		record.Public.LedgerRevision++
		record.Public.LedgerDigest, err = formJobJSONDigest(envelope)
		if err != nil {
			return false, err
		}
		record.Public.UpdatedAt = now.UnixMilli()
		fieldState := FormJobFieldState{
			FieldID: payload.FieldID, EventID: payload.EventID, Revision: payload.Revision,
			ValueKind: payload.Value.Kind,
			State:     payload.State, Source: payload.Source, Confidence: payload.Confidence,
			Validation: payload.Validation, BlankReason: payload.BlankReason,
			ValidationCode: payload.ValidationCode, SupersedesEventID: payload.SupersedesEventID,
			UpdatedAt: now.UnixMilli(),
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
