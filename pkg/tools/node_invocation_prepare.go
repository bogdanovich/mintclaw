package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func isNodeFileTransferDescriptor(descriptor nodes.CommandDescriptor) bool {
	return len(descriptor.FileProfiles) > 0 || isNodeFileTransferCommand(descriptor.Name)
}

func isNodeFileTransferCommand(command string) bool {
	switch command {
	case "file.info.v1", "file.upload.v1", "file.download.v1", nodes.InternalJobArtifactDownloadCommand:
		return true
	default:
		return false
	}
}

func isNodeDownloadTransferCommand(command string) bool {
	return command == "file.download.v1" || command == nodes.InternalJobArtifactDownloadCommand
}

func (runtime *nodeInvocationToolRuntime) prepareInternal(
	ctx context.Context,
	args map[string]any,
	allowWorkspace bool,
) (nodes.GatewayInvocationRecord, error) {
	if runtime == nil || runtime.source == nil || runtime.access == nil {
		return nodes.GatewayInvocationRecord{}, denyNodeInvocation(
			nodeDenialTargetUnavailable,
			nodeConstraintCommandPolicy,
			nodeActionRefreshDiscovery,
			nil,
		)
	}
	agentID := strings.TrimSpace(toolshared.ToolAgentID(ctx))
	resolved, err := runtime.resolveTarget(agentID, stringArgument(args, "target"), false)
	if err != nil {
		if strings.TrimSpace(stringArgument(args, "discovery_revision")) != "" &&
			(errors.Is(err, errNodeTargetNotVisible) || errors.Is(err, errNodeTargetNotPaired)) {
			return nodes.GatewayInvocationRecord{}, denyStaleNodeDiscovery()
		}
		return nodes.GatewayInvocationRecord{}, denyNodeInvocation(
			nodeDenialTargetUnavailable,
			nodeConstraintCommandPolicy,
			nodeActionRefreshDiscovery,
			err,
		)
	}
	command := strings.TrimSpace(stringArgument(args, "command"))
	if command == "" {
		return nodes.GatewayInvocationRecord{}, denyNodeInvocation(
			nodeDenialCommandUnavailable,
			nodeConstraintCommandPolicy,
			nodeActionRefreshDiscovery,
			nil,
		)
	}
	descriptor, advertised := nodeCatalogDescriptor(resolved.snapshot.Catalog, command)
	if !advertised {
		return nodes.GatewayInvocationRecord{}, denyNodeInvocation(
			nodeDenialCommandUnavailable,
			nodeConstraintCommandPolicy,
			nodeActionRefreshDiscovery,
			nil,
		)
	}
	if nodes.IsWorkspaceCommand(command) && !allowWorkspace {
		return nodes.GatewayInvocationRecord{}, denyNodeInvocation(
			nodeDenialCommandUnavailable,
			nodeConstraintCommandPolicy,
			nodeActionAskOperator,
			nil,
		)
	}
	if isNodeFileTransferDescriptor(descriptor) &&
		(!allowWorkspace || !nodes.IsWorkspaceCommand(command)) {
		return nodes.GatewayInvocationRecord{}, denyNodeInvocation(
			nodeDenialCommandUnavailable,
			nodeConstraintCommandPolicy,
			nodeActionRefreshDiscovery,
			nil,
		)
	}
	if len(descriptor.FileProfiles) > 0 {
		var projected bool
		descriptor, projected = projectFileDescriptorForTarget(
			descriptor,
			resolved.binding.FileProfile,
		)
		if !projected {
			return nodes.GatewayInvocationRecord{}, denyNodeInvocation(
				nodeDenialCommandUnavailable,
				nodeConstraintCommandPolicy,
				nodeActionRefreshDiscovery,
				nil,
			)
		}
	}
	if resolved.requiresReapproval {
		return nodes.GatewayInvocationRecord{}, denyNodeInvocation(
			nodeDenialReapprovalRequired,
			nodeConstraintCommandPolicy,
			nodeActionAskOperator,
			nil,
		)
	}
	if len(descriptor.ServiceProfiles) > 0 {
		var projected bool
		descriptor, projected = nodes.ProjectServiceDescriptorForProfile(
			descriptor,
			resolved.binding.ServiceProfile,
		)
		if !projected {
			return nodes.GatewayInvocationRecord{}, denyNodeInvocation(
				nodeDenialCommandUnavailable,
				nodeConstraintCommandPolicy,
				nodeActionRefreshDiscovery,
				nil,
			)
		}
	}
	if len(descriptor.UpdateProfiles) > 0 {
		var projected bool
		descriptor, projected = projectUpdateDescriptorForTarget(
			descriptor,
			resolved.binding.UpdateProfile,
		)
		if !projected {
			return nodes.GatewayInvocationRecord{}, denyNodeInvocation(
				nodeDenialCommandUnavailable,
				nodeConstraintCommandPolicy,
				nodeActionRefreshDiscovery,
				nil,
			)
		}
	}
	if len(descriptor.JobProfiles) > 0 {
		var projected bool
		descriptor, projected = nodes.ProjectJobDescriptorForProfile(
			descriptor,
			resolved.binding.JobProfile,
		)
		if !projected {
			return nodes.GatewayInvocationRecord{}, denyNodeInvocation(
				nodeDenialCommandUnavailable,
				nodeConstraintCommandPolicy,
				nodeActionRefreshDiscovery,
				nil,
			)
		}
	}
	currentRevision, err := runtime.access.discoveryRevision(
		agentID,
		resolved.name,
		command,
		resolved.snapshot,
		*resolved.registration,
		descriptor,
		resolved.available,
	)
	if err != nil {
		return nodes.GatewayInvocationRecord{}, denyNodeInvocation(
			nodeDenialDiscoveryIncomplete,
			nodeConstraintInputSchema,
			nodeActionRefreshDiscovery,
			err,
		)
	}
	if strings.TrimSpace(stringArgument(args, "discovery_revision")) != currentRevision {
		return nodes.GatewayInvocationRecord{}, denyStaleNodeDiscovery()
	}
	if !resolved.available {
		return nodes.GatewayInvocationRecord{}, denyNodeInvocation(
			nodeDenialTargetUnavailable,
			nodeConstraintCommandPolicy,
			nodeActionRefreshDiscovery,
			nil,
		)
	}
	if descriptor.ModelContract == nil ||
		descriptor.ModelContract.Availability == nodes.ModelPartiallyDescribed {
		return nodes.GatewayInvocationRecord{}, denyNodeInvocation(
			nodeDenialDiscoveryIncomplete,
			nodeConstraintInputSchema,
			nodeActionRefreshDiscovery,
			nil,
		)
	}
	if descriptor.ModelContract.Availability == nodes.ModelUnavailable &&
		(!allowWorkspace || !nodes.IsWorkspaceCommand(command)) {
		return nodes.GatewayInvocationRecord{}, denyNodeInvocation(
			nodeDenialCommandUnavailable,
			nodeConstraintCommandPolicy,
			nodeActionAskOperator,
			nil,
		)
	}
	descriptor, err = resolved.registration.ApprovedCommand(command)
	if err != nil {
		return nodes.GatewayInvocationRecord{}, denyNodeInvocation(
			nodeDenialReapprovalRequired,
			nodeConstraintCommandPolicy,
			nodeActionRefreshDiscovery,
			err,
		)
	}
	if len(descriptor.FileProfiles) > 0 {
		var projected bool
		descriptor, projected = projectFileDescriptorForTarget(
			descriptor,
			resolved.binding.FileProfile,
		)
		if !projected {
			return nodes.GatewayInvocationRecord{}, denyStaleNodeDiscovery()
		}
	}
	if len(descriptor.ServiceProfiles) > 0 {
		var projected bool
		descriptor, projected = nodes.ProjectServiceDescriptorForProfile(
			descriptor,
			resolved.binding.ServiceProfile,
		)
		if !projected {
			return nodes.GatewayInvocationRecord{}, denyStaleNodeDiscovery()
		}
	}
	if len(descriptor.UpdateProfiles) > 0 {
		var projected bool
		descriptor, projected = projectUpdateDescriptorForTarget(
			descriptor,
			resolved.binding.UpdateProfile,
		)
		if !projected {
			return nodes.GatewayInvocationRecord{}, denyStaleNodeDiscovery()
		}
	}
	if len(descriptor.JobProfiles) > 0 {
		var projected bool
		descriptor, projected = nodes.ProjectJobDescriptorForProfile(
			descriptor,
			resolved.binding.JobProfile,
		)
		if !projected {
			return nodes.GatewayInvocationRecord{}, denyStaleNodeDiscovery()
		}
	}
	profile := nodes.ExecutionProfile{
		Executor:       resolved.snapshot.Executor,
		PolicyRevision: resolved.snapshot.PolicyRevision,
	}
	if profileErr := profile.Validate(); profileErr != nil {
		return nodes.GatewayInvocationRecord{}, denyNodeInvocation(
			nodeDenialCommandUnavailable,
			nodeConstraintCommandPolicy,
			nodeActionAskOperator,
			profileErr,
		)
	}
	if resolved.binding.Executor != "" && resolved.binding.Executor != profile.Executor {
		return nodes.GatewayInvocationRecord{}, denyNodeInvocation(
			nodeDenialCommandUnavailable,
			nodeConstraintCommandPolicy,
			nodeActionAskOperator,
			nil,
		)
	}
	input, ok := args["input"].(map[string]any)
	if !ok {
		return nodes.GatewayInvocationRecord{}, denyNodeInvocation(
			nodeDenialSchemaInvalid,
			nodeConstraintInputSchema,
			nodeActionCorrectInput,
			nil,
		)
	}
	if constraintErr := validateNodeModelConstraints(descriptor, input); constraintErr != nil {
		return nodes.GatewayInvocationRecord{}, constraintErr
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return nodes.GatewayInvocationRecord{}, denyNodeInvocation(
			nodeDenialSchemaInvalid,
			nodeConstraintInputSchema,
			nodeActionCorrectInput,
			err,
		)
	}
	timeoutMaximum := nodes.MaxInvocationTimeout
	outputMaximum := nodes.MaxInvocationOutput
	if descriptor.ModelContract != nil {
		timeoutMaximum = descriptor.ModelContract.TimeoutSecondsMax
		outputMaximum = descriptor.ModelContract.OutputBytesMax
	}
	timeoutDefault := min(defaultNodeInvocationTimeout, timeoutMaximum)
	if descriptor.Name == "node.update.v1" {
		timeoutDefault = timeoutMaximum
	}
	timeout, err := boundedNodeInteger(
		args,
		"timeout_seconds",
		timeoutDefault,
		timeoutMaximum,
	)
	if err != nil {
		return nodes.GatewayInvocationRecord{}, denyNodeInvocation(
			nodeDenialConstraintViolation,
			nodeConstraintTimeout,
			nodeActionCorrectInput,
			err,
		)
	}
	outputLimit, err := boundedNodeInteger(
		args,
		"output_limit_bytes",
		min(defaultNodeInvocationOutput, outputMaximum),
		outputMaximum,
	)
	if err != nil {
		return nodes.GatewayInvocationRecord{}, denyNodeInvocation(
			nodeDenialConstraintViolation,
			nodeConstraintOutputLimit,
			nodeActionCorrectInput,
			err,
		)
	}
	principal, executionCallID, err := nodeInvocationIdentity(ctx)
	if err != nil {
		return nodes.GatewayInvocationRecord{}, err
	}
	invocationIdentity := []string{
		principal.AgentID,
		principal.SessionID,
		principal.ActorID,
		executionCallID,
	}
	if workspaceAuthority := nodeInvocationWorkspaceAuthority(ctx); workspaceAuthority != "" {
		invocationIdentity = append(invocationIdentity, workspaceAuthority)
	}
	invocationID := stableNodeInvocationID("inv", invocationIdentity...)
	storedToolCallID := stableNodeInvocationID("call", executionCallID)
	var updateAuthority *nodes.NodeUpdatePlanAuthority
	if len(descriptor.UpdateProfiles) == 1 {
		updateAuthority, err = nodes.NewNodeUpdatePlanAuthority(
			executionCallID,
			descriptor.UpdateProfiles[0],
			strings.TrimSpace(stringArgument(input, "release")),
		)
		if err != nil {
			return nodes.GatewayInvocationRecord{}, denyNodeInvocation(
				nodeDenialConstraintViolation,
				nodeConstraintProfile,
				nodeActionRefreshDiscovery,
				err,
			)
		}
	}
	request := nodes.InvocationRequest{
		InvocationID:     invocationID,
		IdempotencyKey:   stableNodeInvocationID("idem", invocationID),
		NodeID:           resolved.snapshot.ID,
		CatalogHash:      resolved.snapshot.CatalogHash,
		Command:          command,
		ServiceProfile:   serviceProfileForInvocation(descriptor),
		JobProfile:       jobProfileForInvocation(descriptor),
		Update:           updateAuthority,
		Input:            inputJSON,
		AgentID:          principal.AgentID,
		SessionID:        principal.SessionID,
		ActorID:          principal.ActorID,
		TimeoutSeconds:   timeout,
		OutputLimitBytes: outputLimit,
	}
	plan, err := nodes.PrepareExecutionPlan(
		request,
		descriptor,
		profile.Executor,
		profile.PolicyRevision,
		time.Now(),
		nodes.MaxExecutionPlanTTL,
	)
	if err != nil {
		return nodes.GatewayInvocationRecord{}, denyNodeInvocation(
			nodeDenialSchemaInvalid,
			nodeConstraintInputSchema,
			nodeActionCorrectInput,
			err,
		)
	}
	requestedRevision := strings.TrimSpace(stringArgument(args, "discovery_revision"))
	record, created, err := runtime.source.PrepareInvocation(
		resolved.binding.Node,
		resolved.name,
		storedToolCallID,
		principal,
		plan,
		descriptor,
		!toolshared.ToolApprovalContinuation(ctx),
		func(current NodeDiscoveryRecord) error {
			return runtime.validatePreparationAuthority(
				agentID,
				resolved.name,
				command,
				requestedRevision,
				current,
				allowWorkspace,
			)
		},
	)
	if errors.Is(err, nodes.ErrGatewayInvocationNotFound) && toolshared.ToolApprovalContinuation(ctx) {
		return nodes.GatewayInvocationRecord{}, denyStaleNodeDiscovery()
	}
	if errors.Is(err, errDiscoveryStale) {
		return nodes.GatewayInvocationRecord{}, denyStaleNodeDiscovery()
	}
	if err != nil {
		return nodes.GatewayInvocationRecord{}, denyNodeInvocation(
			nodeDenialTargetUnavailable,
			nodeConstraintCommandPolicy,
			nodeActionRefreshDiscovery,
			err,
		)
	}
	if !created {
		if retainedErr := validateRetainedNodeInvocation(
			record,
			resolved.name,
			request,
			descriptor,
			profile,
		); retainedErr != nil {
			return nodes.GatewayInvocationRecord{}, denyStaleNodeDiscovery()
		}
		return record, nil
	}
	if err == nil && created {
		runtime.publishInvocationEvent(
			ctx,
			NodeInvocationObservationPrepared,
			runtime.invocationEventSource(),
			record,
			string(nodes.GatewayInvocationPrepared),
			"",
		)
	}
	return record, err
}

