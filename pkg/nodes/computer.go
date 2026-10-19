package nodes

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/invopop/jsonschema"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap/identity"
)

const (
	ComputerCommandTargets       = "computer.targets.v1"
	ComputerCommandOpen          = "computer.session.open.v1"
	ComputerCommandStatus        = "computer.session.status.v1"
	ComputerCommandObserve       = "computer.observe.v1"
	ComputerCommandHandoff       = "computer.session.handoff.v1"
	ComputerCommandRequestResume = "computer.session.request_resume.v1"
	ComputerCommandResume        = "computer.session.resume.v1"
	ComputerCommandRelease       = "computer.session.release.v1"
)

var computerCommands = []struct {
	name string
	risk Risk
}{
	{ComputerCommandTargets, RiskRead},
	{ComputerCommandOpen, RiskWrite},
	{ComputerCommandStatus, RiskRead},
	{ComputerCommandObserve, RiskRead},
	{ComputerCommandHandoff, RiskWrite},
	{ComputerCommandRequestResume, RiskWrite},
	{ComputerCommandResume, RiskWrite},
	{ComputerCommandRelease, RiskWrite},
}

// ComputerProfileDescriptor is operator-selected metadata, never native window
// selection authority. Runtime admission supplies the exact guarded incarnation.
type ComputerProfileDescriptor struct {
	Alias     string                   `json:"alias"`
	Revision  string                   `json:"revision"`
	OS        computer.OperatingSystem `json:"os"`
	Mode      computer.DesktopMode     `json:"mode"`
	Isolation computer.Isolation       `json:"isolation"`
	Backend   string                   `json:"backend"`
}

// ComputerTurn carries the two turn fields not already authenticated by the
// execution plan. Only the trusted first-party router constructs this input;
// computer commands never receive a generic model invocation contract.
type ComputerTurn struct {
	Runtime     identity.Kind `json:"runtime"`
	ExecutionID string        `json:"execution_id"`
}

func (turn ComputerTurn) Principal(plan ExecutionPlan) (identity.Principal, error) {
	principal := identity.Principal{
		Runtime: turn.Runtime, ExecutionID: turn.ExecutionID,
		ActorID: plan.ActorID, AgentID: plan.AgentID, SessionID: plan.SessionID,
	}
	if principal.Validate() != nil {
		return identity.Principal{}, ErrCommandDenied
	}
	return principal, nil
}

type ComputerTargetsInput struct {
	ComputerTurn
}

type ComputerOpenInput struct {
	ComputerTurn
	Profile         string `json:"profile"`
	ProfileRevision string `json:"profile_revision"`
}

type ComputerStatusInput struct {
	ComputerTurn
	SessionID string `json:"session_id"`
}

type ComputerControlInput struct {
	ComputerTurn
	SessionID  string `json:"session_id"`
	Generation uint64 `json:"generation"`
}

type ComputerObserveInput struct {
	ComputerControlInput
	WorkspaceID string `json:"workspace_id"`
}

type ComputerTargetsResult struct {
	Targets    []computer.Target `json:"targets"`
	ObservedAt time.Time         `json:"observed_at"`
}

type ComputerSessionResult struct {
	Session    computer.Session `json:"session"`
	ObservedAt time.Time        `json:"observed_at"`
}

// ComputerOutputDescriptor authorizes only one immutable image through the
// existing authenticated binary transfer channel. It contains no local path.
type ComputerOutputDescriptor struct {
	TransferID    string           `json:"transfer_id"`
	Binding       computer.Binding `json:"binding"`
	ObservationID string           `json:"observation_id"`
	InvocationID  string           `json:"invocation_id"`
	WorkspaceID   string           `json:"workspace_id"`
	ContentType   string           `json:"content_type"`
	Size          uint64           `json:"size"`
	SHA256        string           `json:"sha256"`
	ExpiresAt     time.Time        `json:"expires_at"`
}

