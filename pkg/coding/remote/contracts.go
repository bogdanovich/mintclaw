// Package remote defines the transport-neutral, model-safe contract between a
// local coding runtime and the same-user gateway capability broker.
package remote

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	codingscope "github.com/bogdanovich/mintclaw/pkg/coding/scope"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
)

const (
	SchemaV1 = "mintclaw.coding_remote_ipc.v1"

	MaxFrameBytes              = 1024 * 1024
	MaxRequestIDBytes          = 128
	MaxAliasBytes              = 64
	MaxRevisionBytes           = 128
	MaxProjectKeyBytes         = 96
	MaxCapabilities            = 64
	MaxOperationsPerCapability = 32
	MaxTaskScopes              = 64
	MaxSchemaBytes             = 64 * 1024
	MaxArgumentsBytes          = 256 * 1024
	MaxResultBytes             = 768 * 1024
	MaxArtifactChunkBytes      = 256 * 1024
	MaxFetchedArtifactBytes    = 32 * 1024 * 1024
	MaxChanges                 = 64
)

var (
	ErrInvalidMessage = errors.New("invalid coding remote message")

	aliasPattern        = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	identifierPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	projectKeyPattern   = regexp.MustCompile(`^(directory|git_worktree):[a-f0-9]{64}$`)
	responseCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)
	statePattern        = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

// Operation is one closed broker protocol operation.
type Operation string

const (
	OperationCapabilitiesList Operation = "capabilities.list"
	OperationCapabilityInvoke Operation = "capability.invoke"
	OperationInvocationStatus Operation = "invocation.status"
	OperationInvocationCancel Operation = "invocation.cancel"
	OperationArtifactDescribe Operation = "artifact.describe"
	OperationArtifactFetch    Operation = "artifact.fetch"

	// BrowserReceiptRecoveryOperation is an internal status-only operation
	// alias. A fresh local coding process uses it to ask the broker to recover
	// the immutable browser invocation binding from the retained receipt. It is
	// never advertised as a capability operation and cannot dispatch work.
	BrowserReceiptRecoveryOperation = "browser_receipt"
	BrowserReceiptRecoveryRevision  = "browser-receipt-recovery-v1"
)

func (operation Operation) Valid() bool {
	switch operation {
	case OperationCapabilitiesList, OperationCapabilityInvoke,
		OperationInvocationStatus, OperationInvocationCancel,
		OperationArtifactDescribe, OperationArtifactFetch:
		return true
	default:
		return false
	}
}

// Request is one authenticated broker request after transport-level peer
// admission. It contains no absolute path, node identity, or credential.
type Request struct {
	Schema        string              `json:"schema"`
	RequestID     string              `json:"request_id"`
	Operation     Operation           `json:"operation"`
	Grant         string              `json:"grant"`
	GrantRevision string              `json:"grant_revision"`
	ThreadID      string              `json:"thread_id"`
	SessionKey    string              `json:"session_key"`
	ProjectKey    string              `json:"project_key"`
	LocalProfile  codingscope.Profile `json:"local_profile"`

	// Principal and CallID are supplied by the trusted local coding runtime,
	// never by model-authored tool arguments.
	Principal           *runtimecap.Principal `json:"principal,omitempty"`
	CallID              string                `json:"call_id,omitempty"`
	DiscoveryRevision   string                `json:"discovery_revision,omitempty"`
	Capability          string                `json:"capability,omitempty"`
	CapabilityRevision  string                `json:"capability_revision,omitempty"`
	CapabilityOperation string                `json:"capability_operation,omitempty"`
	Arguments           json.RawMessage       `json:"arguments,omitempty"`
	InvocationID        string                `json:"invocation_id,omitempty"`
	ArtifactRef         string                `json:"artifact_ref,omitempty"`
	Offset              int64                 `json:"offset,omitempty"`
	LimitBytes          int                   `json:"limit_bytes,omitempty"`
	DeadlineUnixMS      int64                 `json:"deadline_unix_ms,omitempty"`
}

