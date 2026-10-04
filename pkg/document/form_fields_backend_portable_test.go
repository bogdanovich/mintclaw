//go:build (linux && amd64) || (darwin && (amd64 || arm64)) || (windows && amd64)

package document

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/form"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func TestPDFCPUFieldHasValueDistinguishesEmptySerializedValues(t *testing.T) {
	for _, test := range []struct {
		name   string
		parent types.Dict
		child  types.Dict
		want   bool
	}{
		{name: "absent", child: types.Dict{}},
		{name: "default only", child: types.Dict{"DV": types.StringLiteral("default")}},
		{name: "empty literal", child: types.Dict{"V": types.StringLiteral("")}},
		{name: "empty hex", child: types.Dict{"V": types.HexLiteral("")}},
		{name: "empty unicode", child: types.Dict{"V": types.HexLiteral("FEFF")}},
		{name: "null", child: types.Dict{"V": nil}},
		{name: "empty list", child: types.Dict{"V": types.Array{}}},
		{name: "empty choices", child: types.Dict{"V": types.Array{types.StringLiteral(""), types.HexLiteral("")}}},
		{name: "selected choice", child: types.Dict{"V": types.Array{types.StringLiteral(""), types.StringLiteral("x")}}, want: true},
		{name: "text", child: types.Dict{"V": types.StringLiteral("source value")}, want: true},
		{name: "whitespace", child: types.Dict{"V": types.StringLiteral(" ")}, want: true},
		{name: "unchecked button", child: types.Dict{"V": types.Name("Off")}, want: true},
		{name: "checked button", child: types.Dict{"V": types.Name("Yes")}, want: true},
		{name: "inherited text", parent: types.Dict{"V": types.StringLiteral("source value")}, child: types.Dict{}, want: true},
		{name: "inherited empty", parent: types.Dict{"V": types.StringLiteral("")}, child: types.Dict{}},
		{name: "empty overrides parent", parent: types.Dict{"V": types.StringLiteral("source value")}, child: types.Dict{"V": types.StringLiteral("")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			context := formFieldsFixtureContext(t)
			childNumber, err := context.InsertObject(test.child)
			if err != nil {
				t.Fatal(err)
			}
			backendID := strconv.Itoa(childNumber)
			if test.parent != nil {
				parentNumber, insertErr := context.InsertObject(test.parent)
				if insertErr != nil {
					t.Fatal(insertErr)
				}
				backendID = strconv.Itoa(parentNumber) + "." + backendID
			}
			for _, indirect := range []bool{false, true} {
				if object, found := test.child.Find("V"); indirect && found && object != nil {
					number, insertErr := context.InsertObject(object)
					if insertErr != nil {
						t.Fatal(insertErr)
					}
					test.child["V"] = *types.NewIndirectRef(number, 0)
				}
				got, failure := fieldHasValue(context, backendID)
				if failure != nil || got != test.want {
					t.Fatalf("fieldHasValue indirect=%v = %v, failure=%#v, want %v", indirect, got, failure, test.want)
				}
			}
		})
	}
}

func TestPDFCPUFieldHasValueRejectsMalformedValues(t *testing.T) {
	for _, value := range []types.Object{
		types.Integer(42),
		types.Array{types.StringLiteral("source value"), types.Integer(42)},
	} {
		context := formFieldsFixtureContext(t)
		number, err := context.InsertObject(types.Dict{"V": value})
		if err != nil {
			t.Fatal(err)
		}
		present, failure := fieldHasValue(context, strconv.Itoa(number))
		if present || failure == nil || failure.Code != FailureMalformedPDF {
			t.Fatalf("malformed value accepted: present=%v failure=%#v", present, failure)
		}
	}
}

