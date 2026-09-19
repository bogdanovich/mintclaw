package document

import (
	"context"
	"crypto/sha256"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const FormCommitOutputPolicyRevision = "mintclaw.document_form_output.verified_pdf.v1"

// FormCommitBinding is the value-free authority hashed into one durable
// approval. It deliberately contains no source reference, field value, path,
// or filename.
type FormCommitBinding struct {
	JobID                string `json:"job_id"`
	OwnerDigest          string `json:"owner_digest"`
	ApprovalRevision     int64  `json:"approval_revision"`
	ReviewRevision       int64  `json:"review_revision"`
	SourceDigest         string `json:"source_digest"`
	FieldSchemaDigest    string `json:"field_schema_digest"`
	BackendRevision      string `json:"backend_revision"`
	AuditPolicyRevision  string `json:"audit_policy_revision"`
	AssignmentDigest     string `json:"assignment_digest"`
	ReviewDigest         string `json:"review_digest"`
	OutputPolicyRevision string `json:"output_policy_revision"`
	OperationID          string `json:"operation_id"`
	ExpiresAt            int64  `json:"expires_at"`
}

type FormCommitRequest struct {
	JobID                string
	ExpectedRevision     int64
	Owner                FormJobOwner
	Schema               FormFieldsFacts
	AuditPolicyRevision  string
	OutputPolicyRevision string
}

type FormCommitArtifactRequest struct {
	JobID            string
	ExpectedRevision int64
	Owner            FormJobOwner
	OperationID      string
	ArtifactRef      string
	ArtifactDigest   string
}

// PrepareFormCommitApproval freezes one exact reviewed assignment behind a
// deterministic PDF2 operation identity. Repeated preparation is idempotent.
func (store *FormJobStore) PrepareFormCommitApproval(
	ctx context.Context,
	request FormCommitRequest,
) (FormJobRecord, FormCommitBinding, error) {
	if err := validateFormCommitRequest(request); err != nil {
		return FormJobRecord{}, FormCommitBinding{}, err
	}
	var result FormJobRecord
	var binding FormCommitBinding
	err := store.update(ctx, func(document *formJobStoreDocument, now time.Time) (bool, error) {
		record, err := store.authorizedPublicRecord(document, request.JobID, request.Owner)
		if err != nil {
			return false, err
		}
		if matchErr := formJobMatchesSchema(record.Public, request.Schema); matchErr != nil {
			return false, matchErr
		}
		if record.Public.AuditPolicyRevision != request.AuditPolicyRevision {
			return false, ErrFormAuditPolicyChanged
		}
		if record.Public.State == FormJobAwaitingApproval {
			binding, err = formCommitBindingFromRecord(record.Public)
			if err != nil || binding.OutputPolicyRevision != request.OutputPolicyRevision {
				return false, ErrFormApprovalStale
			}
			result = cloneFormJobRecord(record.Public)
			return false, nil
		}
		if record.Public.State != FormJobReviewReady || record.Public.Revision != request.ExpectedRevision ||
			record.Public.ReviewRevision != record.Public.Revision || record.Public.ReviewDigest == "" ||
			record.Public.AssignmentDigest == "" || len(record.Public.AuditBlockers) != 0 {
			return false, ErrFormReviewStale
		}
		if !formCommitHasMaterializableAssignment(record.Public, request.Schema) {
			return false, ErrFormReviewStale
		}
		nextRevision := record.Public.Revision + 1
		operationID := deterministicFormCommitOperationID(record.Public, request.OutputPolicyRevision)
		binding = FormCommitBinding{
			JobID: record.Public.JobID, OwnerDigest: record.Public.OwnerDigest,
			ApprovalRevision: nextRevision, ReviewRevision: record.Public.ReviewRevision,
			SourceDigest: record.Public.SourceDigest, FieldSchemaDigest: record.Public.FieldSchemaDigest,
			BackendRevision:     record.Public.BackendRevision,
			AuditPolicyRevision: record.Public.AuditPolicyRevision,
			AssignmentDigest:    record.Public.AssignmentDigest, ReviewDigest: record.Public.ReviewDigest,
			OutputPolicyRevision: request.OutputPolicyRevision, OperationID: operationID,
			ExpiresAt: record.Public.ExpiresAt,
		}
		approvalDigest, err := formJobJSONDigest(binding)
		if err != nil {
			return false, err
		}
		record.Public.State = FormJobAwaitingApproval
		record.Public.Revision = nextRevision
		record.Public.ApprovalRevision = nextRevision
		record.Public.ApprovalDigest = approvalDigest
		record.Public.OutputPolicyRevision = request.OutputPolicyRevision
		record.Public.OperationID = operationID
		record.Public.UpdatedAt = now.UnixMilli()
		document.Records[record.Public.JobID] = record
		result = cloneFormJobRecord(record.Public)
		return true, nil
	})
	return result, binding, err
}

// CurrentFormCommitBinding revalidates source/schema/policy authority before
// an approval is created or consumed.
func (store *FormJobStore) CurrentFormCommitBinding(
	ctx context.Context,
	request FormCommitRequest,
) (FormJobRecord, FormCommitBinding, error) {
	if err := validateFormCommitRequest(request); err != nil {
		return FormJobRecord{}, FormCommitBinding{}, err
	}
	record, err := store.Get(ctx, request.JobID, request.Owner)
	if err != nil {
		return FormJobRecord{}, FormCommitBinding{}, err
	}
	if err = formJobMatchesSchema(record, request.Schema); err != nil {
		return FormJobRecord{}, FormCommitBinding{}, err
	}
	if record.AuditPolicyRevision != request.AuditPolicyRevision ||
		record.OutputPolicyRevision != request.OutputPolicyRevision {
		return FormJobRecord{}, FormCommitBinding{}, ErrFormApprovalStale
	}
	switch record.State {
	case FormJobAwaitingApproval, FormJobCommitting, FormJobDelivering:
	default:
		return FormJobRecord{}, FormCommitBinding{}, ErrFormApprovalStale
	}
	binding, err := formCommitBindingFromRecord(record)
	return record, binding, err
}

// BeginFormCommit consumes the job-side approval boundary once. The human
// interaction registry independently consumes the allow-once grant first.
func (store *FormJobStore) BeginFormCommit(
	ctx context.Context,
	request FormCommitRequest,
	approved FormCommitBinding,
) (FormJobRecord, error) {
	if err := validateFormCommitRequest(request); err != nil {
		return FormJobRecord{}, err
	}
	var result FormJobRecord
	err := store.update(ctx, func(document *formJobStoreDocument, now time.Time) (bool, error) {
		record, err := store.authorizedPublicRecord(document, request.JobID, request.Owner)
		if err != nil {
			return false, err
		}
		if record.Public.State == FormJobCommitting {
			binding, bindingErr := formCommitBindingFromRecord(record.Public)
			if bindingErr != nil || binding != approved {
				return false, ErrFormApprovalStale
			}
			result = cloneFormJobRecord(record.Public)
			return false, nil
		}
		if record.Public.State != FormJobAwaitingApproval ||
			record.Public.Revision != request.ExpectedRevision {
			return false, ErrFormApprovalStale
		}
		if matchErr := formJobMatchesSchema(record.Public, request.Schema); matchErr != nil {
			return false, matchErr
		}
		binding, bindingErr := formCommitBindingFromRecord(record.Public)
		if bindingErr != nil || binding != approved || !now.Before(time.UnixMilli(binding.ExpiresAt)) {
			return false, ErrFormApprovalStale
		}
		record.Public.State = FormJobCommitting
		record.Public.Revision++
		record.Public.UpdatedAt = now.UnixMilli()
		document.Records[record.Public.JobID] = record
		result = cloneFormJobRecord(record.Public)
		return true, nil
	})
	return result, err
}

// MaterializeFormCommit decrypts only the reviewed current events and builds
// the typed PDF2 map in memory. Callers must clear the returned map promptly.
func (store *FormJobStore) MaterializeFormCommit(
	ctx context.Context,
	request FormCommitRequest,
) (FormJobRecord, FillMap, error) {
	if err := validateFormCommitRequest(request); err != nil {
		return FormJobRecord{}, FillMap{}, err
	}
	var result FormJobRecord
	var fill FillMap
	err := store.update(ctx, func(document *formJobStoreDocument, _ time.Time) (bool, error) {
		record, jobKey, err := store.authorizedRecord(document, request.JobID, request.Owner)
		if err != nil {
			return false, err
		}
		defer clear(jobKey)
		if record.Public.State != FormJobCommitting || record.Public.Revision != request.ExpectedRevision ||
			record.Public.AuditPolicyRevision != request.AuditPolicyRevision ||
			record.Public.OutputPolicyRevision != request.OutputPolicyRevision {
			return false, ErrFormApprovalStale
		}
		if matchErr := formJobMatchesSchema(record.Public, request.Schema); matchErr != nil {
			return false, matchErr
		}
		fields, assignments, err := protectedFormAuditFields(record, jobKey, request.Schema)
		if err != nil {
			return false, err
		}
		defer clearFormAuditFields(fields)
		defer clearFormAuditAssignments(assignments)
		assignmentDigest, err := formJobJSONKeyedDigest(store.profileKey, assignments)
		if err != nil || !constantTimeStringEqual(assignmentDigest, record.Public.AssignmentDigest) {
			return false, ErrFormReviewStale
		}
		fill, err = materializeProtectedFormFill(fields)
		if err != nil {
			return false, err
		}
		result = cloneFormJobRecord(record.Public)
		return false, nil
	})
	return result, fill, err
}

// RecordFormCommitArtifact advances a verified and registered PDF2 result to
// the delivery stop gate without creating an outbox intent.
func (store *FormJobStore) RecordFormCommitArtifact(
	ctx context.Context,
	request FormCommitArtifactRequest,
) (FormJobRecord, error) {
	if strings.TrimSpace(request.JobID) == "" || request.ExpectedRevision <= 0 ||
		!validWriteOperationID(request.OperationID) || !validDurableArtifactRef(request.ArtifactRef) ||
		!validDocumentDigest(request.ArtifactDigest) {
		return FormJobRecord{}, errors.New("document form commit artifact is invalid")
	}
	var result FormJobRecord
	err := store.update(ctx, func(document *formJobStoreDocument, now time.Time) (bool, error) {
		record, err := store.authorizedPublicRecord(document, request.JobID, request.Owner)
		if err != nil {
			return false, err
		}
		if record.Public.State == FormJobDelivering {
			if record.Public.OperationID != request.OperationID || record.Public.ArtifactRef != request.ArtifactRef ||
				record.Public.ArtifactDigest != request.ArtifactDigest {
				return false, ErrFormJobConflict
			}
			result = cloneFormJobRecord(record.Public)
			return false, nil
		}
		if record.Public.State != FormJobCommitting || record.Public.Revision != request.ExpectedRevision ||
			record.Public.OperationID != request.OperationID {
			return false, ErrFormJobConflict
		}
		record.Public.State = FormJobDelivering
		record.Public.Revision++
		record.Public.ArtifactRef = request.ArtifactRef
		record.Public.ArtifactDigest = request.ArtifactDigest
		record.Public.UpdatedAt = now.UnixMilli()
		document.Records[record.Public.JobID] = record
		result = cloneFormJobRecord(record.Public)
		return true, nil
	})
	return result, err
}

// FailFormCommit terminalizes a definitely failed or uncertain accepted
// operation while retaining only value-free reconciliation identity.
func (store *FormJobStore) FailFormCommit(
	ctx context.Context,
	jobID string,
	expectedRevision int64,
	owner FormJobOwner,
	uncertain bool,
	failureCode string,
) (FormJobRecord, error) {
	var result FormJobRecord
	err := store.update(ctx, func(document *formJobStoreDocument, now time.Time) (bool, error) {
		record, err := store.authorizedPublicRecord(document, jobID, owner)
		if err != nil {
			return false, err
		}
		if record.Public.State.terminal() {
			result = cloneFormJobRecord(record.Public)
			return false, nil
		}
		if record.Public.State != FormJobCommitting || record.Public.Revision != expectedRevision {
			return false, ErrFormJobConflict
		}
		state := FormJobFailed
		if uncertain {
			state = FormJobUncertain
		}
		store.terminalizeStoredFormCommit(&record, state, failureCode, now)
		document.Records[jobID] = record
		result = cloneFormJobRecord(record.Public)
		return true, nil
	})
	return result, err
}

func validateFormCommitRequest(request FormCommitRequest) error {
	if _, err := request.Owner.canonical(); err != nil {
		return err
	}
	if strings.TrimSpace(request.JobID) == "" || request.ExpectedRevision <= 0 ||
		!validFormFieldsFacts(request.Schema) || strings.TrimSpace(request.AuditPolicyRevision) == "" ||
		request.OutputPolicyRevision != FormCommitOutputPolicyRevision {
		return errors.New("document form commit request is invalid")
	}
	return nil
}

func formCommitBindingFromRecord(record FormJobRecord) (FormCommitBinding, error) {
	binding := FormCommitBinding{
		JobID: record.JobID, OwnerDigest: record.OwnerDigest, ApprovalRevision: record.ApprovalRevision,
		ReviewRevision: record.ReviewRevision, SourceDigest: record.SourceDigest,
		FieldSchemaDigest: record.FieldSchemaDigest, BackendRevision: record.BackendRevision,
		AuditPolicyRevision: record.AuditPolicyRevision, AssignmentDigest: record.AssignmentDigest,
		ReviewDigest: record.ReviewDigest, OutputPolicyRevision: record.OutputPolicyRevision,
		OperationID: record.OperationID, ExpiresAt: record.ExpiresAt,
	}
	digest, err := formJobJSONDigest(binding)
	if err != nil || !constantTimeStringEqual(digest, record.ApprovalDigest) {
		return FormCommitBinding{}, ErrFormApprovalStale
	}
	return binding, nil
}

func deterministicFormCommitOperationID(record FormJobRecord, outputPolicy string) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		"mintclaw.document-form-commit.v1", record.JobID,
		strconv.FormatInt(record.ReviewRevision, 10), record.AssignmentDigest, outputPolicy,
	}, "\x00")))
	bytes := digest[:16]
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return "document_write_" + strings.ReplaceAll(uuid.Must(uuid.FromBytes(bytes)).String(), "-", "")
}

