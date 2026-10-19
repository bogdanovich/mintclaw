package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

type terminalUnavailableBrowser struct {
	tools.BrowserToolSource
	inspected, cleaned, opened int
	owned                      bool
}

func (*terminalUnavailableBrowser) Available() bool                 { return true }
func (*terminalUnavailableBrowser) ScreenshotAvailable() bool       { return false }
func (*terminalUnavailableBrowser) ArtifactTransferAvailable() bool { return false }
func (*terminalUnavailableBrowser) DownloadAvailable() bool         { return false }
func (*terminalUnavailableBrowser) HandoffAvailable() bool          { return false }

func (source *terminalUnavailableBrowser) Open(
	_ context.Context,
	request browser.OpenRequest,
) (browser.Session, error) {
	now := time.Now().UnixMilli()
	session := browser.Session{
		ID:                   "browser_session_probe",
		Owner:                request.Owner,
		Target:               request.Target,
		Profile:              request.Profile,
		State:                browser.SessionReady,
		PolicyRevision:       "policy_probe",
		ControllerGeneration: 1,
		Controller:           browser.ControllerAgent,
		TabID:                "tab_probe",
		Revision:             1,
		CreatedAt:            now,
		UpdatedAt:            now,
		LastActivityAt:       now,
		ExpiresAt:            now + 60000,
	}
	if err := session.Validate(); err != nil {
		return browser.Session{}, err
	}
	source.opened++
	source.owned = true
	return session, nil
}

func (source *terminalUnavailableBrowser) ListOwnerSessions(context.Context, browser.Owner) ([]browser.Session, error) {
	source.inspected++
	return nil, errors.New("synthetic browser owner unavailable")
}

func (source *terminalUnavailableBrowser) CloseOwner(context.Context, browser.Owner) ([]browser.Session, error) {
	source.cleaned++
	return nil, errors.New("synthetic browser owner unavailable")
}

func TestTerminalLifecycleUnconfirmedCleanupCannotReturnSuccessfulTurn(t *testing.T) {
	provider := &sequenceProvider{responses: []*providers.LLMResponse{
		{ToolCalls: []providers.ToolCall{{ID: "open_probe", Name: "browser_session", Arguments: map[string]any{
			"operation": "open", "target": "gateway", "profile": "managed", "interaction_language": "en",
		}}}, FinishReason: "tool_calls"},
		{Content: "Task complete; browser closed.", FinishReason: "stop"},
	}}
	loop, agent, closeFixture := newTurnCoordTestLoop(t, provider)
	defer closeFixture()
	source := &terminalUnavailableBrowser{}
	agent.Tools.Register(tools.NewBrowserSessionTool(tools.NewBrowserToolOptions(config.BrowserToolsConfig{
		Enabled: true, Agents: []string{agent.ID}, Targets: map[string]config.BrowserTargetConfig{
			"gateway": {
				Enabled: true,
				Profiles: map[string]config.BrowserProfileConfig{
					"managed": {Enabled: true, Mode: config.BrowserProfileManaged},
				},
			},
		},
	}), source))
	spec := makeTestTurnSpec("cleanup-failure-probe")
	spec.Dispatch.InboundContext.SenderID = "fixture-owner"
	spec.Dispatch.InboundContext.ActorID = "cli:fixture-owner"
	state := newTurnState(agent, spec, turnEventScope{
		turnID: "cleanup-failure-probe", context: newTurnContext(nil, nil, nil),
	})
	result, err := runTestTurn(loop, t.Context(), state, newTestPipeline(loop))
	if source.opened != 1 || !source.owned || source.inspected == 0 || source.cleaned != 1 {
		t.Fatalf(
			"probe did not reach real browser lifecycle: opened=%d owned=%t inspected=%d cleaned=%d",
			source.opened,
			source.owned,
			source.inspected,
			source.cleaned,
		)
	}
	blocked := result.deliverable != nil && result.deliverable.ObjectiveOutcome != nil &&
		result.deliverable.ObjectiveOutcome.Status == taskresult.OutcomeBlocked
	if err == nil && result.status == TurnEndStatusCompleted && !blocked {
		t.Fatalf(
			"unresolved browser inspection and cleanup returned completed turn with no error: final=%q",
			result.finalContent,
		)
	}
}
