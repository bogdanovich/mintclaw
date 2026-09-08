package document

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	WorkerRequestSchemaVersion = "mintclaw.document_worker_request.v1"
	WorkerResultSchemaVersion  = "mintclaw.document_worker_result.v1"

	workerOperationVerify = "verify_snapshot"
	workerInputFD         = 3

	defaultWorkerTimeout    = 5 * time.Second
	defaultWorkerOutputSize = 16 * 1024
	maxWorkerRequestSize    = 16 * 1024
)

// WorkerInputFileDescriptor is the inherited descriptor used by the private CLI worker entrypoint.
// It is a descriptor, not a path, so the protocol cannot redirect the worker to another file.
func WorkerInputFileDescriptor() uintptr {
	return workerInputFD
}

var opaqueOperationID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

// WorkerInput is the complete path-free identity presented to the document worker.
type WorkerInput struct {
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
}

// WorkerRequest is the versioned, path-free request accepted by the private worker command.
type WorkerRequest struct {
	SchemaVersion string      `json:"schema_version"`
	OperationID   string      `json:"operation_id"`
	Operation     string      `json:"operation"`
	Input         WorkerInput `json:"input"`
}

// WorkerResult is the worker's bounded terminal response. It intentionally has no artifact path fields.
type WorkerResult struct {
	SchemaVersion string       `json:"schema_version"`
	OperationID   string       `json:"operation_id"`
	State         State        `json:"state"`
	Input         *WorkerInput `json:"input,omitempty"`
	Failure       *Failure     `json:"failure,omitempty"`
}

// Worker verifies one immutable snapshot outside the core process.
type Worker interface {
	Verify(context.Context, *Snapshot, DocumentRef) WorkerResult
}

// NewProcessWorker returns the short-lived worker used by production document acquisition.
// It launches the current MintClaw executable in its private document worker mode.
func NewProcessWorker() Worker {
	return newProcessWorker()
}

func (w *processWorker) Verify(ctx context.Context, snapshot *Snapshot, input DocumentRef) WorkerResult {
	request := newWorkerRequest(input)
	if err := validateWorkerRequest(request); err != nil {
		return workerFailure(request.OperationID, StateFailed, FailureWorkerProtocol, "invalid document worker request")
	}
	if snapshot == nil || strings.TrimSpace(snapshot.path) == "" || strings.TrimSpace(snapshot.dir) == "" {
		return workerFailure(
			request.OperationID,
			StateFailed,
			FailureWorkerProtocol,
			"immutable snapshot is unavailable",
		)
	}

	workerScratch, err := os.MkdirTemp(snapshot.dir, ".worker-")
	if err != nil {
		return workerFailure(
			request.OperationID,
			StateFailed,
			FailureInternal,
			"document worker scratch is unavailable",
		)
	}
	if err = os.Chmod(workerScratch, 0o700); err != nil {
		_ = os.RemoveAll(workerScratch)
		return workerFailure(
			request.OperationID,
			StateFailed,
			FailureInternal,
			"document worker scratch is unavailable",
		)
	}

	result := w.run(ctx, snapshot.path, workerScratch, request)
	if cleanupErr := os.RemoveAll(workerScratch); cleanupErr != nil {
		return workerFailure(
			request.OperationID,
			StateFailed,
			FailureInternal,
			"document worker scratch cleanup failed",
		)
	}
	return result
}

func newWorkerRequest(input DocumentRef) WorkerRequest {
	operationID := input.Ref
	if separator := strings.LastIndex(operationID, "/"); separator >= 0 {
		operationID = operationID[separator+1:]
	}
	return WorkerRequest{
		SchemaVersion: WorkerRequestSchemaVersion,
		OperationID:   operationID,
		Operation:     workerOperationVerify,
		Input: WorkerInput{
			ContentType: input.ContentType,
			Size:        input.Size,
			SHA256:      input.SHA256,
		},
	}
}

// ServeWorker handles exactly one private worker request using the snapshot inherited on file descriptor 3.
func ServeWorker(requestReader io.Reader, snapshotReader io.Reader, output io.Writer) error {
	request, err := decodeWorkerRequest(requestReader)
	if err != nil {
		return err
	}

	result := verifyWorkerSnapshot(request, snapshotReader)
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(result)
}

func verifyWorkerSnapshot(request WorkerRequest, snapshotReader io.Reader) WorkerResult {
	hash := sha256.New()
	read, err := io.Copy(hash, io.LimitReader(snapshotReader, request.Input.Size+1))
	if err != nil {
		return workerFailure(request.OperationID, StateFailed, FailureInternal, "immutable snapshot could not be read")
	}
	actualDigest := hex.EncodeToString(hash.Sum(nil))
	if read != request.Input.Size || actualDigest != request.Input.SHA256 {
		return workerFailure(
			request.OperationID,
			StateFailed,
			FailureWorkerInputMismatch,
			"immutable snapshot identity did not match the admitted input",
		)
	}
	return WorkerResult{
		SchemaVersion: WorkerResultSchemaVersion,
		OperationID:   request.OperationID,
		State:         StateSucceeded,
		Input:         &request.Input,
	}
}

