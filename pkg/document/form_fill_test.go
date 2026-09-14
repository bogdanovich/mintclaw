package document

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestNormalizeFillMapCanonicalizesSupportedValues(t *testing.T) {
	input, facts := fillNormalizationFixture()
	values := []FormFillAssignment{
		fillTextAssignment(8, "09/14/2026"),
		{FieldID: fillFieldID(7), Value: FormValue{Type: FormValueChoices, Choices: []string{"one"}}},
		{FieldID: fillFieldID(6), Value: FormValue{Type: FormValueChoices, Choices: []string{"three", "one"}}},
		{FieldID: fillFieldID(5), Value: FormValue{Type: FormValueChoice, Choices: []string{"CA"}}},
		{FieldID: fillFieldID(4), Value: FormValue{Type: FormValueChoice, Choices: []string{" EU "}}},
		fillBooleanAssignment(3, false),
		fillTextAssignment(2, "first\r\nsecond\rthird"),
		fillTextAssignment(1, "Zoë 東京"),
	}
	normalized, err := NormalizeFillMap(input, facts, FillMap{
		SchemaVersion: FillMapSchemaVersion,
		Assignments:   values,
	})
	if err != nil {
		t.Fatal(err)
	}
	if normalized.SchemaVersion != NormalizedFillSchemaVersion ||
		normalized.SourceSHA256 != input.SHA256 || !validDocumentDigest(normalized.RequestSHA256) ||
		!slices.Equal(normalized.AffectedPages, []int{1, 2}) || len(normalized.Assignments) != 8 {
		t.Fatalf("normalized request = %#v", normalized)
	}
	for index, assignment := range normalized.Assignments {
		if assignment.FieldID != fillFieldID(index+1) {
			t.Fatalf("assignment order = %#v", normalized.Assignments)
		}
	}
	if got := *normalized.Assignments[1].Value.Text; got != "first\nsecond\nthird" {
		t.Fatalf("normalized multiline value = %q", got)
	}
	if got := normalized.Assignments[5].Value.Choices; !slices.Equal(got, []string{"one", "three"}) {
		t.Fatalf("normalized list choices = %#v", got)
	}
	if got := normalized.Assignments[3].Value.Choices[0]; got != " EU " {
		t.Fatalf("exact choice = %q", got)
	}
	tampered := normalized
	tampered.AffectedPages = []int{1}
	if validNormalizedFillRequest(tampered) {
		t.Fatal("normalized request accepted an affected-page digest mismatch")
	}

	reversed := append([]FormFillAssignment(nil), values...)
	slices.Reverse(reversed)
	reversed[1] = fillTextAssignment(2, "first\nsecond\nthird")
	second, err := NormalizeFillMap(input, facts, FillMap{
		SchemaVersion: FillMapSchemaVersion,
		Assignments:   reversed,
	})
	if err != nil || second.RequestSHA256 != normalized.RequestSHA256 {
		t.Fatalf("canonical digest = %q, want %q, err=%v", second.RequestSHA256, normalized.RequestSHA256, err)
	}
}

