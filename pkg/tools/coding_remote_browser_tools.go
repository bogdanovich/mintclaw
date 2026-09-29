package tools

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"

	codingremote "github.com/bogdanovich/mintclaw/pkg/coding/remote"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	"github.com/bogdanovich/mintclaw/pkg/tools/loopguard"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

const codingBrowserReceiptField = "broker_receipt"

var codingBrowserSessionOperations = map[string]string{
	"open":   "browser_open",
	"status": "browser_status",
	"close":  "browser_close",
}

var codingBrowserContextOperations = map[string]string{
	"list":   "browser_context_list",
	"open":   "browser_context_open",
	"select": "browser_context_select",
	"close":  "browser_context_close",
}

var codingBrowserCoreWorkflowOperations = []string{
	"browser_open",
	"browser_status",
	"browser_close",
	"browser_observe",
	"browser_act",
}

type codingBrowserToolRuntime struct {
	client BrowserCapabilityClient
	mu     sync.RWMutex
	routes map[string]string
}

type codingBrowserImportedArtifact struct {
	Ref            string
	Name           string
	ContentType    string
	Size           int64
	SHA256         string
	AttachmentRef  string
	ImportState    string
	ReceiptInvalid bool
}

type (
	codingBrowserTargetsTool     struct{ runtime *codingBrowserToolRuntime }
	codingBrowserSessionTool     struct{ runtime *codingBrowserToolRuntime }
	codingBrowserContextsTool    struct{ runtime *codingBrowserToolRuntime }
	codingBrowserObserveTool     struct{ runtime *codingBrowserToolRuntime }
	codingBrowserDiagnosticsTool struct{ runtime *codingBrowserToolRuntime }
	codingBrowserCaptureTool     struct{ runtime *codingBrowserToolRuntime }
	codingBrowserActTool         struct{ runtime *codingBrowserToolRuntime }
)

// NewCodingRemoteBrowserTools projects the exact browser operations admitted
// by the authenticated coding grant under the same first-party names used by
// gateway agents. The returned tools remain transport adapters: persistent
// profiles, sessions, policy, and invocation receipts stay gateway-owned.
func NewCodingRemoteBrowserTools(client BrowserCapabilityClient) ([]toolshared.Tool, error) {
	if codingBrowserCapabilityClientNil(client) {
		return nil, errors.New("coding browser capability client is required")
	}
	runtime := &codingBrowserToolRuntime{client: client, routes: make(map[string]string)}
	tools := []toolshared.Tool{&codingBrowserTargetsTool{runtime: runtime}}
	if runtime.supportsAny(mapValues(codingBrowserSessionOperations)...) {
		tools = append(tools, &codingBrowserSessionTool{runtime: runtime})
	}
	if runtime.supportsAny(mapValues(codingBrowserContextOperations)...) {
		tools = append(tools, &codingBrowserContextsTool{runtime: runtime})
	}
	if runtime.supportsAny("browser_observe") {
		tools = append(tools, &codingBrowserObserveTool{runtime: runtime})
	}
	if runtime.supportsAny("browser_diagnostics") {
		tools = append(tools, &codingBrowserDiagnosticsTool{runtime: runtime})
	}
	if runtime.supportsAny("browser_capture") {
		tools = append(tools, &codingBrowserCaptureTool{runtime: runtime})
	}
	if runtime.supportsAny("browser_act") {
		tools = append(tools, &codingBrowserActTool{runtime: runtime})
	}
	return tools, nil
}

func (*codingBrowserTargetsTool) Name() string { return "browser_targets" }

func (*codingBrowserTargetsTool) Description() string {
	return "List the broker-owned browser profiles explicitly granted to this coding thread without opening a " +
		"session. Each target is a safe capability alias; never infer a target from list order."
}

func (*codingBrowserTargetsTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object", "properties": map[string]any{}, "additionalProperties": false,
	}
}

func (*codingBrowserTargetsTool) ToolLoopSemantics() loopguard.Semantics {
	return loopguard.SemanticsReadOnlyIdempotent
}

