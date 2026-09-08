package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

func TestTurnSteeringStateLinearizesDeliveryAndClose(t *testing.T) {
	state := newTurnSteeringState()
	input := frontend.SteerInput{ID: "steer-1", Text: "inspect the parser"}
	delivered := make(chan struct{})
	release := make(chan struct{})
	steerResult := make(chan error, 1)
	if err := state.steer(t.Context(), input, func(context.Context, frontend.SteerInput) error {
		return nil
	}); !errors.Is(err, ErrNoActiveTurn) {
		t.Fatalf("steer before open error = %v, want %v", err, ErrNoActiveTurn)
	}
	state.open()
	go func() {
		steerResult <- state.steer(t.Context(), input, func(context.Context, frontend.SteerInput) error {
			close(delivered)
			<-release
			return nil
		})
	}()
	<-delivered
	closing := make(chan struct{})
	closed := make(chan struct{})
	go func() {
		close(closing)
		state.close()
		close(closed)
	}()
	<-closing
	select {
	case <-closed:
		t.Fatal("turn steering closed before in-flight delivery completed")
	default:
	}
	close(release)
	if err := <-steerResult; err != nil {
		t.Fatalf("in-flight steer error = %v", err)
	}
	<-closed

	calls := 0
	if err := state.steer(t.Context(), input, func(context.Context, frontend.SteerInput) error {
		calls++
		return nil
	}); !errors.Is(err, ErrNoActiveTurn) {
		t.Fatalf("steer after close error = %v, want %v", err, ErrNoActiveTurn)
	}
	if calls != 0 {
		t.Fatalf("closed steering state invoked delivery %d time(s)", calls)
	}
}