// ComputerObservationResult is live-only. ObservedAt and observation timestamps
// are in the companion's clock domain, not gateway freshness proof. The gateway
// must bound the request with its own clock and verify image bytes before use.
type ComputerObservationResult struct {
	Session           computer.Session          `json:"session"`
	Observation       computer.Observation      `json:"observation"`
	ObservedAt        time.Time                 `json:"observed_at"`
	AccessibilityText string                    `json:"accessibility_text"`
	Output            *ComputerOutputDescriptor `json:"output,omitempty"`
}

type ComputerHostOpenRequest struct {
	Principal       identity.Principal
	Profile         string
	ProfileRevision string
	InvocationID    string
}

type ComputerHostStatusRequest struct {
	Principal identity.Principal
	SessionID string
}

type ComputerHostControlRequest struct {
	ComputerHostStatusRequest
	Generation uint64
	Event      computer.ControlEvent
}

type ComputerHostObserveRequest struct {
	ComputerHostStatusRequest
	Generation   uint64
	InvocationID string
	WorkspaceID  string
}

func (ComputerObservationResult) String() string {
	return "companion computer observation (protected payload omitted)"
}

func (result ComputerObservationResult) GoString() string { return result.String() }

func IsComputerCommand(name string) bool {
	return name == ComputerCommandAct || slices.ContainsFunc(computerCommands, func(command struct {
		name string
		risk Risk
	},
	) bool {
		return command.name == name
	})
}

func validateComputerProfiles(profiles []ComputerProfileDescriptor) error {
	if len(profiles) == 0 || len(profiles) > 32 {
		return fmt.Errorf("%w: computer profile count is outside bounds", ErrInvalidCapability)
	}
	seen := make(map[string]bool)
	for index, profile := range profiles {
		if Alias(profile.Alias).Validate() != nil || !validSHA256Digest(profile.Revision) ||
			!validInvocationIdentifier(profile.Backend) || index > 0 && profiles[index-1].Alias >= profile.Alias ||
			seen[profile.Revision] || profile.OS != computer.OSLinux && profile.OS != computer.OSMacOS ||
			profile.Mode != computer.ModeAttached && profile.Mode != computer.ModeManaged ||
			profile.Isolation != computer.IsolationNone && profile.Isolation != computer.IsolationContainer &&
				profile.Isolation != computer.IsolationVM {
			return fmt.Errorf("%w: malformed computer profiles", ErrInvalidCapability)
		}
		seen[profile.Revision] = true
	}
	return nil
}

func ComputerCommandDescriptors(profiles []ComputerProfileDescriptor) ([]CommandDescriptor, error) {
	profiles = slices.Clone(profiles)
	slices.SortFunc(profiles, func(a, b ComputerProfileDescriptor) int {
		return bytes.Compare([]byte(a.Alias), []byte(b.Alias))
	})
	if err := validateComputerProfiles(profiles); err != nil {
		return nil, err
	}
	descriptors := make([]CommandDescriptor, 0, len(computerCommands))
	for _, command := range computerCommands {
		descriptors = append(descriptors, CommandDescriptor{
			Name:             command.name,
			Risk:             command.risk,
			SupportsCancel:   true,
			ComputerProfiles: slices.Clone(profiles),
			InputSchema: ComputerCommandInputSchema(
				command.name,
			),
			OutputSchema: ComputerCommandOutputSchema(command.name),
		})
	}
	return descriptors, nil
}

func computerSchema(value any) json.RawMessage {
	reflector := jsonschema.Reflector{DoNotReference: true, ExpandedStruct: true}
	return mustJSON(reflector.Reflect(value))
}

// Typed schemas do not depend on profile authority (which is checked
// separately). Reflect once, and clone at every boundary so callers cannot
// mutate the canonical contract or pay reflection cost for every invocation.
var computerSchemaCache = sync.OnceValue(func() map[string][2]json.RawMessage {
	result := make(map[string][2]json.RawMessage, len(computerCommands))
	for _, command := range computerCommands {
		result[command.name] = [2]json.RawMessage{
			computerCommandInputSchema(command.name), computerCommandOutputSchema(command.name),
		}
	}
	result[ComputerCommandAct] = [2]json.RawMessage{
		computerCommandInputSchema(ComputerCommandAct), computerCommandOutputSchema(ComputerCommandAct),
	}
	return result
})

