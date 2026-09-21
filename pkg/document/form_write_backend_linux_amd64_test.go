//go:build linux && amd64

package document

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/form"
)

func TestPDFCPUFormWriteBackendFillsSupportedMatrix(t *testing.T) {
	requirePinnedPopplerFormVisualBackend(t)
	data, input, fields := formWriteFixture(t, "acroform-fields.pdf")
	name := "MintClaw Updated"
	notes := "first line\nsecond line"
	date := "09/14/2026"
	repeated := "same on both pages"
	checked := false
	fill := normalizedNamedFill(t, input, fields, map[string]FormValue{
		"full_name":  {Type: FormValueText, Text: &name},
		"notes":      {Type: FormValueText, Text: &notes},
		"agree":      {Type: FormValueBoolean, Checked: &checked},
		"color":      {Type: FormValueChoice, Choices: []string{"Blue"}},
		"country":    {Type: FormValueChoice, Choices: []string{" EU "}},
		"tags":       {Type: FormValueChoices, Choices: []string{"two", "three"}},
		"start_date": {Type: FormValueText, Text: &date},
		"repeated":   {Type: FormValueText, Text: &repeated},
	})
	request := newWorkerOperationRequest(input, defaultInspectionLimits(), workerOperationFillCandidate)
	request.OperationID = writeTestOperationID("supported_matrix")
	request.Fill = &fill
	sourceDigest := sha256.Sum256(data)

	result := newFormWriteBackend().Fill(data, request)
	if result.State != StateSucceeded || result.Failure != nil || result.Facts == nil ||
		len(result.Artifacts) != 1 || len(result.Candidate) == 0 {
		t.Fatalf("write result state=%q failure=%+v facts=%+v", result.State, result.Failure, result.Facts)
	}
	if digest := sha256.Sum256(data); digest != sourceDigest {
		t.Fatal("form writer modified source bytes")
	}
	if result.Facts.CheckedFields != 8 || result.Facts.CheckedWidgets != 10 ||
		result.Facts.UnchangedFields != 0 || result.Facts.AppearanceWidgets != 10 ||
		result.Facts.VisualAssertions != 12 || result.Facts.RenderedPages != 2 ||
		!validPopplerIdentity(result.Facts.VisualBackend) ||
		!validFormWriteFacts(request, *result.Facts, result.Artifacts[0]) {
		t.Fatalf("write facts = %#v", result.Facts)
	}
	if bytes.Equal(result.Candidate, data) {
		t.Fatal("form writer returned unchanged source for changed values")
	}
	if matches, err := filepath.Glob(".form-visual-*"); err != nil || len(matches) != 0 {
		t.Fatalf("visual verification scratch survived: %v, %v", matches, err)
	}
	if output := os.Getenv("MINTCLAW_PDF2_WRITE_ORACLE_OUTPUT"); output != "" {
		if err := os.WriteFile(output, result.Candidate, 0o600); err != nil {
			t.Fatalf("write oracle candidate: %v", err)
		}
	}
}

