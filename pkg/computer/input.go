package computer

import (
	"bytes"
	"strconv"
	"time"
	"unicode/utf8"
)

// Input contracts are side-effect-free shapes, not an input host or permission.
// A future host must resolve its stored observation, enforce local policy and
// desktop-wide exclusion, and revalidate native scope immediately before input.
type InputKind string

const (
	InputClick       InputKind = "click"
	InputInsertText  InputKind = "insert_text"
	InputReplaceText InputKind = "replace_text"
	InputKey         InputKind = "key"
	InputScroll      InputKind = "scroll"
	InputDrag        InputKind = "drag"

	MaxInputTextBytes     = 16 * 1024
	MaxInputScrollPixels  = 2048
	MaxInputGestureMillis = 2000
)

// InputPoint is in the original observation's pixel coordinates, not desktop
// coordinates. Native conversion must use that same trusted geometry.
type InputPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

func (point *InputPoint) valid(geometry Geometry) bool {
	if point == nil {
		return false
	}
	_, _, err := geometry.DesktopPoint(point.X, point.Y)
	return err == nil
}

// InputSelector uses exactly one pixel point or opaque host-issued reference
// from this observation. An element reference is never a label, native path or
// model-supplied vendor token. The host must resolve it from its own evidence.
type InputSelector struct {
	Point      *InputPoint `json:"point,omitempty"`
	ElementRef string      `json:"element_ref,omitempty"`
}

func (selector InputSelector) valid(geometry Geometry) bool {
	if selector.ElementRef != "" {
		return selector.Point == nil && validIdentifier(selector.ElementRef)
	}
	return selector.Point.valid(geometry)
}

type InputClickSpec struct {
	Target InputSelector `json:"target"`
	Button string        `json:"button"`
	Count  int           `json:"count"`
}

type InputTextSpec struct {
	ElementRef string `json:"element_ref"`
}

type InputModifier string

const (
	InputControl InputModifier = "control"
	InputAlt     InputModifier = "alt"
	InputShift   InputModifier = "shift"
	InputMeta    InputModifier = "meta"
)

type InputKeySpec struct {
	Name      string          `json:"name"`
	Modifiers []InputModifier `json:"modifiers,omitempty"`
}

func (key InputKeySpec) valid() bool {
	if len(key.Modifiers) > 4 {
		return false
	}
	seen := make(map[InputModifier]bool, len(key.Modifiers))
	shortcut := false
	for _, modifier := range key.Modifiers {
		if seen[modifier] {
			return false
		}
		seen[modifier] = true
		switch modifier {
		case InputControl, InputAlt, InputMeta:
			shortcut = true
		case InputShift:
		default:
			return false
		}
	}
	// Plain printable text belongs in the protected text payload, not in a
	// sequence of durable key-name fields. Shortcut semantics still need policy.
	if len(key.Name) == 1 && (key.Name[0] >= 'a' && key.Name[0] <= 'z' ||
		key.Name[0] >= '0' && key.Name[0] <= '9') {
		return shortcut
	}
	switch key.Name {
	case "enter", "tab", "escape", "backspace", "delete", "space", "home", "end",
		"page_up", "page_down", "arrow_left", "arrow_right", "arrow_up", "arrow_down":
		return true
	}
	if len(key.Name) >= 2 && len(key.Name) <= 3 && key.Name[0] == 'f' {
		number, err := strconv.Atoi(key.Name[1:])
		return err == nil && number >= 1 && number <= 12 && key.Name == "f"+strconv.Itoa(number)
	}
	return false
}

type InputScrollSpec struct {
	Point  *InputPoint `json:"point"`
	DeltaX int         `json:"delta_x"`
	DeltaY int         `json:"delta_y"`
}

type InputDragSpec struct {
	From       *InputPoint `json:"from"`
	To         *InputPoint `json:"to"`
	Button     string      `json:"button"`
	DurationMS int         `json:"duration_ms"`
}

// InputAction is a tagged union with exactly one matching arm. Unknown or
// inactive arms fail closed. There are no raw native selectors or held-key /
// held-button operations; supported native mechanisms remain independently gated.
type InputAction struct {
	Kind   InputKind        `json:"kind"`
	Click  *InputClickSpec  `json:"click,omitempty"`
	Text   *InputTextSpec   `json:"text,omitempty"`
	Key    *InputKeySpec    `json:"key,omitempty"`
	Scroll *InputScrollSpec `json:"scroll,omitempty"`
	Drag   *InputDragSpec   `json:"drag,omitempty"`
}

