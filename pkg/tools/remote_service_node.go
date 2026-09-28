package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

var ErrRemoteServiceUnavailable = errors.New("remote service unavailable")

// RemoteServiceOperation is the model-safe projection of one exact service
// command. Node identity, raw unit names, manager details, and profile aliases
// remain inside the gateway-to-companion boundary.
type RemoteServiceOperation struct {
	Target            string
	Available         bool
	Risk              nodes.Risk
	InputSchema       json.RawMessage
	ResultKind        string
	SupportsProgress  bool
	SupportsCancel    bool
	discoveryRevision string
}

// RemoteServiceNodeRouter is the closed service adapter used by the coding
// capability broker. It deliberately does not accept arbitrary node commands.
type RemoteServiceNodeRouter struct {
	agentID string
	runtime *nodeInvocationToolRuntime
	invoke  *NodeInvokeTool
}

func NewRemoteServiceNodeRouter(
	cfg *config.Config,
	source NodeInvocationSource,
	agentID string,
) (*RemoteServiceNodeRouter, error) {
	if cfg == nil || source == nil || strings.TrimSpace(agentID) == "" {
		return nil, fmt.Errorf("remote service node router requires config, source, and agent")
	}
	runtime := newNodeInvocationToolRuntime(NewNodeToolOptions(cfg), source)
	return &RemoteServiceNodeRouter{
		agentID: agentID,
		runtime: runtime,
		invoke:  &NodeInvokeTool{runtime: runtime},
	}, nil
}

func (router *RemoteServiceNodeRouter) SetEventPublisher(eventBus runtimeevents.Bus) {
	if router != nil && router.runtime != nil {
		router.runtime.runtimeEvents = eventBus
	}
}

// Describe revalidates the agent target policy, approved catalog, exact target
// service profile, approval posture, and projection bounds. A service action is
// absent unless the operator has explicitly configured approval bypass for the
// target; the coding grant alone never creates that authority.
func (router *RemoteServiceNodeRouter) Describe(
	target string,
	command string,
) (RemoteServiceOperation, error) {
	if router == nil || router.runtime == nil || router.runtime.access == nil ||
		!nodes.IsServiceCommand(command) {
		return RemoteServiceOperation{}, ErrRemoteServiceUnavailable
	}
	resolved, err := router.runtime.resolveTarget(router.agentID, target, false)
	if err != nil || resolved.registration == nil || resolved.requiresReapproval {
		return RemoteServiceOperation{}, ErrRemoteServiceUnavailable
	}
	descriptor, found := visibleNodeCommand(resolved.snapshot.Catalog, resolved.registration, command)
	if !found || descriptor.ModelContract == nil {
		return RemoteServiceOperation{}, ErrRemoteServiceUnavailable
	}
	descriptor, found = nodes.ProjectServiceDescriptorForProfile(
		descriptor,
		resolved.binding.ServiceProfile,
	)
	if !found || len(descriptor.ServiceProfiles) != 1 || descriptor.ModelContract == nil ||
		descriptor.ModelContract.Availability != nodes.ModelAvailable {
		return RemoteServiceOperation{}, ErrRemoteServiceUnavailable
	}
	revision, err := router.runtime.access.discoveryRevision(
		router.agentID,
		resolved.name,
		command,
		resolved.snapshot,
		*resolved.registration,
		descriptor,
		resolved.available,
	)
	if err != nil {
		return RemoteServiceOperation{}, ErrRemoteServiceUnavailable
	}
	contractDescriptor := projectServiceApprovalForTarget(
		descriptor,
		router.runtime.access.bypassesApproval(target),
	)
	if contractDescriptor.ModelContract == nil ||
		contractDescriptor.ModelContract.ApprovalMode != "" ||
		!commandProjectionFits(contractDescriptor) {
		return RemoteServiceOperation{}, ErrRemoteServiceUnavailable
	}
	if command == "service.action.v1" &&
		contractDescriptor.ServiceProfiles[0].ActionApproval != "operator_bypass_configured" {
		return RemoteServiceOperation{}, ErrRemoteServiceUnavailable
	}
	risk, ok := remoteServiceCodingRisk(command, descriptor.Risk)
	if !ok {
		return RemoteServiceOperation{}, ErrRemoteServiceUnavailable
	}
	schema := nodes.ServiceCommandInputSchema(command, contractDescriptor.ServiceProfiles)
	if len(schema) == 0 || !json.Valid(schema) || schema[0] != '{' {
		return RemoteServiceOperation{}, ErrRemoteServiceUnavailable
	}
	return RemoteServiceOperation{
		Target: target, Available: resolved.available, Risk: risk,
		InputSchema:       append(json.RawMessage(nil), schema...),
		ResultKind:        contractDescriptor.ModelContract.ResultKind,
		SupportsProgress:  contractDescriptor.SupportsProgress,
		SupportsCancel:    contractDescriptor.SupportsCancel,
		discoveryRevision: revision,
	}, nil
}

func (router *RemoteServiceNodeRouter) Execute(
	ctx context.Context,
	target string,
	command string,
	arguments map[string]any,
) *toolshared.ToolResult {
	described, err := router.Describe(target, command)
	if err != nil || !described.Available {
		return toolshared.ErrorResult("remote service operation is unavailable")
	}
	if command == "service.action.v1" && router.runtime.access.bypassesApproval(target) {
		ctx = toolshared.WithToolApprovalBypass(ctx, true)
	}
	return router.invoke.Execute(ctx, map[string]any{
		"target":             target,
		"command":            command,
		"input":              arguments,
		"discovery_revision": described.discoveryRevision,
	})
}

func remoteServiceCodingRisk(command string, risk nodes.Risk) (nodes.Risk, bool) {
	switch command {
	case "service.status.v1", "service.logs.v1":
		return nodes.RiskRead, risk == nodes.RiskRead
	case "service.action.v1":
		return nodes.RiskWrite, risk == nodes.RiskPrivileged
	default:
		return "", false
	}
}
