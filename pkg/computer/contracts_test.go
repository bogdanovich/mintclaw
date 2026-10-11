package computer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
)

func contractPrincipal() runtimecap.Principal {
	return runtimecap.Principal{
		Runtime: runtimecap.KindGateway, ActorID: "actor_fixture", AgentID: "main",
		SessionID: "conversation_fixture", ExecutionID: "execution_fixture",
	}
}

func contractTarget() Target {
	return Target{
		Alias: "fixture", Placement: PlacementNode, Node: "fixture_node", OS: OSLinux,
		Mode: ModeAttached, Isolation: IsolationNone, Backend: "fixture_driver",
		Scope: Scope{DesktopID: "desktop_1", DisplayID: "display_1", WindowID: "window_1", WindowGeneration: 1},
	}
}

func contractSession(now time.Time) Session {
	return Session{
		Binding: Binding{
			Principal: contractPrincipal(), SessionID: "computer_session_1", Target: contractTarget(),
			WorkerID: "worker_1", PolicyRevision: "policy_1", Generation: 1,
		},
		Control: ControlAgent, ExpiresAt: now.Add(time.Minute),
	}
}

func contractObservation(session Session, now time.Time) Observation {
	return Observation{
		ID: "observation_1", Binding: session.Binding,
		Geometry:   Geometry{Width: 100, Height: 100, OriginX: -100, Scale: 2},
		CapturedAt: now.Add(-time.Second), ExpiresAt: now.Add(time.Second),
		AccessibilityText: "private fixture accessibility marker",
	}
}

func TestTargetChoicesAreOrthogonal(t *testing.T) {
	for _, os := range []OperatingSystem{OSLinux, OSMacOS} {
		for _, mode := range []DesktopMode{ModeAttached, ModeManaged} {
			for _, isolation := range []Isolation{IsolationNone, IsolationContainer, IsolationVM} {
				target := contractTarget()
				target.OS, target.Mode, target.Isolation = os, mode, isolation
				if err := target.Validate(); err != nil {
					t.Fatalf("target combination %s/%s/%s: %v", os, mode, isolation, err)
				}
			}
		}
	}
	for _, mutate := range []func(*Target){
		func(target *Target) { target.Placement = "automatic" },
		func(target *Target) { target.Node = "" },
		func(target *Target) { target.OS = "automatic" },
		func(target *Target) { target.Mode = "automatic" },
		func(target *Target) { target.Isolation = "automatic" },
		func(target *Target) { target.Alias = "../replacement" },
		func(target *Target) { target.Scope.WindowGeneration = 0 },
		func(target *Target) { target.Scope.WindowID = "" },
	} {
		target := contractTarget()
		mutate(&target)
		if !errors.Is(target.Validate(), ErrInvalid) {
			t.Fatalf("invalid target accepted: %#v", target)
		}
	}
	gateway := contractTarget()
	gateway.Placement, gateway.Node = PlacementGateway, ""
	if err := gateway.Validate(); err != nil {
		t.Fatal(err)
	}
	gateway.Node = "unexpected_node"
	if !errors.Is(gateway.Validate(), ErrInvalid) {
		t.Fatal("gateway target carried a node selector")
	}
}

