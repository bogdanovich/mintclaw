package pdfiumwasm_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/enums"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
)

const (
	goPDFiumVersion      = "v1.20.0"
	pdfiumRevision       = "8044"
	wazeroVersion        = "v1.12.0"
	goPDFiumLicenseSHA   = "fd871478ba874c3e1736c691e3ca89a350ab769db6c7aac9818024f912afb488"
	wazeroLicenseSHA     = "c46f033d017a5af71a1de0105ec56c41bd47f81a0bbdf779fffe316336dc7c1f"
	pdfiumLicenseSHA     = "1fe9dea718fbd75cf149adaf4d8a22a4335604d964ddb76d1b45383dec8668c9"
	embeddedWASMSize     = int64(5_752_581)
	embeddedWASMSHA256   = "f651270c675cac90702b762f4b95d2b34e365cdb374065e40af016f4f0f304ea"
	maximumInputBytes    = int64(20 * 1024 * 1024)
	maximumPages         = 2_000
	maximumExtractPages  = 20
	maximumExtractChars  = 256_000
	maximumRenderPages   = 8
	maximumRenderEdge    = 3_200
	maximumPixelsPerPage = int64(16_000_000)
	maximumTotalPixels   = int64(32_000_000)
	maximumArtifactBytes = int64(32 * 1024 * 1024)
	wasmMemoryLimitPages = 4_096
	instanceAcquireLimit = 30 * time.Second
	stuckRenderKillLimit = 5 * time.Second
)

func TestPinnedModuleProvenanceAndImports(t *testing.T) {
	moduleRoot := dependencyModuleRoot(t, "github.com/klippa-app/go-pdfium", goPDFiumVersion)
	assertFileSHA256(t, filepath.Join(moduleRoot, "LICENSE"), goPDFiumLicenseSHA)
	wazeroRoot := dependencyModuleRoot(t, "github.com/tetratelabs/wazero", wazeroVersion)
	assertFileSHA256(t, filepath.Join(wazeroRoot, "LICENSE"), wazeroLicenseSHA)
	wasmPath := filepath.Join(moduleRoot, "webassembly", "pdfium.wasm")
	wasm := readBoundedFile(t, wasmPath, embeddedWASMSize)
	if size := int64(len(wasm)); size != embeddedWASMSize {
		t.Fatalf("embedded wasm size = %d, want %d", size, embeddedWASMSize)
	}
	if digest := sha256Hex(wasm); digest != embeddedWASMSHA256 {
		t.Fatalf("embedded wasm sha256 = %s, want %s", digest, embeddedWASMSHA256)
	}

	ctx := t.Context()
	runtimeConfig := candidateRuntimeConfig()
	wasmRuntime := wazero.NewRuntimeWithConfig(ctx, runtimeConfig)
	t.Cleanup(func() {
		if err := wasmRuntime.Close(context.Background()); err != nil {
			t.Errorf("close inspection runtime: %v", err)
		}
	})
	compiled, err := wasmRuntime.CompileModule(ctx, wasm)
	if err != nil {
		t.Fatalf("compile embedded wasm: %v", err)
	}
	t.Cleanup(func() {
		if err := compiled.Close(context.Background()); err != nil {
			t.Errorf("close compiled module: %v", err)
		}
	})

	imports := make([]string, 0, len(compiled.ImportedFunctions()))
	for _, function := range compiled.ImportedFunctions() {
		moduleName, functionName, imported := function.Import()
		if !imported {
			t.Fatalf("reported import has no import identity: %s", function.DebugName())
		}
		if moduleName != "env" && moduleName != "wasi_snapshot_preview1" {
			t.Fatalf("unexpected imported module %q for %q", moduleName, functionName)
		}
		lowerName := strings.ToLower(functionName)
		if strings.Contains(lowerName, "sock") || strings.Contains(lowerName, "network") {
			t.Fatalf("network-capable import %s.%s", moduleName, functionName)
		}
		imports = append(imports, moduleName+"."+functionName)
	}
	sort.Strings(imports)
	if len(imports) == 0 {
		t.Fatal("embedded wasm declared no imports")
	}
	t.Logf(
		"qualified go-pdfium=%s pdfium=%s wazero=%s wasm_sha256=%s imports=%d",
		goPDFiumVersion,
		pdfiumRevision,
		wazeroVersion,
		embeddedWASMSHA256,
		len(imports),
	)
}

func TestProductionNoticesMatchQualifiedLicenses(t *testing.T) {
	root := repositoryRoot(t)
	for name, expected := range map[string]string{
		"go-pdfium.txt": goPDFiumLicenseSHA,
		"pdfium.txt":    pdfiumLicenseSHA,
		"wazero.txt":    wazeroLicenseSHA,
	} {
		t.Run(name, func(t *testing.T) {
			assertFileSHA256(t, filepath.Join(root, "THIRD_PARTY_NOTICES", name), expected)
		})
	}
}

