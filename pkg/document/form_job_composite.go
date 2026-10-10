package document

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	FormCompositeVersion        = "document.form_composite.v1"
	MaxFormCompositeAssignments = 32
	MaxFormCompositeValueBytes  = 16 * 1024
	formCompositeReceiptPrefix  = "form_composite."
)

// FormCompositeAssignment is trusted protected-call input, never a public tool
// argument. Consumers must establish supplied-fact provenance before calling.
type FormCompositeAssignment struct {
	FieldID           string             `json:"field_id"`
	Value             FormProtectedValue `json:"value"`
	SupersedesEventID string             `json:"supersedes_event_id,omitempty"`
	ReplaceExisting   bool               `json:"replace_existing,omitempty"`
}

type FormCompositeRequest struct {
	JobID            string
	ExpectedRevision int64
	Owner            FormJobOwner
	Schema           FormFieldsFacts
	Assignments      []FormCompositeAssignment
	IdempotencyKey   string
}

// FormCompositeResult is value-free; the receipt refers to the whole accepted
// set, not a first field that could be confused with partial success.
type FormCompositeResult struct {
	Job      FormJobRecord `json:"job"`
	Receipt  string        `json:"receipt"`
	FieldIDs []string      `json:"field_ids"`
	Reused   bool          `json:"reused"`
}

type FormCompositeValidationError struct {
	Findings []FormFieldMappingBlocker
}

func (*FormCompositeValidationError) Error() string {
	return "document form composite validation failed"
}

type formCompositeValueBinding struct {
	Version      string `json:"version"`
	ID           string `json:"id"`
	Digest       string `json:"digest"`
	BaseRevision int64  `json:"base_revision"`
	Index        int    `json:"index"`
	Count        int    `json:"count"`
}

// AppendComposite validates every selected assignment and publishes all events
// in one existing atomic snapshot write. Failure never publishes a prefix.
func (store *FormJobStore) AppendComposite(
	ctx context.Context,
	request FormCompositeRequest,
) (FormCompositeResult, error) {
	assignments, err := validateFormCompositeRequest(request)
	if err != nil {
		return FormCompositeResult{}, err
	}
	var result FormCompositeResult
	err = store.update(ctx, func(snapshot *formJobStoreDocument, now time.Time) (bool, error) {
		record, jobKey, authErr := store.authorizedRecord(snapshot, request.JobID, request.Owner)
		if authErr != nil {
			return false, authErr
		}
		defer clear(jobKey)
		if matchErr := formJobMatchesSchema(record.Public, request.Schema); matchErr != nil {
			return false, matchErr
		}
		id := "form_batch_" + keyedDigest(store.profileKey, "composite", record.Public.JobID, request.IdempotencyKey)[:32]
		digest, digestErr := formJobJSONKeyedDigest(store.profileKey, struct {
			Version      string
			JobID        string
			Revision     int64
			SchemaDigest string
			Assignments  []FormCompositeAssignment
		}{FormCompositeVersion, record.Public.JobID, request.ExpectedRevision, record.Public.FieldSchemaDigest, assignments})
		if digestErr != nil {
			return false, digestErr
		}
		prior, replayErr := formCompositeEvents(record, jobKey, id)
		if replayErr != nil {
			return false, replayErr
		}
		if len(prior) != 0 {
			if prior[0].Composite.Digest != digest {
				return false, ErrFormJobAnswerConflict
			}
			result = formCompositeResult(record, id, prior, true)
			return false, nil
		}
		if record.Public.Revision != request.ExpectedRevision || len(record.PendingEvents) != 0 ||
			(record.Public.State != FormJobPrepared && record.Public.State != FormJobCollecting &&
				record.Public.State != FormJobReviewReady && record.Public.State != FormJobAwaitingApproval) {
			return false, ErrFormJobConflict
		}
		if intakeErr := requireNoStagedFormIntake(record, jobKey); intakeErr != nil {
			return false, intakeErr
		}
		if len(record.Events)+len(assignments) > store.maxEventsPerJob {
			return false, ErrFormJobCapacityExceeded
		}
		if record.Public.Revision > math.MaxInt64-int64(len(assignments)) {
			return false, ErrFormJobConflict
		}
		payloads, findings := prepareFormCompositeValues(record, request.Schema, assignments)
		if len(findings) != 0 {
			return false, &FormCompositeValidationError{Findings: findings}
		}
		// Detach slices before construction: updateAt may still save expiry/prune
		// changes on error, so even a corrected field must not alias the snapshot.
		record.Public = cloneFormJobRecord(record.Public)
		record.Events = slices.Clone(record.Events)
		for index := range payloads {
			payload := &payloads[index]
			payload.Revision = record.Public.Revision + 1
			payload.PreviousDigest = record.Public.LedgerDigest
			payload.CreatedAt = now.UnixMilli()
			payload.IdempotencyDigest = keyedDigest(store.profileKey, "composite_value", id, payload.FieldID)
			payload.EventID = "form_value_" + payload.IdempotencyDigest[:32]
			payload.Composite = &formCompositeValueBinding{
				Version: FormCompositeVersion, ID: id, Digest: digest, BaseRevision: request.ExpectedRevision,
				Index: index, Count: len(payloads),
			}
			if appendErr := store.appendValueToRecord(&record, jobKey, *payload, digest); appendErr != nil {
				return false, appendErr
			}
		}
		snapshot.Records[record.Public.JobID] = record
		result = formCompositeResult(record, id, payloads, false)
		return true, nil
	})
	if err != nil {
		return FormCompositeResult{}, err
	}
	return result, nil
}