func (tool *codingBrowserTargetsTool) Execute(context.Context, map[string]any) *toolshared.ToolResult {
	if tool == nil || tool.runtime == nil || tool.runtime.client == nil {
		return codingBrowserError("BROKER_UNAVAILABLE", "coding browser broker is unavailable")
	}
	capabilities := tool.runtime.capabilities()
	views := make([]map[string]any, 0, len(capabilities))
	defaultTarget := ""
	availableTargets := 0
	for _, capability := range capabilities {
		operations := make([]string, 0, len(capability.Operations))
		features := map[string]bool{
			"contexts": false, "diagnostics": false, "screenshot": false,
			"download": false, "handoff": false,
		}
		actions := []string{}
		for _, operation := range capability.Operations {
			operations = append(operations, operation.Alias)
			switch {
			case strings.HasPrefix(operation.Alias, "browser_context_"):
				features["contexts"] = true
			case operation.Alias == "browser_diagnostics":
				features["diagnostics"] = true
			case operation.Alias == "browser_capture":
				features["screenshot"] = true
			case operation.Alias == "browser_act":
				actions = codingBrowserActionKinds(operation.InputSchema)
				features["download"] = slices.Contains(actions, "download")
			}
		}
		sort.Strings(operations)
		status := "unavailable"
		if capability.Available {
			status = "ready"
			availableTargets++
			defaultTarget = capability.Alias
		}
		views = append(views, map[string]any{
			"target": capability.Alias, "placement": capability.Target,
			"status": status, "operations": operations, "actions": actions, "features": features,
		})
	}
	if availableTargets != 1 {
		defaultTarget = ""
	}
	result := map[string]any{"targets": views}
	if defaultTarget != "" {
		result["default_target"] = defaultTarget
	}
	if len(capabilities) == 0 {
		result["status"] = "unavailable"
		result["reason"] = "browser capability discovery is unavailable; check the coding remote broker"
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return codingBrowserError("RESULT_UNAVAILABLE", "coding browser targets are unavailable")
	}
	return toolshared.NewToolResult(string(encoded))
}

func (*codingBrowserSessionTool) Name() string { return "browser_session" }

func (tool *codingBrowserSessionTool) RuntimeCapabilities() []runtimecap.CapabilityID {
	if tool == nil || tool.runtime == nil ||
		!tool.runtime.supportsAllFromOneCapability(codingBrowserCoreWorkflowOperations...) {
		return nil
	}
	return []runtimecap.CapabilityID{runtimecap.CapabilityBrowserWorkflow}
}

func (tool *codingBrowserSessionTool) Description() string {
	return "Open, inspect, or close one broker-owned coding browser session. For open, copy target from " +
		"browser_targets or use its default_target. Status and close resolve the existing session through a " +
		"read-only broker check; close is never replayed after an uncertain outcome."
}

func (tool *codingBrowserSessionTool) Parameters() map[string]any {
	operations := tool.runtime.supportedFacadeOperations(codingBrowserSessionOperations)
	targets := make([]string, 0)
	for _, capability := range tool.runtime.capabilitiesForOperation("browser_open", false) {
		targets = append(targets, capability.Alias)
	}
	targetProperty := map[string]any{
		"type": "string", "description": "For open only: exact target alias from browser_targets.",
	}
	if len(targets) > 0 {
		targetProperty["enum"] = targets
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"operation": map[string]any{"type": "string", "enum": operations},
			"target":    targetProperty,
			"interaction_language": map[string]any{
				"type": "string", "maxLength": 64,
				"description": "Optional language tag retained for compatibility; coding browser open never hands off.",
			},
			"browser_session_id": map[string]any{
				"type": "string", "minLength": 1,
				"description": "For status or close: the exact broker-issued session ID.",
			},
		},
		"required": []string{"operation"}, "additionalProperties": false,
	}
}

func (*codingBrowserSessionTool) ToolLoopSemantics() loopguard.Semantics {
	return loopguard.SemanticsMutating
}

func (tool *codingBrowserSessionTool) Execute(
	ctx context.Context,
	args map[string]any,
) *toolshared.ToolResult {
	operation, _ := args["operation"].(string)
	remoteOperation := codingBrowserSessionOperations[operation]
	if remoteOperation == "" || !tool.runtime.supportsAny(remoteOperation) {
		return codingBrowserError("OPERATION_UNAVAILABLE", "coding browser session operation is unavailable")
	}
	if operation == "open" {
		if len(tool.runtime.capabilitiesForOperation(remoteOperation, true)) == 0 {
			return codingBrowserError(
				"BROKER_UNAVAILABLE",
				"configured browser target is unavailable; inspect browser_targets and the coding remote broker",
			)
		}
		capabilityAlias, err := tool.runtime.openCapability(args)
		if err != nil {
			return codingBrowserError("INVALID_ARGUMENTS", err.Error())
		}
		return tool.runtime.invoke(ctx, capabilityAlias, remoteOperation, map[string]any{})
	}
	sessionID := strings.TrimSpace(stringToolArgument(args, "browser_session_id"))
	if sessionID == "" {
		return codingBrowserError("INVALID_ARGUMENTS", "browser_session_id is required")
	}
	capabilityAlias, statusResult, errResult := tool.runtime.resolveSession(ctx, sessionID, remoteOperation)
	if errResult != nil {
		return errResult
	}
	if operation == "status" && statusResult != nil {
		return statusResult
	}
	return tool.runtime.invoke(ctx, capabilityAlias, remoteOperation, map[string]any{
		"browser_session_id": sessionID,
	})
}

