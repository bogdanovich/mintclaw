package document

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type acquisitionFixtureManifest struct {
	SchemaVersion string               `json:"schema_version"`
	Privacy       string               `json:"privacy"`
	Fixtures      []acquisitionFixture `json:"fixtures"`
}

type acquisitionFixture struct {
	ID            string `json:"id"`
	Construction  string `json:"construction"`
	Source        string `json:"source,omitempty"`
	ExpectedState State  `json:"expected_state"`
	FailureCode   string `json:"failure_code,omitempty"`
	EvidenceTest  string `json:"evidence_test"`
}

func TestAcquisitionFixtureManifestCoversPDF0A(t *testing.T) {
	data, err := os.ReadFile("testdata/acquisition-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest acquisitionFixtureManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode acquisition manifest: %v", err)
	}
	if manifest.SchemaVersion != "mintclaw.document_fixture_manifest.v1" || manifest.Privacy != "synthetic_only" {
		t.Fatalf("manifest identity = %#v", manifest)
	}
	required := map[string]bool{
		"normal-pdf": false, "same-name-different-bytes": false, "symlink": false,
		"fifo": false, "device": false, "path-replacement": false, "mid-copy-mutation": false,
		"oversized-input": false, "cancellation": false, "worker-crash": false,
		"worker-timeout": false, "worker-output-limit": false, "concurrent-acquisition": false,
	}
	for _, fixture := range manifest.Fixtures {
		seen, requiredFixture := required[fixture.ID]
		if !requiredFixture {
			t.Fatalf("unexpected fixture %q", fixture.ID)
		}
		if seen {
			t.Fatalf("duplicate fixture %q", fixture.ID)
		}
		if fixture.Construction == "" || fixture.ExpectedState == "" || fixture.EvidenceTest == "" {
			t.Fatalf("incomplete fixture declaration: %#v", fixture)
		}
		if !acquisitionEvidenceTestExists(t, fixture.EvidenceTest) {
			t.Fatalf("fixture %q cites missing evidence test %q", fixture.ID, fixture.EvidenceTest)
		}
		if fixture.Construction == "checked_in" {
			if _, err := os.Stat(filepath.Join("testdata", fixture.Source)); err != nil {
				t.Fatalf("fixture %q source is unavailable: %v", fixture.ID, err)
			}
		}
		if fixture.ExpectedState != StateSucceeded && fixture.FailureCode == "" {
			t.Fatalf("failure fixture lacks code: %#v", fixture)
		}
		required[fixture.ID] = true
	}
	for id, present := range required {
		if !present {
			t.Errorf("required PDF0A fixture %q is missing", id)
		}
	}
}

func acquisitionEvidenceTestExists(t *testing.T, name string) bool {
	t.Helper()
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	needle := []byte("func " + name + "(")
	for _, file := range files {
		data, readErr := os.ReadFile(file)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if bytes.Contains(data, needle) {
			return true
		}
	}
	return false
}
