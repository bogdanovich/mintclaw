package companion

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

// BrowserCommandHost is the node-local implementation boundary for the typed
// browser capability. Playwright/CDP details remain behind this interface.
type BrowserCommandHost interface {
	BrowserProfiles() []nodes.BrowserProfileDescriptor
	Open(context.Context, nodes.BrowserHostOpenRequest) (nodes.BrowserSessionResult, error)
	Status(context.Context, nodes.BrowserHostStatusRequest) (nodes.BrowserSessionResult, error)
	Observe(context.Context, nodes.BrowserHostObserveRequest) (nodes.BrowserObservationResult, error)
	Navigate(context.Context, nodes.BrowserHostActRequest) (nodes.BrowserObservationResult, error)
	Click(context.Context, nodes.BrowserHostActRequest) (nodes.BrowserObservationResult, error)
	Fill(context.Context, nodes.BrowserHostActRequest) (nodes.BrowserObservationResult, error)
	Select(context.Context, nodes.BrowserHostActRequest) (nodes.BrowserObservationResult, error)
	Press(context.Context, nodes.BrowserHostActRequest) (nodes.BrowserObservationResult, error)
	Scroll(context.Context, nodes.BrowserHostActRequest) (nodes.BrowserObservationResult, error)
	Dialog(context.Context, nodes.BrowserHostActRequest) (nodes.BrowserObservationResult, error)
	Close(context.Context, nodes.BrowserHostStatusRequest) (nodes.BrowserSessionResult, error)
}

type browserContextCommandHost interface {
	Contexts(context.Context, nodes.BrowserHostContextRequest) (nodes.BrowserContextResult, error)
}

type browserOrdinaryInteractionCommandHost interface {
	Check(context.Context, nodes.BrowserHostActRequest) (nodes.BrowserObservationResult, error)
	Uncheck(context.Context, nodes.BrowserHostActRequest) (nodes.BrowserObservationResult, error)
	Hover(context.Context, nodes.BrowserHostActRequest) (nodes.BrowserObservationResult, error)
	Drag(context.Context, nodes.BrowserHostActRequest) (nodes.BrowserObservationResult, error)
}

type browserCommandHandler struct {
	command         string
	descriptorValue nodes.CommandDescriptor
	host            BrowserCommandHost
}

func newBrowserCommandHandlers(host BrowserCommandHost) ([]commandHandler, error) {
	if host == nil {
		return nil, errors.New("node browser command host is required")
	}
	descriptors, err := nodes.BrowserCommandDescriptors(host.BrowserProfiles())
	if err != nil {
		return nil, err
	}
	handlers := make([]commandHandler, 0, len(descriptors))
	for _, descriptor := range descriptors {
		handlers = append(handlers, &browserCommandHandler{
			command: descriptor.Name, descriptorValue: descriptor, host: host,
		})
	}
	return handlers, nil
}

func (handler *browserCommandHandler) descriptor() nodes.CommandDescriptor {
	return handler.descriptorValue
}

func (handler *browserCommandHandler) authorizeEphemeral(
	plan nodes.ExecutionPlan,
	ephemeralInput json.RawMessage,
) error {
	if handler.command == nodes.BrowserCommandContexts {
		var input nodes.BrowserContextInput
		if err := json.Unmarshal(plan.Input, &input); err != nil {
			return nodes.ErrCommandDenied
		}
		_, err := browserEphemeralContextAuthority(input, ephemeralInput)
		return err
	}
	if handler.command != nodes.BrowserCommandAct {
		if len(ephemeralInput) != 0 {
			return nodes.ErrCommandDenied
		}
		return nil
	}
	var input nodes.BrowserActInput
	if err := json.Unmarshal(plan.Input, &input); err != nil {
		return nodes.ErrCommandDenied
	}
	_, err := browserEphemeralActionValue(input, ephemeralInput)
	return err
}

