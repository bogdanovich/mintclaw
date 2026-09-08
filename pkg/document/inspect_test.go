package document

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/media"
)

func TestInspectUsesImmutableSnapshotAndPreservesIdentity(t *testing.T) {
	root := directTempDir(t)
	inputPath := filepath.Join(root, "text.pdf")
	data, err := os.ReadFile(filepath.Join("testdata", "text.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, inputPath, data)
	inspector := &recordingInspector{facts: successfulTestInspection()}

	snapshot, report := inspectWithWorker(
		t.Context(),
		inputPath,
		AcquireOptions{ScratchRoot: filepath.Join(root, "protected"), MaxBytes: int64(len(data))},
		"linux",
		"amd64",
		inspector,
	)
	if snapshot == nil || report.State != StateSucceeded || report.Input == nil || report.Inspection == nil {
		t.Fatalf("report = %#v, snapshot = %#v", report, snapshot)
	}
	if inspector.snapshotPath != snapshot.Path() || inspector.input == nil || inspector.limits != report.Limits {
		t.Fatalf(
			"inspector input = %#v, path = %q, limits = %#v",
			inspector.input,
			inspector.snapshotPath,
			inspector.limits,
		)
	}
	digest := sha256.Sum256(data)
	if report.Operation != operationInspect || report.Input.SHA256 != hex.EncodeToString(digest[:]) ||
		report.Input.OriginalFilename != "text.pdf" {
		t.Fatalf("inspection identity = %#v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{root, inputPath, snapshot.Path(), "MintClaw text fixture"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("report leaked %q: %s", forbidden, encoded)
		}
	}
	if err = snapshot.Close(); err != nil {
		t.Fatalf("close snapshot: %v", err)
	}
	assertEmptyDirectory(t, filepath.Join(root, "protected"))
}

func TestInspectFailureRetainsIdentityAndCleansSnapshot(t *testing.T) {
	root := directTempDir(t)
	inputPath := filepath.Join(root, "malformed.pdf")
	data := []byte("%PDF-1.7\ntruncated")
	writeFixture(t, inputPath, data)
	inspector := &recordingInspector{
		state: StateFailed,
		failure: &Failure{
			Code: FailureMalformedPDF, Message: "unsafe backend detail /private/input.pdf",
		},
	}

	snapshot, report := inspectWithWorker(
		t.Context(), inputPath, AcquireOptions{ScratchRoot: filepath.Join(root, "protected")},
		"linux", "amd64", inspector,
	)
	if snapshot != nil || report.Input == nil {
		t.Fatalf("failed inspection ownership = snapshot %#v, input %#v", snapshot, report.Input)
	}
	assertFailureWithInput(t, report, StateFailed, FailureMalformedPDF)
	if strings.Contains(report.Failure.Message, "/private/input.pdf") {
		t.Fatalf("backend detail leaked into report: %#v", report.Failure)
	}
	assertEmptyDirectory(t, filepath.Join(root, "protected"))
}

func TestInspectFailsClosedOnInvalidWorkerResponse(t *testing.T) {
	root := directTempDir(t)
	inputPath := filepath.Join(root, "text.pdf")
	writeFixture(t, inputPath, []byte("%PDF-1.7\nsynthetic\n%%EOF\n"))
	inspector := &recordingInspector{state: StateSucceeded}

	snapshot, report := inspectWithWorker(
		t.Context(), inputPath, AcquireOptions{ScratchRoot: filepath.Join(root, "protected")},
		"linux", "amd64", inspector,
	)
	if snapshot != nil || report.Input == nil {
		t.Fatalf("invalid response ownership = snapshot %#v, input %#v", snapshot, report.Input)
	}
	assertFailureWithInput(t, report, StateFailed, FailureWorkerProtocol)
	assertEmptyDirectory(t, filepath.Join(root, "protected"))
}

func TestInspectUnsupportedPlatformDoesNotOpenInput(t *testing.T) {
	root := directTempDir(t)
	scratch := filepath.Join(root, "must-not-exist")
	inspector := &recordingInspector{facts: successfulTestInspection()}
	snapshot, report := inspectWithWorker(
		t.Context(), filepath.Join(root, "missing.pdf"), AcquireOptions{ScratchRoot: scratch},
		"darwin", "arm64", inspector,
	)
	if snapshot != nil || inspector.input != nil {
		t.Fatalf("unsupported platform reached inspection: %#v %#v", snapshot, inspector.input)
	}
	assertFailure(t, report, StateUnavailable, FailureUnsupportedPlatform)
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Fatalf("unsupported platform touched scratch: %v", err)
	}
}

func TestInspectMediaPreservesOwnerAuthorityAndDeniesMismatch(t *testing.T) {
	root := directTempDir(t)
	inputPath := filepath.Join(root, "inbound.pdf")
	data, err := os.ReadFile(filepath.Join("testdata", "text.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, inputPath, data)
	store := media.NewFileMediaStore()
	ref, err := store.Store(inputPath, media.MediaMeta{Filename: "inbound.pdf"}, "inbound")
	if err != nil {
		t.Fatal(err)
	}
	owner := testMediaOwner(t)
	if err = store.BindOwner(ref, owner); err != nil {
		t.Fatal(err)
	}
	inspector := &recordingInspector{facts: successfulTestInspection()}
	snapshot, report := inspectMediaWithWorker(
		t.Context(), store, ref, owner, AcquireOptions{ScratchRoot: filepath.Join(root, "protected")},
		"linux", "amd64", inspector,
	)
	if snapshot == nil || report.State != StateSucceeded || report.Input == nil || report.Inspection == nil {
		t.Fatalf("report = %#v, snapshot = %#v", report, snapshot)
	}
	if report.Input.SourceRef != ref || report.Input.Authority != documentAuthority(owner) {
		t.Fatalf("inspection authority = %#v", report.Input)
	}
	if err = snapshot.Close(); err != nil {
		t.Fatal(err)
	}

	mismatch := owner
	mismatch.RouteID += "-other"
	deniedInspector := &recordingInspector{facts: successfulTestInspection()}
	snapshot, report = inspectMediaWithWorker(
		t.Context(), store, ref, mismatch, AcquireOptions{ScratchRoot: filepath.Join(root, "denied")},
		"linux", "amd64", deniedInspector,
	)
	if snapshot != nil || deniedInspector.input != nil {
		t.Fatalf("authority mismatch reached inspector: %#v %#v", snapshot, deniedInspector.input)
	}
	assertFailure(t, report, StateDenied, FailureSourceUnauthorized)
}

func TestServeWorkerInspectionUsesVerifiedBytesAndRequestLimits(t *testing.T) {
	data := []byte("%PDF-1.7\nsynthetic inspection\n%%EOF\n")
	request := testWorkerRequest(data)
	request.Operation = workerOperationInspect
	request.Limits.MaxPages = 7
	requestBytes, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	backend := &recordingBackend{result: backendInspection{
		State: StateSucceeded, Facts: successfulTestInspection(),
	}}
	var output bytes.Buffer
	if err = serveWorkerWithBackend(
		bytes.NewReader(requestBytes),
		bytes.NewReader(data),
		&output,
		backend,
	); err != nil {
		t.Fatal(err)
	}
	result, err := decodeWorkerResult(output.Bytes(), request)
	if err != nil {
		t.Fatalf("decode result: %v\n%s", err, output.String())
	}
	if result.State != StateSucceeded || result.Inspection == nil ||
		!bytes.Equal(backend.data, data) || backend.limits != request.Limits {
		t.Fatalf("result = %#v, backend data = %q limits = %#v", result, backend.data, backend.limits)
	}
}

func TestWorkerRequestRejectsInvalidInspectionLimits(t *testing.T) {
	data := []byte("%PDF-1.7\n%%EOF\n")
	for _, mutate := range []func(*WorkerRequest){
		func(request *WorkerRequest) { request.Limits.MaxInputBytes = 0 },
		func(request *WorkerRequest) { request.Limits.MaxPages = DefaultMaxPages + 1 },
		func(request *WorkerRequest) { request.Limits.MaxContentBytes = DefaultMaxContentBytes + 1 },
		func(request *WorkerRequest) { request.Limits.MaxObjects = DefaultMaxObjects + 1 },
		func(request *WorkerRequest) { request.Limits.MaxRecursionDepth = DefaultMaxRecursionDepth + 1 },
		func(request *WorkerRequest) { request.Limits.MaxInputBytes = request.Input.Size - 1 },
	} {
		request := testWorkerRequest(data)
		request.Operation = workerOperationInspect
		mutate(&request)
		if err := validateWorkerRequest(request); err == nil {
			t.Fatalf("accepted invalid limits: %#v", request.Limits)
		}
	}
}

type recordingInspector struct {
	state        State
	facts        *InspectionFacts
	failure      *Failure
	input        *DocumentRef
	snapshotPath string
	limits       Limits
}

func (w *recordingInspector) Inspect(
	_ context.Context,
	snapshot *Snapshot,
	input DocumentRef,
	limits Limits,
) WorkerResult {
	w.input = &input
	w.snapshotPath = snapshot.Path()
	w.limits = limits
	state := w.state
	if state == "" && w.facts != nil {
		state = StateSucceeded
	}
	result := WorkerResult{
		SchemaVersion: WorkerResultSchemaVersion,
		OperationID:   strings.TrimPrefix(input.Ref, "document://local/"),
		State:         state,
		Inspection:    w.facts,
		Failure:       w.failure,
	}
	if state == StateSucceeded {
		expected := newWorkerOperationRequest(input, limits, workerOperationInspect).Input
		result.Input = &expected
	}
	return result
}

type recordingBackend struct {
	data   []byte
	limits Limits
	result backendInspection
}

func (b *recordingBackend) Inspect(reader io.ReadSeeker, limits Limits) backendInspection {
	b.limits = limits
	data, err := io.ReadAll(reader)
	if err != nil {
		return failedInspection(FailureInternal, "test backend read failed")
	}
	b.data = data
	return b.result
}

func successfulTestInspection() *InspectionFacts {
	return defaultInspectionFacts()
}

func assertFailureWithInput(t *testing.T, report Report, state State, code FailureCode) {
	t.Helper()
	if report.State != state || report.Input == nil || report.Failure == nil || report.Failure.Code != code {
		t.Fatalf("report = %#v, want retained input, state %q, code %q", report, state, code)
	}
}
