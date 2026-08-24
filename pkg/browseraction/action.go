package browseraction

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
)

const (
	MaxIdentifierBytes = 128
	MaxURLBytes        = 2048
	MaxScrollAmount    = 5
)

var (
	ErrInvalid       = errors.New("invalid browser state")
	identifierRegexp = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
)

type ActionKind string

const (
	ActionNavigate    ActionKind = "navigate"
	ActionClick       ActionKind = "click"
	ActionFill        ActionKind = "fill"
	ActionSelect      ActionKind = "select"
	ActionPress       ActionKind = "press"
	ActionScroll      ActionKind = "scroll"
	ActionDialog      ActionKind = "dialog"
	ActionCheck       ActionKind = "check"
	ActionUncheck     ActionKind = "uncheck"
	ActionHover       ActionKind = "hover"
	ActionDrag        ActionKind = "drag"
	ActionFileChooser ActionKind = "file_chooser"
	ActionUpload      ActionKind = "upload"
	ActionDownload    ActionKind = "download"
)

var (
	currentKinds = [...]ActionKind{
		ActionNavigate,
		ActionClick,
		ActionFill,
		ActionSelect,
		ActionPress,
		ActionScroll,
		ActionDialog,
		ActionCheck,
		ActionUncheck,
		ActionHover,
		ActionDrag,
		ActionFileChooser,
		ActionUpload,
		ActionDownload,
	}
	currentKeys = [...]string{
		"Enter",
		"Space",
		"Escape",
		"Tab",
		"Shift+Tab",
		"ArrowUp",
		"ArrowDown",
		"ArrowLeft",
		"ArrowRight",
		"Home",
		"End",
		"PageUp",
		"PageDown",
		"Backspace",
		"Delete",
	}
)

// Kinds returns the complete current action vocabulary in protocol order.
func Kinds() []ActionKind {
	return append([]ActionKind(nil), currentKinds[:]...)
}

func (kind ActionKind) Valid() bool {
	for _, current := range currentKinds {
		if kind == current {
			return true
		}
	}
	return false
}

type Action struct {
	Kind           ActionKind `json:"kind"`
	URL            string     `json:"url,omitempty"`
	Ref            string     `json:"ref,omitempty"`
	SourceRef      string     `json:"source_ref,omitempty"`
	DestinationRef string     `json:"destination_ref,omitempty"`
	DialogID       string     `json:"dialog_id,omitempty"`
	Target         string     `json:"target,omitempty"`
	Value          string     `json:"value,omitempty"`
	Key            string     `json:"key,omitempty"`
	Direction      string     `json:"direction,omitempty"`
	Amount         int        `json:"amount,omitempty"`
	Decision       string     `json:"decision,omitempty"`
	PromptProvided bool       `json:"prompt_provided,omitempty"`
	ArtifactRef    string     `json:"artifact_ref,omitempty"`
	Deliver        bool       `json:"deliver,omitempty"`
}

func (action *Action) UnmarshalJSON(data []byte) error {
	type actionWire struct {
		Kind           ActionKind      `json:"kind"`
		URL            string          `json:"url,omitempty"`
		Ref            string          `json:"ref,omitempty"`
		SourceRef      string          `json:"source_ref,omitempty"`
		DestinationRef string          `json:"destination_ref,omitempty"`
		DialogID       string          `json:"dialog_id,omitempty"`
		Target         string          `json:"target,omitempty"`
		Value          string          `json:"value,omitempty"`
		Key            string          `json:"key,omitempty"`
		Direction      string          `json:"direction,omitempty"`
		Amount         json.RawMessage `json:"amount,omitempty"`
		Decision       string          `json:"decision,omitempty"`
		PromptProvided bool            `json:"prompt_provided,omitempty"`
		ArtifactRef    string          `json:"artifact_ref,omitempty"`
		Deliver        bool            `json:"deliver,omitempty"`
	}
	var value actionWire
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	amount, err := decodeActionAmount(value.Amount)
	if err != nil {
		return err
	}
	*action = Action{
		Kind: value.Kind, URL: value.URL, Ref: value.Ref, SourceRef: value.SourceRef,
		DestinationRef: value.DestinationRef, DialogID: value.DialogID, Target: value.Target,
		Value: value.Value, Key: value.Key, Direction: value.Direction, Amount: amount,
		Decision: value.Decision, PromptProvided: value.PromptProvided,
		ArtifactRef: value.ArtifactRef, Deliver: value.Deliver,
	}
	return nil
}

