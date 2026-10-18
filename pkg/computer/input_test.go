package computer

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

func inputFixture(t *testing.T) (Session, InputEvidence, InputRequest, time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	session := contractSession(now)
	observation := contractObservation(session, now)
	evidence, err := InputEvidenceFromObservation(observation, session, now)
	if err != nil {
		t.Fatal(err)
	}
	request := InputRequest{
		ID: "input_1", Binding: session.Binding, ObservationID: evidence.ID,
		Action: InputAction{
			Kind:  InputClick,
			Click: &InputClickSpec{Target: InputSelector{Point: &InputPoint{X: 20, Y: 30}}, Button: "left", Count: 1},
		},
	}
	return session, evidence, request, now
}

func TestInputActionShapesAndGeometry(t *testing.T) {
	geometry := Geometry{Width: 200, Height: 100, OriginX: -300, OriginY: -100, Scale: 2}
	for _, action := range []InputAction{
		{Kind: InputClick, Click: &InputClickSpec{
			Target: InputSelector{Point: &InputPoint{X: 199, Y: 99}}, Button: "right", Count: 2,
		}},
		{Kind: InputClick, Click: &InputClickSpec{
			Target: InputSelector{ElementRef: "element_1"}, Button: "left", Count: 1,
		}},
		{Kind: InputInsertText, Text: &InputTextSpec{ElementRef: "field_1"}},
		{Kind: InputReplaceText, Text: &InputTextSpec{ElementRef: "field_1"}},
		{Kind: InputKey, Key: &InputKeySpec{Name: "enter"}},
		{Kind: InputKey, Key: &InputKeySpec{Name: "a", Modifiers: []InputModifier{InputControl}}},
		{Kind: InputKey, Key: &InputKeySpec{Name: "f12", Modifiers: []InputModifier{InputShift}}},
		{Kind: InputScroll, Scroll: &InputScrollSpec{Point: &InputPoint{X: 0, Y: 0}, DeltaY: -2048}},
		{Kind: InputDrag, Drag: &InputDragSpec{
			From: &InputPoint{X: 0, Y: 0}, To: &InputPoint{X: 20, Y: 30}, Button: "left", DurationMS: 500,
		}},
	} {
		if err := action.Validate(geometry); err != nil {
			t.Fatalf("bounded %s rejected: %v", action.Kind, err)
		}
	}
	for _, point := range []*InputPoint{
		nil, {X: -1, Y: 0}, {X: 200, Y: 0}, {X: 0, Y: 100}, {X: math.NaN()}, {Y: math.Inf(1)},
	} {
		action := InputAction{Kind: InputClick, Click: &InputClickSpec{
			Target: InputSelector{Point: point}, Button: "left", Count: 1,
		}}
		if !errors.Is(action.Validate(geometry), ErrInvalid) {
			t.Fatal("missing/nonfinite/out-of-frame point accepted")
		}
	}
	if err := (InputAction{Kind: InputKey, Key: &InputKeySpec{Name: "escape"}}).Validate(Geometry{}); err == nil {
		t.Fatal("action accepted invalid evidence geometry")
	}
}