func TestPDFCPUFormWriteBackendProducesVerifiedFlattenedHybridDerivative(t *testing.T) {
	requirePinnedHybridFormVisualBackends(t)
	data, input, fields := formWriteFixture(t, "hybrid-xfa-packet-array.pdf")
	name := "MintClaw Hybrid"
	fill := normalizedNamedFill(t, input, fields, map[string]FormValue{
		"hybrid-name": {Type: FormValueText, Text: &name},
	})
	request := newWorkerOperationRequest(input, defaultInspectionLimits(), workerOperationFillCandidate)
	request.OperationID = writeTestOperationID("hybrid_flattened_derivative")
	request.Fill = &fill
	sourceDigest := sha256.Sum256(data)

	result := newFormWriteBackend().Fill(data, request)
	if result.State != StateSucceeded || result.Failure != nil || result.Facts == nil ||
		len(result.Artifacts) != 1 || len(result.Candidate) == 0 {
		if result.Failure != nil {
			t.Fatalf(
				"hybrid write state=%q failure.code=%q failure.message=%q",
				result.State,
				result.Failure.Code,
				result.Failure.Message,
			)
		}
		t.Fatalf("hybrid write result = %#v", result)
	}
	if digest := sha256.Sum256(data); digest != sourceDigest {
		t.Fatal("hybrid writer modified source bytes")
	}
	if result.Facts.Output.Mode != FormOutputFlattenedPrint || result.Facts.Output.PageCount != 1 ||
		result.Facts.Output.AcroForm != FactAbsent || result.Facts.Output.XFA != FactAbsent ||
		result.Facts.Output.ContentSignatures != FactAbsent || result.Facts.Output.UsageRights != FactAbsent ||
		result.Facts.Output.Actions != FactAbsent || result.Facts.StructuralAssertions != hybridWriteStructuralAssertionCount ||
		result.Facts.RenderedPages != 1 || result.Facts.IndependentRenderedPages != 1 ||
		!validGhostscriptIdentity(result.Facts.IndependentVisualBackend) ||
		!validHybridNormalizations(result.Facts.Output.Normalizations) ||
		!validFormWriteFacts(request, *result.Facts, result.Artifacts[0]) {
		t.Fatalf("hybrid write facts = %#v", result.Facts)
	}
	inspection := newInspectionBackend().Inspect(bytes.NewReader(result.Candidate), defaultInspectionLimits())
	if inspection.State != StateSucceeded || inspection.Facts == nil ||
		inspection.Facts.AcroForm.State != FactAbsent || inspection.Facts.XFA.State != FactAbsent {
		t.Fatalf("flattened hybrid inspection = %#v", inspection)
	}
	fieldResult := newFormFieldsBackend().Fields(
		bytes.NewReader(result.Candidate), defaultInspectionLimits(), result.Facts.OutputSHA256,
	)
	if fieldResult.State != StateUnsupported || fieldResult.Failure == nil ||
		fieldResult.Failure.Code != FailureFormNotPresent {
		t.Fatalf("flattened hybrid fields result = %#v", fieldResult)
	}
}

func TestPopplerFormVerificationRejectsStaleAndClippedCandidate(t *testing.T) {
	requirePinnedPopplerFormVisualBackend(t)
	data, input, fields := formWriteFixture(t, "acroform-fields.pdf")
	name := "Visible User"
	fill := normalizedNamedFill(t, input, fields, map[string]FormValue{
		"full_name": {Type: FormValueText, Text: &name},
	})
	request := newWorkerOperationRequest(input, defaultInspectionLimits(), workerOperationFillCandidate)
	request.OperationID = writeTestOperationID("visual_refusal")
	request.Fill = &fill
	result := newFormWriteBackend().Fill(data, request)
	if result.State != StateSucceeded || len(result.Candidate) == 0 {
		t.Fatalf("candidate result = %#v", result)
	}
	context, failure := readFormContext(bytes.NewReader(result.Candidate), request.Limits)
	if failure != nil {
		t.Fatalf("candidate context failure = %#v", failure)
	}
	group, present, err := form.ExportForm(context.XRefTable, "")
	if err != nil || !present || group == nil || len(group.Forms) != 1 {
		t.Fatalf("candidate form: present=%v err=%v group=%#v", present, err, group)
	}
	_, _, bindings, failure := preparePDFCPUFormWrite(group.Forms[0], fields, fill)
	if failure != nil || len(bindings) != 1 {
		t.Fatalf("candidate bindings = %#v, failure=%#v", bindings, failure)
	}
	for id, binding := range bindings {
		for _, test := range []struct {
			name     string
			expected string
			code     FailureCode
		}{
			{name: "stale", expected: "Different User", code: FailureAppearanceStale},
			{name: "clipped", expected: "Visible User continued", code: FailureContentClipped},
		} {
			t.Run(test.name, func(t *testing.T) {
				candidateBindings := make(map[string]pdfCPUFormBinding, 1)
				changed := binding
				changed.expected.text = test.expected
				candidateBindings[id] = changed
				evidence, candidateFailure := verifyPopplerFormCandidate(
					result.Candidate,
					request,
					context,
					group.Forms[0],
					candidateBindings,
				)
				if evidence != nil || candidateFailure == nil || candidateFailure.Code != test.code ||
					strings.Contains(candidateFailure.Message, test.expected) {
					t.Fatalf("evidence=%#v failure=%#v", evidence, candidateFailure)
				}
			})
		}
	}
}

