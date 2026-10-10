package tools

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/browser"
)

func TestBrowserActClickNotDispatchedGuidanceAndDurableReplay(t *testing.T) {
	source := &fakeBrowserToolSource{
		available: true,
		prepare: browser.Preparation{
			Action: browser.PreparedAction{
				ID:     "prepared_click",
				Action: browser.Action{Kind: browser.ActionClick},
				Effect: browser.EffectRead,
			},
		},
		execute: browser.Invocation{
			ID:          "invocation_click",
			SessionID:   "browser_session_1",
			Effect:      browser.EffectRead,
			State:       browser.InvocationFailed,
			SafeFailure: "click_not_dispatched",
			AcceptedAt:  1,
		},
	}
	args := map[string]any{
		"browser_session_id":  "browser_session_1",
		"tab_id":              "tab_primary",
		"snapshot_id":         "snapshot_1",
		"snapshot_generation": 3,
		"effect":              "read",
		"action":              map[string]any{"kind": "click", "ref": "ref_1"},
	}
	for _, replay := range []bool{false, true} {
		result := NewBrowserActTool(browserToolTestConfig(), source).Execute(browserToolTestContext(), args)
		var payload map[string]any
		if result == nil || !result.IsError || json.Unmarshal([]byte(result.ContentForLLM()), &payload) != nil {
			t.Fatalf("result = %#v", result)
		}
		if payload["code"] != "click_not_dispatched" || payload["click_dispatched"] != false ||
			payload["session_preserved"] != true ||
			payload["browser_session_id"] != "browser_session_1" ||
			payload["action"] != "observe_same_session_then_choose_usable_control" {
			t.Fatalf("replay %t: %#v", replay, payload)
		}
	}
	source.executeErr = errors.Join(browser.ErrClickNotDispatched, browser.ErrSnapshotInvalidation)
	result := NewBrowserActTool(browserToolTestConfig(), source).Execute(browserToolTestContext(), args)
	var payload map[string]any
	if json.Unmarshal([]byte(result.ContentForLLM()), &payload) != nil || payload["session_preserved"] == true {
		t.Fatalf("state failure claimed preservation: %s", result.ContentForLLM())
	}
}
