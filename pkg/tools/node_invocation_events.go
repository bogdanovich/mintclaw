package tools

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type nodeInvocationWorkspaceContextKey struct{}

type nodeInvocationWorkspaceContext struct {
	alias    string
	revision string
}

func withNodeInvocationWorkspace(ctx context.Context, alias string, revision string) context.Context {
	return context.WithValue(ctx, nodeInvocationWorkspaceContextKey{}, nodeInvocationWorkspaceContext{
		alias: alias, revision: revision,
	})
}

func nodeInvocationWorkspaceAuthority(ctx context.Context) string {
	workspace, ok := ctx.Value(nodeInvocationWorkspaceContextKey{}).(nodeInvocationWorkspaceContext)
	if !ok {
		return ""
	}
	return stableNodeInvocationID("workspace", workspace.alias, workspace.revision)
}

func (runtime *nodeInvocationToolRuntime) invocationEventSource() string {
	if runtime != nil && strings.TrimSpace(runtime.eventSource) != "" {
		return runtime.eventSource
	}
	return "nodes_invoke"
}

func serviceProfileForInvocation(descriptor nodes.CommandDescriptor) string {
	if len(descriptor.ServiceProfiles) == 1 {
		return descriptor.ServiceProfiles[0].Alias
	}
	return ""
}

func jobProfileForInvocation(descriptor nodes.CommandDescriptor) string {
	if len(descriptor.JobProfiles) == 1 {
		return descriptor.JobProfiles[0].Alias
	}
	return ""
}

func nodeCatalogDescriptor(
	catalog nodes.CapabilityCatalog,
	command string,
) (nodes.CommandDescriptor, bool) {
	for _, descriptor := range catalog.Commands {
		if descriptor.Name == command {
			return descriptor, true
		}
	}
	return nodes.CommandDescriptor{}, false
}

func (runtime *nodeInvocationToolRuntime) publishInvocationEvent(
	ctx context.Context,
	observation string,
	sourceName string,
	record nodes.GatewayInvocationRecord,
	state string,
	errorCode string,
	result ...json.RawMessage,
) {
	if runtime == nil {
		return
	}
	publishNodeInvocationEvent(
		runtime.runtimeEvents,
		ctx,
		observation,
		sourceName,
		record,
		state,
		errorCode,
		result...,
	)
}

