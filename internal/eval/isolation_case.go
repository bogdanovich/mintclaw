package eval

import (
	"slices"
	"strings"
)

func isolationMainCase(item Case) Case {
	copyCase := item
	steering := *item.Steering
	steering.Scenario, steering.Peer = "boundary", nil
	copyCase.Steering = &steering
	copyCase.Limits.MaxUserTurns = 3
	return copyCase
}

// Materialize one peer scope from the same source contract, never an alternate
// public case or an independently qualified trial.
func isolationPeerCase(item Case) Case {
	peer := item.Steering.Peer
	copyCase := isolationMainCase(item)
	copyCase.Task = strings.ReplaceAll(item.Task, item.Steering.InitialMarker, peer.InitialMarker)
	copyCase.Limits.MaxUserTurns = 3
	copyCase.Steering = &SteeringCase{
		Scenario: "boundary", Modes: []string{"one-at-a-time"},
		InitialMarker: peer.InitialMarker, Updates: []string{peer.Update},
	}
	copyCase.Checks = slices.Clone(item.Checks)
	for index := range copyCase.Checks {
		expected := &copyCase.Checks[index].Expected
		switch *expected {
		case item.Steering.InitialMarker:
			*expected = peer.InitialMarker
		case item.Steering.Updates[0]:
			*expected = peer.Update
		}
	}
	return copyCase
}