func (runtime *nodeInvocationToolRuntime) validatePreparationAuthority(
	agentID string,
	target string,
	command string,
	requestedRevision string,
	current NodeDiscoveryRecord,
	allowWorkspace bool,
) error {
	if current.Registration == nil || current.Snapshot.ID == "" {
		return errDiscoveryStale
	}
	_, advertised := nodeCatalogDescriptor(current.Snapshot.Catalog, command)
	if !advertised {
		return errDiscoveryStale
	}
	descriptor, err := current.Registration.ApprovedCommand(command)
	if err != nil {
		return errDiscoveryStale
	}
	if len(descriptor.FileProfiles) > 0 {
		binding, exists := runtime.access.targets[target]
		if !exists {
			return errDiscoveryStale
		}
		var projected bool
		descriptor, projected = projectFileDescriptorForTarget(descriptor, binding.FileProfile)
		if !projected {
			return errDiscoveryStale
		}
	}
	if len(descriptor.ServiceProfiles) > 0 {
		binding, exists := runtime.access.targets[target]
		if !exists {
			return errDiscoveryStale
		}
		var projected bool
		descriptor, projected = nodes.ProjectServiceDescriptorForProfile(
			descriptor,
			binding.ServiceProfile,
		)
		if !projected {
			return errDiscoveryStale
		}
	}
	if len(descriptor.UpdateProfiles) > 0 {
		binding, exists := runtime.access.targets[target]
		if !exists {
			return errDiscoveryStale
		}
		var projected bool
		descriptor, projected = projectUpdateDescriptorForTarget(
			descriptor,
			binding.UpdateProfile,
		)
		if !projected {
			return errDiscoveryStale
		}
	}
	if len(descriptor.JobProfiles) > 0 {
		binding, exists := runtime.access.targets[target]
		if !exists {
			return errDiscoveryStale
		}
		var projected bool
		descriptor, projected = nodes.ProjectJobDescriptorForProfile(
			descriptor,
			binding.JobProfile,
		)
		if !projected {
			return errDiscoveryStale
		}
	}
	revision, err := runtime.access.discoveryRevision(
		agentID,
		target,
		command,
		current.Snapshot,
		*current.Registration,
		descriptor,
		current.Connected,
	)
	if err != nil || revision != requestedRevision {
		return errDiscoveryStale
	}
	if !current.Connected {
		return errDiscoveryStale
	}
	if descriptor.ModelContract != nil && descriptor.ModelContract.Availability == nodes.ModelUnavailable &&
		(!allowWorkspace || !nodes.IsWorkspaceCommand(command)) {
		return errDiscoveryStale
	}
	return nil
}