func decodeActionAmount(data json.RawMessage) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	value, ok := new(big.Rat).SetString(string(data))
	if !ok || !value.IsInt() || value.Sign() < 0 || !value.Num().IsInt64() || value.Num().Int64() > MaxScrollAmount {
		return 0, fmt.Errorf("%w: browser action amount must be an integer from 0 to %d", ErrInvalid, MaxScrollAmount)
	}
	return int(value.Num().Int64()), nil
}

func (action *Action) Validate(maxTextBytes int) error {
	if !action.Kind.Valid() || len(action.URL) > MaxURLBytes || len(action.Value) > maxTextBytes {
		return fmt.Errorf("%w: malformed browser action", ErrInvalid)
	}
	if (action.Kind != ActionDrag && (action.SourceRef != "" || action.DestinationRef != "")) ||
		(action.Kind != ActionDialog && action.DialogID != "") ||
		(action.Kind == ActionDialog && !validIdentifier(action.DialogID)) {
		return fmt.Errorf("%w: malformed browser authority", ErrInvalid)
	}
	if (action.Kind != ActionFileChooser && action.Kind != ActionUpload && action.ArtifactRef != "") ||
		(action.Kind != ActionDownload && action.Deliver) {
		return fmt.Errorf("%w: malformed browser artifact action", ErrInvalid)
	}
	switch action.Kind {
	case ActionNavigate:
		if action.URL == "" || action.Ref != "" || action.Target != "" || action.Value != "" || action.Key != "" ||
			action.Direction != "" || action.Decision != "" || action.PromptProvided || action.Amount != 0 {
			return fmt.Errorf("%w: malformed navigate action", ErrInvalid)
		}
	case ActionClick:
		if !validIdentifier(action.Ref) || action.URL != "" || action.Target != "" || action.Value != "" ||
			action.Key != "" || action.Decision != "" || action.PromptProvided || action.Direction != "" ||
			action.Amount != 0 {
			return fmt.Errorf("%w: malformed click action", ErrInvalid)
		}
	case ActionCheck, ActionUncheck, ActionHover:
		if !validIdentifier(action.Ref) || action.URL != "" || action.Target != "" || action.Value != "" ||
			action.Key != "" || action.Direction != "" || action.Decision != "" || action.PromptProvided ||
			action.Amount != 0 || action.SourceRef != "" || action.DestinationRef != "" || action.DialogID != "" {
			return fmt.Errorf("%w: malformed %s action", ErrInvalid, action.Kind)
		}
	case ActionDrag:
		if !validIdentifier(action.SourceRef) || !validIdentifier(action.DestinationRef) ||
			action.SourceRef == action.DestinationRef || action.Ref != "" || action.URL != "" || action.Target != "" ||
			action.Value != "" || action.Key != "" || action.Direction != "" || action.Decision != "" ||
			action.PromptProvided || action.Amount != 0 || action.DialogID != "" {
			return fmt.Errorf("%w: malformed drag action", ErrInvalid)
		}
	case ActionFill, ActionSelect:
		if !validIdentifier(action.Ref) || action.URL != "" || action.Target != "" || action.Key != "" ||
			action.Direction != "" || action.Decision != "" || action.PromptProvided ||
			action.Amount != 0 {
			return fmt.Errorf("%w: malformed %s action", ErrInvalid, action.Kind)
		}
	case ActionPress:
		if action.URL != "" || action.Ref != "" || action.Target != "document" || action.Value != "" ||
			!ValidKey(action.Key) || action.Decision != "" || action.PromptProvided || action.Direction != "" ||
			action.Amount != 0 {
			return fmt.Errorf("%w: malformed press action", ErrInvalid)
		}
	case ActionScroll:
		if action.URL != "" || action.Ref != "" || action.Target != "" || action.Value != "" || action.Key != "" ||
			action.Decision != "" || action.PromptProvided ||
			(action.Direction != "up" && action.Direction != "down") ||
			action.Amount < 1 || action.Amount > MaxScrollAmount {
			return fmt.Errorf("%w: malformed scroll action", ErrInvalid)
		}
	case ActionDialog:
		if action.URL != "" || action.Ref != "" || action.Target != "" || action.Key != "" || action.Direction != "" ||
			action.Amount != 0 || (action.Decision != "accept" && action.Decision != "dismiss") ||
			(action.Decision == "dismiss" && (action.Value != "" || action.PromptProvided)) ||
			(!action.PromptProvided && action.Value != "") {
			return fmt.Errorf("%w: malformed dialog action", ErrInvalid)
		}
	case ActionFileChooser:
		if !validIdentifier(action.Ref) || !strings.HasPrefix(action.ArtifactRef, "transfer-artifact://") ||
			len(action.ArtifactRef) > 512 || action.URL != "" || action.Target != "" || action.Value != "" ||
			action.Key != "" || action.Direction != "" || action.Amount != 0 || action.Decision != "" ||
			action.PromptProvided || action.Deliver || action.SourceRef != "" || action.DestinationRef != "" ||
			action.DialogID != "" {
			return fmt.Errorf("%w: malformed file chooser action", ErrInvalid)
		}
	case ActionUpload:
		if !validIdentifier(action.Ref) || !strings.HasPrefix(action.ArtifactRef, "transfer-artifact://") ||
			len(action.ArtifactRef) > 512 || action.URL != "" || action.Target != "" || action.Value != "" ||
			action.Key != "" || action.Direction != "" || action.Amount != 0 || action.Decision != "" ||
			action.PromptProvided || action.Deliver || action.SourceRef != "" || action.DestinationRef != "" ||
			action.DialogID != "" {
			return fmt.Errorf("%w: malformed upload action", ErrInvalid)
		}
	case ActionDownload:
		if !validIdentifier(action.Ref) || action.ArtifactRef != "" || action.URL != "" || action.Target != "" ||
			action.Value != "" || action.Key != "" || action.Direction != "" || action.Amount != 0 ||
			action.Decision != "" || action.PromptProvided {
			return fmt.Errorf("%w: malformed download action", ErrInvalid)
		}
	}
	return nil
}

