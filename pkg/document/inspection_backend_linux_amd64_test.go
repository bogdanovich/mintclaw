//go:build linux && amd64

package document

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	pdfcpuapi "github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/filter"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
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
			} else if facts.Encryption.OperationPermissions != (OperationPermissionFacts{
				Print: PermissionAllowed, FormFill: PermissionAllowed, Modify: PermissionAllowed,
				Assemble: PermissionAllowed,
			}) {
				t.Errorf("unencrypted operation permissions = %#v", facts.Encryption.OperationPermissions)
			}
			if fixture.Expected.UsageRights == FactPresent {
				if facts.Signatures.Content.State != FactAbsent ||
					facts.Signatures.UsageRights.State != FactPresent {
					t.Errorf("usage-rights signature classes = %#v", facts.Signatures)
				}
			} else if fixture.Expected.Signatures == FactPresent &&
				facts.Signatures.Content.State != FactPresent {
				t.Errorf("content signature class = %#v", facts.Signatures)
			}
			if !validInspectionFacts(*facts) {
				t.Fatalf("worker protocol rejected production inspection facts: %#v", facts)
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

func TestPDFCPUBackendClassifiesHybridAuthorityActionsAndUsageRights(t *testing.T) {
	backend := newInspectionBackend()
	tests := []struct {
		file  string
		check func(*testing.T, InspectionFacts)
	}{
		{file: "hybrid-xfa-packet-array.pdf", check: func(t *testing.T, facts InspectionFacts) {
			assertStringFact(
				t, "hybrid authority", facts.HybridForm.Authority,
				FactPresent, "acroform_fixed_pages",
			)
			if facts.HybridForm.XMLParsed != FactPresent || facts.HybridForm.NeedsRendering != FactAbsent ||
				facts.HybridForm.Scripts != FactPresent || facts.HybridForm.PageGrowth != FactAbsent ||
				facts.Actions.State != FactAbsent || facts.Actions.JavaScript != FactAbsent {
				t.Fatalf("static hybrid facts = %#v", facts.HybridForm)
			}
		}},
		{file: "hybrid-xfa-dynamic.pdf", check: func(t *testing.T, facts InspectionFacts) {
			assertStringFact(t, "hybrid authority", facts.HybridForm.Authority, FactPresent, "xfa_dynamic")
			if facts.HybridForm.PageGrowth != FactPresent || hybridFormDiscoveryEligible(facts) {
				t.Fatalf("dynamic hybrid facts = %#v", facts.HybridForm)
			}
		}},
		{file: "hybrid-xfa-page-growth.pdf", check: func(t *testing.T, facts InspectionFacts) {
			if facts.HybridForm.Authority.State != FactUnknown ||
				facts.HybridForm.RepeatingSubforms != FactPresent || facts.HybridForm.PageGrowth != FactPresent ||
				hybridFormDiscoveryEligible(facts) {
				t.Fatalf("page-growing hybrid facts = %#v", facts.HybridForm)
			}
		}},
		{file: "hybrid-xfa-malformed.pdf", check: func(t *testing.T, facts InspectionFacts) {
			if facts.HybridForm.Authority.State != FactUnknown || facts.HybridForm.XMLParsed != FactUnknown ||
				facts.HybridForm.Scripts != FactUnknown || facts.HybridForm.PageGrowth != FactUnknown ||
				facts.XFA.Rendering.State != FactUnknown ||
				hybridFormDiscoveryEligible(facts) ||
				!validInspectionFacts(facts) {
				t.Fatalf("malformed hybrid facts = %#v", facts.HybridForm)
			}
		}},
		{file: "calculated-field.pdf", check: func(t *testing.T, facts InspectionFacts) {
			if facts.Actions.JavaScript != FactPresent || facts.Actions.AdditionalActions != FactPresent ||
				facts.Actions.CalculationOrder != FactAbsent {
				t.Fatalf("calculated field actions = %#v", facts.Actions)
			}
		}},
		{file: "rights-enabled.pdf", check: func(t *testing.T, facts InspectionFacts) {
			if facts.Signatures.Content.State != FactAbsent ||
				facts.Signatures.UsageRights.State != FactPresent ||
				facts.Signatures.UsageRights.Count.Value == nil ||
				*facts.Signatures.UsageRights.Count.Value != 1 {
				t.Fatalf("usage-rights signature facts = %#v", facts.Signatures)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.file, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", test.file))
			if err != nil {
				t.Fatal(err)
			}
			result := backend.Inspect(bytes.NewReader(data), defaultInspectionLimits())
			if result.State != StateSucceeded || result.Facts == nil || result.Failure != nil {
				t.Fatalf("inspection = %#v", result)
			}
			test.check(t, *result.Facts)
		})
	}
}

func TestInspectionDecodesOnlyBoundedOperationPermissions(t *testing.T) {
	reference := types.NewIndirectRef(1, 0)
	context := &model.Context{XRefTable: &model.XRefTable{
		Encrypt: reference,
		E: &model.Enc{
			P: int(
				model.PermissionPrintRev2 |
					model.PermissionModAnnFillForm |
					model.PermissionFillRev3 |
					model.PermissionPrintRev3,
			),
		},
	}}
	facts := defaultInspectionFacts()
	inspectEncryption(context, facts)
	want := OperationPermissionFacts{
		Print: PermissionAllowed, FormFill: PermissionAllowed, Modify: PermissionDenied,
		Assemble: PermissionDenied,
	}
	if facts.Encryption.OperationPermissions != want ||
		facts.Encryption.Permissions != (StringFact{State: FactPresent, Value: "restricted"}) {
		t.Fatalf("encryption facts = %#v, want permissions %#v", facts.Encryption, want)
	}
}

func TestBoundedPageContentRejectsStreamAfterExactBudget(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "decoded-content-array-limit.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	configuration := model.NewDefaultConfiguration()
	configuration.ValidationMode = model.ValidationRelaxed
	context, err := pdfcpuapi.ReadAndValidate(bytes.NewReader(data), configuration)
	if err != nil {
		t.Fatal(err)
	}
	const firstStreamBytes = int64(5 * 1024 * 1024)
	if _, err = boundedPageContent(context, 1, firstStreamBytes); !errors.Is(err, filter.ErrDecodeLimitExceeded) {
		t.Fatalf("boundedPageContent error = %v, want decode limit", err)
	}
}

func TestTextShowingOperatorScanner(t *testing.T) {
	tests := []struct {
		name string
		data string
		want FactState
	}{
		{name: "Tj", data: "BT (MintClaw) Tj ET", want: FactPresent},
		{name: "TJ", data: "BT [(Mint) 20 (Claw)] TJ ET", want: FactPresent},
		{name: "single quote", data: "BT (MintClaw) ' ET", want: FactPresent},
		{name: "double quote", data: "BT 10 2 (MintClaw) \" ET", want: FactPresent},
		{name: "outside text object", data: "(MintClaw) Tj", want: FactAbsent},
		{name: "drawing only", data: "0 0 m 10 10 l s", want: FactAbsent},
		{name: "operator in comment", data: "% BT (MintClaw) Tj ET\n0 0 m", want: FactAbsent},
		{name: "operators in names", data: "/BT /Tj BDC EMC", want: FactAbsent},
		{name: "operators in dictionary", data: "<< /Begin /BT /Show /Tj >> BDC EMC", want: FactAbsent},
		{
			name: "operators in inline image",
			data: "q BI /W 1 /H 1 /BPC 8 /CS /G ID fake BT (MintClaw) Tj EI Q",
			want: FactUnknown,
		},
		{name: "unterminated string", data: "BT (MintClaw", want: FactUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := textShowingOperatorState([]byte(test.data)); got != test.want {
				t.Fatalf("textShowingOperatorState(%q) = %v, want %v", test.data, got, test.want)
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