func TestCandidateHasNoHostFilesystemView(t *testing.T) {
	data := fixtureBytes(t, "text.pdf")
	hostPath := filepath.Join(t.TempDir(), "host-visible.pdf")
	if err := os.WriteFile(hostPath, data, 0o600); err != nil {
		t.Fatalf("write host fixture: %v", err)
	}

	_, instance := newCandidateInstance(t, t.Context())
	if document, err := instance.OpenDocument(&requests.OpenDocument{FilePath: &hostPath}); err == nil {
		closeDocument(t, instance, document.Document)
		t.Fatalf("WASM opened ambient host path %q", hostPath)
	}
	document, err := instance.OpenDocument(&requests.OpenDocument{File: &data})
	if err != nil {
		t.Fatalf("open the same document from bytes: %v", err)
	}
	closeDocument(t, instance, document.Document)
}

func TestCandidateFixtureContract(t *testing.T) {
	tests := []struct {
		name           string
		file           string
		markers        []string
		extract        bool
		render         bool
		renderForm     bool
		expectNoText   bool
		expectFormType enums.FPDF_FORMTYPE
	}{
		{
			name: "plain text", file: "text.pdf", markers: []string{"MintClaw text fixture"},
			extract: true, render: true,
		},
		{
			name: "unicode", file: "unicode.pdf", markers: []string{"MintClaw Café résumé"},
			extract: true, render: true,
		},
		{
			name: "ambiguous reading order", file: "ambiguous-reading-order.pdf",
			markers: []string{"MINTCLAW_FIRST_VISUAL", "MINTCLAW_SECOND_VISUAL"}, extract: true,
		},
		{
			name: "rotated crop", file: "rotated-crop.pdf", markers: []string{"MINTCLAW_ROTATED_CROP"},
			extract: true, render: true,
		},
		{
			name: "image only", file: "image-only.pdf", extract: true, render: true, expectNoText: true,
		},
		{
			name: "acroform", file: "acroform.pdf", markers: []string{"Synthetic AcroForm"},
			extract: true, render: true, renderForm: true, expectFormType: enums.FPDF_FORMTYPE_ACRO_FORM,
		},
		{
			name: "rotated extreme dimensions", file: "extreme-dimensions.pdf", render: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := fixtureBytes(t, test.file)
			if int64(len(data)) > maximumInputBytes {
				t.Fatalf("fixture size = %d, limit = %d", len(data), maximumInputBytes)
			}
			_, instance := newCandidateInstance(t, t.Context())
			document, err := instance.OpenDocument(&requests.OpenDocument{File: &data})
			if err != nil {
				t.Fatalf("open fixture: %v", err)
			}
			t.Cleanup(func() { closeDocument(t, instance, document.Document) })

			pageCount := candidatePageCount(t, instance, document.Document)
			if pageCount < 1 || pageCount > maximumPages {
				t.Fatalf("page count = %d, limit = %d", pageCount, maximumPages)
			}
			if test.expectFormType != enums.FPDF_FORMTYPE_NONE {
				formType, formErr := instance.FPDF_GetFormType(&requests.FPDF_GetFormType{
					Document: document.Document,
				})
				if formErr != nil || formType.FormType != test.expectFormType {
					t.Fatalf("form type = %#v, err = %v", formType, formErr)
				}
			}

			if test.extract {
				text := extractPageText(t, instance, document.Document, 0)
				if !utf8.ValidString(text) {
					t.Fatal("extracted text is not valid UTF-8")
				}
				if utf8.RuneCountInString(text) > maximumExtractChars {
					t.Fatalf("extracted characters exceed limit: %d", utf8.RuneCountInString(text))
				}
				if test.expectNoText && strings.TrimSpace(text) != "" {
					t.Fatalf("image-only text = %q", text)
				}
				for _, marker := range test.markers {
					if !strings.Contains(text, marker) {
						t.Fatalf("extracted text %q does not contain %q", text, marker)
					}
				}
			}
			if test.render {
				rendered := renderPageBounded(t, instance, document.Document, test.renderForm)
				t.Logf(
					"rendered %s: %dx%d bytes=%d sha256=%s",
					test.file,
					rendered.width,
					rendered.height,
					rendered.bytes,
					rendered.sha256,
				)
			}
		})
	}
}

func TestCandidateRejectsUnsupportedAndMalformedFixtures(t *testing.T) {
	tests := []struct {
		name string
		file string
	}{
		{name: "truncated", file: "truncated.pdf"},
		{name: "password required", file: "encrypted-password-required.pdf"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := fixtureBytes(t, test.file)
			_, instance := newCandidateInstance(t, t.Context())
			document, err := instance.OpenDocument(&requests.OpenDocument{File: &data})
			if err == nil {
				closeDocument(t, instance, document.Document)
				t.Fatalf("candidate unexpectedly opened %s", test.file)
			}
		})
	}
}