func validIdentifier(value string) bool {
	return len(value) <= MaxIdentifierBytes && identifierRegexp.MatchString(value)
}

// Keys returns the complete current document-scoped key vocabulary.
func Keys() []string {
	return append([]string(nil), currentKeys[:]...)
}

func ValidKey(key string) bool {
	for _, current := range currentKeys {
		if key == current {
			return true
		}
	}
	return false
}

// Schema projects the current action vocabulary into JSON Schema. Strict
// model-facing callers receive one exclusive branch per kind. Tolerant
// transport callers receive one additive flat object and the derived
// prompt-presence bit for forward compatibility.
func Schema(kinds []ActionKind, maxTextBytes int, allowUnknownFields bool) map[string]any {
	if !allowUnknownFields {
		branches := make([]any, 0, len(kinds))
		for _, kind := range kinds {
			branches = append(branches, strictSchemaBranch(kind, maxTextBytes))
		}
		return map[string]any{"oneOf": branches}
	}
	return flatSchema(kinds, maxTextBytes, true)
}

func flatSchema(kinds []ActionKind, maxTextBytes int, allowUnknownFields bool) map[string]any {
	properties := map[string]any{
		"kind": map[string]any{"type": "string", "enum": actionKindStrings(kinds)},
	}
	fields := schemaFields(kinds)
	identifier := map[string]any{"type": "string", "minLength": 1, "maxLength": MaxIdentifierBytes}
	if fields&schemaURL != 0 {
		properties["url"] = map[string]any{"type": "string", "minLength": 1, "maxLength": MaxURLBytes}
	}
	if fields&schemaRef != 0 {
		properties["ref"] = identifier
	}
	if fields&schemaDragRefs != 0 {
		properties["source_ref"] = identifier
		properties["destination_ref"] = identifier
	}
	if fields&schemaDialog != 0 {
		properties["dialog_id"] = identifier
		properties["decision"] = map[string]any{"type": "string", "enum": []string{"accept", "dismiss"}}
		if allowUnknownFields {
			properties["prompt_provided"] = map[string]any{"type": "boolean"}
		}
	}
	if fields&schemaTargetKey != 0 {
		properties["target"] = map[string]any{"type": "string", "enum": []string{"document"}}
		properties["key"] = map[string]any{"type": "string", "enum": Keys()}
	}
	if fields&schemaValue != 0 {
		properties["value"] = map[string]any{"type": "string", "maxLength": maxTextBytes}
	}
	if fields&schemaScroll != 0 {
		properties["direction"] = map[string]any{"type": "string", "enum": []string{"up", "down"}}
		properties["amount"] = map[string]any{"type": "integer", "minimum": 1, "maximum": MaxScrollAmount}
	}
	if fields&schemaArtifact != 0 {
		properties["artifact_ref"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 512}
	}
	if fields&schemaDeliver != 0 {
		properties["deliver"] = map[string]any{"type": "boolean"}
	}
	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             []string{"kind"},
		"additionalProperties": allowUnknownFields,
	}
}

