package document

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/primitives"
)

const (
	FillMapSchemaVersion        = "mintclaw.document_fill_map.v1"
	NormalizedFillSchemaVersion = "mintclaw.document_normalized_fill.v1"
	DefaultMaxFormValueBytes    = 8 * 1024
	DefaultMaxFillRequestBytes  = 48 * 1024
)

type FormValueType string

const (
	FormValueText    FormValueType = "text"
	FormValueBoolean FormValueType = "boolean"
	FormValueChoice  FormValueType = "choice"
	FormValueChoices FormValueType = "choices"
)

// FormValue is a discriminated value. Exactly one payload is admitted for
// its Type, including a pointer for empty text and false boolean values.
type FormValue struct {
	Type    FormValueType `json:"type"`
	Text    *string       `json:"text,omitempty"`
	Checked *bool         `json:"checked,omitempty"`
	Choices []string      `json:"choices,omitempty"`
}

type FormFillAssignment struct {
	FieldID string    `json:"field_id"`
	Value   FormValue `json:"value"`
}

type FillMap struct {
	SchemaVersion string               `json:"schema_version"`
	Assignments   []FormFillAssignment `json:"assignments"`
}

// NormalizedFillRequest is sensitive ephemeral input for the one-shot worker.
// It must never be placed in ordinary durable history or diagnostic output.
type NormalizedFillRequest struct {
	SchemaVersion string               `json:"schema_version"`
	SourceSHA256  string               `json:"source_sha256"`
	RequestSHA256 string               `json:"request_sha256"`
	Assignments   []FormFillAssignment `json:"assignments"`
	AffectedPages []int                `json:"affected_pages"`
}

type FillRequestError struct {
	Code FailureCode
}

func DecodeFillMap(reader io.Reader) (FillMap, error) {
	var fill FillMap
	if reader == nil {
		return FillMap{}, fillRequestFailure(FailureInvalidInput)
	}
	data, err := io.ReadAll(io.LimitReader(reader, DefaultMaxFillRequestBytes+1))
	if err != nil {
		return FillMap{}, fillRequestFailure(FailureInvalidInput)
	}
	if len(data) > DefaultMaxFillRequestBytes {
		return FillMap{}, fillRequestFailure(FailureLimitExceeded)
	}
	if decodeBoundedJSON(bytes.NewReader(data), DefaultMaxFillRequestBytes, &fill) != nil {
		return FillMap{}, fillRequestFailure(FailureInvalidInput)
	}
	return fill, nil
}

func (e *FillRequestError) Error() string {
	messages := map[FailureCode]string{
		FailureInvalidInput:      "document fill map is invalid",
		FailureLimitExceeded:     "document fill map exceeds a limit",
		FailureFieldNotFound:     "document fill field was not found",
		FailureFieldAmbiguous:    "document fill field is ambiguous",
		FailureFieldReadOnly:     "document fill field is read-only",
		FailureFieldValueInvalid: "document fill field value is invalid",
		FailureChoiceInvalid:     "document fill choice is invalid",
	}
	if message := messages[e.Code]; message != "" {
		return message
	}
	return "document fill map is invalid"
}

func fillRequestFailure(code FailureCode) error {
	return &FillRequestError{Code: code}
}

