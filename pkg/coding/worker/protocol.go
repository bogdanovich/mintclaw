// Package worker defines the private, versioned control contract for one
// task-scoped coding worker. It contains no process, terminal, gateway, or
// Node Companion lifecycle.
package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
)

const (
	ProtocolV1 = 1

	// MaxRecordBytes bounds one JSON object without its JSONL delimiter.
	MaxRecordBytes       = 2 << 20
	MaxIDBytes           = 128
	MaxBuildIDBytes      = 256
	MaxModelIDBytes      = 256
	MaxPathBytes         = 32 << 10
	MaxErrorMessageBytes = 4 << 10
	MaxAttachmentMeta    = 255
)

var (
	ErrInvalidRecord        = errors.New("invalid coding worker protocol record")
	ErrRecordTooLarge       = errors.New("coding worker protocol record exceeds size limit")
	ErrIncompatibleProtocol = errors.New("incompatible coding worker protocol")
	ErrIdentityMismatch     = errors.New("coding worker control identity mismatch")

	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
)

type RecordType string

const (
	RecordRequest  RecordType = "request"
	RecordResponse RecordType = "response"
	RecordEvent    RecordType = "event"
)

type Method string

const (
	MethodInitialize    Method = "initialize"
	MethodTurnStart     Method = "turn.start"
	MethodTurnSteer     Method = "turn.steer"
	MethodTurnInterrupt Method = "turn.interrupt"
	MethodTurnCancel    Method = "turn.cancel"
	MethodSnapshotRead  Method = "snapshot.read"
	MethodShutdown      Method = "shutdown"
)

func (method Method) Valid() bool {
	switch method {
	case MethodInitialize, MethodTurnStart, MethodTurnSteer, MethodTurnInterrupt,
		MethodTurnCancel, MethodSnapshotRead, MethodShutdown:
		return true
	default:
		return false
	}
}

// RequiresIdempotencyKey reports whether a request must carry a key. The
// read-only snapshot command has no replay side effects.
func (method Method) RequiresIdempotencyKey() bool {
	return method.Valid() && method != MethodSnapshotRead
}

type ErrorCode string

const (
	ErrorInvalidRequest     ErrorCode = "invalid_request"
	ErrorUnsupportedVersion ErrorCode = "unsupported_version"
	ErrorNotInitialized     ErrorCode = "not_initialized"
	ErrorIdentityMismatch   ErrorCode = "identity_mismatch"
	ErrorTurnActive         ErrorCode = "turn_active"
	ErrorNoActiveTurn       ErrorCode = "no_active_turn"
	ErrorTurnNotSteerable   ErrorCode = "turn_not_steerable"
	ErrorSteerConflict      ErrorCode = "steer_conflict"
	ErrorSteerLimit         ErrorCode = "steer_limit"
	ErrorWorkerStopping     ErrorCode = "worker_stopping"
	ErrorCanceled           ErrorCode = "canceled"
	ErrorInterrupted        ErrorCode = "interrupted"
	ErrorUncertain          ErrorCode = "uncertain"
	ErrorInternal           ErrorCode = "internal"
)

func (code ErrorCode) Valid() bool {
	switch code {
	case ErrorInvalidRequest, ErrorUnsupportedVersion, ErrorNotInitialized,
		ErrorIdentityMismatch, ErrorTurnActive, ErrorNoActiveTurn, ErrorTurnNotSteerable,
		ErrorSteerConflict, ErrorSteerLimit, ErrorWorkerStopping, ErrorCanceled, ErrorInterrupted,
		ErrorUncertain, ErrorInternal:
		return true
	default:
		return false
	}
}

type ProtocolError struct {
	Code    ErrorCode       `json:"code"`
	Message string          `json:"message"`
	Details json.RawMessage `json:"details,omitempty"`
}

func (protocolError ProtocolError) Validate() error {
	if !protocolError.Code.Valid() || !validBoundedText(protocolError.Message, MaxErrorMessageBytes) {
		return fmt.Errorf("%w: malformed protocol error", ErrInvalidRecord)
	}
	if err := validateStructuredText(protocolError); err != nil {
		return err
	}
	if len(protocolError.Details) != 0 {
		return validateJSONObject("error details", protocolError.Details)
	}
	return nil
}