func (handler *browserCommandHandler) execute(
	ctx context.Context,
	invocation commandInvocation,
) (any, error) {
	switch handler.command {
	case nodes.BrowserCommandSessionOpen:
		var input nodes.BrowserSessionOpenInput
		if err := json.Unmarshal(invocation.Input, &input); err != nil {
			return nil, browserCommandFailure(err)
		}
		result, err := handler.host.Open(ctx, nodes.BrowserHostOpenRequest{
			SessionID: input.SessionID, Profile: input.Profile,
			RoutedSessionID: invocation.Plan.SessionID,
			ProfileRevision: input.ProfileRevision, BrowserPolicyRevision: input.BrowserPolicyRevision,
			AgentID: invocation.Plan.AgentID, ActorID: invocation.Plan.ActorID,
			DryRun: input.DryRun, Limits: input.Limits,
		})
		return result, browserCommandFailure(err)
	case nodes.BrowserCommandSessionStatus:
		var input nodes.BrowserSessionStatusInput
		if err := json.Unmarshal(invocation.Input, &input); err != nil {
			return nil, browserCommandFailure(err)
		}
		result, err := handler.host.Status(ctx, browserStatusRequest(input, invocation.Plan))
		return browserStatusResult(result), browserCommandFailure(err)
	case nodes.BrowserCommandObserve:
		var input nodes.BrowserObserveInput
		if err := json.Unmarshal(invocation.Input, &input); err != nil {
			return nil, browserCommandFailure(err)
		}
		if input.Screenshot {
			return nil, newCommandFailure(
				"COMMAND_DENIED", "browser screenshot is unavailable", nodes.ErrBrowserHostDenied,
			)
		}
		result, err := handler.host.Observe(ctx, nodes.BrowserHostObserveRequest{
			SessionID: input.SessionID, TabID: input.TabID,
			RoutedSessionID:    invocation.Plan.SessionID,
			SnapshotGeneration: input.SnapshotGeneration,
			AgentID:            invocation.Plan.AgentID, ActorID: invocation.Plan.ActorID,
		})
		return result, browserCommandFailure(err)
	case nodes.BrowserCommandAct:
		return handler.executeAct(ctx, invocation)
	case nodes.BrowserCommandContexts:
		contextHost, ok := handler.host.(browserContextCommandHost)
		if !ok {
			return nil, ErrCommandUnavailable
		}
		var input nodes.BrowserContextInput
		if err := json.Unmarshal(invocation.Input, &input); err != nil {
			return nil, browserCommandFailure(err)
		}
		authority, err := browserEphemeralContextAuthority(input, invocation.EphemeralInput)
		if err != nil {
			return nil, newCommandFailure(
				"COMMAND_DENIED", "browser context authority is unavailable", nodes.ErrBrowserHostDenied,
			)
		}
		result, err := contextHost.Contexts(ctx, nodes.BrowserHostContextRequest{
			SessionID: input.SessionID, ProfileRevision: input.ProfileRevision,
			RoutedSessionID: invocation.Plan.SessionID,
			Operation:       input.Operation, RequestID: input.RequestID,
			Authority: authority, TabID: input.TabID, FrameID: input.FrameID,
			AgentID: invocation.Plan.AgentID, ActorID: invocation.Plan.ActorID,
		})
		if err != nil && input.Operation != "list" && errors.Is(err, nodes.ErrBrowserHostLost) {
			return nil, fmt.Errorf("%w: browser context outcome is unknown", ErrInvocationOutcomeUnknown)
		}
		return result, browserCommandFailure(err)
	case nodes.BrowserCommandSessionClose:
		var input nodes.BrowserSessionStatusInput
		if err := json.Unmarshal(invocation.Input, &input); err != nil {
			return nil, browserCommandFailure(err)
		}
		result, err := handler.host.Close(ctx, browserStatusRequest(input, invocation.Plan))
		return browserStatusResult(result), browserCommandFailure(err)
	default:
		return nil, ErrCommandUnavailable
	}
}

