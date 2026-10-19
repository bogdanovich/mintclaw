package gateway

import (
	"context"
	"testing"

	"github.com/bogdanovich/mintclaw/internal/credentials/vault"
	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestCredentialLoginAndOrdinaryActionsRegisterWithDistinctCapabilities(t *testing.T) {
	cfg := gatewayBrowserConfig(t.TempDir())
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.List = []config.AgentConfig{{ID: "browser", Default: true}}
	target := cfg.Tools.Browser.Targets[config.BrowserDefaultTarget]
	target.Driver, target.DriverServer, target.DriverExecutable = config.BrowserDriverPlaywrightLibrary, "", "node"
	profile := target.Profiles[config.BrowserDefaultProfile]
	profile.Mode, profile.DryRun, profile.AllowApprovedActions = config.BrowserProfileEphemeral, false, true
	profile.Runtime = config.BrowserProfileRuntimeConfig{
		EphemeralRoot: t.TempDir(),
		LockFile:      "/tmp/unused-catalog.lock",
	}
	target.Profiles[config.BrowserDefaultProfile] = profile
	cfg.Tools.Browser.Targets[config.BrowserDefaultTarget] = target
	loop := agent.NewAgentLoop(cfg, bus.NewMessageBus(), &startupBlockedProvider{reason: "catalog fixture only"})
	t.Cleanup(loop.Close)
	running := &services{Credentials: &credentialRuntime{
		loginEnabled: true,
		consumeLogin: func(context.Context, vault.Binding, func([]byte, []byte) error) error {
			return errCredentialsUnavailable
		},
	}}
	if setupBrowserTools(cfg, loop, running) != nil {
		t.Fatal("enabled login capability collided with the ordinary browser catalog")
	}
	registry := loop.GetRegistry().GetDefaultAgent().Tools
	for _, name := range []string{"browser_login", "browser_act", "browser_input", "browser_session", "browser_observe"} {
		if _, available := registry.Get(name); !available {
			t.Fatal("an enabled browser capability disappeared during composition")
		}
	}
}