func publishNodeInvocationEvent(
	eventBus runtimeevents.Bus,
	ctx context.Context,
	observation string,
	sourceName string,
	record nodes.GatewayInvocationRecord,
	state string,
	errorCode string,
	result ...json.RawMessage,
) {
	if eventBus == nil {
		return
	}
	sessionKey := strings.TrimSpace(toolshared.ToolRouteSessionKey(ctx))
	if sessionKey == "" {
		sessionKey = strings.TrimSpace(toolshared.ToolSessionKey(ctx))
	}
	traceTurnID := strings.TrimSpace(toolshared.ToolTraceTurnID(ctx))
	if traceTurnID == "" {
		traceTurnID = strings.TrimSpace(toolshared.ToolExecutionID(ctx))
	}
	gatewayState := record.State
	if observation != NodeInvocationObservationPrepared {
		gatewayState = nodes.GatewayInvocationDispatched
	}
	payload := NodeInvocationEventPayload{
		Observation:    observation,
		InvocationID:   record.Plan.InvocationID,
		Target:         record.Target,
		ScopeAlias:     nodeInvocationScopeAlias(record),
		ProfileAlias:   nodeInvocationProfileAlias(record),
		PolicyRevision: record.Plan.PolicyRevision,
		Command:        record.Plan.Command,
		Risk:           record.Plan.Risk,
		GatewayState:   gatewayState,
		State:          state,
		ErrorCode:      errorCode,
	}
	if workspace, ok := ctx.Value(nodeInvocationWorkspaceContextKey{}).(nodeInvocationWorkspaceContext); ok {
		payload.Workspace = workspace.alias
		payload.WorkspaceRevision = workspace.revision
		payload.ScopeAlias = workspace.alias
	}
	payload.Service, payload.Action, payload.LogEntries = serviceInvocationObservation(record.Plan)
	observeJobInvocation(&payload, record.Plan, result...)
	severity := runtimeevents.SeverityInfo
	if observation == NodeInvocationObservationUncertain ||
		observation == NodeInvocationObservationRejected {
		severity = runtimeevents.SeverityWarn
	}
	attrs := map[string]any{
		"observation":     payload.Observation,
		"invocation_id":   payload.InvocationID,
		"target":          payload.Target,
		"policy_revision": payload.PolicyRevision,
		"command":         payload.Command,
		"risk":            payload.Risk,
		"gateway_state":   payload.GatewayState,
		"state":           payload.State,
	}
	if payload.ScopeAlias != "" {
		attrs["scope_alias"] = payload.ScopeAlias
	}
	if payload.ProfileAlias != "" {
		attrs["profile_alias"] = payload.ProfileAlias
	}
	if payload.Workspace != "" {
		attrs["workspace"] = payload.Workspace
		attrs["workspace_revision"] = payload.WorkspaceRevision
	}
	if payload.ErrorCode != "" {
		attrs["error_code"] = payload.ErrorCode
	}
	if payload.Service != "" {
		attrs["service"] = payload.Service
	}
	if payload.Action != "" {
		attrs["action"] = payload.Action
	}
	if payload.LogEntries > 0 {
		attrs["log_entries"] = payload.LogEntries
	}
	if payload.JobProfile != "" {
		attrs["job_profile"] = payload.JobProfile
	}
	if payload.JobID != "" {
		attrs["job_id"] = payload.JobID
	}
	if payload.JobState != "" {
		attrs["job_state"] = payload.JobState
	}
	if payload.JobLogStream != "" {
		attrs["job_log_stream"] = payload.JobLogStream
		attrs["job_log_bytes"] = payload.JobLogBytes
		attrs["job_log_cursor"] = payload.JobLogCursor
	}
	if payload.ArtifactCount > 0 {
		attrs["artifact_count"] = payload.ArtifactCount
	}
	if payload.CancelDisposition != "" {
		attrs["cancel_disposition"] = payload.CancelDisposition
	}
	eventBus.PublishNonBlocking(runtimeevents.Event{
		Kind:   runtimeevents.KindNodeInvocationObserved,
		Source: runtimeevents.Source{Component: "nodes", Name: sourceName},
		Scope: runtimeevents.Scope{
			TraceScope: runtimeevents.NewTraceScope(
				toolshared.ToolWorkspace(ctx),
				traceTurnID,
			),
			AgentID:    toolshared.ToolAgentID(ctx),
			SessionKey: sessionKey,
			Channel:    toolshared.ToolChannel(ctx),
			ChatID:     toolshared.ToolChatID(ctx),
			TopicID:    toolshared.ToolTopicID(ctx),
			SenderID:   toolshared.ToolSenderID(ctx),
			MessageID:  toolshared.ToolMessageID(ctx),
		},
		Correlation: runtimeevents.Correlation{RequestID: toolshared.ToolCallID(ctx)},
		Severity:    severity,
		Payload:     payload,
		Attrs:       attrs,
	})
}

func nodeInvocationScopeAlias(record nodes.GatewayInvocationRecord) string {
	input, ok := nodeInvocationInputObject(record.Plan.Input)
	if !ok {
		return ""
	}
	if record.Plan.Command == nodes.CodingCommandTaskStart {
		var start nodes.CodingTaskStartInput
		if json.Unmarshal(record.Plan.Input, &start) == nil && start.Validate() == nil {
			return start.ScopeAlias
		}
	}
	if record.Descriptor.ModelContract == nil {
		return ""
	}
	allowed := record.Descriptor.ModelContract.Constraints.WorkingScopes
	for _, key := range []string{"working_scope", "cwd"} {
		alias, _ := input[key].(string)
		if slices.Contains(allowed, alias) && nodes.Alias(alias).Validate() == nil {
			return alias
		}
	}
	return ""
}

func nodeInvocationProfileAlias(record nodes.GatewayInvocationRecord) string {
	candidates := []string{record.Plan.ServiceProfile, record.Plan.JobProfile}
	if record.Plan.Update != nil {
		candidates = append(candidates, record.Plan.Update.Profile)
	}
	if len(record.Descriptor.FileProfiles) == 1 {
		candidates = append(candidates, record.Descriptor.FileProfiles[0].Alias)
	}
	if len(record.Descriptor.BrowserProfiles) == 1 {
		candidates = append(candidates, record.Descriptor.BrowserProfiles[0].Alias)
	}
	for _, candidate := range candidates {
		if nodes.Alias(candidate).Validate() == nil {
			return candidate
		}
	}
	if record.Plan.Command == nodes.CodingCommandTaskStart {
		var start nodes.CodingTaskStartInput
		if json.Unmarshal(record.Plan.Input, &start) == nil && start.Validate() == nil {
			return string(start.Profile)
		}
	}
	if record.Descriptor.ModelContract == nil {
		return ""
	}
	input, ok := nodeInvocationInputObject(record.Plan.Input)
	if !ok {
		return ""
	}
	profile, _ := input["profile"].(string)
	if slices.Contains(record.Descriptor.ModelContract.Constraints.ProfileAliases, profile) &&
		nodes.Alias(profile).Validate() == nil {
		return profile
	}
	return ""
}

