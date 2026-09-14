//go:build linux && amd64

package document

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestPDFCPUFormFieldsBackendMatchesManifest(t *testing.T) {
	manifest := loadFormFieldsFixtureManifest(t)
	inspection := newInspectionBackend()
	backend := newFormFieldsBackend()
	for _, fixture := range manifest.Fixtures {
		t.Run(fixture.ID, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", fixture.File))
			if err != nil {
				t.Fatal(err)
			}
			request := testWorkerRequest(data)
			request.Operation = workerOperationFields
			result := serveWorkerFields(request, data, inspection, backend)
			if result.State != fixture.Expected.State {
				t.Fatalf("fields result = %#v", result)
			}
			if fixture.Expected.FailureCode != "" {
				if result.Failure == nil || result.Failure.Code != fixture.Expected.FailureCode {
					t.Fatalf("fields failure = %#v, want %q", result.Failure, fixture.Expected.FailureCode)
				}
				return
			}
			if result.Failure != nil || result.Fields == nil || !validFormFieldsFacts(*result.Fields) {
				t.Fatalf("fields result = %#v", result)
			}
			if len(result.Fields.Fields) != fixture.Expected.FieldCount {
				t.Fatalf("field count = %d, want %d", len(result.Fields.Fields), fixture.Expected.FieldCount)
			}
			widgetCount := 0
			kindSet := map[string]struct{}{}
			for _, field := range result.Fields.Fields {
				widgetCount += len(field.Widgets)
				kindSet[string(field.Kind)] = struct{}{}
			}
			kinds := make([]string, 0, len(kindSet))
			for kind := range kindSet {
				kinds = append(kinds, kind)
			}
			sort.Strings(kinds)
			if widgetCount != fixture.Expected.WidgetCount || !equalStrings(kinds, fixture.Expected.Kinds) {
				t.Fatalf(
					"widgets = %d kinds = %v, want %d %v",
					widgetCount,
					kinds,
					fixture.Expected.WidgetCount,
					fixture.Expected.Kinds,
				)
			}
		})
	}
}

func TestPDFCPUFormFieldIDsBindSourceAndHideBackendObjects(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "acroform-fields.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	backend := newFormFieldsBackend()
	first := backend.Fields(bytes.NewReader(data), defaultInspectionLimits(), stringsOf('a', 64))
	second := backend.Fields(bytes.NewReader(data), defaultInspectionLimits(), stringsOf('b', 64))
	if first.State != StateSucceeded || second.State != StateSucceeded ||
		first.Facts == nil || second.Facts == nil || len(first.Facts.Fields) != len(second.Facts.Fields) {
		t.Fatalf("first = %#v second = %#v", first, second)
	}
	for index := range first.Facts.Fields {
		if first.Facts.Fields[index].ID == second.Facts.Fields[index].ID ||
			first.Facts.Fields[index].ID == "9" || first.Facts.Fields[index].ID == "18" {
			t.Fatalf("field identity did not bind source or exposed backend identity: %#v", first.Facts.Fields[index])
		}
	}
}

func stringsOf(character byte, count int) string {
	return string(bytes.Repeat([]byte{character}, count))
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
