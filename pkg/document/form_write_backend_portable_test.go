//go:build (linux && amd64) || (darwin && (amd64 || arm64))

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
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

func TestPortablePDFCPUFormWriteBackendUsesPDFiumVisibleReadback(t *testing.T) {
	data, input, fields := portableFormWriteFixture(t, "acroform-fields.pdf")
	name := "MINTCLAW_PDFI1_PRIVATE_71c4"
	fill := portableNormalizedNamedFill(t, input, fields, map[string]FormValue{
		"full_name": {Type: FormValueText, Text: &name},
	})
	request := newWorkerOperationRequest(input, defaultInspectionLimits(), workerOperationFillCandidate)
	request.OperationID = writeTestOperationID("portable_pdfium")
	request.Fill = &fill

	backend := pdfCPUFormWriteBackend{policy: formWritePolicy{
		writerIdentity:   pdfcpuIdentity(),
		standardVerifier: verifyPDFiumFormCandidate,
	}}
	result := backend.Fill(data, request)
	if result.State != StateSucceeded || result.Failure != nil || result.Facts == nil ||
		len(result.Artifacts) != 1 || len(result.Candidate) == 0 {
		t.Fatalf("portable form write state=%q failure=%+v facts=%+v", result.State, result.Failure, result.Facts)
	}
	if result.Facts.Backend != pdfcpuIdentity() || result.Facts.VisualBackend != pdfiumWASMIdentity() ||
		result.Facts.IndependentVisualBackend != (BackendIdentity{}) ||
		result.Facts.VisualAssertions != 2 || result.Facts.RenderedPages != 1 {
		t.Fatalf("portable form facts = %#v", result.Facts)
	}
	legacy := *result.Facts
	legacy.Backend = pdfcpuIdentityWithIsolation(NativeBackendIsolationMode)
	legacy.VisualBackend = popplerIdentity()
	if !validFormWriteFactEnvelope(legacy) ||
		validFormWriteBackendsForSet(declaredBackendSet("darwin", "amd64"), legacy) {
		t.Fatalf("legacy form facts compatibility = %#v", legacy)
	}
}

