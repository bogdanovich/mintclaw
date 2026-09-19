package document

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	FormJobSnapshotVersion = "document_form_job.v1"
	FormJobEnvelopeVersion = "document_form_envelope.v1"

	DefaultFormJobRetention         = 7 * 24 * time.Hour
	DefaultFormJobTerminalRetention = 24 * time.Hour
	DefaultFormJobMaxJobs           = 128
	DefaultFormJobMaxEvents         = 512
	DefaultFormJobMaxBytes          = 4 * 1024 * 1024

	maxFormJobIDLength          = 96
	maxFormJobIdentityLength    = 1024
	maxFormJobDigestLength      = 128
	maxFormJobRevisionLength    = 256
	maxFormJobFieldIDLength     = 512
	maxFormJobEventIDLength     = 96
	maxFormJobIdempotencyLength = 512
	maxFormJobTextValueLength   = DefaultMaxFormValueBytes
	maxFormJobChoices           = 128
	maxFormJobChoiceLength      = 4096
)

var safeFormJobCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

var (
	ErrFormJobNotFound           = errors.New("document form job not found")
	ErrFormJobUnauthorized       = errors.New("document form job unauthorized")
	ErrFormJobConflict           = errors.New("document form job revision conflict")
	ErrFormJobStale              = errors.New("document form job is stale")
	ErrFormJobExpired            = errors.New("document form job expired")
	ErrFormJobTerminal           = errors.New("document form job is terminal")
	ErrFormJobStoreUnavailable   = errors.New("document protected form store unavailable")
	ErrFormJobKeyUnavailable     = errors.New("document protected form key unavailable")
	ErrFormJobRecordCorrupt      = errors.New("document protected form record corrupt")
	ErrFormJobAnswerConflict     = errors.New("document protected form answer conflict")
	ErrFormJobCapacityExceeded   = errors.New("document form job store capacity exceeded")
	ErrFormJobUnsupportedVersion = errors.New("document form job version unsupported")
	ErrFormAuditUnavailable      = errors.New("document form audit unavailable")
	ErrFormAuditPolicyChanged    = errors.New("document form audit policy changed")
	ErrFormReviewStale           = errors.New("document form review is stale")
	ErrFormApprovalStale         = errors.New("document form approval is stale")
)

type FormJobState string

const (
	FormJobPrepared         FormJobState = "prepared"
	FormJobCollecting       FormJobState = "collecting"
	FormJobReviewReady      FormJobState = "review_ready"
	FormJobAwaitingApproval FormJobState = "awaiting_approval"
	FormJobCommitting       FormJobState = "committing"
	FormJobDelivering       FormJobState = "delivering"
	FormJobCompleted        FormJobState = "completed"
	FormJobCanceled         FormJobState = "canceled"
	FormJobExpired          FormJobState = "expired"
	FormJobDeleted          FormJobState = "deleted"
	FormJobFailed           FormJobState = "failed"
	FormJobUncertain        FormJobState = "uncertain"
)

func (state FormJobState) terminal() bool {
	switch state {
	case FormJobCompleted, FormJobCanceled, FormJobExpired, FormJobDeleted, FormJobFailed, FormJobUncertain:
		return true
	default:
		return false
	}
}

type ProtectedValueKind string

const (
	ProtectedValueText    ProtectedValueKind = "text"
	ProtectedValueBoolean ProtectedValueKind = "boolean"
	ProtectedValueChoice  ProtectedValueKind = "choice"
	ProtectedValueChoices ProtectedValueKind = "choices"
	ProtectedValueBlank   ProtectedValueKind = "blank"
)

type FormValueState string

const (
	FormValueSupplied       FormValueState = "supplied"
	FormValueConfirmed      FormValueState = "confirmed"
	FormValueBlanked        FormValueState = "intentionally_blank"
	FormValueNotApplicable  FormValueState = "not_applicable"
	FormValueAmbiguous      FormValueState = "ambiguous"
	FormValueConflicting    FormValueState = "conflicting"
	FormValueInvalid        FormValueState = "invalid"
	FormValueModelSuggested FormValueState = "model_suggested"
)

type FormValueConfidence string

const (
	FormValueConfidenceExact     FormValueConfidence = "exact"
	FormValueConfidenceHigh      FormValueConfidence = "high"
	FormValueConfidenceLow       FormValueConfidence = "low"
	FormValueConfidenceConfirmed FormValueConfidence = "confirmed"
)

type FormValueValidation string

