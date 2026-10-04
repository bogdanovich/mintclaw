package tools

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/document"
)

func TestDocumentFormPlanningWindowsReachEveryFieldWithoutDuplicates(t *testing.T) {
	schema := document.FormFieldsFacts{}
	for index := range 250 {
		kind := document.FormFieldText
		if index == 249 {
			kind = document.FormFieldCheckbox
		}
		schema.Fields = append(schema.Fields, document.FormField{
			ID: fmt.Sprintf("field_%03d", index), Name: strings.Repeat("x", 300), Kind: kind,
			Widgets: []document.FormFieldWidget{{Page: index/25 + 1, Ordinal: 1}},
		})
	}
	summary := documentFormDiscoverySummary(schema)
	seen := make(map[string]bool)
	for offset := 0; ; {
		view := documentFormMappingWindow(summary, schema, "", documentFormWindowSelection{
			offset: offset, explicit: true,
		})
		if len(view.CandidateFields) > documentFormCandidateLimit || view.Window.Offset != offset ||
			view.Window.Total != 250 || view.MissingFieldCount != 250 {
			t.Fatalf("invalid bounded window at %d: %#v", offset, view)
		}
		for _, field := range view.CandidateFields {
			if seen[field.FieldID] || !field.LabelTruncated || len(field.Label) > 256 || field.Status != "missing" {
				t.Fatalf("invalid or duplicate field at %d: %#v", offset, field)
			}
			seen[field.FieldID] = true
		}
		encoded, err := json.Marshal(view)
		if err != nil || len(encoded) > 8*1024 {
			t.Fatalf("window exceeds budget: %d bytes, %v", len(encoded), err)
		}
		if view.Window.NextOffset == nil {
			break
		}
		if *view.Window.NextOffset <= offset || !view.Window.Truncated {
			t.Fatalf("window did not make progress: %#v", view.Window)
		}
		offset = *view.Window.NextOffset
	}
	if len(seen) != 250 {
		t.Fatalf("reachable fields = %d, want 250", len(seen))
	}
}

func TestDocumentFormPlanningPageViewIncludesRepeatedWidgetsAndPreservedFields(t *testing.T) {
	schema := document.FormFieldsFacts{Fields: []document.FormField{
		{
			ID: "field_1", Name: "First fact", Kind: document.FormFieldText, HasValue: true,
			Widgets: []document.FormFieldWidget{{Page: 1}, {Page: 3}},
		},
		{
			ID: "field_2", Name: "Second fact", Kind: document.FormFieldText,
			Widgets: []document.FormFieldWidget{{Page: 2}},
		},
	}}
	summary := documentFormDiscoverySummary(schema)
	view := documentFormMappingWindow(summary, schema, "", documentFormWindowSelection{pages: []int{3}, explicit: true})
	if len(view.CandidateFields) != 1 || view.CandidateFields[0].FieldID != "field_1" ||
		view.CandidateFields[0].Status != "preserved" || view.CandidateFields[0].Page != 3 ||
		view.Window.Total != 1 || view.Window.Truncated ||
		view.PreservedFieldCount != 1 || view.MissingFieldCount != 1 || !slices.Equal(view.Window.Pages, []int{3}) {
		t.Fatalf("page view = %#v", view)
	}

	schema.Fields[1].HasValue = true
	summary = documentFormDiscoverySummary(schema)
	defaultView := documentFormMappingProjection(summary, schema)
	if !defaultView.ReadyForReview || len(defaultView.CandidateFields) != 0 || !defaultView.Window.Truncated {
		t.Fatalf("ready default view = %#v", defaultView)
	}
	correctionView := documentFormMappingWindow(summary, schema, "", documentFormWindowSelection{explicit: true})
	if len(correctionView.CandidateFields) != 2 || correctionView.PreservedFieldCount != 2 {
		t.Fatalf("ready correction view = %#v", correctionView)
	}
}

