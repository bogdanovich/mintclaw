package eval

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestPDFOracleOutputBound(t *testing.T) {
	output := &boundedOracleOutput{}
	if n, err := output.Write(bytes.Repeat([]byte("x"), maxOracleOutput)); err != nil || n != maxOracleOutput {
		t.Fatal("bounded oracle output refused")
	}
	if n, err := output.Write([]byte("private-overflow")); !errors.Is(err, ErrPDFOracle) || n != 0 ||
		len(output.data) != maxOracleOutput || strings.Contains(string(output.data), "private-overflow") {
		t.Fatal("oversized oracle response retained")
	}
}

func TestPDFOracleRefusesUnresolvedFactsBeforeExec(t *testing.T) {
	pdf := *testPDFCase().PDF
	_, err := InspectPDFArtifact(t.Context(), "/unavailable/python", []byte("source"), []byte("artifact"),
		digest([]byte("source")), pdf)
	if !errors.Is(err, ErrPDFOracle) || strings.Contains(err.Error(), "synthetic-private") {
		t.Fatal("unresolved private handles or errors admitted")
	}
}

// Manual independent-oracle qualification is optional, never a paid/live CI job.
func TestPDFOracleManualQualification(t *testing.T) {
	python := os.Getenv("MINTCLAW_EVAL_PDF_ORACLE_PYTHON")
	if python == "" {
		t.Skip("manual pinned PDF oracle not admitted")
	}
	source, err := os.ReadFile("../../evals/fixtures/pdf/loan.pdf")
	if err != nil {
		t.Fatal(err)
	}
	pdf := PDFCase{Pages: 2, FieldCount: 5, Fields: []PDFField{
		{Name: "borrower_name", Kind: "text", Preserve: true},
		{Name: "contact_reference", Kind: "text", Preserve: true},
		{Name: "return_by_mail", Kind: "checkbox", Preserve: true},
		{Name: "delivery_note", Kind: "text", Preserve: true},
		{Name: "equipment", Kind: "text", Preserve: true},
	}}
	result, err := InspectPDFArtifact(t.Context(), python, source, source, digest(source), pdf)
	if err != nil || result.Code != "matched" || !validDigest(result.OracleSHA256) {
		t.Fatal("independent unchanged-form control did not qualify", err, result.Code)
	}
	pdf.Fields[1].Preserve = false
	pdf.Fields[1].Value = "MC_EVAL_ORACLE_PRIVATE_UNFILLED_8f17"
	result, err = InspectPDFArtifact(t.Context(), python, source, source, digest(source), pdf)
	if err != nil || result.Code != "mismatch" || result.FieldsMatched || result.AppearanceMatched {
		t.Fatal("claimed fill without an actual filled file passed", err, result.Code)
	}
}
