//go:build linux && amd64

package document

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestPDFCPUBackendMatchesInspectionManifest(t *testing.T) {
	manifest := loadInspectionFixtureManifest(t)
	backend := newInspectionBackend()
	for _, fixture := range manifest.Fixtures {
		t.Run(fixture.ID, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", fixture.File))
			if err != nil {
				t.Fatal(err)
			}
			result := backend.Inspect(bytes.NewReader(data), defaultInspectionLimits())
			if result.State != fixture.Expected.State {
				t.Fatalf("inspection = %#v", result)
			}
			if fixture.Expected.FailureCode != "" {
				if result.Failure == nil || result.Failure.Code != fixture.Expected.FailureCode {
					t.Fatalf("inspection failure = %#v, want %q", result.Failure, fixture.Expected.FailureCode)
				}
				if fixture.Expected.Encryption != "" || fixture.Expected.PasswordRequired != "" {
					if result.Facts == nil {
						t.Fatal("protected failure omitted partial inspection facts")
					}
					assertOptionalState(t, "encryption", result.Facts.Encryption.State, fixture.Expected.Encryption)
					assertOptionalState(
						t, "password required", result.Facts.Encryption.PasswordRequired,
						fixture.Expected.PasswordRequired,
					)
				}
				return
			}
			if result.Facts == nil {
				t.Fatal("successful inspection omitted facts")
			}
			facts := result.Facts
			if facts.Backend != (BackendIdentity{
				Name: PDFCPUBackendName, Version: PDFCPUBackendVersion, Role: "production",
			}) {
				t.Errorf("backend = %#v", facts.Backend)
			}
			if result.Failure != nil {
				t.Fatalf("successful inspection has failure: %#v", result.Failure)
			}
			assertStringFact(t, "PDF version", facts.PDFVersion, FactPresent, fixture.Expected.PDFVersion)
			assertIntegerFact(t, "page count", facts.PageCount, FactPresent, fixture.Expected.PageCount)
			if facts.ExtractableText.State != fixture.Expected.Text ||
				facts.AcroForm.State != fixture.Expected.AcroForm ||
				facts.XFA.State != fixture.Expected.XFA ||
				facts.Signatures.State != fixture.Expected.Signatures ||
				facts.Restrictions.State != fixture.Expected.Restrictions {
				t.Errorf("facts = %#v, expected = %#v", facts, fixture.Expected)
			}
			if fixture.Expected.FieldCount != nil {
				assertIntegerFact(
					t,
					"field count",
					facts.AcroForm.FieldCount,
					FactPresent,
					*fixture.Expected.FieldCount,
				)
			}
			if fixture.Expected.XFARepresentation != "" {
				assertStringFact(
					t, "XFA representation", facts.XFA.Representation, FactPresent,
					fixture.Expected.XFARepresentation,
				)
			}
			if fixture.Expected.XFARendering != "" {
				assertStringFact(
					t, "XFA rendering", facts.XFA.Rendering, FactPresent, fixture.Expected.XFARendering,
				)
			}
			if fixture.Expected.SignatureCount != nil {
				assertIntegerFact(
					t, "signature count", facts.Signatures.Count, FactPresent, *fixture.Expected.SignatureCount,
				)
			}
			assertOptionalState(t, "certified", facts.Signatures.Certified, fixture.Expected.Certified)
			assertOptionalState(t, "timestamped", facts.Signatures.Timestamped, fixture.Expected.Timestamped)
			assertOptionalState(t, "DocMDP", facts.Restrictions.DocMDP, fixture.Expected.DocMDP)
			assertOptionalState(t, "FieldMDP", facts.Restrictions.FieldMDP, fixture.Expected.FieldMDP)
			assertOptionalState(t, "usage rights", facts.Restrictions.UsageRights, fixture.Expected.UsageRights)
			if facts.Encryption.State != FactAbsent {
				t.Errorf("unexpected encryption facts = %#v", facts.Encryption)
			}
		})
	}
}

func assertOptionalState(t *testing.T, name string, actual, expected FactState) {
	t.Helper()
	if expected != "" && actual != expected {
		t.Errorf("%s = %q, want %q", name, actual, expected)
	}
}

func TestPDFCPUBackendReturnsTypedMalformedAndLimits(t *testing.T) {
	backend := newInspectionBackend()
	malformed := backend.Inspect(bytes.NewReader([]byte("%PDF-1.7\ntruncated")), defaultInspectionLimits())
	assertBackendFailure(t, malformed, StateFailed, FailureMalformedPDF)

	mixed, err := os.ReadFile(filepath.Join("testdata", "mixed-pages.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	pageLimits := defaultInspectionLimits()
	pageLimits.MaxPages = 1
	assertBackendFailure(
		t,
		backend.Inspect(bytes.NewReader(mixed), pageLimits),
		StateFailed,
		FailureInspectionLimit,
	)

	contentLimits := defaultInspectionLimits()
	contentLimits.MaxContentBytes = 1
	assertBackendFailure(
		t,
		backend.Inspect(bytes.NewReader(mixed), contentLimits),
		StateFailed,
		FailureInspectionLimit,
	)
}

func TestTextShowingOperatorScanner(t *testing.T) {
	tests := []struct {
		name string
		data string
		want bool
	}{
		{name: "Tj", data: "BT (MintClaw) Tj ET", want: true},
		{name: "TJ", data: "BT [(Mint) 20 (Claw)] TJ ET", want: true},
		{name: "single quote", data: "BT (MintClaw) ' ET", want: true},
		{name: "double quote", data: "BT 10 2 (MintClaw) \" ET", want: true},
		{name: "outside text object", data: "(MintClaw) Tj"},
		{name: "drawing only", data: "0 0 m 10 10 l s"},
		{name: "operator in comment", data: "% BT (MintClaw) Tj ET\n0 0 m"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := hasTextShowingOperator([]byte(test.data)); got != test.want {
				t.Fatalf("hasTextShowingOperator(%q) = %v, want %v", test.data, got, test.want)
			}
		})
	}
}

func assertStringFact(t *testing.T, name string, fact StringFact, state FactState, value string) {
	t.Helper()
	if fact.State != state || fact.Value != value {
		t.Errorf("%s = %#v, want state %q value %q", name, fact, state, value)
	}
}

func assertIntegerFact(t *testing.T, name string, fact IntegerFact, state FactState, value int) {
	t.Helper()
	if fact.State != state || fact.Value == nil || *fact.Value != value {
		t.Errorf("%s = %#v, want state %q value %d", name, fact, state, value)
	}
}

func assertBackendFailure(t *testing.T, result backendInspection, state State, code FailureCode) {
	t.Helper()
	if result.State != state || result.Facts != nil || result.Failure == nil || result.Failure.Code != code {
		t.Fatalf("inspection = %#v, want state %q code %q", result, state, code)
	}
}