func ComputerCommandInputSchema(command string) json.RawMessage {
	return bytes.Clone(computerSchemaCache()[command][0])
}

func ComputerCommandOutputSchema(command string) json.RawMessage {
	return bytes.Clone(computerSchemaCache()[command][1])
}

func computerCommandInputSchema(command string) json.RawMessage {
	switch command {
	case ComputerCommandAct:
		return computerSchema(ComputerActInput{})
	case ComputerCommandTargets:
		return computerSchema(ComputerTargetsInput{})
	case ComputerCommandOpen:
		return computerSchema(ComputerOpenInput{})
	case ComputerCommandStatus:
		return computerSchema(ComputerStatusInput{})
	case ComputerCommandObserve:
		return computerSchema(ComputerObserveInput{})
	case ComputerCommandHandoff, ComputerCommandRequestResume, ComputerCommandResume, ComputerCommandRelease:
		return computerSchema(ComputerControlInput{})
	default:
		return nil
	}
}

func computerCommandOutputSchema(command string) json.RawMessage {
	switch command {
	case ComputerCommandTargets:
		return computerSchema(ComputerTargetsResult{})
	case ComputerCommandObserve, ComputerCommandAct:
		var live any
		value := any(ComputerObservationResult{})
		if command == ComputerCommandAct {
			value = ComputerActResult{}
		}
		_ = json.Unmarshal(computerSchema(value), &live)
		return mustJSON(map[string]any{"oneOf": []any{live, map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"protected_result"},
			"properties": map[string]any{"protected_result": map[string]any{"const": true}},
		}}})
	case ComputerCommandOpen, ComputerCommandStatus, ComputerCommandHandoff,
		ComputerCommandRequestResume, ComputerCommandResume, ComputerCommandRelease:
		return computerSchema(ComputerSessionResult{})
	default:
		return nil
	}
}

func decodeComputerJSON(raw []byte, result any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(result); err != nil {
		return ErrInvalidInvocation
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return ErrInvalidInvocation
	}
	return nil
}

func validateComputerTarget(target computer.Target, profiles []ComputerProfileDescriptor) error {
	if target.Validate() != nil || target.Placement != computer.PlacementNode || target.Scope.WindowID == "" {
		return ErrInvalidInvocation
	}
	for _, profile := range profiles {
		if target.Alias == profile.Alias && target.OS == profile.OS && target.Mode == profile.Mode &&
			target.Isolation == profile.Isolation && target.Backend == profile.Backend {
			return nil
		}
	}
	return ErrInvalidInvocation
}

func validateComputerSession(result ComputerSessionResult, profiles []ComputerProfileDescriptor) error {
	if result.Session.Validate() != nil || result.ObservedAt.IsZero() ||
		validateComputerTarget(result.Session.Binding.Target, profiles) != nil ||
		result.Session.ExpiresAt.Sub(result.ObservedAt) > time.Hour ||
		!result.Session.Control.Terminal() && !result.ObservedAt.Before(result.Session.ExpiresAt) {
		return ErrInvalidInvocation
	}
	for _, profile := range profiles {
		if profile.Alias == result.Session.Binding.Target.Alias &&
			profile.Revision == result.Session.Binding.PolicyRevision {
			return nil
		}
	}
	return ErrInvalidInvocation
}