func (*codingBrowserContextsTool) Name() string { return "browser_contexts" }

func (*codingBrowserContextsTool) Description() string {
	return "List, open, select, or close tabs and frames for an existing coding browser session. The session is " +
		"resolved through the same broker-owned profile before any mutating context operation is sent."
}

func (tool *codingBrowserContextsTool) Parameters() map[string]any {
	return tool.runtime.facadeSchema(codingBrowserContextOperations)
}

func (*codingBrowserContextsTool) ToolLoopSemantics() loopguard.Semantics {
	return loopguard.SemanticsMutating
}

func (tool *codingBrowserContextsTool) Execute(
	ctx context.Context,
	args map[string]any,
) *toolshared.ToolResult {
	operation, _ := args["operation"].(string)
	remoteOperation := codingBrowserContextOperations[operation]
	return tool.runtime.executeSessionOperation(ctx, remoteOperation, args)
}

func (*codingBrowserObserveTool) Name() string { return "browser_observe" }

func (*codingBrowserObserveTool) Description() string {
	return "Observe the current page through the broker-owned session. Use the returned fresh tab, snapshot, and " +
		"context authority for the next browser action. Screenshots are added separately in C4c."
}

func (tool *codingBrowserObserveTool) Parameters() map[string]any {
	return tool.runtime.operationSchema("browser_observe")
}

func (*codingBrowserObserveTool) ToolLoopSemantics() loopguard.Semantics {
	return loopguard.SemanticsReadOnlyIdempotent
}

func (tool *codingBrowserObserveTool) Execute(
	ctx context.Context,
	args map[string]any,
) *toolshared.ToolResult {
	return tool.runtime.executeSessionOperation(ctx, "browser_observe", args)
}

func (*codingBrowserDiagnosticsTool) Name() string { return "browser_diagnostics" }

func (*codingBrowserDiagnosticsTool) Description() string {
	return "Inspect bounded browser diagnostics for an existing coding browser session through the authenticated broker."
}

func (tool *codingBrowserDiagnosticsTool) Parameters() map[string]any {
	return tool.runtime.operationSchema("browser_diagnostics")
}

func (*codingBrowserDiagnosticsTool) ToolLoopSemantics() loopguard.Semantics {
	return loopguard.SemanticsReadOnlyIdempotent
}

func (tool *codingBrowserDiagnosticsTool) Execute(
	ctx context.Context,
	args map[string]any,
) *toolshared.ToolResult {
	return tool.runtime.executeSessionOperation(ctx, "browser_diagnostics", args)
}

func (*codingBrowserCaptureTool) Name() string { return "browser_capture" }

func (*codingBrowserCaptureTool) Description() string {
	return "Capture one retained PNG from an exact fresh coding-browser observation. The authenticated broker " +
		"keeps the profile and source artifact; verified bytes are imported into this coding thread as an attachment."
}

func (tool *codingBrowserCaptureTool) Parameters() map[string]any {
	return tool.runtime.operationSchema("browser_capture")
}

func (*codingBrowserCaptureTool) ToolLoopSemantics() loopguard.Semantics {
	return loopguard.SemanticsMutating
}

func (*codingBrowserCaptureTool) DurableArguments(args map[string]any) (map[string]any, error) {
	return cloneBrowserToolArguments(args)
}

func (tool *codingBrowserCaptureTool) Execute(
	ctx context.Context,
	args map[string]any,
) *toolshared.ToolResult {
	return tool.runtime.executeSessionOperation(ctx, "browser_capture", args)
}

func (*codingBrowserActTool) Name() string { return "browser_act" }

func (*codingBrowserActTool) Description() string {
	return "Execute exactly one browser action using authority copied from a fresh browser_observe result. The " +
		"gateway broker retains profile policy and no-replay ownership; an unknown outcome must be recovered from " +
		"its broker_receipt and must never be repeated blindly."
}