func TestCandidateRequiresStructuralPreflightForMalformedXref(t *testing.T) {
	data := fixtureBytes(t, "malformed-xref.pdf")
	_, instance := newCandidateInstance(t, t.Context())
	document, err := instance.OpenDocument(&requests.OpenDocument{File: &data})
	if err != nil {
		t.Fatalf("PDFium no longer accepts the structural-preflight fixture: %v", err)
	}
	closeDocument(t, instance, document.Document)
}

func TestCandidateKillInterruptsActiveRender(t *testing.T) {
	data := fixtureBytes(t, "many-pages.pdf")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	pool, err := newCandidatePool(ctx)
	if err != nil {
		t.Fatalf("initialize candidate pool: %v", err)
	}
	defer func() { _ = pool.Close() }()
	instance, err := acquireCandidateInstance(ctx, pool)
	if err != nil {
		t.Fatalf("get candidate instance: %v", err)
	}
	document, err := instance.OpenDocument(&requests.OpenDocument{File: &data})
	if err != nil {
		t.Fatalf("open cancellation fixture: %v", err)
	}

	pages := make([]requests.RenderPageInDPI, maximumRenderPages)
	for index := range pages {
		pages[index] = requests.RenderPageInDPI{
			Page: requests.Page{ByIndex: &requests.PageByIndex{
				Document: document.Document,
				Index:    index,
			}},
			DPI: 180,
		}
	}
	renderDone := make(chan error, 1)
	renderStarted := make(chan struct{})
	go func() {
		close(renderStarted)
		_, renderErr := instance.RenderToFile(&requests.RenderToFile{
			RenderPagesInDPI: &requests.RenderPagesInDPI{Pages: pages},
			OutputFormat:     requests.RenderToFileOutputFormatJPG,
			OutputTarget:     requests.RenderToFileOutputTargetBytes,
			OutputQuality:    50,
		})
		renderDone <- renderErr
	}()

	<-renderStarted
	time.Sleep(10 * time.Millisecond)
	select {
	case renderErr := <-renderDone:
		t.Fatalf("qualification render completed before cancellation: %v", renderErr)
	default:
	}
	killDone := make(chan error, 1)
	go func() { killDone <- instance.Kill() }()
	select {
	case <-killDone:
	case <-time.After(stuckRenderKillLimit):
		t.Fatalf("candidate kill exceeded %s", stuckRenderKillLimit)
	}
	select {
	case renderErr := <-renderDone:
		if renderErr == nil {
			t.Fatal("active render returned successfully after kill")
		}
	case <-time.After(stuckRenderKillLimit):
		t.Fatalf("active render did not stop within %s", stuckRenderKillLimit)
	}
}

type renderedPageEvidence struct {
	width  int
	height int
	bytes  int64
	sha256 string
}

func renderPageBounded(
	t *testing.T,
	instance pdfium.Pdfium,
	document references.FPDF_DOCUMENT,
	renderForm bool,
) renderedPageEvidence {
	t.Helper()
	response, err := instance.RenderPageInPixels(&requests.RenderPageInPixels{
		Page: requests.Page{ByIndex: &requests.PageByIndex{
			Document: document,
			Index:    0,
		}},
		Width:      maximumRenderEdge,
		Height:     maximumRenderEdge,
		RenderForm: renderForm,
		Document:   &document,
	})
	if err != nil {
		t.Fatalf("render page: %v", err)
	}
	t.Cleanup(response.Cleanup)
	result := response.Result
	pixels := int64(result.Width) * int64(result.Height)
	if result.Width < 1 || result.Height < 1 || result.Width > maximumRenderEdge ||
		result.Height > maximumRenderEdge || pixels > maximumPixelsPerPage || pixels > maximumTotalPixels {
		t.Fatalf("rendered dimensions = %dx%d (%d pixels)", result.Width, result.Height, pixels)
	}
	if result.RenderedImage == nil {
		t.Fatal("rendered image is nil")
	}
	encoded := &boundedDigestWriter{hash: sha256.New(), maximum: maximumArtifactBytes}
	if err = png.Encode(encoded, result.RenderedImage); err != nil {
		t.Fatalf("encode rendered page within %d bytes: %v", maximumArtifactBytes, err)
	}
	return renderedPageEvidence{
		width:  result.Width,
		height: result.Height,
		bytes:  encoded.written,
		sha256: hex.EncodeToString(encoded.hash.Sum(nil)),
	}
}

type boundedDigestWriter struct {
	hash    hash.Hash
	maximum int64
	written int64
}

