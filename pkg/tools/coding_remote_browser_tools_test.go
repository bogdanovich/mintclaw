package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	codingremote "github.com/bogdanovich/mintclaw/pkg/coding/remote"
	codingscope "github.com/bogdanovich/mintclaw/pkg/coding/scope"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type fakeCodingBrowserCapabilityClient struct {
	capabilities []CodingBrowserCapability
	calls        []fakeCodingBrowserCall
	imports      []fakeCodingBrowserImport
	invoke       func(string, string, map[string]any) *toolshared.ToolResult
	importer     func(string, string, string, string) *toolshared.ToolResult
}

type fakeCodingBrowserCall struct {
	capability string
	operation  string
	input      map[string]any
}

type fakeCodingBrowserImport struct {
	capability  string
	operation   string
	invocation  string
	artifactRef string
}

func (client *fakeCodingBrowserCapabilityClient) Available() bool {
	return slices.ContainsFunc(client.capabilities, func(capability CodingBrowserCapability) bool {
		return capability.Available
	})
}

func (client *fakeCodingBrowserCapabilityClient) BrowserCapabilities() []CodingBrowserCapability {
	return append([]CodingBrowserCapability(nil), client.capabilities...)
}

func (client *fakeCodingBrowserCapabilityClient) InvokeBrowser(
	_ context.Context,
	capability string,
	operation string,
	input map[string]any,
) *toolshared.ToolResult {
	client.calls = append(client.calls, fakeCodingBrowserCall{
		capability: capability, operation: operation, input: input,
	})
	if client.invoke != nil {
		return client.invoke(capability, operation, input)
	}
	return codingBrowserCapabilityResult(capability, operation, "succeeded", json.RawMessage(`{"ok":true}`))
}

func (*fakeCodingBrowserCapabilityClient) BrowserInvocationStatus(
	context.Context,
	string,
	string,
) *toolshared.ToolResult {
	return toolshared.ErrorResult("unused")
}

func (client *fakeCodingBrowserCapabilityClient) ImportBrowserArtifact(
	_ context.Context,
	capability string,
	operation string,
	invocation string,
	artifactRef string,
) *toolshared.ToolResult {
	client.imports = append(client.imports, fakeCodingBrowserImport{
		capability: capability, operation: operation, invocation: invocation, artifactRef: artifactRef,
	})
	if client.importer != nil {
		return client.importer(capability, operation, invocation, artifactRef)
	}
	return toolshared.ErrorResult("unused")
}

func TestCodingRemoteBrowserToolsProjectNativeSurfaceAndReceipt(t *testing.T) {
	client := &fakeCodingBrowserCapabilityClient{capabilities: []CodingBrowserCapability{
		codingBrowserTestCapability("browser-personal", "companion", true),
	}}
	client.invoke = func(capability string, operation string, _ map[string]any) *toolshared.ToolResult {
		if capability != "browser-personal" || operation != "browser_open" {
			t.Fatalf("unexpected invocation %s.%s", capability, operation)
		}
		return codingBrowserCapabilityResult(
			capability,
			operation,
			"succeeded",
			json.RawMessage(`{"browser_session_id":"browser_1","state":"ready"}`),
		)
	}

	projected, err := NewCodingRemoteBrowserTools(client)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(projected))
	for _, tool := range projected {
		names = append(names, tool.Name())
	}
	slices.Sort(names)
	wantNames := []string{
		"browser_act", "browser_capture", "browser_contexts", "browser_diagnostics", "browser_observe",
		"browser_session", "browser_targets",
	}
	if !slices.Equal(names, wantNames) {
		t.Fatalf("coding browser tools = %v, want %v", names, wantNames)
	}
	registry := NewToolRegistry()
	for _, tool := range projected {
		registry.Register(tool)
	}
	for name, arguments := range map[string]map[string]any{
		"browser_session":  {"operation": "open", "target": "browser-personal"},
		"browser_contexts": {"operation": "list", "browser_session_id": "browser_1"},
		"browser_observe":  {"browser_session_id": "browser_1"},
		"browser_act": {
			"browser_session_id": "browser_1", "tab_id": "tab_1",
			"snapshot_id": "snapshot_1", "snapshot_generation": 1,
			"action": map[string]any{"kind": "navigate", "url": "https://example.com"},
		},
	} {
		if err = registry.ValidateArguments(name, arguments); err != nil {
			t.Fatalf("%s schema rejected native arguments: %v", name, err)
		}
	}

	targets := codingBrowserToolByName(t, projected, "browser_targets").Execute(t.Context(), map[string]any{})
	if targets.IsError || !strings.Contains(targets.ContentForLLM(), `"default_target":"browser-personal"`) ||
		!strings.Contains(targets.ContentForLLM(), `"screenshot":true`) ||
		!strings.Contains(targets.ContentForLLM(), `"handoff":false`) {
		t.Fatalf("browser_targets = %s", targets.ContentForLLM())
	}

	session := codingBrowserToolByName(t, projected, "browser_session").Execute(t.Context(), map[string]any{
		"operation": "open", "target": "browser-personal", "interaction_language": "en",
	})
	if session.IsError || !strings.Contains(session.ContentForLLM(), `"browser_session_id":"browser_1"`) ||
		!strings.Contains(session.ContentForLLM(), `"broker_receipt"`) || len(client.calls) != 1 ||
		len(client.calls[0].input) != 0 {
		t.Fatalf("browser_session = %s; calls = %#v", session.ContentForLLM(), client.calls)
	}
}