func (request Request) Validate() error {
	if request.Schema != SchemaV1 {
		return fmt.Errorf("%w: unsupported schema", ErrInvalidMessage)
	}
	if !validIdentifier(request.RequestID, MaxRequestIDBytes) {
		return fmt.Errorf("%w: malformed request ID", ErrInvalidMessage)
	}
	if !request.Operation.Valid() {
		return fmt.Errorf("%w: unsupported operation", ErrInvalidMessage)
	}
	if !ValidAlias(request.Grant) || !validIdentifier(request.GrantRevision, MaxRevisionBytes) {
		return fmt.Errorf("%w: malformed grant", ErrInvalidMessage)
	}
	threadID, err := uuid.Parse(request.ThreadID)
	if err != nil || threadID.String() != request.ThreadID || request.SessionKey != "coding:"+request.ThreadID {
		return fmt.Errorf("%w: malformed coding thread identity", ErrInvalidMessage)
	}
	if len(request.ProjectKey) > MaxProjectKeyBytes || !projectKeyPattern.MatchString(request.ProjectKey) {
		return fmt.Errorf("%w: malformed project key", ErrInvalidMessage)
	}
	if !LocalProfileAllowed(request.LocalProfile) {
		return fmt.Errorf("%w: unsupported local coding profile", ErrInvalidMessage)
	}
	switch request.Operation {
	case OperationCapabilitiesList:
		if request.Principal != nil || request.CallID != "" || request.DiscoveryRevision != "" ||
			request.Capability != "" || request.CapabilityRevision != "" ||
			request.CapabilityOperation != "" || len(request.Arguments) != 0 || request.InvocationID != "" ||
			request.ArtifactRef != "" || request.Offset != 0 || request.LimitBytes != 0 ||
			request.DeadlineUnixMS != 0 {
			return fmt.Errorf("%w: discovery carries execution fields", ErrInvalidMessage)
		}
		return nil
	case OperationCapabilityInvoke:
		if err := request.validateExecutionAuthority(); err != nil {
			return err
		}
		if !ValidAlias(request.CapabilityOperation) ||
			!validObjectJSON(request.Arguments, MaxArgumentsBytes) ||
			!validIdentifier(request.InvocationID, MaxRequestIDBytes) ||
			request.InvocationID != DeriveInvocationID(request) || request.ArtifactRef != "" ||
			request.Offset != 0 || request.LimitBytes != 0 {
			return fmt.Errorf("%w: malformed capability invocation", ErrInvalidMessage)
		}
		return nil
	case OperationInvocationStatus, OperationInvocationCancel:
		if err := request.validateExecutionAuthority(); err != nil {
			return err
		}
		if !ValidAlias(request.CapabilityOperation) || len(request.Arguments) != 0 ||
			!validIdentifier(request.InvocationID, MaxRequestIDBytes) || request.ArtifactRef != "" ||
			request.Offset != 0 || request.LimitBytes != 0 {
			return fmt.Errorf("%w: malformed invocation observation", ErrInvalidMessage)
		}
		return nil
	case OperationArtifactDescribe, OperationArtifactFetch:
		if err := request.validateExecutionAuthority(); err != nil {
			return err
		}
		if request.CapabilityOperation != "workspace_exec" || len(request.Arguments) != 0 ||
			!validIdentifier(request.InvocationID, MaxRequestIDBytes) ||
			!validIdentifier(request.ArtifactRef, MaxRequestIDBytes) {
			return fmt.Errorf("%w: malformed artifact request", ErrInvalidMessage)
		}
		if request.Operation == OperationArtifactDescribe {
			if request.Offset != 0 || request.LimitBytes != 0 {
				return fmt.Errorf("%w: artifact description carries a range", ErrInvalidMessage)
			}
			return nil
		}
		if request.Offset < 0 || request.LimitBytes < 1 || request.LimitBytes > MaxArtifactChunkBytes {
			return fmt.Errorf("%w: malformed artifact range", ErrInvalidMessage)
		}
		return nil
	default:
		return fmt.Errorf("%w: unsupported operation", ErrInvalidMessage)
	}
}

// DeriveInvocationID binds one caller-visible recovery reference before the
// IPC round trip. Arguments and discovery revision are deliberately excluded:
// retrying the same trusted tool call cannot turn changed input or refreshed
// discovery into a second durable invocation.
func DeriveInvocationID(request Request) string {
	digest := sha256.New()
	for _, value := range []string{
		"mintclaw:coding-remote-invocation:v1",
		request.ThreadID,
		request.ProjectKey,
		string(request.LocalProfile),
		request.Grant,
		request.GrantRevision,
		request.Capability,
		request.CapabilityRevision,
		request.CapabilityOperation,
		request.CallID,
	} {
		_, _ = fmt.Fprintf(digest, "%d:", len(value))
		_, _ = digest.Write([]byte(value))
	}
	return "remote_capability_" + hex.EncodeToString(digest.Sum(nil))
}

