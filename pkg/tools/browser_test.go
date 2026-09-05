package tools

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/browseraction"
	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	providercommon "github.com/bogdanovich/mintclaw/pkg/providers/common"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type fakeBrowserToolSource struct {
	available             bool
	open                  browser.Session
	status                browser.Session
	statusAfterObserve    *browser.Session
	observe               browser.Observation
	screenshot            browser.ScreenshotArtifact
	lookup                browser.ScreenshotArtifact
	lookupHit             bool
	screenshotUnavailable bool
	transferUnavailable   bool
	downloadUnavailable   bool
	handoffReady          bool
	handoff               browser.Session
	resume                browser.Session
	prepare               browser.Preparation
	execute               browser.Invocation
	contextCatalog        browser.ContextCatalog
	contextPreparation    browser.ContextPreparation
	contextResult         browser.ContextResult
	diagnostics           browser.DiagnosticSummary
	err                   error
	observeErrors         []error
	executeErr            error

	openRequest            browser.OpenRequest
	statusOwner            browser.Owner
	statusSessionID        string
	statusCalls            int
	prepareRequest         browser.PrepareActionRequest
	screenshotRequest      browser.ScreenshotRequest
	deliveryRequest        browser.ScreenshotDeliveryRequest
	downloadDelivery       browser.DownloadDeliveryRequest
	observeCalls           int
	contextObserveCalls    int
	contextObserveRequests []browser.ObserveRequest
	contextObserveStarted  chan browser.ObserveRequest
	contextObserveRelease  <-chan struct{}
	executeOwner           browser.Owner
	executePrepared        string
	executeApproval        *browser.ApprovalBinding
	prepareCalls           int
	executeCalls           int
	contextRequest         browser.ContextRequest
	diagnosticsRequest     browser.DiagnosticsRequest
	contextApproval        *browser.ApprovalBinding
	profileStatus          browser.ProfileAvailability
	readiness              browser.PassiveReadiness
	readinessCalls         int
	actions                []browser.ActionKind
	cleanupOwner           browser.Owner
	cleanupCalls           int
}

func TestBrowserActDurableArgumentsRedactFillWithoutMutatingExecution(t *testing.T) {
	tool := &BrowserActTool{}
	original := map[string]any{
		"browser_session_id":  "session_1",
		"tab_id":              "tab_1",
		"snapshot_id":         "snapshot_1",
		"snapshot_generation": 1,
		"action":              map[string]any{"kind": "fill", "ref": "ref_1", "value": "canary-secret"},
	}
	projected, err := tool.DurableArguments(original)
	if err != nil {
		t.Fatalf("DurableArguments() error = %v", err)
	}
	projectedAction := projected["action"].(map[string]any)
	if projectedAction["value"] != browserProtectedInputRedaction {
		t.Fatalf("durable value = %#v", projectedAction["value"])
	}
	originalAction := original["action"].(map[string]any)
	if originalAction["value"] != "canary-secret" {
		t.Fatalf("in-memory value was mutated: %#v", originalAction["value"])
	}
}

func TestBrowserActDurableArgumentsRedactDialogPromptIncludingEmptyValue(t *testing.T) {
	tool := &BrowserActTool{}
	for _, value := range []string{"dialog-canary-secret", ""} {
		original := map[string]any{
			"action": map[string]any{
				"kind": "dialog", "dialog_id": "dialog_1", "decision": "accept", "value": value,
			},
		}
		projected, err := tool.DurableArguments(original)
		if err != nil {
			t.Fatalf("DurableArguments(%q) error = %v", value, err)
		}
		if got := projected["action"].(map[string]any)["value"]; got != browserProtectedInputRedaction {
			t.Fatalf("durable dialog value = %#v", got)
		}
		if got := original["action"].(map[string]any)["value"]; got != value {
			t.Fatalf("live dialog value = %#v, want %q", got, value)
		}
		if !tool.ProtectedDurableArguments(original) {
			t.Fatalf("dialog prompt %q was not protected", value)
		}
	}
	if tool.ProtectedDurableArguments(map[string]any{
		"action": map[string]any{"kind": "dialog", "dialog_id": "dialog_1", "decision": "dismiss"},
	}) {
		t.Fatal("dialog dismissal unexpectedly required protected batching")
	}
}

func TestToolRegistryDurableArgumentsRejectMalformedBrowserActionBeforeProjection(t *testing.T) {
	registry := NewToolRegistry()
	registry.Register(&BrowserActTool{})
	secret := "durable-projection-secret"
	projected, protected, err := registry.DurableArguments("browser_act", map[string]any{
		"browser_session_id":  "session_1",
		"tab_id":              "tab_1",
		"snapshot_id":         "snapshot_1",
		"snapshot_generation": 1,
		"action": map[string]any{
			"kind": "fill", "ref": "ref_1", "value": secret, "unknown_sensitive_field": secret,
		},
	})
	if err == nil {
		t.Fatal("DurableArguments() error = nil, want malformed action rejection")
	}
	if projected != nil || protected {
		t.Fatalf("DurableArguments() = %#v, protected %v; want no persistent projection", projected, protected)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("DurableArguments() leaked protected input in error: %v", err)
	}
}

func TestToolRegistryDurableArgumentsOmitNullOptionalBrowserContext(t *testing.T) {
	registry := NewToolRegistry()
	registry.Register(&BrowserActTool{})
	arguments := map[string]any{
		"browser_session_id": "session_1", "tab_id": "tab_1",
		"frame_id": nil, "context_catalog_id": nil, "context_generation": nil,
		"snapshot_id": "snapshot_1", "snapshot_generation": 1,
		"action": map[string]any{"kind": "click", "ref": "ref_1"},
		"effect": "navigation",
	}
	projected, protected, err := registry.DurableArguments("browser_act", arguments)
	if err != nil || protected {
		t.Fatalf("DurableArguments() = %#v, protected %v, %v", projected, protected, err)
	}
	for _, field := range []string{"frame_id", "context_catalog_id", "context_generation"} {
		if _, present := projected[field]; present {
			t.Fatalf("durable arguments retained null optional field %q: %#v", field, projected)
		}
		if value, present := arguments[field]; !present || value != nil {
			t.Fatalf("live arguments were mutated at %q: %#v", field, arguments)
		}
	}
}

func TestToolRegistryExecutesBrowserActionWithNullOptionalContext(t *testing.T) {
	source := &fakeBrowserToolSource{available: true, err: browser.ErrDenied}
	registry := NewToolRegistry()
	registry.Register(NewBrowserActTool(browserToolTestConfig(), source))
	arguments := map[string]any{
		"browser_session_id": "session_1", "tab_id": "tab_primary",
		"frame_id": nil, "context_catalog_id": nil, "context_generation": nil,
		"snapshot_id": "snapshot_1", "snapshot_generation": 1,
		"action": map[string]any{"kind": "click", "ref": "ref_1"},
		"effect": "navigation",
	}
	result := registry.Execute(browserToolTestContext(), "browser_act", arguments)
	if result == nil || !result.IsError || source.prepareCalls != 1 || source.executeCalls != 0 {
		t.Fatalf("Execute() result = %#v; prepare=%d execute=%d", result, source.prepareCalls, source.executeCalls)
	}
	if source.prepareRequest.ContextCatalogID != "" || source.prepareRequest.ContextGeneration != 0 ||
		source.prepareRequest.FrameID != "" {
		t.Fatalf("PrepareAction() context binding = %+v", source.prepareRequest)
	}
	for _, field := range []string{"frame_id", "context_catalog_id", "context_generation"} {
		if value, present := arguments[field]; !present || value != nil {
			t.Fatalf("live provider arguments were mutated at %q: %#v", field, arguments)
		}
	}
}

func TestBrowserPageResultsAreAlwaysProtectedFromDurableState(t *testing.T) {
	observe := &BrowserObserveTool{}
	contexts := &BrowserContextsTool{}
	action := &BrowserActTool{}
	args := map[string]any{"browser_session_id": "session_1"}

	for name, protected := range map[string]bool{
		"observe":  observe.ProtectedDurableResult(args),
		"contexts": contexts.ProtectedDurableResult(args),
		"navigate": action.ProtectedDurableResult(map[string]any{
			"action": map[string]any{"kind": "navigate", "url": "https://example.com"},
		}),
	} {
		if !protected {
			t.Fatalf("%s result was not protected", name)
		}
	}
	if action.ProtectedDurableArguments(map[string]any{
		"action": map[string]any{"kind": "navigate", "url": "https://example.com"},
	}) {
		t.Fatal("navigate intent unexpectedly requires singleton protected batching")
	}
	if !action.ProtectedDurableArguments(map[string]any{
		"action": map[string]any{"kind": "fill", "ref": "ref_1", "value": "secret"},
	}) {
		t.Fatal("fill intent was not protected")
	}

	projected, err := observe.DurableArguments(args)
	if err != nil || !reflect.DeepEqual(projected, args) {
		t.Fatalf("observe durable arguments = %#v, %v", projected, err)
	}
	projected["browser_session_id"] = "changed"
	if args["browser_session_id"] != "session_1" {
		t.Fatal("observe durable projection mutated live arguments")
	}
}

func TestToolRegistryDurableArgumentsPreserveUnknownToolCalls(t *testing.T) {
	registry := NewToolRegistry()
	arguments := map[string]any{"value": "handled by a later layer"}
	projected, protected, err := registry.DurableArguments("hook_owned_tool", arguments)
	if err != nil || protected || !reflect.DeepEqual(projected, arguments) {
		t.Fatalf("DurableArguments() = %#v, protected %v, %v; want unchanged arguments", projected, protected, err)
	}
}

func TestToolLogArgumentsRedactsBrowserFill(t *testing.T) {
	secret := "browser-fill-canary"
	got := ToolLogArguments("browser_act", map[string]any{
		"action": map[string]any{"kind": "fill", "value": secret},
	})
	encoded, err := json.Marshal(got)
	if err != nil || strings.Contains(string(encoded), secret) || got["redacted"] != true {
		t.Fatalf("logged browser arguments = %s, %v", encoded, err)
	}
}

func (source *fakeBrowserToolSource) CloseOwner(_ context.Context, owner browser.Owner) error {
	source.cleanupOwner = owner
	source.cleanupCalls++
	return source.err
}

func (source *fakeBrowserToolSource) ObserveContext(
	_ context.Context,
	request browser.ObserveRequest,
) (browser.Observation, error) {
	source.contextObserveCalls++
	source.contextObserveRequests = append(source.contextObserveRequests, request)
	if source.contextObserveStarted != nil {
		source.contextObserveStarted <- request
	}
	if source.contextObserveRelease != nil {
		<-source.contextObserveRelease
	}
	if source.statusAfterObserve != nil {
		source.status = *source.statusAfterObserve
	}
	return source.observe, source.nextObserveError()
}

func (source *fakeBrowserToolSource) Diagnostics(
	_ context.Context,
	request browser.DiagnosticsRequest,
) (browser.DiagnosticSummary, error) {
	source.diagnosticsRequest = request
	return source.diagnostics, source.err
}

func (source *fakeBrowserToolSource) ListContexts(
	_ context.Context,
	_ browser.Owner,
	_ string,
) (browser.ContextCatalog, error) {
	return source.contextCatalog, source.err
}

func (source *fakeBrowserToolSource) PrepareContext(
	_ context.Context,
	request browser.ContextRequest,
) (browser.ContextPreparation, error) {
	source.contextRequest = request
	result := source.contextPreparation
	result.Request = request
	return result, source.err
}

func (source *fakeBrowserToolSource) ExecuteContext(
	_ context.Context,
	_ browser.ContextPreparation,
	approval *browser.ApprovalBinding,
) (browser.ContextResult, error) {
	source.contextApproval = approval
	return source.contextResult, source.executeErr
}

func (source *fakeBrowserToolSource) Available() bool { return source.available }

func (source *fakeBrowserToolSource) ScreenshotAvailable() bool {
	return source != nil && !source.screenshotUnavailable
}

func (source *fakeBrowserToolSource) ArtifactTransferAvailable() bool {
	return source != nil && !source.transferUnavailable
}

func (source *fakeBrowserToolSource) DownloadAvailable() bool {
	return source != nil && !source.downloadUnavailable
}

func (source *fakeBrowserToolSource) HandoffAvailable() bool {
	return source != nil && source.handoffReady
}

func (source *fakeBrowserToolSource) ProfileAvailability(
	_ context.Context,
	_ string,
	_ string,
) (browser.ProfileAvailability, error) {
	if source.err != nil {
		return browser.ProfileAvailability{}, source.err
	}
	if source.profileStatus.Status == "" {
		return browser.ProfileAvailability{Status: "ready"}, nil
	}
	return source.profileStatus, nil
}

func (source *fakeBrowserToolSource) PassiveTargetDiagnostics(
	_ context.Context,
	_ string,
	profiles []string,
) (BrowserTargetDiagnostics, error) {
	source.readinessCalls++
	if !source.available {
		return BrowserTargetDiagnostics{}, browser.ErrWorkerUnavailable
	}
	if source.err != nil {
		return BrowserTargetDiagnostics{}, source.err
	}
	profile := source.profileStatus
	if profile.Status == "" {
		profile.Status = browser.ReadinessReady
	}
	readiness := source.readiness
	if readiness.Status == "" {
		readiness = browser.PassiveReadiness{
			Status: browser.ReadinessReady, Broker: browser.ReadinessReady,
			Worker: browser.ReadinessReady, Driver: browser.ReadinessReady,
			Browser: browser.ReadinessReady, Proxy: browser.ReadinessReady,
			Compatibility: browser.CompatibilityCompatible,
			Profile:       profile, Passive: true,
		}
		switch profile.Status {
		case browser.ReadinessBusy:
			readiness.Status = browser.ReadinessBusy
			readiness.Code, readiness.Action = "profile_busy", "wait_or_close_session"
		case browser.ReadinessDegraded:
			readiness.Status, readiness.Worker = browser.ReadinessDegraded, browser.ReadinessDegraded
			readiness.Code, readiness.Action = "recovery_required", "close_or_recover_session"
		}
	}
	byProfile := make(map[string]browser.PassiveReadiness, len(profiles))
	for _, name := range profiles {
		byProfile[name] = readiness
	}
	actions := source.actions
	if actions == nil {
		actions = []browser.ActionKind{
			browser.ActionNavigate, browser.ActionClick, browser.ActionFill, browser.ActionSelect,
			browser.ActionCheck, browser.ActionUncheck, browser.ActionHover,
			browser.ActionPress, browser.ActionScroll, browser.ActionDialog,
		}
	}
	return BrowserTargetDiagnostics{
		Profiles:    byProfile,
		Actions:     actions,
		Contexts:    true,
		Diagnostics: true,
		Screenshot:  !source.screenshotUnavailable,
		Upload:      !source.transferUnavailable,
		Download:    !source.transferUnavailable && !source.downloadUnavailable,
		HeadedView:  source.handoffReady, Handoff: source.handoffReady,
	}, nil
}