func TestInputRejectsAmbiguousUnknownAndUnboundedActions(t *testing.T) {
	geometry := Geometry{Width: 100, Height: 100, Scale: 1}
	for name, action := range map[string]InputAction{
		"empty":             {},
		"unknown":           {Kind: "shell", Key: &InputKeySpec{Name: "enter"}},
		"held key":          {Kind: "key_down", Key: &InputKeySpec{Name: "enter"}},
		"application close": {Kind: "close", Key: &InputKeySpec{Name: "enter"}},
		"wrong arm":         {Kind: InputClick, Key: &InputKeySpec{Name: "enter"}},
		"multiple arms":     {Kind: InputClick, Click: &InputClickSpec{}, Key: &InputKeySpec{Name: "enter"}},
		"two selectors": {Kind: InputClick, Click: &InputClickSpec{
			Target: InputSelector{Point: &InputPoint{}, ElementRef: "element_1"}, Button: "left", Count: 1,
		}},
		"native path":      {Kind: InputInsertText, Text: &InputTextSpec{ElementRef: "/native/element/1"}},
		"missing selector": {Kind: InputReplaceText, Text: &InputTextSpec{}},
		"bad button": {Kind: InputClick, Click: &InputClickSpec{
			Target: InputSelector{Point: &InputPoint{}}, Button: "automatic", Count: 1,
		}},
		"unbounded clicks": {Kind: InputClick, Click: &InputClickSpec{
			Target: InputSelector{Point: &InputPoint{}}, Button: "left", Count: 1000,
		}},
		"zero scroll": {Kind: InputScroll, Scroll: &InputScrollSpec{Point: &InputPoint{}}},
		"unbounded scroll": {Kind: InputScroll, Scroll: &InputScrollSpec{
			Point: &InputPoint{}, DeltaX: MaxInputScrollPixels + 1,
		}},
		"unbounded drag": {Kind: InputDrag, Drag: &InputDragSpec{
			From: &InputPoint{}, To: &InputPoint{X: 1}, Button: "left", DurationMS: MaxInputGestureMillis + 1,
		}},
		"no movement": {Kind: InputDrag, Drag: &InputDragSpec{
			From: &InputPoint{}, To: &InputPoint{}, Button: "left", DurationMS: 1,
		}},
	} {
		t.Run(name, func(t *testing.T) {
			if !errors.Is(action.Validate(geometry), ErrInvalid) {
				t.Fatal("malformed action accepted")
			}
		})
	}
	for _, key := range []InputKeySpec{
		{Name: "a"},
		{Name: "9"},
		{Name: "a", Modifiers: []InputModifier{InputShift}},
		{Name: "private text"},
		{Name: "f01"},
		{Name: "f0"},
		{Name: "f13"},
		{Name: ""},
		{Name: "enter", Modifiers: []InputModifier{InputAlt, InputAlt}},
		{Name: "enter", Modifiers: []InputModifier{"automatic"}},
	} {
		if key.valid() {
			t.Fatal("textual, malformed or duplicate-modifier key shape accepted")
		}
	}
}

func TestInputEvidencePreservesScopeAndRejectsChangedAuthority(t *testing.T) {
	session, evidence, request, now := inputFixture(t)
	if err := request.Validate(session, evidence, now); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*InputRequest){
		"request id":         func(r *InputRequest) { r.ID = "" },
		"observation":        func(r *InputRequest) { r.ObservationID = "different_observation" },
		"actor":              func(r *InputRequest) { r.Binding.Principal.ActorID = "different_actor" },
		"agent":              func(r *InputRequest) { r.Binding.Principal.AgentID = "different_agent" },
		"execution":          func(r *InputRequest) { r.Binding.Principal.ExecutionID = "different_execution" },
		"worker":             func(r *InputRequest) { r.Binding.WorkerID = "replacement_worker" },
		"policy":             func(r *InputRequest) { r.Binding.PolicyRevision = "new_policy" },
		"generation":         func(r *InputRequest) { r.Binding.Generation++ },
		"window incarnation": func(r *InputRequest) { r.Binding.Target.Scope.WindowGeneration++ },
	} {
		t.Run(name, func(t *testing.T) {
			changed := request
			mutate(&changed)
			if changed.Validate(session, evidence, now) == nil {
				t.Fatal("altered request binding accepted")
			}
		})
	}
	for _, state := range []ControlState{ControlHuman, ControlResumePending, ControlReleased, ControlLost} {
		changed := session
		changed.Control = state
		if !errors.Is(request.Validate(changed, evidence, now), ErrDenied) {
			t.Fatal("non-agent control admitted an input request")
		}
	}
	expired := evidence
	expired.ExpiresAt = now
	if !errors.Is(request.Validate(session, expired, now), ErrExpired) {
		t.Fatal("expired evidence accepted")
	}
	future := evidence
	future.CapturedAt = now.Add(time.Millisecond)
	if !errors.Is(request.Validate(session, future, now), ErrInvalid) {
		t.Fatal("future evidence accepted")
	}
	observation := contractObservation(session, now)
	observation.AccessibilityText = ""
	if _, err := InputEvidenceFromObservation(observation, session, now); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid source observation produced input evidence")
	}
	human, err := session.Transition(EventHandoff, session.Binding.Generation)
	if err != nil {
		t.Fatal(err)
	}
	pending, _ := human.Transition(EventRequestResume, human.Binding.Generation)
	resumed, _ := pending.Transition(EventResume, pending.Binding.Generation)
	if !errors.Is(request.Validate(resumed, evidence, now), ErrStale) {
		t.Fatal("pre-handoff evidence survived fresh resume")
	}
}

