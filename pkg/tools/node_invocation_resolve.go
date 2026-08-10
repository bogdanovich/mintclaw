package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func (runtime *nodeInvocationToolRuntime) visibleInvocation(
	ctx context.Context,
	args map[string]any,
) (
	nodes.GatewayInvocationRecord,
	nodes.GatewayInvocationPrincipal,
	nodes.Snapshot,
	bool,
	error,
) {
	if runtime == nil || runtime.source == nil || runtime.access == nil {
		return nodes.GatewayInvocationRecord{}, nodes.GatewayInvocationPrincipal{},
			nodes.Snapshot{}, false, errors.New("node invocation runtime is unavailable")
	}
	invocationID := strings.TrimSpace(stringArgument(args, "invocation_id"))
	if invocationID == "" {
		return nodes.GatewayInvocationRecord{}, nodes.GatewayInvocationPrincipal{},
			nodes.Snapshot{}, false, errors.New("invocation_id is required")
	}
	principal, err := nodeInvocationIdentityWithoutCall(ctx)
	if err != nil {
		return nodes.GatewayInvocationRecord{}, nodes.GatewayInvocationPrincipal{},
			nodes.Snapshot{}, false, err
	}
	record, found, err := runtime.source.LookupInvocation(principal, invocationID)
	if err != nil {
		return nodes.GatewayInvocationRecord{}, nodes.GatewayInvocationPrincipal{},
			nodes.Snapshot{}, false, errors.New("invocation registry is unavailable")
	}
	if !found {
		return nodes.GatewayInvocationRecord{}, nodes.GatewayInvocationPrincipal{},
			nodes.Snapshot{}, false, errors.New("invocation was not found in this scope")
	}
	resolved, err := runtime.resolveTarget(toolshared.ToolAgentID(ctx), record.Target, false)
	if err != nil || resolved.snapshot.ID != record.Plan.NodeID {
		return nodes.GatewayInvocationRecord{}, nodes.GatewayInvocationPrincipal{},
			nodes.Snapshot{}, false, errors.New("invocation target is no longer visible")
	}
	return record, principal, resolved.snapshot, resolved.available, nil
}

func (runtime *nodeInvocationToolRuntime) resolveTarget(
	agentID string,
	requested string,
	requireAvailable bool,
) (resolvedNodeTarget, error) {
	names, defaultTarget := runtime.access.visibleTargets(agentID)
	target := strings.TrimSpace(requested)
	if target == "" {
		target = defaultTarget
	}
	if target == "" || !containsSorted(names, target) {
		return resolvedNodeTarget{}, errNodeTargetNotVisible
	}
	entry, snapshot, registration, err := runtime.access.resolve(target, defaultTarget)
	if err != nil {
		return resolvedNodeTarget{}, errors.New("node registry lookup failed")
	}
	if snapshot == nil || registration == nil {
		return resolvedNodeTarget{}, errNodeTargetNotPaired
	}
	if requireAvailable && !entry.liveConnected {
		return resolvedNodeTarget{}, errors.New("target is not currently connected")
	}
	return resolvedNodeTarget{
		name:               target,
		binding:            runtime.access.targets[target],
		snapshot:           *snapshot,
		registration:       registration,
		available:          entry.liveConnected,
		requiresReapproval: entry.RequiresReapproval,
	}, nil
}

func nodeInvocationIdentity(
	ctx context.Context,
) (nodes.GatewayInvocationPrincipal, string, error) {
	principal, err := nodeInvocationIdentityWithoutCall(ctx)
	if err != nil {
		return nodes.GatewayInvocationPrincipal{}, "", err
	}
	toolCallID := strings.TrimSpace(toolshared.ToolCallID(ctx))
	executionID := strings.TrimSpace(toolshared.ToolExecutionID(ctx))
	workspace := strings.TrimSpace(toolshared.ToolWorkspace(ctx))
	if toolCallID == "" || executionID == "" || workspace == "" {
		return nodes.GatewayInvocationPrincipal{}, "", errors.New(
			"node invocation requires workspace, execution, and provider tool-call identity",
		)
	}
	return principal, stableNodeInvocationID(
		"execution",
		workspace,
		executionID,
		toolCallID,
	), nil
}

