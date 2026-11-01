package eval

import "time"

type IsolationEvidence struct {
	Version int                    `json:"version"`
	Peer    IsolationScopeEvidence `json:"peer"`
}

// Scope-only fields avoid recursive Trial evidence and duplicated authority.
type IsolationScopeEvidence struct {
	TrialID          string            `json:"trial_id"`
	SessionID        string            `json:"session_id"`
	SessionKey       string            `json:"session_key"`
	StartedAt        time.Time         `json:"started_at"`
	Events           []Event           `json:"events"`
	ExecutionVerdict Verdict           `json:"execution_verdict"`
	ExecutionCode    string            `json:"execution_code"`
	TransportClosed  bool              `json:"transport_closed"`
	RemoteState      string            `json:"remote_state"`
	Steering         *SteeringEvidence `json:"steering,omitempty"`
}

func isolationScope(trial Trial) IsolationScopeEvidence {
	return IsolationScopeEvidence{
		TrialID: trial.ID, SessionID: trial.SessionID, SessionKey: trial.SessionKey,
		StartedAt: trial.StartedAt, Events: trial.Events,
		ExecutionVerdict: trial.ExecutionVerdict, ExecutionCode: trial.ExecutionCode,
		TransportClosed: trial.TransportClosed, RemoteState: trial.RemoteState, Steering: trial.Steering,
	}
}

func (scope IsolationScopeEvidence) trial(parent Trial) Trial {
	return Trial{
		Version: Version, ID: scope.TrialID, CaseID: parent.CaseID, CaseSHA256: parent.CaseSHA256,
		SessionID: scope.SessionID, SessionKey: scope.SessionKey, AgentID: parent.AgentID,
		StartedAt: scope.StartedAt, Events: scope.Events,
		ExecutionVerdict: scope.ExecutionVerdict, ExecutionCode: scope.ExecutionCode,
		TransportClosed: scope.TransportClosed, RemoteState: scope.RemoteState, Steering: scope.Steering,
	}
}