func (source *fakeBrowserToolSource) Open(
	_ context.Context,
	request browser.OpenRequest,
) (browser.Session, error) {
	source.openRequest = request
	result := source.open
	result.Owner = request.Owner
	return result, source.err
}

func (source *fakeBrowserToolSource) Handoff(
	_ context.Context, owner browser.Owner, _ string,
) (browser.Session, error) {
	result := source.handoff
	result.Owner = owner
	return result, source.err
}

func (source *fakeBrowserToolSource) Resume(
	_ context.Context, owner browser.Owner, _ string,
) (browser.Session, error) {
	result := source.resume
	result.Owner = owner
	return result, source.err
}

func (source *fakeBrowserToolSource) ReleaseHandoff(
	_ context.Context, owner browser.Owner, _ string,
) (browser.Session, error) {
	result := source.handoff
	result.Owner = owner
	result.Controller = browser.ControllerResumePending
	return result, source.err
}

func (source *fakeBrowserToolSource) Status(
	_ context.Context,
	owner browser.Owner,
	sessionID string,
) (browser.Session, error) {
	source.statusCalls++
	source.statusOwner = owner
	source.statusSessionID = sessionID
	return source.status, source.err
}

func (source *fakeBrowserToolSource) Close(
	_ context.Context,
	owner browser.Owner,
	sessionID string,
) (browser.Session, error) {
	source.statusOwner = owner
	source.statusSessionID = sessionID
	return source.status, source.err
}

func (source *fakeBrowserToolSource) Observe(
	_ context.Context,
	owner browser.Owner,
	sessionID string,
	tabID string,
) (browser.Observation, error) {
	source.observeCalls++
	source.statusOwner = owner
	source.statusSessionID = sessionID + ":" + tabID
	if source.statusAfterObserve != nil {
		source.status = *source.statusAfterObserve
	}
	return source.observe, source.nextObserveError()
}

func (source *fakeBrowserToolSource) nextObserveError() error {
	if len(source.observeErrors) == 0 {
		return source.err
	}
	err := source.observeErrors[0]
	source.observeErrors = source.observeErrors[1:]
	return err
}

func (source *fakeBrowserToolSource) LookupScreenshot(
	_ context.Context,
	_ browser.Owner,
	_ string,
	_ string,
) (browser.ScreenshotArtifact, bool, error) {
	return source.lookup, source.lookupHit, source.err
}

func (source *fakeBrowserToolSource) CaptureScreenshot(
	_ context.Context,
	request browser.ScreenshotRequest,
) (browser.ScreenshotArtifact, error) {
	source.screenshotRequest = request
	source.statusOwner = request.Owner
	source.statusSessionID = request.SessionID + ":" + request.TabID + ":screenshot"
	return source.screenshot, source.err
}

func (source *fakeBrowserToolSource) ClaimScreenshotDelivery(
	_ context.Context,
	request browser.ScreenshotDeliveryRequest,
) error {
	source.deliveryRequest = request
	return source.err
}

func (source *fakeBrowserToolSource) ClaimDownloadDelivery(
	_ context.Context,
	request browser.DownloadDeliveryRequest,
) error {
	source.downloadDelivery = request
	return source.err
}

func (source *fakeBrowserToolSource) PrepareAction(
	_ context.Context,
	request browser.PrepareActionRequest,
) (browser.Preparation, error) {
	source.prepareCalls++
	source.prepareRequest = request
	return source.prepare, source.err
}

func (source *fakeBrowserToolSource) ExecuteAction(
	_ context.Context,
	owner browser.Owner,
	preparedID string,
	approval *browser.ApprovalBinding,
) (browser.Invocation, error) {
	source.executeCalls++
	source.executeOwner = owner
	source.executePrepared = preparedID
	if approval != nil {
		copy := *approval
		source.executeApproval = &copy
	}
	if source.executeErr != nil {
		return source.execute, source.executeErr
	}
	return source.execute, source.err
}

func browserToolTestRootConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.Tools.Browser = config.BrowserToolsConfig{
		Enabled: true,
		Agents:  []string{"browser"},
		Targets: map[string]config.BrowserTargetConfig{
			"gateway": {
				Enabled: true, Driver: config.BrowserDriverPlaywrightMCP, DriverServer: "playwright",
				Profiles: map[string]config.BrowserProfileConfig{
					"managed": {
						Enabled: true, Mode: config.BrowserProfileManaged, DryRun: true,
						NetworkMode:    config.BrowserNetworkExactOrigins,
						CapabilityMode: config.BrowserCapabilityFullAccess,
						ApprovalMode:   config.BrowserApprovalAlwaysCommit,
						AllowedOrigins: []string{"https://example.com"},
					},
				},
			},
		},
	}
	return cfg
}

func browserToolTestConfig() BrowserToolOptions {
	cfg := browserToolTestRootConfig()
	return NewBrowserToolOptions(cfg.Tools.Browser)
}

func TestBrowserToolOptionsAreIsolatedFromReloadConfigMutation(t *testing.T) {
	cfg := browserToolTestRootConfig()
	cfg.Tools.Browser.Limits.ActionSeconds = 17
	target := cfg.Tools.Browser.Targets[config.BrowserDefaultTarget]
	profile := target.Profiles[config.BrowserDefaultProfile]
	profile.Policy = &browserpolicy.Policy{
		DefaultDecision: browserpolicy.DecisionAllow,
		Rules: []browserpolicy.Rule{{
			ID: "navigation",
			Match: browserpolicy.RuleMatch{
				Actions: []string{string(browser.ActionNavigate)},
			},
			Decision: browserpolicy.DecisionAllow,
		}},
	}
	target.Profiles[config.BrowserDefaultProfile] = profile
	cfg.Tools.Browser.Targets[config.BrowserDefaultTarget] = target

	options := NewBrowserToolOptions(cfg.Tools.Browser)
	cfg.Tools.Browser.Agents[0] = "other"
	cfg.Tools.Browser.DefaultTarget = "other"
	cfg.Tools.Browser.Limits.ActionSeconds = 29
	profile.AllowedOrigins[0] = "https://changed.example"
	profile.Policy.Rules[0].Match.Actions[0] = string(browser.ActionClick)
	target.Profiles[config.BrowserDefaultProfile] = profile
	cfg.Tools.Browser.Targets[config.BrowserDefaultTarget] = target

	tool := NewBrowserTargetsTool(options, &fakeBrowserToolSource{available: true})
	if !tool.ToolEnabledForAgent("browser") || tool.ToolEnabledForAgent("other") {
		t.Fatal("browser tool options observed a mutated agent allowlist")
	}
	if options.config.DefaultTarget != config.BrowserDefaultTarget || options.config.Limits.ActionSeconds != 17 {
		t.Fatalf("browser tool options observed mutated scalar policy: %#v", options.config)
	}
	snapshotProfile := options.config.Targets[config.BrowserDefaultTarget].Profiles[config.BrowserDefaultProfile]
	if snapshotProfile.AllowedOrigins[0] != "https://example.com" ||
		snapshotProfile.Policy.Rules[0].Match.Actions[0] != string(browser.ActionNavigate) {
		t.Fatalf("browser tool options observed mutated nested policy: %#v", snapshotProfile)
	}
}

func browserToolTestContext() context.Context {
	ctx := toolshared.WithToolInboundMetadata(context.Background(), bus.InboundContext{
		SenderID: "telegram-user-42", ActorID: "person:42",
	})
	ctx = toolshared.WithToolSessionContext(ctx, "browser", "history-session", nil)
	ctx = toolshared.WithToolRouteSessionKey(ctx, "telegram:primary:chat:42")
	ctx = toolshared.WithToolCallID(ctx, "provider-call/1")
	ctx = toolshared.WithToolExecutionIdentity(ctx, "/workspace/private", "execution/1")
	return toolshared.WithToolRecoverableOutbound(ctx, true)
}

func decodeBrowserToolResult(t *testing.T, result *toolshared.ToolResult, target any) {
	t.Helper()
	if result == nil || result.IsError {
		t.Fatalf("result = %#v, want success", result)
	}
	if err := json.Unmarshal([]byte(result.ContentForLLM()), target); err != nil {
		t.Fatalf("decode result: %v; content = %q", err, result.ContentForLLM())
	}
}

func TestBrowserDiagnosticsReturnsProtectedBoundedSummary(t *testing.T) {
	source := &fakeBrowserToolSource{available: true, diagnostics: browser.DiagnosticSummary{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotID: "snapshot_1",
		SnapshotGeneration: 3,
		Categories: []browser.DiagnosticCategorySummary{{
			Category: browser.DiagnosticConsoleErrors, Count: 1,
			Entries: []browser.DiagnosticEntry{{
				Timestamp: 1, Severity: "error", Origin: "https://example.com", Path: "/safe",
				MessageHash: strings.Repeat("a", 64),
			}},
		}},
	}}
	tool := NewBrowserDiagnosticsTool(browserToolTestConfig(), source)
	args := map[string]any{
		"browser_session_id": "browser_session_1",
		"categories":         []any{"console_errors"},
		"tab_id":             "tab_primary", "snapshot_id": "snapshot_1",
		"snapshot_generation": 3,
	}
	var summary browser.DiagnosticSummary
	decodeBrowserToolResult(t, tool.Execute(browserToolTestContext(), args), &summary)
	if !tool.ProtectedDurableResult(args) || summary.Categories[0].Entries[0].Path != "/safe" {
		t.Fatalf("diagnostics result = %+v, protected=%v", summary, tool.ProtectedDurableResult(args))
	}
	if source.diagnosticsRequest.SessionID != "browser_session_1" ||
		source.diagnosticsRequest.TabID != "tab_primary" ||
		source.diagnosticsRequest.SnapshotGeneration != 3 ||
		!reflect.DeepEqual(
			source.diagnosticsRequest.Categories,
			[]browser.DiagnosticCategory{browser.DiagnosticConsoleErrors},
		) {
		t.Fatalf("diagnostics request = %+v", source.diagnosticsRequest)
	}
}

func TestBrowserDiagnosticsRejectsDuplicateCategories(t *testing.T) {
	result := NewBrowserDiagnosticsTool(
		browserToolTestConfig(), &fakeBrowserToolSource{available: true},
	).Execute(browserToolTestContext(), map[string]any{
		"browser_session_id": "browser_session_1",
		"categories":         []any{"console_errors", "console_errors"},
	})
	if result == nil || !result.IsError || !strings.Contains(result.ContentForLLM(), "invalid_request") {
		t.Fatalf("result = %#v", result)
	}
}

func browserToolContextCatalog() browser.ContextCatalog {
	return browser.ContextCatalog{
		ID: "catalog_gateway", Generation: 2, SelectedTabID: "tab_primary",
		Tabs: []browser.TabContext{{
			ID: "tab_primary", Kind: browser.TabPrimary, CreationSequence: 1,
			DocumentGeneration: 1, URL: "https://example.com", Origin: "https://example.com",
		}},
	}
}

func TestBrowserContextsListsBoundedCatalog(t *testing.T) {
	source := &fakeBrowserToolSource{available: true, contextCatalog: browserToolContextCatalog()}
	tool := NewBrowserContextsTool(browserToolTestConfig(), source)
	if !tool.ToolEnabledForAgent("browser") {
		t.Fatal("browser_contexts is not enabled for the admitted agent")
	}
	var result browserContextResultView
	decodeBrowserToolResult(t, tool.Execute(browserToolTestContext(), map[string]any{
		"operation": "list", "browser_session_id": "browser_session_1",
	}), &result)
	if result.ContextCatalog.ID != source.contextCatalog.ID || result.ContextCatalog.Generation != 2 {
		t.Fatalf("browser_contexts list = %#v", result)
	}
}

func TestBrowserContextsSchemaExplainsOperationSpecificAuthority(t *testing.T) {
	tool := NewBrowserContextsTool(browserToolTestConfig(), &fakeBrowserToolSource{available: true})
	description := tool.Description()
	parameters := tool.Parameters()
	properties := parameters["properties"].(map[string]any)

	for _, want := range []string{
		"For list and open, send only operation and browser_session_id",
		"open creates and selects a new tab",
		"For select and close, use the fresh context_catalog_id",
	} {
		if !strings.Contains(description, want) {
			t.Fatalf("browser_contexts description %q does not contain %q", description, want)
		}
	}
	for _, name := range []string{
		"operation", "browser_session_id", "context_catalog_id", "context_generation", "tab_id", "frame_id",
	} {
		property := properties[name].(map[string]any)
		if property["description"] == "" {
			t.Fatalf("browser_contexts property %q has no operation guidance: %#v", name, property)
		}
	}
	if description := properties["context_catalog_id"].(map[string]any)["description"].(string); !strings.Contains(
		description,
		"omit for list and open",
	) {
		t.Fatalf("context_catalog_id description = %q", description)
	}
}