func TestCodingRemoteBrowserToolsImportVerifiedCaptureIntoLiveAndDurableContext(t *testing.T) {
	client := &fakeCodingBrowserCapabilityClient{capabilities: []CodingBrowserCapability{
		codingBrowserTestCapability("browser", "companion", true),
	}}
	artifactRef := "transfer-artifact://capture_0123456789abcdef"
	client.invoke = func(capability string, operation string, _ map[string]any) *toolshared.ToolResult {
		switch operation {
		case "browser_status":
			return codingBrowserCapabilityResult(
				capability,
				operation,
				"succeeded",
				json.RawMessage(`{"browser_session_id":"browser_1","state":"ready"}`),
			)
		case "browser_capture":
			return codingBrowserCapabilityResult(capability, operation, "succeeded", json.RawMessage(fmt.Sprintf(
				`{"artifact":{"ref":%q,"kind":"screenshot","content_type":"image/png","filename":"browser-screenshot.png","size":128,"sha256":%q,"browser_session_id":"browser_1","tab_id":"tab_1","snapshot_id":"snapshot_1","snapshot_generation":1,"target":"page"}}`,
				artifactRef,
				strings.Repeat("a", 64),
			)))
		default:
			t.Fatalf("unexpected browser operation %q", operation)
			return nil
		}
	}
	client.importer = func(capability, operation, invocation, ref string) *toolshared.ToolResult {
		if capability != "browser" || operation != "browser_capture" || invocation == "" || ref != artifactRef {
			t.Fatalf("artifact import = %q, %q, %q, %q", capability, operation, invocation, ref)
		}
		return toolshared.NewToolResult(
			`{"action":"artifact_fetch","attachment_ref":"media://coding-attachment/capture"}`,
		)
	}
	projected, err := NewCodingRemoteBrowserTools(client)
	if err != nil {
		t.Fatal(err)
	}
	result := codingBrowserToolByName(t, projected, "browser_capture").Execute(t.Context(), map[string]any{
		"browser_session_id": "browser_1", "tab_id": "tab_1", "snapshot_id": "snapshot_1",
		"snapshot_generation": float64(1), "target": "page",
	})
	if result.IsError || len(client.calls) != 2 || len(client.imports) != 1 ||
		!strings.Contains(result.ForLLM, `"attachment_ref":"media://coding-attachment/capture"`) ||
		!strings.Contains(result.ContextText, artifactRef) ||
		!slices.Equal(result.ContextMedia, []string{"media://coding-attachment/capture"}) {
		t.Fatalf("browser_capture = %#v; calls=%#v imports=%#v", result, client.calls, client.imports)
	}
}