func TestNormalizeFillMapRejectsInvalidAssignments(t *testing.T) {
	input, facts := fillNormalizationFixture()
	tests := []struct {
		name string
		fill FillMap
		edit func(*FormFieldsFacts)
		code FailureCode
	}{
		{
			name: "schema",
			fill: FillMap{Assignments: []FormFillAssignment{fillTextAssignment(1, "value")}},
			code: FailureInvalidInput,
		},
		{
			name: "unknown field",
			fill: validFillMap(FormFillAssignment{FieldID: fillFieldID(99), Value: textFormValue("value")}),
			code: FailureFieldNotFound,
		},
		{
			name: "duplicate field",
			fill: FillMap{SchemaVersion: FillMapSchemaVersion, Assignments: []FormFillAssignment{
				fillTextAssignment(1, "one"), fillTextAssignment(1, "two"),
			}},
			code: FailureFieldAmbiguous,
		},
		{
			name: "read only", fill: validFillMap(fillTextAssignment(1, "value")),
			edit: func(facts *FormFieldsFacts) { facts.Fields[0].ReadOnly = true }, code: FailureFieldReadOnly,
		},
		{
			name: "wrong type", fill: validFillMap(fillBooleanAssignment(1, true)),
			code: FailureFieldValueInvalid,
		},
		{
			name: "single line control", fill: validFillMap(fillTextAssignment(1, "one\ntwo")),
			code: FailureFieldValueInvalid,
		},
		{
			name: "maximum length", fill: validFillMap(fillTextAssignment(1, strings.Repeat("x", 21))),
			code: FailureFieldValueInvalid,
		},
		{
			name: "required empty", fill: validFillMap(fillTextAssignment(1, "")),
			code: FailureFieldValueInvalid,
		},
		{
			name: "required unchecked",
			fill: validFillMap(fillBooleanAssignment(3, false)),
			edit: func(facts *FormFieldsFacts) { facts.Fields[2].Required = true },
			code: FailureFieldValueInvalid,
		},
		{
			name: "invalid date", fill: validFillMap(fillTextAssignment(8, "2026-09-14")),
			code: FailureFieldValueInvalid,
		},
		{
			name: "inexact choice",
			fill: validFillMap(FormFillAssignment{
				FieldID: fillFieldID(4), Value: FormValue{Type: FormValueChoice, Choices: []string{"EU"}},
			}),
			code: FailureChoiceInvalid,
		},
		{
			name: "duplicate multi choice",
			fill: validFillMap(FormFillAssignment{
				FieldID: fillFieldID(6), Value: FormValue{Type: FormValueChoices, Choices: []string{"one", "one"}},
			}),
			code: FailureChoiceInvalid,
		},
		{
			name: "single list multiple choices",
			fill: validFillMap(FormFillAssignment{
				FieldID: fillFieldID(7), Value: FormValue{Type: FormValueChoices, Choices: []string{"one", "three"}},
			}),
			code: FailureChoiceInvalid,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := facts
			candidate.Fields = append([]FormField(nil), facts.Fields...)
			if test.edit != nil {
				test.edit(&candidate)
			}
			_, err := NormalizeFillMap(input, candidate, test.fill)
			if FillRequestFailureCode(err) != test.code || err == nil {
				t.Fatalf("error = %v (%q), want %q", err, FillRequestFailureCode(err), test.code)
			}
			for _, secret := range []string{"one\ntwo", "2026-09-14", strings.Repeat("x", 21)} {
				if !strings.Contains(err.Error(), secret) {
					continue
				}
				t.Fatalf("error leaked field value: %q", err)
			}
		})
	}
}

func TestNormalizeFillMapEnforcesAggregateBound(t *testing.T) {
	input, facts := fillNormalizationFixture()
	base := facts.Fields[1]
	facts.Fields = nil
	fill := FillMap{SchemaVersion: FillMapSchemaVersion}
	for index := 1; index <= 8; index++ {
		field := base
		field.ID = fillFieldID(index)
		field.Widgets = []FormFieldWidget{{ID: fillWidgetID(index), Page: 1, Ordinal: 1}}
		facts.Fields = append(facts.Fields, field)
		fill.Assignments = append(fill.Assignments, fillTextAssignment(index, strings.Repeat("s", 7*1024)))
	}
	_, err := NormalizeFillMap(input, facts, fill)
	if FillRequestFailureCode(err) != FailureLimitExceeded {
		t.Fatalf("error = %v (%q), want %q", err, FillRequestFailureCode(err), FailureLimitExceeded)
	}
}