func nodeInvocationIdentityWithoutCall(
	ctx context.Context,
) (nodes.GatewayInvocationPrincipal, error) {
	agentID := strings.TrimSpace(toolshared.ToolAgentID(ctx))
	sessionID := strings.TrimSpace(toolshared.ToolRouteSessionKey(ctx))
	if sessionID == "" {
		sessionID = strings.TrimSpace(toolshared.ToolSessionKey(ctx))
	}
	actorID := strings.TrimSpace(toolshared.ToolActorID(ctx))
	if actorID == "" {
		actorID = strings.TrimSpace(toolshared.ToolSenderID(ctx))
	}
	if actorID == "" {
		actorID = agentID
	}
	if agentID == "" || sessionID == "" || actorID == "" {
		return nodes.GatewayInvocationPrincipal{}, errors.New(
			"node invocation requires agent, session, and actor identity",
		)
	}
	workspace := strings.TrimSpace(toolshared.ToolWorkspace(ctx))
	executionID := strings.TrimSpace(toolshared.ToolExecutionID(ctx))
	if workspace == "" || executionID == "" {
		return nodes.GatewayInvocationPrincipal{}, errors.New(
			"node invocation requires workspace and execution identity",
		)
	}
	return nodes.GatewayInvocationPrincipal{
		AgentID:     stableNodeInvocationID("agent", agentID),
		SessionID:   stableNodeInvocationID("session", sessionID),
		ActorID:     stableNodeInvocationID("actor", actorID),
		WorkspaceID: stableNodeInvocationID("workspace", workspace),
		ExecutionID: stableNodeInvocationID("execution_scope", workspace, executionID),
	}, nil
}

func stableNodeInvocationID(prefix string, values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = fmt.Fprintf(hash, "%d:", len(value))
		_, _ = hash.Write([]byte(value))
	}
	return prefix + "_" + hex.EncodeToString(hash.Sum(nil))
}