func TestCodingRemoteBrowserToolsDoNotCombineCoreWorkflowAcrossAuthorities(t *testing.T) {
	lifecycle := codingBrowserTestCapability("browser-personal", "companion", true)
	lifecycle.Operations = slices.DeleteFunc(lifecycle.Operations, func(operation CodingBrowserOperation) bool {
		return !slices.Contains(
			[]string{"browser_open", "browser_status", "browser_close"},
			operation.Alias,
		)
	})
	actions := codingBrowserTestCapability("browser-work", "companion", true)
	actions.Operations = slices.DeleteFunc(actions.Operations, func(operation CodingBrowserOperation) bool {
		return !slices.Contains([]string{"browser_observe", "browser_act"}, operation.Alias)
	})
	client := &fakeCodingBrowserCapabilityClient{capabilities: []CodingBrowserCapability{lifecycle, actions}}

	projected, err := NewCodingRemoteBrowserTools(client)
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]struct{}, len(projected))
	for _, tool := range projected {
		names[tool.Name()] = struct{}{}
	}
	for _, name := range []string{"browser_targets", "browser_session", "browser_observe", "browser_act"} {
		if _, ok := names[name]; !ok {
			t.Fatalf("coding browser tool %q is unavailable: %v", name, names)
		}
	}
	session := codingBrowserToolByName(t, projected, "browser_session")
	provider, ok := session.(interface {
		RuntimeCapabilities() []runtimecap.CapabilityID
	})
	if !ok || len(provider.RuntimeCapabilities()) != 0 {
		t.Fatalf("split-authority browser workflow was admitted: %#v", session)
	}
}

func TestCodingRemoteBrowserToolsDoNotReplayCaptureWhenImportFails(t *testing.T) {
	client := &fakeCodingBrowserCapabilityClient{capabilities: []CodingBrowserCapability{
		codingBrowserTestCapability("browser", "companion", true),
	}}
	client.invoke = func(capability string, operation string, _ map[string]any) *toolshared.ToolResult {
		if operation == "browser_status" {
			return codingBrowserCapabilityResult(
				capability,
				operation,
				"succeeded",
				json.RawMessage(`{"browser_session_id":"browser_1","state":"ready"}`),
			)
		}
		return codingBrowserCapabilityResult(capability, operation, "succeeded", json.RawMessage(fmt.Sprintf(
			`{"artifact":{"ref":"transfer-artifact://capture_1","kind":"screenshot","content_type":"image/png","filename":"browser-screenshot.png","size":128,"sha256":%q}}`,
			strings.Repeat("a", 64),
		)))
	}
	client.importer = func(string, string, string, string) *toolshared.ToolResult {
		return toolshared.ErrorResult(`{"status":"error","code":"ARTIFACT_FETCH_FAILED"}`)
	}
	projected, err := NewCodingRemoteBrowserTools(client)
	if err != nil {
		t.Fatal(err)
	}
	result := codingBrowserToolByName(t, projected, "browser_capture").Execute(t.Context(), map[string]any{
		"browser_session_id": "browser_1", "tab_id": "tab_1", "snapshot_id": "snapshot_1",
		"snapshot_generation": float64(1), "target": "page",
	})
	if result.IsError || len(client.calls) != 2 || len(client.imports) != 1 || len(result.ContextMedia) != 0 ||
		!strings.Contains(result.ForLLM, `"import_state":"failed"`) ||
		!strings.Contains(result.ForLLM, "do not replay the browser operation") {
		t.Fatalf("capture import failure = %#v; calls=%#v imports=%#v", result, client.calls, client.imports)
	}
}

func TestCodingRemoteBrowserToolsPreserveTerminalReceiptWhenArtifactMetadataIsMalformed(t *testing.T) {
	client := &fakeCodingBrowserCapabilityClient{capabilities: []CodingBrowserCapability{
		codingBrowserTestCapability("browser", "companion", true),
	}}
	client.invoke = func(capability string, operation string, _ map[string]any) *toolshared.ToolResult {
		if operation == "browser_status" {
			return codingBrowserCapabilityResult(
				capability,
				operation,
				"succeeded",
				json.RawMessage(`{"browser_session_id":"browser_1","state":"ready"}`),
			)
		}
		return codingBrowserCapabilityResult(
			capability,
			operation,
			"succeeded",
			json.RawMessage(`{"artifact":{"ref":"file:///private/capture.png","kind":"screenshot"}}`),
		)
	}
	projected, err := NewCodingRemoteBrowserTools(client)
	if err != nil {
		t.Fatal(err)
	}
	result := codingBrowserToolByName(t, projected, "browser_capture").Execute(t.Context(), map[string]any{
		"browser_session_id": "browser_1", "tab_id": "tab_1", "snapshot_id": "snapshot_1",
		"snapshot_generation": float64(1), "target": "page",
	})
	if result.IsError || len(client.calls) != 2 || len(client.imports) != 0 || result.ContextText != "" ||
		!strings.Contains(result.ForLLM, `"state":"succeeded"`) ||
		!strings.Contains(result.ForLLM, `"invocation_id":"remote_capability_browser_browser_capture"`) ||
		!strings.Contains(result.ForLLM, `"import_state":"failed"`) ||
		!strings.Contains(result.ForLLM, "already completed") ||
		!strings.Contains(result.ForLLM, "Do not replay the browser operation") ||
		strings.Contains(result.ForLLM, "file:///private/capture.png") {
		t.Fatalf("malformed capture receipt = %#v; calls=%#v imports=%#v", result, client.calls, client.imports)
	}
}