func TestBrowserSessionSchemaDistinguishesTargetAndProfile(t *testing.T) {
	tool := NewBrowserSessionTool(browserToolTestConfig(), &fakeBrowserToolSource{available: true})
	description := tool.Description()
	properties := tool.Parameters()["properties"].(map[string]any)

	for _, want := range []string{
		"target is the browser target name from browser_targets",
		"profile is the profile name nested under that target",
		"same visible local browser window",
		"call resume on the same session",
		"observe fresh state",
	} {
		if !strings.Contains(description, want) {
			t.Fatalf("browser_session description %q does not contain %q", description, want)
		}
	}
	if target := properties["target"].(map[string]any)["description"].(string); !strings.Contains(
		target,
		"browser_targets.default_target",
	) {
		t.Fatalf("target description = %q", target)
	}
	if profile := properties["profile"].(map[string]any)["description"].(string); !strings.Contains(
		profile,
		"such as managed",
	) {
		t.Fatalf("profile description = %q", profile)
	}
}

func TestBrowserContextsCloseSuspendsAndUsesExactPreparedApproval(t *testing.T) {
	catalog := browserToolContextCatalog()
	invocation := browser.Invocation{
		ID: "context_invocation_1", ActionHash: strings.Repeat("a", 64),
		Effect: browser.EffectUnknown, State: browser.InvocationPrepared, ExpiresAt: 12345,
	}
	preparation := browser.ContextPreparation{
		Invocation: invocation,
		Approval: browser.ApprovalBinding{
			PreparedActionID: invocation.ID, ActionHash: invocation.ActionHash, ExpiresAt: invocation.ExpiresAt,
		},
		RequiresApproval: true,
	}
	succeeded := invocation
	succeeded.State = browser.InvocationSucceeded
	source := &fakeBrowserToolSource{
		available: true, contextPreparation: preparation,
		contextResult: browser.ContextResult{Catalog: catalog, Invocation: &succeeded},
	}
	tool := NewBrowserContextsTool(browserToolTestConfig(), source)
	args := map[string]any{
		"operation": "close", "browser_session_id": "browser_session_1",
		"context_catalog_id": catalog.ID, "context_generation": 2,
		"tab_id": "tab_secondary",
	}
	approval, err := tool.ApprovalArguments(browserToolTestContext(), args)
	if err != nil || approval["context_invocation_id"] != invocation.ID ||
		approval["action_hash"] != invocation.ActionHash {
		t.Fatalf("ApprovalArguments() = %#v, %v", approval, err)
	}
	if suspended := tool.Execute(browserToolTestContext(), args); suspended.Control.Suspension == nil {
		t.Fatalf("close did not suspend: %#v", suspended)
	}
	resumeCtx := toolshared.WithToolApprovalContinuation(browserToolTestContext(), true)
	var result browserContextResultView
	decodeBrowserToolResult(t, tool.Execute(resumeCtx, args), &result)
	if source.contextApproval == nil || source.contextApproval.PreparedActionID != invocation.ID ||
		result.InvocationID != invocation.ID || result.State != browser.InvocationSucceeded {
		t.Fatalf("approved close = %#v; binding = %#v", result, source.contextApproval)
	}
}

func TestBrowserTargetsIsScopedAndSideEffectFree(t *testing.T) {
	source := &fakeBrowserToolSource{available: true}
	tool := NewBrowserTargetsTool(browserToolTestConfig(), source)
	if !tool.ToolEnabledForAgent("browser") || tool.ToolEnabledForAgent("main") {
		t.Fatal("browser target tool agent scope is incorrect")
	}
	var result browserTargetResult
	decodeBrowserToolResult(t, tool.Execute(browserToolTestContext(), nil), &result)
	if result.DefaultTarget != "gateway" || len(result.Targets) != 1 || result.Targets[0].Target != "gateway" ||
		result.Targets[0].Status != "ready" || len(result.Targets[0].Profiles) != 1 ||
		result.Targets[0].Profiles[0].Mode != config.BrowserProfileManaged ||
		result.Targets[0].Profiles[0].Persistence != "retained" ||
		result.Targets[0].Profiles[0].NetworkMode != config.BrowserNetworkExactOrigins ||
		result.Targets[0].Profiles[0].CapabilityMode != config.BrowserCapabilityFullAccess ||
		result.Targets[0].Profiles[0].ApprovalMode != config.BrowserApprovalAlwaysCommit ||
		!result.Targets[0].Profiles[0].DryRun || result.Targets[0].Profiles[0].AllowApprovedActions ||
		!result.Targets[0].Features.Screenshot || !result.Targets[0].Features.PageScreenshot ||
		!result.Targets[0].Features.ElementScreenshot ||
		!result.Targets[0].Features.Upload || !result.Targets[0].Features.Download ||
		result.Targets[0].Limits.ScreenshotBytes != config.BrowserMaxScreenshotBytes ||
		result.Targets[0].Limits.UploadBytes != config.BrowserMaxUploadBytes ||
		result.Targets[0].Limits.DownloadBytes != config.BrowserMaxDownloadBytes ||
		result.Targets[0].Limits.SessionSeconds != config.BrowserMaxSessionSeconds ||
		result.Targets[0].Limits.ActionSeconds != config.BrowserMaxActionSeconds ||
		result.Targets[0].Limits.SnapshotRefs != config.BrowserMaxSnapshotRefs ||
		result.Targets[0].Limits.ToolResultBytes != config.BrowserMaxToolResultBytes ||
		result.Targets[0].Limits.RetentionSecs != config.BrowserMaxRetentionSeconds ||
		!result.Targets[0].Features.Diagnostics || result.Targets[0].Features.HeadedView ||
		result.Targets[0].Features.Handoff ||
		!result.Targets[0].Profiles[0].Readiness.Passive ||
		result.Targets[0].Profiles[0].Readiness.Compatibility != browser.CompatibilityCompatible ||
		source.readinessCalls != 1 || source.openRequest.Target != "" {
		t.Fatalf("browser targets = %#v", result)
	}

	other := toolshared.WithToolSessionContext(browserToolTestContext(), "main", "history-session", nil)
	denied := tool.Execute(other, nil)
	if denied == nil || !denied.IsError || !strings.Contains(denied.ContentForLLM(), `"code":"not_granted"`) {
		t.Fatalf("ungranted result = %#v", denied)
	}
}

func TestBrowserTargetsFiltersCanonicalProfilesByExactActorAndAgentGrant(t *testing.T) {
	cfg := browserToolTestConfig()
	target := cfg.Tools.Browser.Targets["gateway"]
	profile := target.Profiles["managed"]
	profile.Revision = "managed-v1"
	profile.AllowedAgents = []string{"browser"}
	profile.AllowedActors = []string{"person:42"}
	profile.Runtime = config.BrowserProfileRuntimeConfig{
		ProfileDirectory: "/private/browser/managed",
		LockFile:         "/private/browser/managed.lock",
		Headed:           true,
	}
	target.Profiles["managed"] = profile
	cfg.Tools.Browser.Targets["gateway"] = target

	source := &fakeBrowserToolSource{available: true}
	tool := NewBrowserTargetsTool(cfg, source)
	granted := tool.Execute(browserToolTestContext(), nil)
	var result browserTargetResult
	decodeBrowserToolResult(t, granted, &result)
	if len(result.Targets) != 1 || len(result.Targets[0].Profiles) != 1 ||
		result.Targets[0].Profiles[0].Profile != "managed" ||
		result.Targets[0].Profiles[0].Mode != config.BrowserProfileManaged ||
		result.Targets[0].Profiles[0].Persistence != "retained" {
		t.Fatalf("granted canonical targets = %#v", result)
	}
	for _, forbidden := range []string{
		"managed-v1", "person:42", "/private/browser", "managed.lock",
	} {
		if strings.Contains(granted.ContentForLLM(), forbidden) {
			t.Fatalf("discovery exposed private authority %q: %s", forbidden, granted.ContentForLLM())
		}
	}

	otherActor := toolshared.WithToolInboundMetadata(browserToolTestContext(), bus.InboundContext{
		SenderID: "telegram-user-99", ActorID: "person:99",
	})
	var hidden browserTargetResult
	decodeBrowserToolResult(t, tool.Execute(otherActor, nil), &hidden)
	if hidden.DefaultTarget != "" || len(hidden.Targets) != 0 || source.readinessCalls != 1 {
		t.Fatalf("ungranted canonical targets = %#v; readiness calls = %d", hidden, source.readinessCalls)
	}
}

func TestBrowserTargetsReportsAndOrdersExplicitDefaultBeforeAlphabeticalTargets(t *testing.T) {
	cfg := browserToolTestRootConfig()
	cfg.Tools.Browser.Targets["companion"] = config.BrowserTargetConfig{
		Enabled: true,
		Profiles: map[string]config.BrowserProfileConfig{
			"managed": {
				Enabled: true, Mode: config.BrowserProfileManaged,
				NetworkMode: config.BrowserNetworkAnyHTTP, DryRun: true,
			},
		},
	}
	cfg.Tools.Browser.DefaultTarget = "gateway"

	var result browserTargetResult
	decodeBrowserToolResult(
		t,
		NewBrowserTargetsTool(
			NewBrowserToolOptions(cfg.Tools.Browser),
			&fakeBrowserToolSource{available: true},
		).Execute(
			browserToolTestContext(), nil,
		),
		&result,
	)
	if result.DefaultTarget != "gateway" || len(result.Targets) != 2 ||
		result.Targets[0].Target != "gateway" || result.Targets[1].Target != "companion" {
		t.Fatalf("browser targets = %#v", result)
	}
}

func TestBrowserTargetsReportsExplicitApprovedActionMode(t *testing.T) {
	cfg := browserToolTestRootConfig()
	target := cfg.Tools.Browser.Targets["gateway"]
	profile := target.Profiles["managed"]
	profile.DryRun = false
	profile.AllowApprovedActions = true
	target.Profiles["managed"] = profile
	cfg.Tools.Browser.Targets["gateway"] = target

	var result browserTargetResult
	decodeBrowserToolResult(
		t,
		NewBrowserTargetsTool(
			NewBrowserToolOptions(cfg.Tools.Browser),
			&fakeBrowserToolSource{available: true},
		).Execute(
			browserToolTestContext(), nil,
		),
		&result,
	)
	if len(result.Targets) != 1 || len(result.Targets[0].Profiles) != 1 ||
		result.Targets[0].Profiles[0].DryRun || !result.Targets[0].Profiles[0].AllowApprovedActions {
		t.Fatalf("approved-action browser targets = %#v", result)
	}
}

func TestBrowserTargetsAdvertisesCompleteContextParityAndBounds(t *testing.T) {
	var result browserTargetResult
	decodeBrowserToolResult(
		t, NewBrowserTargetsTool(browserToolTestConfig(), &fakeBrowserToolSource{available: true}).Execute(
			browserToolTestContext(), nil,
		), &result,
	)
	if len(result.Targets) != 1 || !result.Targets[0].Features.Tabs ||
		!result.Targets[0].Features.Popups || !result.Targets[0].Features.Frames ||
		result.Targets[0].Limits.FramesPerTab != browser.MaxContextFramesPerTab ||
		result.Targets[0].Limits.FrameDepth != browser.MaxContextFrameDepth ||
		result.Targets[0].Limits.ContextCatalogBytes != browser.MaxContextCatalogBytes ||
		result.Targets[0].Limits.ContextLabelBytes != browser.MaxContextLabelBytes {
		t.Fatalf("browser context capabilities = %#v", result)
	}
}

func TestBrowserTargetsReportsUnavailableRuntimeWithoutAdvertisingCapabilities(t *testing.T) {
	source := &fakeBrowserToolSource{available: false}
	var result browserTargetResult
	decodeBrowserToolResult(
		t, NewBrowserTargetsTool(browserToolTestConfig(), source).Execute(browserToolTestContext(), nil), &result,
	)
	if len(result.Targets) != 1 || result.Targets[0].Status != browser.ReadinessUnavailable ||
		result.Targets[0].Reason != "runtime_unavailable" ||
		result.Targets[0].Features.Screenshot || result.Targets[0].Features.Upload ||
		result.Targets[0].Features.Download || result.Targets[0].Features.Diagnostics ||
		len(result.Targets[0].Actions) != 0 ||
		result.Targets[0].Profiles[0].Readiness.Code != "runtime_unavailable" ||
		!result.Targets[0].Profiles[0].Readiness.Passive || source.readinessCalls != 1 {
		t.Fatalf("unavailable browser targets = %#v", result)
	}
}

func TestBrowserTargetsFailsCapabilitiesClosedWhenDiagnosticsSnapshotFails(t *testing.T) {
	source := &fakeBrowserToolSource{available: true, err: errors.New("runtime reloaded")}
	var result browserTargetResult
	decodeBrowserToolResult(
		t, NewBrowserTargetsTool(browserToolTestConfig(), source).Execute(browserToolTestContext(), nil), &result,
	)
	if len(result.Targets) != 1 || result.Targets[0].Status != browser.ReadinessUnavailable ||
		result.Targets[0].Features.Screenshot || result.Targets[0].Features.Upload ||
		result.Targets[0].Features.Download || len(result.Targets[0].Actions) != 0 ||
		result.Targets[0].Profiles[0].Readiness.Code != "runtime_unavailable" {
		t.Fatalf("failed diagnostics snapshot = %#v", result)
	}
}