func validateFormCompositeRequest(request FormCompositeRequest) ([]FormCompositeAssignment, error) {
	if _, err := request.Owner.canonical(); err != nil {
		return nil, err
	}
	if !safeFormJobCodePattern.MatchString(request.JobID) || len(request.JobID) > maxFormJobIDLength ||
		request.ExpectedRevision <= 0 ||
		!validFormFieldsFacts(request.Schema) || strings.TrimSpace(request.IdempotencyKey) == "" ||
		len(request.IdempotencyKey) > maxFormJobIdempotencyLength || !utf8.ValidString(request.IdempotencyKey) ||
		len(request.Assignments) == 0 || len(request.Assignments) > MaxFormCompositeAssignments {
		return nil, errors.New("document form composite request is invalid")
	}
	assignments := slices.Clone(request.Assignments)
	slices.SortFunc(
		assignments,
		func(a, b FormCompositeAssignment) int { return strings.Compare(a.FieldID, b.FieldID) },
	)
	valueBytes := 0
	for index, assignment := range assignments {
		if !opaqueFieldID.MatchString(assignment.FieldID) || assignment.Value.validate() != nil ||
			assignment.Value.Kind == ProtectedValueBlank || len(assignment.SupersedesEventID) > maxFormJobEventIDLength ||
			(assignment.SupersedesEventID != "" && !safeFormJobCodePattern.MatchString(assignment.SupersedesEventID)) ||
			(index > 0 && assignments[index-1].FieldID == assignment.FieldID) {
			return nil, errors.New("document form composite assignment is invalid")
		}
		valueBytes += len(assignment.Value.Text)
		for _, choice := range assignment.Value.Choices {
			valueBytes += len(choice)
		}
	}
	if valueBytes > MaxFormCompositeValueBytes {
		return nil, ErrFormJobCapacityExceeded
	}
	return assignments, nil
}

func prepareFormCompositeValues(
	record formJobStoredRecord,
	schema FormFieldsFacts,
	assignments []FormCompositeAssignment,
) ([]formJobValuePayload, []FormFieldMappingBlocker) {
	var findings []FormFieldMappingBlocker
	payloads := make([]formJobValuePayload, 0, len(assignments))
	for _, assignment := range assignments {
		fieldIndex := slices.IndexFunc(
			schema.Fields,
			func(field FormField) bool { return field.ID == assignment.FieldID },
		)
		if fieldIndex < 0 {
			findings = append(findings, FormFieldMappingBlocker{FieldID: assignment.FieldID, Code: "field_unresolved"})
			continue
		}
		field := schema.Fields[fieldIndex]
		currentIndex := slices.IndexFunc(record.Public.Fields, func(state FormJobFieldState) bool {
			return state.FieldID == assignment.FieldID
		})
		if (currentIndex >= 0 && record.Public.Fields[currentIndex].EventID != assignment.SupersedesEventID) ||
			(currentIndex < 0 && assignment.SupersedesEventID != "") ||
			(currentIndex < 0 && field.HasValue && !assignment.ReplaceExisting) {
			findings = append(findings, FormFieldMappingBlocker{FieldID: assignment.FieldID, Code: "field_conflicting"})
			continue
		}
		value, validation, code := deterministicFormValue(field, assignment.Value)
		if field.ReadOnly {
			validation, code = FormValueValidationUnsupported, "field_unsupported"
		}
		if validation != FormValueValidationValid {
			findings = append(findings, FormFieldMappingBlocker{FieldID: assignment.FieldID, Code: code})
			continue
		}
		payloads = append(payloads, formJobValuePayload{
			FieldID: field.ID, Value: value, State: FormValueConfirmed, Source: FormValueSourceUser,
			Confidence: FormValueConfidenceExact, Validation: FormValueValidationValid,
			SupersedesEventID: assignment.SupersedesEventID,
		})
	}
	return payloads, findings
}

