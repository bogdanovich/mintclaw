package eval

import (
	"slices"
	"strings"
)

// PDFCase describes independent expectations, never native approval authority.
// Field expectations stay with the oracle and are excluded from model context.
type PDFCase struct {
	Fixture      string                      `json:"fixture"`
	Output       string                      `json:"output"`
	Draft        bool                        `json:"draft,omitempty"`
	Pages        int                         `json:"pages"`
	FieldCount   int                         `json:"field_count"`
	Fields       []PDFField                  `json:"fields"`
	SourceFields *PDFSourceFieldsExpectation `json:"source_fields,omitempty"`
}

// This first read contract covers the native turn that asked the canceled
// question. Counts describe the fixture, never instructions sent to the model.
type PDFSourceFieldsExpectation struct {
	MinReturnedFields int `json:"min_returned_fields"`
	RequiredFields    int `json:"required_fields"`
	ValuesPresent     int `json:"values_present"`
	DefaultsPresent   int `json:"defaults_present"`
}

type PDFField struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Fact     string `json:"fact,omitempty"`
	Value    string `json:"value,omitempty"`
	Preserve bool   `json:"preserve,omitempty"`
}

func (pdf PDFCase) validate(item Case) error {
	if item.Suite != "pdf" || !validID(pdf.Fixture) || len(item.Fixtures) != 1 ||
		item.Fixtures[0].ID != pdf.Fixture || item.Fixtures[0].Kind != "pdf" ||
		!slices.Contains(item.Requires, "pdf") ||
		!slices.Contains([]string{"required", "forbidden"}, pdf.Output) ||
		pdf.Output == "forbidden" && pdf.Draft ||
		pdf.Pages < 1 || pdf.Pages > 32 || pdf.FieldCount < 1 || pdf.FieldCount > 256 ||
		len(pdf.Fields) == 0 || len(pdf.Fields) != pdf.FieldCount ||
		strings.Count(item.Task, "{{fixture}}") != 1 {
		return ErrInvalidCase
	}
	seen := make(map[string]bool)
	for _, field := range pdf.Fields {
		if !boundedIdentity(field.Name, 128) || seen[field.Name] ||
			!slices.Contains([]string{"text", "checkbox"}, field.Kind) ||
			len(field.Value) > 4096 || field.Value != "" && !boundedText(field.Value, 4096) ||
			field.Fact != "" && (!validID(field.Fact) || item.Driver.Facts[field.Fact] == "" ||
				field.Value != "" || field.Preserve || field.Kind != "text") ||
			field.Preserve && field.Value != "" ||
			field.Kind == "checkbox" && !field.Preserve && field.Value != "true" && field.Value != "false" {
			return ErrInvalidCase
		}
		seen[field.Name] = true
	}
	checks := make(map[string]bool)
	for _, check := range item.Checks {
		checks[check.Kind] = true
	}
	if checks["pdf_source_fields"] != (pdf.SourceFields != nil) {
		return ErrInvalidCase
	}
	if expected := pdf.SourceFields; expected != nil {
		if pdf.Output != "forbidden" || len(item.Driver.Facts) != 0 || expected.MinReturnedFields < 1 ||
			expected.MinReturnedFields > pdf.FieldCount {
			return ErrInvalidCase
		}
		for _, count := range []int{expected.RequiredFields, expected.ValuesPresent, expected.DefaultsPresent} {
			if count < 0 || count > pdf.FieldCount {
				return ErrInvalidCase
			}
		}
	}
	if !checks["private_surfaces"] ||
		pdf.Output == "required" && (!checks["pdf_fields"] || !checks["pdf_structure"] || !checks["pdf_delivery"]) ||
		pdf.Output == "forbidden" && (!checks["pdf_no_write"] || checks["pdf_delivery"]) {
		return ErrInvalidCase
	}
	return nil
}
