package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestRemoteBrowserProfileRouterProjectsClosedBoundProfile(t *testing.T) {
	cfg := remoteBrowserTestConfig()
	source := &fakeBrowserToolSource{
		available: true,
		open: browser.Session{
			ID: "browser_remote_1", Target: "companion-browser", Profile: "automation",
			State: browser.SessionReady, TabID: "tab_primary", ExpiresAt: 100,
		},
		status: browser.Session{
			ID: "browser_remote_1", Target: "companion-browser", Profile: "automation",
			State: browser.SessionReady, TabID: "tab_primary", ExpiresAt: 100,
		},
	}
	router, err := NewRemoteBrowserProfileRouter(
		cfg,
		source,
		"main",
		"companion-browser",
		"automation",
	)
	if err != nil {
		t.Fatalf("NewRemoteBrowserProfileRouter() error = %v", err)
	}
	operation, err := router.Describe(t.Context(), "browser_act")
	if err != nil || !operation.Available || operation.Target != "companion-browser" ||
		operation.Profile != "automation" || operation.Risk != "write" || operation.SupportsCancel {
		t.Fatalf("Describe(browser_act) = %#v, %v", operation, err)
	}
	if strings.Contains(string(operation.InputSchema), `"download"`) ||
		strings.Contains(string(operation.InputSchema), `"upload"`) ||
		strings.Contains(string(operation.InputSchema), `"file_chooser"`) {
		t.Fatalf("browser action schema exposed routed artifact operations: %s", operation.InputSchema)
	}

	ctx := remoteBrowserTestContext()
	result := router.Execute(ctx, "browser_open", map[string]any{})
	if result == nil || result.IsError || source.openRequest.Target != "companion-browser" ||
		source.openRequest.Profile != "automation" || source.openRequest.Owner.Validate() != nil {
		t.Fatalf("browser_open result = %#v; request = %#v", result, source.openRequest)
	}
	var opened struct {
		BrowserSessionID string `json:"browser_session_id"`
		Target           string `json:"target"`
		Profile          string `json:"profile"`
	}
	if json.Unmarshal([]byte(result.ContentForLLM()), &opened) != nil ||
		opened.BrowserSessionID != "browser_remote_1" || opened.Target != "companion-browser" ||
		opened.Profile != "automation" {
		t.Fatalf("browser_open payload = %s", result.ContentForLLM())
	}

	result = router.Execute(ctx, "browser_observe", map[string]any{
		"browser_session_id": "browser_remote_1", "screenshot": true,
	})
	if result == nil || !result.IsError || source.observeCalls != 0 {
		t.Fatalf("screenshot escape result = %#v; observe calls = %d", result, source.observeCalls)
	}

	source.prepare = browser.Preparation{Action: browser.PreparedAction{
		ID: "prepared_1", SessionID: "browser_remote_1", Target: "companion-browser",
		Profile: "automation", TabID: "tab_primary", SnapshotID: "snapshot_1",
		SnapshotGeneration: 1, Effect: browser.EffectNavigation,
		Action: browser.Action{Kind: browser.ActionNavigate, URL: "https://example.com"},
	}}
	source.execute = browser.Invocation{
		ID: "invocation_1", SessionID: "browser_remote_1",
		Effect: browser.EffectNavigation, State: browser.InvocationSucceeded,
	}
	source.observe = browser.Observation{
		SessionID: "browser_remote_1", TabID: "tab_primary",
		SnapshotID: "snapshot_2", SnapshotGeneration: 2,
		URL: "https://example.com", Origin: "https://example.com", Snapshot: "page",
	}
	result = router.Execute(ctx, "browser_act", map[string]any{
		"browser_session_id": "browser_remote_1", "tab_id": "tab_primary",
		"snapshot_id": "snapshot_1", "snapshot_generation": 1,
		"action": map[string]any{"kind": "navigate", "url": "https://example.com"},
	})
	if result == nil || result.IsError || source.prepareCalls != 1 || source.executeCalls != 1 ||
		source.prepareRequest.Owner != source.statusOwner ||
		source.prepareRequest.SessionID != "browser_remote_1" {
		t.Fatalf(
			"browser_act result = %#v; prepare=%#v execute calls=%d",
			result,
			source.prepareRequest,
			source.executeCalls,
		)
	}

	source.status.Target = "another-browser"
	result = router.Execute(ctx, "browser_status", map[string]any{
		"browser_session_id": "browser_remote_1",
	})
	if result == nil || !result.IsError {
		t.Fatalf("cross-profile session result = %#v", result)
	}
}