func (tool *codingBrowserActTool) Parameters() map[string]any {
	return tool.runtime.operationSchema("browser_act")
}

func (*codingBrowserActTool) ToolLoopSemantics() loopguard.Semantics {
	return loopguard.SemanticsMutating
}

func (*codingBrowserActTool) DurableArguments(args map[string]any) (map[string]any, error) {
	return (&BrowserActTool{}).DurableArguments(args)
}

func (*codingBrowserActTool) CanonicalArguments(args map[string]any) (map[string]any, error) {
	return (&BrowserActTool{}).CanonicalArguments(args)
}

func (*codingBrowserActTool) ProtectedDurableArguments(args map[string]any) bool {
	return (&BrowserActTool{}).ProtectedDurableArguments(args)
}

func (tool *codingBrowserActTool) Execute(
	ctx context.Context,
	args map[string]any,
) *toolshared.ToolResult {
	return tool.runtime.executeSessionOperation(ctx, "browser_act", args)
}

func (runtime *codingBrowserToolRuntime) capabilities() []CodingBrowserCapability {
	if runtime == nil || runtime.client == nil {
		return nil
	}
	capabilities := runtime.client.BrowserCapabilities()
	sort.Slice(capabilities, func(left, right int) bool {
		return capabilities[left].Alias < capabilities[right].Alias
	})
	return capabilities
}

func (runtime *codingBrowserToolRuntime) supportsAny(operations ...string) bool {
	for _, capability := range runtime.capabilities() {
		for _, operation := range capability.Operations {
			if slices.Contains(operations, operation.Alias) {
				return true
			}
		}
	}
	return false
}

func (runtime *codingBrowserToolRuntime) supportsAllFromOneCapability(operations ...string) bool {
	if len(operations) == 0 {
		return false
	}
	for _, capability := range runtime.capabilities() {
		if slices.ContainsFunc(operations, func(operation string) bool {
			return !codingBrowserCapabilitySupports(capability, operation)
		}) {
			continue
		}
		return true
	}
	return false
}

func (runtime *codingBrowserToolRuntime) supportedFacadeOperations(operations map[string]string) []string {
	result := make([]string, 0, len(operations))
	for facade, remote := range operations {
		if runtime.supportsAny(remote) {
			result = append(result, facade)
		}
	}
	sort.Strings(result)
	return result
}

func (runtime *codingBrowserToolRuntime) openCapability(args map[string]any) (string, error) {
	target := strings.TrimSpace(stringToolArgument(args, "target"))
	candidates := runtime.capabilitiesForOperation("browser_open", true)
	if target == "" && len(candidates) == 1 {
		return candidates[0].Alias, nil
	}
	for _, candidate := range candidates {
		if candidate.Alias == target {
			return candidate.Alias, nil
		}
	}
	if target == "" {
		return "", errors.New("target is required when more than one browser target is available")
	}
	return "", fmt.Errorf("browser target %q is unavailable", target)
}

func (runtime *codingBrowserToolRuntime) executeSessionOperation(
	ctx context.Context,
	remoteOperation string,
	args map[string]any,
) *toolshared.ToolResult {
	if remoteOperation == "" || !runtime.supportsAny(remoteOperation) {
		return codingBrowserError("OPERATION_UNAVAILABLE", "coding browser operation is unavailable")
	}
	sessionID := strings.TrimSpace(stringToolArgument(args, "browser_session_id"))
	if sessionID == "" {
		return codingBrowserError("INVALID_ARGUMENTS", "browser_session_id is required")
	}
	capabilityAlias, _, errResult := runtime.resolveSession(ctx, sessionID, remoteOperation)
	if errResult != nil {
		return errResult
	}
	input, err := cloneBrowserToolArguments(args)
	if err != nil {
		return codingBrowserError("INVALID_ARGUMENTS", "coding browser arguments are invalid")
	}
	delete(input, "operation")
	return runtime.invoke(ctx, capabilityAlias, remoteOperation, input)
}