func TestCodingRemoteBrowserToolsRejectTypedNilClient(t *testing.T) {
	var client *fakeCodingBrowserCapabilityClient
	if _, err := NewCodingRemoteBrowserTools(client); err == nil {
		t.Fatal("typed-nil coding browser client was accepted")
	}
}

func TestCodingRemoteBrowserToolsResolveSessionBeforeMutation(t *testing.T) {
	client := &fakeCodingBrowserCapabilityClient{capabilities: []CodingBrowserCapability{
		codingBrowserTestCapability("browser-personal", "companion", true),
		codingBrowserTestCapability("browser-work", "companion", true),
	}}
	client.invoke = func(capability string, operation string, _ map[string]any) *toolshared.ToolResult {
		switch capability + "." + operation {
		case "browser-personal.browser_status":
			return codingBrowserCapabilityFailure(
				capability,
				operation,
				json.RawMessage(`{"status":"denied","code":"not_found","message":"session unavailable"}`),
			)
		case "browser-work.browser_status":
			return codingBrowserCapabilityResult(
				capability,
				operation,
				"succeeded",
				json.RawMessage(`{"browser_session_id":"browser_1","state":"ready"}`),
			)
		case "browser-work.browser_act":
			return codingBrowserCapabilityResult(
				capability,
				operation,
				"succeeded",
				json.RawMessage(`{"state":"succeeded","observation":{"snapshot_id":"snapshot_2"}}`),
			)
		default:
			t.Fatalf("unexpected invocation %s.%s", capability, operation)
			return nil
		}
	}
	projected, err := NewCodingRemoteBrowserTools(client)
	if err != nil {
		t.Fatal(err)
	}
	actionTool := codingBrowserToolByName(t, projected, "browser_act")
	result := actionTool.Execute(t.Context(), map[string]any{
		"browser_session_id": "browser_1", "tab_id": "tab_1",
		"snapshot_id": "snapshot_1", "snapshot_generation": float64(1),
		"action": map[string]any{"kind": "navigate", "url": "https://example.com"},
	})
	if result.IsError || len(client.calls) != 3 ||
		client.calls[0].capability != "browser-personal" || client.calls[0].operation != "browser_status" ||
		client.calls[1].capability != "browser-work" || client.calls[1].operation != "browser_status" ||
		client.calls[2].capability != "browser-work" || client.calls[2].operation != "browser_act" {
		t.Fatalf("browser_act = %s; calls = %#v", result.ContentForLLM(), client.calls)
	}
	if strings.Contains(result.ForLLM, `"snapshot_id":"snapshot_2"`) ||
		!strings.Contains(result.ForLLM, `"broker_receipt"`) ||
		!strings.Contains(result.ContextText, `"snapshot_id":"snapshot_2"`) {
		t.Fatalf("projected browser_act durable=%s live=%s", result.ForLLM, result.ContextText)
	}
	result = actionTool.Execute(t.Context(), map[string]any{
		"browser_session_id": "browser_1", "tab_id": "tab_1",
		"snapshot_id": "snapshot_2", "snapshot_generation": float64(2),
		"action": map[string]any{"kind": "navigate", "url": "https://example.com/next"},
	})
	if result.IsError || len(client.calls) != 5 ||
		client.calls[3].capability != "browser-work" || client.calls[3].operation != "browser_status" ||
		client.calls[4].capability != "browser-work" || client.calls[4].operation != "browser_act" {
		t.Fatalf("cached browser route = %s; calls = %#v", result.ContentForLLM(), client.calls)
	}

	durable, err := actionTool.(interface {
		DurableArguments(map[string]any) (map[string]any, error)
	}).DurableArguments(map[string]any{
		"browser_session_id": "browser_1", "tab_id": "tab_1",
		"snapshot_id": "snapshot_1", "snapshot_generation": float64(1),
		"action": map[string]any{"kind": "fill", "ref": "field_1", "value": "secret"},
	})
	if err != nil || durable["action"].(map[string]any)["value"] != browserProtectedInputRedaction {
		t.Fatalf("durable browser_act = %#v, %v", durable, err)
	}
}