const (
	FormValueValidationPending              FormValueValidation = "pending"
	FormValueValidationValid                FormValueValidation = "valid"
	FormValueValidationInvalid              FormValueValidation = "invalid"
	FormValueValidationAmbiguous            FormValueValidation = "ambiguous"
	FormValueValidationConflicting          FormValueValidation = "conflicting"
	FormValueValidationRequiredBlank        FormValueValidation = "required_blank"
	FormValueValidationUnsupported          FormValueValidation = "unsupported"
	FormValueValidationConfirmationRequired FormValueValidation = "confirmation_required"
)

type FormValueSource string

const (
	FormValueSourceUser          FormValueSource = "user"
	FormValueSourceDocument      FormValueSource = "document"
	FormValueSourceDeterministic FormValueSource = "deterministic"
	FormValueSourceModel         FormValueSource = "model"
)

type FormBlankReason string

const (
	FormBlankNone          FormBlankReason = ""
	FormBlankIntentional   FormBlankReason = "intentional"
	FormBlankSkipped       FormBlankReason = "skipped"
	FormBlankNotApplicable FormBlankReason = "not_applicable"
	FormBlankSourceEmpty   FormBlankReason = "source_empty"
)

func (reason FormBlankReason) valid() bool {
	switch reason {
	case FormBlankIntentional, FormBlankSkipped, FormBlankNotApplicable, FormBlankSourceEmpty:
		return true
	default:
		return false
	}
}

// FormJobOwner is supplied at every authority-bearing store boundary. Raw
// route values are never persisted; the store records a keyed owner digest.
type FormJobOwner struct {
	AgentID         string
	WorkspaceID     string
	RouteSessionKey string
	Channel         string
	AccountID       string
	ChatID          string
	ChatType        string
	SenderID        string
	TopicID         string
	SpaceID         string
	SpaceType       string
}

func (owner FormJobOwner) canonical() (string, error) {
	values := []string{
		owner.AgentID,
		owner.WorkspaceID,
		owner.RouteSessionKey,
		owner.Channel,
		owner.AccountID,
		owner.ChatID,
		owner.ChatType,
		owner.SenderID,
		owner.TopicID,
		owner.SpaceID,
		owner.SpaceType,
	}
	for index := range values {
		values[index] = strings.TrimSpace(values[index])
		if len(values[index]) > maxFormJobIdentityLength || !utf8.ValidString(values[index]) ||
			strings.ContainsRune(values[index], '\x00') {
			return "", errors.New("document form job owner value is invalid")
		}
	}
	if values[0] == "" || values[1] == "" || values[2] == "" || values[3] == "" ||
		values[5] == "" || values[7] == "" {
		return "", errors.New("document form job owner is incomplete")
	}
	return strings.Join(values, "\x00"), nil
}

type FormJobFieldState struct {
	FieldID           string              `json:"field_id"`
	EventID           string              `json:"event_id"`
	ValueKind         ProtectedValueKind  `json:"value_kind"`
	State             FormValueState      `json:"state"`
	Source            FormValueSource     `json:"source"`
	Confidence        FormValueConfidence `json:"confidence,omitempty"`
	Validation        FormValueValidation `json:"validation,omitempty"`
	BlankReason       FormBlankReason     `json:"blank_reason,omitempty"`
	ValidationCode    string              `json:"validation_code,omitempty"`
	SupersedesEventID string              `json:"supersedes_event_id,omitempty"`
	UpdatedAt         int64               `json:"updated_at"`
}

// FormJobReviewBlocker is a value-free audit finding retained in the public
// projection. Code is drawn from the bounded PDF3 failure vocabulary.
type FormJobReviewBlocker struct {
	FieldID string `json:"field_id"`
	Code    string `json:"code"`
}