func (action InputAction) Validate(geometry Geometry) error {
	if geometry.Validate() != nil {
		return ErrInvalid
	}
	arms := 0
	for _, present := range []bool{
		action.Click != nil, action.Text != nil, action.Key != nil, action.Scroll != nil, action.Drag != nil,
	} {
		if present {
			arms++
		}
	}
	if arms != 1 {
		return ErrInvalid
	}
	valid := false
	switch action.Kind {
	case InputClick:
		valid = action.Click != nil && action.Click.Target.valid(geometry) &&
			validInputButton(action.Click.Button) && action.Click.Count >= 1 && action.Click.Count <= 2
	case InputInsertText, InputReplaceText:
		valid = action.Text != nil && validIdentifier(action.Text.ElementRef)
	case InputKey:
		valid = action.Key != nil && action.Key.valid()
	case InputScroll:
		valid = action.Scroll != nil && action.Scroll.Point.valid(geometry) &&
			(action.Scroll.DeltaX != 0 || action.Scroll.DeltaY != 0) &&
			action.Scroll.DeltaX >= -MaxInputScrollPixels && action.Scroll.DeltaX <= MaxInputScrollPixels &&
			action.Scroll.DeltaY >= -MaxInputScrollPixels && action.Scroll.DeltaY <= MaxInputScrollPixels
	case InputDrag:
		valid = action.Drag != nil && action.Drag.From.valid(geometry) && action.Drag.To.valid(geometry) &&
			*action.Drag.From != *action.Drag.To && validInputButton(action.Drag.Button) &&
			action.Drag.DurationMS >= 1 && action.Drag.DurationMS <= MaxInputGestureMillis
	}
	if !valid {
		return ErrInvalid
	}
	return nil
}

func validInputButton(button string) bool {
	return button == "left" || button == "right" || button == "middle"
}

// InputEvidence is payload-free metadata for a host-owned observation. It is
// not a bearer grant: a caller-authored copy must never replace the host record.
type InputEvidence struct {
	ID         string    `json:"id"`
	Binding    Binding   `json:"binding"`
	Geometry   Geometry  `json:"geometry"`
	CapturedAt time.Time `json:"captured_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

func InputEvidenceFromObservation(observation Observation, session Session, now time.Time) (InputEvidence, error) {
	if err := observation.Validate(session, now); err != nil {
		return InputEvidence{}, err
	}
	return InputEvidence{
		ID: observation.ID, Binding: observation.Binding, Geometry: observation.Geometry,
		CapturedAt: observation.CapturedAt, ExpiresAt: observation.ExpiresAt,
	}, nil
}

func (evidence InputEvidence) Validate(session Session, now time.Time) error {
	return validateObservationMetadata(
		evidence.ID, evidence.Binding, evidence.Geometry, evidence.CapturedAt, evidence.ExpiresAt, session, now,
	)
}

// InputRequest separates protected text from serializable action metadata.
// A transport must use an explicitly bound ephemeral input envelope, not marshal
// Text into an execution plan. Validation is not local admission or execution.
type InputRequest struct {
	ID            string      `json:"id"`
	Binding       Binding     `json:"binding"`
	ObservationID string      `json:"observation_id"`
	Action        InputAction `json:"action"`
	Text          []byte      `json:"-"`
}

func (request InputRequest) String() string   { return "computer input (protected payload omitted)" }
func (request InputRequest) GoString() string { return request.String() }

func (request InputRequest) Validate(session Session, evidence InputEvidence, now time.Time) error {
	if !validIdentifier(request.ID) || request.Binding.Validate() != nil || !validIdentifier(request.ObservationID) {
		return ErrInvalid
	}
	if err := evidence.Validate(session, now); err != nil {
		return err
	}
	if request.Binding != evidence.Binding || request.ObservationID != evidence.ID {
		return ErrStale
	}
	if err := request.Action.Validate(evidence.Geometry); err != nil {
		return err
	}
	textAction := request.Action.Kind == InputInsertText || request.Action.Kind == InputReplaceText
	if textAction {
		if request.Text == nil || len(request.Text) > MaxInputTextBytes || !utf8.Valid(request.Text) ||
			bytes.IndexByte(request.Text, 0) >= 0 || request.Action.Kind == InputInsertText && len(request.Text) == 0 {
			return ErrInvalid
		}
	} else if request.Text != nil {
		return ErrInvalid
	}
	if drag := request.Action.Drag; drag != nil &&
		!now.Add(time.Duration(drag.DurationMS)*time.Millisecond).Before(evidence.ExpiresAt) {
		return ErrExpired
	}
	return nil
}