func TestBrowserTargetsAggregatesDriverReadiness(t *testing.T) {
	for _, test := range []struct {
		name   string
		status string
		code   string
	}{
		{name: "missing", status: browser.ReadinessUnavailable, code: "driver_missing"},
		{name: "incompatible", status: browser.ReadinessDegraded, code: "driver_incompatible"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := &fakeBrowserToolSource{available: true, readiness: browser.PassiveReadiness{
				Status: test.status, Broker: browser.ReadinessReady, Worker: test.status,
				Driver: test.status, Browser: browser.ReadinessUnavailable,
				Proxy: browser.ReadinessReady, Compatibility: browser.CompatibilityUnchecked,
				Profile: browser.ProfileAvailability{Status: browser.ReadinessReady},
				Code:    test.code, Action: "contact_operator", Passive: true,
			}}
			var result browserTargetResult
			decodeBrowserToolResult(
				t,
				NewBrowserTargetsTool(browserToolTestConfig(), source).Execute(browserToolTestContext(), nil),
				&result,
			)
			if len(result.Targets) != 1 || result.Targets[0].Status != test.status ||
				result.Targets[0].Reason != test.code {
				t.Fatalf("aggregated readiness = %#v", result)
			}
		})
	}
}

func TestBrowserReadinessRankHasDeterministicFailClosedOrder(t *testing.T) {
	statuses := []string{
		browser.ReadinessReady,
		browser.ReadinessConfigured,
		browser.ReadinessBusy,
		browser.ReadinessDegraded,
		browser.ReadinessUnavailable,
	}
	for index := 1; index < len(statuses); index++ {
		if readinessRank(statuses[index]) <= readinessRank(statuses[index-1]) {
			t.Fatalf("readiness order = %#v", statuses)
		}
	}
	if readinessRank("unexpected") != readinessRank(browser.ReadinessUnavailable) {
		t.Fatal("unknown readiness did not fail closed")
	}
}

func TestBrowserSessionHandoffSuspendsForRoutedHumanRelease(t *testing.T) {
	source := &fakeBrowserToolSource{
		available: true, handoffReady: true,
		handoff: browser.Session{
			ID: "browser_session_1", State: browser.SessionReady, Target: "gateway", Profile: "managed",
			DryRun: true, Controller: browser.ControllerHuman, ControllerGeneration: 2,
			ControllerExpiresAt: 200, TabID: "tab_primary", ExpiresAt: 300,
		},
		resume: browser.Session{
			ID: "browser_session_1", State: browser.SessionReady, Target: "gateway", Profile: "managed",
			DryRun: true, Controller: browser.ControllerAgent, ControllerGeneration: 3,
			TabID: "tab_primary", ExpiresAt: 300,
		},
	}
	var targets browserTargetResult
	decodeBrowserToolResult(
		t, NewBrowserTargetsTool(browserToolTestConfig(), source).Execute(browserToolTestContext(), nil), &targets,
	)
	if len(targets.Targets) != 1 || !targets.Targets[0].Features.HeadedView ||
		!targets.Targets[0].Features.Handoff {
		t.Fatalf("handoff capabilities = %#v", targets)
	}
	tool := NewBrowserSessionTool(browserToolTestConfig(), source)
	handoff := tool.Execute(browserToolTestContext(), map[string]any{
		"operation": "handoff", "browser_session_id": "browser_session_1",
	})
	if handoff == nil || handoff.IsError || handoff.Control.Suspension == nil ||
		handoff.Control.ResolveSuspension == nil ||
		handoff.Control.Suspension.Kind != interactions.KindQuestion ||
		len(handoff.Control.Suspension.Questions) != 1 ||
		strings.Contains(strings.ToLower(handoff.ContentForLLM()), "token") {
		t.Fatalf("handoff result = %#v", handoff)
	}
	if err := interactions.ValidateSuspensionRequest(*handoff.Control.Suspension); err != nil {
		t.Fatalf("handoff suspension is invalid: %v", err)
	}
	question := handoff.Control.Suspension.Questions[0].Question
	for _, want := range []string{"signing in", "reply to release control", "same session"} {
		if !strings.Contains(question, want) {
			t.Fatalf("handoff question %q does not contain %q", question, want)
		}
	}
	var handoffView browserSessionView
	decodeBrowserToolResult(t, handoff, &handoffView)
	if handoffView.Controller != browser.ControllerHuman || handoffView.ControllerExpiresAt != 200 {
		t.Fatalf("handoff view = %#v", handoffView)
	}
	if err := handoff.Control.ResolveSuspension(t.Context(), interactions.OutcomeAnswered); err != nil {
		t.Fatalf("handoff resolution error = %v", err)
	}
	resume := tool.Execute(browserToolTestContext(), map[string]any{
		"operation": "resume", "browser_session_id": "browser_session_1",
	})
	if resume == nil || resume.IsError || resume.Control.Suspension != nil {
		t.Fatalf("resume result = %#v", resume)
	}
}

func TestBrowserScreenshotIsNotAdvertisedOrCapturedWhenDeliveryIsUnsupported(t *testing.T) {
	source := &fakeBrowserToolSource{available: true, screenshotUnavailable: true}
	var targets browserTargetResult
	decodeBrowserToolResult(
		t,
		NewBrowserTargetsTool(browserToolTestConfig(), source).Execute(browserToolTestContext(), nil),
		&targets,
	)
	if len(targets.Targets) != 1 || targets.Targets[0].Features.Screenshot {
		t.Fatalf("browser targets = %#v", targets)
	}
	result := NewBrowserObserveTool(browserToolTestConfig(), source).Execute(
		browserToolTestContext(),
		map[string]any{"browser_session_id": "browser_session_1", "screenshot": true},
	)
	if result == nil || !result.IsError ||
		!strings.Contains(result.ContentForLLM(), `"code":"unsupported_platform"`) ||
		source.observeCalls != 0 || source.screenshotRequest.RequestID != "" {
		t.Fatalf("unsupported screenshot result = %#v; source = %#v", result, source)
	}
}

func TestBrowserArtifactTransfersAreNotAdvertisedOrPreparedWhenPlatformIsUnsupported(t *testing.T) {
	source := &fakeBrowserToolSource{available: true, transferUnavailable: true}
	var targets browserTargetResult
	decodeBrowserToolResult(
		t, NewBrowserTargetsTool(browserToolTestConfig(), source).Execute(browserToolTestContext(), nil), &targets,
	)
	if len(targets.Targets) != 1 || targets.Targets[0].Features.Upload || targets.Targets[0].Features.Download {
		t.Fatalf("unsupported transfer features = %#v", targets)
	}
	for _, action := range targets.Targets[0].Actions {
		if action == browser.ActionFileChooser || action == browser.ActionDownload {
			t.Fatalf("unsupported action advertised: %q", action)
		}
	}
	result := NewBrowserActTool(browserToolTestConfig(), source).Execute(browserToolTestContext(), map[string]any{
		"browser_session_id": "browser_session_1", "tab_id": "tab_primary",
		"snapshot_id": "snapshot_1", "snapshot_generation": 1,
		"action": map[string]any{"kind": "download", "ref": "ref_download"},
	})
	if result == nil || !result.IsError || source.prepareCalls != 0 ||
		!strings.Contains(result.ContentForLLM(), `"code":"driver_incompatible"`) {
		t.Fatalf("unsupported transfer result = %#v; prepare calls = %d", result, source.prepareCalls)
	}
}

func TestBrowserDownloadIsNotAdvertisedOrPreparedWithoutScopedDriver(t *testing.T) {
	source := &fakeBrowserToolSource{available: true, downloadUnavailable: true}
	var targets browserTargetResult
	decodeBrowserToolResult(
		t, NewBrowserTargetsTool(browserToolTestConfig(), source).Execute(browserToolTestContext(), nil), &targets,
	)
	if len(targets.Targets) != 1 || !targets.Targets[0].Features.Upload || targets.Targets[0].Features.Download {
		t.Fatalf("scoped transfer features = %#v", targets)
	}
	fileChooser, download := false, false
	for _, action := range targets.Targets[0].Actions {
		fileChooser = fileChooser || action == browser.ActionFileChooser
		download = download || action == browser.ActionDownload
	}
	if !fileChooser || download {
		t.Fatalf("scoped transfer actions = %#v", targets.Targets[0].Actions)
	}
	result := NewBrowserActTool(browserToolTestConfig(), source).Execute(browserToolTestContext(), map[string]any{
		"browser_session_id": "browser_session_1", "tab_id": "tab_primary",
		"snapshot_id": "snapshot_1", "snapshot_generation": 1,
		"action": map[string]any{"kind": "download", "ref": "ref_download"},
	})
	if result == nil || !result.IsError || source.prepareCalls != 0 ||
		!strings.Contains(result.ContentForLLM(), `"code":"driver_incompatible"`) {
		t.Fatalf("unavailable download result = %#v; prepare calls = %d", result, source.prepareCalls)
	}
}

func TestBrowserActSchemaDoesNotAdvertiseDeferredDownload(t *testing.T) {
	parameters := NewBrowserActTool(
		browserToolTestConfig(),
		&fakeBrowserToolSource{available: true, downloadUnavailable: true},
	).Parameters()
	properties := parameters["properties"].(map[string]any)
	action := properties["action"].(map[string]any)
	if branch := browserActionSchemaBranch(action, browser.ActionDownload); branch != nil {
		t.Fatalf("deferred download action advertised in schema: %#v", branch)
	}
}

func TestBrowserActSchemaAdvertisesAdmittedDownload(t *testing.T) {
	parameters := NewBrowserActTool(browserToolTestConfig(), &fakeBrowserToolSource{available: true}).Parameters()
	properties := parameters["properties"].(map[string]any)
	action := properties["action"].(map[string]any)
	branch := browserActionSchemaBranch(action, browser.ActionDownload)
	if branch == nil {
		t.Fatalf("admitted download action missing from schema: %#v", action)
	}
	actionProperties := branch["properties"].(map[string]any)
	if _, ok := actionProperties["deliver"]; !ok {
		t.Fatalf("admitted download delivery field missing from schema: %#v", actionProperties)
	}
}

func TestBrowserActSchemaAdvertisesOrdinaryInteractions(t *testing.T) {
	parameters := NewBrowserActTool(
		browserToolTestConfig(), &fakeBrowserToolSource{available: true},
	).Parameters()
	properties := parameters["properties"].(map[string]any)
	action := properties["action"].(map[string]any)
	for _, candidate := range browseraction.Kinds() {
		if browserActionSchemaBranch(action, candidate) == nil {
			t.Fatalf("%s missing from browser_act schema: %#v", candidate, action)
		}
	}
	effect := properties["effect"].(map[string]any)
	if !reflect.DeepEqual(effect["enum"], []string{
		"read", "navigation", "local_edit", "external_commit", "unknown",
	}) || !strings.Contains(effect["description"].(string), "Required for click") {
		t.Fatalf("declared click effect schema = %#v", effect)
	}
}

func browserActionSchemaBranch(action map[string]any, kind browser.ActionKind) map[string]any {
	for _, candidate := range action["oneOf"].([]any) {
		branch := candidate.(map[string]any)
		properties := branch["properties"].(map[string]any)
		kindSchema := properties["kind"].(map[string]any)
		if kindSchema["const"] == string(kind) {
			return branch
		}
	}
	return nil
}

func TestBrowserActSchemaUsesExclusiveTypedActionShapes(t *testing.T) {
	parameters := NewBrowserActTool(
		browserToolTestConfig(), &fakeBrowserToolSource{available: true},
	).Parameters()
	properties := parameters["properties"].(map[string]any)
	action := properties["action"].(map[string]any)

	scroll := browserActionSchemaBranch(action, browser.ActionScroll)
	scrollProperties := scroll["properties"].(map[string]any)
	if _, ok := scrollProperties["target"]; ok {
		t.Fatalf("scroll schema advertises press-only target: %#v", scrollProperties)
	}
	amount := scrollProperties["amount"].(map[string]any)
	if amount["minimum"] != 1 || amount["maximum"] != browser.MaxScrollAmount {
		t.Fatalf("scroll amount schema = %#v", amount)
	}
	press := browserActionSchemaBranch(action, browser.ActionPress)
	pressProperties := press["properties"].(map[string]any)
	if target := pressProperties["target"].(map[string]any); !slices.Equal(
		target["enum"].([]string),
		[]string{"document"},
	) {
		t.Fatalf("press target schema = %#v", pressProperties["target"])
	}
	dialog := browserActionSchemaBranch(action, browser.ActionDialog)
	dialogProperties := dialog["properties"].(map[string]any)
	if _, ok := dialogProperties["prompt_provided"]; ok ||
		!slices.Contains(dialog["required"].([]string), "dialog_id") {
		t.Fatalf("dialog authority schema = %#v", dialog)
	}
}

func TestBrowserActSchemaSimpleTransformPreservesActionKinds(t *testing.T) {
	parameters := NewBrowserActTool(
		browserToolTestConfig(), &fakeBrowserToolSource{available: true},
	).Parameters()
	transformed := providercommon.SanitizeSchemaForGoogle(parameters)
	properties := transformed["properties"].(map[string]any)
	action := properties["action"].(map[string]any)
	actionProperties := action["properties"].(map[string]any)
	kind := actionProperties["kind"].(map[string]any)

	want := make([]any, 0, len(browseraction.Kinds()))
	for _, actionKind := range browseraction.Kinds() {
		want = append(want, string(actionKind))
	}
	if !reflect.DeepEqual(kind["enum"], want) {
		t.Fatalf("transformed browser action kinds = %#v, want %#v", kind["enum"], want)
	}
}

func TestBrowserActSchemaExplainsConditionalContextAuthority(t *testing.T) {
	tool := NewBrowserActTool(browserToolTestConfig(), &fakeBrowserToolSource{available: true})
	if description := tool.Description(); !strings.Contains(
		description,
		"missing or incomplete context authority fails closed",
	) {
		t.Fatalf("browser_act description = %q", description)
	}
	properties := tool.Parameters()["properties"].(map[string]any)
	for _, name := range []string{"context_catalog_id", "context_generation"} {
		property := properties[name].(map[string]any)
		description, _ := property["description"].(string)
		if !strings.Contains(description, "Conditionally required") {
			t.Fatalf("%s description = %q", name, description)
		}
	}
	action := properties["action"].(map[string]any)
	if description, _ := action["description"].(string); !strings.Contains(description, "do not add unrelated") {
		t.Fatalf("action description = %q", description)
	}
}