func (runtime *codingBrowserToolRuntime) resolveSession(
	ctx context.Context,
	sessionID string,
	operation string,
) (string, *toolshared.ToolResult, *toolshared.ToolResult) {
	candidates := runtime.capabilitiesForOperation(operation, true)
	if len(candidates) == 0 {
		return "", nil, codingBrowserError(
			"BROKER_UNAVAILABLE",
			"configured browser target is unavailable; inspect browser_targets and the coding remote broker",
		)
	}
	statusCandidates := make([]CodingBrowserCapability, 0, len(candidates))
	for _, candidate := range candidates {
		if codingBrowserCapabilitySupports(candidate, "browser_status") {
			statusCandidates = append(statusCandidates, candidate)
		}
	}
	if retained := runtime.retainedRoute(sessionID); retained != "" {
		sort.SliceStable(statusCandidates, func(left, right int) bool {
			return statusCandidates[left].Alias == retained && statusCandidates[right].Alias != retained
		})
	}
	if len(statusCandidates) == 0 {
		if len(candidates) == 1 {
			return candidates[0].Alias, nil, nil
		}
		return "", nil, codingBrowserError(
			"SESSION_ROUTE_UNAVAILABLE",
			"browser session profile cannot be resolved safely; check browser_targets",
		)
	}
	for _, candidate := range statusCandidates {
		status := runtime.client.InvokeBrowser(ctx, candidate.Alias, "browser_status", map[string]any{
			"browser_session_id": sessionID,
		})
		if status == nil {
			return "", nil, codingBrowserError("BROKER_UNAVAILABLE", "coding browser broker returned no result")
		}
		if !status.IsError {
			runtime.retainRoute(sessionID, candidate.Alias)
			return candidate.Alias, runtime.project(status), nil
		}
		if codingBrowserSessionNotFound(status) {
			runtime.forgetRoute(sessionID, candidate.Alias)
			continue
		}
		if codingBrowserCapabilityEnvelope(status) {
			return "", nil, runtime.project(status)
		}
		return "", nil, status
	}
	return "", nil, codingBrowserError("SESSION_NOT_FOUND", "browser session is unavailable for this coding thread")
}

func (runtime *codingBrowserToolRuntime) invoke(
	ctx context.Context,
	capabilityAlias string,
	operation string,
	input map[string]any,
) *toolshared.ToolResult {
	result := runtime.client.InvokeBrowser(ctx, capabilityAlias, operation, input)
	if result == nil {
		return codingBrowserError("BROKER_UNAVAILABLE", "coding browser broker returned no result")
	}
	if result.IsError && !codingBrowserCapabilityEnvelope(result) {
		return result
	}
	artifact, artifactErr := runtime.importResultArtifact(ctx, result)
	if artifactErr != nil {
		return codingBrowserError("RESULT_UNAVAILABLE", "coding browser artifact receipt is unavailable")
	}
	projected := runtime.projectWithArtifact(result, artifact)
	if projected.IsError {
		return projected
	}
	switch operation {
	case "browser_open":
		if sessionID := codingBrowserResultSessionID(projected.ForLLM); sessionID != "" {
			runtime.retainRoute(sessionID, capabilityAlias)
		}
	case "browser_close":
		runtime.forgetRoute(strings.TrimSpace(stringToolArgument(input, "browser_session_id")), capabilityAlias)
	}
	return projected
}

func (*codingBrowserToolRuntime) project(result *toolshared.ToolResult) *toolshared.ToolResult {
	return projectCodingBrowserResult(result, nil)
}

func (*codingBrowserToolRuntime) projectWithArtifact(
	result *toolshared.ToolResult,
	artifact *codingBrowserImportedArtifact,
) *toolshared.ToolResult {
	return projectCodingBrowserResult(result, artifact)
}

