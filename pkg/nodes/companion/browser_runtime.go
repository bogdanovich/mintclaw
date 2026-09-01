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
	Act(context.Context, nodes.BrowserHostActRequest) (nodes.BrowserObservationResult, error)
	Close(context.Context, nodes.BrowserHostStatusRequest) (nodes.BrowserSessionResult, error)
}

type browserContextCommandHost interface {
	Contexts(context.Context, nodes.BrowserHostContextRequest) (nodes.BrowserContextResult, error)
}

type browserCaptureCommandHost interface {
	Capture(context.Context, nodes.BrowserHostCaptureRequest) (nodes.BrowserOutputDescriptor, error)
}

type browserDiagnosticsCommandHost interface {
	Diagnostics(context.Context, nodes.BrowserHostDiagnosticsRequest) (nodes.BrowserDiagnosticsResult, error)
}

type browserPolicyCommandHost interface {
	EvaluatePolicy(context.Context, nodes.BrowserHostPolicyRequest) (nodes.BrowserPolicyEvaluateResult, error)
}

type browserDownloadCommandHost interface {
	Download(context.Context, nodes.BrowserHostActRequest) (nodes.BrowserOutputDescriptor, error)
}

type browserObservationOutputHost interface {
	PrepareObservationOutput(
		nodes.BrowserHostObservationOutputRequest,
		nodes.BrowserObservationResult,
	) (nodes.BrowserObservationResult, error)
}

