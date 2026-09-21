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
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	WorkerRequestSchemaVersion = "mintclaw.document_worker_request.v1"
	WorkerResultSchemaVersion  = "mintclaw.document_worker_result.v1"

	workerOperationVerify        = "verify_snapshot"
	workerOperationInspect       = "inspect"
	workerOperationExtract       = "extract"
	workerOperationRender        = "render"
	workerOperationFields        = "fields"
	workerOperationFillCandidate = "fill_candidate"
	workerInputFD                = 3

	defaultWorkerTimeout     = 5 * time.Second
	defaultReadWorkerTimeout = 30 * time.Second
	defaultWorkerOutputSize  = DefaultMaxFormReportBytes + 64*1024
	maxWorkerRequestSize     = 64 * 1024
	workerBackendConfigDir   = ".backend-config"
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
	SchemaVersion string                 `json:"schema_version"`
	OperationID   string                 `json:"operation_id"`
	Operation     string                 `json:"operation"`
	Input         WorkerInput            `json:"input"`
	Limits        Limits                 `json:"limits"`
	Read          *WorkerReadRequest     `json:"read,omitempty"`
	Fill          *NormalizedFillRequest `json:"fill,omitempty"`
}

type WorkerReadRequest struct {
	Pages  []int      `json:"pages"`
	Limits ReadLimits `json:"limits"`
}

// WorkerArtifact names one artifact relative to the worker scratch. It never carries a path.
type WorkerArtifact struct {
	Name     string   `json:"name"`
	Artifact Artifact `json:"artifact"`
}