func projectCodingBrowserResult(
	result *toolshared.ToolResult,
	artifact *codingBrowserImportedArtifact,
) *toolshared.ToolResult {
	var envelope codingremote.CapabilityResult
	if result == nil || json.Unmarshal([]byte(result.ContentForLLM()), &envelope) != nil ||
		envelope.Validate() != nil {
		return codingBrowserError("RESULT_UNAVAILABLE", "coding browser result is unavailable")
	}
	receipt := map[string]any{
		"capability": envelope.Capability, "invocation_id": envelope.InvocationID,
		"operation": envelope.Operation, "state": envelope.State,
	}
	if envelope.ErrorCode != "" {
		receipt["error_code"] = envelope.ErrorCode
	}
	if envelope.RecoveryAction != "" {
		receipt["recovery_action"] = envelope.RecoveryAction
	}
	if artifact != nil {
		artifactReceipt := map[string]any{"import_state": artifact.ImportState}
		if !artifact.ReceiptInvalid {
			artifactReceipt["artifact_ref"] = artifact.Ref
			artifactReceipt["name"] = artifact.Name
			artifactReceipt["content_type"] = artifact.ContentType
			artifactReceipt["size"] = artifact.Size
			artifactReceipt["sha256"] = artifact.SHA256
		}
		if artifact.AttachmentRef != "" {
			artifactReceipt["attachment_ref"] = artifact.AttachmentRef
		} else if artifact.ReceiptInvalid {
			artifactReceipt["recovery_action"] = "The browser operation already completed, but its artifact metadata is unavailable. Do not replay the browser operation."
		} else {
			artifactReceipt["recovery_action"] = "Use remote_capability artifact_fetch with this receipt; do not replay the browser operation."
		}
		receipt["artifact"] = artifactReceipt
	}
	safe, err := json.Marshal(map[string]any{codingBrowserReceiptField: receipt})
	if err != nil {
		return codingBrowserError("RESULT_UNAVAILABLE", "coding browser result is unavailable")
	}
	view := toolshared.NewToolResult(string(safe))
	view.IsError = result.IsError
	if artifact != nil && artifact.ReceiptInvalid {
		view.Observation = result.Observation
		view.WriteAudit = append([]toolshared.WriteAuditEntry(nil), result.WriteAudit...)
		return view
	}
	if len(envelope.Result) > 0 {
		var payload any
		if json.Unmarshal(envelope.Result, &payload) != nil {
			return codingBrowserError("RESULT_UNAVAILABLE", "coding browser result is unavailable")
		}
		projected, object := payload.(map[string]any)
		if !object {
			projected = map[string]any{"result": payload}
		}
		if _, collision := projected[codingBrowserReceiptField]; collision {
			return codingBrowserError("RESULT_UNAVAILABLE", "coding browser result conflicts with broker receipt")
		}
		projected[codingBrowserReceiptField] = receipt
		encoded, marshalErr := json.Marshal(projected)
		if marshalErr != nil {
			return codingBrowserError("RESULT_UNAVAILABLE", "coding browser result is unavailable")
		}
		if codingBrowserProtectedOperation(envelope.Operation) {
			view.ContextText = string(encoded)
		} else {
			view.ForLLM = string(encoded)
		}
	} else if envelope.State == "succeeded" {
		return codingBrowserError("RESULT_UNAVAILABLE", "coding browser result is unavailable")
	}
	view.Observation = result.Observation
	view.WriteAudit = append([]toolshared.WriteAuditEntry(nil), result.WriteAudit...)
	if artifact != nil && artifact.AttachmentRef != "" && strings.HasPrefix(artifact.ContentType, "image/") {
		view.ContextMedia = []string{artifact.AttachmentRef}
	}
	return view
}

func codingBrowserProtectedOperation(operation string) bool {
	return strings.HasPrefix(operation, "browser_context_") || operation == "browser_observe" ||
		operation == "browser_diagnostics" || operation == "browser_capture" || operation == "browser_act"
}

func (runtime *codingBrowserToolRuntime) importResultArtifact(
	ctx context.Context,
	result *toolshared.ToolResult,
) (*codingBrowserImportedArtifact, error) {
	if result == nil {
		return nil, nil
	}
	var envelope codingremote.CapabilityResult
	if err := json.Unmarshal([]byte(result.ContentForLLM()), &envelope); err != nil {
		return nil, errors.New("browser capability result is malformed")
	}
	if err := envelope.Validate(); err != nil {
		return nil, errors.New("browser capability result is invalid")
	}
	if envelope.State != "succeeded" {
		return nil, nil
	}
	artifact, valid := codingBrowserArtifactFromEnvelope(envelope)
	if !valid {
		return &codingBrowserImportedArtifact{ImportState: "failed", ReceiptInvalid: true}, nil
	}
	if artifact == nil {
		return nil, nil
	}
	imported := runtime.client.ImportBrowserArtifact(
		ctx,
		envelope.Capability,
		envelope.Operation,
		envelope.InvocationID,
		artifact.Ref,
	)
	artifact.ImportState = "failed"
	if imported == nil || imported.IsError {
		return artifact, nil
	}
	attachmentRef := codingBrowserImportedAttachmentRef(imported)
	if attachmentRef == "" {
		return artifact, nil
	}
	artifact.AttachmentRef = attachmentRef
	artifact.ImportState = "imported"
	return artifact, nil
}

func codingBrowserImportedAttachmentRef(result *toolshared.ToolResult) string {
	var receipt struct {
		AttachmentRef string `json:"attachment_ref"`
	}
	if result == nil || json.Unmarshal([]byte(result.ContentForLLM()), &receipt) != nil ||
		!strings.HasPrefix(receipt.AttachmentRef, "media://") {
		return ""
	}
	return receipt.AttachmentRef
}

