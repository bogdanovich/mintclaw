package runtimecap

import (
	"errors"
	"strings"
)

type Kind string

const (
	KindGateway Kind = "gateway"
	KindCoding  Kind = "coding"
)

func (kind Kind) Valid() bool {
	return kind == KindGateway || kind == KindCoding
}

// Principal is the feature-neutral identity of one admitted turn. Transport,
// repository, workspace, and presentation identities deliberately have no
// place in this structure.
type Principal struct {
	Runtime     Kind   `json:"runtime"`
	ActorID     string `json:"actor_id"`
	AgentID     string `json:"agent_id"`
	SessionID   string `json:"session_id"`
	ExecutionID string `json:"execution_id"`
}

func (principal Principal) Validate() error {
	if !principal.Runtime.Valid() {
		return errors.New("runtime principal has an unsupported runtime")
	}
	for _, value := range []string{
		principal.ActorID,
		principal.AgentID,
		principal.SessionID,
		principal.ExecutionID,
	} {
		if value != strings.TrimSpace(value) || value == "" || len(value) > 1024 || containsControl(value) {
			return errors.New("runtime principal has invalid identity text")
		}
	}
	return nil
}

func containsControl(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}