// FormJobRecord is the path-free, value-free public projection. It is safe to
// expose to ordinary history and diagnostics after an additional bounded
// presentation projection.
type FormJobRecord struct {
	SchemaVersion        string                 `json:"schema_version"`
	JobID                string                 `json:"job_id"`
	State                FormJobState           `json:"state"`
	Revision             int64                  `json:"revision"`
	OwnerDigest          string                 `json:"owner_digest"`
	StartDigest          string                 `json:"start_digest"`
	SourceDigest         string                 `json:"source_digest"`
	FieldSchemaDigest    string                 `json:"field_schema_digest"`
	BackendRevision      string                 `json:"backend_revision"`
	AuditPolicyRevision  string                 `json:"audit_policy_revision"`
	LedgerRevision       int64                  `json:"ledger_revision"`
	LedgerDigest         string                 `json:"ledger_digest,omitempty"`
	Fields               []FormJobFieldState    `json:"fields,omitempty"`
	AuditRevision        int64                  `json:"audit_revision,omitempty"`
	AuditDigest          string                 `json:"audit_digest,omitempty"`
	AuditModel           string                 `json:"audit_model,omitempty"`
	AuditBlockers        []FormJobReviewBlocker `json:"audit_blockers,omitempty"`
	ReviewRevision       int64                  `json:"review_revision,omitempty"`
	AssignmentDigest     string                 `json:"assignment_digest,omitempty"`
	ReviewDigest         string                 `json:"review_digest,omitempty"`
	ApprovalRevision     int64                  `json:"approval_revision,omitempty"`
	ApprovalDigest       string                 `json:"approval_digest,omitempty"`
	OutputPolicyRevision string                 `json:"output_policy_revision,omitempty"`
	OperationID          string                 `json:"operation_id,omitempty"`
	ArtifactRef          string                 `json:"artifact_ref,omitempty"`
	ArtifactDigest       string                 `json:"artifact_digest,omitempty"`
	CreatedAt            int64                  `json:"created_at"`
	UpdatedAt            int64                  `json:"updated_at"`
	ExpiresAt            int64                  `json:"expires_at"`
	TerminalAt           int64                  `json:"terminal_at,omitempty"`
	CleanupAfter         int64                  `json:"cleanup_after,omitempty"`
	FailureCode          string                 `json:"failure_code,omitempty"`
}

type FormJobCreateRequest struct {
	Owner               FormJobOwner
	StartIdempotencyKey string
	SourceRef           string
	SourceDigest        string
	FieldSchemaDigest   string
	BackendRevision     string
	AuditPolicyRevision string
	Retention           time.Duration
}

type FormProtectedValue struct {
	Kind    ProtectedValueKind `json:"kind"`
	Text    string             `json:"text,omitempty"`
	Boolean *bool              `json:"boolean,omitempty"`
	Choices []string           `json:"choices,omitempty"`
}

func (value FormProtectedValue) validate() error {
	if !utf8.ValidString(value.Text) || len(value.Text) > maxFormJobTextValueLength {
		return errors.New("document protected form text value is invalid")
	}
	if len(value.Choices) > maxFormJobChoices {
		return errors.New("document protected form choices exceed limit")
	}
	for _, choice := range value.Choices {
		if strings.TrimSpace(choice) == "" || !utf8.ValidString(choice) || len(choice) > maxFormJobChoiceLength {
			return errors.New("document protected form choice is invalid")
		}
	}
	switch value.Kind {
	case ProtectedValueText:
		if strings.TrimSpace(value.Text) == "" || value.Boolean != nil || len(value.Choices) != 0 {
			return errors.New("document protected text value is invalid")
		}
	case ProtectedValueBoolean:
		if value.Boolean == nil || value.Text != "" || len(value.Choices) != 0 {
			return errors.New("document protected boolean value is invalid")
		}
	case ProtectedValueChoice:
		if value.Boolean != nil || strings.TrimSpace(value.Text) == "" || len(value.Choices) != 0 {
			return errors.New("document protected choice value is invalid")
		}
	case ProtectedValueChoices:
		if value.Boolean != nil || value.Text != "" || len(value.Choices) == 0 {
			return errors.New("document protected choices value is invalid")
		}
	case ProtectedValueBlank:
		if value.Boolean != nil || value.Text != "" || len(value.Choices) != 0 {
			return errors.New("document protected blank value is invalid")
		}
	default:
		return fmt.Errorf("document protected form value kind %q is invalid", value.Kind)
	}
	return nil
}

