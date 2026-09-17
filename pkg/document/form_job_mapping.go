package document

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const FormFieldSchemaDigestVersion = "mintclaw.document_form_field_schema.v1"

type FormFieldMappingRequest struct {
	JobID            string
	ExpectedRevision int64
	Owner            FormJobOwner
	Schema           FormFieldsFacts
	FieldID          string
	SourceEventID    string
	IdempotencyKey   string
}

type FormFieldMappingResult struct {
	Job    FormJobRecord
	Field  FormJobFieldState
	Event  FormJobValueEvent
	Reused bool
}

type FormFieldMappingBlocker struct {
	FieldID string `json:"field_id"`
	Code    string `json:"code"`
}

// FormJobMappingSummary is a bounded, value-free projection suitable for
// ordinary history and diagnostics.
type FormJobMappingSummary struct {
	JobID              string                    `json:"job_id"`
	Revision           int64                     `json:"revision"`
	FieldSchemaDigest  string                    `json:"field_schema_digest"`
	ConfirmedFieldIDs  []string                  `json:"confirmed_field_ids,omitempty"`
	Unresolved         []FormFieldMappingBlocker `json:"unresolved,omitempty"`
	NextUnresolvedID   string                    `json:"next_unresolved_id,omitempty"`
	ReadyForReview     bool                      `json:"ready_for_review"`
	WritableFieldCount int                       `json:"writable_field_count"`
}

type mappedFormValue struct {
	Value          FormProtectedValue
	State          FormValueState
	Confidence     FormValueConfidence
	Validation     FormValueValidation
	BlankReason    FormBlankReason
	ValidationCode string
}