// WorkerResult is the worker's bounded terminal response. It intentionally has no artifact path fields.
type WorkerResult struct {
	SchemaVersion string           `json:"schema_version"`
	OperationID   string           `json:"operation_id"`
	State         State            `json:"state"`
	Input         *WorkerInput     `json:"input,omitempty"`
	Inspection    *InspectionFacts `json:"inspection,omitempty"`
	Extraction    *ExtractionFacts `json:"extraction,omitempty"`
	Rendering     *RenderingFacts  `json:"rendering,omitempty"`
	Fields        *FormFieldsFacts `json:"fields,omitempty"`
	Write         *FormWriteFacts  `json:"write,omitempty"`
	Artifacts     []WorkerArtifact `json:"artifacts,omitempty"`
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

type ExtractorWorker interface {
	Extract(context.Context, *Snapshot, DocumentRef, Limits, WorkerReadRequest) WorkerResult
}

type RendererWorker interface {
	Render(context.Context, *Snapshot, DocumentRef, Limits, WorkerReadRequest) WorkerResult
}

type FormFieldsWorker interface {
	Fields(context.Context, *Snapshot, DocumentRef, Limits) WorkerResult
}

// FormWriterWorker creates a private, structurally verified fill candidate.
// The candidate remains owned by the snapshot and is not final-ready until a
// separate visual verifier admits it.
type FormWriterWorker interface {
	FillCandidate(
		context.Context,
		*Snapshot,
		DocumentRef,
		Limits,
		string,
		NormalizedFillRequest,
	) WorkerResult
}

// NewProcessWorker returns the short-lived worker used by production document acquisition.
// It launches the current MintClaw executable in its private document worker mode.
func NewProcessWorker() Worker {
	return newProcessWorker(defaultWorkerTimeout)
}

// NewProcessInspector returns the same one-shot worker with the bounded inspect operation selected.
func NewProcessInspector() InspectorWorker {
	return newProcessWorker(defaultWorkerTimeout)
}

func NewProcessExtractor() ExtractorWorker { return newProcessWorker(defaultReadWorkerTimeout) }

func NewProcessRenderer() RendererWorker { return newProcessWorker(defaultReadWorkerTimeout) }

func NewProcessFormFieldsWorker() FormFieldsWorker { return newProcessWorker(defaultWorkerTimeout) }

// NewProcessFormWriterWorker returns the one-shot private form candidate worker.
func NewProcessFormWriterWorker() FormWriterWorker { return newProcessWorker(defaultReadWorkerTimeout) }

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

func (w *processWorker) Extract(
	ctx context.Context,
	snapshot *Snapshot,
	input DocumentRef,
	limits Limits,
	read WorkerReadRequest,
) WorkerResult {
	return w.runReadOperation(ctx, snapshot, input, limits, workerOperationExtract, read)
}

func (w *processWorker) Render(
	ctx context.Context,
	snapshot *Snapshot,
	input DocumentRef,
	limits Limits,
	read WorkerReadRequest,
) WorkerResult {
	return w.runReadOperation(ctx, snapshot, input, limits, workerOperationRender, read)
}

func (w *processWorker) Fields(
	ctx context.Context,
	snapshot *Snapshot,
	input DocumentRef,
	limits Limits,
) WorkerResult {
	return w.runOperation(ctx, snapshot, input, limits, workerOperationFields)
}

func (w *processWorker) FillCandidate(
	ctx context.Context,
	snapshot *Snapshot,
	input DocumentRef,
	limits Limits,
	operationID string,
	fill NormalizedFillRequest,
) WorkerResult {
	request := newWorkerOperationRequest(input, limits, workerOperationFillCandidate)
	request.OperationID = operationID
	request.Fill = &fill
	return w.runRequest(ctx, snapshot, request)
}

func (w *processWorker) runReadOperation(
	ctx context.Context,
	snapshot *Snapshot,
	input DocumentRef,
	limits Limits,
	operation string,
	read WorkerReadRequest,
) WorkerResult {
	request := newWorkerOperationRequest(input, limits, operation)
	request.Read = &read
	return w.runRequest(ctx, snapshot, request)
}

func (w *processWorker) runOperation(
	ctx context.Context,
	snapshot *Snapshot,
	input DocumentRef,
	limits Limits,
	operation string,
) WorkerResult {
	request := newWorkerOperationRequest(input, limits, operation)
	return w.runRequest(ctx, snapshot, request)
}

func (w *processWorker) runRequest(ctx context.Context, snapshot *Snapshot, request WorkerRequest) WorkerResult {
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
	if result.State == StateSucceeded && workerOperationHasArtifacts(request.Operation) {
		if err = adoptWorkerArtifacts(snapshot, workerScratch, request, &result); err != nil {
			result = workerFailure(
				request.OperationID,
				StateFailed,
				FailureArtifactInvalid,
				"document worker artifact validation failed",
			)
		}
	}
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
	return serveWorkerWithAllBackends(
		requestReader,
		snapshotReader,
		output,
		newInspectionBackend(),
		newReadBackend(),
		newFormFieldsBackend(),
		newFormWriteBackend(),
	)
}

func serveWorkerWithBackend(
	requestReader io.Reader,
	snapshotReader io.Reader,
	output io.Writer,
	backend inspectionBackend,
) error {
	return serveWorkerWithAllBackends(
		requestReader,
		snapshotReader,
		output,
		backend,
		newReadBackend(),
		newFormFieldsBackend(),
		newFormWriteBackend(),
	)
}

func serveWorkerWithAllBackends(
	requestReader io.Reader,
	snapshotReader io.Reader,
	output io.Writer,
	inspection inspectionBackend,
	reader readBackend,
	formFields formFieldsBackend,
	formWriter formWriteBackend,
) error {
	request, err := decodeWorkerRequest(requestReader)
	if err != nil {
		return err
	}

	data, result := verifyWorkerSnapshot(request, snapshotReader)
	if result.State == StateSucceeded && request.Operation == workerOperationInspect {
		if inspection == nil {
			result = workerFailure(
				request.OperationID,
				StateUnavailable,
				FailureBackendUnavailable,
				"document inspection backend is unavailable",
			)
		} else {
			outcome := inspection.Inspect(bytes.NewReader(data), request.Limits)
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
	if result.State == StateSucceeded &&
		(request.Operation == workerOperationExtract || request.Operation == workerOperationRender) {
		if reader == nil {
			result = workerFailure(
				request.OperationID,
				StateUnavailable,
				FailureBackendUnavailable,
				"document read backend is unavailable",
			)
		} else {
			var outcome backendRead
			if request.Operation == workerOperationExtract {
				outcome = reader.Extract(data, request)
			} else {
				outcome = reader.Render(data, request)
			}
			result = WorkerResult{
				SchemaVersion: WorkerResultSchemaVersion,
				OperationID:   request.OperationID,
				State:         outcome.State,
				Input:         &request.Input,
				Extraction:    outcome.Extraction,
				Rendering:     outcome.Rendering,
				Artifacts:     outcome.Artifacts,
				Failure:       outcome.Failure,
			}
		}
	}
	if result.State == StateSucceeded && request.Operation == workerOperationFields {
		result = serveWorkerFields(request, data, inspection, formFields)
	}
	if result.State == StateSucceeded && request.Operation == workerOperationFillCandidate {
		result = serveWorkerFillCandidate(request, data, formWriter)
	}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(result)
}

func serveWorkerFillCandidate(
	request WorkerRequest,
	data []byte,
	backend formWriteBackend,
) WorkerResult {
	if backend == nil {
		return workerFailure(
			request.OperationID,
			StateUnavailable,
			FailureBackendUnavailable,
			"document form writer is unavailable",
		)
	}
	backendConfig, err := isolatedWorkerBackendConfigPath()
	if err != nil {
		return workerFailure(
			request.OperationID,
			StateUnavailable,
			FailureBackendUnavailable,
			"document form writer isolation is unavailable",
		)
	}
	outcome := backend.Fill(data, request)
	if err = os.RemoveAll(backendConfig); err != nil {
		return workerFailure(
			request.OperationID,
			StateFailed,
			FailureInternal,
			"document form backend cleanup failed",
		)
	}
	result := WorkerResult{
		SchemaVersion: WorkerResultSchemaVersion,
		OperationID:   request.OperationID,
		State:         outcome.State,
		Input:         &request.Input,
		Write:         outcome.Facts,
		Artifacts:     outcome.Artifacts,
		Failure:       outcome.Failure,
	}
	if outcome.State != StateSucceeded {
		return result
	}
	if len(outcome.Candidate) == 0 || len(outcome.Artifacts) != 1 ||
		outcome.Artifacts[0].Name != filledCandidateArtifactName {
		return workerFailure(
			request.OperationID,
			StateFailed,
			FailureWorkerProtocol,
			"document form writer returned an invalid candidate",
		)
	}
	file, err := os.OpenFile(filledCandidateArtifactName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return workerFailure(
			request.OperationID,
			StateFailed,
			FailureWriteFailed,
			"document form candidate could not be written",
		)
	}
	written, writeErr := file.Write(outcome.Candidate)
	closeErr := file.Close()
	if writeErr != nil || written != len(outcome.Candidate) || closeErr != nil {
		_ = os.Remove(filledCandidateArtifactName)
		return workerFailure(
			request.OperationID,
			StateFailed,
			FailureWriteFailed,
			"document form candidate could not be written",
		)
	}
	return result
}

func isolatedWorkerBackendConfigPath() (string, error) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return "", err
	}
	expected := filepath.Join(workingDirectory, workerBackendConfigDir)
	configured := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(configured) || filepath.Clean(configured) != filepath.Clean(expected) {
		return "", errors.New("document worker backend config is not isolated")
	}
	return expected, nil
}

func serveWorkerFields(
	request WorkerRequest,
	data []byte,
	inspection inspectionBackend,
	formFields formFieldsBackend,
) WorkerResult {
	if inspection == nil || formFields == nil {
		return workerFailure(
			request.OperationID,
			StateUnavailable,
			FailureBackendUnavailable,
			"document form backend is unavailable",
		)
	}
	inspectionOutcome := inspection.Inspect(bytes.NewReader(data), request.Limits)
	if inspectionOutcome.State != StateSucceeded || inspectionOutcome.Facts == nil {
		return WorkerResult{
			SchemaVersion: WorkerResultSchemaVersion,
			OperationID:   request.OperationID,
			State:         inspectionOutcome.State,
			Input:         &request.Input,
			Inspection:    inspectionOutcome.Facts,
			Failure:       inspectionOutcome.Failure,
		}
	}
	if failure := formDiscoveryInspectionFailure(*inspectionOutcome.Facts); failure != nil {
		return WorkerResult{
			SchemaVersion: WorkerResultSchemaVersion,
			OperationID:   request.OperationID,
			State:         failureState(failure.Code),
			Input:         &request.Input,
			Inspection:    inspectionOutcome.Facts,
			Failure:       failure,
		}
	}
	fieldsOutcome := formFields.Fields(bytes.NewReader(data), request.Limits, request.Input.SHA256)
	return WorkerResult{
		SchemaVersion: WorkerResultSchemaVersion,
		OperationID:   request.OperationID,
		State:         fieldsOutcome.State,
		Input:         &request.Input,
		Inspection:    inspectionOutcome.Facts,
		Fields:        fieldsOutcome.Facts,
		Failure:       fieldsOutcome.Failure,
	}
}

func formDiscoveryInspectionFailure(facts InspectionFacts) *Failure {
	if hybridFormDiscoveryEligible(facts) {
		return nil
	}
	return ordinaryFormDiscoveryFailure(facts)
}

func ordinaryFormDiscoveryFailure(facts InspectionFacts) *Failure {
	if facts.Encryption.State != FactAbsent || facts.Encryption.PasswordRequired != FactAbsent {
		return &Failure{Code: FailureFormUnsupported, Message: "encrypted PDF forms are unsupported"}
	}
	if facts.Signatures.State != FactAbsent || facts.Restrictions.State != FactAbsent {
		return &Failure{Code: FailureFormUnsupported, Message: "signed or restricted PDF forms are unsupported"}
	}
	if facts.XFA.State != FactAbsent {
		return &Failure{Code: FailureFormUnsupported, Message: "XFA PDF forms are unsupported"}
	}
	if facts.AcroForm.State != FactPresent {
		return &Failure{Code: FailureFormNotPresent, Message: "PDF has no AcroForm fields"}
	}
	if facts.AcroForm.FieldCount.State != FactPresent || facts.AcroForm.FieldCount.Value == nil ||
		*facts.AcroForm.FieldCount.Value == 0 {
		return &Failure{Code: FailureFieldUnsupported, Message: "PDF contains unsupported form fields"}
	}
	return nil
}

func hybridFormDiscoveryEligible(facts InspectionFacts) bool {
	return facts.XFA.State == FactPresent && facts.HybridForm.State == FactPresent &&
		facts.HybridForm.Authority.State == FactPresent &&
		facts.HybridForm.Authority.Value == "acroform_fixed_pages" &&
		facts.HybridForm.NeedsRendering == FactAbsent && facts.HybridForm.XMLParsed == FactPresent &&
		facts.HybridForm.RepeatingSubforms == FactAbsent && facts.HybridForm.PageGrowth == FactAbsent &&
		facts.Encryption.PasswordRequired == FactAbsent &&
		facts.Encryption.OperationPermissions.Print == PermissionAllowed &&
		facts.Encryption.OperationPermissions.FormFill == PermissionAllowed &&
		facts.Signatures.Content.State == FactAbsent && facts.Signatures.Certified == FactAbsent &&
		facts.Signatures.Timestamped == FactAbsent && facts.Restrictions.DocMDP == FactAbsent &&
		facts.Restrictions.FieldMDP == FactAbsent && facts.Restrictions.ReaderExtensions == FactAbsent &&
		facts.Actions.CalculationOrder == FactAbsent && facts.AcroForm.State == FactPresent &&
		facts.AcroForm.FieldCount.State == FactPresent && facts.AcroForm.FieldCount.Value != nil &&
		*facts.AcroForm.FieldCount.Value > 0
}

func formWriteAdmissionFailure(facts InspectionFacts) *Failure {
	if facts.XFA.State == FactPresent {
		if hybridFormWriteEligible(facts) {
			return nil
		}
		return &Failure{
			Code: FailureFormUnsupported, Message: "hybrid PDF form is not safe for print-ready transformation",
		}
	}
	return ordinaryFormDiscoveryFailure(facts)
}

func hybridFormWriteEligible(facts InspectionFacts) bool {
	return hybridFormDiscoveryEligible(facts) && facts.HybridForm.DataConnections == FactAbsent &&
		facts.Actions.State == FactAbsent &&
		facts.Actions.JavaScript == FactAbsent && facts.Actions.SubmitForm == FactAbsent &&
		facts.Actions.Launch == FactAbsent && facts.Actions.ExternalNavigation == FactAbsent &&
		facts.Actions.OpenAction == FactAbsent && facts.Actions.AdditionalActions == FactAbsent
}

func formDiscoveryInspectionEligibility(facts InspectionFacts) FormEligibilityFacts {
	if formDiscoveryInspectionFailure(facts) == nil {
		mode := FormEligibilityOrdinary
		if facts.XFA.State == FactPresent {
			mode = FormEligibilityHybridDiscovery
			if hybridFormWriteEligible(facts) {
				mode = FormEligibilityHybridPrintReady
			}
		}
		return FormEligibilityFacts{State: FormEligible, Mode: mode}
	}
	blockers := make([]FormBlocker, 0, 11)
	appendBlocker := func(code FormBlockerCode, state FactState) {
		if state != FactAbsent {
			blockers = append(blockers, FormBlocker{Code: code, State: state})
		}
	}
	appendBlocker(FormBlockerEncryption, facts.Encryption.State)
	appendBlocker(FormBlockerPasswordRequired, facts.Encryption.PasswordRequired)
	appendBlocker(FormBlockerSignature, facts.Signatures.Content.State)
	appendBlocker(FormBlockerEncryptedPermissions, facts.Restrictions.EncryptedPermissions)
	if facts.Encryption.OperationPermissions.Print != PermissionAllowed {
		blockers = append(blockers, FormBlocker{
			Code: FormBlockerPrintPermission, Permission: facts.Encryption.OperationPermissions.Print,
		})
	}
	if facts.Encryption.OperationPermissions.FormFill != PermissionAllowed {
		blockers = append(blockers, FormBlocker{
			Code: FormBlockerFormFillPermission, Permission: facts.Encryption.OperationPermissions.FormFill,
		})
	}
	appendBlocker(FormBlockerDocMDP, facts.Restrictions.DocMDP)
	appendBlocker(FormBlockerFieldMDP, facts.Restrictions.FieldMDP)
	appendBlocker(FormBlockerUsageRights, facts.Restrictions.UsageRights)
	appendBlocker(FormBlockerReaderExtensions, facts.Restrictions.ReaderExtensions)
	appendBlocker(FormBlockerXFA, facts.XFA.State)
	if facts.XFA.State == FactPresent &&
		(facts.HybridForm.Authority.State != FactPresent ||
			facts.HybridForm.Authority.Value != "acroform_fixed_pages") {
		state := facts.HybridForm.Authority.State
		if state == FactAbsent {
			state = FactUnknown
		}
		blockers = append(blockers, FormBlocker{Code: FormBlockerHybridAuthority, State: state})
	}
	appendBlocker(FormBlockerNeedsRendering, facts.HybridForm.NeedsRendering)
	if facts.AcroForm.State != FactPresent {
		blockers = append(blockers, FormBlocker{Code: FormBlockerAcroForm, State: facts.AcroForm.State})
	} else if facts.AcroForm.FieldCount.State != FactPresent || facts.AcroForm.FieldCount.Value == nil ||
		*facts.AcroForm.FieldCount.Value == 0 {
		blockers = append(blockers, FormBlocker{
			Code: FormBlockerAcroFormFields, State: facts.AcroForm.FieldCount.State,
		})
	}
	return FormEligibilityFacts{State: FormBlocked, Blockers: blockers}
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
			!validWorkerSuccessPayload(request, result) {
			return WorkerResult{}, errors.New("document worker success response is invalid")
		}
		return result, nil
	}
	if !validWorkerFailure(result.State, result.Failure) {
		return WorkerResult{}, errors.New("document worker failure response is invalid")
	}
	if request.Operation == workerOperationVerify &&
		(result.Input != nil || result.Inspection != nil || result.Extraction != nil || result.Rendering != nil ||
			result.Fields != nil || result.Write != nil ||
			len(result.Artifacts) != 0) {
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
	if request.Operation == workerOperationFields {
		if result.Input != nil && *result.Input != request.Input {
			return WorkerResult{}, errors.New("document worker fields failure input is invalid")
		}
		if result.Inspection != nil && !validInspectionFacts(*result.Inspection) {
			return WorkerResult{}, errors.New("document worker fields failure facts are invalid")
		}
		if result.Fields != nil || result.Extraction != nil || result.Rendering != nil || result.Write != nil ||
			len(result.Artifacts) != 0 {
			return WorkerResult{}, errors.New("document worker fields failure payload is invalid")
		}
	}
	if request.Operation == workerOperationExtract || request.Operation == workerOperationRender {
		if result.Input != nil && *result.Input != request.Input {
			return WorkerResult{}, errors.New("document worker read failure input is invalid")
		}
		if result.Extraction != nil || result.Rendering != nil || result.Write != nil || len(result.Artifacts) != 0 {
			return WorkerResult{}, errors.New("document worker read failure payload is invalid")
		}
	}
	if request.Operation == workerOperationFillCandidate {
		if result.Input != nil && *result.Input != request.Input {
			return WorkerResult{}, errors.New("document worker fill failure input is invalid")
		}
		if result.Inspection != nil || result.Extraction != nil || result.Rendering != nil || result.Fields != nil ||
			result.Write != nil || len(result.Artifacts) != 0 {
			return WorkerResult{}, errors.New("document worker fill failure payload is invalid")
		}
	}
	return result, nil
}

func validWorkerSuccessPayload(request WorkerRequest, result WorkerResult) bool {
	switch request.Operation {
	case workerOperationVerify:
		return result.Inspection == nil && result.Extraction == nil && result.Rendering == nil &&
			result.Fields == nil && result.Write == nil &&
			len(result.Artifacts) == 0
	case workerOperationInspect:
		return result.Inspection != nil && validInspectionFacts(*result.Inspection) &&
			result.Extraction == nil && result.Rendering == nil && result.Fields == nil && result.Write == nil &&
			len(result.Artifacts) == 0
	case workerOperationExtract:
		return result.Inspection == nil && result.Extraction != nil && result.Rendering == nil &&
			result.Fields == nil && result.Write == nil &&
			validExtractionFacts(request, *result.Extraction, result.Artifacts)
	case workerOperationRender:
		return result.Inspection == nil && result.Extraction == nil && result.Rendering != nil &&
			result.Fields == nil && result.Write == nil &&
			validRenderingFacts(request, *result.Rendering, result.Artifacts)
	case workerOperationFields:
		return result.Inspection != nil && validInspectionFacts(*result.Inspection) &&
			result.Extraction == nil && result.Rendering == nil && result.Fields != nil && result.Write == nil &&
			validFormFieldsFacts(*result.Fields) && result.Fields.SourceSHA256 == request.Input.SHA256 &&
			validFieldsAgainstInspection(*result.Fields, *result.Inspection) &&
			len(result.Artifacts) == 0
	case workerOperationFillCandidate:
		return result.Inspection == nil && result.Extraction == nil && result.Rendering == nil &&
			result.Fields == nil && result.Write != nil && len(result.Artifacts) == 1 &&
			validFormWriteFacts(request, *result.Write, result.Artifacts[0])
	default:
		return false
	}
}

func validFieldsAgainstInspection(fields FormFieldsFacts, inspection InspectionFacts) bool {
	return inspection.AcroForm.State == FactPresent && inspection.AcroForm.FieldCount.State == FactPresent &&
		inspection.AcroForm.FieldCount.Value != nil && *inspection.AcroForm.FieldCount.Value == len(fields.Fields) &&
		formDiscoveryInspectionFailure(inspection) == nil
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
		return failure.Code == FailurePasswordRequired || failure.Code == FailureUnsupportedFeature ||
			failure.Code == FailureTextUnavailable || failure.Code == FailureVisionUnavailable ||
			failure.Code == FailureFormNotPresent || failure.Code == FailureFormUnsupported ||
			failure.Code == FailureFieldUnsupported || failure.Code == FailureAppearanceUnavailable
	case StateFailed:
		switch failure.Code {
		case FailureInternal, FailureWorkerProtocol, FailureWorkerCrashed, FailureWorkerOutputLimit,
			FailureWorkerTimeout, FailureWorkerInputMismatch, FailureMalformedPDF, FailureInspectionLimit,
			FailureExtractionLimit, FailureRenderLimit, FailureArtifactInvalid, FailureLimitExceeded,
			FailureFieldNotFound,
			FailureFieldAmbiguous, FailureFieldReadOnly, FailureFieldValueInvalid, FailureChoiceInvalid,
			FailureWriteFailed, FailureAppearanceStale, FailureContentClipped,
			FailureVerificationStructural, FailureVerificationVisual:
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
		FailureCanceled:               "document worker was canceled",
		FailureUnsupportedPlatform:    "document worker is initially admitted only on linux/amd64",
		FailureWorkerUnavailable:      "document worker executable is unavailable",
		FailureInternal:               "document worker failed",
		FailureWorkerProtocol:         "document worker returned an invalid response",
		FailureWorkerCrashed:          "document worker terminated unexpectedly",
		FailureWorkerOutputLimit:      "document worker exceeded its output limit",
		FailureWorkerTimeout:          "document worker exceeded its runtime limit",
		FailureWorkerInputMismatch:    "immutable snapshot identity did not match the admitted input",
		FailureMalformedPDF:           "PDF structure is malformed or unsupported",
		FailurePasswordRequired:       "document inspection requires a protected password input",
		FailureInspectionLimit:        "document exceeds an inspection limit",
		FailureBackendUnavailable:     "document backend is unavailable",
		FailureExtractionLimit:        "document extraction exceeded a limit",
		FailureRenderLimit:            "document rendering exceeded a limit",
		FailureArtifactInvalid:        "document worker artifact validation failed",
		FailureLimitExceeded:          "document operation exceeded a limit",
		FailureUnsupportedFeature:     "document feature is not supported",
		FailureTextUnavailable:        "selected document pages have no extractable text",
		FailureVisionUnavailable:      "document vision processing is unavailable",
		FailureFormNotPresent:         "PDF has no AcroForm fields",
		FailureFormUnsupported:        "PDF form is not supported",
		FailureFieldUnsupported:       "PDF contains unsupported form fields",
		FailureFieldNotFound:          "PDF form field was not found",
		FailureFieldAmbiguous:         "PDF form field is ambiguous",
		FailureFieldReadOnly:          "PDF form field is read-only",
		FailureFieldValueInvalid:      "PDF form field value is invalid",
		FailureChoiceInvalid:          "PDF form choice is invalid",
		FailureWriteFailed:            "document form candidate could not be written",
		FailureAppearanceUnavailable:  "document form appearance is unavailable",
		FailureAppearanceStale:        "document form appearance is stale",
		FailureContentClipped:         "document form content is clipped",
		FailureVerificationStructural: "document form candidate failed structural verification",
		FailureVerificationVisual:     "document form candidate failed visual verification",
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
		(request.Operation != workerOperationVerify && request.Operation != workerOperationInspect &&
			request.Operation != workerOperationExtract && request.Operation != workerOperationRender &&
			request.Operation != workerOperationFields && request.Operation != workerOperationFillCandidate) ||
		!opaqueOperationID.MatchString(request.OperationID) {
		return errors.New("document worker request identity is invalid")
	}
	if request.Input.ContentType != "application/pdf" || request.Input.Size < 0 ||
		!validWorkerLimits(request.Limits) || request.Input.Size > request.Limits.MaxInputBytes {
		return errors.New("document worker input metadata is invalid")
	}
	if request.Operation == workerOperationExtract || request.Operation == workerOperationRender {
		if request.Read == nil || !validWorkerReadRequest(request.Operation, *request.Read) {
			return errors.New("document worker read request is invalid")
		}
	} else if request.Read != nil {
		return errors.New("document worker read request is unexpected")
	}
	if request.Operation == workerOperationFillCandidate {
		if request.Fill == nil || !validWriteOperationID(request.OperationID) ||
			!validNormalizedFillRequest(*request.Fill) || request.Fill.SourceSHA256 != request.Input.SHA256 {
			return errors.New("document worker fill request is invalid")
		}
	} else if request.Fill != nil {
		return errors.New("document worker fill request is unexpected")
	}
	decodedDigest, err := hex.DecodeString(request.Input.SHA256)
	if err != nil || len(decodedDigest) != sha256.Size ||
		request.Input.SHA256 != strings.ToLower(request.Input.SHA256) {
		return errors.New("document worker digest is invalid")
	}
	return nil
}

func workerOperationHasArtifacts(operation string) bool {
	return operation == workerOperationExtract || operation == workerOperationRender ||
		operation == workerOperationFillCandidate
}

func validWorkerReadRequest(operation string, read WorkerReadRequest) bool {
	if len(read.Pages) == 0 || !validReadLimits(read.Limits) {
		return false
	}
	maximumPages := read.Limits.MaxPages
	if operation == workerOperationExtract && maximumPages > DefaultMaxExtractPages {
		return false
	}
	if operation == workerOperationRender && maximumPages > DefaultMaxRenderPages {
		return false
	}
	if len(read.Pages) > maximumPages {
		return false
	}
	previous := 0
	for _, page := range read.Pages {
		if page <= previous || page > DefaultMaxPages {
			return false
		}
		previous = page
	}
	return true
}

func validReadLimits(limits ReadLimits) bool {
	return limits.MaxPages > 0 && limits.MaxPages <= DefaultMaxExtractPages &&
		limits.MaxCharacters > 0 && limits.MaxCharacters <= DefaultMaxExtractChars &&
		limits.DPI > 0 && limits.DPI <= DefaultRenderDPI &&
		limits.MaxDimension > 0 && limits.MaxDimension <= HardMaxRenderEdge &&
		limits.MaxPixelsPerPage > 0 && limits.MaxPixelsPerPage <= DefaultMaxPixelsPerPage &&
		limits.MaxTotalPixels > 0 && limits.MaxTotalPixels <= DefaultMaxRenderPixels &&
		limits.MaxArtifactBytes > 0 && limits.MaxArtifactBytes <= DefaultMaxArtifactBytes
}

func defaultReadLimits(operation string) ReadLimits {
	maximumPages := DefaultMaxExtractPages
	if operation == workerOperationRender {
		maximumPages = DefaultMaxRenderPages
	}
	return ReadLimits{
		MaxPages: maximumPages, MaxCharacters: DefaultMaxExtractChars, DPI: DefaultRenderDPI,
		MaxDimension: DefaultMaxRenderEdge, MaxPixelsPerPage: DefaultMaxPixelsPerPage,
		MaxTotalPixels: DefaultMaxRenderPixels, MaxArtifactBytes: DefaultMaxArtifactBytes,
	}
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
		facts.Signatures.Content.State,
		facts.Signatures.Content.Count.State,
		facts.Signatures.UsageRights.State,
		facts.Signatures.UsageRights.Count.State,
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
		facts.Actions.State,
		facts.Actions.JavaScript,
		facts.Actions.SubmitForm,
		facts.Actions.Launch,
		facts.Actions.ExternalNavigation,
		facts.Actions.OpenAction,
		facts.Actions.AdditionalActions,
		facts.Actions.CalculationOrder,
		facts.HybridForm.State,
		facts.HybridForm.Authority.State,
		facts.HybridForm.NeedsRendering,
		facts.HybridForm.XMLParsed,
		facts.HybridForm.Scripts,
		facts.HybridForm.DataConnections,
		facts.HybridForm.RepeatingSubforms,
		facts.HybridForm.PageGrowth,
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
		!validEnumeratedStringFact(facts.HybridForm.Authority, "acroform_fixed_pages", "xfa_dynamic") ||
		!validIntegerFact(facts.PageCount, 1, DefaultMaxPages) ||
		!validIntegerFact(facts.Signatures.Count, 0, DefaultMaxPages*16) ||
		!validIntegerFact(facts.AcroForm.FieldCount, 0, DefaultMaxPages*10_000) {
		return false
	}
	if !validEncryptionFacts(facts.Encryption) || !validSignatureFacts(facts.Signatures) ||
		!validRestrictionFacts(facts.Restrictions) || !validFormFacts(facts.AcroForm, facts.XFA) ||
		!validActionFacts(facts.Actions) || !validHybridFormFacts(facts.AcroForm, facts.XFA, facts.HybridForm) ||
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
	if !validOperationPermissions(facts.OperationPermissions) {
		return false
	}
	switch facts.State {
	case FactAbsent:
		return facts.PasswordRequired == FactAbsent && facts.Permissions.State == FactAbsent &&
			facts.OperationPermissions == (OperationPermissionFacts{
				Print: PermissionAllowed, FormFill: PermissionAllowed, Modify: PermissionAllowed,
				Assemble: PermissionAllowed,
			})
	case FactPresent:
		if facts.PasswordRequired == FactPresent {
			return facts.Permissions.State == FactUnknown &&
				facts.OperationPermissions == unknownOperationPermissions()
		}
		if facts.PasswordRequired != FactAbsent ||
			(facts.Permissions.State != FactPresent && facts.Permissions.State != FactUnknown) {
			return false
		}
		if facts.Permissions.State == FactUnknown {
			return facts.OperationPermissions == unknownOperationPermissions()
		}
		return facts.OperationPermissions.Print != PermissionUnknown &&
			facts.OperationPermissions.FormFill != PermissionUnknown &&
			facts.OperationPermissions.Modify != PermissionUnknown &&
			facts.OperationPermissions.Assemble != PermissionUnknown
	case FactUnknown:
		return facts.PasswordRequired == FactUnknown && facts.Permissions.State == FactUnknown &&
			facts.OperationPermissions == unknownOperationPermissions()
	default:
		return false
	}
}

func validOperationPermissions(facts OperationPermissionFacts) bool {
	for _, decision := range []PermissionDecision{facts.Print, facts.FormFill, facts.Modify, facts.Assemble} {
		if decision != PermissionAllowed && decision != PermissionDenied && decision != PermissionUnknown {
			return false
		}
	}
	return true
}

func validSignatureFacts(facts SignatureFacts) bool {
	if !validSignatureClassFacts(facts.Content) || !validSignatureClassFacts(facts.UsageRights) {
		return false
	}
	switch facts.State {
	case FactAbsent:
		return integerFactEquals(facts.Count, 0) && integerFactEquals(facts.Content.Count, 0) &&
			integerFactEquals(facts.UsageRights.Count, 0) && facts.Certified == FactAbsent &&
			facts.Timestamped == FactAbsent
	case FactPresent:
		return facts.Count.Value != nil && facts.Content.Count.Value != nil && facts.UsageRights.Count.Value != nil &&
			*facts.Count.Value > 0 && *facts.Count.Value == *facts.Content.Count.Value+*facts.UsageRights.Count.Value &&
			(facts.Certified == FactPresent || facts.Certified == FactAbsent || facts.Certified == FactUnknown) &&
			(facts.Timestamped == FactPresent || facts.Timestamped == FactAbsent || facts.Timestamped == FactUnknown) &&
			(facts.Content.State == FactPresent || (facts.Certified == FactAbsent && facts.Timestamped == FactAbsent))
	case FactUnknown:
		return facts.Count.State == FactUnknown && facts.Content.State == FactUnknown &&
			facts.UsageRights.State == FactUnknown && facts.Certified == FactUnknown &&
			facts.Timestamped == FactUnknown
	default:
		return false
	}
}

func validSignatureClassFacts(facts SignatureClassFacts) bool {
	switch facts.State {
	case FactAbsent:
		return integerFactEquals(facts.Count, 0)
	case FactPresent:
		return facts.Count.State == FactPresent && facts.Count.Value != nil && *facts.Count.Value > 0
	case FactUnknown:
		return facts.Count.State == FactUnknown
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

func validActionFacts(facts ActionFacts) bool {
	return facts.State == aggregatePresence(
		facts.JavaScript,
		facts.SubmitForm,
		facts.Launch,
		facts.ExternalNavigation,
		facts.OpenAction,
		facts.AdditionalActions,
		facts.CalculationOrder,
	)
}

func validHybridFormFacts(acroForm AcroFormFacts, xfa XFAFacts, facts HybridFormFacts) bool {
	if xfa.State == FactAbsent {
		return facts.State == FactAbsent && facts.Authority.State == FactAbsent &&
			facts.NeedsRendering == FactAbsent && facts.XMLParsed == FactAbsent && facts.Scripts == FactAbsent &&
			facts.DataConnections == FactAbsent && facts.RepeatingSubforms == FactAbsent &&
			facts.PageGrowth == FactAbsent
	}
	if xfa.State == FactUnknown {
		return facts.State == FactUnknown && facts.Authority.State == FactUnknown &&
			facts.NeedsRendering == FactUnknown && facts.XMLParsed == FactUnknown && facts.Scripts == FactUnknown &&
			facts.DataConnections == FactUnknown && facts.RepeatingSubforms == FactUnknown &&
			facts.PageGrowth == FactUnknown
	}
	if xfa.State != FactPresent || facts.State != FactPresent ||
		(facts.Authority.State != FactPresent && facts.Authority.State != FactUnknown) ||
		(facts.XMLParsed != FactPresent && facts.XMLParsed != FactUnknown) {
		return false
	}
	if facts.XMLParsed == FactUnknown &&
		(facts.Scripts != FactUnknown || facts.DataConnections != FactUnknown ||
			facts.RepeatingSubforms != FactUnknown || facts.PageGrowth != FactUnknown) {
		return false
	}
	if facts.XMLParsed == FactPresent &&
		(facts.Scripts == FactUnknown || facts.DataConnections == FactUnknown ||
			facts.RepeatingSubforms == FactUnknown || facts.PageGrowth == FactUnknown) {
		return false
	}
	if facts.Authority.State == FactUnknown {
		return true
	}
	switch facts.Authority.Value {
	case "acroform_fixed_pages":
		return facts.XMLParsed == FactPresent && facts.NeedsRendering == FactAbsent &&
			facts.RepeatingSubforms == FactAbsent && facts.PageGrowth == FactAbsent &&
			(xfa.Rendering.State != FactPresent || xfa.Rendering.Value != "dynamic") &&
			acroForm.State == FactPresent && acroForm.FieldCount.State == FactPresent &&
			acroForm.FieldCount.Value != nil && *acroForm.FieldCount.Value > 0
	case "xfa_dynamic":
		return facts.NeedsRendering == FactPresent ||
			(xfa.Rendering.State == FactPresent && xfa.Rendering.Value == "dynamic")
	default:
		return false
	}
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
