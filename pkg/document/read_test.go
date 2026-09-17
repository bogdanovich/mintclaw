package document

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNormalizePageSelectionIsClosedAndOrdered(t *testing.T) {
	pages, ok := normalizePageSelection(nil, 3, 3)
	if !ok || !equalPages(pages, []int{1, 2, 3}) {
		t.Fatalf("default pages = %v, %v", pages, ok)
	}
	for _, invalid := range [][]int{{2, 1}, {1, 1}, {0}, {4}, {1, 2, 3, 4}} {
		if pages, ok = normalizePageSelection(invalid, 3, 3); ok || pages != nil {
			t.Fatalf("accepted invalid pages %v as %v", invalid, pages)
		}
	}
	if pages, ok = normalizePageSelection(nil, 4, 3); ok || pages != nil {
		t.Fatalf("default selection exceeded operation limit: %v", pages)
	}
}

func TestPublicReadFailsClosedBeforeOpeningOnUnadmittedPlatform(t *testing.T) {
	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		t.Skip("runtime tuple may have the admitted backend")
	}
	root := directTempDir(t)
	scratch := filepath.Join(root, "must-not-exist")
	for _, operation := range []func(context.Context, string, ReadOptions) (*Snapshot, Report){Extract, Render} {
		snapshot, report := operation(
			t.Context(),
			filepath.Join(root, "missing.pdf"),
			ReadOptions{Acquire: AcquireOptions{ScratchRoot: scratch}},
		)
		if snapshot != nil || report.State != StateUnavailable || report.Failure == nil ||
			report.Failure.Code != FailureUnsupportedPlatform {
			t.Fatalf("unsupported read = snapshot %#v, report %#v", snapshot, report)
		}
	}
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Fatalf("unsupported read touched scratch: %v", err)
	}
}

func TestNormalizeReadLimitsOnlyLowersHardBounds(t *testing.T) {
	limits, ok := normalizeReadLimits(operationRender, ReadLimits{DPI: 72, MaxDimension: 1_000})
	if !ok || limits.DPI != 72 || limits.MaxDimension != 1_000 || limits.MaxPages != DefaultMaxRenderPages {
		t.Fatalf("normalized limits = %#v, %v", limits, ok)
	}
	for _, supplied := range []ReadLimits{
		{DPI: DefaultRenderDPI + 1},
		{MaxDimension: 4_097},
		{MaxArtifactBytes: DefaultMaxArtifactBytes + 1},
		{MaxPages: DefaultMaxExtractPages + 1},
	} {
		if _, ok = normalizeReadLimits(operationExtract, supplied); ok {
			t.Fatalf("accepted raised hard bound: %#v", supplied)
		}
	}
}

