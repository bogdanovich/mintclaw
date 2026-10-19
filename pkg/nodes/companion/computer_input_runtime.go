package companion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

// ComputerInputCommandHost is a separately constructed trusted owner, not a
// driver grant or a default host. It must enforce host-owned fresh evidence,
// native target/geometry, OS-local exclusion and control-generation interlocks
// before any dispatch. Act is synchronous and must not retain protected text.
// Partial/uncertain effects require ErrInvocationOutcomeUnknown. A disconnect
// uses this same owner's existing ComputerCommandHost.Disconnect boundary.
type ComputerInputCommandHost interface {
	ComputerCommandHost
	ComputerInputProfiles() []nodes.ComputerProfileDescriptor
	Act(context.Context, nodes.ComputerHostActRequest) (nodes.ComputerActResult, error)
}

// ErrComputerInputCancellationConfirmed is valid only after independently
// attested input stop and cleanup of this executor's owned held state. A canceled
// context, closed lease, killed worker or acknowledged stop is not that proof.
var ErrComputerInputCancellationConfirmed = errors.New("computer input stop and owned-state cleanup confirmed")

func WithComputerInputHost(host ComputerInputCommandHost) RuntimeOption {
	return func(options *runtimeOptions) error {
		if host == nil {
			return errors.New("computer input command host is required")
		}
		// One owner for capture/control/input/disconnect; do not construct a
		// separate lifecycle or infer input support from a read-only host.
		options.computerHost, options.computerInputHost = host, host
		return nil
	}
}

type computerInputCommandHandler struct {
	descriptorValue nodes.CommandDescriptor
	host            ComputerInputCommandHost
}

func newComputerInputCommandHandler(host ComputerInputCommandHost) (*computerInputCommandHandler, error) {
	descriptor, err := nodes.ComputerInputCommandDescriptor(host.ComputerInputProfiles())
	if err != nil {
		return nil, err
	}
	return &computerInputCommandHandler{descriptorValue: descriptor, host: host}, nil
}

func (handler *computerInputCommandHandler) descriptor() nodes.CommandDescriptor {
	return handler.descriptorValue
}

func (*computerInputCommandHandler) authorizeEphemeral(plan nodes.ExecutionPlan, raw json.RawMessage) error {
	request, err := nodes.BindComputerAct(plan, raw)
	clear(request.Text)
	return err
}

func (handler *computerInputCommandHandler) execute(ctx context.Context, invocation commandInvocation) (any, error) {
	request, err := nodes.BindComputerAct(invocation.Plan, invocation.EphemeralInput)
	if err != nil {
		return nil, nodes.ErrCommandDenied
	}
	defer clear(request.Text)
	var input nodes.ComputerActInput
	if json.Unmarshal(invocation.Input, &input) != nil {
		return nil, nodes.ErrCommandDenied
	}
	started := time.Now()
	result, err := handler.host.Act(ctx, nodes.ComputerHostActRequest{
		Request: request, WorkspaceID: input.WorkspaceID,
	})
	if errors.Is(err, ErrInvocationOutcomeUnknown) {
		return nil, computerCommandFailure(ErrInvocationOutcomeUnknown)
	}
	if errors.Is(err, ErrComputerInputCancellationConfirmed) {
		if !errors.Is(context.Cause(ctx), errCancellationRequested) {
			return nil, computerCommandFailure(ErrInvocationOutcomeUnknown)
		}
		return nil, fmt.Errorf("%w: computer input canceled", errCommandCancellationConfirmed)
	}
	if err != nil {
		switch {
		case errors.Is(err, computer.ErrDenied), errors.Is(err, computer.ErrInvalid), errors.Is(err, computer.ErrStale),
			errors.Is(err, computer.ErrConflict), errors.Is(err, computer.ErrBusy), errors.Is(err, computer.ErrExpired):
			// The host contract reserves these for pre-dispatch refusal, never
			// for a partial mutation. Do not copy its private error detail.
			return nil, computerCommandFailure(err)
		default:
			// Context cancellation is not held-state cleanup/effect proof.
			return nil, computerCommandFailure(ErrInvocationOutcomeUnknown)
		}
	}
	observation := result.Observation
	if result.ActionID != request.ID || observation.Session.Binding != request.Binding ||
		observation.Observation.ID == request.ObservationID ||
		observation.Observation.CapturedAt.Before(started) ||
		observation.Validate(handler.descriptorValue.ComputerProfiles) != nil {
		return nil, computerCommandFailure(ErrInvocationOutcomeUnknown)
	}
	if output := observation.Output; output != nil &&
		(output.InvocationID != invocation.Plan.InvocationID || output.WorkspaceID != input.WorkspaceID) {
		return nil, computerCommandFailure(ErrInvocationOutcomeUnknown)
	}
	return result, nil
}
