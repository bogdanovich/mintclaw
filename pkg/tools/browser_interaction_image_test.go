package tools

import (
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestBrowserCaptureInteractionImageUsesOwnedDeliveryWithoutChangingDefault(t *testing.T) {
	for _, mode := range []string{"normal", "interaction"} {
		t.Run(mode, func(t *testing.T) {
			source := &fakeBrowserToolSource{available: true, screenshot: browser.ScreenshotArtifact{
				Ref:           "transfer-artifact://challenge",
				MediaRef:      "media://challenge",
				ContentType:   "image/png",
				Filename:      "challenge.png",
				SessionID:     "session",
				DeliveryState: browser.ScreenshotDeliveryPending,
				Recovery: &browser.ScreenshotRecovery{
					WorkspaceID: "workspace",
					AgentID:     "browser",
					ActorID:     "actor",
					RouteID:     "route",
					SessionID:   "session",
					ToolCallID:  "capture",
				},
			}}
			args := map[string]any{
				"delivery":            mode,
				"browser_session_id":  "session",
				"tab_id":              "tab",
				"snapshot_id":         "snapshot",
				"snapshot_generation": 1,
				"target":              "page",
			}
			tool := NewBrowserCaptureTool(browserToolTestConfig(), source)
			result := tool.Execute(browserToolTestContext(), args)
			expected := toolshared.DeliveryImmediateContinue
			if mode == "interaction" {
				expected = toolshared.DeliveryInteractionMedia
			}
			if result.IsError || result.Delivery.Intent != expected || result.Delivery.Outbound == nil ||
				result.Delivery.Commit == nil {
				t.Fatalf(
					"capture did not retain owned image delivery: error=%v intent=%s",
					result.IsError,
					result.Delivery.Intent,
				)
			}
			if err := result.Delivery.Commit(
				browserToolTestContext(),
			); err != nil ||
				source.deliveryRequest.Ref != "transfer-artifact://challenge" {
				t.Fatal("image delivery lost its source claim", err)
			}
			if mode == "interaction" && !tool.executeRetained(browserToolTestContext(), args).IsError {
				t.Fatal("retained-only coding transport claimed interactive user delivery")
			}
		})
	}
	for _, mode := range []any{"unknown", false, 1} {
		source := &fakeBrowserToolSource{available: true}
		result := NewBrowserCaptureTool(
			browserToolTestConfig(),
			source,
		).Execute(browserToolTestContext(), map[string]any{"delivery": mode})
		if !result.IsError || source.screenshotRequest.SessionID != "" {
			t.Fatal("invalid delivery mode reached the capture source")
		}
	}
}