func codingBrowserArtifactFromEnvelope(
	envelope codingremote.CapabilityResult,
) (*codingBrowserImportedArtifact, bool) {
	if envelope.Operation != "browser_capture" && envelope.Operation != "browser_act" {
		return nil, true
	}
	var payload struct {
		Artifact *struct {
			Ref         string `json:"ref"`
			Kind        string `json:"kind"`
			ContentType string `json:"content_type"`
			Filename    string `json:"filename"`
			Size        int64  `json:"size"`
			SHA256      string `json:"sha256"`
		} `json:"artifact"`
		ArtifactState string `json:"artifact_state"`
	}
	if json.Unmarshal(envelope.Result, &payload) != nil {
		return nil, false
	}
	if payload.Artifact == nil {
		if envelope.Operation == "browser_capture" || payload.ArtifactState == "committed" {
			return nil, false
		}
		return nil, true
	}
	wantKind := "screenshot"
	if envelope.Operation == "browser_act" {
		wantKind = "download"
		if payload.ArtifactState != "committed" {
			return nil, false
		}
	}
	artifact := payload.Artifact
	if artifact.Kind != wantKind || !validCodingBrowserArtifactRef(artifact.Ref) ||
		!validCodingBrowserArtifactText(artifact.Filename, 255, true) ||
		strings.ContainsAny(artifact.Filename, `/\\`) ||
		!validCodingBrowserArtifactText(artifact.ContentType, 127, true) ||
		artifact.Size < 1 || artifact.Size > codingremote.MaxFetchedArtifactBytes ||
		len(artifact.SHA256) != 64 {
		return nil, false
	}
	if _, err := hex.DecodeString(artifact.SHA256); err != nil {
		return nil, false
	}
	return &codingBrowserImportedArtifact{
		Ref: artifact.Ref, Name: artifact.Filename, ContentType: artifact.ContentType,
		Size: artifact.Size, SHA256: artifact.SHA256,
	}, true
}