func TestRemoteBrowserProfileRouterOmitsApprovalAndAttachedEscapes(t *testing.T) {
	cfg := remoteBrowserTestConfig()
	target := cfg.Tools.Browser.Targets["companion-browser"]
	profile := target.Profiles["automation"]
	profile.ApprovalMode = config.BrowserApprovalAlwaysCommit
	target.Profiles["automation"] = profile
	cfg.Tools.Browser.Targets["companion-browser"] = target
	router, err := NewRemoteBrowserProfileRouter(
		cfg,
		&fakeBrowserToolSource{available: true},
		"main",
		"companion-browser",
		"automation",
	)
	if err != nil {
		t.Fatalf("NewRemoteBrowserProfileRouter() error = %v", err)
	}
	if _, err = router.Describe(t.Context(), "browser_act"); err == nil {
		t.Fatal("approval-requiring browser_act was advertised")
	}
	if _, err = router.Describe(t.Context(), "browser_observe"); err != nil {
		t.Fatalf("read-only browser_observe was omitted: %v", err)
	}

	profile.Mode = config.BrowserProfileAttachedUser
	target.Profiles["automation"] = profile
	cfg.Tools.Browser.Targets["companion-browser"] = target
	if _, err = NewRemoteBrowserProfileRouter(
		cfg,
		&fakeBrowserToolSource{available: true},
		"main",
		"companion-browser",
		"automation",
	); err == nil {
		t.Fatal("attached-user browser profile was admitted")
	}
}