func (w *boundedDigestWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > w.maximum-w.written {
		return 0, fmt.Errorf("encoded artifact exceeds %d bytes", w.maximum)
	}
	written, err := w.hash.Write(data)
	w.written += int64(written)
	return written, err
}

func extractPageText(
	t *testing.T,
	instance pdfium.Pdfium,
	document references.FPDF_DOCUMENT,
	page int,
) string {
	t.Helper()
	if page < 0 || page >= maximumExtractPages {
		t.Fatalf("extract page index = %d", page)
	}
	response, err := instance.GetPageText(&requests.GetPageText{Page: requests.Page{
		ByIndex: &requests.PageByIndex{Document: document, Index: page},
	}})
	if err != nil {
		t.Fatalf("extract page text: %v", err)
	}
	return response.Text
}

func candidatePageCount(t *testing.T, instance pdfium.Pdfium, document references.FPDF_DOCUMENT) int {
	t.Helper()
	response, err := instance.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: document})
	if err != nil {
		t.Fatalf("get page count: %v", err)
	}
	return response.PageCount
}

func newCandidateInstance(t *testing.T, ctx context.Context) (pdfium.Pool, pdfium.Pdfium) {
	t.Helper()
	pool, err := newCandidatePool(ctx)
	if err != nil {
		t.Fatalf("initialize candidate pool: %v", err)
	}
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Errorf("close candidate pool: %v", err)
		}
	})
	instance, err := acquireCandidateInstance(ctx, pool)
	if err != nil {
		t.Fatalf("get candidate instance: %v", err)
	}
	t.Cleanup(func() {
		if err := instance.Close(); err != nil {
			t.Errorf("close candidate instance: %v", err)
		}
	})
	return pool, instance
}

func newCandidatePool(ctx context.Context) (pdfium.Pool, error) {
	return webassembly.Init(webassembly.Config{
		Context:       ctx,
		MinIdle:       0,
		MaxIdle:       1,
		MaxTotal:      1,
		FSConfig:      wazero.NewFSConfig(),
		RuntimeConfig: candidateRuntimeConfig(),
		Stdout:        io.Discard,
		Stderr:        io.Discard,
		ReuseWorkers:  false,
	})
}

func acquireCandidateInstance(ctx context.Context, pool pdfium.Pool) (pdfium.Pdfium, error) {
	acquireCtx, cancel := context.WithTimeout(ctx, instanceAcquireLimit)
	defer cancel()
	return pool.GetInstanceWithContext(acquireCtx)
}

func candidateRuntimeConfig() wazero.RuntimeConfig {
	features := api.CoreFeaturesV2 | experimental.CoreFeaturesExceptionHandling
	return wazero.NewRuntimeConfig().
		WithCoreFeatures(features).
		WithMemoryLimitPages(wasmMemoryLimitPages).
		WithCloseOnContextDone(true)
}

func closeDocument(t *testing.T, instance pdfium.Pdfium, document references.FPDF_DOCUMENT) {
	t.Helper()
	if _, err := instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: document}); err != nil {
		t.Errorf("close document: %v", err)
	}
}

func fixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	return readBoundedFile(t, filepath.Join(repositoryRoot(t), "pkg", "document", "testdata", name), maximumInputBytes)
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve qualification source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func dependencyModuleRoot(t *testing.T, module, version string) string {
	t.Helper()
	command := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Dir}}", module+"@"+version)
	output, err := command.Output()
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			t.Fatalf("locate %s@%s: %v: %s", module, version, err, bytes.TrimSpace(exitError.Stderr))
		}
		t.Fatalf("locate %s@%s: %v", module, version, err)
	}
	path := strings.TrimSpace(string(output))
	if path == "" || !filepath.IsAbs(path) {
		t.Fatalf("module path = %q", path)
	}
	return path
}

func readBoundedFile(t *testing.T, path string, maximum int64) []byte {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Errorf("close %s: %v", path, err)
		}
	}()
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if int64(len(data)) > maximum {
		t.Fatalf("%s exceeds %d bytes", path, maximum)
	}
	return data
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func assertFileSHA256(t *testing.T, path, expected string) {
	t.Helper()
	data := readBoundedFile(t, path, 64*1024)
	if digest := sha256Hex(data); digest != expected {
		t.Fatalf("%s sha256 = %s, want %s", path, digest, expected)
	}
}

func TestQualificationLimitsMatchRoadmapEnvelope(t *testing.T) {
	if maximumExtractPages > maximumPages || maximumRenderPages > maximumPages ||
		maximumPixelsPerPage > maximumTotalPixels || maximumArtifactBytes < maximumInputBytes ||
		instanceAcquireLimit <= 0 {
		t.Fatal("qualification resource envelope is internally inconsistent")
	}
}