// Record is one complete JSONL value. SchemaVersion is present on every
// record so a captured line remains self-describing outside its pipe.
type Record struct {
	SchemaVersion  int             `json:"schema_version"`
	Type           RecordType      `json:"type"`
	ID             string          `json:"id,omitempty"`
	Method         Method          `json:"method,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
	Params         json.RawMessage `json:"params,omitempty"`
	OK             *bool           `json:"ok,omitempty"`
	Result         json.RawMessage `json:"result,omitempty"`
	Error          *ProtocolError  `json:"error,omitempty"`
	Event          EventName       `json:"event,omitempty"`
	Payload        json.RawMessage `json:"payload,omitempty"`
}

func (record Record) Validate() error {
	if record.SchemaVersion != ProtocolV1 {
		return fmt.Errorf("%w: unsupported schema version %d", ErrInvalidRecord, record.SchemaVersion)
	}
	switch record.Type {
	case RecordRequest:
		return record.validateRequest()
	case RecordResponse:
		return record.validateResponse()
	case RecordEvent:
		return record.validateEvent()
	default:
		return fmt.Errorf("%w: unsupported record type %q", ErrInvalidRecord, record.Type)
	}
}

func (record Record) validateRequest() error {
	if !validIdentifier(record.ID) || !record.Method.Valid() {
		return fmt.Errorf("%w: request requires a valid ID and method", ErrInvalidRecord)
	}
	if record.Method.RequiresIdempotencyKey() {
		if !validIdentifier(record.IdempotencyKey) {
			return fmt.Errorf("%w: %s requires an idempotency key", ErrInvalidRecord, record.Method)
		}
	} else if record.IdempotencyKey != "" {
		return fmt.Errorf("%w: %s does not accept an idempotency key", ErrInvalidRecord, record.Method)
	}
	if _, err := DecodeRequestPayload(record.Method, record.Params); err != nil {
		return err
	}
	if record.OK != nil || len(record.Result) != 0 || record.Error != nil ||
		record.Event != "" || len(record.Payload) != 0 {
		return fmt.Errorf("%w: request contains fields from another record type", ErrInvalidRecord)
	}
	return nil
}

func (record Record) validateResponse() error {
	if !validIdentifier(record.ID) || !record.Method.Valid() || record.OK == nil {
		return fmt.Errorf("%w: response requires a valid ID, method, and ok", ErrInvalidRecord)
	}
	if record.IdempotencyKey != "" || len(record.Params) != 0 ||
		record.Event != "" || len(record.Payload) != 0 {
		return fmt.Errorf("%w: response contains fields from another record type", ErrInvalidRecord)
	}
	if *record.OK {
		if record.Error != nil {
			return fmt.Errorf("%w: successful response contains an error", ErrInvalidRecord)
		}
		_, err := DecodeResultPayload(record.Method, record.Result)
		return err
	}
	if len(record.Result) != 0 || record.Error == nil {
		return fmt.Errorf("%w: failed response requires only an error", ErrInvalidRecord)
	}
	return record.Error.Validate()
}

func (record Record) validateEvent() error {
	if !record.Event.Valid() {
		return fmt.Errorf("%w: event requires a supported name", ErrInvalidRecord)
	}
	if _, err := DecodeEventPayload(record.Event, record.Payload); err != nil {
		return err
	}
	if record.ID != "" || record.Method != "" || record.IdempotencyKey != "" ||
		len(record.Params) != 0 || record.OK != nil || len(record.Result) != 0 || record.Error != nil {
		return fmt.Errorf("%w: event contains fields from another record type", ErrInvalidRecord)
	}
	return nil
}

func (record Record) validateWireShape(members map[string]json.RawMessage) error {
	required := []string{"schema_version", "type", "id", "method"}
	switch record.Type {
	case RecordRequest:
		required = append(required, "params")
		if record.Method.RequiresIdempotencyKey() {
			required = append(required, "idempotency_key")
		}
	case RecordResponse:
		required = append(required, "ok")
		if record.OK != nil && *record.OK {
			required = append(required, "result")
		} else {
			required = append(required, "error")
		}
	case RecordEvent:
		required = []string{"schema_version", "type", "event", "payload"}
	default:
		return fmt.Errorf("%w: unsupported record type %q", ErrInvalidRecord, record.Type)
	}
	if len(members) != len(required) {
		return fmt.Errorf("%w: non-canonical %s envelope fields", ErrInvalidRecord, record.Type)
	}
	for _, name := range required {
		if _, present := members[name]; !present {
			return fmt.Errorf("%w: %s envelope is missing %q", ErrInvalidRecord, record.Type, name)
		}
	}
	return nil
}

// DecodeRequestPayload selects and validates the closed request schema owned
// by method. Worker implementations consume this dispatcher instead of
// reproducing method switches at each transport boundary.
func DecodeRequestPayload(method Method, raw json.RawMessage) (any, error) {
	var payload interface{ Validate() error }
	switch method {
	case MethodInitialize:
		payload = &InitializeParams{}
	case MethodTurnStart:
		payload = &TurnStartParams{}
	case MethodTurnSteer:
		payload = &TurnSteerParams{}
	case MethodTurnInterrupt, MethodTurnCancel, MethodSnapshotRead, MethodShutdown:
		payload = &GenerationParams{}
	default:
		return nil, fmt.Errorf("%w: unsupported request method %q", ErrInvalidRecord, method)
	}
	if err := validateProtocolText(raw); err != nil {
		return nil, err
	}
	if err := DecodePayload(raw, payload); err != nil {
		return nil, err
	}
	if err := payload.Validate(); err != nil {
		return nil, fmt.Errorf("%w: invalid %s request payload: %w", ErrInvalidRecord, method, err)
	}
	return payload, nil
}

// AckResult is the explicit closed schema for commands whose successful
// response carries no data.
type AckResult struct{}

func (AckResult) Validate() error { return nil }

// DecodeResultPayload selects and validates a self-describing successful
// response. Response envelopes retain the method so captured records remain
// independently verifiable.
func DecodeResultPayload(method Method, raw json.RawMessage) (any, error) {
	var payload interface{ Validate() error }
	switch method {
	case MethodInitialize:
		payload = &InitializeResult{}
	case MethodSnapshotRead:
		payload = &SnapshotResult{}
	case MethodTurnStart, MethodTurnSteer, MethodTurnInterrupt, MethodTurnCancel, MethodShutdown:
		payload = &AckResult{}
	default:
		return nil, fmt.Errorf("%w: unsupported response method %q", ErrInvalidRecord, method)
	}
	if err := validateProtocolText(raw); err != nil {
		return nil, err
	}
	if err := DecodePayload(raw, payload); err != nil {
		return nil, err
	}
	if err := payload.Validate(); err != nil {
		return nil, fmt.Errorf("%w: invalid %s response payload: %w", ErrInvalidRecord, method, err)
	}
	return payload, nil
}

func NegotiateProtocol(minimum, maximum int) (int, error) {
	if minimum <= 0 || maximum < minimum || minimum > ProtocolV1 || maximum < ProtocolV1 {
		return 0, fmt.Errorf(
			"%w: peer range %d-%d does not include %d",
			ErrIncompatibleProtocol,
			minimum,
			maximum,
			ProtocolV1,
		)
	}
	return ProtocolV1, nil
}

type TaskMode string

const (
	TaskModeInvestigate TaskMode = "investigate"
	TaskModeMutate      TaskMode = "mutate"
)

func (mode TaskMode) Valid() bool {
	return mode == TaskModeInvestigate || mode == TaskModeMutate
}

// ThreadOpenMode makes thread creation versus strict resume an immutable
// supervisor decision. Workers must not infer this authority from whether
// thread state happens to exist on disk.
type ThreadOpenMode string

const (
	ThreadOpenNew    ThreadOpenMode = "new"
	ThreadOpenResume ThreadOpenMode = "resume"
)

func (mode ThreadOpenMode) Valid() bool {
	return mode == ThreadOpenNew || mode == ThreadOpenResume
}

// Binding is the immutable identity authorized before repository or model
// construction. Project identifies the configured source project;
// ExecutionRoot may become a distinct P7.3 worktree.
type Binding struct {
	TaskID                string                 `json:"task_id"`
	TaskGenerationID      string                 `json:"task_generation_id"`
	WorkerGenerationID    string                 `json:"worker_generation_id"`
	ThreadID              string                 `json:"thread_id"`
	ThreadOpenMode        ThreadOpenMode         `json:"thread_open_mode"`
	Project               thread.ProjectIdentity `json:"project"`
	ExecutionRoot         string                 `json:"execution_root"`
	ExecutionRootIdentity string                 `json:"execution_root_identity"`
	Mode                  TaskMode               `json:"mode"`
	ProviderProfile       string                 `json:"provider_profile"`
	Model                 string                 `json:"model"`
	Provider              string                 `json:"provider"`
	ExpectedWorkerBuildID string                 `json:"expected_worker_build_id"`
}

func (binding Binding) ControlIdentity() ControlIdentity {
	return ControlIdentity{
		TaskID:             binding.TaskID,
		TaskGenerationID:   binding.TaskGenerationID,
		WorkerGenerationID: binding.WorkerGenerationID,
	}
}

func (binding Binding) Validate() error {
	if err := binding.ControlIdentity().Validate(); err != nil {
		return err
	}
	parsedThreadID, err := uuid.Parse(binding.ThreadID)
	if err != nil || parsedThreadID.String() != binding.ThreadID {
		return fmt.Errorf("%w: thread ID must be a canonical UUID", ErrInvalidRecord)
	}
	if err := binding.Project.Validate(); err != nil {
		return fmt.Errorf("%w: invalid project identity: %w", ErrInvalidRecord, err)
	}
	if !validPath(binding.Project.ProjectRoot) || !validPath(binding.Project.InvocationCWD) ||
		!validOptionalPath(binding.Project.GitWorktreeRoot) || !validOptionalPath(binding.Project.GitDir) ||
		!validOptionalPath(binding.Project.GitCommonDir) ||
		containsStructuralControl(binding.Project.GitOrigin) ||
		containsStructuralControl(binding.Project.GitBranch) || !validPath(binding.ExecutionRoot) {
		return fmt.Errorf("%w: invalid project or execution identity text", ErrInvalidRecord)
	}
	if binding.ExecutionRootIdentity != ExecutionRootIdentity(binding.ExecutionRoot) {
		return fmt.Errorf("%w: execution root identity does not match its path", ErrInvalidRecord)
	}
	if !binding.ThreadOpenMode.Valid() || !binding.Mode.Valid() || !validIdentifier(binding.ProviderProfile) ||
		!validBoundedText(binding.Model, MaxModelIDBytes) || !validIdentifier(binding.Provider) ||
		!validBuildID(binding.ExpectedWorkerBuildID) {
		return fmt.Errorf(
			"%w: invalid thread/task mode, model/provider profile, or worker build identity",
			ErrInvalidRecord,
		)
	}
	return nil
}

// Authorize verifies that a post-handshake command targets this exact bound
// task and worker generation.
func (binding Binding) Authorize(identity ControlIdentity) error {
	if err := binding.Validate(); err != nil {
		return err
	}
	if err := identity.Validate(); err != nil {
		return err
	}
	if identity != binding.ControlIdentity() {
		return ErrIdentityMismatch
	}
	return nil
}

func ExecutionRootIdentity(root string) string {
	digest := sha256.Sum256([]byte(filepath.Clean(root)))
	return hex.EncodeToString(digest[:])
}

type InitializeParams struct {
	MinProtocolVersion int     `json:"min_protocol_version"`
	MaxProtocolVersion int     `json:"max_protocol_version"`
	ParentBuildID      string  `json:"parent_build_id"`
	Binding            Binding `json:"binding"`
}

func (params InitializeParams) Validate() error {
	if _, err := NegotiateProtocol(params.MinProtocolVersion, params.MaxProtocolVersion); err != nil {
		return err
	}
	if !validBuildID(params.ParentBuildID) {
		return fmt.Errorf("%w: invalid parent build identity", ErrInvalidRecord)
	}
	return params.Binding.Validate()
}

type BoundIdentity struct {
	ProtocolVersion int     `json:"protocol_version"`
	WorkerBuildID   string  `json:"worker_build_id"`
	Binding         Binding `json:"binding"`
}

func (identity BoundIdentity) Validate() error {
	if identity.ProtocolVersion != ProtocolV1 || !validBuildID(identity.WorkerBuildID) ||
		identity.WorkerBuildID != identity.Binding.ExpectedWorkerBuildID {
		return fmt.Errorf("%w: worker protocol or build identity mismatch", ErrInvalidRecord)
	}
	return identity.Binding.Validate()
}

type TurnAttachment struct {
	SourcePath  string `json:"source_path"`
	Filename    string `json:"filename,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

// ControlIdentity makes every post-handshake command assert the exact task
// and worker generation it intends to control. The worker compares this value
// with its immutable binding before applying an operation.
type ControlIdentity struct {
	TaskID             string `json:"task_id"`
	TaskGenerationID   string `json:"task_generation_id"`
	WorkerGenerationID string `json:"worker_generation_id"`
}

func (identity ControlIdentity) Validate() error {
	if !validIdentifier(identity.TaskID) || !validIdentifier(identity.TaskGenerationID) ||
		!validIdentifier(identity.WorkerGenerationID) {
		return fmt.Errorf("%w: malformed task or worker generation identity", ErrInvalidRecord)
	}
	return nil
}

type TurnStartParams struct {
	ControlIdentity
	Text        string           `json:"text,omitempty"`
	Attachments []TurnAttachment `json:"attachments,omitempty"`
}

func (params TurnStartParams) Validate() error {
	if err := params.ControlIdentity.Validate(); err != nil {
		return err
	}
	if len(params.Attachments) == 0 {
		if err := thread.ValidatePrompt(params.Text); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidRecord, err)
		}
		return nil
	}
	if len(params.Attachments) > frontend.MaxTurnAttachments || !utf8.ValidString(params.Text) ||
		len(params.Text) > thread.MaxPromptBytes {
		return fmt.Errorf("%w: invalid turn text or attachment count", ErrInvalidRecord)
	}
	for index, attachment := range params.Attachments {
		if !validPath(attachment.SourcePath) || !validOptionalText(attachment.Filename, MaxAttachmentMeta) ||
			!validOptionalText(attachment.ContentType, MaxAttachmentMeta) {
			return fmt.Errorf("%w: invalid attachment %d", ErrInvalidRecord, index+1)
		}
	}
	return nil
}

