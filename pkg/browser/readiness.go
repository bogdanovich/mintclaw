package browser

import "context"

const (
	ReadinessReady       = "ready"
	ReadinessConfigured  = "configured"
	ReadinessBusy        = "busy"
	ReadinessDegraded    = "degraded"
	ReadinessUnavailable = "unavailable"

	CompatibilityUnchecked    = "unchecked"
	CompatibilityCompatible   = "compatible"
	CompatibilityIncompatible = "incompatible"
)

// DriverReadiness is a bounded, path-free snapshot supplied by a worker
// factory. Reading it must not start a driver, probe a browser, or mutate the
// factory's last-observed compatibility state.
type DriverReadiness struct {
	Status        string `json:"status"`
	Driver        string `json:"driver"`
	Browser       string `json:"browser"`
	Proxy         string `json:"proxy"`
	Compatibility string `json:"compatibility"`
	Code          string `json:"code,omitempty"`
	Action        string `json:"action,omitempty"`
}

// PassiveReadiness combines the factory snapshot with the broker's durable
// profile lease projection. It contains enums and operator guidance only.
type PassiveReadiness struct {
	Status        string              `json:"status"`
	Broker        string              `json:"broker"`
	Worker        string              `json:"worker"`
	Driver        string              `json:"driver"`
	Browser       string              `json:"browser"`
	Proxy         string              `json:"proxy"`
	Compatibility string              `json:"compatibility"`
	Profile       ProfileAvailability `json:"profile"`
	Code          string              `json:"code,omitempty"`
	Action        string              `json:"action,omitempty"`
	Passive       bool                `json:"passive"`
}

// TargetDiagnostics is one immutable worker-factory capability snapshot for a
// target and its configured profiles.
type TargetDiagnostics struct {
	Actions     []ActionKind
	Profiles    map[string]DriverReadiness
	Contexts    bool
	Diagnostics bool
}

type targetDiagnosticsFactory interface {
	PassiveTargetDiagnostics(context.Context, string, []string) (TargetDiagnostics, error)
}

type readinessFactory interface {
	PassiveReadiness() DriverReadiness
}

func configuredDriverReadiness() DriverReadiness {
	return DriverReadiness{
		Status: ReadinessConfigured, Driver: ReadinessConfigured,
		Browser: ReadinessConfigured, Proxy: ReadinessConfigured,
		Compatibility: CompatibilityUnchecked,
	}
}