func TestControlTransitionsAreTypedAndRevokeOldGenerations(t *testing.T) {
	now := time.Now().UTC()
	session := contractSession(now)
	for _, step := range []struct {
		event ControlEvent
		state ControlState
	}{
		{EventHandoff, ControlHuman},
		{EventRequestResume, ControlResumePending},
		{EventResume, ControlAgent},
		{EventRelease, ControlReleased},
	} {
		previous := session
		var err error
		session, err = session.Transition(step.event, previous.Binding.Generation)
		if err != nil || session.Control != step.state || session.Binding.Generation != previous.Binding.Generation+1 {
			t.Fatalf("transition %s: %#v, %v", step.event, session, err)
		}
		if _, err = session.Transition(EventRelease, previous.Binding.Generation); !errors.Is(err, ErrStale) {
			t.Fatalf("old generation accepted: %v", err)
		}
	}
	if session.AgentAuthority(now) == nil {
		t.Fatal("released lease retained agent authority")
	}
	if repeated, err := session.Transition(
		EventRelease,
		session.Binding.Generation,
	); err != nil ||
		repeated != session {
		t.Fatalf("terminal release was not idempotent: %#v, %v", repeated, err)
	}
	for _, state := range []ControlState{ControlAgent, ControlHuman, ControlResumePending} {
		session := contractSession(now)
		session.Control = state
		for _, event := range []ControlEvent{EventRelease, EventLose} {
			next, err := session.Transition(event, session.Binding.Generation)
			if err != nil || !next.Control.Terminal() {
				t.Fatalf("terminal transition from %s: %#v, %v", state, next, err)
			}
		}
	}
	session = contractSession(now)
	if _, err := session.Transition(EventResume, session.Binding.Generation); !errors.Is(err, ErrConflict) {
		t.Fatal("resume skipped human/resume-pending ownership")
	}
	if _, err := session.Transition("готово", session.Binding.Generation); !errors.Is(err, ErrConflict) {
		t.Fatal("natural language was interpreted as a runtime event")
	}
	session.Binding.Generation = math.MaxUint64
	if _, err := session.Transition(EventRelease, session.Binding.Generation); !errors.Is(err, ErrStale) {
		t.Fatal("generation overflow was accepted")
	}
}

func TestBindingRejectsIncompleteAuthority(t *testing.T) {
	for _, mutate := range []func(*Binding){
		func(binding *Binding) { binding.Principal.ActorID = "" },
		func(binding *Binding) { binding.Principal.Runtime = "unknown" },
		func(binding *Binding) { binding.SessionID = "" },
		func(binding *Binding) { binding.WorkerID = "" },
		func(binding *Binding) { binding.PolicyRevision = "" },
		func(binding *Binding) { binding.Generation = 0 },
	} {
		binding := contractSession(time.Now().UTC()).Binding
		mutate(&binding)
		if !errors.Is(binding.Validate(), ErrInvalid) {
			t.Fatalf("incomplete authority accepted: %#v", binding)
		}
	}
}

func TestObservationRejectsStaleAuthorityAndProtectedPayloadErrors(t *testing.T) {
	now := time.Now().UTC()
	session := contractSession(now)
	if err := contractObservation(session, now).Validate(session, now); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*Observation)
		want   error
	}{
		{"actor", func(obs *Observation) { obs.Binding.Principal.ActorID = "other_actor" }, ErrStale},
		{"worker", func(obs *Observation) { obs.Binding.WorkerID = "replacement_worker" }, ErrStale},
		{"policy", func(obs *Observation) { obs.Binding.PolicyRevision = "policy_2" }, ErrStale},
		{"generation", func(obs *Observation) { obs.Binding.Generation++ }, ErrStale},
		{"window reuse", func(obs *Observation) { obs.Binding.Target.Scope.WindowGeneration++ }, ErrStale},
		{"expired", func(obs *Observation) { obs.ExpiresAt = now }, ErrExpired},
		{"future capture", func(obs *Observation) { obs.CapturedAt = now.Add(time.Second) }, ErrInvalid},
		{"outlives lease", func(obs *Observation) { obs.ExpiresAt = session.ExpiresAt.Add(time.Second) }, ErrInvalid},
		{"missing evidence", func(obs *Observation) { obs.AccessibilityText = "" }, ErrInvalid},
		{"oversized accessibility", func(obs *Observation) {
			obs.AccessibilityText = strings.Repeat("x", MaxAccessibilityBytes+1)
		}, ErrInvalid},
		{"invalid UTF8", func(obs *Observation) { obs.AccessibilityText = string([]byte{0xff}) }, ErrInvalid},
		{"capture without image", func(obs *Observation) { obs.ContentType = "image/png" }, ErrInvalid},
		{"oversized capture", func(obs *Observation) { obs.Screenshot = make([]byte, MaxCaptureBytes+1) }, ErrInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			observation := contractObservation(session, now)
			test.mutate(&observation)
			if err := observation.Validate(session, now); !errors.Is(err, test.want) {
				t.Fatalf("observation validation = %v, want %v", err, test.want)
			}
		})
	}
	for _, state := range []ControlState{ControlHuman, ControlResumePending, ControlReleased, ControlLost} {
		session.Control = state
		if err := contractObservation(session, now).Validate(session, now); !errors.Is(err, ErrDenied) {
			t.Fatalf("observation under %s control: %v", state, err)
		}
	}
	session = contractSession(now)
	session.ExpiresAt = now
	if err := contractObservation(session, now).Validate(session, now); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired lease: %v", err)
	}
}