func TestBrowserActionToolStaleErrorInstructsAuthorityCopy(t *testing.T) {
	result := browserActionToolError(browser.ErrStale)
	if result == nil || !result.IsError ||
		!strings.Contains(result.ContentForLLM(), `"action":"observe_again_and_copy_authority"`) ||
		!strings.Contains(result.ContentForLLM(), "copy every returned authority field") {
		t.Fatalf("stale browser result = %#v", result)
	}
}

func TestBrowserActionNoProgressErrorRequiresScopeReplanning(t *testing.T) {
	result := browserActionToolError(browser.ErrNoProgress)
	if result == nil || !result.IsError ||
		!strings.Contains(result.ContentForLLM(), `"code":"no_progress"`) ||
		!strings.Contains(result.ContentForLLM(), `"action":"replan_collection_scope"`) {
		t.Fatalf("no-progress browser result = %#v", result)
	}
}

func TestBrowserToolStaleErrorRemainsOperationNeutral(t *testing.T) {
	result := browserToolError(browser.ErrStale)
	if result == nil || !result.IsError ||
		!strings.Contains(result.ContentForLLM(), `"action":"observe_again"`) ||
		strings.Contains(result.ContentForLLM(), "into the action") {
		t.Fatalf("neutral stale browser result = %#v", result)
	}
}

func TestBrowserToolSnapshotTransferErrorIsSafeAndRetryable(t *testing.T) {
	result := browserToolError(browser.ErrSnapshotTransfer)
	if result == nil || !result.IsError ||
		!strings.Contains(result.ContentForLLM(), `"code":"snapshot_transfer_failed"`) ||
		!strings.Contains(result.ContentForLLM(), `"action":"observe_again"`) {
		t.Fatalf("snapshot transfer browser result = %#v", result)
	}
}

func TestBrowserObserveRecoversOneStaleTopLevelRead(t *testing.T) {
	source := &fakeBrowserToolSource{
		available: true,
		observe: browser.Observation{
			SessionID: "browser_session_1", TabID: "tab_primary",
			SnapshotID: "snapshot_fresh", SnapshotGeneration: 2,
			URL: "https://example.com/postings", Origin: "https://example.com",
			Snapshot: "fresh postings",
		},
		observeErrors: []error{browser.ErrStale, nil},
	}
	result := NewBrowserObserveTool(browserToolTestConfig(), source).Execute(
		browserToolTestContext(),
		map[string]any{"browser_session_id": "browser_session_1", "tab_id": "tab_primary"},
	)
	var view browserObservationView
	decodeBrowserToolResult(t, result, &view)
	if result.IsError || source.observeCalls != 0 || source.contextObserveCalls != 2 ||
		source.statusCalls != 2 ||
		!view.StaleRecovered || view.SnapshotID != "snapshot_fresh" ||
		source.prepareCalls != 0 || source.executeCalls != 0 {
		t.Fatalf("stale recovery result = %#v; view = %#v; source = %#v", result, view, source)
	}
}

func TestBrowserObserveBoundsRepeatedStaleTopLevelRead(t *testing.T) {
	source := &fakeBrowserToolSource{
		available:     true,
		observeErrors: []error{browser.ErrStale, browser.ErrStale, nil},
	}
	result := NewBrowserObserveTool(browserToolTestConfig(), source).Execute(
		browserToolTestContext(),
		map[string]any{"browser_session_id": "browser_session_1", "tab_id": "tab_primary"},
	)
	if result == nil || !result.IsError || source.observeCalls != 0 || source.contextObserveCalls != 2 ||
		!strings.Contains(result.ContentForLLM(), `"action":"list_contexts_again"`) ||
		source.prepareCalls != 0 || source.executeCalls != 0 {
		t.Fatalf("bounded stale result = %#v; source = %#v", result, source)
	}
}

func TestBrowserObserveRetryUsesRefreshedImplicitSelectedTab(t *testing.T) {
	refreshed := browser.Session{ID: "browser_session_1", TabID: "tab_new"}
	source := &fakeBrowserToolSource{
		available:          true,
		status:             browser.Session{ID: "browser_session_1", TabID: "tab_old"},
		statusAfterObserve: &refreshed,
		observe: browser.Observation{
			SessionID: "browser_session_1", TabID: "tab_new", SnapshotID: "snapshot_fresh",
			SnapshotGeneration: 2,
		},
		observeErrors: []error{browser.ErrStale, nil},
	}
	result := NewBrowserObserveTool(browserToolTestConfig(), source).Execute(
		browserToolTestContext(), map[string]any{"browser_session_id": "browser_session_1"},
	)
	var view browserObservationView
	decodeBrowserToolResult(t, result, &view)
	if result.IsError || !view.StaleRecovered || view.TabID != "tab_new" || source.statusCalls != 2 ||
		len(source.contextObserveRequests) != 2 || source.contextObserveRequests[0].TabID != "tab_old" ||
		source.contextObserveRequests[1].TabID != "tab_new" {
		t.Fatalf("implicit tab refresh result = %#v; view = %#v; source = %#v", result, view, source)
	}
}

func TestBrowserObserveExplicitTabFailsClosedWhenSelectedTabChanges(t *testing.T) {
	refreshed := browser.Session{ID: "browser_session_1", TabID: "tab_new"}
	source := &fakeBrowserToolSource{
		available:          true,
		status:             browser.Session{ID: "browser_session_1", TabID: "tab_old"},
		statusAfterObserve: &refreshed,
		observeErrors:      []error{browser.ErrStale, nil},
	}
	result := NewBrowserObserveTool(browserToolTestConfig(), source).Execute(
		browserToolTestContext(),
		map[string]any{"browser_session_id": "browser_session_1", "tab_id": "tab_old"},
	)
	if result == nil || !result.IsError || source.statusCalls != 2 || source.contextObserveCalls != 1 ||
		!strings.Contains(result.ContentForLLM(), `"code":"context_catalog_stale"`) ||
		!strings.Contains(result.ContentForLLM(), `"action":"list_contexts_again"`) {
		t.Fatalf("explicit tab transition result = %#v; source = %#v", result, source)
	}
}

func TestBrowserObserveDoesNotReplayFrameSpecificStaleRead(t *testing.T) {
	source := &fakeBrowserToolSource{available: true, err: browser.ErrStale}
	result := NewBrowserObserveTool(browserToolTestConfig(), source).Execute(
		browserToolTestContext(),
		map[string]any{
			"browser_session_id": "browser_session_1", "tab_id": "tab_primary",
			"frame_id": "frame_1", "context_catalog_id": "catalog_1", "context_generation": 4,
		},
	)
	if result == nil || !result.IsError || source.contextObserveCalls != 1 || source.observeCalls != 0 ||
		!strings.Contains(result.ContentForLLM(), `"code":"context_catalog_stale"`) ||
		!strings.Contains(result.ContentForLLM(), `"action":"list_contexts_again"`) ||
		source.prepareCalls != 0 || source.executeCalls != 0 {
		t.Fatalf("frame stale result = %#v; source = %#v", result, source)
	}
}

func TestBrowserObserveDoesNotReplayImplicitSelectedFrameStaleRead(t *testing.T) {
	clearedStatus := browser.Session{ID: "browser_session_1", TabID: "tab_primary"}
	source := &fakeBrowserToolSource{
		available: true,
		status: browser.Session{
			ID: "browser_session_1", TabID: "tab_primary", FrameID: "frame_selected",
		},
		statusAfterObserve: &clearedStatus,
		observeErrors:      []error{browser.ErrStale, nil},
	}
	result := NewBrowserObserveTool(browserToolTestConfig(), source).Execute(
		browserToolTestContext(),
		map[string]any{"browser_session_id": "browser_session_1", "tab_id": "tab_primary"},
	)
	if result == nil || !result.IsError || source.statusCalls != 1 || source.observeCalls != 0 ||
		source.contextObserveCalls != 1 ||
		!strings.Contains(result.ContentForLLM(), `"code":"context_catalog_stale"`) ||
		!strings.Contains(result.ContentForLLM(), `"action":"list_contexts_again"`) ||
		source.prepareCalls != 0 || source.executeCalls != 0 {
		t.Fatalf("implicit frame stale result = %#v; source = %#v", result, source)
	}
}

func TestBrowserObserveDoesNotReplayFrameSelectedBetweenStatusAndObserve(t *testing.T) {
	catalog := browser.ContextCatalog{ID: "catalog_1", Generation: 1, SelectedTabID: "tab_primary"}
	observeStarted := make(chan browser.ObserveRequest, 1)
	releaseObserve := make(chan struct{})
	source := &fakeBrowserToolSource{
		available: true,
		status: browser.Session{
			ID: "browser_session_1", TabID: "tab_primary", ContextAuthority: &catalog,
		},
		observeErrors:         []error{browser.ErrStale, nil},
		contextObserveStarted: observeStarted,
		contextObserveRelease: releaseObserve,
	}
	resultCh := make(chan *toolshared.ToolResult, 1)
	go func() {
		resultCh <- NewBrowserObserveTool(browserToolTestConfig(), source).Execute(
			browserToolTestContext(),
			map[string]any{"browser_session_id": "browser_session_1", "tab_id": "tab_primary"},
		)
	}()

	request := <-observeStarted
	if request.FrameID != "" || request.ContextCatalogID != catalog.ID ||
		request.ContextGeneration != catalog.Generation {
		t.Fatalf("first bound observe request = %+v", request)
	}
	source.status.FrameID = "frame_selected"
	close(releaseObserve)
	result := <-resultCh
	if result == nil || !result.IsError || source.statusCalls != 2 || source.observeCalls != 0 ||
		source.contextObserveCalls != 1 ||
		!strings.Contains(result.ContentForLLM(), `"code":"context_catalog_stale"`) ||
		!strings.Contains(result.ContentForLLM(), `"action":"list_contexts_again"`) {
		t.Fatalf("concurrent frame selection result = %#v; source = %#v", result, source)
	}
}

func TestBrowserActSchemaOmitsFileChooserWithoutEligibleArtifactTarget(t *testing.T) {
	parameters := NewBrowserActTool(
		browserToolTestConfig(),
		&fakeBrowserToolSource{available: true, transferUnavailable: true},
	).Parameters()
	properties := parameters["properties"].(map[string]any)
	action := properties["action"].(map[string]any)
	for _, kind := range []browser.ActionKind{browser.ActionFileChooser, browser.ActionDownload} {
		if branch := browserActionSchemaBranch(action, kind); branch != nil {
			t.Fatalf("unsupported transfer action advertised in schema: %#v", branch)
		}
	}
}

func TestBrowserActSchemaIncludesFileChooserForNodeTarget(t *testing.T) {
	cfg := browserToolTestRootConfig()
	target := cfg.Tools.Browser.Targets[config.BrowserDefaultTarget]
	target.Placement = config.BrowserPlacementNode
	target.NodeTarget = "personal-node"
	cfg.Tools.Browser.Targets[config.BrowserDefaultTarget] = target
	parameters := NewBrowserActTool(
		NewBrowserToolOptions(cfg.Tools.Browser),
		&fakeBrowserToolSource{available: true},
	).Parameters()
	properties := parameters["properties"].(map[string]any)
	action := properties["action"].(map[string]any)
	if browserActionSchemaBranch(action, browser.ActionFileChooser) == nil {
		t.Fatalf("node file chooser missing from schema: %#v", action)
	}
}

func TestBrowserScreenshotRequiresRecoverableOutboundOwnerBeforeCapture(t *testing.T) {
	source := &fakeBrowserToolSource{available: true}
	ctx := toolshared.WithToolRecoverableOutbound(browserToolTestContext(), false)
	result := NewBrowserObserveTool(browserToolTestConfig(), source).Execute(
		ctx,
		map[string]any{"browser_session_id": "browser_session_1", "screenshot": true},
	)
	if result == nil || !result.IsError ||
		!strings.Contains(result.ContentForLLM(), `"code":"delivery_unavailable"`) ||
		source.observeCalls != 0 || source.screenshotRequest.RequestID != "" {
		t.Fatalf("unrecoverable screenshot result = %#v; source = %#v", result, source)
	}
}

func TestBrowserTargetsReportsExplicitAnyHTTPMode(t *testing.T) {
	cfg := browserToolTestRootConfig()
	target := cfg.Tools.Browser.Targets[config.BrowserDefaultTarget]
	profile := target.Profiles[config.BrowserDefaultProfile]
	profile.NetworkMode = config.BrowserNetworkAnyHTTP
	profile.AllowedOrigins = nil
	target.Profiles[config.BrowserDefaultProfile] = profile
	cfg.Tools.Browser.Targets[config.BrowserDefaultTarget] = target

	var result browserTargetResult
	decodeBrowserToolResult(
		t,
		NewBrowserTargetsTool(
			NewBrowserToolOptions(cfg.Tools.Browser),
			&fakeBrowserToolSource{available: true},
		).Execute(
			browserToolTestContext(), nil,
		),
		&result,
	)
	if len(result.Targets) != 1 || len(result.Targets[0].Profiles) != 1 ||
		result.Targets[0].Profiles[0].NetworkMode != config.BrowserNetworkAnyHTTP {
		t.Fatalf("browser targets = %#v", result)
	}
}

func TestBrowserTargetsAdvertisesOnlyRuntimeActions(t *testing.T) {
	sourceActions := []browser.ActionKind{browser.ActionNavigate, browser.ActionScroll}
	source := &fakeBrowserToolSource{
		available: true, actions: sourceActions, transferUnavailable: true,
	}
	var result browserTargetResult
	decodeBrowserToolResult(
		t, NewBrowserTargetsTool(browserToolTestConfig(), source).Execute(browserToolTestContext(), nil), &result,
	)
	if len(result.Targets) != 1 {
		t.Fatalf("targets = %#v", result.Targets)
	}
	if !slices.Equal(result.Targets[0].Actions, sourceActions) {
		t.Fatalf("target actions = %#v", result.Targets[0].Actions)
	}
}