// FormFieldSchemaDigest returns the immutable digest used to bind a PDF3 job
// to one PDF2 field schema. The source digest and backend revision are bound
// separately and are intentionally excluded from this digest.
func FormFieldSchemaDigest(facts FormFieldsFacts) (string, error) {
	if !validFormFieldsFacts(facts) {
		return "", errors.New("document form field schema is invalid")
	}
	canonical := struct {
		SchemaVersion string          `json:"schema_version"`
		Limits        FormFieldLimits `json:"limits"`
		Fields        []FormField     `json:"fields"`
	}{
		SchemaVersion: FormFieldSchemaDigestVersion,
		Limits:        facts.Limits,
		Fields:        facts.Fields,
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", errors.New("document form field schema is invalid")
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// FormFieldsBackendRevision binds the exact field-discovery backend identity.
func FormFieldsBackendRevision(facts FormFieldsFacts) (string, error) {
	if !validFormFieldsFacts(facts) {
		return "", errors.New("document form field schema is invalid")
	}
	return strings.Join([]string{
		facts.Backend.Name,
		facts.Backend.Version,
		facts.Backend.Role,
		facts.Backend.IsolationMode,
	}, ":"), nil
}

// MapFormField deterministically validates and normalizes one committed
// protected answer. The raw value never leaves the encrypted ledger.
func (store *FormJobStore) MapFormField(
	ctx context.Context,
	request FormFieldMappingRequest,
) (FormFieldMappingResult, error) {
	if err := validateFormFieldMappingRequest(request); err != nil {
		return FormFieldMappingResult{}, err
	}
	record, err := store.Get(ctx, request.JobID, request.Owner)
	if err != nil {
		return FormFieldMappingResult{}, err
	}
	if matchErr := formJobMatchesSchema(record, request.Schema); matchErr != nil {
		return FormFieldMappingResult{}, matchErr
	}
	fieldIndex := slices.IndexFunc(request.Schema.Fields, func(field FormField) bool {
		return field.ID == request.FieldID
	})
	if fieldIndex < 0 {
		return FormFieldMappingResult{}, ErrFormJobStale
	}
	source, err := store.readFormJobValueEvent(ctx, request.JobID, request.Owner, request.SourceEventID)
	if err != nil {
		return FormFieldMappingResult{}, err
	}
	if source.FieldID != request.FieldID {
		return FormFieldMappingResult{}, ErrFormJobConflict
	}
	currentIndex := slices.IndexFunc(record.Fields, func(field FormJobFieldState) bool {
		return field.FieldID == request.FieldID
	})
	currentSource := currentIndex >= 0 && record.Fields[currentIndex].EventID == source.EventID
	if currentSource && (!formValueSourceMappable(source) ||
		formFieldStateResolved(record.Fields[currentIndex], request.Schema.Fields[fieldIndex])) {
		return FormFieldMappingResult{
			Job: record, Field: record.Fields[currentIndex], Event: source, Reused: true,
		}, nil
	}
	if !formValueSourceMappable(source) {
		return FormFieldMappingResult{}, ErrFormJobConflict
	}
	mapped := mapProtectedFormValue(request.Schema.Fields[fieldIndex], source)
	updated, event, err := store.AppendValue(ctx, FormJobAppendValueRequest{
		JobID:             request.JobID,
		ExpectedRevision:  request.ExpectedRevision,
		Owner:             request.Owner,
		FieldID:           request.FieldID,
		IdempotencyKey:    "mapping:" + request.IdempotencyKey,
		Value:             mapped.Value,
		State:             mapped.State,
		Source:            FormValueSourceDeterministic,
		Confidence:        mapped.Confidence,
		Validation:        mapped.Validation,
		BlankReason:       mapped.BlankReason,
		ValidationCode:    mapped.ValidationCode,
		SupersedesEventID: source.EventID,
	})
	if err != nil {
		return FormFieldMappingResult{}, err
	}
	updatedIndex := slices.IndexFunc(updated.Fields, func(field FormJobFieldState) bool {
		return field.FieldID == request.FieldID
	})
	if updatedIndex < 0 {
		return FormFieldMappingResult{}, ErrFormJobRecordCorrupt
	}
	return FormFieldMappingResult{
		Job:    updated,
		Field:  updated.Fields[updatedIndex],
		Event:  event,
		Reused: record.Revision != request.ExpectedRevision || updated.Fields[updatedIndex].EventID != event.EventID,
	}, nil
}

func formValueSourceMappable(source FormJobValueEvent) bool {
	if source.Source == FormValueSourceModel || source.Confidence != "" || source.Validation != "" {
		return false
	}
	switch source.State {
	case FormValueSupplied, FormValueBlanked, FormValueNotApplicable:
		return true
	default:
		return false
	}
}

// FormMappingSummary returns the next unresolved stable field without reading
// or exposing protected values. Confirmed fields are omitted from the prompt
// candidate so they are not re-asked after restart or compaction.
func (store *FormJobStore) FormMappingSummary(
	ctx context.Context,
	jobID string,
	owner FormJobOwner,
	schema FormFieldsFacts,
) (FormJobMappingSummary, error) {
	record, err := store.Get(ctx, jobID, owner)
	if err != nil {
		return FormJobMappingSummary{}, err
	}
	if matchErr := formJobMatchesSchema(record, schema); matchErr != nil {
		return FormJobMappingSummary{}, matchErr
	}
	current := make(map[string]FormJobFieldState, len(record.Fields))
	for _, state := range record.Fields {
		current[state.FieldID] = state
	}
	summary := FormJobMappingSummary{
		JobID: record.JobID, Revision: record.Revision, FieldSchemaDigest: record.FieldSchemaDigest,
	}
	for _, field := range schema.Fields {
		if field.ReadOnly {
			continue
		}
		summary.WritableFieldCount++
		state, found := current[field.ID]
		if !found {
			if field.HasValue {
				summary.ConfirmedFieldIDs = append(summary.ConfirmedFieldIDs, field.ID)
				continue
			}
			summary.Unresolved = append(summary.Unresolved, FormFieldMappingBlocker{
				FieldID: field.ID, Code: "field_unresolved",
			})
			continue
		}
		if formFieldStateResolved(state, field) {
			summary.ConfirmedFieldIDs = append(summary.ConfirmedFieldIDs, field.ID)
			continue
		}
		summary.Unresolved = append(summary.Unresolved, FormFieldMappingBlocker{
			FieldID: field.ID, Code: formFieldBlockerCode(state, field),
		})
	}
	if len(summary.Unresolved) != 0 {
		summary.NextUnresolvedID = summary.Unresolved[0].FieldID
	} else {
		summary.ReadyForReview = true
	}
	return summary, nil
}

func validateFormFieldMappingRequest(request FormFieldMappingRequest) error {
	if _, err := request.Owner.canonical(); err != nil {
		return err
	}
	if strings.TrimSpace(request.JobID) == "" || len(request.JobID) > maxFormJobIDLength ||
		request.ExpectedRevision <= 0 || strings.TrimSpace(request.FieldID) == "" ||
		len(request.FieldID) > maxFormJobFieldIDLength || !utf8.ValidString(request.FieldID) ||
		strings.TrimSpace(request.SourceEventID) == "" || len(request.SourceEventID) > maxFormJobEventIDLength ||
		strings.TrimSpace(request.IdempotencyKey) == "" ||
		len(request.IdempotencyKey) > maxFormJobIdempotencyLength-8 || !utf8.ValidString(request.IdempotencyKey) ||
		!validFormFieldsFacts(request.Schema) {
		return errors.New("document form field mapping request is invalid")
	}
	return nil
}

func formJobMatchesSchema(record FormJobRecord, schema FormFieldsFacts) error {
	if record.State == FormJobExpired {
		return ErrFormJobExpired
	}
	if record.State.terminal() {
		return ErrFormJobTerminal
	}
	digest, err := FormFieldSchemaDigest(schema)
	if err != nil {
		return err
	}
	backendRevision, err := FormFieldsBackendRevision(schema)
	if err != nil {
		return err
	}
	if record.SourceDigest != schema.SourceSHA256 || record.FieldSchemaDigest != digest ||
		record.BackendRevision != backendRevision {
		return ErrFormJobStale
	}
	return nil
}

func (store *FormJobStore) readFormJobValueEvent(
	ctx context.Context,
	jobID string,
	owner FormJobOwner,
	eventID string,
) (FormJobValueEvent, error) {
	var result FormJobValueEvent
	err := store.update(ctx, func(document *formJobStoreDocument, _ time.Time) (bool, error) {
		record, jobKey, err := store.authorizedRecord(document, jobID, owner)
		if err != nil {
			return false, err
		}
		defer clear(jobKey)
		for _, envelope := range record.Events {
			if envelope.EventID != eventID {
				continue
			}
			payload, err := openFormJobValuePayload(jobKey, envelope)
			if err != nil {
				return false, err
			}
			result = publicFormJobValueEvent(payload)
			return false, nil
		}
		return false, ErrFormJobNotFound
	})
	return result, err
}

func mapProtectedFormValue(field FormField, source FormJobValueEvent) mappedFormValue {
	if field.ReadOnly {
		return invalidMappedValue(source.Value, FormValueValidationUnsupported, "field_unsupported")
	}
	if source.Value.Kind == ProtectedValueBlank {
		state := source.State
		if state != FormValueNotApplicable {
			state = FormValueBlanked
		}
		validation := FormValueValidationValid
		code := ""
		if field.Required {
			validation = FormValueValidationRequiredBlank
			code = "field_required_blank"
		}
		return mappedFormValue{
			Value: source.Value, State: state, Confidence: FormValueConfidenceExact,
			Validation: validation, BlankReason: source.BlankReason, ValidationCode: code,
		}
	}
	value, validation, code := deterministicFormValue(field, source.Value)
	if validation != FormValueValidationValid {
		return invalidMappedValue(source.Value, validation, code)
	}
	return mappedFormValue{
		Value: value, State: FormValueConfirmed, Confidence: FormValueConfidenceExact,
		Validation: FormValueValidationValid,
	}
}

func deterministicFormValue(
	field FormField,
	protected FormProtectedValue,
) (FormProtectedValue, FormValueValidation, string) {
	var candidate FormValue
	switch field.Kind {
	case FormFieldText, FormFieldDate:
		if protected.Kind != ProtectedValueText {
			return FormProtectedValue{}, FormValueValidationInvalid, "field_invalid"
		}
		text := protected.Text
		candidate = FormValue{Type: FormValueText, Text: &text}
	case FormFieldCheckbox:
		checked, ok := deterministicBoolean(protected)
		if !ok {
			return FormProtectedValue{}, FormValueValidationInvalid, "field_invalid"
		}
		candidate = FormValue{Type: FormValueBoolean, Checked: &checked}
	case FormFieldRadio, FormFieldCombo:
		choices, validation := deterministicChoices(field, protected)
		if validation != FormValueValidationValid {
			return FormProtectedValue{}, validation, validationCode(validation)
		}
		if len(choices) != 1 {
			return FormProtectedValue{}, FormValueValidationConflicting, "field_conflicting"
		}
		candidate = FormValue{Type: FormValueChoice, Choices: choices}
	case FormFieldList:
		choices, validation := deterministicChoices(field, protected)
		if validation != FormValueValidationValid {
			return FormProtectedValue{}, validation, validationCode(validation)
		}
		if !field.MultiSelect && len(choices) != 1 {
			return FormProtectedValue{}, FormValueValidationConflicting, "field_conflicting"
		}
		candidate = FormValue{Type: FormValueChoices, Choices: choices}
	default:
		return FormProtectedValue{}, FormValueValidationUnsupported, "field_unsupported"
	}
	normalized, err := normalizeFormValue(field, candidate)
	if err != nil {
		return FormProtectedValue{}, FormValueValidationInvalid, "field_invalid"
	}
	return protectedValueFromNormalized(normalized), FormValueValidationValid, ""
}

func deterministicBoolean(value FormProtectedValue) (bool, bool) {
	if value.Kind == ProtectedValueBoolean && value.Boolean != nil {
		return *value.Boolean, true
	}
	if value.Kind != ProtectedValueText {
		return false, false
	}
	switch strings.ToLower(strings.TrimSpace(value.Text)) {
	case "yes", "true", "1", "on", "checked":
		return true, true
	case "no", "false", "0", "off", "unchecked":
		return false, true
	default:
		return false, false
	}
}

func deterministicChoices(
	field FormField,
	value FormProtectedValue,
) ([]string, FormValueValidation) {
	var candidates []string
	switch value.Kind {
	case ProtectedValueText, ProtectedValueChoice:
		candidates = []string{value.Text}
	case ProtectedValueChoices:
		candidates = value.Choices
	default:
		return nil, FormValueValidationInvalid
	}
	if len(candidates) == 0 {
		return nil, FormValueValidationInvalid
	}
	resolved := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		export, validation := resolveFormChoice(field.Options, candidate)
		if validation != FormValueValidationValid {
			return nil, validation
		}
		if _, duplicate := seen[export]; duplicate {
			return nil, FormValueValidationConflicting
		}
		seen[export] = struct{}{}
		resolved = append(resolved, export)
	}
	return resolved, FormValueValidationValid
}

func resolveFormChoice(options []FormFieldOption, candidate string) (string, FormValueValidation) {
	candidate = strings.TrimSpace(candidate)
	for _, option := range options {
		if candidate == option.Export {
			return option.Export, FormValueValidationValid
		}
	}
	matches := make([]string, 0, 2)
	for _, option := range options {
		if candidate == option.Display || strings.EqualFold(candidate, option.Export) ||
			strings.EqualFold(candidate, option.Display) {
			if !slices.Contains(matches, option.Export) {
				matches = append(matches, option.Export)
			}
		}
	}
	switch len(matches) {
	case 0:
		return "", FormValueValidationInvalid
	case 1:
		return matches[0], FormValueValidationValid
	default:
		return "", FormValueValidationAmbiguous
	}
}

func protectedValueFromNormalized(value FormValue) FormProtectedValue {
	switch value.Type {
	case FormValueText:
		return FormProtectedValue{Kind: ProtectedValueText, Text: *value.Text}
	case FormValueBoolean:
		checked := *value.Checked
		return FormProtectedValue{Kind: ProtectedValueBoolean, Boolean: &checked}
	case FormValueChoice:
		return FormProtectedValue{Kind: ProtectedValueChoice, Text: value.Choices[0]}
	case FormValueChoices:
		return FormProtectedValue{Kind: ProtectedValueChoices, Choices: append([]string(nil), value.Choices...)}
	default:
		return FormProtectedValue{}
	}
}

func invalidMappedValue(
	value FormProtectedValue,
	validation FormValueValidation,
	code string,
) mappedFormValue {
	state := FormValueInvalid
	switch validation {
	case FormValueValidationAmbiguous:
		state = FormValueAmbiguous
	case FormValueValidationConflicting:
		state = FormValueConflicting
	}
	return mappedFormValue{
		Value: cloneProtectedValue(value), State: state, Confidence: FormValueConfidenceExact,
		Validation: validation, ValidationCode: code,
	}
}

func validationCode(validation FormValueValidation) string {
	switch validation {
	case FormValueValidationAmbiguous:
		return "field_ambiguous"
	case FormValueValidationConflicting:
		return "field_conflicting"
	case FormValueValidationUnsupported:
		return "field_unsupported"
	default:
		return "field_invalid"
	}
}

func formFieldStateResolved(state FormJobFieldState, field FormField) bool {
	if state.Confidence == FormValueConfidenceLow || state.Validation != FormValueValidationValid {
		return false
	}
	if field.Required && (state.State == FormValueBlanked || state.State == FormValueNotApplicable) {
		return false
	}
	switch state.State {
	case FormValueConfirmed, FormValueBlanked, FormValueNotApplicable:
		return true
	default:
		return false
	}
}

func formFieldBlockerCode(state FormJobFieldState, field FormField) string {
	if field.Required && (state.State == FormValueBlanked || state.State == FormValueNotApplicable ||
		state.Validation == FormValueValidationRequiredBlank) {
		return "field_required_blank"
	}
	switch state.Validation {
	case FormValueValidationAmbiguous:
		return "field_ambiguous"
	case FormValueValidationConflicting:
		return "field_conflicting"
	case FormValueValidationInvalid:
		return "field_invalid"
	case FormValueValidationUnsupported:
		return "field_unsupported"
	case FormValueValidationConfirmationRequired:
		return "field_confirmation_required"
	}
	if state.Confidence == FormValueConfidenceLow || state.State == FormValueModelSuggested {
		return "field_confirmation_required"
	}
	return "field_unresolved"
}