func nodeInvocationInputObject(input json.RawMessage) (map[string]any, bool) {
	var value map[string]any
	if json.Unmarshal(input, &value) != nil || value == nil {
		return nil, false
	}
	return value, true
}

func observeJobInvocation(
	payload *NodeInvocationEventPayload,
	plan nodes.ExecutionPlan,
	results ...json.RawMessage,
) {
	if payload == nil || (!nodes.IsJobCommand(plan.Command) &&
		plan.Command != nodes.InternalJobArtifactDownloadCommand) {
		return
	}
	payload.JobProfile = plan.JobProfile
	var input struct {
		JobID      string `json:"job_id"`
		JobProfile string `json:"job_profile"`
	}
	if json.Unmarshal(plan.Input, &input) == nil {
		if nodes.ID(input.JobID).Validate() == nil {
			payload.JobID = input.JobID
		}
		if payload.JobProfile == "" && nodes.Alias(input.JobProfile).Validate() == nil {
			payload.JobProfile = input.JobProfile
		}
	}
	if len(results) == 0 || len(results[0]) == 0 {
		return
	}
	var output struct {
		JobID       string            `json:"job_id"`
		State       string            `json:"state"`
		Stream      string            `json:"stream"`
		Data        string            `json:"data"`
		NextCursor  int64             `json:"next_cursor"`
		Artifacts   []json.RawMessage `json:"artifacts"`
		Disposition string            `json:"disposition"`
	}
	if json.Unmarshal(results[0], &output) != nil {
		return
	}
	if nodes.ID(output.JobID).Validate() == nil {
		payload.JobID = output.JobID
	}
	if len(output.State) <= nodes.MaxIDLength && nodes.ID(output.State).Validate() == nil {
		payload.JobState = output.State
	}
	if plan.Command == nodes.JobCommandLogs && (output.Stream == "stdout" || output.Stream == "stderr") {
		payload.JobLogStream = output.Stream
		payload.JobLogBytes = len([]byte(output.Data))
		if output.NextCursor >= 0 && output.NextCursor <= nodes.MaxJobLogBytes {
			payload.JobLogCursor = output.NextCursor
		}
	}
	if plan.Command == nodes.JobCommandArtifacts && len(output.Artifacts) <= nodes.MaxJobArtifactCount {
		payload.ArtifactCount = len(output.Artifacts)
	}
	if plan.Command == nodes.JobCommandCancel && len(output.Disposition) <= nodes.MaxIDLength &&
		nodes.ID(output.Disposition).Validate() == nil {
		payload.CancelDisposition = output.Disposition
	}
}

func serviceInvocationObservation(
	plan nodes.ExecutionPlan,
) (string, nodes.ServiceAction, int) {
	if !nodes.IsServiceCommand(plan.Command) {
		return "", "", 0
	}
	var input struct {
		Service string              `json:"service"`
		Action  nodes.ServiceAction `json:"action"`
		Entries float64             `json:"entries"`
	}
	if err := json.Unmarshal(plan.Input, &input); err != nil ||
		(nodes.Alias(input.Service)).Validate() != nil {
		return "", "", 0
	}
	entries := 0
	if input.Entries > 0 && input.Entries <= nodes.MaxServiceLogEntries {
		entries = int(input.Entries)
	}
	if !input.Action.Valid() {
		input.Action = ""
	}
	return input.Service, input.Action, entries
}

func validateRetainedNodeInvocation(
	retained nodes.GatewayInvocationRecord,
	target string,
	request nodes.InvocationRequest,
	descriptor nodes.CommandDescriptor,
	profile nodes.ExecutionProfile,
) error {
	ttlSeconds := retained.Plan.ExpiresAt - retained.Plan.PreparedAt
	if ttlSeconds <= 0 {
		return errors.New("retained invocation has invalid authority")
	}
	candidate, err := nodes.PrepareExecutionPlan(
		request,
		descriptor,
		profile.Executor,
		profile.PolicyRevision,
		time.Unix(retained.Plan.PreparedAt, 0),
		time.Duration(ttlSeconds)*time.Second,
	)
	if err != nil ||
		retained.Target != target ||
		candidate.PlanHash != retained.ExpectedPlanHash {
		return errors.New("tool call conflicts with retained invocation authority")
	}
	return nil
}