func formCommitHasMaterializableAssignment(record FormJobRecord, schema FormFieldsFacts) bool {
	current := make(map[string]FormJobFieldState, len(record.Fields))
	for _, field := range record.Fields {
		current[field.FieldID] = field
	}
	for _, field := range schema.Fields {
		if field.ReadOnly {
			continue
		}
		state, found := current[field.ID]
		if !found {
			continue
		}
		if state.ValueKind != ProtectedValueBlank || field.HasValue {
			return true
		}
	}
	return false
}

func materializeProtectedFormFill(fields []FormAuditField) (FillMap, error) {
	fill := FillMap{SchemaVersion: FillMapSchemaVersion}
	for _, field := range fields {
		if field.Existing {
			continue
		}
		value, selected, err := materializeProtectedFormValue(field.Schema, field.State, field.Value)
		if err != nil {
			clearFillMap(&fill)
			return FillMap{}, err
		}
		if selected {
			fill.Assignments = append(fill.Assignments, FormFillAssignment{FieldID: field.Schema.ID, Value: value})
		}
	}
	if len(fill.Assignments) == 0 {
		return FillMap{}, ErrFormReviewStale
	}
	return fill, nil
}

func materializeProtectedFormValue(
	field FormField,
	state FormJobFieldState,
	protected FormProtectedValue,
) (FormValue, bool, error) {
	if !formFieldStateResolved(state, field) {
		return FormValue{}, false, ErrFormReviewStale
	}
	if protected.Kind == ProtectedValueBlank {
		if !field.HasValue {
			return FormValue{}, false, nil
		}
		switch field.Kind {
		case FormFieldText, FormFieldDate:
			empty := ""
			return FormValue{Type: FormValueText, Text: &empty}, true, nil
		case FormFieldCheckbox:
			unchecked := false
			return FormValue{Type: FormValueBoolean, Checked: &unchecked}, true, nil
		case FormFieldRadio, FormFieldCombo:
			return FormValue{Type: FormValueChoice}, true, nil
		case FormFieldList:
			return FormValue{Type: FormValueChoices}, true, nil
		default:
			return FormValue{}, false, ErrFormReviewStale
		}
	}
	switch protected.Kind {
	case ProtectedValueText:
		value := protected.Text
		return FormValue{Type: FormValueText, Text: &value}, true, nil
	case ProtectedValueBoolean:
		if protected.Boolean == nil {
			return FormValue{}, false, ErrFormJobRecordCorrupt
		}
		value := *protected.Boolean
		return FormValue{Type: FormValueBoolean, Checked: &value}, true, nil
	case ProtectedValueChoice:
		return FormValue{Type: FormValueChoice, Choices: []string{protected.Text}}, true, nil
	case ProtectedValueChoices:
		return FormValue{Type: FormValueChoices, Choices: append([]string(nil), protected.Choices...)}, true, nil
	default:
		return FormValue{}, false, ErrFormJobRecordCorrupt
	}
}

