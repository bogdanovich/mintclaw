//go:build linux || darwin

package gateway

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

func TestCompanionDownloadDoesNotRequireGatewayPlaywrightDownloadSupport(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Browser = config.BrowserToolsConfig{
		Enabled: true,
		Agents:  []string{"browser"},
		Targets: map[string]config.BrowserTargetConfig{
			"companion": {
				Enabled: true, Placement: config.BrowserPlacementNode, NodeTarget: "personal-node",
				Driver: config.BrowserDriverPlaywrightMCP,
				Profiles: map[string]config.BrowserProfileConfig{
					"managed": {
						Enabled: true, Mode: config.BrowserProfileManaged,
						NetworkMode: config.BrowserNetworkAnyHTTP,
					},
				},
			},
		},
	}
	source := &gatewayBrowserToolSource{
		config: cfg, services: &services{}, downloadAvailable: false,
	}
	if !source.DownloadAvailable() {
		t.Fatal("companion download was gated by gateway-local Playwright support")
	}
	tool := tools.NewBrowserActTool(cfg, source)
	schema, err := json.Marshal(tool.Parameters())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(schema), `"const":"download"`) {
		t.Fatalf("browser_act schema omits companion download: %s", schema)
	}
	_, err = tool.ApprovalArguments(gatewayBrowserToolContext("browser"), map[string]any{
		"browser_session_id":  "browser_session_1",
		"tab_id":              "tab_primary",
		"snapshot_id":         "snapshot_1",
		"snapshot_generation": 1,
		"action":              map[string]any{"kind": "download", "ref": "ref_download"},
	})
	if !errors.Is(err, browser.ErrWorkerUnavailable) || errors.Is(err, browser.ErrDriverIncompatible) {
		t.Fatalf("download preparation error = %v, want worker unavailable after capability gate", err)
	}
}
