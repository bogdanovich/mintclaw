package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
)

func TestNormalizeBrowserLoopbackFixtureOrigin(t *testing.T) {
	for _, origin := range []string{
		"http://127.0.0.1:34567", "http://[::1]:34567", "http://127.0.0.1:1024", "http://[::1]:65535",
	} {
		got, err := NormalizeBrowserNetworkOrigin(BrowserNetworkLoopbackFixture, origin)
		if err != nil || got != origin {
			t.Fatal("canonical fixture origin refused", err)
		}
		if _, err = NormalizeBrowserOrigin(origin); err == nil {
			t.Fatal("new fixture mode widened the existing public-origin boundary")
		}
	}
	for _, origin := range []string{
		"http://localhost:34567", "http://fixture.example:34567", "http://127.0.0.2:34567",
		"http://127.1:34567", "http://2130706433:34567", "http://0177.0.0.1:34567",
		"http://0x7f000001:34567", "http://127.0.0.1.:34567", "http://[::ffff:127.0.0.1]:34567",
		"http://[::1%25lo0]:34567", "http://[0:0:0:0:0:0:0:1]:34567", "http://0.0.0.0:34567",
		"http://192.168.1.1:34567", "http://169.254.169.254:34567", "http://8.8.8.8:34567",
		"https://127.0.0.1:34567", "ftp://127.0.0.1:34567", "HTTP://127.0.0.1:34567",
		"http://127.0.0.1", "http://127.0.0.1:80", "http://127.0.0.1:1023", "http://127.0.0.1:0",
		"http://127.0.0.1:65536", "http://127.0.0.1:034567", "http://127.0.0.1:34567/",
		"http://private-canary@127.0.0.1:34567", "http://127.0.0.1:34567/private-canary",
		"http://127.0.0.1:34567?private-canary", "http://127.0.0.1:34567#private-canary",
	} {
		if got, err := NormalizeBrowserNetworkOrigin(BrowserNetworkLoopbackFixture, origin); err == nil || got != "" ||
			strings.Contains(err.Error(), "private-canary") {
			t.Fatal("unsafe fixture origin or value-bearing error admitted")
		}
	}
}

