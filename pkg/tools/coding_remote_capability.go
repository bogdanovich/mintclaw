package tools

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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
	ArtifactResult       = codingremote.ArtifactResult
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
	OperationArtifactDescribe = codingremote.OperationArtifactDescribe
	OperationArtifactFetch    = codingremote.OperationArtifactFetch
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

// CodingRemoteArtifactStore admits a completely downloaded and verified
// temporary file into the canonical attachment set of the current coding
// thread. Implementations must copy the bytes before returning.
type CodingRemoteArtifactStore interface {
	ImportRemoteArtifact(
		context.Context,
		string,
		CodingRemoteArtifactMetadata,
	) (string, error)
}

type CodingRemoteArtifactMetadata struct {
	Filename    string
	ContentType string
	Size        int64
	SHA256      string
}

// CapabilityTool exposes only aliases already admitted by one broker
// snapshot. Every execution is revalidated by the gateway against the exact
// snapshot, grant, catalog, and node policy revisions.
type CodingRemoteCapabilityTool struct {
	client    BrokerClient
	authority CodingRemoteToolAuthority

	mu            sync.RWMutex
	snapshot      CapabilitySnapshot
	retained      map[string]string
	invocations   map[string]codingRemoteInvocationLink
	artifactStore CodingRemoteArtifactStore
}

type codingRemoteInvocationLink struct {
	Capability         string
	CapabilityRevision string
	Operation          string
	Target             string
	Risk               codingremote.Risk
	Kind               codingremote.CapabilityKind
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
		"an invocation returned by this tool. Job and browser output artifacts can be described or fetched into the current " +
		"coding thread as durable attachments. Remote placement is explicit. A failed or uncertain call never " +
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
				"type": "string", "enum": []string{
					"list", "invoke", "status", "cancel", "artifact_describe", "artifact_fetch",
				},
			},
			"capability": capabilityProperty,
			"operation":  operationProperty,
			"input": map[string]any{
				"type": "object", "description": "Typed input matching the listed operation schema.",
			},
			"invocation_id": map[string]any{
				"type": "string", "description": "Durable invocation ID returned by invoke.",
			},
			"artifact_ref": map[string]any{
				"type":        "string",
				"description": "Opaque output artifact reference returned by the producing invocation.",
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
	case "artifact_describe", "artifact_fetch":
		return tool.executeArtifact(ctx, action, args)
	default:
		return remoteToolError(
			"INVALID_ACTION",
			"select list, invoke, status, cancel, artifact_describe, or artifact_fetch",
		)
	}
}

// SetArtifactStore binds the canonical attachment writer after the coding
// composition root has acquired the thread lease.
func (tool *CodingRemoteCapabilityTool) SetArtifactStore(store CodingRemoteArtifactStore) {
	if tool == nil {
		return
	}
	tool.mu.Lock()
	tool.artifactStore = store
	tool.mu.Unlock()
}