// NormalizeFillMap validates one explicit source-bound field map and returns a
// canonical request. It preserves meaningful whitespace and never includes a
// host path or backend object identity.
func NormalizeFillMap(
	input DocumentRef,
	facts FormFieldsFacts,
	fill FillMap,
) (NormalizedFillRequest, error) {
	if fill.SchemaVersion != FillMapSchemaVersion || !validDocumentDigest(input.SHA256) ||
		!validFormFieldsFacts(facts) || facts.SourceSHA256 != input.SHA256 || len(fill.Assignments) == 0 ||
		len(fill.Assignments) > facts.Limits.MaxFields {
		return NormalizedFillRequest{}, fillRequestFailure(FailureInvalidInput)
	}
	fields := make(map[string]FormField, len(facts.Fields))
	for _, field := range facts.Fields {
		fields[field.ID] = field
	}
	assignments := make([]FormFillAssignment, 0, len(fill.Assignments))
	affectedPages := map[int]struct{}{}
	seen := make(map[string]struct{}, len(fill.Assignments))
	for _, assignment := range fill.Assignments {
		if _, duplicate := seen[assignment.FieldID]; duplicate {
			return NormalizedFillRequest{}, fillRequestFailure(FailureFieldAmbiguous)
		}
		seen[assignment.FieldID] = struct{}{}
		field, found := fields[assignment.FieldID]
		if !found {
			return NormalizedFillRequest{}, fillRequestFailure(FailureFieldNotFound)
		}
		if field.ReadOnly {
			return NormalizedFillRequest{}, fillRequestFailure(FailureFieldReadOnly)
		}
		value, err := normalizeFormValue(field, assignment.Value)
		if err != nil {
			return NormalizedFillRequest{}, err
		}
		assignments = append(assignments, FormFillAssignment{FieldID: field.ID, Value: value})
		for _, widget := range field.Widgets {
			affectedPages[widget.Page] = struct{}{}
		}
	}
	sort.Slice(assignments, func(left, right int) bool {
		return assignments[left].FieldID < assignments[right].FieldID
	})
	pages := make([]int, 0, len(affectedPages))
	for page := range affectedPages {
		pages = append(pages, page)
	}
	sort.Ints(pages)
	canonical := struct {
		SchemaVersion string               `json:"schema_version"`
		SourceSHA256  string               `json:"source_sha256"`
		Assignments   []FormFillAssignment `json:"assignments"`
		AffectedPages []int                `json:"affected_pages"`
	}{
		SchemaVersion: NormalizedFillSchemaVersion,
		SourceSHA256:  input.SHA256,
		Assignments:   assignments,
		AffectedPages: pages,
	}
	encoded, err := json.Marshal(canonical)
	if err != nil || len(encoded) > DefaultMaxFillRequestBytes {
		return NormalizedFillRequest{}, fillRequestFailure(FailureLimitExceeded)
	}
	digest := sha256.Sum256(encoded)
	return NormalizedFillRequest{
		SchemaVersion: NormalizedFillSchemaVersion,
		SourceSHA256:  input.SHA256,
		RequestSHA256: hex.EncodeToString(digest[:]),
		Assignments:   assignments,
		AffectedPages: pages,
	}, nil
}

func normalizeFormValue(field FormField, value FormValue) (FormValue, error) {
	switch field.Kind {
	case FormFieldText, FormFieldDate:
		return normalizeTextFormValue(field, value)
	case FormFieldCheckbox:
		if value.Type != FormValueBoolean || value.Checked == nil || value.Text != nil ||
			len(value.Choices) != 0 || (field.Required && !*value.Checked) {
			return FormValue{}, fillRequestFailure(FailureFieldValueInvalid)
		}
		checked := *value.Checked
		return FormValue{Type: FormValueBoolean, Checked: &checked}, nil
	case FormFieldRadio, FormFieldCombo:
		if value.Type != FormValueChoice || value.Text != nil || value.Checked != nil ||
			len(value.Choices) != 1 || !fieldHasChoice(field, value.Choices[0]) {
			return FormValue{}, fillRequestFailure(FailureChoiceInvalid)
		}
		return FormValue{Type: FormValueChoice, Choices: []string{value.Choices[0]}}, nil
	case FormFieldList:
		if value.Type != FormValueChoices || value.Text != nil || value.Checked != nil ||
			len(value.Choices) == 0 || (!field.MultiSelect && len(value.Choices) != 1) {
			return FormValue{}, fillRequestFailure(FailureChoiceInvalid)
		}
		selected := make(map[string]struct{}, len(value.Choices))
		for _, choice := range value.Choices {
			if _, duplicate := selected[choice]; duplicate || !fieldHasChoice(field, choice) {
				return FormValue{}, fillRequestFailure(FailureChoiceInvalid)
			}
			selected[choice] = struct{}{}
		}
		choices := make([]string, 0, len(selected))
		for _, option := range field.Options {
			if _, found := selected[option.Export]; found {
				choices = append(choices, option.Export)
			}
		}
		return FormValue{Type: FormValueChoices, Choices: choices}, nil
	default:
		return FormValue{}, fillRequestFailure(FailureFieldValueInvalid)
	}
}

