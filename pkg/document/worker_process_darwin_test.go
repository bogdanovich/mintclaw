//go:build darwin && (amd64 || arm64)

package document

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

const portableWorkerSecretCanary = "MINTCLAW_PORTABLE_WORKER_SECRET_CANARY"

func TestPortableProcessAcquireInspectExtractAndRender(t *testing.T) {
	t.Setenv(portableWorkerSecretCanary, "must-not-reach-worker")
	worker := portableTestProcessWorker("serve")

	t.Run("acquire inspect extract", func(t *testing.T) {
		snapshot, input := portableAcquiredFixture(t, worker, "text.pdf")
		inspection := worker.Inspect(t.Context(), snapshot, input, defaultInspectionLimits())
		if inspection.State != StateSucceeded || inspection.Inspection == nil ||
			inspection.Inspection.ExtractableText.State != FactPresent {
			t.Fatalf("portable inspection = %#v", inspection)
		}
		result := worker.Extract(
			t.Context(),
			snapshot,
			input,
			defaultInspectionLimits(),
			WorkerReadRequest{Pages: []int{1}, Limits: defaultReadLimits(workerOperationExtract)},
		)
		if result.State != StateSucceeded || result.Extraction == nil || len(result.Artifacts) != 1 ||
			result.Extraction.Backend != pdfiumWASMIdentity() {
			t.Fatalf("portable extraction = %#v", result)
		}
		artifact, err := snapshot.OpenArtifact(result.Artifacts[0].Artifact.Ref)
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(artifact)
		_ = artifact.Close()
		if readErr != nil || !bytes.Contains(data, []byte("MintClaw text fixture")) ||
			bytes.Contains(data, []byte(snapshot.path)) {
			t.Fatalf("portable extracted artifact = %q, err=%v", data, readErr)
		}
		if err = snapshot.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("render", func(t *testing.T) {
		snapshot, input := portableAcquiredFixture(t, worker, "image-only.pdf")
		result := worker.Render(
			t.Context(),
			snapshot,
			input,
			defaultInspectionLimits(),
			WorkerReadRequest{Pages: []int{1}, Limits: defaultReadLimits(workerOperationRender)},
		)
		if result.State != StateSucceeded || result.Rendering == nil || len(result.Artifacts) != 1 ||
			result.Rendering.Backend != pdfiumWASMIdentity() ||
			result.Rendering.Pages[0].Width != 1_224 || result.Rendering.Pages[0].Height != 1_584 {
			t.Fatalf("portable rendering state=%q failure=%+v rendering=%+v artifacts=%+v", result.State,
				result.Failure, result.Rendering, result.Artifacts)
		}
		artifact, err := snapshot.OpenArtifact(result.Artifacts[0].Artifact.Ref)
		if err != nil {
			t.Fatal(err)
		}
		header := make([]byte, 8)
		_, readErr := io.ReadFull(artifact, header)
		_ = artifact.Close()
		if readErr != nil || !bytes.Equal(header, []byte("\x89PNG\r\n\x1a\n")) {
			t.Fatalf("portable rendered header = %x, err=%v", header, readErr)
		}
		if err = snapshot.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestPortableProcessMalformedInputFailsWithoutCrashingParent(t *testing.T) {
	worker := portableTestProcessWorker("serve")
	snapshot, input := portableAcquiredFixture(t, worker, "truncated.pdf")
	result := worker.Inspect(t.Context(), snapshot, input, defaultInspectionLimits())
	if result.State != StateFailed || result.Failure == nil || result.Failure.Code != FailureMalformedPDF {
		t.Fatalf("portable malformed result state=%q failure=%+v", result.State, result.Failure)
	}

	validSnapshot, validInput := portableAcquiredFixture(t, worker, "text.pdf")
	valid := worker.Inspect(t.Context(), validSnapshot, validInput, defaultInspectionLimits())
	if valid.State != StateSucceeded || valid.Inspection == nil {
		t.Fatalf("portable worker after malformed input = %#v", valid)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validSnapshot.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPortableProcessCancellationTerminatesWorker(t *testing.T) {
	worker := portableTestProcessWorker("hang")
	worker.timeout = time.Minute
	snapshot, input := portableAcquiredFixture(t, portableTestProcessWorker("serve"), "text.pdf")
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan WorkerResult, 1)
	go func() {
		result <- worker.Verify(ctx, snapshot, input)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case terminal := <-result:
		assertWorkerFailure(t, terminal, StateCanceled, FailureCanceled)
	case <-time.After(5 * time.Second):
		t.Fatal("portable canceled worker did not terminate")
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPortableDocumentWorkerHelperProcess(t *testing.T) {
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		return
	}
	switch os.Args[separator+1] {
	case "serve":
		if os.Getenv(portableWorkerSecretCanary) != "" {
			os.Exit(91)
		}
		input := os.NewFile(WorkerInputFileDescriptor(), "document-snapshot")
		if input == nil {
			os.Exit(92)
		}
		defer func() { _ = input.Close() }()
		if err := ServeWorker(os.Stdin, input, os.Stdout); err != nil {
			os.Exit(93)
		}
		os.Exit(0)
	case "hang":
		time.Sleep(time.Minute)
	default:
		_, _ = fmt.Fprint(os.Stderr, "unknown portable worker helper mode")
		os.Exit(98)
	}
}

func portableTestProcessWorker(mode string) *processWorker {
	return &processWorker{
		executable: os.Args[0],
		args: []string{
			"-test.run=^TestPortableDocumentWorkerHelperProcess$",
			"--",
			mode,
		},
		timeout:   15 * time.Second,
		maxOutput: defaultWorkerOutputSize,
	}
}

func portableAcquiredFixture(t *testing.T, worker *processWorker, filename string) (*Snapshot, DocumentRef) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", filename))
	if err != nil {
		t.Fatal(err)
	}
	root := directTempDir(t)
	inputPath := filepath.Join(root, filename)
	writeFixture(t, inputPath, data)
	snapshot, report := acquireWithWorker(
		t.Context(),
		inputPath,
		AcquireOptions{ScratchRoot: filepath.Join(root, "protected")},
		"darwin",
		runtime.GOARCH,
		worker,
	)
	if snapshot == nil || report.State != StateSucceeded || report.Input == nil ||
		report.Input.Ref == "" || report.Input.Authority.Kind != "local_operator" {
		t.Fatalf("portable acquisition = %#v, snapshot=%#v", report, snapshot)
	}
	return snapshot, *report.Input
}