func (result ComputerObservationResult) Validate(profiles []ComputerProfileDescriptor) error {
	if validateComputerSession(
		ComputerSessionResult{Session: result.Session, ObservedAt: result.ObservedAt},
		profiles,
	) != nil ||
		result.Session.AgentAuthority(result.ObservedAt) != nil {
		return ErrInvalidInvocation
	}
	observation := result.Observation
	if observation.Binding != result.Session.Binding || !validInvocationIdentifier(observation.ID) ||
		observation.Geometry.Validate() != nil || observation.CapturedAt.IsZero() ||
		observation.CapturedAt.After(result.ObservedAt) || !observation.ExpiresAt.After(result.ObservedAt) ||
		observation.ExpiresAt.After(result.Session.ExpiresAt) ||
		observation.ExpiresAt.Sub(observation.CapturedAt) > time.Minute ||
		len(observation.Screenshot) != 0 || observation.AccessibilityText != "" ||
		len(result.AccessibilityText) > computer.MaxAccessibilityBytes || !utf8.ValidString(result.AccessibilityText) {
		return ErrInvalidInvocation
	}
	if result.Output == nil {
		if result.AccessibilityText == "" || observation.ContentType != "" {
			return ErrInvalidInvocation
		}
		return nil
	}
	output := result.Output
	if !validInvocationIdentifier(output.TransferID) || !validInvocationIdentifier(output.InvocationID) ||
		!validInvocationIdentifier(output.WorkspaceID) || output.Binding != observation.Binding ||
		output.ObservationID != observation.ID || output.ContentType != "image/png" ||
		observation.ContentType != output.ContentType || output.Size == 0 || output.Size > computer.MaxCaptureBytes ||
		!validSHA256Digest(output.SHA256) || !output.ExpiresAt.After(result.ObservedAt) ||
		output.ExpiresAt.After(observation.ExpiresAt) {
		return ErrInvalidInvocation
	}
	return nil
}

func validateComputerInvocationInput(descriptor CommandDescriptor, raw []byte) error {
	var turn ComputerTurn
	if json.Unmarshal(raw, &turn) != nil {
		return ErrInvalidInvocation
	}
	if (identity.Principal{
		Runtime: turn.Runtime, ExecutionID: turn.ExecutionID,
		ActorID: "shape", AgentID: "shape", SessionID: "shape",
	}).Validate() != nil {
		return ErrInvalidInvocation
	}
	switch descriptor.Name {
	case ComputerCommandAct:
		var input ComputerActInput
		if decodeComputerJSON(raw, &input) != nil || input.Validate() != nil ||
			validateComputerTarget(input.Request.Binding.Target, descriptor.ComputerProfiles) != nil {
			return ErrInvalidInvocation
		}
		for _, profile := range descriptor.ComputerProfiles {
			if profile.Alias == input.Request.Binding.Target.Alias &&
				profile.Revision == input.Request.Binding.PolicyRevision {
				return nil
			}
		}
		return ErrInvalidInvocation
	case ComputerCommandTargets:
		var input ComputerTargetsInput
		return decodeComputerJSON(raw, &input)
	case ComputerCommandOpen:
		var input ComputerOpenInput
		if decodeComputerJSON(raw, &input) != nil {
			return ErrInvalidInvocation
		}
		for _, profile := range descriptor.ComputerProfiles {
			if input.Profile == profile.Alias && input.ProfileRevision == profile.Revision {
				return nil
			}
		}
		return ErrInvalidInvocation
	case ComputerCommandStatus:
		var input ComputerStatusInput
		if decodeComputerJSON(raw, &input) != nil || !validInvocationIdentifier(input.SessionID) {
			return ErrInvalidInvocation
		}
	case ComputerCommandObserve:
		var input ComputerObserveInput
		if decodeComputerJSON(raw, &input) != nil || !validInvocationIdentifier(input.SessionID) ||
			input.Generation == 0 || !validInvocationIdentifier(input.WorkspaceID) {
			return ErrInvalidInvocation
		}
	default:
		var input ComputerControlInput
		if !IsComputerCommand(descriptor.Name) || decodeComputerJSON(raw, &input) != nil ||
			!validInvocationIdentifier(input.SessionID) || input.Generation == 0 {
			return ErrInvalidInvocation
		}
	}
	return nil
}