func TestPopplerFormVerificationRejectsStaleListSelection(t *testing.T) {
	requirePinnedPopplerFormVisualBackend(t)
	data, input, fields := formWriteFixture(t, "acroform-fields.pdf")
	fill := normalizedNamedFill(t, input, fields, map[string]FormValue{
		"tags": {Type: FormValueChoices, Choices: []string{"two"}},
	})
	request := newWorkerOperationRequest(input, defaultInspectionLimits(), workerOperationFillCandidate)
	request.OperationID = writeTestOperationID("stale_list_selection")
	request.Fill = &fill
	result := newFormWriteBackend().Fill(data, request)
	if result.State != StateSucceeded || len(result.Candidate) == 0 {
		t.Fatalf("candidate result = %#v", result)
	}
	context, failure := readFormContext(bytes.NewReader(result.Candidate), request.Limits)
	if failure != nil {
		t.Fatalf("candidate context failure = %#v", failure)
	}
	group, present, err := form.ExportForm(context.XRefTable, "")
	if err != nil || !present || group == nil || len(group.Forms) != 1 {
		t.Fatalf("candidate form: present=%v err=%v group=%#v", present, err, group)
	}
	_, _, bindings, failure := preparePDFCPUFormWrite(group.Forms[0], fields, fill)
	if failure != nil || len(bindings) != 1 {
		t.Fatalf("candidate bindings = %#v, failure=%#v", bindings, failure)
	}
	for id, binding := range bindings {
		binding.expected.choices = []string{"one"}
		bindings[id] = binding
	}
	evidence, candidateFailure := verifyPopplerFormCandidate(
		result.Candidate,
		request,
		context,
		group.Forms[0],
		bindings,
	)
	if evidence != nil || candidateFailure == nil || candidateFailure.Code != FailureAppearanceStale {
		t.Fatalf("evidence=%#v failure=%#v", evidence, candidateFailure)
	}
}

func TestPDFCPUFormWriteBackendPreservesUnassignedFields(t *testing.T) {
	requirePinnedPopplerFormVisualBackend(t)
	data, input, fields := formWriteFixture(t, "acroform-fields.pdf")
	name := "Only this field changes"
	fill := normalizedNamedFill(t, input, fields, map[string]FormValue{
		"full_name": {Type: FormValueText, Text: &name},
	})
	request := newWorkerOperationRequest(input, defaultInspectionLimits(), workerOperationFillCandidate)
	request.OperationID = writeTestOperationID("partial_fill")
	request.Fill = &fill

	result := newFormWriteBackend().Fill(data, request)
	if result.State != StateSucceeded || result.Facts == nil || result.Facts.CheckedFields != 1 ||
		result.Facts.CheckedWidgets != 1 || result.Facts.UnchangedFields != 7 {
		t.Fatalf("partial write result = %#v", result)
	}
}

func TestPDFCPUFormWriteBackendVerifiesAlreadyAssignedValues(t *testing.T) {
	requirePinnedPopplerFormVisualBackend(t)
	data, input, fields := formWriteFixture(t, "acroform-fields.pdf")
	name := "Existing User"
	checked := true
	fill := normalizedNamedFill(t, input, fields, map[string]FormValue{
		"full_name": {Type: FormValueText, Text: &name},
		"country":   {Type: FormValueChoice, Choices: []string{"US"}},
		"agree":     {Type: FormValueBoolean, Checked: &checked},
		"color":     {Type: FormValueChoice, Choices: []string{"Red"}},
	})
	request := newWorkerOperationRequest(input, defaultInspectionLimits(), workerOperationFillCandidate)
	request.OperationID = writeTestOperationID("already_assigned")
	request.Fill = &fill

	result := newFormWriteBackend().Fill(data, request)
	if result.State != StateSucceeded || result.Failure != nil || result.Facts == nil ||
		result.Facts.CheckedFields != 4 || result.Facts.AppearanceWidgets != 5 {
		t.Fatalf("already assigned result state=%q failure=%+v facts=%+v", result.State, result.Failure, result.Facts)
	}
}

