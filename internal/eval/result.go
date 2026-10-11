package eval

import (
	"time"

	"github.com/bogdanovich/mintclaw/pkg/events"
)

type Verdict string

const (
	Pass         Verdict = "pass"
	Fail         Verdict = "fail"
	Error        Verdict = "error"
	Inconclusive Verdict = "inconclusive"
	Skipped      Verdict = "skipped"
)

type Event struct {
	ID                 string              `json:"id"`
	Turn               int                 `json:"turn"`
	Kind               string              `json:"kind"`
	RequestID          string              `json:"request_id"`
	MessageID          string              `json:"message_id,omitempty"`
	Content            string              `json:"content,omitempty"`
	AgentID            string              `json:"agent_id,omitempty"`
	SessionKey         string              `json:"session_key,omitempty"`
	Model              string              `json:"model,omitempty"`
	TraceScopes        []events.TraceScope `json:"trace_scopes,omitempty"`
	InteractionID      string              `json:"interaction_id,omitempty"`
	InteractionShortID string              `json:"interaction_short_id,omitempty"`
}

type CheckResult struct {
	ID          string   `json:"id"`
	Verdict     Verdict  `json:"verdict"`
	Code        string   `json:"code"`
	EvidenceIDs []string `json:"evidence_ids"`
}

type Trial struct {
	Version          int               `json:"version"`
	ID               string            `json:"id"`
	CaseID           string            `json:"case_id"`
	CaseSHA256       string            `json:"case_sha256"`
	SessionID        string            `json:"session_id"`
	AgentID          string            `json:"agent_id"`
	SessionKey       string            `json:"session_key,omitempty"`
	Verdict          Verdict           `json:"verdict"`
	Code             string            `json:"code"`
	ExecutionVerdict Verdict           `json:"execution_verdict,omitempty"`
	ExecutionCode    string            `json:"execution_code,omitempty"`
	StartedAt        time.Time         `json:"started_at"`
	DurationMS       int64             `json:"duration_ms"`
	Events           []Event           `json:"events"`
	Checks           []CheckResult     `json:"checks"`
	DriverCalls      []RoleCall        `json:"driver_calls,omitempty"`
	Judge            *Judgement        `json:"judge,omitempty"`
	EvidenceEligible bool              `json:"evidence_eligible"`
	TransportClosed  bool              `json:"transport_closed"`
	RemoteState      string            `json:"remote_state"`
	PDF              *PDFTrialEvidence `json:"pdf,omitempty"`
}

type PDFTrialEvidence struct {
	Version      int                     `json:"version"`
	SourceSHA256 string                  `json:"source_sha256"`
	JobID        string                  `json:"job_id,omitempty"`
	State        string                  `json:"state"`
	Cleanup      string                  `json:"cleanup"`
	WriteIntent  *PDFWriteIntentEvidence `json:"write_intent,omitempty"`
	Settlement   *PDFSettlementEvidence  `json:"settlement,omitempty"`
	Artifact     *PDFArtifactEvidence    `json:"artifact,omitempty"`
}

type PDFArtifactEvidence struct {
	Verdict Verdict          `json:"verdict"`
	Code    string           `json:"code"`
	File    string           `json:"file,omitempty"`
	Oracle  *PDFOracleResult `json:"oracle,omitempty"`
}

// Native metadata is not an independent artifact, privacy or cleanup oracle.
type PDFSettlementEvidence struct {
	ApprovalInteractionID string   `json:"approval_interaction_id"`
	ApprovalRequestID     string   `json:"approval_request_id"`
	ApprovalConsumedAt    int64    `json:"approval_consumed_at"`
	OperationID           string   `json:"operation_id"`
	OperationRevision     uint64   `json:"operation_revision"`
	BackendGeneration     string   `json:"backend_generation"`
	OutboxDeliveryID      string   `json:"outbox_delivery_id"`
	PlatformMessageIDs    []string `json:"platform_message_ids"`
	ArtifactRef           string   `json:"artifact_ref"`
	ArtifactSHA256        string   `json:"artifact_sha256"`
	ArtifactSize          int64    `json:"artifact_size"`
}

// An intent is not a consumed native grant, settled write or delivery receipt.
type PDFWriteIntentEvidence struct {
	ReviewRevision        int64  `json:"review_revision"`
	ConfirmationRequestID string `json:"confirmation_request_id"`
	ApprovalInteractionID string `json:"approval_interaction_id,omitempty"`
	OperationID           string `json:"operation_id,omitempty"`
}

// Hard failure remains visible even when the transport, judge, or cleanup also
// fails. A required unknown must never be reduced to a passing verdict.
func combineVerdicts(verdicts ...Verdict) Verdict {
	unknown := false
	for _, observed := range verdicts {
		if observed == Fail {
			return Fail
		}
		if observed != Pass && observed != Error && observed != Inconclusive && observed != Skipped {
			unknown = true
		}
	}
	if unknown {
		return Error
	}
	for _, wanted := range []Verdict{Error, Inconclusive, Skipped} {
		for _, observed := range verdicts {
			if observed == wanted {
				return wanted
			}
		}
	}
	return Pass
}