func TestCodingRemoteBrowserToolsPreserveUnknownOutcomeWithoutReplay(t *testing.T) {
	client := &fakeCodingBrowserCapabilityClient{capabilities: []CodingBrowserCapability{
		codingBrowserTestCapability("browser", "companion", true),
	}}
	client.invoke = func(capability string, operation string, _ map[string]any) *toolshared.ToolResult {
		if operation == "browser_status" {
			return codingBrowserCapabilityResult(
				capability,
				operation,
				"succeeded",
				json.RawMessage(`{"browser_session_id":"browser_1","state":"ready"}`),
			)
		}
		result := codingBrowserCapabilityResult(capability, operation, "unknown", nil)
		if !result.IsError {
			t.Fatal("unknown browser result is not marked as an error")
		}
		return result
	}
	projected, err := NewCodingRemoteBrowserTools(client)
	if err != nil {
		t.Fatal(err)
	}
	result := codingBrowserToolByName(t, projected, "browser_act").Execute(t.Context(), map[string]any{
		"browser_session_id": "browser_1", "tab_id": "tab_1",
		"snapshot_id": "snapshot_1", "snapshot_generation": float64(1),
		"action": map[string]any{"kind": "click", "ref": "button_1"}, "effect": "external_commit",
	})
	if !result.IsError || len(client.calls) != 2 || result.ContextText != "" ||
		!strings.Contains(result.ForLLM, `"state":"unknown"`) ||
		!strings.Contains(result.ForLLM, `"recovery_action"`) {
		t.Fatalf("unknown browser_act = %s; calls = %#v", result.ContentForLLM(), client.calls)
	}
}

func TestCodingRemoteBrowserToolsUseAuthenticatedCapabilityFacade(t *testing.T) {
	threadID := uuid.NewString()
	sessionKey := "coding:" + threadID
	snapshot := codingRemoteBrowserToolTestSnapshot()
	broker := &fakeCodingRemoteBroker{snapshot: snapshot}
	broker.executeFunc = func(request codingremote.Request) (codingremote.CapabilityResult, error) {
		return codingremote.CapabilityResult{
			Grant: request.Grant, GrantRevision: request.GrantRevision,
			DiscoveryRevision: request.DiscoveryRevision,
			Capability:        request.Capability, CapabilityRevision: request.CapabilityRevision,
			Operation: request.CapabilityOperation, InvocationID: request.InvocationID,
			Target: "companion-browser", Risk: codingremote.RiskWrite, State: "succeeded",
			Result: json.RawMessage(`{"browser_session_id":"browser_1","state":"ready"}`),
		}, nil
	}
	remote, err := NewCodingRemoteCapabilityTool(broker, CodingRemoteToolAuthority{
		Grant: "local-development", GrantRevision: "grant-v1",
		ThreadID: threadID, SessionKey: sessionKey,
		ProjectKey: "git_worktree:" + strings.Repeat("b", 64), LocalProfile: codingscope.ProfileMutate,
	}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewCodingRemoteBrowserCapabilityClient(remote, []string{"browser"})
	if err != nil {
		t.Fatal(err)
	}
	projected, err := NewCodingRemoteBrowserTools(client)
	if err != nil {
		t.Fatal(err)
	}
	principal := runtimecap.Principal{
		Runtime: runtimecap.KindCoding, ActorID: "local:operator", AgentID: "main",
		SessionID: sessionKey, ExecutionID: "turn-browser-native",
	}
	runtime := runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindCoding}).BindPrincipal(principal)
	ctx := toolshared.WithRuntimeCapabilities(t.Context(), runtime)
	ctx = toolshared.WithToolCallID(ctx, "provider-browser-open")
	result := codingBrowserToolByName(t, projected, "browser_session").Execute(ctx, map[string]any{
		"operation": "open", "target": "browser",
	})
	if result.IsError || len(broker.executionCalls) != 1 ||
		broker.executionCalls[0].Capability != "browser" ||
		broker.executionCalls[0].CapabilityOperation != "browser_open" ||
		broker.executionCalls[0].Principal == nil ||
		broker.executionCalls[0].Principal.SessionID != sessionKey ||
		!strings.Contains(result.ForLLM, `"browser_session_id":"browser_1"`) {
		t.Fatalf("native browser open = %#v; calls = %#v", result, broker.executionCalls)
	}
}