func TestPDFCPUEmptyRequiredValueRemainsMissingAfterRestart(t *testing.T) {
	context := formFieldsFixtureContext(t)
	group, present, err := form.ExportForm(context.XRefTable, "")
	if err != nil || !present || len(group.Forms) != 1 {
		t.Fatalf("export form present=%v err=%v", present, err)
	}
	var backendID string
	for _, field := range group.Forms[0].TextFields {
		if field.Name == "full_name" {
			backendID = field.ID
			break
		}
	}
	dictionary, err := pdfCPUFieldDict(context, backendID)
	if err != nil {
		t.Fatal(err)
	}
	dictionary["V"] = types.StringLiteral("")
	flags, failure := fieldFlags(context, backendID)
	if failure != nil {
		t.Fatal(failure)
	}
	dictionary["Ff"] = types.Integer(flags | 2)
	schema, failure := normalizePDFCPUFields(context, group.Forms[0], stringsOf('a', 64))
	if failure != nil {
		t.Fatal(failure)
	}
	var missingID string
	for _, field := range schema.Fields {
		if field.Name == "full_name" {
			if field.HasValue || !field.Required {
				t.Fatalf("empty required field = %#v", field)
			}
			missingID = field.ID
		}
	}
	store, options := newTestFormJobStore(t)
	owner := testFormJobOwner()
	previousSchema := *schema
	previousSchema.Fields = append([]FormField(nil), schema.Fields...)
	for index := range previousSchema.Fields {
		if previousSchema.Fields[index].ID == missingID {
			previousSchema.Fields[index].HasValue = true
		}
	}
	previousOwner := owner
	previousOwner.RouteSessionKey += "-previous"
	previousRecord := createMappedFormJob(t, store, previousOwner, previousSchema)
	if _, staleErr := store.FormMappingSummary(t.Context(), previousRecord.JobID, previousOwner, *schema); !errors.Is(
		staleErr,
		ErrFormJobStale,
	) {
		t.Fatalf("previous empty-value classification was silently rebound: %v", staleErr)
	}
	record := createMappedFormJob(t, store, owner, *schema)
	for _, restart := range []bool{false, true} {
		if restart {
			store.Close()
			store, err = OpenFormJobStore(options)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(store.Close)
		}
		summary, summaryErr := store.FormMappingSummary(t.Context(), record.JobID, owner, *schema)
		if summaryErr != nil || summary.ReadyForReview {
			t.Fatalf("empty required form is review-ready: %#v, %v", summary, summaryErr)
		}
		found := false
		for _, progress := range summary.FieldProgress {
			if progress.FieldID == missingID {
				found = progress.Status == "missing"
			}
		}
		if !found {
			t.Fatalf("empty required field not missing: %#v", summary.FieldProgress)
		}
	}
}

func formFieldsFixtureContext(t *testing.T) *model.Context {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "acroform-fields.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	context, failure := readFormContext(bytes.NewReader(data), defaultInspectionLimits())
	if failure != nil {
		t.Fatal(failure)
	}
	return context
}

func TestNormalizedOptionsDropsEmptyPlaceholder(t *testing.T) {
	options, failure := normalizedOptions([]string{"", "WA", "CA"}, defaultFormFieldLimits())
	if failure != nil {
		t.Fatalf("normalizedOptions failure = %#v", failure)
	}
	want := []FormFieldOption{{Export: "WA", Display: "WA"}, {Export: "CA", Display: "CA"}}
	if !reflect.DeepEqual(options, want) {
		t.Fatalf("normalizedOptions = %#v, want %#v", options, want)
	}
}

func TestPDFCPUFormFieldsBackendMatchesManifest(t *testing.T) {
	manifest := loadFormFieldsFixtureManifest(t)
	backends := declaredBackendSet(runtime.GOOS, runtime.GOARCH)
	backends.inspection = newInspectionBackend()
	backends.formFields = newFormFieldsBackend()
	for _, fixture := range manifest.Fixtures {
		t.Run(fixture.ID, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", fixture.File))
			if err != nil {
				t.Fatal(err)
			}
			request := testWorkerRequest(data)
			request.Operation = workerOperationFields
			result := serveWorkerFieldsWithBackendSet(request, data, backends)
			expectedState := fixture.Expected.State
			expectedFailure := fixture.Expected.FailureCode
			if runtime.GOOS == "darwin" && fixture.ID == "hybrid-xfa-discovery" {
				expectedState = StateUnsupported
				expectedFailure = FailureFormUnsupported
			}
			if result.State != expectedState {
				t.Fatalf("fields result = %#v", result)
			}
			if expectedFailure != "" {
				if result.Failure == nil || result.Failure.Code != expectedFailure {
					t.Fatalf("fields failure = %#v, want %q", result.Failure, expectedFailure)
				}
				return
			}
			if result.Failure != nil || result.Fields == nil ||
				!validFormFieldsFactsForSet(backends, *result.Fields) {
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

func TestNormalizedOptionsPreserveExactWhitespace(t *testing.T) {
	options, failure := normalizedOptions([]string{" exact export "}, defaultFormFieldLimits())
	if failure != nil || len(options) != 1 || options[0].Export != " exact export " ||
		options[0].Display != " exact export " {
		t.Fatalf("options = %#v, failure = %#v", options, failure)
	}
}

func TestPDFCPUFormFieldsBackendPreservesInheritedDateMaxLength(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "acroform-fields.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	result := newFormFieldsBackend().Fields(
		bytes.NewReader(data),
		defaultInspectionLimits(),
		stringsOf('a', 64),
	)
	if result.State != StateSucceeded || result.Facts == nil {
		t.Fatalf("fields result = %#v", result)
	}
	for _, field := range result.Facts.Fields {
		if field.Name == "start_date" {
			if field.Kind != FormFieldDate || field.MaxLength != 10 {
				t.Fatalf("date field = %#v", field)
			}
			return
		}
	}
	t.Fatal("start_date field not found")
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