func TestInputTextIsEphemeralAndNotAReplacementFallback(t *testing.T) {
	session, evidence, request, now := inputFixture(t)
	request.Action = InputAction{Kind: InputInsertText, Text: &InputTextSpec{ElementRef: "field_1"}}
	const marker = "protected input Привет 世界 café 👩‍💻"
	request.Text = []byte(marker)
	if err := request.Validate(session, evidence, now); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), marker) ||
		strings.Contains(string(encoded), base64.StdEncoding.EncodeToString([]byte(marker))) {
		t.Fatal("protected input text escaped ordinary JSON")
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(encoded, &fields); err != nil || fields["Text"] != nil || fields["text"] != nil {
		t.Fatal("protected payload has an ordinary serialization field")
	}
	for _, value := range []string{
		fmt.Sprint(request), fmt.Sprintf("%+v", request), fmt.Sprintf("%#v", request), fmt.Sprintf("%#v", &request),
	} {
		if value != "computer input (protected payload omitted)" {
			t.Fatal("input diagnostics exposed fields rather than the fixed protected projection")
		}
	}
	var durable InputRequest
	if err = json.Unmarshal(encoded, &durable); err != nil {
		t.Fatal(err)
	}
	if durable.Validate(session, evidence, now) == nil {
		t.Fatal("text-free serialized metadata became an executable text request")
	}
	for _, payload := range [][]byte{nil, {}, {0xff}, {'a', 0, 'b'}, make([]byte, MaxInputTextBytes+1)} {
		request.Text = payload
		if !errors.Is(request.Validate(session, evidence, now), ErrInvalid) {
			t.Fatal("missing/empty/malformed/oversized insertion payload accepted")
		}
	}
	request.Action.Kind = InputReplaceText
	request.Text = []byte{}
	if err = request.Validate(session, evidence, now); err != nil {
		t.Fatal("explicit empty replacement should be representable:", err)
	}
	request.Text = nil
	if !errors.Is(request.Validate(session, evidence, now), ErrInvalid) {
		t.Fatal("missing replacement payload accepted")
	}
	request.Action = InputAction{Kind: InputKey, Key: &InputKeySpec{Name: "enter"}}
	request.Text = []byte{}
	if !errors.Is(request.Validate(session, evidence, now), ErrInvalid) {
		t.Fatal("non-text action carried an ephemeral text arm")
	}
}

func TestInputEvidenceMetadataContainsNoProtectedCapture(t *testing.T) {
	session, evidence, _, now := inputFixture(t)
	encoded, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		contractObservation(session, now).AccessibilityText, "screenshot", "accessibility_text",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatal("protected observation content entered input evidence metadata")
		}
	}
}

func TestInputGestureMustFitOriginalEvidenceLease(t *testing.T) {
	session, evidence, request, now := inputFixture(t)
	request.Action = InputAction{Kind: InputDrag, Drag: &InputDragSpec{
		From: &InputPoint{}, To: &InputPoint{X: 20, Y: 30}, Button: "left", DurationMS: 500,
	}}
	if err := request.Validate(session, evidence, now); err != nil {
		t.Fatal(err)
	}
	for _, remaining := range []time.Duration{time.Millisecond, 500 * time.Millisecond} {
		short := evidence
		short.ExpiresAt = now.Add(remaining)
		if !errors.Is(request.Validate(session, short, now), ErrExpired) {
			t.Fatal("gesture started without remaining evidence budget")
		}
	}
}
