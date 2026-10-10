package tools

import (
	"context"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/browser"
)

type recoveryBrowserToolSource struct {
	*fakeBrowserToolSource
	recovery browser.NavigationRecovery
	calls    int
}

func (source *recoveryBrowserToolSource) RecoverNavigation(
	context.Context, browser.Owner, string, string,
) (browser.NavigationRecovery, error) {
	source.calls++
	return source.recovery, nil
}

func TestBrowserActReturnsNewRecoveryAuthorityOrOmitsClosedSource(t *testing.T) {
	for _, success := range []bool{true, false} {
		name := "unavailable"
		if success {
			name = "recovered"
		}
		t.Run(name, func(t *testing.T) {
			source := &recoveryBrowserToolSource{
				fakeBrowserToolSource: &fakeBrowserToolSource{
					available: true,
					prepare: browser.Preparation{
						Action: browser.PreparedAction{
							ID:     "prepared_1",
							Action: browser.Action{Kind: browser.ActionNavigate},
						},
					},
					execute: browser.Invocation{
						ID:        "invocation_1",
						SessionID: "local_1",
						State:     browser.InvocationSucceeded,
					},
					observe: browser.Observation{SessionID: "local_1", SnapshotID: "local_snapshot"},
				},
				recovery: browser.NavigationRecovery{Status: name, FromSessionID: "local_1", Reason: "quota_exhausted"},
			}
			if success {
				source.recovery.SessionID = "steel_1"
				source.recovery.Observation = &browser.Observation{
					SessionID:  "steel_1",
					TabID:      "tab_steel",
					SnapshotID: "steel_snapshot",
				}
			}
			tool := NewBrowserActTool(browserToolTestConfig(), source)
			result := tool.Execute(browserToolTestContext(), map[string]any{
				"browser_session_id":  "local_1",
				"tab_id":              "tab_primary",
				"snapshot_id":         "local_snapshot",
				"snapshot_generation": 1,
				"action":              map[string]any{"kind": "navigate", "url": "https://example.com/"},
			})
			var view browserActionResult
			decodeBrowserToolResult(t, result, &view)
			if source.calls != 1 || view.Recovery == nil {
				t.Fatalf("recovery source not called: %+v", view)
			}
			if success &&
				(view.Observation == nil || view.Observation.BrowserSessionID != "steel_1" || view.Observation.SnapshotID != "steel_snapshot") {
				t.Fatalf("fresh Steel authority missing: %+v", view)
			}
			if !success && view.Observation != nil {
				t.Fatal("closed local observation leaked as reusable authority")
			}
		})
	}
}