func validateProtectedValueClassification(
	kind ProtectedValueKind,
	state FormValueState,
	source FormValueSource,
	blankReason FormBlankReason,
	validationCode string,
) error {
	switch state {
	case FormValueSupplied, FormValueConfirmed, FormValueBlanked, FormValueNotApplicable,
		FormValueAmbiguous, FormValueConflicting, FormValueInvalid, FormValueModelSuggested:
	default:
		return errors.New("document protected form answer state is invalid")
	}
	switch source {
	case FormValueSourceUser, FormValueSourceDocument, FormValueSourceDeterministic, FormValueSourceModel:
	default:
		return errors.New("document protected form answer source is invalid")
	}
	if (state == FormValueBlanked || state == FormValueNotApplicable) && kind != ProtectedValueBlank {
		return errors.New("document protected blank state and value disagree")
	}
	if kind == ProtectedValueBlank && state != FormValueBlanked && state != FormValueNotApplicable &&
		state != FormValueInvalid && state != FormValueAmbiguous && state != FormValueConflicting {
		return errors.New("document protected blank state and value disagree")
	}
	if state == FormValueBlanked && !blankReason.valid() {
		return errors.New("document protected blank reason is invalid")
	}
	if state == FormValueNotApplicable && blankReason != FormBlankNotApplicable {
		return errors.New("document protected not-applicable reason is invalid")
	}
	if state != FormValueBlanked && state != FormValueNotApplicable && blankReason != FormBlankNone {
		return errors.New("document protected nonblank value has a blank reason")
	}
	if validationCode != "" && !safeFormJobCodePattern.MatchString(validationCode) {
		return errors.New("document protected form validation code is invalid")
	}
	return nil
}

func validateFormValueMappingClassification(
	state FormValueState,
	confidence FormValueConfidence,
	validation FormValueValidation,
) error {
	if confidence == "" && validation == "" {
		return nil
	}
	switch confidence {
	case FormValueConfidenceExact, FormValueConfidenceHigh, FormValueConfidenceLow,
		FormValueConfidenceConfirmed:
	default:
		return errors.New("document protected form answer confidence is invalid")
	}
	switch validation {
	case FormValueValidationPending, FormValueValidationValid, FormValueValidationInvalid,
		FormValueValidationAmbiguous, FormValueValidationConflicting, FormValueValidationRequiredBlank,
		FormValueValidationUnsupported, FormValueValidationConfirmationRequired:
	default:
		return errors.New("document protected form answer validation state is invalid")
	}
	switch state {
	case FormValueConfirmed, FormValueBlanked, FormValueNotApplicable:
		if validation != FormValueValidationValid && validation != FormValueValidationRequiredBlank {
			return errors.New("document protected form answer confirmation is not valid")
		}
	case FormValueInvalid:
		if validation != FormValueValidationInvalid && validation != FormValueValidationRequiredBlank &&
			validation != FormValueValidationUnsupported {
			return errors.New("document protected form invalid answer validation disagrees")
		}
	case FormValueAmbiguous:
		if validation != FormValueValidationAmbiguous {
			return errors.New("document protected form ambiguous answer validation disagrees")
		}
	case FormValueConflicting:
		if validation != FormValueValidationConflicting {
			return errors.New("document protected form conflicting answer validation disagrees")
		}
	case FormValueModelSuggested:
		if validation != FormValueValidationConfirmationRequired || confidence != FormValueConfidenceLow {
			return errors.New("document protected form suggestion classification is invalid")
		}
	case FormValueSupplied:
		if validation != FormValueValidationPending {
			return errors.New("document protected form supplied answer validation disagrees")
		}
	}
	return nil
}

type FormJobAppendValueRequest struct {
	JobID             string
	ExpectedRevision  int64
	Owner             FormJobOwner
	FieldID           string
	IdempotencyKey    string
	Value             FormProtectedValue
	State             FormValueState
	Source            FormValueSource
	Confidence        FormValueConfidence
	Validation        FormValueValidation
	BlankReason       FormBlankReason
	ValidationCode    string
	SupersedesEventID string
}

type FormJobValueEvent struct {
	EventID           string
	FieldID           string
	Revision          int64
	Value             FormProtectedValue
	State             FormValueState
	Source            FormValueSource
	Confidence        FormValueConfidence
	Validation        FormValueValidation
	BlankReason       FormBlankReason
	ValidationCode    string
	SupersedesEventID string
	CreatedAt         int64
}