func TestObservationCaptureGeometryAndPrivacy(t *testing.T) {
	now := time.Now().UTC()
	session := contractSession(now)
	observation := contractObservation(session, now)
	var capture bytes.Buffer
	if err := png.Encode(&capture, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	observation.Screenshot, observation.ContentType = capture.Bytes(), "image/png"
	observation.Geometry.Width, observation.Geometry.Height = 2, 2
	if err := observation.Validate(session, now); err != nil {
		t.Fatal(err)
	}
	observation.Geometry.Width++
	if !errors.Is(observation.Validate(session, now), ErrInvalid) {
		t.Fatal("capture dimensions did not bind coordinates")
	}
	observation.Geometry.Width--
	observation.ContentType = "image/jpeg"
	if !errors.Is(observation.Validate(session, now), ErrInvalid) {
		t.Fatal("capture MIME did not match the actual image")
	}
	observation.ContentType = "image/png"
	encoded, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{string(encoded), fmt.Sprint(observation), fmt.Sprintf("%#v", observation)} {
		if strings.Contains(text, observation.AccessibilityText) || strings.Contains(text, "screenshot") ||
			strings.Contains(text, "accessibility_text") {
			t.Fatalf("protected payload entered diagnostics/JSON: %s", text)
		}
	}
	geometry := Geometry{Width: 200, Height: 100, OriginX: -100, OriginY: -50, Scale: 2}
	x, y, err := geometry.DesktopPoint(100, 50)
	if err != nil || x != -50 || y != -25 {
		t.Fatalf("Retina/negative-origin transform = (%v,%v), %v", x, y, err)
	}
	for _, point := range [][2]float64{{-1, 0}, {200, 0}, {0, 100}, {math.NaN(), 0}, {0, math.Inf(1)}} {
		if _, _, err = geometry.DesktopPoint(point[0], point[1]); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid point accepted: %v", point)
		}
	}
	for _, mutate := range []func(*Geometry){
		func(geometry *Geometry) { geometry.Scale = 0 },
		func(geometry *Geometry) { geometry.Scale = math.NaN() },
		func(geometry *Geometry) { geometry.OriginX = math.Inf(1) },
		func(geometry *Geometry) { geometry.Width = MaxCaptureDimension + 1 },
		func(geometry *Geometry) { geometry.Width, geometry.Height = MaxCaptureDimension, MaxCaptureDimension },
	} {
		invalid := geometry
		mutate(&invalid)
		if !errors.Is(invalid.Validate(), ErrInvalid) {
			t.Fatalf("invalid geometry accepted: %#v", invalid)
		}
	}
}

type contractFakeHost struct {
	session         Session
	now             time.Time
	applicationOpen bool
	observations    int
}

var _ Host = (*contractFakeHost)(nil)

func (host *contractFakeHost) Targets(_ context.Context, principal runtimecap.Principal) ([]Target, error) {
	if principal != contractPrincipal() {
		return nil, ErrDenied
	}
	return []Target{contractTarget()}, nil
}

func (host *contractFakeHost) Open(_ context.Context, request OpenRequest) (Session, error) {
	if request.Validate() != nil || request.Principal != contractPrincipal() || request.Target != contractTarget() {
		return Session{}, ErrDenied
	}
	host.session = contractSession(host.now)
	host.applicationOpen = true
	return host.session, nil
}

