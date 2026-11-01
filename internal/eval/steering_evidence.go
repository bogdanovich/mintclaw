package eval

import "time"

// ActiveToolHostEvidence records local listener cleanup, never native turn
// completion. A clean child and complete private log remain separate gates.
type ActiveToolHostEvidence struct {
	Version int               `json:"version"`
	Profile ActiveToolProfile `json:"profile"`
	Code    string            `json:"code"`
}

func (host *ActiveToolHostEvidence) valid(report RunReport) bool {
	if host == nil {
		return true
	}
	return host.Version == Version && host.Profile.validate() == nil &&
		(host.Code == "closed" || host.Code == "shutdown_unknown") && report.TargetBudget != nil &&
		report.PrivateSurfaces != nil && report.TargetLifecycle != nil && report.TargetLifecycle.valid() &&
		report.TargetLifecycle.PID == report.Runtime.PID && report.Runtime.LogLevel == "info"
}

func (host *ActiveToolHostEvidence) verdict(lifecycle *TargetLifecycle) Verdict {
	if host == nil {
		return Pass
	}
	if host.Code != "closed" || lifecycle == nil || lifecycle.Code != "native_exit_zero" || lifecycle.LogTruncated {
		return Inconclusive
	}
	return Pass
}

// These bounded original observations support offline hard checks. They are
// private operator-owned evidence, not model input or hostile-local attestation.
type SteeringEvidence struct {
	Version          int                        `json:"version"`
	Code             string                     `json:"code"`
	Profile          ActiveToolProfile          `json:"profile"`
	PrimaryRequestID string                     `json:"primary_request_id"`
	Guidance         []SteeringGuidanceEvidence `json:"guidance"`
	Fixture          activeToolObservation      `json:"fixture"`
	Metadata         string                     `json:"metadata"`
	MetadataSHA256   string                     `json:"metadata_sha256"`
	History          []string                   `json:"history"`
	HistorySHA256    string                     `json:"history_sha256"`
	Trace            string                     `json:"trace"`
	TraceSHA256      string                     `json:"trace_sha256"`
	Outbox           []SteeringOutboxEvidence   `json:"outbox"`
	OutboxNames      []string                   `json:"outbox_names"`
}

type SteeringGuidanceEvidence struct {
	InputEventID string                `json:"input_event_id"`
	Enqueue      string                `json:"enqueue"`
	SHA256       string                `json:"sha256"`
	Held         activeToolObservation `json:"held"`
	ObservedAt   time.Time             `json:"observed_at"`
}

type SteeringOutboxEvidence struct {
	File   string `json:"file"`
	JSON   string `json:"json"`
	SHA256 string `json:"sha256"`
}

func requiresActiveTool(item Case) bool { return item.Steering != nil || item.NativeStop != nil }