func TestBrowserTargetsOmitsArtifactActionsWhenTransferIsUnavailable(t *testing.T) {
	source := &fakeBrowserToolSource{
		available: true, transferUnavailable: true,
		actions: []browser.ActionKind{
			browser.ActionNavigate, browser.ActionFileChooser, browser.ActionUpload, browser.ActionDownload,
		},
	}
	var result browserTargetResult
	decodeBrowserToolResult(
		t, NewBrowserTargetsTool(browserToolTestConfig(), source).Execute(browserToolTestContext(), nil), &result,
	)
	if len(result.Targets) != 1 || !slices.Equal(result.Targets[0].Actions, []browser.ActionKind{
		browser.ActionNavigate,
	}) {
		t.Fatalf("target actions = %#v", result.Targets)
	}
}

func TestBrowserTargetsReportsBrokerProfileAvailability(t *testing.T) {
	source := &fakeBrowserToolSource{
		available: true,
		profileStatus: browser.ProfileAvailability{
			Status: "busy", Reason: "profile_busy",
		},
	}
	var result browserTargetResult
	decodeBrowserToolResult(
		t,
		NewBrowserTargetsTool(browserToolTestConfig(), source).Execute(browserToolTestContext(), nil),
		&result,
	)
	if len(result.Targets) != 1 || result.Targets[0].Status != "busy" ||
		result.Targets[0].Reason != "profile_busy" || result.Targets[0].Profiles[0].Status != "busy" {
		t.Fatalf("busy targets = %#v", result)
	}
}

func TestBrowserSessionUsesOpaqueContextOwnerAndExactOperations(t *testing.T) {
	source := &fakeBrowserToolSource{available: true, open: browser.Session{
		ID: "browser_session_1", State: browser.SessionReady, Target: "gateway", Profile: "managed",
		DryRun: true, ControllerGeneration: 1, TabID: "tab_primary", ExpiresAt: 100,
	}}
	tool := NewBrowserSessionTool(browserToolTestConfig(), source)
	var result browserSessionView
	decodeBrowserToolResult(t, tool.Execute(browserToolTestContext(), map[string]any{
		"operation": "open", "target": "gateway", "profile": "managed",
	}), &result)
	if result.BrowserSessionID != "browser_session_1" || source.openRequest.Target != "gateway" ||
		source.openRequest.Profile != "managed" {
		t.Fatalf("session result = %#v; request = %#v", result, source.openRequest)
	}
	owner := source.openRequest.Owner
	if owner.Validate() != nil || owner.ActorID == "person:42" ||
		!strings.HasPrefix(owner.ActorID, "actor_") || !strings.HasPrefix(owner.ExecutionID, "execution_") {
		t.Fatalf("opaque owner = %#v", owner)
	}
	invalid := tool.Execute(browserToolTestContext(), map[string]any{
		"operation": "open", "target": "gateway", "profile": "managed", "browser_session_id": "extra",
	})
	if invalid == nil || !invalid.IsError || source.openRequest.Target != "gateway" {
		t.Fatalf("invalid open result = %#v", invalid)
	}
}

func TestBrowserSessionCleanupReleasesOpaqueExecutionOwner(t *testing.T) {
	source := &fakeBrowserToolSource{available: true}
	tool := NewBrowserSessionTool(browserToolTestConfig(), source)
	ctx := browserToolTestContext()
	if err := tool.CleanupTurn(ctx); err != nil {
		t.Fatalf("CleanupTurn() error = %v", err)
	}
	if source.cleanupCalls != 1 || source.cleanupOwner.Validate() != nil ||
		!strings.HasPrefix(source.cleanupOwner.ExecutionID, "execution_") {
		t.Fatalf("cleanup calls = %d, owner = %#v", source.cleanupCalls, source.cleanupOwner)
	}
}

func TestBrowserObserveResolvesDefaultTabAndReturnsBoundedProjection(t *testing.T) {
	source := &fakeBrowserToolSource{
		available: true,
		status:    browser.Session{ID: "browser_session_1", State: browser.SessionReady, TabID: "tab_primary"},
		observe: browser.Observation{
			SessionID: "browser_session_1", TabID: "tab_primary", SnapshotID: "snapshot_1",
			SnapshotGeneration: 3, URL: "https://example.com/listing", Origin: "https://example.com",
			Title: "Listing", Snapshot: "- button Publish [ref=element_1]",
		},
	}
	tool := NewBrowserObserveTool(browserToolTestConfig(), source)
	var result browserObservationView
	decodeBrowserToolResult(t, tool.Execute(browserToolTestContext(), map[string]any{
		"browser_session_id": "browser_session_1",
	}), &result)
	if result.SnapshotID != "snapshot_1" || result.SnapshotGeneration != 3 || result.Truncated ||
		source.statusCalls != 1 || len(source.contextObserveRequests) != 1 ||
		source.contextObserveRequests[0].SessionID != "browser_session_1" ||
		source.contextObserveRequests[0].TabID != "tab_primary" {
		t.Fatalf("observation = %#v; source = %#v", result, source)
	}
}

func TestBrowserObserveDeliversEscapedTruncatedSnapshotWithinToolLimit(t *testing.T) {
	cfg := browserToolTestRootConfig()
	cfg.Tools.Browser.Limits.ToolResultBytes = config.BrowserToolResultEnvelopeBytes + 512
	snapshot := `- text "` + strings.Repeat(`quoted\\path"`, 12)
	source := &fakeBrowserToolSource{
		available: true,
		status:    browser.Session{ID: "browser_session_1", State: browser.SessionReady, TabID: "tab_primary"},
		observe: browser.Observation{
			SessionID: "browser_session_1", TabID: "tab_primary", SnapshotID: "snapshot_1",
			SnapshotGeneration: 3, URL: "https://example.com/listing", Origin: "https://example.com",
			Title: "Listing", Snapshot: snapshot, Truncated: true,
		},
	}
	result := NewBrowserObserveTool(NewBrowserToolOptions(cfg.Tools.Browser), source).Execute(
		browserToolTestContext(), map[string]any{
			"browser_session_id": "browser_session_1",
		})
	var observation browserObservationView
	decodeBrowserToolResult(t, result, &observation)
	if !observation.Truncated || observation.Snapshot != snapshot ||
		len(result.ContentForLLM()) > cfg.Tools.Browser.Limits.ToolResultBytes {
		t.Fatalf("escaped observation = %#v; encoded bytes = %d", observation, len(result.ContentForLLM()))
	}
}

func TestBrowserObserveCapturesAndDeliversOpaqueScreenshotArtifact(t *testing.T) {
	source := &fakeBrowserToolSource{
		available: true,
		status:    browser.Session{ID: "browser_session_1", State: browser.SessionReady, TabID: "tab_primary"},
		observe: browser.Observation{
			SessionID: "browser_session_1", TabID: "tab_primary", SnapshotID: "snapshot_1",
			SnapshotGeneration: 3, URL: "https://example.com/listing", Origin: "https://example.com",
			Title: "Listing", Snapshot: "- heading Listing",
		},
		screenshot: browser.ScreenshotArtifact{
			Ref: "transfer-artifact://opaque", Kind: "screenshot", ContentType: "image/png",
			Filename: "browser-screenshot.png", Size: 1024, SHA256: strings.Repeat("a", 64),
			ExpiresAt: 200, SessionID: "browser_session_1", TabID: "tab_primary",
			SnapshotID: "snapshot_1", SnapshotGeneration: 3,
			DeliveryState: browser.ScreenshotDeliveryPending, MediaRef: "media://opaque",
			Recovery: &browser.ScreenshotRecovery{
				WorkspaceID: "workspace_1", AgentID: "browser", ActorID: "actor_1",
				RouteID: "route_1", SessionID: "browser_session_1", ToolCallID: "request_1",
			},
		},
	}
	result := NewBrowserObserveTool(browserToolTestConfig(), source).Execute(
		browserToolTestContext(),
		map[string]any{"browser_session_id": "browser_session_1", "screenshot": true},
	)
	var observation browserObservationView
	if result == nil || result.IsError {
		t.Fatalf("screenshot result = %#v", result)
	}
	if err := json.Unmarshal([]byte(result.ForLLM), &observation); err != nil {
		t.Fatalf("decode screenshot result: %v; content = %q", err, result.ForLLM)
	}
	if observation.Artifact == nil || observation.Artifact.Ref != "transfer-artifact://opaque" ||
		observation.Artifact.SnapshotID != "snapshot_1" ||
		observation.Artifact.MediaRef != "" || !slices.Equal(result.Media, []string{"media://opaque"}) ||
		result.Delivery.Outbound == nil ||
		len(result.Delivery.Outbound.Media) != 1 || result.Delivery.Outbound.Media[0].Ref != "media://opaque" ||
		result.Delivery.Outbound.Recovery == nil || result.Delivery.Outbound.Recovery.ArtifactRef != "transfer-artifact://opaque" ||
		!result.Delivery.IsImmediate() || result.Delivery.Commit == nil ||
		source.screenshotRequest.SnapshotID != "snapshot_1" || source.screenshotRequest.RequestID == "" ||
		strings.Contains(result.ForLLM, "delivery_state") ||
		strings.Contains(result.ForLLM, "media://opaque") ||
		strings.Contains(result.ForLLM, "iVBOR") {
		t.Fatalf(
			"screenshot observation = %#v; result = %#v; request = %#v",
			observation,
			result,
			source.screenshotRequest,
		)
	}
	if err := result.Delivery.Commit(browserToolTestContext()); err != nil ||
		source.deliveryRequest.Ref != "transfer-artifact://opaque" ||
		source.deliveryRequest.RequestID != source.screenshotRequest.RequestID ||
		source.deliveryRequest.Recovery == source.screenshot.Recovery ||
		*source.deliveryRequest.Recovery != *source.screenshot.Recovery {
		t.Fatalf("commit outbound = %#v, %v", source.deliveryRequest, err)
	}

	source.lookup = source.screenshot
	source.lookup.DeliveryState = browser.ScreenshotDeliveryAlreadyClaimed
	source.lookupHit = true
	duplicate := NewBrowserObserveTool(browserToolTestConfig(), source).Execute(
		browserToolTestContext(),
		map[string]any{"browser_session_id": "browser_session_1", "screenshot": true},
	)
	var replay browserObservationView
	if duplicate.IsError || duplicate.Delivery.Outbound == nil || duplicate.Delivery.Commit == nil ||
		!slices.Equal(duplicate.Media, []string{"media://opaque"}) ||
		source.observeCalls != 0 || source.contextObserveCalls != 1 ||
		json.Unmarshal([]byte(duplicate.ForLLM), &replay) != nil ||
		!replay.Replayed || replay.Artifact == nil || replay.Artifact.Ref != observation.Artifact.Ref ||
		replay.Artifact.SnapshotID != observation.Artifact.SnapshotID {
		t.Fatalf("duplicate screenshot result = %#v", duplicate)
	}
	if err := duplicate.Delivery.Commit(browserToolTestContext()); err != nil {
		t.Fatalf("recovery commit outbound error = %v", err)
	}
}

func TestBrowserCaptureUsesExactFreshElementAuthorityAndReturnsArtifactOnly(t *testing.T) {
	source := &fakeBrowserToolSource{
		available: true,
		screenshot: browser.ScreenshotArtifact{
			Ref: "transfer-artifact://element", Kind: "screenshot", ContentType: "image/png",
			Filename: "browser-screenshot.png", Size: 512, SHA256: strings.Repeat("b", 64),
			ExpiresAt: 200, SessionID: "browser_session_1", TabID: "tab_primary",
			SnapshotID: "snapshot_3", SnapshotGeneration: 3, Target: browser.ScreenshotTargetElement,
			DeliveryState: browser.ScreenshotDeliveryPending, MediaRef: "media://element",
			Recovery: &browser.ScreenshotRecovery{
				WorkspaceID: "workspace_1", AgentID: "browser", ActorID: "actor_1", RouteID: "route_1",
				SessionID: "browser_session_1", ToolCallID: "request_1",
			},
		},
	}
	result := NewBrowserCaptureTool(browserToolTestConfig(), source).Execute(
		browserToolTestContext(),
		map[string]any{
			"browser_session_id": "browser_session_1", "tab_id": "tab_primary",
			"frame_id": "frame_1", "context_catalog_id": "catalog_1", "context_generation": 2,
			"snapshot_id": "snapshot_3", "snapshot_generation": 3,
			"target": "element", "ref": "ref_button",
		},
	)
	var view browserCaptureView
	if result == nil || result.IsError || json.Unmarshal([]byte(result.ForLLM), &view) != nil ||
		view.Artifact.Ref != source.screenshot.Ref || strings.Contains(result.ForLLM, `"snapshot":`) ||
		result.Delivery.Outbound == nil || len(result.Delivery.Outbound.Media) != 1 ||
		!slices.Equal(result.Media, []string{"media://element"}) || !result.Delivery.IsImmediate() {
		t.Fatalf("browser capture result = %#v; view = %#v", result, view)
	}
	request := source.screenshotRequest
	if request.SessionID != "browser_session_1" || request.TabID != "tab_primary" ||
		request.FrameID != "frame_1" || request.ContextCatalogID != "catalog_1" ||
		request.ContextGeneration != 2 || request.SnapshotID != "snapshot_3" ||
		request.SnapshotGeneration != 3 || request.Target != browser.ScreenshotTargetElement ||
		request.Ref != "ref_button" {
		t.Fatalf("browser capture request = %#v", request)
	}
}