func loopbackFixtureConfig(t *testing.T) *Config {
	t.Helper()
	cfg := browserConfigFixture(t)
	target := cfg.Tools.Browser.Targets[BrowserDefaultTarget]
	target.Driver, target.DriverServer = BrowserDriverPlaywrightLibrary, ""
	target.DriverExecutable = "/opt/mintclaw/playwright-library/sidecar.cjs"
	profile := target.Profiles[BrowserDefaultProfile]
	root := filepath.Dir(profile.Runtime.ProfileDirectory)
	profile.Mode, profile.Revision = BrowserProfileEphemeral, "fixture-v1"
	profile.NetworkMode, profile.AllowedOrigins = BrowserNetworkLoopbackFixture, []string{"http://127.0.0.1:34567"}
	profile.CapabilityMode, profile.ApprovalMode = BrowserCapabilityRestricted, BrowserApprovalPolicy
	profile.Policy = &browserpolicy.Policy{DefaultDecision: browserpolicy.DecisionDeny}
	profile.Runtime.ProfileDirectory = ""
	profile.Runtime.EphemeralRoot = filepath.Join(root, "fixture-ephemeral")
	if err := os.Mkdir(profile.Runtime.EphemeralRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	target.Profiles[BrowserDefaultProfile] = profile
	cfg.Tools.Browser.Targets[BrowserDefaultTarget] = target
	return cfg
}

func TestBrowserLoopbackFixtureRequiresNarrowExplicitAuthority(t *testing.T) {
	for _, mode := range []string{
		"valid", "ipv6", "live", "deny-rule", "managed", "attached", "full-access", "default-allow", "hook", "execute", "empty",
		"multiple", "noncanonical", "broad-rule", "foreign-rule", "mcp", "node", "cloud", "fallback",
	} {
		t.Run(mode, func(t *testing.T) {
			cfg := loopbackFixtureConfig(t)
			target := cfg.Tools.Browser.Targets[BrowserDefaultTarget]
			profile := target.Profiles[BrowserDefaultProfile]
			switch mode {
			case "ipv6":
				profile.AllowedOrigins = []string{"http://[::1]:34567"}
			case "live":
				profile.DryRun, profile.AllowApprovedActions = false, true
			case "deny-rule":
				profile.Policy.Rules = []browserpolicy.Rule{{ID: "deny-all", Decision: browserpolicy.DecisionDeny}}
			case "managed":
				profile.Mode = BrowserProfileManaged
			case "attached":
				profile.Mode = BrowserProfileAttachedUser
			case "full-access":
				profile.CapabilityMode, profile.ApprovalMode, profile.Policy = BrowserCapabilityFullAccess, BrowserApprovalNone, nil
			case "default-allow":
				profile.Policy.DefaultDecision = browserpolicy.DecisionAllow
			case "hook":
				profile.Policy.Hook = &browserpolicy.Hook{Command: []string{"/bin/false"}, TimeoutMS: 100}
			case "execute":
				profile.PrivilegedExecution.Enabled = true
			case "empty":
				profile.AllowedOrigins = nil
			case "multiple":
				profile.AllowedOrigins = append(profile.AllowedOrigins, "http://127.0.0.1:34568")
			case "noncanonical":
				profile.AllowedOrigins[0] += "/"
			case "broad-rule", "foreign-rule":
				rule := browserpolicy.Rule{ID: "broad", Decision: browserpolicy.DecisionAllow}
				if mode == "foreign-rule" {
					rule.Match.Origins = []string{"https://example.com"}
				}
				profile.Policy.Rules = []browserpolicy.Rule{rule}
			case "mcp":
				target.Driver = BrowserDriverPlaywrightMCP
			case "node":
				target.Placement = BrowserPlacementNode
			case "cloud":
				target.Placement, target.Provider = BrowserPlacementCloud, BrowserProviderSteel
			case "fallback":
				target.Fallback.Target = "another"
			}
			target.Profiles[BrowserDefaultProfile] = profile
			cfg.Tools.Browser.Targets[BrowserDefaultTarget] = target
			err := cfg.ValidateBrowserConfig()
			valid := mode == "valid" || mode == "ipv6" || mode == "live" || mode == "deny-rule"
			if valid != (err == nil) {
				t.Fatal("fixture network authority mismatch", mode, err)
			}
		})
	}
}

func TestBrowserLoopbackFixturePolicyRemainsOriginBoundAndRevisioned(t *testing.T) {
	cfg := loopbackFixtureConfig(t)
	target := cfg.Tools.Browser.Targets[BrowserDefaultTarget]
	profile := target.Profiles[BrowserDefaultProfile]
	profile.Policy.Rules = []browserpolicy.Rule{
		{
			ID:       "ask-fixture",
			Match:    browserpolicy.RuleMatch{Origins: profile.AllowedOrigins},
			Decision: browserpolicy.DecisionAsk,
		},
	}
	if err := ValidateLoopbackFixtureBrowserProfile(profile); err != nil {
		t.Fatal("exact fixture action rule refused", err)
	}
	profile.Policy.Rules = nil
	before, err := cfg.Tools.Browser.PolicyRevision()
	if err != nil {
		t.Fatal(err)
	}
	profile.AllowedOrigins = []string{"http://127.0.0.1:34568"}
	target.Profiles[BrowserDefaultProfile] = profile
	cfg.Tools.Browser.Targets[BrowserDefaultTarget] = target
	after, err := cfg.Tools.Browser.PolicyRevision()
	if err != nil || before == after {
		t.Fatal("fixture destination change reused policy authority", err)
	}
}
