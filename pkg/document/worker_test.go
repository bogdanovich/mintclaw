package document

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkerRequestIsPathFreeAndMinimal(t *testing.T) {
	input := DocumentRef{
		Ref:              "document://local/document_operation_path_free",
		OriginalFilename: "secret-tax-form.pdf",
		ContentType:      "application/pdf",
		Size:             17,
		SHA256:           strings.Repeat("a", 64),
		Authority: Authority{
			Kind: "local_operator", WorkspaceID: "/private/workspace", ActorID: "secret-actor",
		},
		SourceKind:    "/private/source/path",
		CleanupPolicy: "delete_on_operation_close",
	}

	encoded, err := json.Marshal(newWorkerRequest(input))
	if err != nil {
		t.Fatalf("marshal worker request: %v", err)
	}
	for _, forbidden := range []string{
		input.OriginalFilename,
		input.Authority.WorkspaceID,
		input.Authority.ActorID,
		input.SourceKind,
		input.CleanupPolicy,
	} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("worker request leaked %q: %s", forbidden, encoded)
		}
	}
	if bytes.Contains(encoded, []byte(`"path"`)) || bytes.Contains(encoded, []byte(`"filename"`)) {
		t.Fatalf("worker request exposes a path-bearing field: %s", encoded)
	}
}

func TestServeWorkerVerifiesInheritedSnapshot(t *testing.T) {
	data := []byte("%PDF-1.7\nworker fixture\n%%EOF\n")
	request := testWorkerRequest(data)
	requestBytes, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var output bytes.Buffer
	if err = ServeWorker(bytes.NewReader(requestBytes), bytes.NewReader(data), &output); err != nil {
		t.Fatalf("serve worker: %v", err)
	}

	result, err := decodeWorkerResult(output.Bytes(), request)
	if err != nil {
		t.Fatalf("decode result: %v\n%s", err, output.String())
	}
	if result.State != StateSucceeded || result.Input == nil || *result.Input != request.Input {
		t.Fatalf("result = %#v", result)
	}
}

func TestServeWorkerRejectsSnapshotIdentityMismatch(t *testing.T) {
	request := testWorkerRequest([]byte("%PDF-1.7\nexpected\n"))
	requestBytes, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var output bytes.Buffer
	if err = ServeWorker(bytes.NewReader(requestBytes), strings.NewReader("%PDF-1.7\nchanged\n"), &output); err != nil {
		t.Fatalf("serve worker: %v", err)
	}

	result, err := decodeWorkerResult(output.Bytes(), request)
	if err != nil {
		t.Fatalf("decode result: %v", err)
	}
	assertWorkerFailure(t, result, StateFailed, FailureWorkerInputMismatch)
}

func TestWorkerProtocolRejectsUnboundedOrAmbiguousJSON(t *testing.T) {
	request := testWorkerRequest([]byte("%PDF-1.7\n%%EOF\n"))
	valid, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	tests := []struct {
		name string
		data []byte
	}{
		{name: "unknown field", data: bytes.Replace(valid, []byte("}"), []byte(",\"path\":\"/etc/passwd\"}"), 1)},
		{name: "trailing value", data: append(append([]byte{}, valid...), []byte(" {}")...)},
		{name: "oversized", data: bytes.Repeat([]byte("x"), maxWorkerRequestSize+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, decodeErr := decodeWorkerRequest(bytes.NewReader(test.data)); decodeErr == nil {
				t.Fatalf("decodeWorkerRequest accepted %s", test.name)
			}
		})
	}
}

func TestAcquireRequiresSuccessfulWorkerAndCleansFailure(t *testing.T) {
	root := directTempDir(t)
	inputPath := filepath.Join(root, "input.pdf")
	data := []byte("%PDF-1.7\nacquisition worker\n%%EOF\n")
	writeFixture(t, inputPath, data)
	scratch := filepath.Join(root, "protected")

	worker := &recordingWorker{result: workerFailure(
		"document_operation_replaced_below",
		StateFailed,
		FailureWorkerCrashed,
		"crash exposed /private/secret/path",
	)}
	snapshot, report := acquireWithWorker(
		t.Context(),
		inputPath,
		AcquireOptions{ScratchRoot: scratch},
		"linux",
		"amd64",
		worker,
	)
	if worker.input == nil || worker.snapshotPath == "" {
		t.Fatal("acquisition did not present its immutable snapshot to the worker")
	}
	if snapshot != nil {
		t.Fatal("failed worker retained snapshot ownership")
	}
	assertFailure(t, report, StateFailed, FailureWorkerCrashed)
	if strings.Contains(report.Failure.Message, "/private/secret/path") {
		t.Fatalf("worker failure leaked into report: %#v", report.Failure)
	}
	entries, err := os.ReadDir(scratch)
	if err != nil {
		t.Fatalf("read scratch: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed acquisition retained scratch: %+v", entries)
	}
}

type recordingWorker struct {
	result       WorkerResult
	input        *DocumentRef
	snapshotPath string
}

func (w *recordingWorker) Verify(_ context.Context, snapshot *Snapshot, input DocumentRef) WorkerResult {
	w.input = &input
	w.snapshotPath = snapshot.Path()
	result := w.result
	result.OperationID = strings.TrimPrefix(input.Ref, "document://local/")
	return result
}

func testWorkerRequest(data []byte) WorkerRequest {
	digest := sha256.Sum256(data)
	return WorkerRequest{
		SchemaVersion: WorkerRequestSchemaVersion,
		OperationID:   "document_operation_worker_test",
		Operation:     workerOperationVerify,
		Input: WorkerInput{
			ContentType: "application/pdf",
			Size:        int64(len(data)),
			SHA256:      hex.EncodeToString(digest[:]),
		},
	}
}

func assertWorkerFailure(t *testing.T, result WorkerResult, state State, code FailureCode) {
	t.Helper()
	if result.State != state || result.Input != nil || result.Failure == nil || result.Failure.Code != code {
		t.Fatalf("worker result = %#v, want state %q and code %q", result, state, code)
	}
}
