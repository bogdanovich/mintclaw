package nodes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/nodes/internal/jsonstrict"
)

const (
	ComputerCommandAct = "computer.act.v1"
	// The decoded UTF-8 bound is independent of JSON's worst-case escaping.
	MaxComputerEphemeralInputBytes = computer.MaxInputTextBytes*6 + 128
)

// ComputerActInput is durable metadata, never native execution authority. Its
// request contains no text; the full immutable request plus ephemeral UTF-8 is
// committed together. The host must independently resolve its stored evidence.
type ComputerActInput struct {
	ComputerTurn
	Request     computer.InputRequest `json:"request"`
	WorkspaceID string                `json:"workspace_id"`
	TextDigest  string                `json:"text_digest,omitempty"`
	TextBytes   int                   `json:"text_bytes"`
}

type ComputerActEphemeralInput struct {
	Text string `json:"text"`
}

// ComputerActResult is live-only post-action evidence, not a claim that the
// user's business task succeeded. Replays retain only a protected receipt and
// require a fresh observation instead of reconstructing private app content.
type ComputerActResult struct {
	ActionID    string                    `json:"action_id"`
	Observation ComputerObservationResult `json:"observation"`
}

func (ComputerActResult) String() string          { return "computer action (protected evidence omitted)" }
func (result ComputerActResult) GoString() string { return result.String() }

type ComputerHostActRequest struct {
	Request     computer.InputRequest
	WorkspaceID string
}

func (ComputerHostActRequest) String() string           { return "computer action (protected input omitted)" }
func (request ComputerHostActRequest) GoString() string { return request.String() }

// ComputerInputCommandDescriptor is separately opt-in. The ordinary read-only
// descriptor family never advertises input merely because these types exist.
func ComputerInputCommandDescriptor(profiles []ComputerProfileDescriptor) (CommandDescriptor, error) {
	readOnly, err := ComputerCommandDescriptors(profiles)
	if err != nil {
		return CommandDescriptor{}, err
	}
	return CommandDescriptor{
		Name: ComputerCommandAct, Risk: RiskWrite, SupportsCancel: true,
		ComputerProfiles: readOnly[0].ComputerProfiles,
		InputSchema:      ComputerCommandInputSchema(ComputerCommandAct),
		OutputSchema:     ComputerCommandOutputSchema(ComputerCommandAct),
	}, nil
}

func (ComputerActEphemeralInput) String() string         { return "computer input (protected text omitted)" }
func (value ComputerActEphemeralInput) GoString() string { return value.String() }

func NewComputerActInputs(
	request computer.InputRequest, workspaceID string,
) (ComputerActInput, *ComputerActEphemeralInput, error) {
	if request.ValidateEnvelope() != nil {
		return ComputerActInput{}, nil, ErrInvalidInvocation
	}
	// Own the action's pointer/slice metadata; later caller mutation cannot
	// change an already prepared value. Ordinary JSON excludes request.Text.
	encoded, err := json.Marshal(request)
	if err != nil {
		return ComputerActInput{}, nil, ErrInvalidInvocation
	}
	var metadata computer.InputRequest
	if json.Unmarshal(encoded, &metadata) != nil {
		return ComputerActInput{}, nil, ErrInvalidInvocation
	}
	input := ComputerActInput{
		ComputerTurn: ComputerTurn{
			Runtime: request.Binding.Principal.Runtime, ExecutionID: request.Binding.Principal.ExecutionID,
		},
		Request: metadata, WorkspaceID: workspaceID,
	}
	var ephemeral *ComputerActEphemeralInput
	if computerTextAction(request.Action.Kind) {
		ephemeral = &ComputerActEphemeralInput{Text: string(request.Text)}
		input.TextBytes = len(request.Text)
		input.TextDigest, err = computerActTextDigest(input, ephemeral.Text)
		if err != nil {
			return ComputerActInput{}, nil, ErrInvalidInvocation
		}
	}
	if _, err = input.Bind(ephemeral); err != nil {
		return ComputerActInput{}, nil, err
	}
	return input, ephemeral, nil
}

