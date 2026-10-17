package companion

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
)

// ComputerCommandHost is an opt-in local boundary. The host owns native window
// selection, capture and output transfers; commands cannot dispatch input or
// close an application. Existing browser authority never implies this authority.
type ComputerCommandHost interface {
	ComputerProfiles() []nodes.ComputerProfileDescriptor
	Targets(context.Context, runtimecap.Principal) (nodes.ComputerTargetsResult, error)
	Open(context.Context, nodes.ComputerHostOpenRequest) (nodes.ComputerSessionResult, error)
	Status(context.Context, nodes.ComputerHostStatusRequest) (nodes.ComputerSessionResult, error)
	Control(context.Context, nodes.ComputerHostControlRequest) (nodes.ComputerSessionResult, error)
	Observe(context.Context, nodes.ComputerHostObserveRequest) (nodes.ComputerObservationResult, error)
	Disconnect(context.Context) error
}

type computerCommandHandler struct {
	descriptorValue nodes.CommandDescriptor
	host            ComputerCommandHost
}

func WithComputerHost(host ComputerCommandHost) RuntimeOption {
	return func(options *runtimeOptions) error {
		if host == nil {
			return errors.New("node computer command host is required")
		}
		options.computerHost = host
		return nil
	}
}

func newComputerCommandHandlers(host ComputerCommandHost) ([]commandHandler, error) {
	descriptors, err := nodes.ComputerCommandDescriptors(host.ComputerProfiles())
	if err != nil {
		return nil, err
	}
	handlers := make([]commandHandler, 0, len(descriptors))
	for _, descriptor := range descriptors {
		handlers = append(handlers, &computerCommandHandler{descriptorValue: descriptor, host: host})
	}
	return handlers, nil
}

func (handler *computerCommandHandler) descriptor() nodes.CommandDescriptor {
	return handler.descriptorValue
}

func (*computerCommandHandler) authorizeEphemeral(_ nodes.ExecutionPlan, input json.RawMessage) error {
	if len(input) != 0 {
		return nodes.ErrCommandDenied
	}
	return nil
}

func (handler *computerCommandHandler) execute(ctx context.Context, invocation commandInvocation) (any, error) {
	var turn nodes.ComputerTurn
	if json.Unmarshal(invocation.Input, &turn) != nil {
		return nil, nodes.ErrCommandDenied
	}
	principal, err := turn.Principal(invocation.Plan)
	if err != nil {
		return nil, err
	}
	switch handler.descriptorValue.Name {
	case nodes.ComputerCommandTargets:
		result, callErr := handler.host.Targets(ctx, principal)
		if callErr != nil {
			return nil, computerCommandFailure(callErr)
		}
		if result.Targets == nil {
			result.Targets = []computer.Target{}
		}
		for _, target := range result.Targets {
			if target.Node != string(invocation.Plan.NodeID) {
				return nil, computerCommandFailure(computer.ErrStale)
			}
		}
		return result, nil
	case nodes.ComputerCommandOpen:
		var input nodes.ComputerOpenInput
		if json.Unmarshal(invocation.Input, &input) != nil {
			return nil, nodes.ErrCommandDenied
		}
		result, callErr := handler.host.Open(ctx, nodes.ComputerHostOpenRequest{
			Principal: principal, Profile: input.Profile, ProfileRevision: input.ProfileRevision,
			InvocationID: invocation.Plan.InvocationID,
		})
		if callErr != nil {
			return nil, computerCommandFailure(callErr)
		}
		if !computerResultOwned(result, principal, invocation.Plan.NodeID) ||
			result.Session.Binding.Target.Alias != input.Profile || result.Session.Binding.PolicyRevision != input.ProfileRevision ||
			result.Session.Control != computer.ControlAgent || result.Session.Binding.Generation != 1 {
			return nil, errors.Join(ErrInvocationOutcomeUnknown, computerCommandFailure(computer.ErrStale))
		}
		return result, nil
	case nodes.ComputerCommandStatus:
		var input nodes.ComputerStatusInput
		if json.Unmarshal(invocation.Input, &input) != nil {
			return nil, nodes.ErrCommandDenied
		}
		result, callErr := handler.host.Status(ctx, nodes.ComputerHostStatusRequest{
			Principal: principal, SessionID: input.SessionID,
		})
		if callErr != nil {
			return nil, computerCommandFailure(callErr)
		}
		if !computerResultOwned(result, principal, invocation.Plan.NodeID) ||
			result.Session.Binding.SessionID != input.SessionID {
			return nil, computerCommandFailure(computer.ErrStale)
		}
		return result, nil
	case nodes.ComputerCommandObserve:
		var input nodes.ComputerObserveInput
		if json.Unmarshal(invocation.Input, &input) != nil {
			return nil, nodes.ErrCommandDenied
		}
		result, callErr := handler.host.Observe(ctx, nodes.ComputerHostObserveRequest{
			ComputerHostStatusRequest: nodes.ComputerHostStatusRequest{
				Principal: principal,
				SessionID: input.SessionID,
			},
			Generation:   input.Generation,
			InvocationID: invocation.Plan.InvocationID,
			WorkspaceID:  input.WorkspaceID,
		})
		if callErr != nil {
			return nil, computerCommandFailure(callErr)
		}
		if result.Session.Binding.Principal != principal ||
			result.Session.Binding.Target.Node != string(invocation.Plan.NodeID) ||
			result.Session.Binding.SessionID != input.SessionID ||
			result.Session.Binding.Generation != input.Generation {
			return nil, computerCommandFailure(computer.ErrStale)
		}
		if result.Output != nil && (result.Output.InvocationID != invocation.Plan.InvocationID ||
			result.Output.WorkspaceID != input.WorkspaceID) {
			return nil, computerCommandFailure(computer.ErrStale)
		}
		if result.Validate(handler.descriptorValue.ComputerProfiles) != nil {
			return nil, computerCommandFailure(computer.ErrStale)
		}
		return result, nil
	default:
		var input nodes.ComputerControlInput
		if json.Unmarshal(invocation.Input, &input) != nil {
			return nil, nodes.ErrCommandDenied
		}
		event, expected := computerControlEvent(handler.descriptorValue.Name)
		if event == "" {
			return nil, nodes.ErrCommandDenied
		}
		result, callErr := handler.host.Control(ctx, nodes.ComputerHostControlRequest{
			ComputerHostStatusRequest: nodes.ComputerHostStatusRequest{
				Principal: principal,
				SessionID: input.SessionID,
			},
			Generation: input.Generation,
			Event:      event,
		})
		if callErr != nil {
			return nil, computerCommandFailure(callErr)
		}
		binding := result.Session.Binding
		validTransition := binding.Generation > input.Generation && binding.Generation-input.Generation == 1 &&
			result.Session.Control == expected
		if event == computer.EventRelease && result.Session.Control.Terminal() {
			validTransition = binding.Generation == input.Generation ||
				binding.Generation > input.Generation && binding.Generation-input.Generation == 1
		}
		if !computerResultOwned(result, principal, invocation.Plan.NodeID) || binding.SessionID != input.SessionID ||
			!validTransition {
			return nil, errors.Join(ErrInvocationOutcomeUnknown, computerCommandFailure(computer.ErrStale))
		}
		return result, nil
	}
}