func browserEphemeralContextAuthority(
	input nodes.BrowserContextInput,
	ephemeralInput json.RawMessage,
) (*nodes.BrowserContextCatalog, error) {
	if input.Operation == "list" || input.Operation == "open" {
		if len(ephemeralInput) != 0 || input.AuthorityDigest != "" || input.AuthorityBytes != 0 {
			return nil, nodes.ErrCommandDenied
		}
		return nil, nil
	}
	if (input.Operation != "select" && input.Operation != "close") ||
		input.AuthorityBytes < 1 || input.AuthorityBytes > nodes.MaxBrowserContextInputBytes ||
		len(ephemeralInput) != input.AuthorityBytes {
		return nil, nodes.ErrCommandDenied
	}
	var ephemeral struct {
		Authority nodes.BrowserContextCatalog `json:"authority"`
	}
	if err := decodeStrictJSON(ephemeralInput, &ephemeral); err != nil ||
		ephemeral.Authority.ID != input.ContextCatalogID ||
		ephemeral.Authority.Generation != input.ContextGeneration ||
		!nodes.BrowserContextAuthorityDigestMatches(input.AuthorityDigest, ephemeral.Authority) {
		return nil, nodes.ErrCommandDenied
	}
	return &ephemeral.Authority, nil
}

func browserStatusResult(result nodes.BrowserSessionResult) nodes.BrowserSessionResult {
	return nodes.BrowserSessionResult{
		SessionID: result.SessionID, State: result.State,
		Reason: result.Reason, Recovery: result.Recovery,
	}
}

