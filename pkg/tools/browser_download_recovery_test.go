package tools

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/browser"
)

func TestBrowserActDownloadNotDispatchedGuidanceAndDurableReplay(t *testing.T) {
	source := &fakeBrowserToolSource{
		available: true,
		prepare: browser.Preparation{Action: browser.PreparedAction{
			ID:     "prepared_download",
			Action: browser.Action{Kind: browser.ActionDownload},
			Effect: browser.EffectUnknown,
		}},
		execute: browser.Invocation{
			ID: "invocation_download", SessionID: "browser_session_1", Effect: browser.EffectUnknown,
			State: browser.InvocationFailed, SafeFailure: "download_not_dispatched", AcceptedAt: 1,
		},
	}
	args := map[string]any{
		"browser_session_id": "browser_session_1", "tab_id": "tab_primary",
		"snapshot_id": "snapshot_1", "snapshot_generation": 3,
		"action": map[string]any{"kind": "download", "ref": "ref_1"},
	}
	for range 2 {
		result := NewBrowserActTool(browserToolTestConfig(), source).Execute(browserToolTestContext(), args)
		var payload map[string]any
		if result == nil || !result.IsError || json.Unmarshal([]byte(result.ContentForLLM()), &payload) != nil {
			t.Fatalf("result = %#v", result)
		}
		if payload["code"] != "download_not_dispatched" || payload["download_dispatched"] != false ||
			payload["session_preserved"] != true || payload["browser_session_id"] != "browser_session_1" {
			t.Fatalf("download failure = %#v", payload)
		}
	}
	source.executeErr = errors.Join(browser.ErrDownloadNotDispatched, browser.ErrSnapshotInvalidation)
	result := NewBrowserActTool(browserToolTestConfig(), source).Execute(browserToolTestContext(), args)
	var payload map[string]any
	if json.Unmarshal([]byte(result.ContentForLLM()), &payload) != nil || payload["session_preserved"] == true {
		t.Fatalf("state failure claimed preservation: %s", result.ContentForLLM())
	}
}
