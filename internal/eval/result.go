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
	Controls           string              `json:"controls,omitempty"`
	InteractionKind    string              `json:"interaction_kind,omitempty"`
	Usage              *TokenUsage         `json:"usage,omitempty"`
}

type CheckResult struct {
	ID          string   `json:"id"`
	Verdict     Verdict  `json:"verdict"`
	Code        string   `json:"code"`
	EvidenceIDs []string `json:"evidence_ids"`
}

type Trial struct {
	Version          int                           `json:"version"`
	ID               string                        `json:"id"`
	CaseID           string                        `json:"case_id"`
	CaseSHA256       string                        `json:"case_sha256"`
	Repetition       int                           `json:"repetition,omitempty"`
	SessionID        string                        `json:"session_id"`
	AgentID          string                        `json:"agent_id"`
	SessionKey       string                        `json:"session_key,omitempty"`
	Verdict          Verdict                       `json:"verdict"`
	Code             string                        `json:"code"`
	ExecutionVerdict Verdict                       `json:"execution_verdict,omitempty"`
	ExecutionCode    string                        `json:"execution_code,omitempty"`
	StartedAt        time.Time                     `json:"started_at"`
	DurationMS       int64                         `json:"duration_ms"`
	Events           []Event                       `json:"events"`
	Checks           []CheckResult                 `json:"checks"`
	DriverCalls      []RoleCall                    `json:"driver_calls,omitempty"`
	Judge            *Judgement                    `json:"judge,omitempty"`
	EvidenceEligible bool                          `json:"evidence_eligible"`
	TransportClosed  bool                          `json:"transport_closed"`
	RemoteState      string                        `json:"remote_state"`
	PDF              *PDFTrialEvidence             `json:"pdf,omitempty"`
	Browser          *BrowserTrialEvidence         `json:"browser,omitempty"`
	Cancellation     *QuestionCancellationEvidence `json:"question_cancellation,omitempty"`
}

// This records one native interaction's terminal state, not runtime quiescence
// or cleanup of every resource owned by the session.
type InteractionCancellationEvidence struct {
	Version          int    `json:"version"`
	InteractionID    string `json:"interaction_id"`
	ShortID          string `json:"short_id"`
	PromptMessageID  string `json:"prompt_message_id"`
	PromptRequestID  string `json:"prompt_request_id"`
	PromptDeliveryID string `json:"prompt_delivery_id"`
	CancelRequestID  string `json:"cancel_request_id"`
	ReplyMessageID   string `json:"reply_message_id"`
	AgentID          string `json:"agent_id"`
	SessionKey       string `json:"session_key"`
	Workspace        string `json:"workspace"`
	OriginTurnID     string `json:"origin_turn_id"`
	WaitingRevision  int64  `json:"waiting_revision"`
	TerminalRevision int64  `json:"terminal_revision"`
	ResolvedAt       int64  `json:"resolved_at"`
}

type QuestionCancellationEvidence = InteractionCancellationEvidence

type PDFTrialEvidence struct {
	Version      int                      `json:"version"`
	SourceSHA256 string                   `json:"source_sha256"`
	JobID        string                   `json:"job_id,omitempty"`
	State        string                   `json:"state"`
	Cleanup      string                   `json:"cleanup"`
	WriteIntent  *PDFWriteIntentEvidence  `json:"write_intent,omitempty"`
	Settlement   *PDFSettlementEvidence   `json:"settlement,omitempty"`
	Artifact     *PDFArtifactEvidence     `json:"artifact,omitempty"`
	Census       *PDFCensusEvidence       `json:"census,omitempty"`
	Cancellation *PDFCancellationEvidence `json:"cancellation,omitempty"`
	Wire         *MediaWireEvidence       `json:"wire,omitempty"`
	PostStop     *PDFPostStopEvidence     `json:"post_stop,omitempty"`
	SourceFields *PDFSourceFieldsEvidence `json:"source_fields,omitempty"`
}

// Post-stop evidence is scoped to the admitted owned runtime and native stores,
// not all privacy surfaces or the independent artifact oracle.
type PDFPostStopEvidence struct {
	Version         int                `json:"version"`
	Code            string             `json:"code"`
	RuntimeStopped  bool               `json:"runtime_stopped"`
	LogicalSettled  bool               `json:"logical_settled"`
	FixtureRemoved  bool               `json:"fixture_removed"`
	AssessmentScope string             `json:"assessment_scope,omitempty"`
	Census          *PDFCensusEvidence `json:"census,omitempty"`
}

// This observes native-bound messages on one client connection, not future
// delivery uniqueness, file contents, no-write safety, privacy or remote cleanup.
type MediaWireEvidence struct {
	Version          int    `json:"version"`
	Scope            string `json:"scope"`
	Code             string `json:"code"`
	Complete         bool   `json:"complete"`
	ObservedMessages int    `json:"observed_messages"`
	MatchingMessages int    `json:"matching_messages"`
	MessageID        string `json:"message_id,omitempty"`
}

// This records bounded native observations, not an idle or privacy proof.
type PDFCensusEvidence struct {
	Version         int              `json:"version"`
	Code            string           `json:"code"`
	BaselineSHA256  string           `json:"baseline_sha256,omitempty"`
	ObservedSHA256  string           `json:"observed_sha256,omitempty"`
	Counts          *PDFCensusCounts `json:"counts,omitempty"`
	SourceUnchanged *bool            `json:"source_unchanged,omitempty"`
}

type PDFCensusCounts struct {
	DocumentDeliveries int `json:"document_deliveries"`
	WriteOperations    int `json:"write_operations"`
	MediaIntents       int `json:"media_intents"`
	PendingOperations  int `json:"pending_operations"`
	ForeignChanges     int `json:"foreign_changes"`
}

// A canceled job and question do not prove whole-runtime idle or no-write safety.
type PDFCancellationEvidence struct {
	Question            QuestionCancellationEvidence `json:"question"`
	JobWaitingRevision  int64                        `json:"job_waiting_revision"`
	JobTerminalRevision int64                        `json:"job_terminal_revision"`
	JobTerminalAt       int64                        `json:"job_terminal_at"`
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