func boundedNodeInteger(
	args map[string]any,
	name string,
	fallback int,
	maximum int,
) (int, error) {
	raw, exists := args[name]
	if !exists {
		return fallback, nil
	}
	var value int
	switch typed := raw.(type) {
	case int:
		value = typed
	case int64:
		if typed > int64(maximum) {
			return 0, fmt.Errorf("%s is outside bounds", name)
		}
		value = int(typed)
	case float64:
		if typed < 1 || typed > float64(maximum) || typed != float64(int(typed)) {
			return 0, fmt.Errorf("%s must be an integer", name)
		}
		value = int(typed)
	default:
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	if value <= 0 || value > maximum {
		return 0, fmt.Errorf("%s is outside bounds", name)
	}
	return value, nil
}

func validateNodeModelConstraints(
	descriptor nodes.CommandDescriptor,
	input map[string]any,
) error {
	if descriptor.ModelContract == nil {
		return nil
	}
	constraints := descriptor.ModelContract.Constraints
	switch descriptor.Name {
	case "system.exec.v1":
		return validateSystemExecModelConstraints(descriptor, input, constraints)
	case "shell.exec.v1":
		return validateShellExecModelConstraints(descriptor, input, constraints)
	case nodes.WorkspaceCommandRead, nodes.WorkspaceCommandSearch,
		nodes.WorkspaceCommandWrite, nodes.WorkspaceCommandPatch:
		profile, profileOK := input["profile_revision"].(string)
		scope, scopeOK := input["working_scope"].(string)
		if !profileOK || !scopeOK ||
			!containsSorted(constraints.ProfileAliases, profile) ||
			!containsSorted(constraints.WorkingScopes, scope) {
			return denyNodeInvocation(
				nodeDenialConstraintViolation,
				nodeConstraintProfile,
				nodeActionRefreshDiscovery,
				nil,
			)
		}
		return nil
	default:
		return nil
	}
}

func validateSystemExecModelConstraints(
	descriptor nodes.CommandDescriptor,
	input map[string]any,
	constraints nodes.CommandModelConstraints,
) error {
	argv, ok := input["argv"].([]any)
	if !ok || len(argv) == 0 {
		return denyNodeInvocation(
			nodeDenialSchemaInvalid,
			nodeConstraintInputSchema,
			nodeActionCorrectInput,
			nil,
		)
	}
	executable, ok := argv[0].(string)
	if !ok || strings.TrimSpace(executable) == "" {
		return denyNodeInvocation(
			nodeDenialSchemaInvalid,
			nodeConstraintInputSchema,
			nodeActionCorrectInput,
			nil,
		)
	}
	if !containsSorted(constraints.ExecutableAliases, executable) {
		return denyNodeInvocation(
			nodeDenialConstraintViolation,
			nodeConstraintExecutable,
			nodeActionCorrectInput,
			nil,
		)
	}
	return validateNodeScopeEnvironmentTimeout(descriptor, input, constraints)
}

func validateShellExecModelConstraints(
	descriptor nodes.CommandDescriptor,
	input map[string]any,
	constraints nodes.CommandModelConstraints,
) error {
	if err := nodes.ValidateShellExecModelInput(input); err != nil {
		return denyNodeInvocation(
			nodeDenialConstraintViolation,
			nodeConstraintInputSize,
			nodeActionCorrectInput,
			err,
		)
	}
	profile, ok := input["profile"].(string)
	if !ok || !containsSorted(constraints.ProfileAliases, profile) {
		return denyNodeInvocation(
			nodeDenialConstraintViolation,
			nodeConstraintProfile,
			nodeActionCorrectInput,
			nil,
		)
	}
	if err := validateNodeScopeEnvironmentTimeout(descriptor, input, constraints); err != nil {
		return err
	}
	if err := nodes.ValidateShellExecModelInputSchema(*descriptor.ModelContract, input); err != nil {
		return denyNodeInvocation(
			nodeDenialSchemaInvalid,
			nodeConstraintInputSchema,
			nodeActionCorrectInput,
			err,
		)
	}
	return nil
}

func validateNodeScopeEnvironmentTimeout(
	descriptor nodes.CommandDescriptor,
	input map[string]any,
	constraints nodes.CommandModelConstraints,
) error {
	if raw, exists := input["cwd"]; exists {
		workingScope, valid := raw.(string)
		if !valid {
			return denyNodeInvocation(
				nodeDenialSchemaInvalid,
				nodeConstraintInputSchema,
				nodeActionCorrectInput,
				nil,
			)
		}
		if !containsSorted(constraints.WorkingScopes, workingScope) {
			return denyNodeInvocation(
				nodeDenialConstraintViolation,
				nodeConstraintWorkingScope,
				nodeActionCorrectInput,
				nil,
			)
		}
	}
	if raw, exists := input["env"]; exists {
		environment, valid := raw.(map[string]any)
		if !valid {
			return denyNodeInvocation(
				nodeDenialSchemaInvalid,
				nodeConstraintInputSchema,
				nodeActionCorrectInput,
				nil,
			)
		}
		for name := range environment {
			if !containsSorted(constraints.EnvironmentNames, name) {
				return denyNodeInvocation(
					nodeDenialConstraintViolation,
					nodeConstraintEnvironment,
					nodeActionCorrectInput,
					nil,
				)
			}
		}
	}
	if raw, exists := input["timeout_seconds"]; exists {
		timeout, valid := nodeInteger(raw)
		if !valid {
			return denyNodeInvocation(
				nodeDenialSchemaInvalid,
				nodeConstraintInputSchema,
				nodeActionCorrectInput,
				nil,
			)
		}
		if timeout <= 0 || timeout > descriptor.ModelContract.TimeoutSecondsMax {
			return denyNodeInvocation(
				nodeDenialConstraintViolation,
				nodeConstraintTimeout,
				nodeActionCorrectInput,
				nil,
			)
		}
	}
	return nil
}

func nodeInteger(raw any) (int, bool) {
	switch typed := raw.(type) {
	case int:
		return typed, true
	case int64:
		if int64(int(typed)) != typed {
			return 0, false
		}
		return int(typed), true
	case float64:
		if typed != float64(int(typed)) {
			return 0, false
		}
		return int(typed), true
	default:
		return 0, false
	}
}

func stringArgument(args map[string]any, name string) string {
	value, _ := args[name].(string)
	return value
}

func nodeInvocationError(code string, message string, view *nodeInvokeResult) *toolshared.ToolResult {
	payload := map[string]any{"error": message, "error_code": code}
	if view != nil {
		payload["invocation"] = view
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return toolshared.ErrorResult("node invocation failed")
	}
	return toolshared.ErrorResult(string(data))
}

func nodeDenialToolResult(denial nodeDenialResult) *toolshared.ToolResult {
	data, err := json.Marshal(denial)
	if err != nil {
		return toolshared.ErrorResult("node invocation denied")
	}
	return toolshared.ErrorResult(string(data))
}

func gatewayStatusResult(
	record nodes.GatewayInvocationRecord,
	available bool,
) nodeStatusResult {
	return nodeStatusResult{
		InvocationID:  record.Plan.InvocationID,
		Target:        record.Target,
		Command:       record.Plan.Command,
		Risk:          record.Plan.Risk,
		GatewayState:  record.State,
		NodeAvailable: available,
	}
}

func remoteStatusResult(
	gateway nodes.GatewayInvocationRecord,
	remote nodes.InvocationRecord,
	available bool,
) nodeStatusResult {
	view := gatewayStatusResult(gateway, available)
	view.State = string(remote.State)
	view.AcceptedAt = remote.AcceptedAt
	view.UpdatedAt = remote.UpdatedAt
	view.CompletedAt = remote.CompletedAt
	view.Result = remote.Result
	view.Failure = remote.Failure
	view.Cancellation = remote.Cancellation
	return view
}
