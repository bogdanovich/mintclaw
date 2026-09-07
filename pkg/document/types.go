// Package document defines MintClaw's versioned document-operation contracts.
package document

import "time"

const (
	ReportSchemaVersion     = "mintclaw.document_report.v1"
	CapabilitySchemaVersion = "mintclaw.document_capabilities.v1"
	DefaultMaxInputBytes    = int64(20 * 1024 * 1024)
)

type State string

const (
	StateSucceeded   State = "succeeded"
	StateUnavailable State = "unavailable"
	StateUnsupported State = "unsupported"
	StateDenied      State = "denied"
	StateCanceled    State = "canceled"
	StateFailed      State = "failed"
	StateUncertain   State = "uncertain"
)

type FailureCode string

const (
	FailureUnsupportedPlatform FailureCode = "unsupported_platform"
	FailureInvalidInput        FailureCode = "invalid_input"
	FailureUnsupportedType     FailureCode = "unsupported_type"
	FailureLimitExceeded       FailureCode = "limit_exceeded"
	FailureSourceChanged       FailureCode = "source_changed"
	FailureSourceUnauthorized  FailureCode = "source_not_authorized"
	FailureCanceled            FailureCode = "canceled"
	FailureWorkerUnavailable   FailureCode = "worker_unavailable"
	FailureWorkerProtocol      FailureCode = "worker_protocol"
	FailureWorkerCrashed       FailureCode = "worker_crashed"
	FailureWorkerOutputLimit   FailureCode = "worker_output_limit"
	FailureWorkerTimeout       FailureCode = "worker_timeout"
	FailureWorkerInputMismatch FailureCode = "worker_input_mismatch"
	FailureInternal            FailureCode = "internal_failure"
)

type Authority struct {
	Kind        string `json:"kind"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	AgentID     string `json:"agent_id,omitempty"`
	ActorID     string `json:"actor_id,omitempty"`
	RouteID     string `json:"route_id,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
}

type DocumentRef struct {
	Ref              string    `json:"ref"`
	SourceRef        string    `json:"source_ref,omitempty"`
	OriginalFilename string    `json:"original_filename"`
	ContentType      string    `json:"content_type"`
	Size             int64     `json:"size"`
	SHA256           string    `json:"sha256"`
	Authority        Authority `json:"authority"`
	SourceKind       string    `json:"source_kind"`
	CreatedAt        time.Time `json:"created_at"`
	CleanupPolicy    string    `json:"cleanup_policy"`
}

type Limits struct {
	MaxInputBytes int64 `json:"max_input_bytes"`
}

type Failure struct {
	Code    FailureCode `json:"code"`
	Message string      `json:"message"`
}

type Report struct {
	SchemaVersion string       `json:"schema_version"`
	OperationID   string       `json:"operation_id"`
	Operation     string       `json:"operation"`
	State         State        `json:"state"`
	Input         *DocumentRef `json:"input,omitempty"`
	Limits        Limits       `json:"limits"`
	Failure       *Failure     `json:"failure,omitempty"`
}

type OperationCapability struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

type CapabilityReport struct {
	SchemaVersion string                         `json:"schema_version"`
	Platform      string                         `json:"platform"`
	Architecture  string                         `json:"architecture"`
	Operations    map[string]OperationCapability `json:"operations"`
	Limits        Limits                         `json:"limits"`
}
