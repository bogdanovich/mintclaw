package companion

import (
	"context"
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
	Select(context.Context, nodes.BrowserHostActRequest) (nodes.BrowserObservationResult, error)
	Press(context.Context, nodes.BrowserHostActRequest) (nodes.BrowserObservationResult, error)
	Scroll(context.Context, nodes.BrowserHostActRequest) (nodes.BrowserObservationResult, error)
	Close(context.Context, nodes.BrowserHostStatusRequest) (nodes.BrowserSessionResult, error)
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
	input.Action.Value = value
	if (input.Action.Kind != "navigate" && input.Action.Kind != "scroll" && input.Action.Kind != "click" &&
		input.Action.Kind != "select" && input.Action.Kind != "press") ||
		(input.Action.Kind == "navigate" && input.Effect != "navigation") ||
		(input.Action.Kind == "scroll" && input.Effect != "read") ||
		(input.Action.Kind == "select" &&
			(input.Effect != "local_edit" || input.ExpectedRole != "combobox" ||
				input.ApprovalDigest != "")) ||
		(input.Action.Kind == "press" &&
			(input.Effect != "unknown" || input.Action.Target != "document" ||
				input.ExpectedRole != "" || input.ExpectedName != "" || !nodes.BrowserApprovalDigestMatches(input))) ||
		(input.Action.Kind == "click" &&
			(input.Effect != nodes.BrowserClickEffect(input.ExpectedRole) ||
				input.ExpectedRole == "" || !nodes.BrowserApprovalDigestMatches(input))) ||
		(input.Action.Kind != "click" && input.Action.Kind != "select" && input.Action.Kind != "press" &&
			(input.ApprovalDigest != "" || input.ExpectedRole != "" || input.ExpectedName != "")) {
		return nil, newCommandFailure(
			"COMMAND_DENIED", "browser action is unavailable", nodes.ErrBrowserHostDenied,
		)
	}
	request := nodes.BrowserHostActRequest{
		SessionID: input.SessionID, TabID: input.TabID,
		RoutedSessionID:    invocation.Plan.SessionID,
		SnapshotGeneration: input.SnapshotGeneration,
		ActionInvocationID: input.ActionInvocationID, Action: input.Action,
		Effect: input.Effect, CurrentOrigin: input.CurrentOrigin,
		PreparedActionHash:    input.PreparedActionHash,
		BrowserPolicyRevision: input.BrowserPolicyRevision, ProfileRevision: input.ProfileRevision,
		ExpectedRole: input.ExpectedRole, ExpectedName: input.ExpectedName,
		ApprovalDigest: input.ApprovalDigest,
		AgentID:        invocation.Plan.AgentID, ActorID: invocation.Plan.ActorID,
	}
	var observation nodes.BrowserObservationResult
	switch input.Action.Kind {
	case "scroll":
		observation, err = handler.host.Scroll(ctx, request)
	case "click":
		observation, err = handler.host.Click(ctx, request)
	case "select":
		observation, err = handler.host.Select(ctx, request)
	case "press":
		observation, err = handler.host.Press(ctx, request)
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
	if input.Action.Kind != "select" {
		if len(ephemeralInput) != 0 || input.InputDigest != "" || input.InputBytes != 0 {
			return "", nodes.ErrCommandDenied
		}
		return "", nil
	}
	if input.Action.Value != "" || input.InputBytes < 1 ||
		input.InputBytes > nodes.MaxBrowserTextInputBytes || len(ephemeralInput) == 0 {
		return "", nodes.ErrCommandDenied
	}
	var ephemeral struct {
		Value string `json:"value"`
	}
	if err := decodeStrictJSON(ephemeralInput, &ephemeral); err != nil || ephemeral.Value == "" ||
		len(ephemeral.Value) != input.InputBytes ||
		!nodes.BrowserInputDigestMatches(input.InputDigest, ephemeral.Value) {
		return "", nodes.ErrCommandDenied
	}
	return ephemeral.Value, nil
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