func validCodingBrowserArtifactRef(ref string) bool {
	const prefix = "transfer-artifact://"
	identifier := strings.TrimPrefix(ref, prefix)
	if identifier == ref || identifier == "" || len(identifier) > 128 {
		return false
	}
	for _, character := range identifier {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validCodingBrowserArtifactText(value string, maximum int, required bool) bool {
	if len(value) > maximum || required && value == "" {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func codingBrowserCapabilityEnvelope(result *toolshared.ToolResult) bool {
	if result == nil {
		return false
	}
	var envelope codingremote.CapabilityResult
	return json.Unmarshal([]byte(result.ContentForLLM()), &envelope) == nil && envelope.Validate() == nil
}

// Session routes are advisory process-local accelerators, never authority.
// Every use is revalidated through browser_status, and a resumed process can
// reconstruct the same route by safely probing the exact admitted profiles.
func (runtime *codingBrowserToolRuntime) retainRoute(sessionID string, capabilityAlias string) {
	if runtime == nil || sessionID == "" || capabilityAlias == "" {
		return
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.routes == nil {
		runtime.routes = make(map[string]string)
	}
	runtime.routes[sessionID] = capabilityAlias
}

func (runtime *codingBrowserToolRuntime) retainedRoute(sessionID string) string {
	if runtime == nil || sessionID == "" {
		return ""
	}
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	return runtime.routes[sessionID]
}

func (runtime *codingBrowserToolRuntime) forgetRoute(sessionID string, capabilityAlias string) {
	if runtime == nil || sessionID == "" {
		return
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if retained := runtime.routes[sessionID]; capabilityAlias == "" || retained == capabilityAlias {
		delete(runtime.routes, sessionID)
	}
}

func codingBrowserResultSessionID(content string) string {
	var payload struct {
		SessionID string `json:"browser_session_id"`
	}
	if json.Unmarshal([]byte(content), &payload) != nil {
		return ""
	}
	return strings.TrimSpace(payload.SessionID)
}

func (runtime *codingBrowserToolRuntime) operationSchema(operation string) map[string]any {
	schemas := make([]map[string]any, 0)
	encodedSchemas := make(map[string]struct{})
	for _, capability := range runtime.capabilitiesForOperation(operation, false) {
		for _, described := range capability.Operations {
			if described.Alias != operation {
				continue
			}
			var schema map[string]any
			if json.Unmarshal(described.InputSchema, &schema) != nil || len(schema) == 0 {
				continue
			}
			canonical, _ := json.Marshal(schema)
			if _, duplicate := encodedSchemas[string(canonical)]; duplicate {
				continue
			}
			encodedSchemas[string(canonical)] = struct{}{}
			schemas = append(schemas, schema)
		}
	}
	if len(schemas) == 1 {
		return schemas[0]
	}
	if len(schemas) > 1 {
		branches := make([]any, len(schemas))
		for index := range schemas {
			branches[index] = schemas[index]
		}
		return map[string]any{"type": "object", "anyOf": branches}
	}
	return map[string]any{
		"type": "object", "properties": map[string]any{}, "additionalProperties": false,
	}
}

func (runtime *codingBrowserToolRuntime) facadeSchema(operations map[string]string) map[string]any {
	branches := make([]any, 0, len(operations))
	for _, facade := range runtime.supportedFacadeOperations(operations) {
		remote := operations[facade]
		schema := runtime.operationSchema(remote)
		properties, _ := schema["properties"].(map[string]any)
		if properties == nil {
			continue
		}
		properties["operation"] = map[string]any{"type": "string", "enum": []string{facade}}
		required, _ := schema["required"].([]any)
		if !slices.Contains(required, any("operation")) {
			required = append(required, "operation")
		}
		schema["required"] = required
		branches = append(branches, schema)
	}
	if len(branches) == 1 {
		return branches[0].(map[string]any)
	}
	return map[string]any{"type": "object", "oneOf": branches}
}

func (runtime *codingBrowserToolRuntime) capabilitiesForOperation(
	operation string,
	availableOnly bool,
) []CodingBrowserCapability {
	capabilities := make([]CodingBrowserCapability, 0)
	for _, capability := range runtime.capabilities() {
		if availableOnly && !capability.Available || !codingBrowserCapabilitySupports(capability, operation) {
			continue
		}
		capabilities = append(capabilities, capability)
	}
	return capabilities
}

func codingBrowserCapabilitySupports(capability CodingBrowserCapability, operation string) bool {
	return slices.ContainsFunc(capability.Operations, func(candidate CodingBrowserOperation) bool {
		return candidate.Alias == operation
	})
}

func codingBrowserSessionNotFound(result *toolshared.ToolResult) bool {
	if result == nil {
		return false
	}
	var envelope codingremote.CapabilityResult
	if json.Unmarshal([]byte(result.ContentForLLM()), &envelope) != nil || len(envelope.Result) == 0 {
		return false
	}
	var failure map[string]any
	if json.Unmarshal(envelope.Result, &failure) != nil {
		return false
	}
	code, _ := failure["code"].(string)
	return strings.EqualFold(strings.TrimSpace(code), "not_found")
}

func codingBrowserActionKinds(schema json.RawMessage) []string {
	var decoded map[string]any
	if json.Unmarshal(schema, &decoded) != nil {
		return []string{}
	}
	properties, _ := decoded["properties"].(map[string]any)
	actionSchema, _ := properties["action"].(map[string]any)
	if actionSchema == nil {
		return []string{}
	}
	result := make(map[string]struct{})
	var collect func(any)
	collect = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			branchProperties, _ := typed["properties"].(map[string]any)
			kind, _ := branchProperties["kind"].(map[string]any)
			if fixed, ok := kind["const"].(string); ok && fixed != "" {
				result[fixed] = struct{}{}
			}
			if enum, ok := kind["enum"].([]any); ok {
				for _, item := range enum {
					if name, stringOK := item.(string); stringOK && name != "" {
						result[name] = struct{}{}
					}
				}
			}
			for _, branchName := range []string{"oneOf", "anyOf"} {
				collect(typed[branchName])
			}
		case []any:
			for _, child := range typed {
				collect(child)
			}
		}
	}
	collect(actionSchema)
	names := make([]string, 0, len(result))
	for name := range result {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func codingBrowserError(code, message string) *toolshared.ToolResult {
	encoded, _ := json.Marshal(map[string]string{
		"status": "error", "code": code, "message": message,
	})
	return toolshared.ErrorResult(string(encoded)).WithObservation(toolshared.CommandObservation{
		Action: "browser", Source: "remote", Status: "failed",
	})
}

func mapValues(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

func codingBrowserCapabilityClientNil(client BrowserCapabilityClient) bool {
	if client == nil {
		return true
	}
	value := reflect.ValueOf(client)
	return value.Kind() == reflect.Pointer && value.IsNil()
}
