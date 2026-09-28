package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	codingremote "github.com/bogdanovich/mintclaw/pkg/coding/remote"
	codingscope "github.com/bogdanovich/mintclaw/pkg/coding/scope"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	"github.com/bogdanovich/mintclaw/pkg/tools/loopguard"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type (
	BrokerClient         = codingremote.BrokerClient
	CapabilitySnapshot   = codingremote.CapabilitySnapshot
	CapabilityDescriptor = codingremote.CapabilityDescriptor
	OperationDescriptor  = codingremote.OperationDescriptor
	CapabilityResult     = codingremote.CapabilityResult
	TaskScopeDescriptor  = codingremote.TaskScopeDescriptor
	Request              = codingremote.Request
	Operation            = codingremote.Operation
	BrokerError          = codingremote.BrokerError
)

const (
	SchemaV1                  = codingremote.SchemaV1
	MaxArgumentsBytes         = codingremote.MaxArgumentsBytes
	OperationCapabilitiesList = codingremote.OperationCapabilitiesList
	OperationCapabilityInvoke = codingremote.OperationCapabilityInvoke
	OperationInvocationStatus = codingremote.OperationInvocationStatus
	OperationInvocationCancel = codingremote.OperationInvocationCancel
)

var safeBrokerCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

const codingRemoteOperationTimeout = 60 * time.Second

// ToolAuthority is fixed before the coding tool registry is constructed.
type CodingRemoteToolAuthority struct {
	Grant         string
	GrantRevision string
	ThreadID      string
	SessionKey    string
	ProjectKey    string
	LocalProfile  codingscope.Profile
}

// CapabilityTool exposes only aliases already admitted by one broker
// snapshot. Every execution is revalidated by the gateway against the exact
// snapshot, grant, catalog, and node policy revisions.
type CodingRemoteCapabilityTool struct {
	client    BrokerClient
	authority CodingRemoteToolAuthority

	mu          sync.RWMutex
	snapshot    CapabilitySnapshot
	retained    map[string]string
	invocations map[string]codingRemoteInvocationLink
}

type codingRemoteInvocationLink struct {
	Capability         string
	CapabilityRevision string
	Operation          string
	Target             string
	Risk               codingremote.Risk
}

func NewCodingRemoteCapabilityTool(
	client BrokerClient,
	authority CodingRemoteToolAuthority,
	snapshot CapabilitySnapshot,
) (*CodingRemoteCapabilityTool, error) {
	if codingRemoteBrokerClientNil(client) || !codingremote.ValidAlias(authority.Grant) ||
		authority.GrantRevision == "" ||
		!codingremote.LocalProfileAllowed(authority.LocalProfile) {
		return nil, errors.New("coding remote capability tool authority is unavailable")
	}
	if snapshot.Schema != "" && (snapshot.Validate() != nil || snapshot.Grant != authority.Grant ||
		snapshot.GrantRevision != authority.GrantRevision) {
		return nil, errors.New("coding remote capability tool snapshot is unavailable")
	}
	probe := Request{
		Schema: SchemaV1, RequestID: "authority-probe", Operation: OperationCapabilitiesList,
		Grant: authority.Grant, GrantRevision: authority.GrantRevision,
		ThreadID: authority.ThreadID, SessionKey: authority.SessionKey,
		ProjectKey: authority.ProjectKey, LocalProfile: authority.LocalProfile,
	}
	if err := probe.Validate(); err != nil {
		return nil, errors.New("coding remote capability tool authority is invalid")
	}
	retained := make(map[string]string, len(snapshot.Capabilities))
	for _, capability := range snapshot.Capabilities {
		retained[capability.Alias] = capability.Revision
	}
	return &CodingRemoteCapabilityTool{
		client: client, authority: authority, snapshot: snapshot, retained: retained,
		invocations: make(map[string]codingRemoteInvocationLink),
	}, nil
}

func codingRemoteBrokerClientNil(client BrokerClient) bool {
	if client == nil {
		return true
	}
	value := reflect.ValueOf(client)
	return value.Kind() == reflect.Pointer && value.IsNil()
}

func (*CodingRemoteCapabilityTool) Name() string { return "remote_capability" }

func (*CodingRemoteCapabilityTool) Description() string {
	return "List or invoke one explicitly granted typed capability on a paired companion, or inspect/cancel " +
		"an invocation returned by this tool. Remote placement is explicit. A failed or uncertain call never " +
		"falls back locally and must not be replayed; use status with the retained invocation_id."
}

