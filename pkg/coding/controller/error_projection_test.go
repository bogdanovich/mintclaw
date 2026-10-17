package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

type projectedFailureRuntime struct {
	*blockingRuntime
	projector *frontend.Projector
	project   bool
	terminal  bool
	err       error
}

func (runtime *projectedFailureRuntime) RunTurn(
	_ context.Context,
	_ frontend.TurnInput,
	ready func(),
) error {
	ready()
	if runtime.project {
		runtime.projector.TurnStarted("turn-1", "inspect repository")
		runtime.projector.Error("turn-1", "provider-error", "Authentication failed. Sign in again.")
		if runtime.terminal {
			runtime.projector.TurnFailed("turn-1", "authentication failed")
		}
	}
	return runtime.err
}

func TestTurnErrorProjectionAvoidsOnlyCurrentRuntimeDuplicate(t *testing.T) {
	for _, test := range []struct {
		name        string
		priorError  bool
		project     bool
		terminal    bool
		joinedError bool
		wantGeneric bool
	}{
		{name: "current error", project: true, terminal: true},
		{name: "updated current error", priorError: true, project: true, terminal: true},
		{name: "unprojected failure", wantGeneric: true},
		{name: "prior failure does not hide new failure", priorError: true, wantGeneric: true},
		{name: "nonterminal error does not hide failure", project: true, wantGeneric: true},
		{
			name:    "independent joined persistence failure remains visible",
			project: true, terminal: true, joinedError: true, wantGeneric: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
			if err != nil {
				t.Fatal(err)
			}
			if test.priorError {
				projector.TurnStarted("turn-1", "previous request")
				projector.Error("turn-1", "provider-error", "Previous error")
				projector.TurnFailed("turn-1", "previous failure")
			}
			failure := errors.New("synthetic-provider-secret")
			runErr := failure
			if test.joinedError {
				runErr = errors.Join(failure, errors.New("persist metadata"))
			}
			runtime := &projectedFailureRuntime{
				blockingRuntime: newBlockingRuntime(), projector: projector,
				project: test.project, terminal: test.terminal, err: runErr,
			}
			controller, err := New(projector, runtime)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = controller.Close(context.Background()) })
			if err = controller.Submit(t.Context(), frontend.TurnInput{Text: "inspect repository"}); err != nil {
				t.Fatal(err)
			}
			if err = controller.AwaitTurn(t.Context()); !errors.Is(err, failure) {
				t.Fatalf("settlement error = %v, want original failure", err)
			}
			snapshot, err := controller.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			generic := false
			for _, entry := range snapshot.Messages() {
				if entry.ID == "controller:turn-error" {
					generic = true
				}
			}
			if generic != test.wantGeneric {
				t.Fatalf("generic error = %v, want %v; items = %+v", generic, test.wantGeneric, snapshot.Items)
			}
		})
	}
}