func TestReadServiceInspectsBeforeExtractingAndKeepsPathFreeEvidence(t *testing.T) {
	root := directTempDir(t)
	inputPath := filepath.Join(root, "same-name.pdf")
	data, err := os.ReadFile(filepath.Join("testdata", "mixed-pages.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, inputPath, data)
	pageCount := 2
	inspector := &readTestInspector{facts: successfulTestInspection()}
	inspector.facts.PageCount = IntegerFact{State: FactPresent, Value: &pageCount}
	inspector.facts.ExtractableText = TextFacts{
		State: FactMixed, PagesWithText: 1, PagesWithoutText: 1,
	}
	extractor := &readTestExtractor{}
	snapshot, report := readWithWorkers(
		t.Context(),
		inputPath,
		ReadOptions{Acquire: AcquireOptions{ScratchRoot: filepath.Join(root, "protected")}, Pages: []int{1}},
		"linux",
		"amd64",
		operationExtract,
		inspector,
		extractor,
		nil,
	)
	if snapshot == nil || report.State != StateSucceeded || report.Inspection == nil || report.Extraction == nil ||
		len(report.Artifacts) != 1 || !equalPages(report.Extraction.SelectedPages, []int{1}) {
		t.Fatalf("read report = %#v, snapshot = %#v", report, snapshot)
	}
	if inspector.called != 1 || extractor.called != 1 || extractor.input.SHA256 != report.Input.SHA256 {
		t.Fatalf("worker calls: inspect=%d extract=%d input=%#v", inspector.called, extractor.called, extractor.input)
	}
	encoded := mustMarshalReport(t, report)
	for _, forbidden := range []string{root, inputPath, "MintClaw mixed text page"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("report leaked %q: %s", forbidden, encoded)
		}
	}
	if err = snapshot.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReadServiceRejectsImageOnlyExtractionAndXFARenderBeforeBackend(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		mutate    func(*InspectionFacts)
		code      FailureCode
	}{
		{
			name: "image-only extraction", operation: operationExtract, code: FailureTextUnavailable,
			mutate: func(facts *InspectionFacts) {
				facts.ExtractableText = TextFacts{State: FactAbsent, PagesWithoutText: 1}
			},
		},
		{
			name: "XFA render", operation: operationRender, code: FailureUnsupportedFeature,
			mutate: func(facts *InspectionFacts) {
				facts.AcroForm = AcroFormFacts{
					State: FactPresent, FieldCount: IntegerFact{State: FactPresent, Value: intPointer(0)},
				}
				facts.XFA = XFAFacts{
					State:          FactPresent,
					Representation: StringFact{State: FactPresent, Value: "stream"},
					Rendering:      StringFact{State: FactPresent, Value: "dynamic"},
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := directTempDir(t)
			inputPath := filepath.Join(root, "input.pdf")
			writeFixture(t, inputPath, []byte("%PDF-1.7\nsynthetic\n%%EOF\n"))
			facts := successfulTestInspection()
			test.mutate(facts)
			pageCount := 1
			facts.PageCount = IntegerFact{State: FactPresent, Value: &pageCount}
			if facts.ExtractableText.State == FactUnknown {
				facts.ExtractableText = TextFacts{State: FactPresent, PagesWithText: 1}
			}
			inspector := &readTestInspector{facts: facts}
			extractor := &readTestExtractor{}
			renderer := &readTestRenderer{}
			snapshot, report := readWithWorkers(
				t.Context(), inputPath,
				ReadOptions{Acquire: AcquireOptions{ScratchRoot: filepath.Join(root, "protected")}, Pages: []int{1}},
				"linux", "amd64", test.operation, inspector, extractor, renderer,
			)
			if snapshot != nil || report.Failure == nil || report.Failure.Code != test.code ||
				extractor.called != 0 || renderer.called != 0 {
				t.Fatalf(
					"report=%#v snapshot=%#v extract=%d render=%d",
					report,
					snapshot,
					extractor.called,
					renderer.called,
				)
			}
		})
	}
}

type readTestInspector struct {
	facts  *InspectionFacts
	called int
}

func (worker *readTestInspector) Inspect(
	_ context.Context,
	_ *Snapshot,
	input DocumentRef,
	limits Limits,
) WorkerResult {
	worker.called++
	request := newWorkerOperationRequest(input, limits, workerOperationInspect)
	return WorkerResult{
		SchemaVersion: WorkerResultSchemaVersion,
		OperationID:   request.OperationID,
		State:         StateSucceeded,
		Input:         &request.Input,
		Inspection:    worker.facts,
	}
}

type readTestExtractor struct {
	called int
	input  DocumentRef
}

func (worker *readTestExtractor) Extract(
	_ context.Context,
	_ *Snapshot,
	input DocumentRef,
	limits Limits,
	read WorkerReadRequest,
) WorkerResult {
	worker.called++
	worker.input = input
	request := newReadWorkerRequest(input, limits, workerOperationExtract, read)
	content := []byte(`{"page":1,"text":"marker"}` + "\n")
	digest := sha256.Sum256(content)
	descriptor := Artifact{
		Ref:  workerArtifactRef(request.OperationID, "extracted-text.jsonl"),
		Kind: "extracted_text", ContentType: "application/x-ndjson", Size: int64(len(content)),
		SHA256: hex.EncodeToString(digest[:]), SourceSHA256: input.SHA256, Pages: append([]int(nil), read.Pages...),
	}
	return WorkerResult{
		SchemaVersion: WorkerResultSchemaVersion, OperationID: request.OperationID, State: StateSucceeded,
		Input: &request.Input,
		Extraction: &ExtractionFacts{
			Backend: popplerIdentity(), SelectedPages: append([]int(nil), read.Pages...),
			Pages: []PageTextFacts{{Page: read.Pages[0], Characters: 6}}, TotalCharacters: 6,
		},
		Artifacts: []WorkerArtifact{{Name: "extracted-text.jsonl", Artifact: descriptor}},
	}
}

type readTestRenderer struct{ called int }

func (worker *readTestRenderer) Render(
	context.Context,
	*Snapshot,
	DocumentRef,
	Limits,
	WorkerReadRequest,
) WorkerResult {
	worker.called++
	return WorkerResult{}
}

func intPointer(value int) *int { return &value }

func mustMarshalReport(t *testing.T, report Report) string {
	t.Helper()
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
