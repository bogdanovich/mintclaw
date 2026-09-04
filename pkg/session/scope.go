package session

import "time"

// ScopeVersion is the current structured session-scope schema version.
const ScopeVersion = 2

type SessionEpoch struct {
	Strategy string    `json:"strategy"`
	ID       string    `json:"id"`
	Start    time.Time `json:"start"`
}

// SessionScope describes the semantic session partition selected for a turn.
type SessionScope struct {
	Version       int               `json:"version"`
	AgentID       string            `json:"agent_id"`
	Channel       string            `json:"channel"`
	Account       string            `json:"account"`
	Dimensions    []string          `json:"dimensions"`
	Values        map[string]string `json:"values"`
	RouteScopeKey string            `json:"route_scope_key,omitempty"`
	// ClientSessionID is frontend provenance, not canonical identity.
	ClientSessionID string        `json:"client_session_id,omitempty"`
	Epoch           *SessionEpoch `json:"epoch,omitempty"`
}

// CloneScope returns a deep copy of scope.
func CloneScope(scope *SessionScope) *SessionScope {
	if scope == nil {
		return nil
	}
	cloned := *scope
	if scope.Dimensions != nil {
		cloned.Dimensions = append([]string{}, scope.Dimensions...)
	}
	if scope.Values != nil {
		cloned.Values = make(map[string]string, len(scope.Values))
		for key, value := range scope.Values {
			cloned.Values[key] = value
		}
	}
	if scope.Epoch != nil {
		epoch := *scope.Epoch
		cloned.Epoch = &epoch
	}
	return &cloned
}
