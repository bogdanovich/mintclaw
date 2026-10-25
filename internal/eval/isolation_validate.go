package eval

import (
	"slices"

	"github.com/bogdanovich/mintclaw/pkg/health"
)

func validSteeringEvidence(
	trial Trial, item Case, workspace string, profile ActiveToolProfile, runtime health.RuntimeProbe,
) bool {
	if !validSteeringScopeEvidence(trial, item, workspace, profile, runtime) {
		return false
	}
	if item.Steering.Peer == nil {
		return trial.Isolation == nil
	}
	proof := trial.Isolation
	if proof == nil || proof.Version != Version || item.Steering.Scenario != "isolation" ||
		profile.MaxParallelTurns != 2 {
		return false
	}
	peer := proof.Peer.trial(trial)
	if !validID(peer.ID) || peer.ID == trial.ID || !validID(peer.SessionID) ||
		peer.SessionID == trial.SessionID || peer.SessionKey == trial.SessionKey ||
		peer.StartedAt.Before(trial.StartedAt) || peer.StartedAt.After(trial.Steering.Guidance[0].ObservedAt) ||
		!peer.TransportClosed || peer.ExecutionVerdict != Pass ||
		!validTrialConversation(peer, trial.AgentID, workspace) ||
		!validSteeringScopeEvidence(peer, isolationPeerCase(item), workspace, profile, runtime) {
		return false
	}
	a, b := trial.Steering.Fixture, peer.Steering.Fixture
	// Both fixture calls use one owned host's monotonic sequence. B's admitted
	// read and actual steering are enclosed by A's still-active read.
	if a.EnteredSequence >= b.EnteredSequence || b.SettledSequence >= a.ReleaseSequence {
		return false
	}
	if !slices.Equal(trial.Steering.Outbox, peer.Steering.Outbox) ||
		!slices.Equal(trial.Steering.OutboxNames, peer.Steering.OutboxNames) {
		return false
	}
	return !scopeContainsMarkers(trial, item.Steering.Peer.InitialMarker, item.Steering.Peer.Update) &&
		!scopeContainsMarkers(peer, item.Steering.InitialMarker, item.Steering.Updates[0])
}

func scopeContainsMarkers(trial Trial, initial, current string) bool {
	return privateJSONValue(struct {
		Events   []Event
		Metadata string
		History  []string
	}{trial.Events, trial.Steering.Metadata, trial.Steering.History},
		map[string]string{"foreign_initial": initial, "foreign_current": current})
}