func TestDocumentFormPlanningFollowupPreservesSourceSchemaAndSelectedWindow(t *testing.T) {
	source := "media://00000000-0000-0000-0000-000000000123"
	digest := strings.Repeat("a", 64)
	projection := safeDocumentFormResult{
		SourceRef: source, FieldSchemaDigest: digest,
		Mapping: &safeDocumentFormMapping{
			Window:          safeDocumentFormWindow{Pages: []int{3}, Offset: 8, Total: 20},
			CandidateFields: []safeDocumentFormField{{FieldID: "field_test", Blocker: "field_unresolved"}},
		},
	}
	followup, err := documentFormDiscoverToolOnlyFollowup(projection)
	if err != nil || followup == nil {
		t.Fatalf("follow-up unavailable: %#v, %v", followup, err)
	}
	for _, args := range []map[string]any{
		{"action": "form", "form_action": "discover", "source": source, "pages": []int{4}},
		{"action": "form", "form_action": "discover", "source": source, "pages": []int{3}, "field_offset": 16},
		{
			"action": "form", "form_action": "start", "source": source, "field_schema_digest": digest,
			"pages": []int{3}, "field_offset": 8,
		},
	} {
		if err := followup.ValidateArguments(args); err != nil {
			t.Fatalf("valid transition rejected: %#v, %v", args, err)
		}
	}
	for _, args := range []map[string]any{
		{"action": "form", "form_action": "discover", "source": "media://other", "pages": []int{4}},
		{"action": "form", "form_action": "discover", "source": source},
		{"action": "form", "form_action": "start", "source": source, "field_schema_digest": digest},
		{
			"action": "form", "form_action": "start", "source": source, "field_schema_digest": strings.Repeat("b", 64),
			"pages": []int{3}, "field_offset": 8,
		},
	} {
		if err := followup.ValidateArguments(args); err == nil {
			t.Fatalf("invalid transition accepted: %#v", args)
		}
	}
}

func TestDocumentFormPlanningProjectsValueFreeProgress(t *testing.T) {
	schema := document.FormFieldsFacts{}
	summary := document.FormJobMappingSummary{
		WritableFieldCount: 5,
		ConfirmedFieldIDs:  []string{"preserved", "provided", "blank"},
		Unresolved: []document.FormFieldMappingBlocker{
			{FieldID: "missing", Code: "field_unresolved"},
			{FieldID: "conflict", Code: "field_conflicting"},
		},
		FieldProgress: []document.FormFieldProgress{
			{FieldID: "preserved", Status: "preserved"},
			{FieldID: "provided", Status: "confirmed"},
			{FieldID: "blank", Status: "optional_blank"},
			{FieldID: "missing", Status: "missing"},
			{FieldID: "conflict", Status: "field_conflicting"},
		},
	}
	for _, id := range []string{"preserved", "provided", "blank", "missing", "conflict"} {
		schema.Fields = append(schema.Fields, document.FormField{ID: id, Name: id, Kind: document.FormFieldText})
	}
	view := documentFormMappingProjectionExcludingConfirmed(summary, schema, "provided")
	if view.PreservedFieldCount != 1 || view.ProvidedFieldCount != 1 || view.OptionalBlankCount != 1 ||
		view.MissingFieldCount != 1 || view.ConflictingFieldCount != 1 || view.ConfirmedFieldCount != 3 {
		t.Fatalf("progress counts = %#v", view)
	}
	for _, field := range view.CandidateFields {
		if field.FieldID == "provided" || field.Status == "" {
			t.Fatalf("completed field repeated or status missing: %#v", field)
		}
	}
}

func TestDocumentFormPlanningLabelTruncationUsesCharacters(t *testing.T) {
	for _, count := range []int{200, 256, 300} {
		field := document.FormField{Name: strings.Repeat("\u00e9", count)}
		view := documentFormFieldProjection(field, "")
		if view.LabelTruncated != (count > 256) || view.Label != strings.Repeat("\u00e9", min(count, 256)) {
			t.Fatalf("label with %d characters = %#v", count, view)
		}
	}
}

func TestDocumentFormPlanningRejectsInvalidSelectors(t *testing.T) {
	for name, options := range map[string]map[string]any{
		"unsorted": {"pages": []int{3, 1}}, "duplicate": {"pages": []int{1, 1}},
		"empty": {"pages": []int{}}, "null": {"pages": nil}, "too many": {"pages": []int{1, 2, 3, 4}},
		"zero page": {"pages": []int{0}}, "unknown page": {"pages": []int{document.DefaultMaxPages + 1}},
		"negative offset": {"field_offset": -1}, "null offset": {"field_offset": nil},
		"fractional offset": {"field_offset": 1.5}, "unbounded offset": {"field_offset": document.DefaultMaxFormFields + 1},
	} {
		t.Run(name, func(t *testing.T) {
			args := map[string]any{"action": "form", "form_action": "discover", "source": "media://source"}
			for key, value := range options {
				args[key] = value
			}
			if err := validateDocumentActionOptions("form", args); err == nil {
				t.Fatalf("invalid selectors admitted: %#v", args)
			}
		})
	}
	if err := validateDocumentActionOptions("form", map[string]any{
		"action": "form", "form_action": "commit", "job_id": "job", "field_offset": 0,
	}); err == nil {
		t.Fatal("planning selectors reached mutation")
	}
}
