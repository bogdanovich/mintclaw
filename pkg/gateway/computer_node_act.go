package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

func (client *nodeComputerClient) Act(
	ctx context.Context, principal runtimecap.Principal, action tools.ComputerActRequest,
) (computer.Observation, error) {
	if nodes.ID(action.RequestID).Validate() != nil {
		return computer.Observation{}, computer.ErrInvalid
	}
	entry, err := client.locked(ctx, principal, action.SessionID)
	if err != nil {
		return computer.Observation{}, err
	}
	defer computerNodeUnlock(entry.gate)
	if !entry.authority.policy.AllowInput {
		return computer.Observation{}, computer.ErrDenied
	}
	if entry.uncertain || entry.inputUncertain {
		return computer.Observation{}, errComputerOutcomeUnknown
	}
	start := time.Now()
	session := entry.projection()
	if authorityErr := session.AgentAuthority(start); authorityErr != nil {
		return computer.Observation{}, authorityErr
	}
	if entry.evidence.ID == "" || session.Binding.Target.Scope.WindowID == "" {
		return computer.Observation{}, computer.ErrStale
	}
	request := computer.InputRequest{
		ID:      client.invocationID(principal, entry.authority.alias, nodes.ComputerCommandAct, action.RequestID),
		Binding: session.Binding, ObservationID: action.ObservationID, Action: action.Action, Text: action.Text,
	}
	if validationErr := request.Validate(session, entry.evidence, start); validationErr != nil {
		return computer.Observation{}, validationErr
	}
	input, payload, err := nodes.NewComputerActInputs(request, client.workspaceID)
	if err != nil {
		return computer.Observation{}, computer.ErrInvalid
	}
	var ephemeral json.RawMessage
	if payload != nil {
		ephemeral, err = json.Marshal(payload)
		if err != nil {
			return computer.Observation{}, computer.ErrInvalid
		}
		defer clear(ephemeral)
	}

	// The gateway's evidence window limits dispatch. The companion still
	// rechecks its own evidence, target and generation before granting input.
	writeBefore := entry.evidence.ExpiresAt
	if drag := request.Action.Drag; drag != nil {
		writeBefore = writeBefore.Add(-time.Duration(drag.DurationMS) * time.Millisecond)
	}
	actionContext, cancel := context.WithDeadline(ctx, session.ExpiresAt)
	defer cancel()
	entry.evidence = computer.InputEvidence{}
	var result nodes.ComputerActResult
	invocationID, err := client.invokeWithEphemeral(
		actionContext,
		entry.authority,
		principal,
		action.RequestID,
		nodes.ComputerCommandAct,
		input,
		ephemeral,
		writeBefore,
		&result,
	)
	if err != nil {
		entry.inputUncertain = errors.Is(err, errComputerOutcomeUnknown)
		return computer.Observation{}, err
	}
	if result.ActionID != invocationID || result.Observation.Observation.ID == request.ObservationID {
		entry.inputUncertain = true
		return computer.Observation{}, errComputerOutcomeUnknown
	}
	observation, err := client.acceptObservation(actionContext, entry, result.Observation, start, invocationID)
	if err != nil {
		entry.inputUncertain = true
		return computer.Observation{}, errComputerOutcomeUnknown
	}
	return observation, nil
}

func (client *nodeComputerClient) requestInputCancellation(
	authority computerNodeAuthority, principal nodes.GatewayInvocationPrincipal, invocationID string,
) {
	// The interrupted action context cannot send a cancellation. Sending it
	// here does not establish that the node received it or that input stopped.
	ctx, cancel := context.WithTimeout(context.Background(), computerNodeRequestTimeout)
	defer cancel()
	_, _, _ = client.source.CancelInvocation(
		ctx,
		principal,
		authority.policy.NodeTarget,
		authority.nodeID,
		invocationID,
	)
}
