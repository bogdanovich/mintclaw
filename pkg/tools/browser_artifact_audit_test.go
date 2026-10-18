package tools

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestBrowserDownloadAuditCarriesOnlyCommittedOutputArtifact(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*browser.Invocation, *browser.Preparation)
		proof  bool
	}{
		{"committed original", func(*browser.Invocation, *browser.Preparation) {}, true},
		{"no artifact", func(i *browser.Invocation, _ *browser.Preparation) { i.Download = nil }, false},
		{"truncated", func(i *browser.Invocation, _ *browser.Preparation) { i.Download.Truncated = true }, false},
		{"wrong session", func(i *browser.Invocation, _ *browser.Preparation) { i.Download.SessionID = "other" }, false},
		{"wrong kind", func(i *browser.Invocation, _ *browser.Preparation) { i.Download.Kind = "screenshot" }, false},
		{"empty ref", func(i *browser.Invocation, _ *browser.Preparation) { i.Download.Ref = "" }, false},
		{"upload input", func(_ *browser.Invocation, p *browser.Preparation) { p.Action.Action.Kind = browser.ActionUpload }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			invocation := browser.Invocation{
				ID: "inv_download", SessionID: "session_1", State: browser.InvocationSucceeded,
				Effect: browser.EffectUnknown,
				Download: &browser.DownloadArtifact{
					Ref: "transfer-artifact://original", SessionID: "session_1", Kind: "download",
					SHA256: strings.Repeat("a", 64), Size: 68,
				},
			}
			preparation := browser.Preparation{Action: browser.PreparedAction{
				Action: browser.Action{Kind: browser.ActionDownload},
			}}
			test.mutate(&invocation, &preparation)
			result := toolshared.NewToolResult("completed")
			attachBrowserExternalActionAudit(result, invocation, preparation, "browser_act")
			require.Len(t, result.WriteAudit, 1, "effect accounting must remain intact")
			audit := result.WriteAudit[0]
			require.Equal(t, "external_action", audit.Kind)
			require.Equal(t, "unknown", audit.Metadata["effect"])
			if test.proof {
				require.Equal(t, invocation.Download.Ref, audit.Metadata["artifact_ref"])
			} else {
				require.Empty(t, audit.Metadata["artifact_ref"])
			}
		})
	}
}