func (tool *CodingRemoteCapabilityTool) executeArtifact(
	ctx context.Context,
	action string,
	args map[string]any,
) *toolshared.ToolResult {
	if len(args) < 4 || len(args) > 5 {
		return remoteToolError(
			"INVALID_ARGUMENTS",
			action+" requires capability, invocation_id, artifact_ref, and browser operation when applicable",
		)
	}
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
	capabilityAlias := strings.TrimSpace(stringToolArgument(args, "capability"))
	invocationID := strings.TrimSpace(stringToolArgument(args, "invocation_id"))
	artifactRef := strings.TrimSpace(stringToolArgument(args, "artifact_ref"))
	operationAlias := strings.TrimSpace(stringToolArgument(args, "operation"))
	snapshot := tool.currentSnapshot()
	capability, found := snapshotCapability(snapshot, capabilityAlias)
	if !found || invocationID == "" || artifactRef == "" {
		return remoteToolError("ARTIFACT_UNAVAILABLE", "artifact is not owned by this remote invocation")
	}
	if operationAlias == "" {
		if capability.Kind == codingremote.CapabilityRemoteWorkspace {
			operationAlias = "workspace_exec"
		} else if retained, linked := tool.invocationLink(invocationID); linked {
			operationAlias = retained.Operation
		}
	}
	operation, operationFound := snapshotOperation(capability, operationAlias)
	validArtifactOperation := capability.Kind == codingremote.CapabilityRemoteWorkspace &&
		operationAlias == "workspace_exec" || capability.Kind == codingremote.CapabilityBrowserProfile &&
		(operationAlias == "browser_capture" || operationAlias == "browser_act")
	if !operationFound || !validArtifactOperation {
		return remoteToolError(
			"ARTIFACT_UNAVAILABLE",
			"artifact operation is unavailable; provide the exact producing browser operation after resume",
		)
	}
	link := codingRemoteInvocationLink{
		Capability: capabilityAlias, CapabilityRevision: capability.Revision,
		Operation: operationAlias, Target: capability.Target, Risk: operation.Risk, Kind: capability.Kind,
	}
	if retained, linked := tool.invocationLink(invocationID); linked && retained != link {
		return remoteToolError("ARTIFACT_UNAVAILABLE", "artifact is not owned by this remote invocation")
	}
	request := tool.artifactRequest(
		ctx,
		principal,
		providerCallID,
		snapshot,
		capability,
		operationAlias,
		invocationID,
		artifactRef,
		OperationArtifactDescribe,
	)
	described, err := tool.client.Artifact(ctx, request)
	if err != nil {
		return remoteBrokerToolError(err)
	}
	if !matchingArtifactResult(described, request, link) {
		return remoteToolError("RESULT_UNAVAILABLE", "remote artifact description is unavailable")
	}
	// Invocation links are a process-local fast path, not the authority for a
	// retained artifact. A resumed coding runtime reconstructs this link only
	// after the gateway has resolved the exact owner-bound durable invocation.
	if !tool.retainInvocation(invocationID, link) {
		return remoteToolError("RESULT_UNAVAILABLE", "remote artifact authority conflicts")
	}
	if action == "artifact_describe" {
		return artifactToolResult(action, described, "")
	}
	tool.mu.RLock()
	store := tool.artifactStore
	tool.mu.RUnlock()
	if store == nil {
		return remoteToolError("ARTIFACT_STORE_UNAVAILABLE", "coding attachment store is unavailable")
	}
	if described.Size > codingremote.MaxFetchedArtifactBytes {
		return remoteToolError("ARTIFACT_TOO_LARGE", "remote artifact exceeds the coding attachment limit")
	}
	attachmentRef, fetchErr := tool.fetchArtifact(ctx, store, request, described, link)
	if fetchErr != nil {
		return remoteToolError("ARTIFACT_FETCH_FAILED", "remote artifact could not be fetched safely")
	}
	return artifactToolResult(action, described, attachmentRef)
}