func (request Request) validateExecutionAuthority() error {
	if request.Principal == nil || request.Principal.Runtime != runtimecap.KindCoding ||
		request.Principal.Validate() != nil || request.Principal.SessionID != request.SessionKey ||
		!validIdentifier(request.CallID, MaxRequestIDBytes) ||
		!validIdentifier(request.DiscoveryRevision, MaxRevisionBytes) ||
		!ValidAlias(request.Capability) ||
		!validIdentifier(request.CapabilityRevision, MaxRevisionBytes) || request.DeadlineUnixMS <= 0 {
		return fmt.Errorf("%w: malformed execution authority", ErrInvalidMessage)
	}
	return nil
}

// ResponseStatus is a safe broker outcome. Details that could expose
// transport, node, path, or credential state are never placed in Message.
type ResponseStatus string

const (
	ResponseOK          ResponseStatus = "ok"
	ResponseDenied      ResponseStatus = "denied"
	ResponseUnavailable ResponseStatus = "unavailable"
	ResponseError       ResponseStatus = "error"
)

func (status ResponseStatus) Valid() bool {
	switch status {
	case ResponseOK, ResponseDenied, ResponseUnavailable, ResponseError:
		return true
	default:
		return false
	}
}

// Response is the strict top-level result envelope.
type Response struct {
	Schema    string              `json:"schema"`
	RequestID string              `json:"request_id"`
	Status    ResponseStatus      `json:"status"`
	Code      string              `json:"code,omitempty"`
	Message   string              `json:"message,omitempty"`
	Snapshot  *CapabilitySnapshot `json:"snapshot,omitempty"`
	Result    *CapabilityResult   `json:"result,omitempty"`
	Artifact  *ArtifactResult     `json:"artifact,omitempty"`
}

func (response Response) Validate() error {
	if response.Schema != SchemaV1 || !validIdentifier(response.RequestID, MaxRequestIDBytes) ||
		!response.Status.Valid() {
		return fmt.Errorf("%w: malformed response envelope", ErrInvalidMessage)
	}
	if (response.Code != "" && !responseCodePattern.MatchString(response.Code)) ||
		!validSafeText(response.Message, 1024) {
		return fmt.Errorf("%w: malformed response detail", ErrInvalidMessage)
	}
	if response.Status == ResponseOK {
		if response.Code != "" || response.Message != "" ||
			boolCount(response.Snapshot != nil, response.Result != nil, response.Artifact != nil) != 1 {
			return fmt.Errorf("%w: successful response requires exactly one payload", ErrInvalidMessage)
		}
		if response.Snapshot != nil {
			return response.Snapshot.Validate()
		}
		if response.Result != nil {
			return response.Result.Validate()
		}
		return response.Artifact.Validate()
	}
	if response.Snapshot != nil || response.Result != nil || response.Artifact != nil || response.Code == "" {
		return fmt.Errorf("%w: failed response requires a safe code and no payload", ErrInvalidMessage)
	}
	return nil
}

func boolCount(values ...bool) int {
	count := 0
	for _, value := range values {
		if value {
			count++
		}
	}
	return count
}

// ArtifactResult describes or carries one bounded chunk of an immutable job
// artifact owned by the producing coding invocation. It deliberately exposes
// neither the companion job ID nor node-local or gateway-local paths.
type ArtifactResult struct {
	Grant              string `json:"grant"`
	GrantRevision      string `json:"grant_revision"`
	DiscoveryRevision  string `json:"discovery_revision"`
	Capability         string `json:"capability"`
	CapabilityRevision string `json:"capability_revision"`
	InvocationID       string `json:"invocation_id"`
	Target             string `json:"target"`
	ArtifactRef        string `json:"artifact_ref"`
	Name               string `json:"name"`
	State              string `json:"state"`
	Size               int64  `json:"size"`
	SHA256             string `json:"sha256"`
	ContentType        string `json:"content_type"`
	Offset             int64  `json:"offset,omitempty"`
	NextOffset         int64  `json:"next_offset,omitempty"`
	EOF                bool   `json:"eof,omitempty"`
	DataBase64         string `json:"data_base64,omitempty"`
}

