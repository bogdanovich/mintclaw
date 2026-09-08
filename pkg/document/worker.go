package document

import (
	"bytes"
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

	workerOperationVerify  = "verify_snapshot"
	workerOperationInspect = "inspect"
	workerInputFD          = 3

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
	Limits        Limits      `json:"limits"`
}

// WorkerResult is the worker's bounded terminal response. It intentionally has no artifact path fields.
type WorkerResult struct {
	SchemaVersion string           `json:"schema_version"`
	OperationID   string           `json:"operation_id"`
	State         State            `json:"state"`
	Input         *WorkerInput     `json:"input,omitempty"`
	Inspection    *InspectionFacts `json:"inspection,omitempty"`
	Failure       *Failure         `json:"failure,omitempty"`
}

// Worker verifies one immutable snapshot outside the core process.
type Worker interface {
	Verify(context.Context, *Snapshot, DocumentRef) WorkerResult
}

// InspectorWorker inspects one immutable snapshot outside the core process.
type InspectorWorker interface {
	Inspect(context.Context, *Snapshot, DocumentRef, Limits) WorkerResult
}

// NewProcessWorker returns the short-lived worker used by production document acquisition.
// It launches the current MintClaw executable in its private document worker mode.
func NewProcessWorker() Worker {
	return newProcessWorker()
}

// NewProcessInspector returns the same one-shot worker with the bounded inspect operation selected.
func NewProcessInspector() InspectorWorker {
	return newProcessWorker()
}

func (w *processWorker) Verify(ctx context.Context, snapshot *Snapshot, input DocumentRef) WorkerResult {
	return w.runOperation(ctx, snapshot, input, defaultInspectionLimits(), workerOperationVerify)
}

func (w *processWorker) Inspect(
	ctx context.Context,
	snapshot *Snapshot,
	input DocumentRef,
	limits Limits,
) WorkerResult {
	return w.runOperation(ctx, snapshot, input, limits, workerOperationInspect)
}

func (w *processWorker) runOperation(
	ctx context.Context,
	snapshot *Snapshot,
	input DocumentRef,
	limits Limits,
	operation string,
) WorkerResult {
	request := newWorkerOperationRequest(input, limits, operation)
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
	return newWorkerOperationRequest(input, defaultInspectionLimits(), workerOperationVerify)
}

func newWorkerOperationRequest(input DocumentRef, limits Limits, operation string) WorkerRequest {
	operationID := input.Ref
	if separator := strings.LastIndex(operationID, "/"); separator >= 0 {
		operationID = operationID[separator+1:]
	}
	return WorkerRequest{
		SchemaVersion: WorkerRequestSchemaVersion,
		OperationID:   operationID,
		Operation:     operation,
		Input: WorkerInput{
			ContentType: input.ContentType,
			Size:        input.Size,
			SHA256:      input.SHA256,
		},
		Limits: limits,
	}
}

// ServeWorker handles exactly one private worker request using the snapshot inherited on file descriptor 3.
func ServeWorker(requestReader io.Reader, snapshotReader io.Reader, output io.Writer) error {
	return serveWorkerWithBackend(requestReader, snapshotReader, output, newInspectionBackend())
}

func serveWorkerWithBackend(
	requestReader io.Reader,
	snapshotReader io.Reader,
	output io.Writer,
	backend inspectionBackend,
) error {
	request, err := decodeWorkerRequest(requestReader)
	if err != nil {
		return err
	}

	data, result := verifyWorkerSnapshot(request, snapshotReader)
	if result.State == StateSucceeded && request.Operation == workerOperationInspect {
		if backend == nil {
			result = workerFailure(
				request.OperationID,
				StateUnavailable,
				FailureBackendUnavailable,
				"document inspection backend is unavailable",
			)
		} else {
			outcome := backend.Inspect(bytes.NewReader(data), request.Limits)
			result = WorkerResult{
				SchemaVersion: WorkerResultSchemaVersion,
				OperationID:   request.OperationID,
				State:         outcome.State,
				Input:         &request.Input,
				Inspection:    outcome.Facts,
				Failure:       outcome.Failure,
			}
		}
	}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(result)
}