func (host *contractFakeHost) Status(_ context.Context, principal runtimecap.Principal, id string) (Session, error) {
	if principal != host.session.Binding.Principal || id != host.session.Binding.SessionID {
		return Session{}, ErrDenied
	}
	return host.session, nil
}

func (host *contractFakeHost) Observe(_ context.Context, session Session) (Observation, error) {
	if session.Binding != host.session.Binding {
		return Observation{}, ErrStale
	}
	if err := host.session.AgentAuthority(host.now); err != nil {
		return Observation{}, err
	}
	host.observations++
	observation := contractObservation(host.session, host.now)
	observation.ID = fmt.Sprintf("observation_%d", host.observations)
	return observation, observation.Validate(host.session, host.now)
}

func (host *contractFakeHost) transition(binding Binding, event ControlEvent) (Session, error) {
	if binding != host.session.Binding {
		return Session{}, ErrStale
	}
	next, err := host.session.Transition(event, binding.Generation)
	if err == nil {
		host.session = next
	}
	return next, err
}

func (host *contractFakeHost) Handoff(_ context.Context, binding Binding) (Session, error) {
	return host.transition(binding, EventHandoff)
}

func (host *contractFakeHost) RequestResume(_ context.Context, binding Binding) (Session, error) {
	return host.transition(binding, EventRequestResume)
}

func (host *contractFakeHost) Resume(_ context.Context, binding Binding) (Session, error) {
	return host.transition(binding, EventResume)
}

func (host *contractFakeHost) Release(_ context.Context, binding Binding) (Session, error) {
	return host.transition(binding, EventRelease)
}

func TestComputerHostContractSmoke(t *testing.T) {
	host := &contractFakeHost{now: time.Now().UTC()}
	ctx := t.Context()
	targets, err := host.Targets(ctx, contractPrincipal())
	if err != nil || len(targets) != 1 || host.applicationOpen || host.observations != 0 {
		t.Fatalf("passive discovery launched/captured: %#v, %v", host, err)
	}
	session, err := host.Open(ctx, OpenRequest{Principal: contractPrincipal(), Target: targets[0]})
	if err != nil {
		t.Fatal(err)
	}
	first, err := host.Observe(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	session, err = host.Handoff(ctx, session.Binding)
	if err != nil || session.Control != ControlHuman {
		t.Fatalf("handoff: %#v, %v", session, err)
	}
	if _, err = host.Observe(ctx, session); !errors.Is(err, ErrDenied) || host.observations != 1 {
		t.Fatalf("human ownership allowed observation: %v", err)
	}
	session, err = host.RequestResume(ctx, session.Binding)
	if err != nil {
		t.Fatal(err)
	}
	session, err = host.Resume(ctx, session.Binding)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(first.Validate(session, host.now), ErrStale) {
		t.Fatal("pre-handoff observation remained fresh")
	}
	fresh, err := host.Observe(ctx, session)
	if err != nil || fresh.ID == first.ID || fresh.Validate(session, host.now) != nil {
		t.Fatalf("fresh same-session observation: %#v, %v", fresh, err)
	}
	session, err = host.Release(ctx, session.Binding)
	if err != nil || session.Control != ControlReleased || !host.applicationOpen {
		t.Fatalf("release closed application or retained control: %#v, %v", host, err)
	}
	if _, err = host.Observe(ctx, session); !errors.Is(err, ErrDenied) || host.observations != 2 {
		t.Fatalf("released session allowed capture: %v", err)
	}
	status, err := host.Status(ctx, contractPrincipal(), session.Binding.SessionID)
	if err != nil || status != session {
		t.Fatalf("released session status: %#v, %v", status, err)
	}
	other := contractPrincipal()
	other.ActorID = "other_actor"
	if _, err = host.Status(ctx, other, session.Binding.SessionID); !errors.Is(err, ErrDenied) {
		t.Fatalf("cross-principal status was allowed: %v", err)
	}
}