func TestPDFCPUFormWriteBackendClearsOptionalChoiceValues(t *testing.T) {
	requirePinnedPopplerFormVisualBackend(t)
	data, input, fields := formWriteFixture(t, "acroform-fields.pdf")
	fill := normalizedNamedFill(t, input, fields, map[string]FormValue{
		"color":   {Type: FormValueChoice},
		"country": {Type: FormValueChoice},
		"tags":    {Type: FormValueChoices},
	})
	request := newWorkerOperationRequest(input, defaultInspectionLimits(), workerOperationFillCandidate)
	request.OperationID = writeTestOperationID("clear_optional_choices")
	request.Fill = &fill
	result := newFormWriteBackend().Fill(data, request)
	if result.State != StateSucceeded || result.Failure != nil || result.Facts == nil ||
		result.Facts.CheckedFields != 3 {
		t.Fatalf("choice clear result state=%q failure=%+v facts=%+v", result.State, result.Failure, result.Facts)
	}
}

func TestPDFCPUFormWriteBackendPreservesUnicodeValueAndAppearance(t *testing.T) {
	requirePinnedPopplerFormVisualBackend(t)
	data, input, fields := formWriteFixture(t, "acroform-fields.pdf")
	name := "Мария Résumé"
	fill := normalizedNamedFill(t, input, fields, map[string]FormValue{
		"full_name": {Type: FormValueText, Text: &name},
	})
	request := newWorkerOperationRequest(input, defaultInspectionLimits(), workerOperationFillCandidate)
	request.OperationID = writeTestOperationID("unicode_fill")
	request.Fill = &fill

	result := newFormWriteBackend().Fill(data, request)
	if result.State != StateSucceeded || result.Failure != nil || result.Facts == nil ||
		result.Facts.AppearanceWidgets != 1 {
		t.Fatalf("unicode write result state=%q failure=%+v facts=%+v", result.State, result.Failure, result.Facts)
	}
}

func TestPDFCPUFormWriteBackendRefusesUnavailableUnicodeGlyph(t *testing.T) {
	data, input, fields := formWriteFixture(t, "acroform-fields.pdf")
	name := "Мария 東京"
	fill := normalizedNamedFill(t, input, fields, map[string]FormValue{
		"full_name": {Type: FormValueText, Text: &name},
	})
	request := newWorkerOperationRequest(input, defaultInspectionLimits(), workerOperationFillCandidate)
	request.OperationID = writeTestOperationID("missing_unicode_glyph")
	request.Fill = &fill

	result := newFormWriteBackend().Fill(data, request)
	if result.State != StateUnsupported || result.Failure == nil ||
		result.Failure.Code != FailureAppearanceUnavailable || result.Facts != nil ||
		len(result.Artifacts) != 0 || len(result.Candidate) != 0 {
		t.Fatalf("missing glyph result = %#v", result)
	}
}

func TestPDFCPUFormWriteBackendSupportsEachFixtureField(t *testing.T) {
	requirePinnedPopplerFormVisualBackend(t)
	data, input, fields := formWriteFixture(t, "acroform-fields.pdf")
	text := "MintClaw"
	notes := "first line\nsecond line"
	date := "09/14/2026"
	checked := false
	values := map[string]FormValue{
		"full_name":  {Type: FormValueText, Text: &text},
		"notes":      {Type: FormValueText, Text: &notes},
		"agree":      {Type: FormValueBoolean, Checked: &checked},
		"color":      {Type: FormValueChoice, Choices: []string{"Blue"}},
		"country":    {Type: FormValueChoice, Choices: []string{"CA"}},
		"tags":       {Type: FormValueChoices, Choices: []string{"two"}},
		"start_date": {Type: FormValueText, Text: &date},
		"repeated":   {Type: FormValueText, Text: &text},
	}
	for name, value := range values {
		t.Run(name, func(t *testing.T) {
			fill := normalizedNamedFill(t, input, fields, map[string]FormValue{name: value})
			request := newWorkerOperationRequest(input, defaultInspectionLimits(), workerOperationFillCandidate)
			request.OperationID = writeTestOperationID("single_" + name)
			request.Fill = &fill
			result := newFormWriteBackend().Fill(data, request)
			if result.State != StateSucceeded || result.Failure != nil {
				t.Fatalf("write result state=%q failure=%+v", result.State, result.Failure)
			}
		})
	}
}