func TestDecodeFillMapIsBoundedAndStrict(t *testing.T) {
	valid := `{"schema_version":"mintclaw.document_fill_map.v1","assignments":[]}`
	if fill, err := DecodeFillMap(strings.NewReader(valid)); err != nil || fill.SchemaVersion != FillMapSchemaVersion {
		t.Fatalf("fill = %#v, err=%v", fill, err)
	}
	for _, invalid := range []string{
		valid + `{}`,
		`{"schema_version":"mintclaw.document_fill_map.v1","assignments":[],"unknown":true}`,
	} {
		if _, err := DecodeFillMap(strings.NewReader(invalid)); FillRequestFailureCode(err) != FailureInvalidInput {
			t.Fatalf("decode error = %v (%q)", err, FillRequestFailureCode(err))
		}
	}
	if _, err := DecodeFillMap(
		strings.NewReader(strings.Repeat("x", DefaultMaxFillRequestBytes+1)),
	); FillRequestFailureCode(
		err,
	) != FailureLimitExceeded {
		t.Fatalf("bounded decode error = %v (%q)", err, FillRequestFailureCode(err))
	}
}

func TestNormalizedFillRequestJSONContainsNoBackendOrHostIdentity(t *testing.T) {
	input, facts := fillNormalizationFixture()
	normalized, err := NormalizeFillMap(input, facts, validFillMap(fillTextAssignment(1, "private value")))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"/home/operator", "backend_id", "object_number", "full_name"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("normalized request leaked %q: %s", forbidden, encoded)
		}
	}
}

func fillNormalizationFixture() (DocumentRef, FormFieldsFacts) {
	field := func(index, page int, kind FormFieldKind) FormField {
		return FormField{
			ID: fillFieldID(index), Kind: kind,
			Widgets: []FormFieldWidget{{ID: fillWidgetID(index), Page: page, Ordinal: 1}},
		}
	}
	fields := []FormField{
		field(1, 1, FormFieldText),
		field(2, 1, FormFieldText),
		field(3, 1, FormFieldCheckbox),
		field(4, 1, FormFieldRadio),
		field(5, 1, FormFieldCombo),
		field(6, 1, FormFieldList),
		field(7, 1, FormFieldList),
		field(8, 1, FormFieldDate),
	}
	fields[0].Required = true
	fields[0].MaxLength = 20
	fields[0].Widgets = append(fields[0].Widgets, FormFieldWidget{ID: fillWidgetID(9), Page: 2, Ordinal: 2})
	fields[1].Multiline = true
	fields[3].Options = []FormFieldOption{
		{Export: "US", Display: "United States"},
		{Export: " EU ", Display: " Europe "},
	}
	fields[4].Options = []FormFieldOption{{Export: "CA", Display: "Canada"}}
	fields[5].MultiSelect = true
	fields[5].Options = []FormFieldOption{{Export: "one", Display: "one"}, {Export: "three", Display: "three"}}
	fields[6].Options = append([]FormFieldOption(nil), fields[5].Options...)
	fields[7].DateFormat = "mm/dd/yyyy"
	fields[7].MaxLength = 10
	return DocumentRef{SHA256: strings.Repeat("a", 64)}, FormFieldsFacts{
		Backend: BackendIdentity{
			Name: PDFCPUBackendName, Version: PDFCPUBackendVersion, Role: "production",
			IsolationMode: "one_shot_process",
		},
		Limits: defaultFormFieldLimits(),
		Fields: fields,
	}
}

func validFillMap(assignment FormFillAssignment) FillMap {
	return FillMap{SchemaVersion: FillMapSchemaVersion, Assignments: []FormFillAssignment{assignment}}
}

func fillTextAssignment(index int, value string) FormFillAssignment {
	return FormFillAssignment{FieldID: fillFieldID(index), Value: textFormValue(value)}
}

func fillBooleanAssignment(index int, value bool) FormFillAssignment {
	return FormFillAssignment{
		FieldID: fillFieldID(index), Value: FormValue{Type: FormValueBoolean, Checked: &value},
	}
}

func textFormValue(value string) FormValue {
	return FormValue{Type: FormValueText, Text: &value}
}

func fillFieldID(index int) string {
	return fmt.Sprintf("field_%064x", index)
}

func fillWidgetID(index int) string {
	return fmt.Sprintf("widget_%064x", index)
}
