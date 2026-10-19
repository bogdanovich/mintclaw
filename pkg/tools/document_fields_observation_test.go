package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/document"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestDocumentFieldsObservationBindsSuccessfulReadWithoutContent(t *testing.T) {
	schema := workflowTestSchema([]byte("private source bytes"))
	schema.Fields[0].Name = "private-field-name"
	schema.Fields[0].HasValue = true
	schema.Fields[0].HasDefault = true
	report := document.Report{
		SchemaVersion: document.ReportSchemaVersion, Operation: "fields", State: document.StateSucceeded,
		Input:  &document.DocumentRef{SHA256: schema.SourceSHA256, SourceRef: "media://private-source"},
		Fields: &schema,
	}
	result := documentToolReportResult(report)
	want := toolshared.DocumentFieldsObservation{
		Operation: "fields", SourceSHA256: schema.SourceSHA256,
		SchemaFields: 1, ReturnedFields: 1, RequiredFields: 1, ValuesPresent: 1, DefaultsPresent: 1,
	}
	if result.IsError || result.DocumentFields != want {
		t.Fatalf("field observation = %#v, error=%v", result.DocumentFields, result.IsError)
	}
	data, err := json.Marshal(result.DocumentFields)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"private source bytes", "private-field-name", "media://private-source"} {
		if strings.Contains(string(data), private) {
			t.Fatalf("field observation retained %q", private)
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil || strings.Contains(string(encoded), "DocumentFields") ||
		strings.Contains(string(encoded), "SchemaFields") {
		t.Fatalf("transient observation became canonical: %s, %v", encoded, err)
	}
	for _, test := range []struct {
		name string
		edit func(*document.Report)
	}{
		{"inspect", func(r *document.Report) { r.Operation = "inspect" }},
		{"failed", func(r *document.Report) { r.State = document.StateFailed }},
		{"failure", func(r *document.Report) { r.Failure = &document.Failure{Code: document.FailureInternal} }},
		{"no source", func(r *document.Report) { r.Input = nil }},
		{"wrong source", func(r *document.Report) { r.Input = &document.DocumentRef{SHA256: strings.Repeat("a", 64)} }},
		{"no fields", func(r *document.Report) { r.Fields = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := report
			test.edit(&invalid)
			if got := documentReportFieldsObservation(invalid); got != (toolshared.DocumentFieldsObservation{}) {
				t.Fatalf("invalid report produced observation: %#v", got)
			}
		})
	}
}

func TestDocumentFieldsObservationCountsSchemaSeparatelyFromWindow(t *testing.T) {
	schema := workflowTestSchema([]byte("source"))
	first := schema.Fields[0]
	schema.Fields = append(schema.Fields, first, first)
	schema.Fields[1].Required = false
	schema.Fields[1].HasDefault = true
	schema.Fields[2].HasValue = true
	got := documentFieldsObservation("form.discover", schema, 1)
	if got.SchemaFields != 3 || got.ReturnedFields != 1 || got.RequiredFields != 2 ||
		got.ValuesPresent != 1 || got.DefaultsPresent != 1 {
		t.Fatalf("schema/window observation = %#v", got)
	}
	for _, count := range []int{-1, 4} {
		if got := documentFieldsObservation(
			"form.discover",
			schema,
			count,
		); got != (toolshared.DocumentFieldsObservation{}) {
			t.Fatalf("invalid window produced observation: %#v", got)
		}
	}
}