func (tool *CodingRemoteCapabilityTool) Parameters() map[string]any {
	snapshot := tool.currentSnapshot()
	capabilities := make([]string, 0, len(snapshot.Capabilities))
	operations := make([]string, 0)
	for _, capability := range snapshot.Capabilities {
		capabilities = append(capabilities, capability.Alias)
		for _, operation := range capability.Operations {
			operations = append(operations, operation.Alias)
		}
	}
	tool.mu.RLock()
	for alias := range tool.retained {
		capabilities = append(capabilities, alias)
	}
	tool.mu.RUnlock()
	slices.Sort(capabilities)
	capabilities = slices.Compact(capabilities)
	slices.Sort(operations)
	operations = slices.Compact(operations)
	capabilityProperty := map[string]any{
		"type": "string", "description": "Exact capability alias returned by list or a retained invocation.",
	}
	if len(capabilities) > 0 {
		capabilityProperty["enum"] = capabilities
	}
	operationProperty := map[string]any{
		"type": "string", "description": "Exact operation alias returned for the selected capability.",
	}
	if len(operations) > 0 {
		operationProperty["enum"] = operations
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type": "string", "enum": []string{"list", "invoke", "status", "cancel"},
			},
			"capability": capabilityProperty,
			"operation":  operationProperty,
			"input": map[string]any{
				"type": "object", "description": "Typed input matching the listed operation schema.",
			},
			"invocation_id": map[string]any{
				"type": "string", "description": "Durable invocation ID returned by invoke.",
			},
		},
		"required":             []string{"action"},
		"additionalProperties": false,
	}
}

func (tool *CodingRemoteCapabilityTool) CodingStartObservation(args map[string]any) *toolshared.ToolObservation {
	action, _ := args["action"].(string)
	if action == "" || action == "list" {
		return nil
	}
	capability, _ := args["capability"].(string)
	operation, _ := args["operation"].(string)
	command := capability
	if operation != "" {
		command += "." + operation
	}
	invocationID, _ := args["invocation_id"].(string)
	return toolshared.SanitizeToolObservation(&toolshared.ToolObservation{
		Command: &toolshared.CommandObservation{
			Action: action, Command: command, Source: "remote", Status: "running", SessionID: invocationID,
		},
	})
}

func (tool *CodingRemoteCapabilityTool) Execute(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	action := strings.TrimSpace(stringToolArgument(args, "action"))
	switch action {
	case "list":
		if len(args) != 1 {
			return remoteToolError("INVALID_ARGUMENTS", "list accepts no additional arguments")
		}
		return tool.list(ctx)
	case "invoke", "status", "cancel":
		return tool.executeOperation(ctx, action, args)
	default:
		return remoteToolError("INVALID_ACTION", "select list, invoke, status, or cancel")
	}
}

func (tool *CodingRemoteCapabilityTool) list(ctx context.Context) *toolshared.ToolResult {
	request := tool.baseRequest(OperationCapabilitiesList, tool.authority.GrantRevision)
	refreshed, err := tool.client.Discover(ctx, request)
	if err != nil {
		return remoteBrokerToolError(err)
	}
	if refreshed.Validate() != nil || refreshed.Grant != tool.authority.Grant ||
		refreshed.GrantRevision != tool.authority.GrantRevision {
		return remoteToolError("RESULT_UNAVAILABLE", "capability list is unavailable")
	}
	tool.mu.Lock()
	tool.snapshot = refreshed
	for _, capability := range refreshed.Capabilities {
		tool.retained[capability.Alias] = capability.Revision
	}
	tool.mu.Unlock()
	encoded, err := json.Marshal(refreshed)
	if err != nil {
		return remoteToolError("RESULT_UNAVAILABLE", "capability list is unavailable")
	}
	return toolshared.NewToolResult(string(encoded))
}