func strictSchemaBranch(kind ActionKind, maxTextBytes int) map[string]any {
	branch := flatSchema([]ActionKind{kind}, maxTextBytes, false)
	properties := branch["properties"].(map[string]any)
	properties["kind"] = map[string]any{"type": "string", "const": string(kind)}
	required := []string{"kind"}
	switch kind {
	case ActionNavigate:
		required = append(required, "url")
	case ActionClick, ActionCheck, ActionUncheck, ActionHover, ActionDownload:
		required = append(required, "ref")
	case ActionFill, ActionSelect:
		required = append(required, "ref", "value")
		properties["value"].(map[string]any)["minLength"] = 1
	case ActionPress:
		required = append(required, "target", "key")
	case ActionScroll:
		required = append(required, "direction", "amount")
	case ActionDialog:
		required = append(required, "dialog_id", "decision")
	case ActionDrag:
		required = append(required, "source_ref", "destination_ref")
	case ActionFileChooser, ActionUpload:
		required = append(required, "ref", "artifact_ref")
	}
	branch["required"] = required
	return branch
}

// DecodeModelAction parses the strict model-facing wire contract. It rejects
// unknown, cross-kind, mistyped, and incomplete fields before any browser
// preparation can observe the request.
func DecodeModelAction(raw any, maxTextBytes int) (Action, error) {
	args, ok := raw.(map[string]any)
	if !ok {
		return Action{}, ErrInvalid
	}
	kindValue, ok := args["kind"].(string)
	kind := ActionKind(kindValue)
	if !ok || !kind.Valid() {
		return Action{}, ErrInvalid
	}
	branch := strictSchemaBranch(kind, maxTextBytes)
	properties := branch["properties"].(map[string]any)
	for _, name := range branch["required"].([]string) {
		if _, present := args[name]; !present {
			return Action{}, ErrInvalid
		}
	}
	action := Action{Kind: kind}
	for name, value := range args {
		property, admitted := properties[name].(map[string]any)
		if !admitted {
			return Action{}, ErrInvalid
		}
		switch property["type"] {
		case "string":
			text, valid := value.(string)
			if !valid {
				return Action{}, ErrInvalid
			}
			assignModelActionString(&action, name, text)
		case "boolean":
			boolean, valid := value.(bool)
			if !valid || name != "deliver" {
				return Action{}, ErrInvalid
			}
			action.Deliver = boolean
		case "integer":
			amount, valid := decodeModelActionAmount(value)
			if !valid || name != "amount" {
				return Action{}, ErrInvalid
			}
			action.Amount = amount
		default:
			return Action{}, ErrInvalid
		}
	}
	if action.Kind == ActionDialog {
		_, action.PromptProvided = args["value"]
	}
	if (action.Kind == ActionFill || action.Kind == ActionSelect) && action.Value == "" {
		return Action{}, ErrInvalid
	}
	if err := action.Validate(maxTextBytes); err != nil {
		return Action{}, ErrInvalid
	}
	return action, nil
}

func assignModelActionString(action *Action, name, value string) {
	switch name {
	case "kind":
	case "url":
		action.URL = value
	case "ref":
		action.Ref = value
	case "source_ref":
		action.SourceRef = value
	case "destination_ref":
		action.DestinationRef = value
	case "dialog_id":
		action.DialogID = value
	case "target":
		action.Target = value
	case "value":
		action.Value = value
	case "key":
		action.Key = value
	case "direction":
		action.Direction = value
	case "decision":
		action.Decision = value
	case "artifact_ref":
		action.ArtifactRef = value
	}
}

func decodeModelActionAmount(value any) (int, bool) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return 0, false
	}
	amount, err := decodeActionAmount(encoded)
	return amount, err == nil
}

type schemaField uint16

const (
	schemaURL schemaField = 1 << iota
	schemaRef
	schemaDragRefs
	schemaDialog
	schemaTargetKey
	schemaValue
	schemaScroll
	schemaArtifact
	schemaDeliver
)

func schemaFields(kinds []ActionKind) schemaField {
	var fields schemaField
	for _, kind := range kinds {
		switch kind {
		case ActionNavigate:
			fields |= schemaURL
		case ActionClick, ActionCheck, ActionUncheck, ActionHover:
			fields |= schemaRef
		case ActionFill, ActionSelect:
			fields |= schemaRef | schemaValue
		case ActionPress:
			fields |= schemaTargetKey
		case ActionScroll:
			fields |= schemaScroll
		case ActionDialog:
			fields |= schemaDialog | schemaValue
		case ActionDrag:
			fields |= schemaDragRefs
		case ActionFileChooser, ActionUpload:
			fields |= schemaRef | schemaArtifact
		case ActionDownload:
			fields |= schemaRef | schemaDeliver
		}
	}
	return fields
}

func actionKindStrings(kinds []ActionKind) []string {
	values := make([]string, len(kinds))
	for index, kind := range kinds {
		values[index] = string(kind)
	}
	return values
}
