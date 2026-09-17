package document

import (
	"encoding/json"
	"errors"
	"fmt"
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
	maxFormJobTextValueLength   = 64 * 1024
	maxFormJobChoices           = 128
	maxFormJobChoiceLength      = 4096
)

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
	FormValueConflicting    FormValueState = "conflicting"
	FormValueInvalid        FormValueState = "invalid"
	FormValueModelSuggested FormValueState = "model_suggested"
)

type FormValueSource string

const (
	FormValueSourceUser          FormValueSource = "user"
	FormValueSourceDocument      FormValueSource = "document"
	FormValueSourceDeterministic FormValueSource = "deterministic"
	FormValueSourceModel         FormValueSource = "model"
)

// FormJobOwner is supplied at every authority-bearing store boundary. Raw
// route values are never persisted; the store records a keyed owner digest.
type FormJobOwner struct {
	AgentID         string
	WorkspaceID     string
	RouteSessionKey string
	Channel         string
	AccountID       string
	ChatID          string
	SenderID        string
	TopicID         string
	SpaceID         string
}

func (owner FormJobOwner) canonical() (string, error) {
	values := []string{
		owner.AgentID,
		owner.WorkspaceID,
		owner.RouteSessionKey,
		owner.Channel,
		owner.AccountID,
		owner.ChatID,
		owner.SenderID,
		owner.TopicID,
		owner.SpaceID,
	}
	for index := range values {
		values[index] = strings.TrimSpace(values[index])
		if len(values[index]) > maxFormJobIdentityLength || !utf8.ValidString(values[index]) {
			return "", errors.New("document form job owner value is invalid")
		}
	}
	if values[0] == "" || values[1] == "" || values[2] == "" || values[3] == "" ||
		values[5] == "" || values[6] == "" {
		return "", errors.New("document form job owner is incomplete")
	}
	return strings.Join(values, "\x00"), nil
}

type FormJobFieldState struct {
	FieldID        string             `json:"field_id"`
	EventID        string             `json:"event_id"`
	ValueKind      ProtectedValueKind `json:"value_kind"`
	State          FormValueState     `json:"state"`
	Source         FormValueSource    `json:"source"`
	BlankReason    string             `json:"blank_reason,omitempty"`
	ValidationCode string             `json:"validation_code,omitempty"`
	UpdatedAt      int64              `json:"updated_at"`
}

// FormJobRecord is the path-free, value-free public projection. It is safe to
// expose to ordinary history and diagnostics after an additional bounded
// presentation projection.
type FormJobRecord struct {
	SchemaVersion       string              `json:"schema_version"`
	JobID               string              `json:"job_id"`
	State               FormJobState        `json:"state"`
	Revision            int64               `json:"revision"`
	OwnerDigest         string              `json:"owner_digest"`
	StartDigest         string              `json:"start_digest"`
	SourceDigest        string              `json:"source_digest"`
	FieldSchemaDigest   string              `json:"field_schema_digest"`
	BackendRevision     string              `json:"backend_revision"`
	AuditPolicyRevision string              `json:"audit_policy_revision"`
	LedgerRevision      int64               `json:"ledger_revision"`
	LedgerDigest        string              `json:"ledger_digest,omitempty"`
	Fields              []FormJobFieldState `json:"fields,omitempty"`
	CreatedAt           int64               `json:"created_at"`
	UpdatedAt           int64               `json:"updated_at"`
	ExpiresAt           int64               `json:"expires_at"`
	TerminalAt          int64               `json:"terminal_at,omitempty"`
	CleanupAfter        int64               `json:"cleanup_after,omitempty"`
	FailureCode         string              `json:"failure_code,omitempty"`
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
		if value.Boolean != nil || len(value.Choices) != 0 {
			return errors.New("document protected text value has incompatible members")
		}
	case ProtectedValueBoolean:
		if value.Boolean == nil || value.Text != "" || len(value.Choices) != 0 {
			return errors.New("document protected boolean value is invalid")
		}
	case ProtectedValueChoice:
		if value.Boolean != nil || value.Text == "" || len(value.Choices) != 0 {
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

type FormJobAppendValueRequest struct {
	JobID             string
	ExpectedRevision  int64
	Owner             FormJobOwner
	FieldID           string
	IdempotencyKey    string
	Value             FormProtectedValue
	State             FormValueState
	Source            FormValueSource
	BlankReason       string
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
	BlankReason       string
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
		seen[field.FieldID] = struct{}{}
	}
	return nil
}

func cloneFormJobRecord(record FormJobRecord) FormJobRecord {
	cloned := record
	cloned.Fields = append([]FormJobFieldState(nil), record.Fields...)
	return cloned
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
