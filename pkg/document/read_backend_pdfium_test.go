//go:build (linux && amd64) || (darwin && (amd64 || arm64))

package document

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPDFiumReadBackendMatchesPortableContract(t *testing.T) {
	tests := []struct {
		name          string
		fixtureID     string
		operation     string
		truncated     bool
		maxCharacters int
		width         int
		height        int
	}{
		{
			name: "text extraction", fixtureID: "text", operation: operationExtract,
		},
		{
			name: "unicode extraction", fixtureID: "unicode", operation: operationExtract,
		},
		{
			name: "backend-specific reading order", fixtureID: "ambiguous-reading-order",
			operation: operationExtract,
		},
		{
			name: "image-only extraction", fixtureID: "scan", operation: operationExtract,
		},
		{
			name: "character truncation", fixtureID: "character-limit", operation: operationExtract,
			truncated: true, maxCharacters: 64,
		},
		{
			name: "image render", fixtureID: "scan", operation: operationRender,
			width: 1_224, height: 1_584,
		},
		{
			name: "rotated crop render", fixtureID: "rotated-crop", operation: operationRender,
			width: 792, height: 612,
		},
		{
			name: "render dimension limit", fixtureID: "pixel-limit", operation: operationRender,
		},
	}
	fixtures := make(map[string]readFixture)
	for _, fixture := range loadReadFixtureManifest(t).Fixtures {
		fixtures[fixture.ID] = fixture
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, found := fixtures[test.fixtureID]
			if !found {
				t.Fatalf("portable fixture %q is absent from the read manifest", test.fixtureID)
			}
			data, err := os.ReadFile(filepath.Join("testdata", fixture.File))
			if err != nil {
				t.Fatal(err)
			}
			t.Chdir(t.TempDir())
			request := testWorkerRequest(data)
			request.Operation = test.operation
			limits := defaultReadLimits(test.operation)
			if test.maxCharacters > 0 {
				limits.MaxCharacters = test.maxCharacters
			}
			request.Read = &WorkerReadRequest{Pages: append([]int(nil), fixture.Pages...), Limits: limits}
			backend := newPortableReadBackend(newPortablePDFiumPool)
			var result backendRead
			if test.operation == operationExtract {
				result = backend.Extract(data, request)
			} else {
				result = backend.Render(data, request)
			}
			expectedState := fixture.ExpectedState
			expectedFailure := fixture.FailureCode
			if test.operation == operationExtract && fixture.ExtractFailureCode != "" {
				expectedState = StateUnsupported
				expectedFailure = fixture.ExtractFailureCode
			}
			if result.State != expectedState {
				t.Fatalf(
					"portable result state = %q: failure=%#v rendering=%#v",
					result.State,
					result.Failure,
					result.Rendering,
				)
			}
			if expectedFailure != "" {
				if result.Failure == nil || result.Failure.Code != expectedFailure || len(result.Artifacts) != 0 {
					t.Fatalf("portable failure = %#v artifacts=%#v", result.Failure, result.Artifacts)
				}
				return
			}
			workerResult := WorkerResult{
				SchemaVersion: WorkerResultSchemaVersion, OperationID: request.OperationID,
				State: result.State, Input: &request.Input, Extraction: result.Extraction,
				Rendering: result.Rendering, Artifacts: result.Artifacts, Failure: result.Failure,
			}
			if !validWorkerSuccessPayload(request, workerResult) {
				t.Fatalf("portable success payload = %#v", workerResult)
			}
			for _, artifact := range result.Artifacts {
				contents, readErr := os.ReadFile(artifact.Name)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if int64(len(contents)) != artifact.Artifact.Size {
					t.Fatalf("artifact bytes = %d, descriptor = %d", len(contents), artifact.Artifact.Size)
				}
				if test.operation == operationExtract {
					for _, marker := range fixture.Markers {
						if !bytes.Contains(contents, []byte(marker)) {
							t.Fatalf("artifact lacks marker %q", marker)
						}
					}
				}
			}
			if test.operation == operationExtract {
				if result.Extraction == nil || result.Extraction.Truncated != test.truncated ||
					result.Extraction.Backend != pdfiumWASMIdentity() {
					t.Fatalf("portable extraction = %#v", result.Extraction)
				}
				artifact, err := os.Open(result.Artifacts[0].Name)
				if err != nil {
					t.Fatal(err)
				}
				artifactData, readErr := io.ReadAll(artifact)
				_ = artifact.Close()
				if readErr != nil || !strings.Contains(string(artifactData), `"page":1`) {
					t.Fatalf("extraction artifact = %q, err=%v", artifactData, readErr)
				}
			} else if result.Rendering == nil || result.Rendering.Backend != pdfiumWASMIdentity() ||
				result.Rendering.Pages[0].Width != test.width || result.Rendering.Pages[0].Height != test.height {
				t.Fatalf("portable rendering = %#v", result.Rendering)
			}
		})
	}
}

func TestPDFiumReadBackendFailsClosedWithoutFactory(t *testing.T) {
	if backend := newPortableReadBackend(nil); backend != nil {
		t.Fatalf("portable backend without factory = %#v", backend)
	}
}