func (tool *CodingRemoteCapabilityTool) executeOperation(
	ctx context.Context,
	action string,
	args map[string]any,
) *toolshared.ToolResult {
	runtime, ok := toolshared.RuntimeCapabilities(ctx)
	if !ok {
		return remoteToolError("IDENTITY_UNAVAILABLE", "turn-bound coding identity is unavailable")
	}
	principal, ok := runtime.Principal()
	if !ok || principal.Runtime != runtimecap.KindCoding || principal.Validate() != nil ||
		principal.SessionID != tool.authority.SessionKey {
		return remoteToolError("IDENTITY_UNAVAILABLE", "turn-bound coding identity is unavailable")
	}
	providerCallID := strings.TrimSpace(toolshared.ToolCallID(ctx))
	if providerCallID == "" {
		return remoteToolError("IDENTITY_UNAVAILABLE", "tool-call identity is unavailable")
	}
	snapshot := tool.currentSnapshot()
	capabilityAlias := strings.TrimSpace(stringToolArgument(args, "capability"))
	capability, found := snapshotCapability(snapshot, capabilityAlias)
	capabilityRevision := ""
	operationAlias := ""
	invocationID := ""
	expectedTarget := ""
	expectedRisk := codingremote.Risk("")
	invocationLink := codingRemoteInvocationLink{}

	switch action {
	case "invoke":
		if len(args) != 4 {
			return remoteToolError("INVALID_ARGUMENTS", "invoke requires capability, operation, and input")
		}
		if !found {
			return remoteToolError("CAPABILITY_UNAVAILABLE", "capability is unavailable; call list")
		}
		capabilityRevision = capability.Revision
		operationAlias = strings.TrimSpace(stringToolArgument(args, "operation"))
		operation, operationFound := snapshotOperation(capability, operationAlias)
		if !operationFound {
			return remoteToolError("OPERATION_UNAVAILABLE", "operation is unavailable; call list")
		}
		expectedTarget = capability.Target
		expectedRisk = operation.Risk
		invocationLink = codingRemoteInvocationLink{
			Capability: capabilityAlias, CapabilityRevision: capabilityRevision,
			Operation: operationAlias, Target: expectedTarget, Risk: expectedRisk,
		}
	case "status", "cancel":
		if len(args) != 3 {
			return remoteToolError("INVALID_ARGUMENTS", action+" requires capability and invocation_id")
		}
		invocationID = strings.TrimSpace(stringToolArgument(args, "invocation_id"))
		link, linked := tool.invocationLink(invocationID)
		if !linked || link.Capability != capabilityAlias {
			return remoteToolError(
				"INVOCATION_UNAVAILABLE",
				"invocation was not returned by this remote capability",
			)
		}
		capabilityRevision = link.CapabilityRevision
		operationAlias = link.Operation
		expectedTarget = link.Target
		expectedRisk = link.Risk
	}

	request := tool.baseRequest(operationForAction(action), tool.authority.GrantRevision)
	deadline := time.Now().Add(codingRemoteOperationTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	request.DeadlineUnixMS = deadline.UnixMilli()
	request.Principal = &principal
	request.CallID = trustedCallID(principal, providerCallID)
	request.DiscoveryRevision = snapshot.DiscoveryRevision
	request.Capability = capabilityAlias
	request.CapabilityRevision = capabilityRevision
	request.CapabilityOperation = operationAlias

	switch action {
	case "invoke":
		input, ok := args["input"].(map[string]any)
		if !ok || input == nil {
			return remoteToolError("INVALID_ARGUMENTS", "invoke input must be an object")
		}
		encoded, err := json.Marshal(input)
		if err != nil || len(encoded) > MaxArgumentsBytes {
			return remoteToolError("INVALID_ARGUMENTS", "invoke input is invalid")
		}
		request.Arguments = encoded
		request.InvocationID = codingremote.DeriveInvocationID(request)
		if !tool.retainInvocation(request.InvocationID, invocationLink) {
			return remoteToolError("RESULT_UNAVAILABLE", "remote capability invocation identity conflicts")
		}
	case "status", "cancel":
		request.InvocationID = invocationID
	}
	result, err := tool.client.Execute(ctx, request)
	if err != nil {
		if action == "invoke" && remoteCapabilityInvocationUncertain(err) {
			return capabilityToolResult(
				action,
				remoteCapabilityUncertainResult(request, invocationLink, remoteCapabilityUncertainCode(err)),
			)
		}
		if action == "invoke" {
			tool.forgetInvocation(request.InvocationID, invocationLink)
		}
		return remoteBrokerToolError(err)
	}
	if result.Validate() != nil || result.Grant != request.Grant ||
		result.GrantRevision != request.GrantRevision ||
		result.DiscoveryRevision != request.DiscoveryRevision ||
		result.Capability != request.Capability ||
		result.CapabilityRevision != request.CapabilityRevision ||
		result.Operation != request.CapabilityOperation ||
		result.Target != expectedTarget || result.Risk != expectedRisk ||
		result.InvocationID != request.InvocationID {
		if action == "invoke" {
			return capabilityToolResult(
				action,
				remoteCapabilityUncertainResult(request, invocationLink, "RESULT_UNAVAILABLE"),
			)
		}
		return remoteToolError("RESULT_UNAVAILABLE", "remote capability result is unavailable")
	}
	if action == "invoke" && !tool.retainInvocation(result.InvocationID, invocationLink) {
		return remoteToolError("RESULT_UNAVAILABLE", "remote capability invocation identity conflicts")
	}
	return capabilityToolResult(action, result)
}

func (tool *CodingRemoteCapabilityTool) baseRequest(operation Operation, grantRevision string) Request {
	return Request{
		Schema: SchemaV1, RequestID: "request_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		Operation: operation, Grant: tool.authority.Grant, GrantRevision: grantRevision,
		ThreadID: tool.authority.ThreadID, SessionKey: tool.authority.SessionKey,
		ProjectKey: tool.authority.ProjectKey, LocalProfile: tool.authority.LocalProfile,
	}
}

func (tool *CodingRemoteCapabilityTool) currentSnapshot() CapabilitySnapshot {
	tool.mu.RLock()
	defer tool.mu.RUnlock()
	return cloneCapabilitySnapshot(tool.snapshot)
}

func (tool *CodingRemoteCapabilityTool) invocationLink(
	invocationID string,
) (codingRemoteInvocationLink, bool) {
	tool.mu.RLock()
	defer tool.mu.RUnlock()
	link, ok := tool.invocations[invocationID]
	return link, ok
}

func (tool *CodingRemoteCapabilityTool) retainInvocation(
	invocationID string,
	link codingRemoteInvocationLink,
) bool {
	tool.mu.Lock()
	defer tool.mu.Unlock()
	retained, exists := tool.invocations[invocationID]
	if exists {
		return retained == link
	}
	tool.invocations[invocationID] = link
	tool.retained[link.Capability] = link.CapabilityRevision
	return true
}

func (tool *CodingRemoteCapabilityTool) forgetInvocation(
	invocationID string,
	link codingRemoteInvocationLink,
) {
	tool.mu.Lock()
	defer tool.mu.Unlock()
	if retained, exists := tool.invocations[invocationID]; exists && retained == link {
		delete(tool.invocations, invocationID)
	}
}

func operationForAction(action string) Operation {
	switch action {
	case "invoke":
		return OperationCapabilityInvoke
	case "status":
		return OperationInvocationStatus
	case "cancel":
		return OperationInvocationCancel
	default:
		return ""
	}
}

func snapshotCapability(snapshot CapabilitySnapshot, alias string) (CapabilityDescriptor, bool) {
	index, found := slices.BinarySearchFunc(
		snapshot.Capabilities,
		alias,
		func(value CapabilityDescriptor, want string) int {
			return strings.Compare(value.Alias, want)
		},
	)
	if !found {
		return CapabilityDescriptor{}, false
	}
	return snapshot.Capabilities[index], true
}

func snapshotOperation(capability CapabilityDescriptor, alias string) (OperationDescriptor, bool) {
	index, found := slices.BinarySearchFunc(
		capability.Operations,
		alias,
		func(value OperationDescriptor, want string) int {
			return strings.Compare(value.Alias, want)
		},
	)
	if !found {
		return OperationDescriptor{}, false
	}
	return capability.Operations[index], true
}

func trustedCallID(principal runtimecap.Principal, providerCallID string) string {
	digest := sha256.New()
	for _, value := range []string{
		principal.ActorID, principal.AgentID, principal.SessionID, principal.ExecutionID, providerCallID,
	} {
		_, _ = fmt.Fprintf(digest, "%d:", len(value))
		_, _ = digest.Write([]byte(value))
	}
	return "call_" + hex.EncodeToString(digest.Sum(nil))
}

func capabilityToolResult(action string, result CapabilityResult) *toolshared.ToolResult {
	encoded, err := json.Marshal(result)
	if err != nil {
		return remoteToolError("RESULT_UNAVAILABLE", "remote capability result is unavailable")
	}
	toolResult := toolshared.NewToolResult(string(encoded))
	if result.ErrorCode != "" || slices.Contains(
		[]string{"denied", "failed", "rejected", "unknown", "unavailable"}, result.State,
	) {
		toolResult.IsError = true
	}
	command := result.Capability
	if result.Operation != "" {
		command += "." + result.Operation
	}
	toolResult.WithObservation(toolshared.CommandObservation{
		Action: action, Command: command, Source: "remote", Status: result.State,
		SessionID: result.InvocationID, Canceled: result.CancellationConfirmed,
	})
	for _, change := range result.Changes {
		toolResult.WithWriteAudit(toolshared.WriteAuditEntry{
			Kind: "file", Target: "remote:" + result.Capability + "/" + change.Path,
			Action: change.Action, Tool: "remote_capability",
			Metadata: map[string]string{"placement": "remote", "target": result.Target},
		})
	}
	return toolResult
}

func remoteCapabilityInvocationUncertain(err error) bool {
	var brokerErr *BrokerError
	return !errors.As(err, &brokerErr) || brokerErr.Code == "INVOCATION_UNCERTAIN"
}

func remoteCapabilityUncertainCode(err error) string {
	errorCode := "BROKER_UNAVAILABLE"
	var brokerErr *BrokerError
	if errors.As(err, &brokerErr) && safeBrokerCodePattern.MatchString(brokerErr.Code) {
		errorCode = brokerErr.Code
	}
	return errorCode
}

func remoteCapabilityUncertainResult(
	request Request,
	link codingRemoteInvocationLink,
	errorCode string,
) CapabilityResult {
	return CapabilityResult{
		Grant: request.Grant, GrantRevision: request.GrantRevision,
		DiscoveryRevision: request.DiscoveryRevision,
		Capability:        request.Capability, CapabilityRevision: request.CapabilityRevision,
		Operation: link.Operation, InvocationID: request.InvocationID,
		Target: link.Target, Risk: link.Risk, State: "unknown", ErrorCode: errorCode,
		RecoveryAction: "Call remote_capability status with this invocation_id; do not replay the operation.",
	}
}

func remoteBrokerToolError(err error) *toolshared.ToolResult {
	code := "BROKER_UNAVAILABLE"
	message := "remote capability broker is unavailable"
	var brokerErr *BrokerError
	if errors.As(err, &brokerErr) && safeBrokerCodePattern.MatchString(brokerErr.Code) {
		code = brokerErr.Code
		if safeBrokerMessage(brokerErr.Message) {
			message = brokerErr.Message
		}
	}
	return remoteToolError(code, message)
}

func remoteToolError(code, message string) *toolshared.ToolResult {
	encoded, _ := json.Marshal(map[string]string{"status": "error", "code": code, "message": message})
	return toolshared.ErrorResult(string(encoded)).WithObservation(toolshared.CommandObservation{
		Action: "remote", Source: "remote", Status: "failed",
	})
}

func stringToolArgument(args map[string]any, name string) string {
	value, _ := args[name].(string)
	return value
}

func cloneCapabilitySnapshot(snapshot CapabilitySnapshot) CapabilitySnapshot {
	cloned := snapshot
	cloned.Capabilities = make([]CapabilityDescriptor, len(snapshot.Capabilities))
	for index, capability := range snapshot.Capabilities {
		cloned.Capabilities[index] = capability
		cloned.Capabilities[index].Operations = make([]OperationDescriptor, len(capability.Operations))
		for operationIndex, operation := range capability.Operations {
			cloned.Capabilities[index].Operations[operationIndex] = operation
			cloned.Capabilities[index].Operations[operationIndex].InputSchema = append(
				json.RawMessage(nil), operation.InputSchema...,
			)
		}
	}
	cloned.TaskScopes = append([]TaskScopeDescriptor(nil), snapshot.TaskScopes...)
	for index := range cloned.TaskScopes {
		cloned.TaskScopes[index].Profiles = append([]codingscope.Profile(nil), snapshot.TaskScopes[index].Profiles...)
	}
	return cloned
}

func (*CodingRemoteCapabilityTool) ToolLoopSemantics() loopguard.Semantics {
	// The closed action union contains mutations and cancellation. The runtime
	// therefore journals every call conservatively even when this invocation is
	// a list, read, or status observation.
	return loopguard.SemanticsMutating
}

func safeBrokerMessage(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 1024 {
		return false
	}
	return !strings.ContainsFunc(value, func(character rune) bool {
		return character < 0x20 || character == 0x7f
	})
}
