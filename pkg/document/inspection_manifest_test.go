package document

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type inspectionFixtureManifest struct {
	SchemaVersion string              `json:"schema_version"`
	Privacy       string              `json:"privacy"`
	Generator     string              `json:"generator"`
	License       string              `json:"license"`
	Fixtures      []inspectionFixture `json:"fixtures"`
}

type inspectionFixture struct {
	ID           string                    `json:"id"`
	File         string                    `json:"file"`
	SHA256       string                    `json:"sha256"`
	Construction string                    `json:"construction"`
	License      string                    `json:"license"`
	Expected     inspectionFixtureExpected `json:"expected"`
	EvidenceTest string                    `json:"evidence_test"`
}

type inspectionFixtureExpected struct {
	State             State       `json:"state"`
	FailureCode       FailureCode `json:"failure_code,omitempty"`
	PDFVersion        string      `json:"pdf_version"`
	PageCount         int         `json:"page_count"`
	Text              FactState   `json:"text"`
	AcroForm          FactState   `json:"acroform"`
	FieldCount        *int        `json:"field_count,omitempty"`
	XFA               FactState   `json:"xfa"`
	XFARepresentation string      `json:"xfa_representation,omitempty"`
	XFARendering      string      `json:"xfa_rendering,omitempty"`
	Signatures        FactState   `json:"signatures"`
	SignatureCount    *int        `json:"signature_count,omitempty"`
	Certified         FactState   `json:"certified,omitempty"`
	Timestamped       FactState   `json:"timestamped,omitempty"`
	Restrictions      FactState   `json:"restrictions"`
	DocMDP            FactState   `json:"doc_mdp,omitempty"`
	FieldMDP          FactState   `json:"field_mdp,omitempty"`
	UsageRights       FactState   `json:"usage_rights,omitempty"`
	Encryption        FactState   `json:"encryption,omitempty"`
	PasswordRequired  FactState   `json:"password_required,omitempty"`
}

func TestInspectionFixtureManifestIsSyntheticCompleteAndCurrent(t *testing.T) {
	manifest := loadInspectionFixtureManifest(t)
	if manifest.SchemaVersion != "mintclaw.document_inspection_fixture_manifest.v1" ||
		manifest.Privacy != "synthetic_only" || manifest.Generator != "testdata/generate/main.go" ||
		manifest.License == "" {
		t.Fatalf("manifest identity = %#v", manifest)
	}
	required := map[string]bool{
		"text": false, "image-only": false, "mixed-pages": false, "acroform": false,
		"name-operands-no-text": false, "inline-image": false, "orphan-structure": false,
		"xfa-dynamic": false, "hybrid-xfa-static": false, "unsigned-signature": false,
		"signed-certified": false, "field-restricted": false, "timestamped": false, "rights-enabled": false,
		"encrypted-password-required": false,
		"truncated":                   false, "malformed-xref": false, "oversized-stream-declaration": false,
		"oversized-object-count": false, "adversarial-nesting": false,
		"decoded-content-limit":       false,
		"decoded-content-array-limit": false,
		"xfa-decoded-limit":           false,
	}
	for _, fixture := range manifest.Fixtures {
		seen, expected := required[fixture.ID]
		if !expected || seen {
			t.Fatalf("unexpected or duplicate fixture %q", fixture.ID)
		}
		if filepath.Base(fixture.File) != fixture.File || filepath.Ext(fixture.File) != ".pdf" ||
			fixture.Construction == "" || fixture.License == "" ||
			fixture.EvidenceTest != "TestPDFCPUBackendMatchesInspectionManifest" {
			t.Fatalf("incomplete fixture declaration: %#v", fixture)
		}
		data, err := os.ReadFile(filepath.Join("testdata", fixture.File))
		if err != nil {
			t.Fatalf("read fixture %q: %v", fixture.ID, err)
		}
		digest := sha256.Sum256(data)
		if actual := hex.EncodeToString(digest[:]); actual != fixture.SHA256 {
			t.Fatalf("fixture %q digest = %s, want %s", fixture.ID, actual, fixture.SHA256)
		}
		required[fixture.ID] = true
	}
	for id, present := range required {
		if !present {
			t.Errorf("required PDF0B fixture %q is missing", id)
		}
	}

	generator, err := os.ReadFile(filepath.Join("testdata", "generate", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range [][]byte{
		[]byte("tax"), []byte("immigration"), []byte("medical"), []byte("government"),
	} {
		if bytes.Contains(bytes.ToLower(generator), forbidden) {
			t.Fatalf("fixture generator contains forbidden personal-domain marker %q", forbidden)
		}
	}
}

func loadInspectionFixtureManifest(t *testing.T) inspectionFixtureManifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "inspection-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest inspectionFixtureManifest
	if err = json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode inspection manifest: %v", err)
	}
	return manifest
}
