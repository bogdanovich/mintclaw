//go:build linux && amd64

package document

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type formVisualManifest struct {
	SchemaVersion     string                `json:"schema_version"`
	Privacy           string                `json:"privacy"`
	Fixture           formVisualFixture     `json:"fixture"`
	ProductionWriter  formVisualBackend     `json:"production_writer"`
	VisualBackend     formVisualBackend     `json:"visual_backend"`
	IndependentReader formVisualBackend     `json:"independent_reader"`
	GridWidth         int                   `json:"grid_width"`
	GridHeight        int                   `json:"grid_height"`
	MaximumDifference float64               `json:"maximum_mean_difference"`
	Pages             []formVisualPageEntry `json:"pages"`
	Cases             []formVisualCase      `json:"cases"`
}

type formVisualFixture struct {
	File         string `json:"file"`
	SHA256       string `json:"sha256"`
	Construction string `json:"construction"`
	License      string `json:"license"`
}

type formVisualBackend struct {
	Name           string `json:"name"`
	Version        string `json:"version"`
	DPI            int    `json:"dpi"`
	RendererSHA256 string `json:"renderer_sha256"`
	TextSHA256     string `json:"text_sha256"`
}

type formVisualPageEntry struct {
	Page             int      `json:"page"`
	Width            int      `json:"width"`
	Height           int      `json:"height"`
	MinimumChanged   int      `json:"minimum_changed_pixels"`
	DifferenceGolden []uint16 `json:"difference_golden"`
}

type formVisualCase struct {
	ID            string      `json:"id"`
	ExpectedState State       `json:"expected_state"`
	FailureCode   FailureCode `json:"failure_code"`
	EvidenceTest  string      `json:"evidence_test"`
}

func TestFormVisualManifestIsSyntheticCompleteAndCurrent(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "form-visual-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest formVisualManifest
	if err = json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != "mintclaw.document_form_visual_fixture_manifest.v1" ||
		manifest.Privacy != "synthetic_only" || manifest.Fixture.Construction != "deterministic_go_generator" ||
		manifest.Fixture.License == "" || filepath.Base(manifest.Fixture.File) != manifest.Fixture.File ||
		manifest.ProductionWriter.Name != PDFCPUBackendName ||
		manifest.ProductionWriter.Version != PDFCPUBackendVersion ||
		manifest.VisualBackend.Name != PopplerBackendName ||
		manifest.VisualBackend.Version != PopplerBackendVersion || manifest.VisualBackend.DPI != DefaultRenderDPI ||
		manifest.VisualBackend.RendererSHA256 != popplerRenderSHA256 ||
		manifest.VisualBackend.TextSHA256 != popplerTextSHA256 ||
		manifest.IndependentReader.Name != "pypdf" || manifest.IndependentReader.Version != "6.1.1" ||
		manifest.GridWidth != 8 || manifest.GridHeight != 8 || manifest.MaximumDifference <= 0 ||
		manifest.MaximumDifference > 0.01 || len(manifest.Pages) != 2 {
		t.Fatalf("visual manifest identity = %#v", manifest)
	}
	fixture, err := os.ReadFile(filepath.Join("testdata", manifest.Fixture.File))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(fixture)
	if hex.EncodeToString(digest[:]) != manifest.Fixture.SHA256 {
		t.Fatal("visual fixture digest is stale")
	}
	for index, page := range manifest.Pages {
		if page.Page != index+1 || page.Width <= 0 || page.Height <= 0 || page.MinimumChanged <= 0 ||
			len(page.DifferenceGolden) != manifest.GridWidth*manifest.GridHeight {
			t.Fatalf("visual page golden = %#v", page)
		}
	}
	required := map[string]bool{
		"supported-matrix-visible":      false,
		"supported-unicode-visible":     false,
		"missing-font-refusal":          false,
		"stale-appearance-refusal":      false,
		"clipped-content-refusal":       false,
		"misattributed-glyph-refusal":   false,
		"stale-list-selection-refusal":  false,
		"overlapping-widget-refusal":    false,
		"oversized-visual-page-refusal": false,
		"affected-page-limit":           false,
		"flatten-withheld":              false,
	}
	for _, item := range manifest.Cases {
		seen, known := required[item.ID]
		if !known || seen || item.ExpectedState == "" || item.EvidenceTest == "" ||
			!acquisitionEvidenceTestExists(t, item.EvidenceTest) {
			t.Fatalf("visual case = %#v", item)
		}
		required[item.ID] = true
	}
	for id, present := range required {
		if !present {
			t.Errorf("required visual case %q is missing", id)
		}
	}
}