type browserObservationOutputDiscardHost interface {
	DiscardObservationOutput(nodes.BrowserOutputDescriptor) error
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
		if err != nil {
			return result, browserCommandFailure(err)
		}
		result, err = handler.prepareObservationOutput(
			invocation, input.WorkspaceID, input.BrowserTarget, result, result,
		)
		if err != nil {
			// The read already advanced the host generation. Persist a protected
			// success so the gateway can advance and issue a fresh read identity.
			return protectedBrowserObservationReceipt()
		}
		return result, nil
	case nodes.BrowserCommandCapture:
		captureHost, ok := handler.host.(browserCaptureCommandHost)
		if !ok {
			return nil, ErrCommandUnavailable
		}
		var input nodes.BrowserCaptureInput
		if err := json.Unmarshal(invocation.Input, &input); err != nil {
			return nil, browserCommandFailure(err)
		}
		result, err := captureHost.Capture(ctx, nodes.BrowserHostCaptureRequest{
			BrowserCaptureInput: input, RoutedSessionID: invocation.Plan.SessionID,
			AgentID: invocation.Plan.AgentID, ActorID: invocation.Plan.ActorID,
		})
		return result, browserCommandFailure(err)
	case nodes.BrowserCommandDiagnostics:
		diagnosticsHost, ok := handler.host.(browserDiagnosticsCommandHost)
		if !ok {
			return nil, ErrCommandUnavailable
		}
		var input nodes.BrowserDiagnosticsInput
		if err := json.Unmarshal(invocation.Input, &input); err != nil {
			return nil, browserCommandFailure(err)
		}
		result, err := diagnosticsHost.Diagnostics(ctx, nodes.BrowserHostDiagnosticsRequest{
			BrowserDiagnosticsInput: input, RoutedSessionID: invocation.Plan.SessionID,
			AgentID: invocation.Plan.AgentID, ActorID: invocation.Plan.ActorID,
		})
		return result, browserCommandFailure(err)
	case nodes.BrowserCommandPolicyEvaluate:
		policyHost, ok := handler.host.(browserPolicyCommandHost)
		if !ok {
			return nil, ErrCommandUnavailable
		}
		var input nodes.BrowserPolicyEvaluateInput
		if err := json.Unmarshal(invocation.Input, &input); err != nil ||
			nodes.ValidateBrowserPolicyEvaluateInput(input, handler.descriptorValue.BrowserProfiles) != nil {
			return nil, newCommandFailure(
				"COMMAND_DENIED", "browser policy is unavailable", nodes.ErrBrowserHostDenied,
			)
		}
		result, err := policyHost.EvaluatePolicy(ctx, nodes.BrowserHostPolicyRequest{
			BrowserPolicyEvaluateInput: input,
			AgentID:                    invocation.Plan.AgentID,
			ActorID:                    invocation.Plan.ActorID,
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
		if err == nil && result.Observation != nil {
			observation, stageErr := handler.prepareObservationOutput(
				invocation, input.WorkspaceID, input.BrowserTarget, *result.Observation, result,
			)
			if stageErr != nil {
				// Selection already completed. Preserve that known mutation and let
				// the gateway recover with list plus observe instead of replaying it.
				return protectedBrowserContextReceipt(input.Operation)
			}
			result.Observation = &observation
			staged, encodeErr := json.Marshal(result)
			if encodeErr != nil || len(staged) > invocation.Plan.OutputLimitBytes {
				handler.discardObservationOutput(observation.Output)
				return protectedBrowserContextReceipt(input.Operation)
			}
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

func (handler *browserCommandHandler) discardObservationOutput(
	descriptor *nodes.BrowserOutputDescriptor,
) {
	if descriptor == nil {
		return
	}
	host, ok := handler.host.(browserObservationOutputDiscardHost)
	if ok {
		_ = host.DiscardObservationOutput(*descriptor)
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
	if err := nodes.ValidateBrowserActInput(input, handler.descriptorValue.BrowserProfiles); err != nil {
		return nil, newCommandFailure(
			"COMMAND_DENIED", "browser action is unavailable", nodes.ErrBrowserHostDenied,
		)
	}
	value, err := browserEphemeralActionValue(input, invocation.EphemeralInput)
	if err != nil {
		return nil, newCommandFailure(
			"COMMAND_DENIED", "browser action input is unavailable", nodes.ErrBrowserHostDenied,
		)
	}
	input.Action.Value = value
	request := nodes.BrowserHostActRequest{
		SessionID: input.SessionID, TabID: input.TabID,
		RoutedSessionID:    invocation.Plan.SessionID,
		SnapshotGeneration: input.SnapshotGeneration,
		ActionInvocationID: input.ActionInvocationID, Action: input.Action,
		Effect: input.Effect, CurrentOrigin: input.CurrentOrigin,
		Confirmation:          input.Confirmation,
		PreparedActionHash:    input.PreparedActionHash,
		BrowserPolicyRevision: input.BrowserPolicyRevision, ProfileRevision: input.ProfileRevision,
		ExpectedRole: input.ExpectedRole, ExpectedName: input.ExpectedName,
		DestinationExpectedRole: input.DestinationExpectedRole,
		DestinationExpectedName: input.DestinationExpectedName,
		DialogType:              input.DialogType, DialogMessageDigest: input.DialogMessageDigest,
		DialogMessageBytes: input.DialogMessageBytes,
		InputDigest:        input.InputDigest, InputBytes: input.InputBytes,
		ArtifactSHA256: input.ArtifactSHA256, ArtifactBytes: input.ArtifactBytes,
		ArtifactFilename: input.ArtifactFilename, ArtifactContentType: input.ArtifactContentType,
		ApprovalDigest: input.ApprovalDigest,
		WorkspaceID:    input.WorkspaceID, RouteID: input.RouteID, BrowserTarget: input.BrowserTarget,
		RestrictedDecision:       input.RestrictedDecision,
		RestrictedPolicyRevision: input.RestrictedPolicyRevision,
		RestrictedOrigin:         input.RestrictedOrigin,
		AgentID:                  invocation.Plan.AgentID, ActorID: invocation.Plan.ActorID,
	}
	if input.Action.Kind == "download" {
		downloadHost, ok := handler.host.(browserDownloadCommandHost)
		if !ok {
			return nil, browserCommandFailure(nodes.ErrBrowserHostDenied)
		}
		output, downloadErr := downloadHost.Download(ctx, request)
		if errors.Is(downloadErr, nodes.ErrBrowserHostArtifactUnavailable) {
			return nodes.BrowserActResult{
				ActionInvocationID: input.ActionInvocationID,
				State:              "succeeded",
			}, nil
		}
		if downloadErr != nil {
			if errors.Is(downloadErr, nodes.ErrBrowserHostLost) {
				return nil, fmt.Errorf("%w: browser action outcome is unknown", ErrInvocationOutcomeUnknown)
			}
			return nil, browserCommandFailure(downloadErr)
		}
		return nodes.BrowserActResult{
			ActionInvocationID: input.ActionInvocationID,
			State:              "succeeded",
			Output:             &output,
		}, nil
	}
	observation, err := handler.host.Act(ctx, request)
	if err != nil {
		if errors.Is(err, nodes.ErrBrowserHostLost) {
			return nil, fmt.Errorf("%w: browser action outcome is unknown", ErrInvocationOutcomeUnknown)
		}
		return nil, browserCommandFailure(err)
	}
	inlineResult := nodes.BrowserActResult{
		ActionInvocationID: input.ActionInvocationID,
		State:              "succeeded",
		Observation:        &observation,
	}
	observation, err = handler.prepareObservationOutput(
		invocation, input.WorkspaceID, input.BrowserTarget, observation, inlineResult,
	)
	if err != nil {
		// The accepted action is authoritative even if its fresh projection
		// cannot be staged. Return a succeeded receipt without page data so the
		// gateway recovers with a new read-only observe and never replays it.
		return succeededBrowserActionReceipt(input.ActionInvocationID)
	}
	return nodes.BrowserActResult{
		ActionInvocationID: input.ActionInvocationID,
		State:              "succeeded",
		Observation:        &observation,
	}, nil
}

func protectedBrowserObservationReceipt() (any, error) {
	return struct {
		ProtectedResult bool `json:"protected_result"`
	}{ProtectedResult: true}, nil
}

func protectedBrowserContextReceipt(operation string) (any, error) {
	return struct {
		Operation       string `json:"operation"`
		ProtectedResult bool   `json:"protected_result"`
	}{Operation: operation, ProtectedResult: true}, nil
}

func succeededBrowserActionReceipt(invocationID string) (any, error) {
	return nodes.BrowserActResult{ActionInvocationID: invocationID, State: "succeeded"}, nil
}

func (handler *browserCommandHandler) prepareObservationOutput(
	invocation commandInvocation,
	workspaceID string,
	browserTarget string,
	observation nodes.BrowserObservationResult,
	inlineResult any,
) (nodes.BrowserObservationResult, error) {
	host, ok := handler.host.(browserObservationOutputHost)
	if !ok {
		return observation, nil
	}
	encoded, err := json.Marshal(inlineResult)
	if err != nil {
		return nodes.BrowserObservationResult{}, err
	}
	return host.PrepareObservationOutput(nodes.BrowserHostObservationOutputRequest{
		SessionID: observation.SessionID, RoutedSessionID: invocation.Plan.SessionID,
		InvocationID: invocation.Plan.InvocationID, WorkspaceID: workspaceID,
		BrowserTarget: browserTarget, AgentID: invocation.Plan.AgentID, ActorID: invocation.Plan.ActorID,
		InlineResultBytes: len(encoded),
	}, observation)
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
	case errors.Is(err, nodes.ErrBrowserHostNavigationFailed):
		return newCommandFailure(
			nodes.InvocationDispatchBrowserNavigationFailed,
			"browser navigation failed",
			err,
		)
	case errors.Is(err, nodes.ErrBrowserHostLost):
		return newCommandFailure("SESSION_LOST", "browser session is lost", err)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return newCommandFailure("COMMAND_TIMEOUT", "browser command did not complete", err)
	default:
		return newCommandFailure("BROWSER_UNAVAILABLE", "browser command failed", err)
	}
}