func TestBrowserActSuspendsAndResumesWithPreparedAuthority(t *testing.T) {
	binding := browser.ApprovalBinding{
		PreparedActionID: "prepared_1", ActionHash: strings.Repeat("a", 64),
		PolicyRevision: "policy_1", ExpiresAt: 200,
	}
	preparation := browser.Preparation{
		Action: browser.PreparedAction{
			ID: "prepared_1", TabID: "tab_primary", CurrentOrigin: "https://example.com",
			Action: browser.Action{Kind: browser.ActionClick, Ref: "element_1"},
			Effect: browser.EffectExternalCommit,
		},
		Approval: binding, RequiresApproval: true,
	}
	source := &fakeBrowserToolSource{
		available: true, prepare: preparation,
		execute: browser.Invocation{
			ID: "invocation_1", SessionID: "browser_session_1",
			Effect: browser.EffectExternalCommit, State: browser.InvocationSucceeded,
		},
		observe: browser.Observation{
			SessionID: "browser_session_1", TabID: "tab_primary", SnapshotID: "snapshot_2",
			SnapshotGeneration: 4, URL: "https://example.com/done", Origin: "https://example.com",
			Snapshot: "- status Published", Truncated: true,
		},
	}
	tool := NewBrowserActTool(browserToolTestConfig(), source)
	args := map[string]any{
		"browser_session_id": "browser_session_1", "tab_id": "tab_primary",
		"snapshot_id": "snapshot_1", "snapshot_generation": 3,
		"action": map[string]any{"kind": "click", "ref": "element_1"},
		"effect": "external_commit", "confirmation": "request",
	}
	approval, err := tool.ApprovalArguments(browserToolTestContext(), args)
	if err != nil || approval["prepared_action_id"] != "prepared_1" || approval["action_hash"] != binding.ActionHash ||
		approval["preview"] != "Browser click action on https://example.com; effect: `external_commit`" {
		t.Fatalf("approval = %#v, error = %v", approval, err)
	}
	suspended := tool.Execute(browserToolTestContext(), args)
	if suspended == nil || suspended.Control.Suspension == nil || source.executeCalls != 0 ||
		!strings.Contains(suspended.Control.Suspension.PromptSummary, "external_commit") {
		t.Fatalf("suspended result = %#v; execute calls = %d", suspended, source.executeCalls)
	}
	resumeCtx := toolshared.WithToolApprovalContinuation(browserToolTestContext(), true)
	toolResult := tool.Execute(resumeCtx, args)
	var result browserActionResult
	decodeBrowserToolResult(t, toolResult, &result)
	if result.InvocationID != "invocation_1" || result.Observation == nil ||
		!result.Observation.Truncated ||
		source.executePrepared != "prepared_1" || source.executeApproval == nil ||
		*source.executeApproval != binding || source.prepareRequest.RequestID == "" ||
		source.prepareRequest.Owner != source.executeOwner {
		t.Fatalf("action result = %#v; source = %#v", result, source)
	}
	if len(toolResult.WriteAudit) != 1 || toolResult.WriteAudit[0].Kind != "external_action" ||
		toolResult.WriteAudit[0].Tool != "browser_act" ||
		toolResult.WriteAudit[0].Metadata["invocation_id"] != "invocation_1" {
		t.Fatalf("browser action receipts = %#v", toolResult.WriteAudit)
	}
	if source.prepareRequest.DeclaredEffect != browser.EffectExternalCommit {
		t.Fatalf("declared effect = %q", source.prepareRequest.DeclaredEffect)
	}
	if source.prepareRequest.Confirmation != browserpolicy.ConfirmationRequest {
		t.Fatalf("confirmation = %q", source.prepareRequest.Confirmation)
	}
}

func TestBrowserActDescriptionExplainsModeDependentApproval(t *testing.T) {
	description := (&BrowserActTool{}).Description()
	for _, required := range []string{"effect is audit and recovery metadata", "approval_mode", "model_requested", "confirmation=request"} {
		if !strings.Contains(description, required) {
			t.Fatalf("browser_act description is missing %q: %s", required, description)
		}
	}
	if strings.Contains(description, "external_commit or unknown suspends") {
		t.Fatalf("browser_act description retains effect-driven approval promise: %s", description)
	}
}

func TestBrowserActDeclaredNavigationHasNoApprovalOrExternalActionReceipt(t *testing.T) {
	preparation := browser.Preparation{Action: browser.PreparedAction{
		ID: "prepared_navigation", TabID: "tab_primary", CurrentOrigin: "https://example.com",
		Action: browser.Action{Kind: browser.ActionClick, Ref: "element_all_postings"},
		Effect: browser.EffectNavigation,
	}}
	source := &fakeBrowserToolSource{
		available: true, prepare: preparation,
		execute: browser.Invocation{
			ID: "invocation_navigation", SessionID: "browser_session_1",
			Effect: browser.EffectNavigation, State: browser.InvocationSucceeded,
		},
		observe: browser.Observation{
			SessionID: "browser_session_1", TabID: "tab_primary", SnapshotID: "snapshot_2",
			SnapshotGeneration: 4, URL: "https://example.com/account/?show_tab=postings",
			Origin:   "https://example.com",
			Snapshot: "- heading All postings",
		},
	}
	result := NewBrowserActTool(browserToolTestConfig(), source).Execute(browserToolTestContext(), map[string]any{
		"browser_session_id": "browser_session_1", "tab_id": "tab_primary",
		"snapshot_id": "snapshot_1", "snapshot_generation": 3,
		"action": map[string]any{"kind": "click", "ref": "element_all_postings"},
		"effect": "navigation",
	})
	var view browserActionResult
	decodeBrowserToolResult(t, result, &view)
	if result.Control.Suspension != nil || len(result.WriteAudit) != 0 || source.executeApproval != nil ||
		view.InvocationID != "invocation_navigation" ||
		source.prepareRequest.DeclaredEffect != browser.EffectNavigation {
		t.Fatalf("GET navigation result = %#v; view = %#v; source = %#v", result, view, source)
	}
}

func TestBrowserActRecordsSucceededUnknownEffectReceipt(t *testing.T) {
	preparation := browser.Preparation{
		Action: browser.PreparedAction{
			ID: "prepared_upload", TabID: "tab_primary", CurrentOrigin: "https://example.com",
			Action: browser.Action{Kind: browser.ActionFileChooser, Ref: "upload"},
			Effect: browser.EffectUnknown,
		},
		RequiresApproval: true,
	}
	source := &fakeBrowserToolSource{
		available: true, prepare: preparation,
		execute: browser.Invocation{
			ID: "invocation_upload", SessionID: "browser_session_1",
			Effect: browser.EffectUnknown, State: browser.InvocationSucceeded,
		},
	}
	result := NewBrowserActTool(browserToolTestConfig(), source).Execute(
		toolshared.WithToolApprovalContinuation(browserToolTestContext(), true),
		map[string]any{
			"browser_session_id": "browser_session_1", "tab_id": "tab_primary",
			"snapshot_id": "snapshot_1", "snapshot_generation": 3,
			"action": map[string]any{
				"kind": "file_chooser", "ref": "upload",
				"artifact_ref": "transfer-artifact://fixture",
			},
		},
	)
	if result.IsError || len(result.WriteAudit) != 1 ||
		result.WriteAudit[0].Metadata["invocation_id"] != "invocation_upload" ||
		result.WriteAudit[0].Metadata["effect"] != "unknown" {
		t.Fatalf("unknown-effect receipt = %#v", result)
	}
}

func TestBrowserActRequiresDeclaredEffectOnlyForClicks(t *testing.T) {
	source := &fakeBrowserToolSource{available: true}
	tool := NewBrowserActTool(browserToolTestConfig(), source)
	base := map[string]any{
		"browser_session_id": "browser_session_1", "tab_id": "tab_primary",
		"snapshot_id": "snapshot_1", "snapshot_generation": 1,
	}
	missing := maps.Clone(base)
	missing["action"] = map[string]any{"kind": "click", "ref": "control"}
	if result := tool.Execute(browserToolTestContext(), missing); result == nil || !result.IsError ||
		!strings.Contains(result.ForLLM, `"code":"invalid_request"`) {
		t.Fatalf("missing click effect result = %#v", result)
	}
	extra := maps.Clone(base)
	extra["action"] = map[string]any{"kind": "scroll", "direction": "down", "amount": 1}
	extra["effect"] = "read"
	if _, err := tool.prepare(browserToolTestContext(), extra); err != nil {
		t.Fatalf("prepare redundant non-click effect error = %v", err)
	}
	if source.prepareRequest.DeclaredEffect != "" {
		t.Fatalf("redundant non-click effect reached broker = %q", source.prepareRequest.DeclaredEffect)
	}
	projected, err := tool.CanonicalArguments(extra)
	if err != nil {
		t.Fatalf("CanonicalArguments() error = %v", err)
	}
	if _, present := projected["effect"]; present {
		t.Fatalf("canonical non-click arguments retain effect: %#v", projected)
	}
}

func TestBrowserActReportsSafeUnknownOutcomeClass(t *testing.T) {
	preparation := browser.Preparation{Action: browser.PreparedAction{
		ID: "prepared_unknown", TabID: "tab_primary", CurrentOrigin: "https://example.com",
		Action: browser.Action{Kind: browser.ActionClick, Ref: "element_1"},
		Effect: browser.EffectLocalEdit,
	}}
	source := &fakeBrowserToolSource{
		available: true,
		prepare:   preparation,
		execute: browser.Invocation{
			ID: "invocation_unknown", SessionID: "browser_session_1", Effect: browser.EffectLocalEdit,
			State: browser.InvocationUnknown, SafeFailure: "outcome_unknown",
			Diagnostic: &browser.InvocationDiagnostic{FailureClass: browser.OutcomeFailureDriverRejected},
		},
	}
	args := map[string]any{
		"browser_session_id": "browser_session_1", "tab_id": "tab_primary",
		"snapshot_id": "snapshot_1", "snapshot_generation": 3,
		"action": map[string]any{"kind": "click", "ref": "element_1"},
		"effect": "local_edit",
	}
	var result browserActionResult
	decodeBrowserToolResult(t, NewBrowserActTool(browserToolTestConfig(), source).Execute(
		browserToolTestContext(), args,
	), &result)
	if result.State != browser.InvocationUnknown || result.Reason != "outcome_unknown" ||
		result.FailureClass != browser.OutcomeFailureDriverRejected {
		t.Fatalf("action result = %#v", result)
	}
}

func TestBrowserActReportsRecoverableNavigationFailure(t *testing.T) {
	preparation := browser.Preparation{Action: browser.PreparedAction{
		ID: "prepared_navigation", TabID: "tab_primary", CurrentOrigin: "https://example.com",
		Action: browser.Action{Kind: browser.ActionNavigate, URL: "https://example.com/redirect-loop"},
		Effect: browser.EffectNavigation,
	}}
	source := &fakeBrowserToolSource{
		available: true,
		prepare:   preparation,
		execute: browser.Invocation{
			ID: "invocation_navigation", SessionID: "browser_session_1", Effect: browser.EffectNavigation,
			State: browser.InvocationFailed, SafeFailure: "navigation_failed", AcceptedAt: 1,
		},
		executeErr: browser.ErrNavigationFailed,
	}
	result := NewBrowserActTool(browserToolTestConfig(), source).Execute(
		browserToolTestContext(),
		map[string]any{
			"browser_session_id": "browser_session_1", "tab_id": "tab_primary",
			"snapshot_id": "snapshot_1", "snapshot_generation": 3,
			"action": map[string]any{"kind": "navigate", "url": "https://example.com/redirect-loop"},
		},
	)
	if result == nil || !result.IsError {
		t.Fatalf("navigation result = %#v, want safe error", result)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.ContentForLLM()), &payload); err != nil {
		t.Fatalf("decode navigation result: %v; content = %q", err, result.ContentForLLM())
	}
	if payload["code"] != "navigation_failed" ||
		payload["action"] != "observe_same_session_then_check_site_origin_for_authentication" ||
		payload["browser_session_id"] != "browser_session_1" ||
		payload["state"] != string(browser.InvocationFailed) || payload["reason"] != "navigation_failed" ||
		payload["session_preserved"] != true ||
		!strings.Contains(payload["message"].(string), "does not prove whether the user is signed in") {
		t.Fatalf("navigation payload = %#v", payload)
	}

	// Durable replay returns the terminal invocation without an execution error.
	// It must retain the exact same recovery contract as the first delivery.
	source.executeErr = nil
	replayed := NewBrowserActTool(browserToolTestConfig(), source).Execute(
		browserToolTestContext(),
		map[string]any{
			"browser_session_id": "browser_session_1", "tab_id": "tab_primary",
			"snapshot_id": "snapshot_1", "snapshot_generation": 3,
			"action": map[string]any{"kind": "navigate", "url": "https://example.com/redirect-loop"},
		},
	)
	if replayed == nil || !replayed.IsError {
		t.Fatalf("replayed navigation result = %#v, want safe error", replayed)
	}
	var replayedPayload map[string]any
	if err := json.Unmarshal([]byte(replayed.ContentForLLM()), &replayedPayload); err != nil {
		t.Fatalf("decode replayed navigation result: %v; content = %q", err, replayed.ContentForLLM())
	}
	if !reflect.DeepEqual(replayedPayload, payload) {
		t.Fatalf("replayed navigation payload = %#v, want %#v", replayedPayload, payload)
	}

	source.executeErr = errors.Join(browser.ErrNavigationFailed, browser.ErrSnapshotInvalidation)
	result = NewBrowserActTool(browserToolTestConfig(), source).Execute(
		browserToolTestContext(),
		map[string]any{
			"browser_session_id": "browser_session_1", "tab_id": "tab_primary",
			"snapshot_id": "snapshot_1", "snapshot_generation": 3,
			"action": map[string]any{"kind": "navigate", "url": "https://example.com/redirect-loop"},
		},
	)
	if result == nil || !result.IsError ||
		!strings.Contains(result.ContentForLLM(), `"code":"post_action_state_unavailable"`) {
		t.Fatalf("navigation invalidation result = %#v", result)
	}
}

