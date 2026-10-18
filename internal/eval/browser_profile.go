package eval

import (
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

const browserRuntimeDirectory = ".eval-browser"

var browserEvalTools = []string{
	"browser_targets", "browser_session", "browser_observe", "browser_act",
	"browser_contexts", "browser_capture", "browser_diagnostics",
}

// Only fixture-bound, local, ephemeral authority is admitted. Native policy
// still derives effects; an allow rule may not silently admit a commit.
func validateBrowserEvalPolicy(cfg *config.Config, preset Preset) error {
	if !cfg.Tools.Browser.Enabled || cfg.ValidateBrowserConfig() != nil ||
		len(cfg.Tools.Browser.Targets) != 1 ||
		!slices.Equal(cfg.Tools.Browser.Agents, []string{preset.ExpectedAgent}) ||
		cfg.Tools.Browser.EffectiveDefaultTarget() != config.BrowserDefaultTarget {
		return ErrUnsafeTarget
	}
	target := cfg.Tools.Browser.Targets[config.BrowserDefaultTarget]
	if !target.Enabled || target.EffectivePlacement() != config.BrowserPlacementGateway ||
		target.EffectiveProvider() != config.BrowserProviderLocal || target.Driver != config.BrowserDriverPlaywrightLibrary ||
		target.Fallback != (config.BrowserFallbackConfig{}) || len(target.Profiles) != 1 {
		return ErrUnsafeTarget
	}
	profile, ok := target.Profiles[target.EffectiveDefaultProfile()]
	if !ok || config.ValidateLoopbackFixtureBrowserProfile(profile) != nil || profile.DryRun ||
		!slices.Equal(profile.AllowedAgents, []string{preset.ExpectedAgent}) ||
		!slices.Equal(profile.AllowedActors, []string{"mintclaw:mintclaw-user"}) ||
		profile.Runtime != browserEvalRuntime(preset.IsolationRoot) {
		return ErrUnsafeTarget
	}
	for _, rule := range profile.Policy.Rules {
		if rule.Decision != browserpolicy.DecisionAllow {
			continue
		}
		if len(rule.Match.Effects) == 0 {
			return ErrUnsafeTarget
		}
		for _, effect := range rule.Match.Effects {
			if effect != "read" && effect != "local_edit" {
				return ErrUnsafeTarget
			}
		}
	}
	return nil
}

func browserEvalRuntime(home string) config.BrowserProfileRuntimeConfig {
	return config.BrowserProfileRuntimeConfig{
		EphemeralRoot: filepath.Join(home, browserRuntimeDirectory, "ephemeral"),
		LockFile:      filepath.Join(home, browserRuntimeDirectory, "locks", "profile.lock"),
	}
}

func browserEvalProfile(cfg *config.Config) (string, config.BrowserProfileConfig) {
	target := cfg.Tools.Browser.Targets[config.BrowserDefaultTarget]
	name := target.EffectiveDefaultProfile()
	return name, target.Profiles[name]
}

// Preparation reserves no long-lived port and starts no browser. Run must
// acquire the selected address before launching a gateway; a collision refuses
// admission instead of adopting the service that won the race.
func prepareBrowserRuntime(root *os.Root, cfg *config.Config, home string) error {
	if !cfg.Tools.Browser.Enabled {
		return nil
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return ErrPreparation
	}
	origin := "http://" + listener.Addr().String()
	if listener.Close() != nil {
		return ErrPreparation
	}
	if _, err = config.NormalizeBrowserNetworkOrigin(config.BrowserNetworkLoopbackFixture, origin); err != nil {
		return ErrPreparation
	}
	for _, directory := range []string{browserRuntimeDirectory, browserRuntimeDirectory + "/ephemeral", browserRuntimeDirectory + "/locks"} {
		if root.Mkdir(directory, 0o700) != nil {
			return ErrPreparation
		}
	}
	target := cfg.Tools.Browser.Targets[config.BrowserDefaultTarget]
	name, profile := browserEvalProfile(cfg)
	previous := profile.AllowedOrigins[0]
	profile.AllowedOrigins = []string{origin}
	profile.Runtime = browserEvalRuntime(home)
	for index := range profile.Policy.Rules {
		for position, value := range profile.Policy.Rules[index].Match.Origins {
			if value == previous {
				profile.Policy.Rules[index].Match.Origins[position] = origin
			}
		}
	}
	target.Profiles[name] = profile
	cfg.Tools.Browser.Targets[config.BrowserDefaultTarget] = target
	return nil
}

func validateFreshBrowserRuntime(root *os.Root) error {
	for _, level := range []struct {
		path     string
		children []string
	}{
		{browserRuntimeDirectory, []string{"ephemeral", "locks"}},
		{browserRuntimeDirectory + "/ephemeral", nil},
		{browserRuntimeDirectory + "/locks", nil},
	} {
		before, err := root.Lstat(level.path)
		if err != nil || !before.IsDir() || before.Mode().Perm() != 0o700 {
			return ErrOwnedGateway
		}
		directory, err := root.Open(level.path)
		if err != nil {
			return ErrOwnedGateway
		}
		opened, statErr := directory.Stat()
		entries, readErr := directory.ReadDir(4)
		closeErr := directory.Close()
		after, pathErr := root.Lstat(level.path)
		if statErr != nil || pathErr != nil || !sameCensusFile(before, opened) || !sameCensusFile(opened, after) ||
			readErr != nil && !errors.Is(readErr, io.EOF) || closeErr != nil || len(entries) != len(level.children) {
			return ErrOwnedGateway
		}
		for _, entry := range entries {
			if !slices.Contains(level.children, entry.Name()) {
				return ErrOwnedGateway
			}
		}
	}
	return nil
}

func bindOwnedBrowserFixture(preset Preset, home string) (*browserFixtureHost, error) {
	profile, err := readPreparedProfile(preset, home)
	if err != nil || validateConversationProfile(profile.config, preset, profile.workspace) != nil ||
		validateFreshOwnedProfile(profile) != nil {
		return nil, ErrOwnedGateway
	}
	if !profile.config.Tools.Browser.Enabled {
		return nil, nil
	}
	_, browser := browserEvalProfile(profile.config)
	origin, err := url.Parse(browser.AllowedOrigins[0])
	if err != nil {
		return nil, ErrOwnedGateway
	}
	host, err := newBrowserFixtureHost(origin.Host)
	if err != nil {
		return nil, ErrOwnedGateway
	}
	return host, nil
}

func browserFixtureMatches(cfg *config.Config, host *browserFixtureHost) bool {
	if !cfg.Tools.Browser.Enabled {
		return host == nil
	}
	if host == nil {
		return false
	}
	_, profile := browserEvalProfile(cfg)
	if !slices.Equal(profile.AllowedOrigins, []string{host.origin}) ||
		strings.TrimPrefix(host.origin, "http://") != host.listener.Addr().String() {
		return false
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.closed || host.failed {
		return false
	}
	select {
	case <-host.done:
		return false
	default:
		return true
	}
}