func clearFillMap(fill *FillMap) {
	if fill == nil {
		return
	}
	for index := range fill.Assignments {
		fill.Assignments[index].FieldID = ""
		if fill.Assignments[index].Value.Text != nil {
			*fill.Assignments[index].Value.Text = ""
		}
		if fill.Assignments[index].Value.Checked != nil {
			*fill.Assignments[index].Value.Checked = false
		}
		clear(fill.Assignments[index].Value.Choices)
		fill.Assignments[index].Value.Choices = nil
	}
	clear(fill.Assignments)
	fill.Assignments = nil
	fill.SchemaVersion = ""
}

func (store *FormJobStore) terminalizeStoredFormCommit(
	record *formJobStoredRecord,
	state FormJobState,
	failureCode string,
	now time.Time,
) {
	clearStoredFormProtectedMaterial(record)
	clearFormJobReviewProjection(&record.Public)
	record.Public.State = state
	record.Public.Revision++
	record.Public.UpdatedAt = now.UnixMilli()
	record.Public.TerminalAt = now.UnixMilli()
	record.Public.CleanupAfter = now.Add(store.terminalRetention).UnixMilli()
	record.Public.FailureCode = strings.TrimSpace(failureCode)
}

func clearStoredFormProtectedMaterial(record *formJobStoredRecord) {
	if record == nil {
		return
	}
	record.WrappedKey = nil
	record.Source = nil
	clear(record.Events)
	record.Events = nil
	clear(record.PendingEvents)
	record.PendingEvents = nil
	record.Public.Fields = nil
	record.Public.LedgerDigest = ""
}