func (handler *browserCommandHandler) executeAct(
	ctx context.Context,
	invocation commandInvocation,
) (any, error) {
	var input nodes.BrowserActInput
	if err := json.Unmarshal(invocation.Input, &input); err != nil {
		return nil, browserCommandFailure(err)
	}
	value, err := browserEphemeralActionValue(input, invocation.EphemeralInput)
	if err != nil {
		return nil, newCommandFailure(
			"COMMAND_DENIED", "browser action input is unavailable", nodes.ErrBrowserHostDenied,
		)
	}
	if (input.Action.Kind != "navigate" && input.Action.Kind != "scroll" && input.Action.Kind != "click" &&
		input.Action.Kind != "fill" && input.Action.Kind != "select" && input.Action.Kind != "press" &&
		input.Action.Kind != "dialog" && input.Action.Kind != "check" && input.Action.Kind != "uncheck" &&
		input.Action.Kind != "hover" && input.Action.Kind != "drag") ||
		(input.Action.Kind == "navigate" && input.Effect != "navigation") ||
		(input.Action.Kind == "scroll" && input.Effect != "read") ||
		(input.Action.Kind == "select" &&
			(input.Effect != "local_edit" || input.ExpectedRole != "combobox" ||
				input.ApprovalDigest != "")) ||
		(input.Action.Kind == "fill" &&
			(input.Effect != "local_edit" ||
				!nodes.BrowserFillFieldAllowed(input.ExpectedRole, input.ExpectedName) ||
				input.ApprovalDigest != "")) ||
		(input.Action.Kind == "press" &&
			(input.Effect != "unknown" || input.Action.Target != "document" ||
				input.ExpectedRole != "" || input.ExpectedName != "" || !nodes.BrowserApprovalDigestMatches(input))) ||
		(input.Action.Kind == "click" &&
			(input.Effect != nodes.BrowserClickEffect(input.ExpectedRole) ||
				input.ExpectedRole == "" || !nodes.BrowserApprovalDigestMatches(input))) ||
		(input.Action.Kind == "dialog" && !validCompanionDialogAction(input)) ||
		((input.Action.Kind == "check" || input.Action.Kind == "uncheck") &&
			(input.Effect != "local_edit" || !nodes.BrowserCheckRoleAllowed(input.Action.Kind, input.ExpectedRole) ||
				input.ApprovalDigest != "")) ||
		(input.Action.Kind == "hover" &&
			(input.Effect != "read" || input.ExpectedRole == "" || input.ApprovalDigest != "")) ||
		(input.Action.Kind == "drag" &&
			(input.Effect != "unknown" || input.Action.SourceRef == "" || input.Action.DestinationRef == "" ||
				input.Action.SourceRef == input.Action.DestinationRef || input.ExpectedRole == "" ||
				len(input.ExpectedRole) > 128 || len(input.ExpectedName) > 4096 ||
				input.DestinationExpectedRole == "" || len(input.DestinationExpectedRole) > 128 ||
				len(input.DestinationExpectedName) > 4096 || !nodes.BrowserApprovalDigestMatches(input))) ||
		(input.Action.Kind != "drag" &&
			(input.DestinationExpectedRole != "" || input.DestinationExpectedName != "")) ||
		(input.Action.Kind != "click" && input.Action.Kind != "fill" && input.Action.Kind != "select" &&
			input.Action.Kind != "press" && input.Action.Kind != "dialog" && input.Action.Kind != "check" &&
			input.Action.Kind != "uncheck" && input.Action.Kind != "hover" && input.Action.Kind != "drag" &&
			(input.ApprovalDigest != "" || input.ExpectedRole != "" || input.ExpectedName != "")) {
		return nil, newCommandFailure(
			"COMMAND_DENIED", "browser action is unavailable", nodes.ErrBrowserHostDenied,
		)
	}
	input.Action.Value = value
	request := nodes.BrowserHostActRequest{
		SessionID: input.SessionID, TabID: input.TabID,
		RoutedSessionID:    invocation.Plan.SessionID,
		SnapshotGeneration: input.SnapshotGeneration,
		ActionInvocationID: input.ActionInvocationID, Action: input.Action,
		Effect: input.Effect, CurrentOrigin: input.CurrentOrigin,
		PreparedActionHash:    input.PreparedActionHash,
		BrowserPolicyRevision: input.BrowserPolicyRevision, ProfileRevision: input.ProfileRevision,
		ExpectedRole: input.ExpectedRole, ExpectedName: input.ExpectedName,
		DestinationExpectedRole: input.DestinationExpectedRole,
		DestinationExpectedName: input.DestinationExpectedName,
		DialogType:              input.DialogType, DialogMessageDigest: input.DialogMessageDigest,
		DialogMessageBytes: input.DialogMessageBytes,
		InputDigest:        input.InputDigest, InputBytes: input.InputBytes,
		ApprovalDigest: input.ApprovalDigest,
		AgentID:        invocation.Plan.AgentID, ActorID: invocation.Plan.ActorID,
	}
	var observation nodes.BrowserObservationResult
	switch input.Action.Kind {
	case "scroll":
		observation, err = handler.host.Scroll(ctx, request)
	case "click":
		observation, err = handler.host.Click(ctx, request)
	case "fill":
		observation, err = handler.host.Fill(ctx, request)
	case "select":
		observation, err = handler.host.Select(ctx, request)
	case "press":
		observation, err = handler.host.Press(ctx, request)
	case "dialog":
		observation, err = handler.host.Dialog(ctx, request)
	case "check", "uncheck", "hover", "drag":
		ordinary, ok := handler.host.(browserOrdinaryInteractionCommandHost)
		if !ok {
			return nil, browserCommandFailure(nodes.ErrBrowserHostDenied)
		}
		switch input.Action.Kind {
		case "check":
			observation, err = ordinary.Check(ctx, request)
		case "uncheck":
			observation, err = ordinary.Uncheck(ctx, request)
		case "drag":
			observation, err = ordinary.Drag(ctx, request)
		default:
			observation, err = ordinary.Hover(ctx, request)
		}
	default:
		observation, err = handler.host.Navigate(ctx, request)
	}
	if err != nil {
		if errors.Is(err, nodes.ErrBrowserHostLost) {
			return nil, fmt.Errorf("%w: browser action outcome is unknown", ErrInvocationOutcomeUnknown)
		}
		return nil, browserCommandFailure(err)
	}
	return nodes.BrowserActResult{
		ActionInvocationID: input.ActionInvocationID,
		State:              "succeeded",
		Observation:        &observation,
	}, nil
}