func computerResultOwned(result nodes.ComputerSessionResult, principal runtimecap.Principal, nodeID nodes.ID) bool {
	return result.Session.Binding.Principal == principal && result.Session.Binding.Target.Node == string(nodeID)
}

func computerControlEvent(command string) (computer.ControlEvent, computer.ControlState) {
	switch command {
	case nodes.ComputerCommandHandoff:
		return computer.EventHandoff, computer.ControlHuman
	case nodes.ComputerCommandRequestResume:
		return computer.EventRequestResume, computer.ControlResumePending
	case nodes.ComputerCommandResume:
		return computer.EventResume, computer.ControlAgent
	case nodes.ComputerCommandRelease:
		return computer.EventRelease, computer.ControlReleased
	default:
		return "", ""
	}
}

func computerCommandFailure(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, ErrInvocationOutcomeUnknown):
		return errors.Join(ErrInvocationOutcomeUnknown, newCommandFailure(
			nodes.InvocationDispatchUnknown, "computer command outcome is unknown", ErrInvocationOutcomeUnknown,
		))
	case errors.Is(err, computer.ErrDenied), errors.Is(err, computer.ErrInvalid):
		return newCommandFailure(
			nodes.InvocationDispatchCommandDenied,
			"computer command denied",
			nodes.ErrCommandDenied,
		)
	case errors.Is(err, computer.ErrStale), errors.Is(err, computer.ErrConflict):
		return newCommandFailure(nodes.InvocationDispatchComputerStale, "computer state is stale", computer.ErrStale)
	case errors.Is(err, computer.ErrBusy):
		return newCommandFailure(nodes.InvocationDispatchComputerBusy, "computer desktop is busy", computer.ErrBusy)
	case errors.Is(err, computer.ErrExpired):
		return newCommandFailure(
			nodes.InvocationDispatchComputerExpired,
			"computer session expired",
			computer.ErrExpired,
		)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return newCommandFailure(
			nodes.InvocationDispatchCommandTimeout,
			"computer command did not complete",
			context.DeadlineExceeded,
		)
	default:
		return newCommandFailure(
			nodes.InvocationDispatchComputerUnavailable,
			"computer command unavailable",
			computer.ErrUnavailable,
		)
	}
}
