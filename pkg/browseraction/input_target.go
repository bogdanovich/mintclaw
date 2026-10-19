package browseraction

import (
	"crypto/sha256"
	"encoding/hex"
)

const InputTargetVersion = "browser_input_target.v1"

// InputTargetObservation is value-free native target metadata, not permission
// to supply input or execute an action. It must be joined to a delivered native
// question and current owner/session state before a consumer relies on it.
type InputTargetObservation struct {
	Version              string `json:"version"`
	BindingSHA256        string `json:"binding_sha256"`
	ActorID              string `json:"actor_id"`
	AgentID              string `json:"agent_id"`
	SessionKey           string `json:"session_key"`
	ExecutionID          string `json:"execution_id"`
	RequestID            string `json:"request_id"`
	SessionID            string `json:"session_id"`
	TabID                string `json:"tab_id"`
	FrameID              string `json:"frame_id,omitempty"`
	SnapshotID           string `json:"snapshot_id"`
	SnapshotGeneration   uint64 `json:"snapshot_generation"`
	ContextCatalogID     string `json:"context_catalog_id,omitempty"`
	ContextGeneration    uint64 `json:"context_generation,omitempty"`
	ControllerGeneration uint64 `json:"controller_generation"`
	Ref                  string `json:"ref"`
	ElementRole          string `json:"element_role"`
	ElementNameSHA256    string `json:"element_name_sha256"`
	ElementPosition      uint32 `json:"element_position"`
	URLSHA256            string `json:"url_sha256"`
	OriginSHA256         string `json:"origin_sha256"`
	PageStateSHA256      string `json:"page_state_sha256"`
	CatalogRevision      string `json:"catalog_revision"`
	IssuedAt             int64  `json:"issued_at"`
	ExpiresAt            int64  `json:"expires_at"`
}

func (observation InputTargetObservation) Valid() bool {
	if observation.Version != InputTargetVersion || observation.SnapshotGeneration == 0 ||
		observation.ControllerGeneration == 0 || observation.ElementPosition == 0 ||
		observation.IssuedAt <= 0 || observation.ExpiresAt <= observation.IssuedAt {
		return false
	}
	for _, id := range []string{
		observation.ActorID, observation.AgentID, observation.SessionKey, observation.ExecutionID,
		observation.RequestID, observation.SessionID, observation.TabID, observation.SnapshotID, observation.Ref,
	} {
		if !identifierRegexp.MatchString(id) {
			return false
		}
	}
	if observation.ContextCatalogID == "" {
		if observation.ContextGeneration != 0 || observation.FrameID != "" {
			return false
		}
	} else if !identifierRegexp.MatchString(observation.ContextCatalogID) || observation.ContextGeneration == 0 ||
		observation.FrameID != "" && !identifierRegexp.MatchString(observation.FrameID) {
		return false
	}
	switch observation.ElementRole {
	case "textbox", "searchbox", "combobox", "spinbutton":
	default:
		return false
	}
	for _, hash := range []string{
		observation.BindingSHA256, observation.ElementNameSHA256, observation.URLSHA256,
		observation.OriginSHA256, observation.PageStateSHA256, observation.CatalogRevision,
	} {
		if len(hash) != 64 {
			return false
		}
		decoded, err := hex.DecodeString(hash)
		if err != nil || hex.EncodeToString(decoded) != hash {
			return false
		}
	}
	return true
}

// MatchesToken correlates an observation with the exact opaque native binding.
// It does not authenticate a token or make arbitrary metadata authoritative.
func (observation InputTargetObservation) MatchesToken(token string) bool {
	digest := sha256.Sum256([]byte(token))
	return observation.BindingSHA256 == hex.EncodeToString(digest[:])
}