func decodeWorkerRequest(reader io.Reader) (WorkerRequest, error) {
	var request WorkerRequest
	if err := decodeBoundedJSON(reader, maxWorkerRequestSize, &request); err != nil {
		return WorkerRequest{}, fmt.Errorf("decode document worker request: %w", err)
	}
	if err := validateWorkerRequest(request); err != nil {
		return WorkerRequest{}, err
	}
	return request, nil
}

func decodeWorkerResult(data []byte, request WorkerRequest) (WorkerResult, error) {
	var result WorkerResult
	if err := decodeBoundedJSON(strings.NewReader(string(data)), len(data), &result); err != nil {
		return WorkerResult{}, err
	}
	if result.SchemaVersion != WorkerResultSchemaVersion || result.OperationID != request.OperationID {
		return WorkerResult{}, errors.New("document worker response identity is invalid")
	}
	if result.State == StateSucceeded {
		if result.Input == nil || result.Failure != nil || *result.Input != request.Input {
			return WorkerResult{}, errors.New("document worker success response is invalid")
		}
		return result, nil
	}
	if result.Input != nil || !validWorkerFailure(result.State, result.Failure) {
		return WorkerResult{}, errors.New("document worker failure response is invalid")
	}
	return result, nil
}

func validWorkerFailure(state State, failure *Failure) bool {
	if failure == nil {
		return false
	}
	switch state {
	case StateCanceled:
		return failure.Code == FailureCanceled
	case StateUnavailable:
		return failure.Code == FailureWorkerUnavailable || failure.Code == FailureUnsupportedPlatform
	case StateFailed:
		switch failure.Code {
		case FailureInternal, FailureWorkerProtocol, FailureWorkerCrashed, FailureWorkerOutputLimit,
			FailureWorkerTimeout, FailureWorkerInputMismatch:
			return true
		}
	}
	return false
}

func safeWorkerFailure(result WorkerResult) Failure {
	if !validWorkerFailure(result.State, result.Failure) {
		return Failure{Code: FailureWorkerProtocol, Message: "document worker returned an invalid response"}
	}
	messages := map[FailureCode]string{
		FailureCanceled:            "document worker was canceled",
		FailureUnsupportedPlatform: "document worker is initially admitted only on linux/amd64",
		FailureWorkerUnavailable:   "document worker executable is unavailable",
		FailureInternal:            "document worker failed",
		FailureWorkerProtocol:      "document worker returned an invalid response",
		FailureWorkerCrashed:       "document worker terminated unexpectedly",
		FailureWorkerOutputLimit:   "document worker exceeded its output limit",
		FailureWorkerTimeout:       "document worker exceeded its runtime limit",
		FailureWorkerInputMismatch: "immutable snapshot identity did not match the admitted input",
	}
	return Failure{Code: result.Failure.Code, Message: messages[result.Failure.Code]}
}

func decodeBoundedJSON(reader io.Reader, maximum int, target any) error {
	if maximum <= 0 {
		return errors.New("JSON limit is invalid")
	}
	data, err := io.ReadAll(io.LimitReader(reader, int64(maximum)+1))
	if err != nil {
		return err
	}
	if len(data) > maximum {
		return errors.New("JSON exceeds limit")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON contains multiple values")
		}
		return err
	}
	return nil
}

func validateWorkerRequest(request WorkerRequest) error {
	if request.SchemaVersion != WorkerRequestSchemaVersion || request.Operation != workerOperationVerify ||
		!opaqueOperationID.MatchString(request.OperationID) {
		return errors.New("document worker request identity is invalid")
	}
	if request.Input.ContentType != "application/pdf" || request.Input.Size < 0 ||
		request.Input.Size > DefaultMaxInputBytes {
		return errors.New("document worker input metadata is invalid")
	}
	decodedDigest, err := hex.DecodeString(request.Input.SHA256)
	if err != nil || len(decodedDigest) != sha256.Size ||
		request.Input.SHA256 != strings.ToLower(request.Input.SHA256) {
		return errors.New("document worker digest is invalid")
	}
	return nil
}

func workerFailure(operationID string, state State, code FailureCode, message string) WorkerResult {
	return WorkerResult{
		SchemaVersion: WorkerResultSchemaVersion,
		OperationID:   operationID,
		State:         state,
		Failure:       &Failure{Code: code, Message: message},
	}
}