func browserEphemeralActionValue(
	input nodes.BrowserActInput,
	ephemeralInput json.RawMessage,
) (string, error) {
	protectedDialog := input.Action.Kind == "dialog" && input.Action.PromptProvided
	if input.Action.Kind != "select" && input.Action.Kind != "fill" && !protectedDialog {
		if len(ephemeralInput) != 0 || input.InputDigest != "" || input.InputBytes != 0 {
			return "", nodes.ErrCommandDenied
		}
		return "", nil
	}
	minimumBytes := 1
	if protectedDialog {
		minimumBytes = 0
	}
	if input.Action.Value != "" || input.InputBytes < minimumBytes ||
		input.InputBytes > nodes.MaxBrowserTextInputBytes || len(ephemeralInput) == 0 {
		return "", nodes.ErrCommandDenied
	}
	var ephemeral struct {
		Value string `json:"value"`
	}
	if err := decodeStrictJSON(ephemeralInput, &ephemeral); err != nil ||
		(!protectedDialog && ephemeral.Value == "") ||
		len(ephemeral.Value) != input.InputBytes ||
		!nodes.BrowserInputDigestMatches(input.InputDigest, ephemeral.Value) {
		return "", nodes.ErrCommandDenied
	}
	return ephemeral.Value, nil
}

func validCompanionDialogAction(input nodes.BrowserActInput) bool {
	if input.Action.DialogID == "" ||
		(input.Action.Decision != "accept" && input.Action.Decision != "dismiss") ||
		(input.DialogType != "alert" && input.DialogType != "beforeunload" &&
			input.DialogType != "confirm" && input.DialogType != "prompt") ||
		input.DialogMessageBytes < 0 || input.DialogMessageBytes > nodes.MaxBrowserDialogMessageBytes ||
		len(input.DialogMessageDigest) != sha256.Size*2 || input.ExpectedRole != "" || input.ExpectedName != "" {
		return false
	}
	if input.Action.Decision == "dismiss" {
		return input.Effect == "read" && !input.Action.PromptProvided && input.InputDigest == "" &&
			input.InputBytes == 0 && input.ApprovalDigest == ""
	}
	if input.Effect != "external_commit" || !nodes.BrowserApprovalDigestMatches(input) {
		return false
	}
	if input.Action.PromptProvided {
		return input.DialogType == "prompt" && len(input.InputDigest) == sha256.Size*2 && input.InputBytes >= 0
	}
	return input.InputDigest == "" && input.InputBytes == 0
}

func browserStatusRequest(
	input nodes.BrowserSessionStatusInput,
	plan nodes.ExecutionPlan,
) nodes.BrowserHostStatusRequest {
	return nodes.BrowserHostStatusRequest{
		SessionID: input.SessionID, ProfileRevision: input.ProfileRevision,
		RoutedSessionID: plan.SessionID,
		AgentID:         plan.AgentID, ActorID: plan.ActorID,
	}
}

func browserCommandFailure(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, nodes.ErrBrowserHostDenied):
		return newCommandFailure("COMMAND_DENIED", "browser command denied", err)
	case errors.Is(err, nodes.ErrBrowserHostBusy):
		return newCommandFailure("PROFILE_BUSY", "browser profile is busy", err)
	case errors.Is(err, nodes.ErrBrowserHostNotFound):
		return newCommandFailure("SESSION_NOT_FOUND", "browser session was not found", err)
	case errors.Is(err, nodes.ErrBrowserHostStale):
		return newCommandFailure("STALE_BROWSER_STATE", "browser state is stale", err)
	case errors.Is(err, nodes.ErrBrowserHostLost):
		return newCommandFailure("SESSION_LOST", "browser session is lost", err)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return newCommandFailure("COMMAND_TIMEOUT", "browser command did not complete", err)
	default:
		return newCommandFailure("BROWSER_UNAVAILABLE", "browser command failed", err)
	}
}