func normalizeTextFormValue(field FormField, value FormValue) (FormValue, error) {
	if value.Type != FormValueText || value.Text == nil || value.Checked != nil || len(value.Choices) != 0 {
		return FormValue{}, fillRequestFailure(FailureFieldValueInvalid)
	}
	text := *value.Text
	if field.Multiline {
		text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	}
	if !validFormValueText(text, field.Multiline) || len(text) > DefaultMaxFormValueBytes ||
		(field.MaxLength > 0 && utf8.RuneCountInString(text) > field.MaxLength) ||
		(field.Required && text == "") {
		return FormValue{}, fillRequestFailure(FailureFieldValueInvalid)
	}
	if field.Kind == FormFieldDate && text != "" {
		format, err := primitives.DateFormatForFmtExt(field.DateFormat)
		if err != nil {
			return FormValue{}, fillRequestFailure(FailureFieldValueInvalid)
		}
		if _, err = time.Parse(format.Int, text); err != nil {
			return FormValue{}, fillRequestFailure(FailureFieldValueInvalid)
		}
	}
	return FormValue{Type: FormValueText, Text: &text}, nil
}

func validFormValueText(value string, multiline bool) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) && (!multiline || character != '\n') {
			return false
		}
	}
	return true
}

func fieldHasChoice(field FormField, value string) bool {
	for _, option := range field.Options {
		if option.Export == value {
			return true
		}
	}
	return false
}

func validNormalizedFillRequest(request NormalizedFillRequest) bool {
	if request.SchemaVersion != NormalizedFillSchemaVersion || !validDocumentDigest(request.SourceSHA256) ||
		!validDocumentDigest(request.RequestSHA256) || len(request.Assignments) == 0 ||
		len(request.Assignments) > DefaultMaxFormFields || len(request.AffectedPages) == 0 ||
		len(request.AffectedPages) > DefaultMaxFieldWidgets {
		return false
	}
	previousField := ""
	for _, assignment := range request.Assignments {
		if !opaqueFieldID.MatchString(assignment.FieldID) || assignment.FieldID <= previousField ||
			!validNormalizedFormValue(assignment.Value) {
			return false
		}
		previousField = assignment.FieldID
	}
	previousPage := 0
	for _, page := range request.AffectedPages {
		if page <= previousPage || page > DefaultMaxPages {
			return false
		}
		previousPage = page
	}
	canonical := struct {
		SchemaVersion string               `json:"schema_version"`
		SourceSHA256  string               `json:"source_sha256"`
		Assignments   []FormFillAssignment `json:"assignments"`
		AffectedPages []int                `json:"affected_pages"`
	}{
		SchemaVersion: request.SchemaVersion,
		SourceSHA256:  request.SourceSHA256,
		Assignments:   request.Assignments,
		AffectedPages: request.AffectedPages,
	}
	encoded, err := json.Marshal(canonical)
	if err != nil || len(encoded) > DefaultMaxFillRequestBytes {
		return false
	}
	digest := sha256.Sum256(encoded)
	return request.RequestSHA256 == hex.EncodeToString(digest[:])
}

func validNormalizedFormValue(value FormValue) bool {
	switch value.Type {
	case FormValueText:
		return value.Text != nil && value.Checked == nil && len(value.Choices) == 0 &&
			len(*value.Text) <= DefaultMaxFormValueBytes && validFormValueText(*value.Text, true)
	case FormValueBoolean:
		return value.Text == nil && value.Checked != nil && len(value.Choices) == 0
	case FormValueChoice:
		return value.Text == nil && value.Checked == nil && len(value.Choices) == 1 &&
			validFieldText(value.Choices[0], DefaultMaxFieldTextBytes)
	case FormValueChoices:
		if value.Text != nil || value.Checked != nil || len(value.Choices) == 0 ||
			len(value.Choices) > DefaultMaxFieldOptions {
			return false
		}
		seen := make(map[string]struct{}, len(value.Choices))
		for _, choice := range value.Choices {
			if !validFieldText(choice, DefaultMaxFieldTextBytes) {
				return false
			}
			if _, duplicate := seen[choice]; duplicate {
				return false
			}
			seen[choice] = struct{}{}
		}
		return true
	default:
		return false
	}
}

func validDocumentDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func FillRequestFailureCode(err error) FailureCode {
	var typed *FillRequestError
	if errors.As(err, &typed) {
		return typed.Code
	}
	return FailureInternal
}
