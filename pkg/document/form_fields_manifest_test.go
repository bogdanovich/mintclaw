package document

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

type formFieldsFixtureManifest struct {
	SchemaVersion string                    `json:"schema_version"`
	Privacy       string                    `json:"privacy"`
	Generator     string                    `json:"generator"`
	License       string                    `json:"license"`
	Fixtures      []formFieldsFixtureRecord `json:"fixtures"`
}

type formFieldsFixtureRecord struct {
	ID           string                    `json:"id"`
	File         string                    `json:"file"`
	SHA256       string                    `json:"sha256"`
	Construction string                    `json:"construction"`
	License      string                    `json:"license"`
	Expected     formFieldsFixtureExpected `json:"expected"`
	EvidenceTest string                    `json:"evidence_test"`
}

type formFieldsFixtureExpected struct {
	State       State       `json:"state"`
	FailureCode FailureCode `json:"failure_code,omitempty"`
	FieldCount  int         `json:"field_count,omitempty"`
	WidgetCount int         `json:"widget_count,omitempty"`
	Kinds       []string    `json:"kinds,omitempty"`
}

func TestFormFieldsFixtureManifestIsSyntheticCompleteAndCurrent(t *testing.T) {
	manifest := loadFormFieldsFixtureManifest(t)
	if manifest.SchemaVersion != "mintclaw.document_form_fields_fixture_manifest.v1" ||
		manifest.Privacy != "synthetic_only" || manifest.Generator != "testdata/generate/main.go" ||
		manifest.License == "" {
		t.Fatalf("manifest identity = %#v", manifest)
	}
	required := map[string]bool{
		"supported-field-matrix":     false,
		"form-not-present":           false,
		"hybrid-xfa-discovery":       false,
		"hybrid-xfa-dynamic-refusal": false,
		"signed-refusal":             false,
		"signature-field-refusal":    false,
		"calculated-field-refusal":   false,
		"password-refusal":           false,
	}
	for _, fixture := range manifest.Fixtures {
		seen, expected := required[fixture.ID]
		if !expected || seen || filepath.Base(fixture.File) != fixture.File ||
			filepath.Ext(fixture.File) != ".pdf" || fixture.Construction == "" || fixture.License == "" ||
			fixture.EvidenceTest != "TestPDFCPUFormFieldsBackendMatchesManifest" || fixture.Expected.State == "" {
			t.Fatalf("invalid fields fixture: %#v", fixture)
		}
		data, err := os.ReadFile(filepath.Join("testdata", fixture.File))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != fixture.SHA256 {
			t.Fatalf("fixture %q digest is stale", fixture.ID)
		}
		required[fixture.ID] = true
	}
	for id, present := range required {
		if !present {
			t.Errorf("required PDF2 fields fixture %q is missing", id)
		}
	}
}

func loadFormFieldsFixtureManifest(t *testing.T) formFieldsFixtureManifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "form-fields-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest formFieldsFixtureManifest
	if err = json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	for index := range manifest.Fixtures {
		sort.Strings(manifest.Fixtures[index].Expected.Kinds)
	}
	return manifest
}
