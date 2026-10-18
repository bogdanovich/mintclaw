package eval

import (
	"context"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/browseraction"
	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

func enableAdmissionBrowser(t *testing.T, fixture *admissionFixture) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	origin := "http://" + listener.Addr().String()
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.preset.Capabilities = append(fixture.preset.Capabilities, "browser")
	fixture.cfg.Agents.List[0].ToolPolicy.Allow = append(
		fixture.cfg.Agents.List[0].ToolPolicy.Allow,
		browserEvalTools...)
	fixture.cfg.Tools.Browser = config.BrowserToolsConfig{
		Enabled: true, Agents: []string{fixture.preset.ExpectedAgent}, DefaultTarget: config.BrowserDefaultTarget,
		Targets: map[string]config.BrowserTargetConfig{config.BrowserDefaultTarget: {
			Enabled: true, Driver: config.BrowserDriverPlaywrightLibrary,
			DriverExecutable: "/opt/mintclaw/playwright-library/sidecar.cjs", DefaultProfile: "eval-browser",
			Profiles: map[string]config.BrowserProfileConfig{"eval-browser": {
				Enabled:  true,
				Revision: "eval-browser-v1",
				Mode:     config.BrowserProfileEphemeral,
				AllowedAgents: []string{
					fixture.preset.ExpectedAgent,
				},
				AllowedActors:        []string{"mintclaw:mintclaw-user"},
				NetworkMode:          config.BrowserNetworkLoopbackFixture,
				AllowedOrigins:       []string{origin},
				CapabilityMode:       config.BrowserCapabilityRestricted,
				ApprovalMode:         config.BrowserApprovalPolicy,
				AllowApprovedActions: true,
				Runtime:              browserEvalRuntime(fixture.home),
				Policy: &browserpolicy.Policy{DefaultDecision: browserpolicy.DecisionDeny, Rules: []browserpolicy.Rule{
					{
						ID:       "read",
						Match:    browserpolicy.RuleMatch{Origins: []string{origin}, Effects: []string{"read"}},
						Decision: browserpolicy.DecisionAllow,
					},
					{
						ID: "fixture-navigation", Decision: browserpolicy.DecisionAllow,
						Match: browserpolicy.RuleMatch{
							Actions: []string{"navigate"}, Effects: []string{"navigation"}, Origins: []string{origin},
						},
					},
					{
						ID:       "ask",
						Match:    browserpolicy.RuleMatch{Origins: []string{origin}},
						Decision: browserpolicy.DecisionAsk,
					},
				}},
			}},
		}},
	}
	for _, path := range []string{browserRuntimeDirectory, browserRuntimeDirectory + "/ephemeral", browserRuntimeDirectory + "/locks"} {
		if err = os.Mkdir(filepath.Join(fixture.home, path), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	fixture.writeConfig(t)
	if err = validateConversationPolicy(fixture.cfg, fixture.preset, fixture.workspace); err != nil {
		t.Fatal("test browser profile not admitted", err)
	}
}

func TestBrowserEvalProfileRejectsBroaderOrUnscopedAuthority(t *testing.T) {
	for _, mode := range []string{
		"valid", "broad-allow", "commit-allow", "unknown-allow", "actor", "agent", "agents", "extra-target",
		"extra-profile", "managed", "cloud", "mcp", "fallback", "any-http", "hooks", "execute", "headed",
		"foreign-root", "foreign-lock", "dry-run", "tool-exec", "tool-login", "tool-execute", "bypass",
	} {
		t.Run(mode, func(t *testing.T) {
			fixture := newAdmissionFixture(t)
			enableAdmissionBrowser(t, fixture)
			target := fixture.cfg.Tools.Browser.Targets[config.BrowserDefaultTarget]
			profile := target.Profiles["eval-browser"]
			switch mode {
			case "broad-allow":
				profile.Policy.Rules[0].Match.Effects = nil
			case "commit-allow":
				profile.Policy.Rules[0].Match.Effects = []string{"read", "external_commit"}
			case "unknown-allow":
				profile.Policy.Rules[0].Match.Effects = []string{"unknown"}
			case "actor":
				profile.AllowedActors = append(profile.AllowedActors, "mintclaw:other")
			case "agent":
				profile.AllowedAgents = nil
			case "agents":
				fixture.cfg.Tools.Browser.Agents = nil
			case "extra-target":
				fixture.cfg.Tools.Browser.Targets["other"] = target
			case "extra-profile":
				target.Profiles["other"] = profile
			case "managed":
				profile.Mode = config.BrowserProfileManaged
			case "cloud":
				target.Placement, target.Provider = config.BrowserPlacementCloud, config.BrowserProviderSteel
			case "mcp":
				target.Driver = config.BrowserDriverPlaywrightMCP
			case "fallback":
				target.Fallback.Target = "other"
			case "any-http":
				profile.NetworkMode, profile.AllowedOrigins = config.BrowserNetworkAnyHTTP, nil
			case "hooks":
				profile.Policy.Hook = &browserpolicy.Hook{Command: []string{"/bin/false"}, TimeoutMS: 100}
			case "execute":
				profile.PrivilegedExecution.Enabled = true
			case "headed":
				profile.Runtime.Headed = true
			case "foreign-root":
				profile.Runtime.EphemeralRoot = t.TempDir()
			case "foreign-lock":
				profile.Runtime.LockFile = filepath.Join(t.TempDir(), "foreign.lock")
			case "dry-run":
				profile.DryRun, profile.AllowApprovedActions = true, false
			case "tool-exec", "tool-login", "tool-execute":
				tool := map[string]string{"tool-exec": "exec", "tool-login": "browser_login", "tool-execute": "browser_execute"}[mode]
				fixture.cfg.Agents.List[0].ToolPolicy.Allow = append(fixture.cfg.Agents.List[0].ToolPolicy.Allow, tool)
			case "bypass":
				fixture.cfg.Tools.Approval.Mode = config.ToolApprovalModeAllowAll
			}
			target.Profiles["eval-browser"] = profile
			fixture.cfg.Tools.Browser.Targets[config.BrowserDefaultTarget] = target
			err := validateConversationPolicy(fixture.cfg, fixture.preset, fixture.workspace)
			if (mode == "valid") != (err == nil) {
				t.Fatal("browser authority admission mismatch", mode, err)
			}
			if fixture.probes.Load() != 0 || fixture.tasks.Load() != 0 {
				t.Fatal("public policy validation performed target work")
			}
		})
	}
}

func TestBrowserEvalSeedTemplatePreservesTheNarrowPolicyContract(t *testing.T) {
	fixture := newAdmissionFixture(t)
	data, err := os.ReadFile("../../evals/profiles/browser.template.json")
	cfg := config.DefaultConfig()
	if err != nil || config.DecodeCurrentConfig(data, cfg) != nil {
		t.Fatal("browser seed template cannot be decoded", err)
	}
	cfg.Agents.Defaults.Workspace = fixture.workspace
	cfg.ModelList[0].Model = "synthetic-model"
	target := cfg.Tools.Browser.Targets[config.BrowserDefaultTarget]
	target.DriverExecutable = "/opt/mintclaw/playwright-library/sidecar.cjs"
	target.DriverArguments = []string{"--browser=chromium", "--executable-path=/opt/test/chromium"}
	profile := target.Profiles["eval-browser"]
	profile.Runtime = browserEvalRuntime(fixture.home)
	target.Profiles["eval-browser"] = profile
	cfg.Tools.Browser.Targets[config.BrowserDefaultTarget] = target
	fixture.preset.Capabilities = append(fixture.preset.Capabilities, "browser")
	if err = validateConversationPolicy(cfg, fixture.preset, fixture.workspace); err != nil {
		t.Fatal("browser seed template policy is not admitted after replacing declared paths/model", err)
	}
}

func TestBrowserEvalProfileAdmitsOnlyAnExplicitFixtureNavigationRule(t *testing.T) {
	for _, mode := range []string{
		"valid", "no-action", "wrong-action", "extra-action", "mixed-effects", "missing-origin",
		"foreign-origin", "extra-origin", "commit", "unknown",
	} {
		t.Run(mode, func(t *testing.T) {
			fixture := newAdmissionFixture(t)
			enableAdmissionBrowser(t, fixture)
			target := fixture.cfg.Tools.Browser.Targets[config.BrowserDefaultTarget]
			profile := target.Profiles["eval-browser"]
			rule := profile.Policy.Rules[1]
			switch mode {
			case "no-action":
				rule.Match.Actions = nil
			case "wrong-action":
				rule.Match.Actions = []string{"click"}
			case "extra-action":
				rule.Match.Actions = []string{"navigate", "click"}
			case "mixed-effects":
				rule.Match.Effects = []string{"navigation", "read"}
			case "missing-origin":
				rule.Match.Origins = nil
			case "foreign-origin":
				rule.Match.Origins = []string{"https://example.org"}
			case "extra-origin":
				rule.Match.Origins = append(slices.Clone(rule.Match.Origins), "http://127.0.0.1:23456")
			case "commit":
				rule.Match.Effects = []string{"navigation", "external_commit"}
			case "unknown":
				rule.Match.Effects = []string{"navigation", "unknown"}
			}
			profile.Policy.Rules[1] = rule
			target.Profiles["eval-browser"] = profile
			fixture.cfg.Tools.Browser.Targets[config.BrowserDefaultTarget] = target
			err := validateConversationPolicy(fixture.cfg, fixture.preset, fixture.workspace)
			if (mode == "valid") != (err == nil) || fixture.probes.Load() != 0 || fixture.tasks.Load() != 0 {
				t.Fatal("navigation policy admission widened authority or performed target work", mode, err)
			}
		})
	}
}

func TestBrowserEvalSeedPolicyUsesNativeNavigationWithoutApprovingCommits(t *testing.T) {
	data, err := os.ReadFile("../../evals/profiles/browser.template.json")
	cfg := config.DefaultConfig()
	if err != nil || config.DecodeCurrentConfig(data, cfg) != nil {
		t.Fatal("browser seed unavailable", err)
	}
	_, profile := browserEvalProfile(cfg)
	revision, err := browserpolicy.PolicyRevision(*profile.Policy)
	if err != nil {
		t.Fatal(err)
	}
	origin := profile.AllowedOrigins[0]
	for _, check := range []struct {
		name     string
		action   browseraction.Action
		role     string
		origin   string
		decision string
	}{
		{
			"fixture-navigation",
			browseraction.Action{Kind: browseraction.ActionNavigate, URL: origin + "/eval/test/"},
			"", origin, browserpolicy.DecisionAllow,
		},
		{
			"foreign-port",
			browseraction.Action{Kind: browseraction.ActionNavigate, URL: "http://127.0.0.1:23456/"},
			"", "http://127.0.0.1:23456", browserpolicy.DecisionDeny,
		},
		{
			"public-origin",
			browseraction.Action{Kind: browseraction.ActionNavigate, URL: "https://example.org/"},
			"", "https://example.org", browserpolicy.DecisionDeny,
		},
		{
			"submit-button",
			browseraction.Action{Kind: browseraction.ActionClick, Ref: "e1"},
			"button", origin, browserpolicy.DecisionAsk,
		},
		{
			"ambiguous-link",
			browseraction.Action{Kind: browseraction.ActionClick, Ref: "e1"},
			"link", origin, browserpolicy.DecisionAsk,
		},
		{
			"keypress",
			browseraction.Action{Kind: browseraction.ActionPress, Ref: "e1", Key: "Enter"},
			"textbox", origin, browserpolicy.DecisionAsk,
		},
		{
			"fill",
			browseraction.Action{Kind: browseraction.ActionFill, Ref: "e1", Value: "synthetic"},
			"textbox", origin, browserpolicy.DecisionAllow,
		},
		{
			"hover",
			browseraction.Action{Kind: browseraction.ActionHover, Ref: "e1"},
			"button", origin, browserpolicy.DecisionAllow,
		},
		{
			"accept-dialog",
			browseraction.Action{Kind: browseraction.ActionDialog, DialogID: "dialog-test", Decision: "accept"},
			"", origin, browserpolicy.DecisionAsk,
		},
	} {
		t.Run(check.name, func(t *testing.T) {
			effect, err := browserpolicy.DeriveActionEffect(check.action, check.role)
			if err != nil {
				t.Fatal(err)
			}
			result, err := browserpolicy.Evaluate(t.Context(), *profile.Policy, browserpolicy.ActionMetadata{
				Action: string(check.action.Kind), Effect: effect, Origin: check.origin, Role: check.role,
				ProfileRevision: profile.Revision, PolicyRevision: revision,
			})
			if err != nil || result.Decision != check.decision {
				t.Fatal(
					"native policy effect lost fixture navigation or silently approved a commit",
					result.Decision,
					err,
				)
			}
		})
	}
}

func TestPrepareBrowserProfileRebasesOnlyEmptyOwnedRuntimeAndExactOrigins(t *testing.T) {
	fixture, parent := preparationFixture(t)
	enableAdmissionBrowser(t, fixture)
	name, before := browserEvalProfile(fixture.cfg)
	oldOrigin := before.AllowedOrigins[0]
	parsed, err := url.Parse(oldOrigin)
	if err != nil {
		t.Fatal(err)
	}
	sourceHost, err := newBrowserFixtureHost(parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sourceHost.close() })
	if err := os.WriteFile(
		filepath.Join(before.Runtime.EphemeralRoot, "old-session-state"),
		[]byte("unlisted browser state"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := config.NewRepository(fixture.preset.ConfigPath).Save(fixture.cfg); err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareProfile(t.Context(), fixture.preset, fixture.home, parent)
	if err != nil || !prepared.BrowserPrepared || prepared.RuntimeRun || prepared.Outcome != "prepared" ||
		fixture.probes.Load() != 0 || fixture.tasks.Load() != 0 {
		t.Fatal("browser preparation failed or ran target work", err)
	}
	result, err := config.NewRepository(prepared.ConfigPath).ReadDurable()
	if err != nil {
		t.Fatal(err)
	}
	preset, err := LoadPreset(prepared.PresetPath)
	if err != nil || validateConversationProfile(result.Config, preset, prepared.Workspace) != nil {
		t.Fatal("prepared browser profile invalid", err)
	}
	afterName, after := browserEvalProfile(result.Config)
	if afterName != name || after.Runtime != browserEvalRuntime(prepared.Home) ||
		after.AllowedOrigins[0] == oldOrigin ||
		!slices.Equal(before.AllowedOrigins, []string{oldOrigin}) {
		t.Fatal("browser profile identity/paths/origin were copied or seed changed")
	}
	for _, rule := range after.Policy.Rules {
		if !slices.Equal(rule.Match.Origins, after.AllowedOrigins) {
			t.Fatal("browser rules retained the seed's destination")
		}
	}
	if err = validateFreshOwnedProfile(preparedProfile{
		config: result.Config, home: prepared.Home, workspace: prepared.Workspace,
	}); err != nil {
		t.Fatal("prepared browser runtime was not empty/fresh", err)
	}
}

func TestOwnedBrowserProfileRequiresTheMatchingLiveFixtureBeforeLaunch(t *testing.T) {
	fixture := newOwnedGatewayFixture(t, "normal")
	enableAdmissionBrowser(t, fixture)
	child, _, err := startOwnedGateway(t.Context(), fixture.preset, fixture.home)
	registerOwnedGatewayCleanup(t, child)
	if !errors.Is(err, ErrOwnedGateway) || child != nil {
		t.Fatal("browser gateway launched without owned fixture authority", err)
	}
	host, err := bindOwnedBrowserFixture(fixture.preset, fixture.home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.close() })
	other := newBrowserFixtureHostForTest(t)
	if child, _, err = startOwnedGatewayWithBrowser(t.Context(), fixture.preset, fixture.home, other); child != nil ||
		!errors.Is(err, ErrOwnedGateway) {
		t.Fatal("foreign listener reached native process launch", err)
	}
	child, _, err = startOwnedGatewayWithBrowser(t.Context(), fixture.preset, fixture.home, host)
	registerOwnedGatewayCleanup(t, child)
	if err != nil || child == nil || !child.admitted {
		t.Fatal("matching owned fixture/gateway refused", err)
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if receipt := child.stopAndWait(stopCtx); receipt.Code != "native_exit_zero" {
		t.Fatal("owned test child did not stop", receipt.Code)
	}
}

func TestOwnedBrowserProfileRefusesOccupiedOriginAndRetainedRuntime(t *testing.T) {
	for _, mode := range []string{"occupied", "ephemeral", "lock", "extra", "symlink", "public"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newOwnedGatewayFixture(t, "normal")
			enableAdmissionBrowser(t, fixture)
			_, profile := browserEvalProfile(fixture.cfg)
			switch mode {
			case "occupied":
				host, err := bindOwnedBrowserFixture(fixture.preset, fixture.home)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = host.close() })
			case "ephemeral", "lock", "extra":
				path := filepath.Join(profile.Runtime.EphemeralRoot, "old")
				switch mode {
				case "lock":
					path = profile.Runtime.LockFile
				case "extra":
					path = filepath.Join(fixture.home, browserRuntimeDirectory, "old")
				}
				if err := os.WriteFile(path, []byte("retained state"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(profile.Runtime.EphemeralRoot); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), profile.Runtime.EphemeralRoot); err != nil {
					t.Fatal(err)
				}
			case "public":
				if err := os.Chmod(profile.Runtime.EphemeralRoot, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			host, err := bindOwnedBrowserFixture(fixture.preset, fixture.home)
			if host != nil {
				_ = host.close()
			}
			if host != nil || !errors.Is(err, ErrOwnedGateway) {
				t.Fatal("unowned or stale browser authority was admitted", mode, err)
			}
			if _, err = os.Lstat(filepath.Join(fixture.home, ".eval-gateway-owner")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("refused browser profile acquired a process marker")
			}
		})
	}
}