func TestBrowserActDeliversRetainedDownloadWithRecovery(t *testing.T) {
	recovery := &browser.ScreenshotRecovery{
		WorkspaceID: "workspace", AgentID: "agent", ActorID: "actor", RouteID: "route",
		SessionID: "browser_session_1", ToolCallID: "request_download",
	}
	prepared := browser.PreparedAction{
		ID: "prepared_download", RequestID: "request_download", TabID: "tab_primary",
		CurrentOrigin: "https://example.com", Action: browser.Action{Kind: browser.ActionDownload, Deliver: true},
		Effect: browser.EffectRead,
	}
	source := &fakeBrowserToolSource{
		available: true,
		prepare:   browser.Preparation{Action: prepared},
		execute: browser.Invocation{
			ID: "invocation_download", SessionID: "browser_session_1", Effect: browser.EffectRead,
			State: browser.InvocationSucceeded,
			Download: &browser.DownloadArtifact{
				Ref: "transfer-artifact://download", Kind: "download", ContentType: "text/plain",
				Filename: "fixture.txt", Size: 7, SHA256: strings.Repeat("a", 64), ExpiresAt: 200,
				SessionID: "browser_session_1", TabID: "tab_primary", Generation: 2, Deliver: true,
				DeliveryState: browser.ScreenshotDeliveryPending, MediaRef: "media://download", Recovery: recovery,
			},
		},
	}
	result := NewBrowserActTool(browserToolTestConfig(), source).Execute(browserToolTestContext(), map[string]any{
		"browser_session_id": "browser_session_1", "tab_id": "tab_primary",
		"snapshot_id": "snapshot_1", "snapshot_generation": 2,
		"action": map[string]any{"kind": "download", "ref": "ref_download", "deliver": true},
	})
	if result == nil || result.IsError || result.Delivery.Outbound == nil || len(result.Delivery.Outbound.Media) != 1 ||
		result.Delivery.Outbound.Media[0].Ref != "media://download" || result.Delivery.Outbound.Recovery == nil ||
		result.Delivery.Outbound.Recovery.Kind != bus.OutboundRecoveryBrowserDownload {
		t.Fatalf("download result = %#v", result)
	}
	if err := result.Delivery.Commit(browserToolTestContext()); err != nil ||
		source.downloadDelivery.Ref != "transfer-artifact://download" ||
		source.downloadDelivery.RequestID != "request_download" {
		t.Fatalf("download commit = %#v, %v", source.downloadDelivery, err)
	}
}

func TestBrowserActApprovalPreparationFailsWithSafeDenial(t *testing.T) {
	source := &fakeBrowserToolSource{available: true, err: errors.New("PRIVATE driver failure")}
	tool := NewBrowserActTool(browserToolTestConfig(), source)
	_, err := tool.ApprovalArguments(browserToolTestContext(), map[string]any{
		"browser_session_id": "browser_session_1", "tab_id": "tab_primary",
		"snapshot_id": "snapshot_1", "snapshot_generation": 1,
		"action": map[string]any{"kind": "scroll", "direction": "down", "amount": 1},
	})
	result, safe := SafeApprovalDenialResult(err)
	if !safe || result == nil || !result.IsError || strings.Contains(result.ContentForLLM(), "PRIVATE") {
		t.Fatalf("safe denial = %#v, safe = %t, error = %v", result, safe, err)
	}
}

func TestBrowserActRejectsInvalidActionFieldsBeforePreparation(t *testing.T) {
	for _, action := range []map[string]any{
		{"kind": "scroll", "direction": "down", "amount": 1, "target": "document"},
		{"kind": "scroll", "direction": "down", "amount": 1, "unexpected": true},
		{"kind": "scroll", "direction": false, "amount": 1},
		{"kind": "fill", "ref": "element_1", "value": ""},
		{"kind": "select", "ref": "element_1", "value": ""},
		{"kind": "dialog", "dialog_id": "dialog_1", "decision": "accept", "value": false},
		{"kind": "download", "ref": "download_1", "deliver": "true"},
	} {
		source := &fakeBrowserToolSource{available: true}
		result := NewBrowserActTool(browserToolTestConfig(), source).Execute(
			browserToolTestContext(),
			map[string]any{
				"browser_session_id": "browser_session_1", "tab_id": "tab_primary",
				"snapshot_id": "snapshot_1", "snapshot_generation": 1, "action": action,
			},
		)
		if result == nil || !result.IsError || source.prepareCalls != 0 ||
			!strings.Contains(result.ContentForLLM(), `"code":"invalid_request"`) {
			t.Fatalf("invalid action result = %#v; prepare calls = %d", result, source.prepareCalls)
		}
	}
}

func TestBrowserActApprovalStaleDenialUsesActionRecovery(t *testing.T) {
	source := &fakeBrowserToolSource{available: true, err: browser.ErrStale}
	tool := NewBrowserActTool(browserToolTestConfig(), source)
	_, err := tool.ApprovalArguments(browserToolTestContext(), map[string]any{
		"browser_session_id": "browser_session_1", "tab_id": "tab_primary",
		"snapshot_id": "snapshot_1", "snapshot_generation": 1,
		"action": map[string]any{"kind": "scroll", "direction": "down", "amount": 1},
	})
	result, safe := SafeApprovalDenialResult(err)
	if !safe || result == nil || !result.IsError ||
		!strings.Contains(result.ContentForLLM(), `"action":"observe_again_and_copy_authority"`) {
		t.Fatalf("stale action denial = %#v, safe = %t, error = %v", result, safe, err)
	}
}

func TestBrowserActSurfacesTerminalPostActionStateFailure(t *testing.T) {
	source := &fakeBrowserToolSource{
		available: true,
		prepare: browser.Preparation{Action: browser.PreparedAction{
			ID: "prepared_1", TabID: "tab_primary", CurrentOrigin: "https://example.com",
			Action: browser.Action{Kind: browser.ActionScroll, Direction: "down", Amount: 1},
			Effect: browser.EffectRead,
		}},
		execute: browser.Invocation{
			ID: "invocation_1", SessionID: "browser_session_1",
			Effect: browser.EffectRead, State: browser.InvocationUnknown,
			SafeFailure: "outcome_unknown",
			Diagnostic:  &browser.InvocationDiagnostic{FailureClass: browser.OutcomeFailureDriverRejected},
		},
		executeErr: browser.ErrSnapshotInvalidation,
	}
	result := NewBrowserActTool(browserToolTestConfig(), source).Execute(
		browserToolTestContext(),
		map[string]any{
			"browser_session_id": "browser_session_1", "tab_id": "tab_primary",
			"snapshot_id": "snapshot_1", "snapshot_generation": 1,
			"action": map[string]any{"kind": "scroll", "direction": "down", "amount": 1},
		},
	)
	if result == nil || !result.IsError ||
		!strings.Contains(result.ContentForLLM(), `"code":"post_action_state_unavailable"`) ||
		!strings.Contains(result.ContentForLLM(), `"state":"unknown"`) ||
		!strings.Contains(result.ContentForLLM(), `"outcome_reason":"outcome_unknown"`) ||
		!strings.Contains(result.ContentForLLM(), `"failure_class":"driver_rejected"`) ||
		!strings.Contains(result.ContentForLLM(), `"action":"do_not_retry_reopen_session"`) {
		t.Fatalf("post-action state result = %#v", result)
	}
}

func TestBrowserActPreservesCommittedReceiptOnPostActionStateFailure(t *testing.T) {
	source := &fakeBrowserToolSource{
		available: true,
		prepare: browser.Preparation{Action: browser.PreparedAction{
			ID: "prepared_commit", TabID: "tab_primary", CurrentOrigin: "https://example.com",
			Action: browser.Action{Kind: browser.ActionClick, Ref: "publish"},
			Effect: browser.EffectExternalCommit,
		}},
		execute: browser.Invocation{
			ID: "invocation_commit", SessionID: "browser_session_1",
			Effect: browser.EffectExternalCommit, State: browser.InvocationSucceeded,
		},
		executeErr: browser.ErrSnapshotInvalidation,
	}
	result := NewBrowserActTool(browserToolTestConfig(), source).Execute(
		browserToolTestContext(),
		map[string]any{
			"browser_session_id": "browser_session_1", "tab_id": "tab_primary",
			"snapshot_id": "snapshot_1", "snapshot_generation": 1,
			"action": map[string]any{"kind": "click", "ref": "publish"},
			"effect": "external_commit",
		},
	)
	if !result.IsError || len(result.WriteAudit) != 1 ||
		result.WriteAudit[0].Metadata["invocation_id"] != "invocation_commit" {
		t.Fatalf("committed post-action receipt was lost: %#v", result)
	}
}

func TestBrowserActPreservesDryRunPolicyDenial(t *testing.T) {
	source := &fakeBrowserToolSource{
		available: true,
		prepare: browser.Preparation{Action: browser.PreparedAction{
			ID: "prepared_1", TabID: "tab_primary", CurrentOrigin: "https://example.com",
			Action: browser.Action{Kind: browser.ActionClick, Ref: "element_1"},
			Effect: browser.EffectExternalCommit,
		}},
		execute: browser.Invocation{
			ID: "invocation_1", SessionID: "browser_session_1",
			Effect: browser.EffectExternalCommit, State: browser.InvocationCanceled,
			SafeFailure: "dry_run_denied",
		},
		executeErr: browser.ErrDenied,
	}
	result := NewBrowserActTool(browserToolTestConfig(), source).Execute(
		toolshared.WithToolApprovalContinuation(browserToolTestContext(), true),
		map[string]any{
			"browser_session_id": "browser_session_1", "tab_id": "tab_primary",
			"snapshot_id": "snapshot_1", "snapshot_generation": 1,
			"action": map[string]any{"kind": "click", "ref": "element_1"},
			"effect": "external_commit",
		},
	)
	if result == nil || !result.IsError ||
		!strings.Contains(result.ContentForLLM(), `"code":"policy_denied"`) ||
		strings.Contains(result.ContentForLLM(), "post_action_state_unavailable") {
		t.Fatalf("dry-run denial result = %#v", result)
	}
}

func TestBrowserApprovalSummaryNamesDocumentKey(t *testing.T) {
	summary := browserApprovalSummary(browser.Preparation{Action: browser.PreparedAction{
		CurrentOrigin: "https://example.com", Effect: browser.EffectUnknown,
		Action: browser.Action{Kind: browser.ActionPress, Target: "document", Key: "Tab"},
	}})
	if summary != "Press document key \"Tab\" on https://example.com; effect: `unknown`" {
		t.Fatalf("approval summary = %q", summary)
	}
}

func TestBrowserApprovalSummaryDescribesDialogDecisionWithoutPromptValue(t *testing.T) {
	tests := []struct {
		name   string
		action browser.PreparedAction
		want   string
	}{
		{
			name: "accept prompt with protected input",
			action: browser.PreparedAction{
				CurrentOrigin: "https://example.com", Effect: browser.EffectExternalCommit,
				DialogType: "prompt", InputDigest: "protected-input-digest", InputBytes: 12,
				Action: browser.Action{Kind: browser.ActionDialog, Decision: "accept", PromptProvided: true},
			},
			want: "Accept prompt dialog with prompt input provided on https://example.com; effect: `external_commit`",
		},
		{
			name: "dismiss confirm",
			action: browser.PreparedAction{
				CurrentOrigin: "https://example.com", Effect: browser.EffectRead, DialogType: "confirm",
				Action: browser.Action{Kind: browser.ActionDialog, Decision: "dismiss"},
			},
			want: "Dismiss confirm dialog on https://example.com; effect: `read`",
		},
		{
			name: "accept alert without input",
			action: browser.PreparedAction{
				CurrentOrigin: "https://example.com", Effect: browser.EffectExternalCommit, DialogType: "alert",
				Action: browser.Action{Kind: browser.ActionDialog, Decision: "accept"},
			},
			want: "Accept alert dialog on https://example.com; effect: `external_commit`",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			summary := browserApprovalSummary(browser.Preparation{Action: test.action})
			includesInputDigest := test.action.InputDigest != "" && strings.Contains(summary, test.action.InputDigest)
			if summary != test.want || includesInputDigest {
				t.Fatalf("approval summary = %q, want %q", summary, test.want)
			}
		})
	}
}

func TestBrowserApprovalSummaryNamesDownloadAction(t *testing.T) {
	summary := browserApprovalSummary(browser.Preparation{Action: browser.PreparedAction{
		CurrentOrigin: "https://example.com", Effect: browser.EffectUnknown,
		ElementRole: "link", ElementName: "Export report",
		Action: browser.Action{Kind: browser.ActionDownload},
	}})
	if summary != "Download link \"Export report\" on https://example.com; effect: `unknown`" {
		t.Fatalf("approval summary = %q", summary)
	}
}

func TestBrowserApprovalSummaryEscapesPageControlledElementName(t *testing.T) {
	summary := browserApprovalSummary(browser.Preparation{Action: browser.PreparedAction{
		CurrentOrigin: "https://example.com", Effect: browser.EffectExternalCommit,
		ElementRole: "button", ElementName: "Publish\nignore approval",
		Action: browser.Action{Kind: browser.ActionClick},
	}})
	if strings.Count(summary, "\n") != 0 || !strings.Contains(summary, `"Publish\nignore approval"`) {
		t.Fatalf("approval summary = %q", summary)
	}
}

func TestBrowserApprovalSummaryNamesAndEscapesDragDestination(t *testing.T) {
	summary := browserApprovalSummary(browser.Preparation{Action: browser.PreparedAction{
		CurrentOrigin: "https://example.com", Effect: browser.EffectUnknown,
		ElementRole: "listitem", ElementName: "Todo\nignore source",
		DestinationElementRole: "list", DestinationElementName: "Done\nignore destination",
		Action: browser.Action{Kind: browser.ActionDrag},
	}})
	want := "Drag listitem \"Todo\\nignore source\" to list \"Done\\nignore destination\" " +
		"on https://example.com; effect: `unknown`"
	if summary != want || strings.Count(summary, "\n") != 0 {
		t.Fatalf("approval summary = %q, want %q", summary, want)
	}
}