func (params TurnStartParams) FrontendInput() frontend.TurnInput {
	input := frontend.TurnInput{Text: params.Text}
	input.Attachments = make([]frontend.TurnAttachment, len(params.Attachments))
	for index, attachment := range params.Attachments {
		input.Attachments[index] = frontend.TurnAttachment{
			Path: attachment.SourcePath, Filename: attachment.Filename, ContentType: attachment.ContentType,
		}
	}
	return input
}

type QuestionAnswerRef struct {
	QuestionID       string `json:"question_id"`
	QuestionRevision uint64 `json:"question_revision"`
	AnswerID         string `json:"answer_id"`
}

func (reference QuestionAnswerRef) Validate() error {
	if !validIdentifier(reference.QuestionID) || reference.QuestionRevision == 0 ||
		!validIdentifier(reference.AnswerID) {
		return fmt.Errorf("%w: malformed question answer reference", ErrInvalidRecord)
	}
	return nil
}

type TurnSteerParams struct {
	ControlIdentity
	Text           string             `json:"text"`
	QuestionAnswer *QuestionAnswerRef `json:"question_answer,omitempty"`
}

func (params TurnSteerParams) Validate() error {
	if err := params.ControlIdentity.Validate(); err != nil {
		return err
	}
	if err := thread.ValidatePrompt(params.Text); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRecord, err)
	}
	if params.QuestionAnswer != nil {
		return params.QuestionAnswer.Validate()
	}
	return nil
}