func verifyWorkerSnapshot(request WorkerRequest, snapshotReader io.Reader) ([]byte, WorkerResult) {
	hash := sha256.New()
	var snapshot bytes.Buffer
	read, err := io.Copy(
		io.MultiWriter(hash, &snapshot),
		io.LimitReader(snapshotReader, request.Input.Size+1),
	)
	if err != nil {
		return nil, workerFailure(
			request.OperationID,
			StateFailed,
			FailureInternal,
			"immutable snapshot could not be read",
		)
	}
	actualDigest := hex.EncodeToString(hash.Sum(nil))
	if read != request.Input.Size || actualDigest != request.Input.SHA256 {
		return nil, workerFailure(
			request.OperationID,
			StateFailed,
			FailureWorkerInputMismatch,
			"immutable snapshot identity did not match the admitted input",
		)
	}
	return snapshot.Bytes(), WorkerResult{
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
		if result.Input == nil || result.Failure != nil || *result.Input != request.Input ||
			!validWorkerSuccessPayload(request.Operation, result.Inspection) {
			return WorkerResult{}, errors.New("document worker success response is invalid")
		}
		return result, nil
	}
	if !validWorkerFailure(result.State, result.Failure) {
		return WorkerResult{}, errors.New("document worker failure response is invalid")
	}
	if request.Operation == workerOperationVerify && (result.Input != nil || result.Inspection != nil) {
		return WorkerResult{}, errors.New("document worker verify failure response is invalid")
	}
	if request.Operation == workerOperationInspect {
		if result.Input != nil && *result.Input != request.Input {
			return WorkerResult{}, errors.New("document worker inspect failure input is invalid")
		}
		if result.Inspection != nil && !validInspectionFacts(*result.Inspection) {
			return WorkerResult{}, errors.New("document worker inspect failure facts are invalid")
		}
	}
	return result, nil
}

func validWorkerSuccessPayload(operation string, inspection *InspectionFacts) bool {
	switch operation {
	case workerOperationVerify:
		return inspection == nil
	case workerOperationInspect:
		return inspection != nil && validInspectionFacts(*inspection)
	default:
		return false
	}
}