func codingBrowserTestCapability(alias string, target string, available bool) CodingBrowserCapability {
	actSchema, _ := json.Marshal((&BrowserActTool{}).Parameters())
	sessionSchema := json.RawMessage(
		`{"type":"object","additionalProperties":false,"required":["browser_session_id"],"properties":{"browser_session_id":{"type":"string"}}}`,
	)
	contextSchema := json.RawMessage(
		`{"type":"object","additionalProperties":false,"required":["browser_session_id"],"properties":{"browser_session_id":{"type":"string"}}}`,
	)
	captureSchema, _ := json.Marshal((&BrowserCaptureTool{}).Parameters())
	return CodingBrowserCapability{
		Alias: alias, Revision: alias + "-v1", Target: target, Available: available,
		Operations: []CodingBrowserOperation{
			{
				Alias:       "browser_open",
				Risk:        codingremote.RiskWrite,
				InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`),
			},
			{Alias: "browser_status", Risk: codingremote.RiskRead, InputSchema: sessionSchema},
			{Alias: "browser_close", Risk: codingremote.RiskWrite, InputSchema: sessionSchema},
			{Alias: "browser_context_list", Risk: codingremote.RiskRead, InputSchema: contextSchema},
			{Alias: "browser_observe", Risk: codingremote.RiskRead, InputSchema: sessionSchema},
			{Alias: "browser_diagnostics", Risk: codingremote.RiskRead, InputSchema: sessionSchema},
			{Alias: "browser_capture", Risk: codingremote.RiskRead, InputSchema: captureSchema},
			{Alias: "browser_act", Risk: codingremote.RiskWrite, InputSchema: actSchema},
		},
	}
}

func codingBrowserCapabilityResult(
	capability string,
	operation string,
	state string,
	payload json.RawMessage,
) *toolshared.ToolResult {
	risk := codingremote.RiskWrite
	if operation == "browser_status" || operation == "browser_observe" ||
		operation == "browser_diagnostics" || operation == "browser_capture" ||
		operation == "browser_context_list" {
		risk = codingremote.RiskRead
	}
	result := codingremote.CapabilityResult{
		Grant: "local-development", GrantRevision: "grant-v1", DiscoveryRevision: "discovery-v1",
		Capability: capability, CapabilityRevision: capability + "-v1", Operation: operation,
		InvocationID: "remote_capability_" + strings.ReplaceAll(capability+"_"+operation, "-", "_"),
		Target:       "companion", Risk: risk, State: state, Result: payload,
	}
	if state == "unknown" {
		result.ErrorCode = "INVOCATION_UNCERTAIN"
		result.RecoveryAction = "Call remote_capability status with this invocation_id; do not replay the operation."
	}
	return capabilityToolResult("invoke", result)
}

func codingBrowserCapabilityFailure(
	capability string,
	operation string,
	payload json.RawMessage,
) *toolshared.ToolResult {
	result := codingBrowserCapabilityResult(capability, operation, "failed", payload)
	var envelope codingremote.CapabilityResult
	if json.Unmarshal([]byte(result.ContentForLLM()), &envelope) != nil {
		return result
	}
	envelope.ErrorCode = "BROWSER_OPERATION_FAILED"
	return capabilityToolResult("invoke", envelope)
}

func codingBrowserToolByName(t *testing.T, tools []toolshared.Tool, name string) toolshared.Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name() == name {
			return tool
		}
	}
	t.Fatalf("coding browser tool %q is unavailable", name)
	return nil
}