type GenerationParams struct {
	ControlIdentity
}

func (params GenerationParams) Validate() error {
	return params.ControlIdentity.Validate()
}

type InitializeResult struct {
	Identity BoundIdentity `json:"identity"`
}

func (result InitializeResult) Validate() error {
	return result.Identity.Validate()
}

func validIdentifier(value string) bool {
	return len(value) > 0 && len(value) <= MaxIDBytes && identifierPattern.MatchString(value)
}

func validBuildID(value string) bool {
	return len(value) > 0 && len(value) <= MaxBuildIDBytes && utf8.ValidString(value) &&
		value == strings.TrimSpace(value) && !containsStructuralControl(value)
}

func validPath(value string) bool {
	return value != "" && len(value) <= MaxPathBytes && utf8.ValidString(value) &&
		filepath.IsAbs(value) && filepath.Clean(value) == value && !containsStructuralControl(value)
}

func validOptionalPath(value string) bool {
	return value == "" || validPath(value)
}

func validBoundedText(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && utf8.ValidString(value) &&
		value == strings.TrimSpace(value) && !containsStructuralControl(value)
}

func validOptionalText(value string, maximum int) bool {
	return value == "" || len(value) <= maximum && utf8.ValidString(value) &&
		!containsStructuralControl(value)
}