func validateComputerInvocationOutput(descriptor CommandDescriptor, raw []byte) error {
	switch descriptor.Name {
	case ComputerCommandAct:
		if bytes.Equal(raw, []byte(`{"protected_result":true}`)) {
			return nil
		}
		var result ComputerActResult
		if decodeComputerJSON(raw, &result) != nil || !validInvocationIdentifier(result.ActionID) {
			return ErrInvalidInvocation
		}
		return result.Observation.Validate(descriptor.ComputerProfiles)
	case ComputerCommandTargets:
		var result ComputerTargetsResult
		if decodeComputerJSON(raw, &result) != nil || result.ObservedAt.IsZero() || len(result.Targets) > 32 {
			return ErrInvalidInvocation
		}
		seen := make(map[string]bool)
		for _, target := range result.Targets {
			if validateComputerTarget(target, descriptor.ComputerProfiles) != nil || seen[target.Alias] {
				return ErrInvalidInvocation
			}
			seen[target.Alias] = true
		}
	case ComputerCommandObserve:
		if bytes.Equal(raw, []byte(`{"protected_result":true}`)) {
			return nil
		}
		var result ComputerObservationResult
		if decodeComputerJSON(raw, &result) != nil {
			return ErrInvalidInvocation
		}
		return result.Validate(descriptor.ComputerProfiles)
	default:
		var result ComputerSessionResult
		if !IsComputerCommand(descriptor.Name) || decodeComputerJSON(raw, &result) != nil {
			return ErrInvalidInvocation
		}
		return validateComputerSession(result, descriptor.ComputerProfiles)
	}
	return nil
}

func (descriptor CommandDescriptor) validateComputerProfiles() error {
	if !IsComputerCommand(descriptor.Name) {
		if len(descriptor.ComputerProfiles) != 0 || strings.HasPrefix(descriptor.Name, "computer.") {
			return fmt.Errorf("%w: non-computer command carries computer profiles", ErrInvalidCapability)
		}
		return nil
	}
	if err := validateComputerProfiles(descriptor.ComputerProfiles); err != nil {
		return err
	}
	if descriptor.ModelContract != nil || descriptor.SupportsProgress || descriptor.SupportsTerminal ||
		!descriptor.SupportsCancel || descriptor.Name == ComputerCommandAct && descriptor.Risk != RiskWrite {
		return fmt.Errorf("%w: malformed internal computer command behavior", ErrInvalidCapability)
	}
	for _, command := range computerCommands {
		if command.name == descriptor.Name && command.risk != descriptor.Risk {
			return fmt.Errorf("%w: computer command has incorrect risk", ErrInvalidCapability)
		}
	}
	for _, schemas := range [][2]json.RawMessage{
		{descriptor.InputSchema, ComputerCommandInputSchema(descriptor.Name)},
		{descriptor.OutputSchema, ComputerCommandOutputSchema(descriptor.Name)},
	} {
		actual, err := canonicalJSON(schemas[0])
		if err != nil {
			return ErrInvalidCapability
		}
		expected, err := canonicalJSON(schemas[1])
		if err != nil || !bytes.Equal(actual, expected) {
			return fmt.Errorf("%w: computer schema differs from typed contract", ErrInvalidCapability)
		}
	}
	return nil
}

func validateComputerCatalog(commands []CommandDescriptor) error {
	var profiles []ComputerProfileDescriptor
	var inputProfiles []ComputerProfileDescriptor
	count := 0
	for _, descriptor := range commands {
		if !IsComputerCommand(descriptor.Name) {
			continue
		}
		if descriptor.Name == ComputerCommandAct {
			inputProfiles = descriptor.ComputerProfiles
			continue
		}
		count++
		if profiles == nil {
			profiles = descriptor.ComputerProfiles
		} else if !slices.Equal(profiles, descriptor.ComputerProfiles) {
			return fmt.Errorf("%w: computer commands disagree on profiles", ErrInvalidCapability)
		}
	}
	if count != 0 && count != len(computerCommands) {
		return fmt.Errorf("%w: computer catalog lacks complete command set", ErrInvalidCapability)
	}
	for _, profile := range inputProfiles {
		if !slices.Contains(profiles, profile) {
			return fmt.Errorf("%w: input profile lacks matching read-only authority", ErrInvalidCapability)
		}
	}
	return nil
}