func TestPortablePDFCPUFormWriteBackendFillsSupportedMatrix(t *testing.T) {
	data, input, fields := portableFormWriteFixture(t, "acroform-fields.pdf")
	name := "MINTCLAW_PDFI1_PRIVATE_71c4"
	notes := "first line\nsecond line"
	date := "09/18/2026"
	repeated := "same on both pages"
	checked := false
	fill := portableNormalizedNamedFill(t, input, fields, map[string]FormValue{
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
	request.OperationID = writeTestOperationID("portable_supported_matrix")
	request.Fill = &fill
	backend := pdfCPUFormWriteBackend{policy: formWritePolicy{
		writerIdentity:   pdfcpuIdentity(),
		standardVerifier: verifyPDFiumFormCandidate,
	}}

	result := backend.Fill(data, request)
	if result.State != StateSucceeded || result.Failure != nil || result.Facts == nil {
		t.Fatalf("portable matrix state=%q failure=%+v", result.State, result.Failure)
	}
	if result.Facts.CheckedFields != 8 || result.Facts.CheckedWidgets != 10 ||
		result.Facts.AppearanceWidgets != 10 || result.Facts.VisualAssertions != 12 ||
		result.Facts.RenderedPages != 2 {
		t.Fatalf("portable matrix facts = %#v", result.Facts)
	}
}

func TestPortablePDFCPUFormWriteBackendRefusesHybridForm(t *testing.T) {
	data, input, fields := portableFormWriteFixture(t, "hybrid-xfa-packet-array.pdf")
	name := "Portable Hybrid"
	fill := portableNormalizedNamedFill(t, input, fields, map[string]FormValue{
		"hybrid-name": {Type: FormValueText, Text: &name},
	})
	request := newWorkerOperationRequest(input, defaultInspectionLimits(), workerOperationFillCandidate)
	request.OperationID = writeTestOperationID("portable_hybrid_refusal")
	request.Fill = &fill
	backend := pdfCPUFormWriteBackend{policy: formWritePolicy{
		writerIdentity:   pdfcpuIdentity(),
		standardVerifier: verifyPDFiumFormCandidate,
	}}

	result := backend.Fill(data, request)
	if result.State != StateUnsupported || result.Failure == nil ||
		result.Failure.Code != FailureFormUnsupported || result.Facts != nil ||
		len(result.Artifacts) != 0 || len(result.Candidate) != 0 || strings.Contains(result.Failure.Message, name) {
		t.Fatalf("portable hybrid refusal = %#v", result)
	}
}

func TestPDFiumFormVerificationRejectsStaleAndClippedCandidate(t *testing.T) {
	data, input, fields := portableFormWriteFixture(t, "acroform-fields.pdf")
	name := "Visible User"
	fill := portableNormalizedNamedFill(t, input, fields, map[string]FormValue{
		"full_name": {Type: FormValueText, Text: &name},
	})
	request := newWorkerOperationRequest(input, defaultInspectionLimits(), workerOperationFillCandidate)
	request.OperationID = writeTestOperationID("portable_visual_refusal")
	request.Fill = &fill
	backend := pdfCPUFormWriteBackend{policy: formWritePolicy{
		writerIdentity: pdfcpuIdentity(),
		standardVerifier: func(
			_ []byte,
			request WorkerRequest,
			_ *model.Context,
			_ form.Form,
			bindings map[string]pdfCPUFormBinding,
		) (*formVisualEvidence, *Failure) {
			return &formVisualEvidence{
				Assertions:    len(request.Fill.AffectedPages) + len(bindings),
				RenderedPages: len(request.Fill.AffectedPages),
			}, nil
		},
	}}
	result := backend.Fill(data, request)
	if result.State != StateSucceeded || len(result.Candidate) == 0 {
		t.Fatalf("candidate state=%q failure=%+v", result.State, result.Failure)
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
				evidence, candidateFailure := verifyPDFiumFormCandidate(
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

func TestPDFiumFormVerificationRejectsStaleListSelection(t *testing.T) {
	data, input, fields := portableFormWriteFixture(t, "acroform-fields.pdf")
	fill := portableNormalizedNamedFill(t, input, fields, map[string]FormValue{
		"tags": {Type: FormValueChoices, Choices: []string{"two"}},
	})
	request := newWorkerOperationRequest(input, defaultInspectionLimits(), workerOperationFillCandidate)
	request.OperationID = writeTestOperationID("portable_stale_list")
	request.Fill = &fill
	backend := pdfCPUFormWriteBackend{policy: formWritePolicy{
		writerIdentity: pdfcpuIdentity(),
		standardVerifier: func(
			_ []byte,
			request WorkerRequest,
			_ *model.Context,
			_ form.Form,
			bindings map[string]pdfCPUFormBinding,
		) (*formVisualEvidence, *Failure) {
			return &formVisualEvidence{
				Assertions:    len(request.Fill.AffectedPages) + len(bindings),
				RenderedPages: len(request.Fill.AffectedPages),
			}, nil
		},
	}}
	result := backend.Fill(data, request)
	if result.State != StateSucceeded || len(result.Candidate) == 0 {
		t.Fatalf("candidate state=%q failure=%+v", result.State, result.Failure)
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
		bindings = map[string]pdfCPUFormBinding{id: binding}
	}
	evidence, candidateFailure := verifyPDFiumFormCandidate(
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

func portableFormWriteFixture(t *testing.T, filename string) ([]byte, DocumentRef, FormFieldsFacts) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", filename))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	input := DocumentRef{
		Ref:         "document://local/portable_form_write_fixture",
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

func portableNormalizedNamedFill(
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
