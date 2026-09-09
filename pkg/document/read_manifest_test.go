package document

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type readFixtureManifest struct {
	SchemaVersion         string                `json:"schema_version"`
	Privacy               string                `json:"privacy"`
	Generator             string                `json:"generator"`
	License               string                `json:"license"`
	ProductionBackend     readFixtureBackend    `json:"production_backend"`
	Oracle                readFixtureOracle     `json:"oracle"`
	Fixtures              []readFixture         `json:"fixtures"`
	InjectedTerminalCases []readInjectedFixture `json:"injected_terminal_cases"`
}

type readFixtureBackend struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type readFixtureOracle struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	NPMSHA1 string `json:"npm_sha1"`
}

type readFixture struct {
	ID                 string      `json:"id"`
	File               string      `json:"file"`
	SHA256             string      `json:"sha256"`
	Pages              []int       `json:"pages"`
	Operations         []string    `json:"operations"`
	Markers            []string    `json:"markers"`
	ExpectedState      State       `json:"expected_state"`
	FailureCode        FailureCode `json:"failure_code"`
	ExtractFailureCode FailureCode `json:"extract_failure_code"`
	ExpectedTruncated  bool        `json:"expected_truncated"`
	PixelTolerance     float64     `json:"pixel_tolerance"`
	TextOrder          string      `json:"text_order"`
	EvidenceTest       string      `json:"evidence_test"`
}

type readInjectedFixture struct {
	ID           string      `json:"id"`
	FailureCode  FailureCode `json:"failure_code"`
	EvidenceTest string      `json:"evidence_test"`
}

func TestReadFixtureManifestIsSyntheticCompleteAndCurrent(t *testing.T) {
	manifest := loadReadFixtureManifest(t)
	if manifest.SchemaVersion != "mintclaw.document_read_fixture_manifest.v1" ||
		manifest.Privacy != "synthetic_only" || manifest.Generator != "testdata/generate/main.go" ||
		manifest.License == "" || manifest.ProductionBackend.Name != PopplerBackendName ||
		manifest.ProductionBackend.Version != PopplerBackendVersion || manifest.Oracle.Name != "clawpdf" ||
		manifest.Oracle.Version != "0.3.2" || len(manifest.Oracle.NPMSHA1) != 40 {
		t.Fatalf("read manifest identity = %#v", manifest)
	}
	required := map[string]bool{
		"text": false, "mixed": false, "scan": false, "unicode": false, "rotated-crop": false,
		"ambiguous-reading-order": false, "acroform-appearance": false, "xfa-refusal": false,
		"signed-classification-preserved": false, "password-refusal": false, "malformed-refusal": false,
		"character-limit": false, "page-limit": false, "pixel-limit": false,
	}
	for _, fixture := range manifest.Fixtures {
		seen, expected := required[fixture.ID]
		if !expected || seen || filepath.Base(fixture.File) != fixture.File || fixture.EvidenceTest == "" ||
			fixture.ExpectedState == "" || len(fixture.Operations) == 0 {
			t.Fatalf("invalid read fixture: %#v", fixture)
		}
		data, err := os.ReadFile(filepath.Join("testdata", fixture.File))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != fixture.SHA256 {
			t.Fatalf("fixture %q digest is stale", fixture.ID)
		}
		if !acquisitionEvidenceTestExists(t, fixture.EvidenceTest) {
			t.Fatalf("fixture %q cites missing evidence %q", fixture.ID, fixture.EvidenceTest)
		}
		required[fixture.ID] = true
	}
	for id, present := range required {
		if !present {
			t.Errorf("required PDF1A fixture %q is missing", id)
		}
	}
	if len(manifest.InjectedTerminalCases) < 7 {
		t.Fatalf("terminal case coverage = %d", len(manifest.InjectedTerminalCases))
	}
	for _, fixture := range manifest.InjectedTerminalCases {
		if fixture.ID == "" || fixture.EvidenceTest == "" ||
			!acquisitionEvidenceTestExists(t, fixture.EvidenceTest) {
			t.Fatalf("invalid injected terminal fixture: %#v", fixture)
		}
	}
}

func loadReadFixtureManifest(t *testing.T) readFixtureManifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "read-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest readFixtureManifest
	if err = json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}