func requirePinnedPopplerFormVisualBackend(t *testing.T) {
	t.Helper()
	if !readBackendAvailable() {
		t.Skip("pinned Poppler 24.02.0 visual backend is unavailable")
	}
}

func requirePinnedHybridFormVisualBackends(t *testing.T) {
	t.Helper()
	if !readBackendAvailable() || !ghostscriptBackendAvailable() {
		t.Skip("pinned Poppler and Ghostscript visual backends are unavailable")
	}
}

func TestPDFCPUFormWriteBackendRefusesUnsafeClassesBeforeWriting(t *testing.T) {
	_, _, safeFields := formWriteFixture(t, "acroform-fields.pdf")
	for _, filename := range []string{
		"encrypted-password-required.pdf",
		"field-restricted.pdf",
		"rights-enabled.pdf",
		"signed-certified.pdf",
		"timestamped.pdf",
		"unsigned-signature.pdf",
		"hybrid-xfa-static.pdf",
		"xfa-dynamic.pdf",
		"truncated.pdf",
	} {
		t.Run(filename, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", filename))
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(data)
			input := DocumentRef{
				Ref:         "document://local/unsafe",
				ContentType: "application/pdf",
				Size:        int64(len(data)),
				SHA256:      hex.EncodeToString(digest[:]),
			}
			facts := safeFields
			facts.SourceSHA256 = input.SHA256
			var field FormField
			for _, candidate := range facts.Fields {
				if candidate.Name == "full_name" {
					field = candidate
					break
				}
			}
			if field.ID == "" {
				t.Fatal("full_name field is unavailable")
			}
			value := "must not be written"
			fill, err := NormalizeFillMap(input, facts, FillMap{
				SchemaVersion: FillMapSchemaVersion,
				Assignments: []FormFillAssignment{{
					FieldID: field.ID,
					Value:   FormValue{Type: FormValueText, Text: &value},
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			request := newWorkerOperationRequest(input, defaultInspectionLimits(), workerOperationFillCandidate)
			request.OperationID = writeTestOperationID(filename)
			request.Fill = &fill
			sourceDigest := sha256.Sum256(data)

			result := newFormWriteBackend().Fill(data, request)
			if result.State == StateSucceeded || result.Failure == nil || len(result.Candidate) != 0 ||
				len(result.Artifacts) != 0 || result.Facts != nil {
				t.Fatalf("unsafe write result = %#v", result)
			}
			if digestAfter := sha256.Sum256(data); digestAfter != sourceDigest {
				t.Fatal("refused form write modified source bytes")
			}
		})
	}
}

func formWriteFixture(t *testing.T, filename string) ([]byte, DocumentRef, FormFieldsFacts) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", filename))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	input := DocumentRef{
		Ref:         "document://local/form_write_fixture",
		ContentType: "application/pdf",
		Size:        int64(len(data)),
		SHA256:      hex.EncodeToString(digest[:]),
	}
	result := newFormFieldsBackend().Fields(bytes.NewReader(data), defaultInspectionLimits(), input.SHA256)
	if result.State != StateSucceeded || result.Facts == nil {
		t.Fatalf("fields result = %#v", result)
	}
	return data, input, *result.Facts
}

func normalizedNamedFill(
	t *testing.T,
	input DocumentRef,
	facts FormFieldsFacts,
	values map[string]FormValue,
) NormalizedFillRequest {
	t.Helper()
	byName := make(map[string]FormField, len(facts.Fields))
	for _, field := range facts.Fields {
		byName[field.Name] = field
	}
	assignments := make([]FormFillAssignment, 0, len(values))
	for name, value := range values {
		field, found := byName[name]
		if !found {
			t.Fatalf("field %q is unavailable", name)
		}
		assignments = append(assignments, FormFillAssignment{FieldID: field.ID, Value: value})
	}
	fill, err := NormalizeFillMap(input, facts, FillMap{
		SchemaVersion: FillMapSchemaVersion,
		Assignments:   assignments,
	})
	if err != nil {
		t.Fatal(err)
	}
	return fill
}