func (input ComputerActInput) Validate() error {
	request := input.Request
	if request.ValidateMetadata() != nil || request.Text != nil ||
		!validInvocationIdentifier(request.ID) || !validInvocationIdentifier(request.ObservationID) ||
		!validInvocationIdentifier(input.WorkspaceID) || request.Binding.Target.Placement != computer.PlacementNode ||
		request.Binding.Target.Scope.WindowID == "" || !validSHA256Digest(request.Binding.PolicyRevision) ||
		input.Runtime != request.Binding.Principal.Runtime || input.ExecutionID != request.Binding.Principal.ExecutionID {
		return ErrInvalidInvocation
	}
	if computerTextAction(request.Action.Kind) {
		if !validSHA256Digest(input.TextDigest) || input.TextBytes < 0 ||
			input.TextBytes > computer.MaxInputTextBytes ||
			request.Action.Kind == computer.InputInsertText && input.TextBytes == 0 {
			return ErrInvalidInvocation
		}
	} else if input.TextBytes != 0 || input.TextDigest != "" {
		return ErrInvalidInvocation
	}
	return nil
}

func (input ComputerActInput) Bind(ephemeral *ComputerActEphemeralInput) (computer.InputRequest, error) {
	if input.Validate() != nil {
		return computer.InputRequest{}, ErrInvalidInvocation
	}
	request := input.Request
	if computerTextAction(request.Action.Kind) {
		if ephemeral == nil || len(ephemeral.Text) != input.TextBytes {
			return computer.InputRequest{}, ErrInvalidInvocation
		}
		// Preserve an explicit empty replacement, distinct from missing bytes.
		request.Text = make([]byte, len(ephemeral.Text))
		copy(request.Text, ephemeral.Text)
		if request.ValidateEnvelope() != nil {
			return computer.InputRequest{}, ErrInvalidInvocation
		}
		digest, err := computerActTextDigest(input, ephemeral.Text)
		if err != nil || digest != input.TextDigest {
			return computer.InputRequest{}, ErrInvalidInvocation
		}
	} else if ephemeral != nil || request.ValidateEnvelope() != nil {
		return computer.InputRequest{}, ErrInvalidInvocation
	}
	return request, nil
}

func computerTextAction(kind computer.InputKind) bool {
	return kind == computer.InputInsertText || kind == computer.InputReplaceText
}

func computerActTextDigest(input ComputerActInput, text string) (string, error) {
	encoded, err := json.Marshal(struct {
		Domain      string                `json:"domain"`
		Turn        ComputerTurn          `json:"turn"`
		Request     computer.InputRequest `json:"request"`
		WorkspaceID string                `json:"workspace_id"`
		TextBytes   int                   `json:"text_bytes"`
		Text        string                `json:"text"`
	}{"mintclaw.computer.input.v1", input.ComputerTurn, input.Request, input.WorkspaceID, input.TextBytes, text})
	if err != nil {
		return "", ErrInvalidInvocation
	}
	defer clear(encoded)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// BindComputerAct verifies payload commitment and authenticated plan ownership
// before ledger acceptance. The caller must still authorize the canonical plan;
// this function does not supply host policy, freshness, geometry or native scope.
func BindComputerAct(plan ExecutionPlan, ephemeralInput json.RawMessage) (computer.InputRequest, error) {
	var input ComputerActInput
	if plan.Command != ComputerCommandAct || !utf8.Valid(plan.Input) {
		return computer.InputRequest{}, ErrCommandDenied
	}
	if _, err := canonicalInvocationInput(plan.Input); err != nil || decodeComputerJSON(plan.Input, &input) != nil {
		return computer.InputRequest{}, ErrCommandDenied
	}
	principal, err := input.Principal(plan)
	if err != nil || input.Request.Binding.Principal != principal || input.Request.ID != plan.InvocationID ||
		input.Request.Binding.Target.Node != string(plan.NodeID) {
		return computer.InputRequest{}, ErrCommandDenied
	}
	var ephemeral *ComputerActEphemeralInput
	if len(ephemeralInput) != 0 {
		if len(ephemeralInput) > MaxComputerEphemeralInputBytes || !utf8.Valid(ephemeralInput) {
			return computer.InputRequest{}, ErrCommandDenied
		}
		value, decodeErr := jsonstrict.Decode(ephemeralInput)
		if decodeErr != nil {
			return computer.InputRequest{}, ErrCommandDenied
		}
		object, ok := value.(map[string]any)
		if !ok || len(object) != 1 {
			return computer.InputRequest{}, ErrCommandDenied
		}
		text, ok := object["text"].(string)
		if !ok {
			return computer.InputRequest{}, ErrCommandDenied
		}
		ephemeral = &ComputerActEphemeralInput{Text: text}
	}
	request, err := input.Bind(ephemeral)
	if err != nil {
		return computer.InputRequest{}, ErrCommandDenied
	}
	return request, nil
}
