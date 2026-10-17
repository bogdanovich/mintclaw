//go:build (linux && amd64) || (darwin && (amd64 || arm64)) || (windows && amd64)

package document

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/form"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/primitives"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func TestPortableFormTextDateValuesKeepTheirStructuralKindAndRemainEditable(t *testing.T) {
	for _, value := range []string{"2026-01-02", "01/02/2026"} {
		t.Run(value, func(t *testing.T) {
			data, input, fields := portableFormWriteFixture(t, "acroform-fields.pdf")
			backend := pdfCPUFormWriteBackend{policy: formWritePolicy{
				writerIdentity: pdfcpuIdentity(), standardVerifier: verifyPDFiumFormCandidate,
			}}
			for _, replacement := range []string{value, "Ordinary text again"} {
				fill := portableNormalizedNamedFill(t, input, fields, map[string]FormValue{
					"full_name": {Type: FormValueText, Text: &replacement},
				})
				request := newWorkerOperationRequest(input, defaultInspectionLimits(), workerOperationFillCandidate)
				request.OperationID = writeTestOperationID("schema_value_stability")
				request.Fill = &fill
				result := backend.Fill(data, request)
				if result.State != StateSucceeded || result.Failure != nil || len(result.Candidate) == 0 {
					t.Fatalf("plain text fill state=%q failure=%+v", result.State, result.Failure)
				}
				digest := sha256.Sum256(result.Candidate)
				nextSHA := hex.EncodeToString(digest[:])
				discovered := newFormFieldsBackend().Fields(
					bytes.NewReader(result.Candidate), defaultInspectionLimits(), nextSHA,
				)
				if discovered.State != StateSucceeded || discovered.Facts == nil {
					t.Fatal("filled plain text form could not be rediscovered")
				}
				foundText, foundDate := false, false
				for _, field := range discovered.Facts.Fields {
					if field.Name == "full_name" {
						foundText = field.Kind == FormFieldText && field.DateFormat == ""
					}
					if field.Name == "start_date" {
						foundDate = field.Kind == FormFieldDate && field.DateFormat != ""
					}
				}
				if !foundText || !foundDate {
					t.Fatal("a value changed text semantics or removed an explicit date formatter")
				}
				data, fields = result.Candidate, *discovered.Facts
				input.SHA256, input.Size = nextSHA, int64(len(data))
			}
		})
	}
}

func TestPDFCPUDateLookingTextDefaultsPreserveFlagsAndConstraints(t *testing.T) {
	for _, key := range []string{"V", "DV"} {
		t.Run(key, func(t *testing.T) {
			context := formFieldsFixtureContext(t)
			group, present, err := form.ExportForm(context.XRefTable, "")
			if err != nil || !present || len(group.Forms) != 1 {
				t.Fatal("fixture form unavailable")
			}
			for _, item := range group.Forms[0].TextFields {
				if item.Name != "full_name" {
					continue
				}
				field, fieldErr := pdfCPUFieldDict(context, item.ID)
				if fieldErr != nil {
					t.Fatal(fieldErr)
				}
				field[key] = types.StringLiteral("2026-01-02")
				field["Ff"] = types.Integer(primitives.FieldRequired | primitives.FieldMultiline)
				field["MaxLen"] = types.Integer(30)
			}
			group, present, err = form.ExportForm(context.XRefTable, "")
			if err != nil || !present || len(group.Forms) != 1 {
				t.Fatal("mutated fixture unavailable")
			}
			digest := sha256.Sum256([]byte("structural-default-fixture"))
			facts, failure := normalizePDFCPUFields(context, group.Forms[0], hex.EncodeToString(digest[:]))
			if failure != nil || facts == nil {
				t.Fatal("date-looking plain text was refused")
			}
			for _, field := range facts.Fields {
				if field.Name == "full_name" {
					if field.Kind != FormFieldText || field.DateFormat != "" ||
						!field.Required || !field.Multiline || field.MaxLength != 30 ||
						(key == "DV" && !field.HasDefault) {
						t.Fatal("normalization changed structural text metadata")
					}
					return
				}
			}
			t.Fatal("plain text field disappeared")
		})
	}
}

func TestPDFCPUDateLookingTextDoesNotAdmitFieldActions(t *testing.T) {
	for _, key := range []string{"A", "AA"} {
		t.Run(key, func(t *testing.T) {
			context := formFieldsFixtureContext(t)
			group, present, err := form.ExportForm(context.XRefTable, "")
			if err != nil || !present || len(group.Forms) != 1 {
				t.Fatal("fixture unavailable")
			}
			var target types.Dict
			for _, item := range group.Forms[0].TextFields {
				if item.Name == "full_name" {
					target, err = pdfCPUFieldDict(context, item.ID)
					if err != nil {
						t.Fatal(err)
					}
					target["V"] = types.StringLiteral("2026-01-02")
				}
			}
			if target == nil {
				t.Fatal("text field unavailable")
			}
			group, present, err = form.ExportForm(context.XRefTable, "")
			if err != nil || !present || len(group.Forms) != 1 {
				t.Fatal("date-looking text unavailable")
			}
			action := types.Dict{"S": types.Name("JavaScript"), "JS": types.StringLiteral("app.alert(1)")}
			if key == "A" {
				target[key] = action
			} else {
				target[key] = types.Dict{"K": action}
			}
			digest := sha256.Sum256([]byte("action-refusal-fixture"))
			facts, failure := normalizePDFCPUFields(context, group.Forms[0], hex.EncodeToString(digest[:]))
			if failure == nil || facts != nil {
				t.Fatal("value-based normalization admitted an untrusted field action")
			}
		})
	}
}
