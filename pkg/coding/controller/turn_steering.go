package controller

import (
	"context"
	"sync"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

type turnSteeringPhase uint8

const (
	turnSteeringWaiting turnSteeringPhase = iota
	turnSteeringOpen
	turnSteeringClosed
)

// turnSteeringState is the controller-owned lifetime and idempotency boundary
// for one turn. Runtime completion and steering delivery share its lock, so a
// successful delivery is ordered before RunTurn returns or rejected after it.
type turnSteeringState struct {
	mu       sync.Mutex
	phase    turnSteeringPhase
	accepted map[string]string
}

func newTurnSteeringState() *turnSteeringState {
	return &turnSteeringState{accepted: make(map[string]string)}
}

func (state *turnSteeringState) open() {
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.phase == turnSteeringWaiting {
		state.phase = turnSteeringOpen
	}
}

func (state *turnSteeringState) close() {
	if state == nil {
		return
	}
	state.mu.Lock()
	state.phase = turnSteeringClosed
	state.mu.Unlock()
}

func (state *turnSteeringState) steer(
	ctx context.Context,
	input frontend.SteerInput,
	deliver func(context.Context, frontend.SteerInput) error,
) error {
	if state == nil {
		return ErrNoActiveTurn
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.phase != turnSteeringOpen {
		return ErrNoActiveTurn
	}
	if accepted, exists := state.accepted[input.ID]; exists {
		if accepted == input.Text {
			return nil
		}
		return ErrSteerConflict
	}
	if len(state.accepted) >= frontend.MaxSteersPerTurn {
		return ErrSteerLimit
	}
	if err := deliver(ctx, input); err != nil {
		return err
	}
	state.accepted[input.ID] = input.Text
	return nil
}
