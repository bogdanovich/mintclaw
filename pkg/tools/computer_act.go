package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/invopop/jsonschema"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	"github.com/bogdanovich/mintclaw/pkg/tools/loopguard"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

// ComputerActRequest is an action proposal. The source supplies the binding
// and checks its own observation and current policy before dispatch.
type ComputerActRequest struct {
	SessionID     string
	ObservationID string
	RequestID     string
	Action        computer.InputAction
	Text          []byte
}

func (ComputerActRequest) String() string { return "computer action proposal (protected text omitted)" }
func (ComputerActRequest) GoString() string {
	return "computer action proposal (protected text omitted)"
}

type ComputerInputToolSource interface {
	Available() bool
	Act(context.Context, runtimecap.Principal, ComputerActRequest) (computer.Observation, error)
}

type ComputerActTool struct {
	options ComputerToolOptions
	source  ComputerInputToolSource
}

func NewComputerActTool(options ComputerToolOptions, source ComputerInputToolSource) *ComputerActTool {
	return &ComputerActTool{options: options, source: source}
}

func (tool *ComputerActTool) ToolEnabledForAgent(agent string) bool {
	if !tool.options.policy.Enabled || tool.source == nil {
		return false
	}
	for _, target := range tool.options.policy.Targets {
		if target.AllowInput && slices.Contains(target.Agents, agent) {
			return true
		}
	}
	return false
}

type computerActArguments struct {
	SessionID     string               `json:"computer_session_id"`
	ObservationID string               `json:"observation_id"`
	Action        computer.InputAction `json:"action"`
	Text          *string              `json:"text,omitempty"      jsonschema:"maxLength=16384"`
}

var computerActToolSchema = sync.OnceValue(func() []byte {
	reflector := jsonschema.Reflector{DoNotReference: true, ExpandedStruct: true}
	raw, err := json.Marshal(reflector.Reflect(computerActArguments{}))
	if err != nil {
		panic(err)
	}
	return raw
})

func (*ComputerActTool) Name() string { return "computer_act" }

func (*ComputerActTool) Description() string {
	return "Perform one bounded action in the selected window using its latest observation. " +
		"Supply the computer session and observation IDs. Use an observation-issued element reference " +
		"for text, protected text for insert_text or replace_text, and original pixel coordinates for " +
		"point actions. The action kinds are click, insert_text, replace_text, key, scroll, and drag. " +
		"The companion profile may support a subset. If an outcome is uncertain, inspect current state without " +
		"repeating the action."
}

func (*ComputerActTool) Parameters() map[string]any {
	var schema map[string]any
	if err := json.Unmarshal(computerActToolSchema(), &schema); err != nil {
		panic(err)
	}
	return schema
}

func (*ComputerActTool) ToolLoopSemantics() loopguard.Semantics { return loopguard.SemanticsMutating }

func (*ComputerActTool) ProtectedDurableArguments(map[string]any) bool { return true }
func (*ComputerActTool) ProtectedDurableResult(map[string]any) bool    { return true }

func decodeComputerAct(args map[string]any) (computerActArguments, error) {
	var input computerActArguments
	if value, supplied := args["text"]; supplied {
		text, ok := value.(string)
		if !ok || len(text) > computer.MaxInputTextBytes || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
			return input, computer.ErrInvalid
		}
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return input, computer.ErrInvalid
	}
	defer clear(raw)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || nodes.ID(input.SessionID).Validate() != nil ||
		nodes.ID(input.ObservationID).Validate() != nil || input.Action.ValidateShape() != nil {
		return computerActArguments{}, computer.ErrInvalid
	}
	textAction := input.Action.Kind == computer.InputInsertText || input.Action.Kind == computer.InputReplaceText
	_, textSupplied := args["text"]
	if textSupplied != textAction || input.Action.Kind == computer.InputInsertText &&
		(input.Text == nil || *input.Text == "") {
		return computerActArguments{}, computer.ErrInvalid
	}
	return input, nil
}

func (*ComputerActTool) DurableArguments(args map[string]any) (map[string]any, error) {
	input, err := decodeComputerAct(args)
	if err != nil {
		return nil, err
	}
	if input.Text != nil {
		redacted := "*"
		input.Text = &redacted
	}
	return computerActArgumentsMap(input)
}

func (*ComputerActTool) CanonicalArguments(args map[string]any) (map[string]any, error) {
	input, err := decodeComputerAct(args)
	if err != nil {
		return nil, err
	}
	return computerActArgumentsMap(input)
}

func computerActArgumentsMap(input computerActArguments) (map[string]any, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, computer.ErrInvalid
	}
	defer clear(raw)
	var projected map[string]any
	if json.Unmarshal(raw, &projected) != nil {
		return nil, computer.ErrInvalid
	}
	return projected, nil
}

func (tool *ComputerActTool) Execute(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	principal, err := computerPrincipal(ctx)
	if err != nil || !tool.ToolEnabledForAgent(principal.AgentID) || !tool.source.Available() {
		return computerToolError(computer.ErrDenied)
	}
	input, err := decodeComputerAct(args)
	if err != nil {
		return computerToolError(err)
	}
	requestID, err := computerToolRequestID(ctx)
	if err != nil {
		return computerToolError(err)
	}
	var text []byte
	if input.Text != nil {
		text = []byte(*input.Text)
		defer clear(text)
	}
	observation, err := tool.source.Act(ctx, principal, ComputerActRequest{
		SessionID: input.SessionID, ObservationID: input.ObservationID, RequestID: requestID,
		Action: input.Action, Text: text,
	})
	if err != nil {
		return computerToolError(err)
	}
	if observation.ID == input.ObservationID {
		clear(observation.Screenshot)
		return computerToolError(computer.ErrStale)
	}
	return computerObservationToolResult(observation, principal, input.SessionID)
}