// ReadCompositeReceipt resolves safe progress only. It never replays the
// assignments, returns values, or redirects a receipt into a different job.
func (store *FormJobStore) ReadCompositeReceipt(
	ctx context.Context,
	reference string,
	owner FormJobOwner,
) (FormCompositeResult, error) {
	jobID, id, err := ParseFormCompositeReceipt(reference)
	if err != nil {
		return FormCompositeResult{}, err
	}
	var result FormCompositeResult
	err = store.update(ctx, func(snapshot *formJobStoreDocument, _ time.Time) (bool, error) {
		record, jobKey, authErr := store.authorizedRecord(snapshot, jobID, owner)
		if authErr != nil {
			return false, authErr
		}
		defer clear(jobKey)
		events, readErr := formCompositeEvents(record, jobKey, id)
		if readErr != nil {
			return false, readErr
		}
		if len(events) == 0 {
			return false, ErrFormJobNotFound
		}
		result = formCompositeResult(record, id, events, true)
		return false, nil
	})
	if err != nil {
		return FormCompositeResult{}, err
	}
	return result, err
}

func ParseFormCompositeReceipt(reference string) (string, string, error) {
	parts := strings.Split(strings.TrimPrefix(reference, formCompositeReceiptPrefix), ".")
	if !strings.HasPrefix(reference, formCompositeReceiptPrefix) || len(parts) != 2 ||
		!safeFormJobCodePattern.MatchString(parts[0]) || !strings.HasPrefix(parts[1], "form_batch_") ||
		!validLowerHex(strings.TrimPrefix(parts[1], "form_batch_"), 32) {
		return "", "", ErrFormJobAnswerConflict
	}
	return parts[0], parts[1], nil
}

func formCompositeEvents(record formJobStoredRecord, jobKey []byte, id string) ([]formJobValuePayload, error) {
	var events []formJobValuePayload
	for _, envelope := range record.Events {
		payload, err := openFormJobValuePayload(jobKey, envelope)
		if err != nil {
			return nil, err
		}
		if payload.Composite != nil && payload.Composite.ID == id {
			events = append(events, payload)
		}
	}
	return events, nil
}

func formCompositeResult(
	record formJobStoredRecord,
	id string,
	events []formJobValuePayload,
	reused bool,
) FormCompositeResult {
	result := FormCompositeResult{
		Job: cloneFormJobRecord(record.Public), Receipt: formCompositeReceiptPrefix + record.Public.JobID + "." + id,
		FieldIDs: make([]string, 0, len(events)), Reused: reused,
	}
	for _, event := range events {
		result.FieldIDs = append(result.FieldIDs, event.FieldID)
	}
	return result
}

func validFormCompositeValueBinding(binding formCompositeValueBinding, revision int64) bool {
	return binding.Version == FormCompositeVersion && strings.HasPrefix(binding.ID, "form_batch_") &&
		validLowerHex(strings.TrimPrefix(binding.ID, "form_batch_"), 32) && validLowerHex(binding.Digest, 64) &&
		binding.BaseRevision > 0 && binding.Count > 0 && binding.Count <= MaxFormCompositeAssignments &&
		binding.Index >= 0 && binding.Index < binding.Count && revision == binding.BaseRevision+int64(binding.Index)+1
}

func validateFormCompositeEvents(events map[string]formJobValuePayload) error {
	groups := make(map[string][]formJobValuePayload)
	for _, event := range events {
		if event.Composite != nil {
			groups[event.Composite.ID] = append(groups[event.Composite.ID], event)
		}
	}
	for _, group := range groups {
		binding := *group[0].Composite
		if len(group) != binding.Count {
			return ErrFormJobRecordCorrupt
		}
		indexes := make(map[int]bool, len(group))
		fields := make(map[string]bool, len(group))
		for _, event := range group {
			member := *event.Composite
			if !validFormCompositeValueBinding(member, event.Revision) || member.Digest != binding.Digest ||
				member.Count != binding.Count || member.BaseRevision != binding.BaseRevision ||
				indexes[member.Index] || fields[event.FieldID] {
				return fmt.Errorf("%w: invalid composite membership", ErrFormJobRecordCorrupt)
			}
			indexes[member.Index], fields[event.FieldID] = true, true
		}
	}
	return nil
}