func validateFormJobRecord(record FormJobRecord) error {
	if record.SchemaVersion != FormJobSnapshotVersion {
		return ErrFormJobUnsupportedVersion
	}
	if strings.TrimSpace(record.JobID) == "" || len(record.JobID) > maxFormJobIDLength ||
		strings.TrimSpace(record.OwnerDigest) == "" || len(record.OwnerDigest) > maxFormJobDigestLength ||
		strings.TrimSpace(record.StartDigest) == "" || len(record.StartDigest) > maxFormJobDigestLength ||
		strings.TrimSpace(record.SourceDigest) == "" || len(record.SourceDigest) > maxFormJobDigestLength ||
		strings.TrimSpace(record.FieldSchemaDigest) == "" || len(record.FieldSchemaDigest) > maxFormJobDigestLength ||
		strings.TrimSpace(record.BackendRevision) == "" || len(record.BackendRevision) > maxFormJobRevisionLength ||
		strings.TrimSpace(
			record.AuditPolicyRevision,
		) == "" || len(record.AuditPolicyRevision) > maxFormJobRevisionLength ||
		record.Revision <= 0 || record.LedgerRevision < 0 || record.CreatedAt <= 0 || record.UpdatedAt <= 0 ||
		record.ExpiresAt <= record.CreatedAt {
		return ErrFormJobRecordCorrupt
	}
	switch record.State {
	case FormJobPrepared, FormJobCollecting, FormJobReviewReady, FormJobAwaitingApproval,
		FormJobCommitting, FormJobDelivering, FormJobCompleted, FormJobCanceled, FormJobExpired,
		FormJobDeleted, FormJobFailed, FormJobUncertain:
	default:
		return ErrFormJobRecordCorrupt
	}
	if record.State.terminal() && (record.TerminalAt <= 0 || record.CleanupAfter <= record.TerminalAt) {
		return ErrFormJobRecordCorrupt
	}
	if !record.State.terminal() && (record.TerminalAt != 0 || record.CleanupAfter != 0) {
		return ErrFormJobRecordCorrupt
	}
	if record.FailureCode != "" && !safeFormJobCodePattern.MatchString(record.FailureCode) {
		return ErrFormJobRecordCorrupt
	}
	if err := validateFormJobReviewProjection(record); err != nil {
		return err
	}
	if err := validateFormJobCommitProjection(record); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(record.Fields))
	for _, field := range record.Fields {
		if strings.TrimSpace(field.FieldID) == "" || len(field.FieldID) > maxFormJobFieldIDLength ||
			strings.TrimSpace(field.EventID) == "" || len(field.EventID) > maxFormJobEventIDLength ||
			field.UpdatedAt <= 0 {
			return ErrFormJobRecordCorrupt
		}
		if _, duplicate := seen[field.FieldID]; duplicate {
			return ErrFormJobRecordCorrupt
		}
		if validateProtectedValueClassification(
			field.ValueKind,
			field.State,
			field.Source,
			field.BlankReason,
			field.ValidationCode,
		) != nil || validateFormValueMappingClassification(field.State, field.Confidence, field.Validation) != nil ||
			len(field.SupersedesEventID) > maxFormJobEventIDLength {
			return ErrFormJobRecordCorrupt
		}
		seen[field.FieldID] = struct{}{}
	}
	return nil
}

func cloneFormJobRecord(record FormJobRecord) FormJobRecord {
	cloned := record
	cloned.Fields = append([]FormJobFieldState(nil), record.Fields...)
	cloned.AuditBlockers = append([]FormJobReviewBlocker(nil), record.AuditBlockers...)
	return cloned
}

func clearFormJobReviewProjection(record *FormJobRecord) {
	if record == nil {
		return
	}
	record.AuditRevision = 0
	record.AuditDigest = ""
	record.AuditModel = ""
	record.AuditBlockers = nil
	record.ReviewRevision = 0
	record.AssignmentDigest = ""
	record.ReviewDigest = ""
}

func clearFormJobCommitProjection(record *FormJobRecord) {
	if record == nil {
		return
	}
	record.ApprovalRevision = 0
	record.ApprovalDigest = ""
	record.OutputPolicyRevision = ""
	record.OperationID = ""
	record.ArtifactRef = ""
	record.ArtifactDigest = ""
}

