package computerhost

import (
	"context"
	"errors"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/nodes/companion"
)

func TestInputHostUsesOnlyCurrentWorkspaceObservation(t *testing.T) {
	base, _ := nativeHostFixture(t, nil)
	if _, ok := any(base).(companion.ComputerInputCommandHost); ok {
		t.Fatal("ordinary host inferred an input capability")
	}
	execute := func(context.Context, InputAuthorization) InputDisposition {
		t.Error("input executor entered without a native lease")
		return InputUnverified
	}
	backend := InputBackend{Revision: [32]byte{1}, Execute: execute}
	if _, err := withInputBackends(base, map[string]InputBackend{"missing": backend}); err == nil {
		t.Fatal("unconfigured profile admitted")
	}
	if _, err := withInputBackends(base, map[string]InputBackend{"fixture": {Execute: execute}}); err == nil {
		t.Fatal("unpinned backend admitted")
	}
	captureOnly := base.ComputerProfiles()[0].Revision
	host, err := withInputBackends(base, map[string]InputBackend{"fixture": backend})
	if err != nil {
		t.Fatal(err)
	}
	if host.ComputerInputProfiles()[0].Revision == captureOnly {
		t.Fatal("profile revision did not bind the executor")
	}
	session := openFixture(t, base)
	first := observeFixture(t, base, session, "first-capture")
	next := observeFixture(t, base, session, "next-capture")
	request := computer.InputRequest{
		ID: "input", Binding: session.Binding, ObservationID: next.Observation.ID,
		Action: computer.InputAction{Kind: computer.InputScroll, Scroll: &computer.InputScrollSpec{
			Point: &computer.InputPoint{X: 20, Y: 20}, DeltaY: 40,
		}},
	}
	for _, item := range []struct {
		id, workspace string
		want          error
	}{
		{first.Observation.ID, "workspace", computer.ErrStale},
		{next.Observation.ID, "other-workspace", computer.ErrStale},
		{next.Observation.ID, "workspace", computer.ErrDenied}, // no native guard
	} {
		request.ObservationID = item.id
		if _, err := host.Act(t.Context(), nodes.ComputerHostActRequest{
			Request: request, WorkspaceID: item.workspace,
		}); !errors.Is(err, item.want) {
			t.Fatalf("observation checked against wrong authority: got %v, want %v", err, item.want)
		}
	}
	_ = controlFixture(t, base, session, computer.EventHandoff)
	if _, err := host.Act(t.Context(), nodes.ComputerHostActRequest{
		Request: request, WorkspaceID: "workspace",
	}); !errors.Is(err, computer.ErrStale) {
		t.Fatal("pre-handoff evidence remained actionable", err)
	}
}