func (tool *CodingRemoteCapabilityTool) artifactRequest(
	ctx context.Context,
	principal runtimecap.Principal,
	providerCallID string,
	snapshot CapabilitySnapshot,
	capability CapabilityDescriptor,
	capabilityOperation string,
	invocationID string,
	artifactRef string,
	operation Operation,
) Request {
	request := tool.baseRequest(operation, tool.authority.GrantRevision)
	deadline := time.Now().Add(codingRemoteOperationTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	request.DeadlineUnixMS = deadline.UnixMilli()
	request.Principal = &principal
	request.CallID = trustedCallID(principal, providerCallID)
	request.DiscoveryRevision = snapshot.DiscoveryRevision
	request.Capability = capability.Alias
	request.CapabilityRevision = capability.Revision
	request.CapabilityOperation = capabilityOperation
	request.InvocationID = invocationID
	request.ArtifactRef = artifactRef
	return request
}

func (tool *CodingRemoteCapabilityTool) importBrowserArtifact(
	ctx context.Context,
	capability string,
	operation string,
	invocationID string,
	artifactRef string,
) *toolshared.ToolResult {
	return tool.executeArtifact(ctx, "artifact_fetch", map[string]any{
		"action": "artifact_fetch", "capability": capability, "operation": operation,
		"invocation_id": invocationID, "artifact_ref": artifactRef,
	})
}

func (tool *CodingRemoteCapabilityTool) fetchArtifact(
	ctx context.Context,
	store CodingRemoteArtifactStore,
	request Request,
	described ArtifactResult,
	link codingRemoteInvocationLink,
) (string, error) {
	temporary, err := os.CreateTemp("", "mintclaw-remote-artifact-*")
	if err != nil {
		return "", err
	}
	path := temporary.Name()
	defer func() { _ = os.Remove(path) }()
	digest := sha256.New()
	writer := io.MultiWriter(temporary, digest)
	for offset := int64(0); offset < described.Size; {
		chunkRequest := request
		chunkRequest.RequestID = "request_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		chunkRequest.Operation = OperationArtifactFetch
		chunkRequest.Offset = offset
		chunkRequest.LimitBytes = int(min(
			int64(codingremote.MaxArtifactChunkBytes),
			described.Size-offset,
		))
		chunk, fetchErr := tool.client.Artifact(ctx, chunkRequest)
		if fetchErr != nil || !matchingArtifactResult(chunk, chunkRequest, link) || chunk.DataBase64 == "" ||
			chunk.Size != described.Size || chunk.SHA256 != described.SHA256 {
			_ = temporary.Close()
			return "", errors.New("remote artifact chunk is unavailable")
		}
		data, decodeErr := base64.StdEncoding.DecodeString(chunk.DataBase64)
		if decodeErr != nil || chunk.NextOffset != offset+int64(len(data)) {
			_ = temporary.Close()
			return "", errors.New("remote artifact chunk is malformed")
		}
		if _, writeErr := writer.Write(data); writeErr != nil {
			_ = temporary.Close()
			return "", writeErr
		}
		offset = chunk.NextOffset
		if chunk.EOF != (offset == described.Size) {
			_ = temporary.Close()
			return "", errors.New("remote artifact chunk ended unexpectedly")
		}
	}
	if err = temporary.Sync(); err != nil {
		_ = temporary.Close()
		return "", err
	}
	if err = temporary.Close(); err != nil {
		return "", err
	}
	if hex.EncodeToString(digest.Sum(nil)) != described.SHA256 {
		return "", errors.New("remote artifact digest mismatch")
	}
	return store.ImportRemoteArtifact(ctx, path, CodingRemoteArtifactMetadata{
		Filename: described.Name, ContentType: described.ContentType,
		Size: described.Size, SHA256: described.SHA256,
	})
}

func matchingArtifactResult(
	result ArtifactResult,
	request Request,
	link codingRemoteInvocationLink,
) bool {
	return result.Validate() == nil && result.Grant == request.Grant &&
		result.GrantRevision == request.GrantRevision &&
		result.DiscoveryRevision == request.DiscoveryRevision && result.Capability == request.Capability &&
		result.CapabilityRevision == request.CapabilityRevision && result.InvocationID == request.InvocationID &&
		result.Target == link.Target && result.ArtifactRef == request.ArtifactRef
}

func artifactToolResult(action string, artifact ArtifactResult, attachmentRef string) *toolshared.ToolResult {
	view := map[string]any{
		"action": action, "capability": artifact.Capability, "invocation_id": artifact.InvocationID,
		"artifact_ref": artifact.ArtifactRef, "name": artifact.Name, "state": artifact.State,
		"size": artifact.Size, "sha256": artifact.SHA256, "content_type": artifact.ContentType,
	}
	if attachmentRef != "" {
		view["attachment_ref"] = attachmentRef
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		return remoteToolError("RESULT_UNAVAILABLE", "remote artifact result is unavailable")
	}
	return toolshared.NewToolResult(string(encoded))
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
	recoverBrowserReceipt := false

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
			Operation: operationAlias, Target: expectedTarget, Risk: expectedRisk, Kind: capability.Kind,
		}
	case "status", "cancel":
		if len(args) != 3 {
			return remoteToolError("INVALID_ARGUMENTS", action+" requires capability and invocation_id")
		}
		invocationID = strings.TrimSpace(stringToolArgument(args, "invocation_id"))
		link, linked := tool.invocationLink(invocationID)
		if !linked {
			if action != "status" {
				return remoteToolError(
					"INVOCATION_UNAVAILABLE",
					"invocation was not returned by this remote capability",
				)
			}
			recoverBrowserReceipt = true
			capabilityRevision = codingremote.BrowserReceiptRecoveryRevision
			operationAlias = codingremote.BrowserReceiptRecoveryOperation
			break
		}
		if link.Capability != capabilityAlias {
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
	if recoverBrowserReceipt && request.DiscoveryRevision == "" {
		request.DiscoveryRevision = codingremote.BrowserReceiptRecoveryRevision
	}
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
	if recoverBrowserReceipt {
		if result.Validate() != nil || result.Grant != request.Grant ||
			result.GrantRevision != request.GrantRevision ||
			result.DiscoveryRevision != request.DiscoveryRevision ||
			result.Capability != request.Capability ||
			result.InvocationID != request.InvocationID || (expectedTarget != "" && result.Target != expectedTarget) ||
			result.Operation == "" || result.Operation == codingremote.BrowserReceiptRecoveryOperation {
			return remoteToolError("RESULT_UNAVAILABLE", "remote browser invocation receipt is unavailable")
		}
		recoveredLink := codingRemoteInvocationLink{
			Capability: result.Capability, CapabilityRevision: result.CapabilityRevision,
			Operation: result.Operation, Target: result.Target, Risk: result.Risk,
			Kind: codingremote.CapabilityBrowserProfile,
		}
		if !tool.retainInvocation(result.InvocationID, recoveredLink) {
			return remoteToolError("RESULT_UNAVAILABLE", "remote capability invocation identity conflicts")
		}
		return capabilityToolResult(action, result)
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

// DurableArguments preserves the remote capability envelope while applying
// the same protected-input projection as browser_act to nested browser input.
// A remote wrapper must not weaken the browser tool's fill/dialog boundary.
func (tool *CodingRemoteCapabilityTool) DurableArguments(
	args map[string]any,
) (map[string]any, error) {
	encoded, err := json.Marshal(args)
	if err != nil {
		return nil, errors.New("remote capability arguments are unavailable")
	}
	projected := make(map[string]any, len(args))
	if err = json.Unmarshal(encoded, &projected); err != nil {
		return nil, errors.New("remote capability arguments are unavailable")
	}
	if tool.remoteBrowserOperation(projected) != "browser_act" || projected["action"] != "invoke" {
		return projected, nil
	}
	input, ok := projected["input"].(map[string]any)
	if !ok {
		projected["input"] = browserInvalidActionDurableProjection()
		return projected, nil
	}
	durable, durableOK := remoteBrowserDurableInput(input)
	if !durableOK {
		projected["input"] = browserInvalidActionDurableProjection()
		return projected, nil
	}
	projected["input"] = durable
	return projected, nil
}

func remoteBrowserDurableInput(input map[string]any) (map[string]any, bool) {
	durable, err := (&BrowserActTool{}).DurableArguments(input)
	return durable, err == nil
}

func (tool *CodingRemoteCapabilityTool) ProtectedDurableArguments(args map[string]any) bool {
	if tool.remoteBrowserOperation(args) != "browser_act" || args["action"] != "invoke" {
		return false
	}
	input, ok := args["input"].(map[string]any)
	return !ok || (&BrowserActTool{}).ProtectedDurableArguments(input)
}

// Browser page/context/action results retain their existing live-only
// boundary even when transported through the coding remote facade. Session
// lifecycle receipts remain durable because they contain only opaque broker
// references and bounded state.
func (tool *CodingRemoteCapabilityTool) ProtectedDurableResult(args map[string]any) bool {
	switch tool.remoteBrowserOperation(args) {
	case "browser_context_list", "browser_context_open", "browser_context_select", "browser_context_close",
		"browser_observe", "browser_diagnostics", "browser_capture", "browser_act",
		codingremote.BrowserReceiptRecoveryOperation:
		return true
	default:
		return false
	}
}

func (tool *CodingRemoteCapabilityTool) remoteBrowserOperation(args map[string]any) string {
	if tool == nil {
		return ""
	}
	action, _ := args["action"].(string)
	if action == "invoke" {
		capabilityAlias, _ := args["capability"].(string)
		operation, _ := args["operation"].(string)
		capability, found := snapshotCapability(tool.currentSnapshot(), capabilityAlias)
		if found && capability.Kind == codingremote.CapabilityBrowserProfile {
			return operation
		}
		return ""
	}
	if action != "status" && action != "cancel" {
		return ""
	}
	invocationID, _ := args["invocation_id"].(string)
	link, found := tool.invocationLink(invocationID)
	if found {
		if link.Kind == codingremote.CapabilityBrowserProfile {
			return link.Operation
		}
		return ""
	}
	if action == "status" {
		return codingremote.BrowserReceiptRecoveryOperation
	}
	return ""
}

func safeBrokerMessage(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 1024 {
		return false
	}
	return !strings.ContainsFunc(value, func(character rune) bool {
		return character < 0x20 || character == 0x7f
	})
}