func (result ArtifactResult) Validate() error {
	if !ValidAlias(result.Grant) || !validIdentifier(result.GrantRevision, MaxRevisionBytes) ||
		!validIdentifier(result.DiscoveryRevision, MaxRevisionBytes) || !ValidAlias(result.Capability) ||
		!validIdentifier(result.CapabilityRevision, MaxRevisionBytes) ||
		!validIdentifier(result.InvocationID, MaxRequestIDBytes) || !ValidAlias(result.Target) ||
		!validIdentifier(result.ArtifactRef, MaxRequestIDBytes) ||
		!validSafeText(result.Name, 255) || result.Name == "" || result.State != "available" ||
		result.Size < 0 || result.Size > MaxFetchedArtifactBytes ||
		len(result.SHA256) != sha256.Size*2 || !validHex(result.SHA256) ||
		!validSafeText(result.ContentType, 127) || result.ContentType == "" ||
		result.Offset < 0 || result.NextOffset < result.Offset || result.NextOffset > result.Size {
		return fmt.Errorf("%w: malformed artifact result", ErrInvalidMessage)
	}
	if result.DataBase64 == "" {
		if result.Offset != 0 || result.NextOffset != 0 || result.EOF {
			return fmt.Errorf("%w: malformed artifact description", ErrInvalidMessage)
		}
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(result.DataBase64)
	if err != nil || len(decoded) == 0 || len(decoded) > MaxArtifactChunkBytes ||
		result.NextOffset-result.Offset != int64(len(decoded)) || result.EOF != (result.NextOffset == result.Size) {
		return fmt.Errorf("%w: malformed artifact chunk", ErrInvalidMessage)
	}
	return nil
}

func validHex(value string) bool {
	_, err := hex.DecodeString(value)
	return err == nil
}

// CapabilityResult is the bounded, model-safe projection of one durable
// companion invocation. It contains aliases and a retained invocation ID, but
// never node IDs, absolute paths, command bodies, credentials, or raw policy.
type CapabilityResult struct {
	Grant                 string          `json:"grant"`
	GrantRevision         string          `json:"grant_revision"`
	DiscoveryRevision     string          `json:"discovery_revision"`
	Capability            string          `json:"capability"`
	CapabilityRevision    string          `json:"capability_revision"`
	Operation             string          `json:"operation,omitempty"`
	InvocationID          string          `json:"invocation_id"`
	JobInvocationID       string          `json:"job_invocation_id,omitempty"`
	Target                string          `json:"target"`
	Risk                  Risk            `json:"risk"`
	State                 string          `json:"state"`
	Result                json.RawMessage `json:"result,omitempty"`
	ErrorCode             string          `json:"error_code,omitempty"`
	RecoveryAction        string          `json:"recovery_action,omitempty"`
	CancellationConfirmed bool            `json:"cancellation_confirmed,omitempty"`
	Changes               []ChangeReceipt `json:"changes,omitempty"`
}

func (result CapabilityResult) Validate() error {
	if !ValidAlias(result.Grant) || !validIdentifier(result.GrantRevision, MaxRevisionBytes) ||
		!validIdentifier(result.DiscoveryRevision, MaxRevisionBytes) || !ValidAlias(result.Capability) ||
		!validIdentifier(result.CapabilityRevision, MaxRevisionBytes) ||
		(result.Operation != "" && !ValidAlias(result.Operation)) ||
		!validIdentifier(result.InvocationID, MaxRequestIDBytes) || !ValidAlias(result.Target) ||
		!result.Risk.Valid() || !statePattern.MatchString(result.State) ||
		(result.ErrorCode != "" && !responseCodePattern.MatchString(result.ErrorCode)) ||
		!validSafeText(result.RecoveryAction, 2048) || len(result.Changes) > MaxChanges {
		return fmt.Errorf("%w: malformed capability result", ErrInvalidMessage)
	}
	if result.JobInvocationID != "" {
		if !validIdentifier(result.JobInvocationID, MaxRequestIDBytes) ||
			result.Operation == "workspace_exec" && result.JobInvocationID != result.InvocationID ||
			result.Operation != "workspace_exec" && !strings.HasPrefix(result.Operation, "job_") {
			return fmt.Errorf("%w: malformed job invocation reference", ErrInvalidMessage)
		}
	}
	if len(result.Result) > MaxResultBytes || (len(result.Result) != 0 && !json.Valid(result.Result)) {
		return fmt.Errorf("%w: malformed capability result payload", ErrInvalidMessage)
	}
	prior := ""
	for _, change := range result.Changes {
		if err := change.Validate(); err != nil || prior != "" && change.Path <= prior {
			return fmt.Errorf("%w: malformed capability changes", ErrInvalidMessage)
		}
		prior = change.Path
	}
	return nil
}

type ChangeReceipt struct {
	Path   string `json:"path"`
	Action string `json:"action"`
}

func (receipt ChangeReceipt) Validate() error {
	if !validSafeText(receipt.Path, 4096) || receipt.Path == "" ||
		path.IsAbs(receipt.Path) || path.Clean(receipt.Path) != receipt.Path || receipt.Path == "." ||
		strings.HasPrefix(receipt.Path, "../") || !validIdentifier(receipt.Action, 64) {
		return fmt.Errorf("%w: malformed change receipt", ErrInvalidMessage)
	}
	return nil
}

// CapabilitySnapshot is an immutable, revision-bound view safe for a coding
// model. It deliberately contains aliases rather than node IDs or paths.
type CapabilitySnapshot struct {
	Schema            string                 `json:"schema"`
	Grant             string                 `json:"grant"`
	GrantRevision     string                 `json:"grant_revision"`
	DiscoveryRevision string                 `json:"discovery_revision"`
	GeneratedAtUnixMS int64                  `json:"generated_at_unix_ms"`
	Capabilities      []CapabilityDescriptor `json:"capabilities"`
	TaskScopes        []TaskScopeDescriptor  `json:"task_scopes"`
}

func (snapshot CapabilitySnapshot) Validate() error {
	if snapshot.Schema != SchemaV1 || !ValidAlias(snapshot.Grant) ||
		!validIdentifier(snapshot.GrantRevision, MaxRevisionBytes) ||
		!validIdentifier(snapshot.DiscoveryRevision, MaxRevisionBytes) || snapshot.GeneratedAtUnixMS <= 0 ||
		len(snapshot.Capabilities) > MaxCapabilities || len(snapshot.TaskScopes) > MaxTaskScopes {
		return fmt.Errorf("%w: malformed capability snapshot", ErrInvalidMessage)
	}
	prior := ""
	for _, capability := range snapshot.Capabilities {
		if err := capability.Validate(); err != nil {
			return err
		}
		if prior != "" && capability.Alias <= prior {
			return fmt.Errorf("%w: capabilities are not sorted and unique", ErrInvalidMessage)
		}
		prior = capability.Alias
	}
	prior = ""
	for _, taskScope := range snapshot.TaskScopes {
		if err := taskScope.Validate(); err != nil {
			return err
		}
		if prior != "" && taskScope.Alias <= prior {
			return fmt.Errorf("%w: task scopes are not sorted and unique", ErrInvalidMessage)
		}
		prior = taskScope.Alias
	}
	return nil
}

type CapabilityKind string

const (
	CapabilityRemoteWorkspace CapabilityKind = "remote_workspace"
	CapabilityNodeCommand     CapabilityKind = "node_command"
	CapabilityBrowserProfile  CapabilityKind = "browser_profile"
)

func (kind CapabilityKind) Valid() bool {
	return kind == CapabilityRemoteWorkspace || kind == CapabilityNodeCommand ||
		kind == CapabilityBrowserProfile
}

type Availability string

const (
	AvailabilityAvailable Availability = "available"
	AvailabilityOffline   Availability = "offline"
)

func (availability Availability) Valid() bool {
	return availability == AvailabilityAvailable || availability == AvailabilityOffline
}

type Risk string

const (
	RiskRead  Risk = "read"
	RiskWrite Risk = "write"
)

func (risk Risk) Valid() bool {
	return risk == RiskRead || risk == RiskWrite
}

// CapabilityDescriptor is a bounded model-facing alias. Operation aliases
// are already intersected with the current approved node catalog.
type CapabilityDescriptor struct {
	Alias        string                `json:"alias"`
	Revision     string                `json:"revision"`
	Target       string                `json:"target"`
	Kind         CapabilityKind        `json:"kind"`
	Availability Availability          `json:"availability"`
	Operations   []OperationDescriptor `json:"operations"`
}

func (descriptor CapabilityDescriptor) Validate() error {
	if !ValidAlias(descriptor.Alias) || !validIdentifier(descriptor.Revision, MaxRevisionBytes) ||
		!ValidAlias(descriptor.Target) || !descriptor.Kind.Valid() || !descriptor.Availability.Valid() ||
		len(descriptor.Operations) == 0 || len(descriptor.Operations) > MaxOperationsPerCapability {
		return fmt.Errorf("%w: malformed capability descriptor", ErrInvalidMessage)
	}
	prior := ""
	for _, operation := range descriptor.Operations {
		if err := operation.Validate(); err != nil {
			return err
		}
		if prior != "" && operation.Alias <= prior {
			return fmt.Errorf("%w: capability operations are not sorted and unique", ErrInvalidMessage)
		}
		prior = operation.Alias
	}
	return nil
}

type OperationDescriptor struct {
	Alias            string          `json:"alias"`
	Risk             Risk            `json:"risk"`
	InputSchema      json.RawMessage `json:"input_schema"`
	ResultKind       string          `json:"result_kind"`
	SupportsProgress bool            `json:"supports_progress,omitempty"`
	SupportsCancel   bool            `json:"supports_cancel,omitempty"`
}

func (descriptor OperationDescriptor) Validate() error {
	if !ValidAlias(descriptor.Alias) || !descriptor.Risk.Valid() ||
		!validIdentifier(descriptor.ResultKind, 64) ||
		!validObjectJSON(descriptor.InputSchema, MaxSchemaBytes) {
		return fmt.Errorf("%w: malformed operation descriptor", ErrInvalidMessage)
	}
	return nil
}

type TaskScopeDescriptor struct {
	Alias        string                `json:"alias"`
	Revision     string                `json:"revision"`
	Target       string                `json:"target"`
	Profiles     []codingscope.Profile `json:"profiles"`
	Availability Availability          `json:"availability"`
}

func (descriptor TaskScopeDescriptor) Validate() error {
	if !ValidAlias(descriptor.Alias) || !validIdentifier(descriptor.Revision, MaxRevisionBytes) ||
		!ValidAlias(descriptor.Target) || !descriptor.Availability.Valid() ||
		len(descriptor.Profiles) == 0 || len(descriptor.Profiles) > 5 ||
		!slices.IsSorted(descriptor.Profiles) {
		return fmt.Errorf("%w: malformed task scope descriptor", ErrInvalidMessage)
	}
	seen := make(map[codingscope.Profile]struct{}, len(descriptor.Profiles))
	for _, profile := range descriptor.Profiles {
		if !profile.AdmittedInV5() {
			return fmt.Errorf("%w: unsupported task profile", ErrInvalidMessage)
		}
		if _, duplicate := seen[profile]; duplicate {
			return fmt.Errorf("%w: duplicate task profile", ErrInvalidMessage)
		}
		seen[profile] = struct{}{}
	}
	return nil
}

func ValidAlias(value string) bool {
	return len(value) <= MaxAliasBytes && aliasPattern.MatchString(value)
}

// LocalProfileAllowed restricts the IPC caller to ordinary local coding
// profiles. Remote task profiles are granted independently.
func LocalProfileAllowed(profile codingscope.Profile) bool {
	return profile == codingscope.ProfileInvestigate || profile == codingscope.ProfileMutate
}

func DecodeRequest(raw []byte) (Request, error) {
	var request Request
	if err := decodeStrict(raw, &request); err != nil {
		return Request{}, err
	}
	if err := request.Validate(); err != nil {
		return Request{}, err
	}
	return request, nil
}

func DecodeResponse(raw []byte) (Response, error) {
	var response Response
	if err := decodeStrict(raw, &response); err != nil {
		return Response{}, err
	}
	if err := response.Validate(); err != nil {
		return Response{}, err
	}
	return response, nil
}

func decodeStrict(raw []byte, destination any) error {
	if len(raw) == 0 || len(raw) > MaxFrameBytes || !utf8.Valid(raw) {
		return fmt.Errorf("%w: frame is outside bounds", ErrInvalidMessage)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidMessage, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing value", ErrInvalidMessage)
	}
	return nil
}

func validIdentifier(value string, maximum int) bool {
	return len(value) <= maximum && identifierPattern.MatchString(value)
}

func validSafeText(value string, maximum int) bool {
	if len(value) > maximum || !utf8.ValidString(value) || value != strings.TrimSpace(value) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validObjectJSON(raw json.RawMessage, maximum int) bool {
	if len(raw) == 0 || len(raw) > maximum || !json.Valid(raw) {
		return false
	}
	var object map[string]any
	return json.Unmarshal(raw, &object) == nil && object != nil
}