func validateFormJobReviewProjection(record FormJobRecord) error {
	for _, value := range []string{
		record.AuditDigest,
		record.AssignmentDigest,
		record.ReviewDigest,
	} {
		if len(value) > maxFormJobDigestLength {
			return ErrFormJobRecordCorrupt
		}
	}
	if len(record.AuditModel) > maxFormJobRevisionLength || len(record.AuditBlockers) > DefaultFormJobMaxEvents {
		return ErrFormJobRecordCorrupt
	}
	seen := make(map[string]struct{}, len(record.AuditBlockers))
	for _, blocker := range record.AuditBlockers {
		if strings.TrimSpace(blocker.FieldID) == "" || len(blocker.FieldID) > maxFormJobFieldIDLength ||
			!safeFormJobCodePattern.MatchString(blocker.Code) {
			return ErrFormJobRecordCorrupt
		}
		key := blocker.FieldID + "\x00" + blocker.Code
		if _, duplicate := seen[key]; duplicate {
			return ErrFormJobRecordCorrupt
		}
		seen[key] = struct{}{}
	}
	if record.AuditRevision == 0 {
		if record.AuditDigest != "" || record.AuditModel != "" || len(record.AuditBlockers) != 0 ||
			record.ReviewRevision != 0 || record.AssignmentDigest != "" || record.ReviewDigest != "" {
			return ErrFormJobRecordCorrupt
		}
		return nil
	}
	if record.AuditRevision > record.Revision || record.AuditDigest == "" || record.AuditModel == "" ||
		record.AssignmentDigest == "" {
		return ErrFormJobRecordCorrupt
	}
	switch record.State {
	case FormJobCollecting:
		if len(record.AuditBlockers) == 0 || record.ReviewRevision != 0 || record.ReviewDigest != "" {
			return ErrFormJobRecordCorrupt
		}
	case FormJobReviewReady:
		if len(record.AuditBlockers) != 0 || record.ReviewRevision != record.Revision || record.ReviewDigest == "" {
			return ErrFormJobRecordCorrupt
		}
	case FormJobAwaitingApproval, FormJobCommitting, FormJobDelivering, FormJobCompleted:
		if len(record.AuditBlockers) != 0 || record.AuditRevision != record.ReviewRevision ||
			record.ReviewRevision <= 0 || record.ReviewRevision >= record.Revision || record.ReviewDigest == "" {
			return ErrFormJobRecordCorrupt
		}
	case FormJobUncertain:
		if record.FailureCode != formJobExpiredDuringCommitFailure || len(record.AuditBlockers) != 0 ||
			record.AuditRevision != record.ReviewRevision || record.ReviewRevision <= 0 ||
			record.ReviewRevision >= record.Revision || record.ReviewDigest == "" {
			return ErrFormJobRecordCorrupt
		}
	default:
		return ErrFormJobRecordCorrupt
	}
	return nil
}

func validateFormJobCommitProjection(record FormJobRecord) error {
	for _, value := range []string{
		record.ApprovalDigest,
		record.OutputPolicyRevision,
		record.OperationID,
		record.ArtifactRef,
		record.ArtifactDigest,
	} {
		if len(value) > maxFormJobRevisionLength {
			return ErrFormJobRecordCorrupt
		}
	}
	hasCommit := record.ApprovalRevision != 0 || record.ApprovalDigest != "" ||
		record.OutputPolicyRevision != "" || record.OperationID != "" ||
		record.ArtifactRef != "" || record.ArtifactDigest != ""
	if !hasCommit {
		switch record.State {
		case FormJobAwaitingApproval, FormJobCommitting, FormJobDelivering, FormJobCompleted:
			return ErrFormJobRecordCorrupt
		}
		return nil
	}
	if record.ApprovalRevision <= record.ReviewRevision || record.ApprovalRevision > record.Revision ||
		record.ApprovalDigest == "" || record.OutputPolicyRevision == "" ||
		!validWriteOperationID(record.OperationID) {
		return ErrFormJobRecordCorrupt
	}
	switch record.State {
	case FormJobAwaitingApproval:
		if record.ApprovalRevision != record.Revision || record.ArtifactRef != "" || record.ArtifactDigest != "" {
			return ErrFormJobRecordCorrupt
		}
	case FormJobCommitting:
		if record.ApprovalRevision >= record.Revision || record.ArtifactRef != "" || record.ArtifactDigest != "" {
			return ErrFormJobRecordCorrupt
		}
	case FormJobDelivering, FormJobCompleted:
		if record.ApprovalRevision >= record.Revision || !validDurableArtifactRef(record.ArtifactRef) ||
			!validDocumentDigest(record.ArtifactDigest) {
			return ErrFormJobRecordCorrupt
		}
	case FormJobFailed, FormJobUncertain:
		if record.ApprovalRevision > record.Revision {
			return ErrFormJobRecordCorrupt
		}
	default:
		return ErrFormJobRecordCorrupt
	}
	return nil
}

func cloneProtectedValue(value FormProtectedValue) FormProtectedValue {
	cloned := value
	cloned.Choices = append([]string(nil), value.Choices...)
	if value.Boolean != nil {
		boolean := *value.Boolean
		cloned.Boolean = &boolean
	}
	return cloned
}

func formJobJSONDigest(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return digestBytes(data), nil
}

func formJobJSONKeyedDigest(key []byte, value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	defer clear(data)
	return keyedDigestBytes(key, "binding", data), nil
}