func TestRemoteBrowserProfileRouterRetainsCaptureAndDownloadWithoutRoutedDelivery(t *testing.T) {
	cfg := remoteBrowserTestConfig()
	source := &fakeBrowserToolSource{
		available: true,
		status: browser.Session{
			ID: "browser_remote_1", Target: "companion-browser", Profile: "automation",
			State: browser.SessionReady, TabID: "tab_primary", ExpiresAt: 100,
		},
		actions: []browser.ActionKind{browser.ActionNavigate, browser.ActionDownload},
		screenshot: browser.ScreenshotArtifact{
			Ref: "transfer-artifact://capture_1", Kind: "screenshot", ContentType: "image/png",
			Filename: "browser-screenshot.png", Size: 128, SHA256: strings.Repeat("a", 64),
			SessionID: "browser_remote_1", TabID: "tab_primary", SnapshotID: "snapshot_1",
			SnapshotGeneration: 1, Target: browser.ScreenshotTargetPage,
		},
	}
	router, err := NewRemoteBrowserProfileRouter(
		cfg,
		source,
		"main",
		"companion-browser",
		"automation",
	)
	if err != nil {
		t.Fatal(err)
	}
	capture, err := router.Describe(t.Context(), "browser_capture")
	if err != nil || !capture.Available || capture.Risk != "read" ||
		capture.ResultKind != "browser_artifact" {
		t.Fatalf("Describe(browser_capture) = %#v, %v", capture, err)
	}
	action, err := router.Describe(t.Context(), "browser_act")
	if err != nil || !strings.Contains(string(action.InputSchema), `"const":"download"`) ||
		strings.Contains(string(action.InputSchema), `"deliver"`) ||
		strings.Contains(string(action.InputSchema), `"upload"`) ||
		strings.Contains(string(action.InputSchema), `"file_chooser"`) {
		t.Fatalf("retained browser action schema = %s, %v", action.InputSchema, err)
	}

	ctx := remoteBrowserTestContext()
	captured := router.Execute(ctx, "browser_capture", map[string]any{
		"browser_session_id": "browser_remote_1", "tab_id": "tab_primary",
		"snapshot_id": "snapshot_1", "snapshot_generation": 1, "target": "page",
	})
	if captured.IsError || len(captured.Media) != 0 || len(captured.ContextMedia) != 0 ||
		source.deliveryRequest.Ref != "" || !strings.Contains(captured.ContentForLLM(), source.screenshot.Ref) {
		t.Fatalf("retained capture = %#v; delivery=%#v", captured, source.deliveryRequest)
	}

	source.prepare = browser.Preparation{Action: browser.PreparedAction{
		ID: "prepared_download", RequestID: "request_download", SessionID: "browser_remote_1",
		Target: "companion-browser", Profile: "automation", TabID: "tab_primary",
		SnapshotID: "snapshot_1", SnapshotGeneration: 1, Effect: browser.EffectUnknown,
		Action: browser.Action{Kind: browser.ActionDownload, Ref: "download_ref"},
	}}
	source.execute = browser.Invocation{
		ID: "invocation_download", SessionID: "browser_remote_1", Effect: browser.EffectUnknown,
		State: browser.InvocationSucceeded,
		Download: &browser.DownloadArtifact{
			Ref: "transfer-artifact://download_1", Kind: "download", ContentType: "text/plain",
			Filename: "report.txt", Size: 64, SHA256: strings.Repeat("b", 64),
			SessionID: "browser_remote_1", TabID: "tab_primary", Generation: 1,
		},
	}
	source.observe = browser.Observation{
		SessionID: "browser_remote_1", TabID: "tab_primary", SnapshotID: "snapshot_2",
		SnapshotGeneration: 2, URL: "https://example.com", Origin: "https://example.com", Snapshot: "page",
	}
	downloaded := router.Execute(ctx, "browser_act", map[string]any{
		"browser_session_id": "browser_remote_1", "tab_id": "tab_primary",
		"snapshot_id": "snapshot_1", "snapshot_generation": 1,
		"action": map[string]any{"kind": "download", "ref": "download_ref"},
	})
	if downloaded.IsError || len(downloaded.Media) != 0 || source.downloadDelivery.Ref != "" ||
		!strings.Contains(downloaded.ContentForLLM(), `"artifact_state":"committed"`) ||
		!strings.Contains(downloaded.ContentForLLM(), source.execute.Download.Ref) {
		t.Fatalf("retained download = %#v; delivery=%#v", downloaded, source.downloadDelivery)
	}
}

func remoteBrowserTestConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.Tools.Browser = config.BrowserToolsConfig{
		Enabled: true,
		Agents:  []string{"main"},
		Targets: map[string]config.BrowserTargetConfig{
			"companion-browser": {
				Enabled: true, Placement: config.BrowserPlacementNode, NodeTarget: "companion",
				Driver: config.BrowserDriverPlaywrightLibrary,
				Profiles: map[string]config.BrowserProfileConfig{
					"automation": {
						Enabled: true, Revision: "automation-v1", Mode: config.BrowserProfileManaged,
						AllowedAgents: []string{"main"}, AllowedActors: []string{"local:operator"},
						NetworkMode: config.BrowserNetworkPublicWeb, CapabilityMode: config.BrowserCapabilityFullAccess,
						ApprovalMode: config.BrowserApprovalNone, AllowApprovedActions: true,
					},
				},
			},
		},
	}
	return cfg
}

func remoteBrowserTestContext() context.Context {
	principal := runtimecap.Principal{
		Runtime: runtimecap.KindCoding, ActorID: "local:operator", AgentID: "main",
		SessionID: "coding:thread", ExecutionID: "remote_browser_execution",
	}
	runtime := runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindCoding}).BindPrincipal(principal)
	ctx := toolshared.WithRuntimeCapabilities(context.Background(), runtime)
	ctx = toolshared.WithToolSessionContext(ctx, "main", "coding:thread", nil)
	ctx = toolshared.WithToolRouteSessionKey(ctx, "coding:thread")
	ctx = toolshared.WithToolCallID(ctx, "remote_capability_browser_call")
	return toolshared.WithToolExecutionIdentity(ctx, "project", "remote_browser_execution")
}