func validWorkerFailure(state State, failure *Failure) bool {
	if failure == nil {
		return false
	}
	switch state {
	case StateCanceled:
		return failure.Code == FailureCanceled
	case StateUnavailable:
		return failure.Code == FailureWorkerUnavailable || failure.Code == FailureUnsupportedPlatform ||
			failure.Code == FailureBackendUnavailable
	case StateUnsupported:
		return failure.Code == FailurePasswordRequired
	case StateFailed:
		switch failure.Code {
		case FailureInternal, FailureWorkerProtocol, FailureWorkerCrashed, FailureWorkerOutputLimit,
			FailureWorkerTimeout, FailureWorkerInputMismatch, FailureMalformedPDF, FailureInspectionLimit:
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
		FailureMalformedPDF:        "PDF structure is malformed or unsupported",
		FailurePasswordRequired:    "document inspection requires a protected password input",
		FailureInspectionLimit:     "document exceeds an inspection limit",
		FailureBackendUnavailable:  "document inspection backend is unavailable",
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
	if request.SchemaVersion != WorkerRequestSchemaVersion ||
		(request.Operation != workerOperationVerify && request.Operation != workerOperationInspect) ||
		!opaqueOperationID.MatchString(request.OperationID) {
		return errors.New("document worker request identity is invalid")
	}
	if request.Input.ContentType != "application/pdf" || request.Input.Size < 0 ||
		!validWorkerLimits(request.Limits) || request.Input.Size > request.Limits.MaxInputBytes {
		return errors.New("document worker input metadata is invalid")
	}
	decodedDigest, err := hex.DecodeString(request.Input.SHA256)
	if err != nil || len(decodedDigest) != sha256.Size ||
		request.Input.SHA256 != strings.ToLower(request.Input.SHA256) {
		return errors.New("document worker digest is invalid")
	}
	return nil
}

func validWorkerLimits(limits Limits) bool {
	return limits.MaxInputBytes > 0 && limits.MaxInputBytes <= DefaultMaxInputBytes &&
		limits.MaxPages > 0 && limits.MaxPages <= DefaultMaxPages &&
		limits.MaxContentBytes > 0 && limits.MaxContentBytes <= DefaultMaxContentBytes &&
		limits.MaxObjects > 0 && limits.MaxObjects <= DefaultMaxObjects &&
		limits.MaxRecursionDepth > 0 && limits.MaxRecursionDepth <= DefaultMaxRecursionDepth
}

func defaultInspectionLimits() Limits {
	return Limits{
		MaxInputBytes:     DefaultMaxInputBytes,
		MaxPages:          DefaultMaxPages,
		MaxContentBytes:   DefaultMaxContentBytes,
		MaxObjects:        DefaultMaxObjects,
		MaxRecursionDepth: DefaultMaxRecursionDepth,
	}
}

func validInspectionFacts(facts InspectionFacts) bool {
	if facts.Backend.Name != PDFCPUBackendName || facts.Backend.Version != PDFCPUBackendVersion ||
		facts.Backend.Role != "production" {
		return false
	}
	states := []FactState{
		facts.PDFVersion.State,
		facts.PageCount.State,
		facts.Encryption.State,
		facts.Encryption.PasswordRequired,
		facts.Encryption.Permissions.State,
		facts.Signatures.State,
		facts.Signatures.Count.State,
		facts.Signatures.Certified,
		facts.Signatures.Timestamped,
		facts.Restrictions.State,
		facts.Restrictions.EncryptedPermissions,
		facts.Restrictions.DocMDP,
		facts.Restrictions.FieldMDP,
		facts.Restrictions.UsageRights,
		facts.Restrictions.ReaderExtensions,
		facts.AcroForm.State,
		facts.AcroForm.FieldCount.State,
		facts.XFA.State,
		facts.XFA.Representation.State,
		facts.XFA.Rendering.State,
		facts.ExtractableText.State,
	}
	for _, state := range states {
		if state != FactPresent && state != FactAbsent && state != FactMixed && state != FactUnknown {
			return false
		}
	}
	if !validEnumeratedStringFact(facts.PDFVersion, "1.0", "1.1", "1.2", "1.3", "1.4", "1.5", "1.6", "1.7", "2.0") ||
		!validEnumeratedStringFact(facts.Encryption.Permissions, "full", "restricted") ||
		!validEnumeratedStringFact(facts.XFA.Representation, "stream", "packet_array") ||
		!validEnumeratedStringFact(facts.XFA.Rendering, "dynamic", "static") ||
		!validIntegerFact(facts.PageCount, 1, DefaultMaxPages) ||
		!validIntegerFact(facts.Signatures.Count, 0, DefaultMaxPages*16) ||
		!validIntegerFact(facts.AcroForm.FieldCount, 0, DefaultMaxPages*10_000) {
		return false
	}
	if !validEncryptionFacts(facts.Encryption) || !validSignatureFacts(facts.Signatures) ||
		!validRestrictionFacts(facts.Restrictions) || !validFormFacts(facts.AcroForm, facts.XFA) ||
		!validTextFacts(facts.ExtractableText, facts.PageCount) {
		return false
	}
	for _, warning := range facts.Warnings {
		if warning != "acroform_field_count_unknown" && warning != "text_signal_uses_content_stream_operators" {
			return false
		}
	}
	return true
}

func validEnumeratedStringFact(fact StringFact, values ...string) bool {
	if fact.State != FactPresent {
		return fact.Value == ""
	}
	for _, value := range values {
		if fact.Value == value {
			return true
		}
	}
	return false
}

func validIntegerFact(fact IntegerFact, minimum, maximum int) bool {
	if fact.State != FactPresent {
		return fact.Value == nil
	}
	return fact.Value != nil && *fact.Value >= minimum && *fact.Value <= maximum
}

func validEncryptionFacts(facts EncryptionFacts) bool {
	switch facts.State {
	case FactAbsent:
		return facts.PasswordRequired == FactAbsent && facts.Permissions.State == FactAbsent
	case FactPresent:
		if facts.PasswordRequired == FactPresent {
			return facts.Permissions.State == FactUnknown
		}
		return facts.PasswordRequired == FactAbsent &&
			(facts.Permissions.State == FactPresent || facts.Permissions.State == FactUnknown)
	case FactUnknown:
		return facts.PasswordRequired == FactUnknown && facts.Permissions.State == FactUnknown
	default:
		return false
	}
}

func validSignatureFacts(facts SignatureFacts) bool {
	switch facts.State {
	case FactAbsent:
		return integerFactEquals(facts.Count, 0) && facts.Certified == FactAbsent && facts.Timestamped == FactAbsent
	case FactPresent:
		return facts.Count.Value != nil && *facts.Count.Value > 0 &&
			(facts.Certified == FactPresent || facts.Certified == FactAbsent || facts.Certified == FactUnknown) &&
			(facts.Timestamped == FactPresent || facts.Timestamped == FactAbsent || facts.Timestamped == FactUnknown)
	case FactUnknown:
		return facts.Count.State == FactUnknown && facts.Certified == FactUnknown && facts.Timestamped == FactUnknown
	default:
		return false
	}
}

func validRestrictionFacts(facts RestrictionFacts) bool {
	return facts.State == aggregatePresence(
		facts.EncryptedPermissions,
		facts.DocMDP,
		facts.FieldMDP,
		facts.UsageRights,
		facts.ReaderExtensions,
	)
}

func validFormFacts(acroForm AcroFormFacts, xfa XFAFacts) bool {
	switch acroForm.State {
	case FactAbsent:
		if !integerFactEquals(acroForm.FieldCount, 0) || xfa.State != FactAbsent {
			return false
		}
	case FactPresent:
		if acroForm.FieldCount.State != FactPresent && acroForm.FieldCount.State != FactUnknown {
			return false
		}
	case FactUnknown:
		if acroForm.FieldCount.State != FactUnknown || xfa.State != FactUnknown {
			return false
		}
	default:
		return false
	}
	switch xfa.State {
	case FactAbsent:
		return xfa.Representation.State == FactAbsent && xfa.Rendering.State == FactAbsent
	case FactPresent:
		return acroForm.State == FactPresent && xfa.Representation.State == FactPresent &&
			(xfa.Rendering.State == FactPresent || xfa.Rendering.State == FactUnknown)
	case FactUnknown:
		return xfa.Representation.State == FactUnknown && xfa.Rendering.State == FactUnknown
	default:
		return false
	}
}

func validTextFacts(facts TextFacts, pages IntegerFact) bool {
	if facts.PagesWithText < 0 || facts.PagesWithoutText < 0 || facts.PagesUnknown < 0 {
		return false
	}
	total := facts.PagesWithText + facts.PagesWithoutText + facts.PagesUnknown
	if pages.State == FactUnknown {
		return facts.State == FactUnknown && total == 0
	}
	if pages.Value == nil || total != *pages.Value {
		return false
	}
	return facts.State == textFactState(facts)
}

func integerFactEquals(fact IntegerFact, value int) bool {
	return fact.State == FactPresent && fact.Value != nil && *fact.Value == value
}

func workerFailure(operationID string, state State, code FailureCode, message string) WorkerResult {
	return WorkerResult{
		SchemaVersion: WorkerResultSchemaVersion,
		OperationID:   operationID,
		State:         state,
		Failure:       &Failure{Code: code, Message: message},
	}
}
