package eval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/internal/gatewayclient"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/health"
	"github.com/bogdanovich/mintclaw/pkg/pid"
)

var ErrUnsafeTarget = errors.New("eval: prepared isolated target not admitted")

type admittedTarget struct {
	preset       Preset
	config       *config.Config
	probe        health.RuntimeProbe
	serviceToken string
}

func admitPreparedTarget(ctx context.Context, preset Preset, home string) (admittedTarget, error) {
	var target admittedTarget
	if preset.Validate() != nil || runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return target, ErrUnsafeTarget
	}
	physicalHome, err := privateHome(preset.IsolationRoot)
	if err != nil || !samePhysicalPath(home, physicalHome) || !samePhysicalPath(config.GetHome(), physicalHome) ||
		!samePhysicalPath(preset.ConfigPath, filepath.Join(physicalHome, "config.json")) {
		return target, ErrUnsafeTarget
	}
	workspace, err := filepath.EvalSymlinks(preset.ExpectedWorkspace)
	if err != nil || !pathWithin(physicalHome, workspace) {
		return target, ErrUnsafeTarget
	}
	root, err := os.OpenRoot(physicalHome)
	if err != nil {
		return target, ErrUnsafeTarget
	}
	defer func() { _ = root.Close() }()
	data, err := readBoundedFile(root, "config.json", MaxManifestBytes)
	if err != nil {
		return target, ErrUnsafeTarget
	}
	// Validate with the native byte decoder first, without its file loader's
	// diagnostic logging. The prepared profile is operator-owned and immutable
	// for a run; concurrent hostile filesystem mutation is not certified here.
	var preview config.Config
	if config.DecodeCurrentConfig(data, &preview) != nil {
		return target, ErrUnsafeTarget
	}
	cfg, err := config.LoadConfigReadOnly(preset.ConfigPath)
	if err != nil || validateConversationProfile(cfg, preset, workspace) != nil {
		return target, ErrUnsafeTarget
	}
	after, err := readBoundedFile(root, "config.json", MaxManifestBytes)
	if err != nil || digest(data) != digest(after) {
		return target, ErrUnsafeTarget
	}
	pidData, err := readBoundedFile(root, ".mintclaw.pid", 4096)
	if err != nil {
		return target, ErrUnsafeTarget
	}
	process, err := DecodeManifest[pid.PidFileData](pidData)
	token, tokenErr := hex.DecodeString(process.Token)
	if err != nil || tokenErr != nil || len(token) != 16 || process.PID <= 1 ||
		process.Port != cfg.Gateway.Port || !pid.ProcessRunning(process.PID) {
		return target, ErrUnsafeTarget
	}
	probe, err := requestRuntimeProbe(ctx, cfg, process.Token)
	if err != nil {
		return target, ErrUnsafeTarget
	}
	fingerprint, err := preparedRuntimeFingerprint(cfg)
	configPath, pathErr := filepath.EvalSymlinks(preset.ConfigPath)
	if err != nil || pathErr != nil || probe.Version != 1 || probe.PID != process.PID ||
		probe.ConfigSHA256 != fingerprint || probe.HomeSHA256 != admissionHash(physicalHome) ||
		probe.ConfigPathSHA256 != admissionHash(configPath) || probe.InstanceSHA256 != admissionHash(process.Token) ||
		!slices.Contains([]string{"warn", "error", "fatal"}, probe.LogLevel) ||
		probe.GOOS != runtime.GOOS || probe.GOARCH != runtime.GOARCH {
		return target, ErrUnsafeTarget
	}
	target = admittedTarget{preset: preset, config: cfg, probe: probe, serviceToken: process.Token}
	return target, nil
}

func validateConversationProfile(cfg *config.Config, preset Preset, workspace string) error {
	if cfg == nil || cfg.ValidateModelList() != nil || cfg.ValidateAgents() != nil || len(cfg.Agents.List) != 1 ||
		!cfg.Agents.Defaults.RestrictToWorkspace || cfg.Agents.Defaults.AllowReadOutsideWorkspace ||
		cfg.WorkspacePath() != workspace || preset.ExpectedWorkspace != workspace ||
		cfg.Gateway.HotReload || cfg.Gateway.Deploy.Enabled || cfg.Gateway.SafeRestart.Enabled ||
		cfg.Gateway.CodingRemote.Enabled || cfg.Coding.Remote.Enabled || cfg.Nodes.Enabled ||
		cfg.Nodes.TerminalEnabled || cfg.Heartbeat.Enabled || cfg.Devices.Enabled || cfg.Hooks.Enabled ||
		cfg.Tools.Cron.Enabled || cfg.Tools.Exec.Enabled || cfg.Tools.MCP.Enabled ||
		cfg.Tools.Browser.Enabled || cfg.Tools.Document.Enabled ||
		cfg.Diagnostics.TraceCapture.Enabled ||
		cfg.Tools.Approval.EffectiveMode() != config.ToolApprovalModeRequired ||
		len(cfg.Tools.Approval.BypassNodeTargets) != 0 || len(cfg.Tools.AllowReadPaths) != 0 ||
		len(cfg.Tools.AllowWritePaths) != 0 || len(cfg.Session.IdentityLinks) != 0 ||
		cfg.Agents.Defaults.TargetPolicy != nil || cfg.Agents.Defaults.Subagents != nil ||
		cfg.Agents.Defaults.Routing != nil && cfg.Agents.Defaults.Routing.Enabled ||
		cfg.Agents.Dispatch != nil && len(cfg.Agents.Dispatch.Rules) != 0 ||
		len(cfg.Agents.Defaults.ContextManagerConfig) != 0 {
		return ErrUnsafeTarget
	}
	for _, channel := range cfg.Channels {
		if channel != nil && channel.Enabled && channel.Type != config.ChannelMintClaw {
			return ErrUnsafeTarget
		}
	}
	if _, err := gatewayclient.ChannelSettings(cfg); err != nil {
		return ErrUnsafeTarget
	}
	agent := cfg.Agents.List[0]
	if agent.ID != preset.ExpectedAgent || agent.Workspace != "" && !samePhysicalPath(agent.Workspace, workspace) ||
		agent.TargetPolicy != nil || agent.Subagents != nil || agent.ToolPolicy == nil ||
		agent.ToolPolicy.Default != config.AgentCapabilityDefaultDeny ||
		agent.MCPServerPolicy == nil || agent.MCPServerPolicy.Default != config.AgentCapabilityDefaultDeny ||
		len(agent.MCPServerPolicy.Allow) != 0 {
		return ErrUnsafeTarget
	}
	for _, tool := range agent.ToolPolicy.Allow {
		if tool != "request_user_input" {
			return ErrUnsafeTarget
		}
	}
	for _, capability := range preset.Capabilities {
		if capability != "conversation" && capability != "interactions" ||
			capability == "interactions" && (!cfg.Tools.RequestUserInput.Enabled ||
				!slices.Contains(agent.ToolPolicy.Allow, "request_user_input") || len(agent.ToolPolicy.Deny) != 0) {
			return ErrUnsafeTarget
		}
	}
	names := make(map[string]bool)
	for _, model := range cfg.ModelList {
		if !slices.Contains([]string{"openai", "anthropic", "gemini"}, model.Provider) ||
			len(model.ExtraBody) != 0 || model.Enabled && names[model.ModelName] {
			return ErrUnsafeTarget
		}
		if model.Enabled {
			names[model.ModelName] = true
		}
	}
	if !names[cfg.Agents.Defaults.GetModelName()] {
		return ErrUnsafeTarget
	}
	if cfg.Tools.Web.PreferNative {
		return ErrUnsafeTarget
	}
	return nil
}

func requestRuntimeProbe(ctx context.Context, cfg *config.Config, token string) (health.RuntimeProbe, error) {
	var result health.RuntimeProbe
	endpoint, err := gatewayclient.Endpoint(cfg, "preflight")
	if err != nil {
		return result, ErrUnsafeTarget
	}
	endpoint.Scheme, endpoint.Path, endpoint.RawQuery = "http", "/runtime", ""
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return result, ErrUnsafeTarget
	}
	request.Header.Set("Authorization", "Bearer "+token)
	transport := &http.Transport{
		DialContext:       (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		DisableKeepAlives: true,
	}
	defer transport.CloseIdleConnections()
	client := http.Client{
		Transport: transport, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(request)
	if err != nil {
		return result, ErrUnsafeTarget
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return result, ErrUnsafeTarget
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 8193))
	if err != nil || len(data) > 8192 {
		return result, ErrUnsafeTarget
	}
	result, err = DecodeManifest[health.RuntimeProbe](data)
	if err != nil {
		return health.RuntimeProbe{}, ErrUnsafeTarget
	}
	return result, nil
}

func privateHome(name string) (string, error) {
	absolute, err := filepath.Abs(name)
	if err != nil || filepath.Dir(absolute) == absolute {
		return "", ErrUnsafeTarget
	}
	physical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", ErrUnsafeTarget
	}
	info, err := os.Stat(physical)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 || filepath.Dir(physical) == physical {
		return "", ErrUnsafeTarget
	}
	return physical, nil
}

func samePhysicalPath(first, second string) bool {
	if strings.TrimSpace(first) == "" || strings.TrimSpace(second) == "" {
		return false
	}
	one, firstErr := filepath.EvalSymlinks(first)
	two, secondErr := filepath.EvalSymlinks(second)
	if firstErr != nil || secondErr != nil {
		return false
	}
	one, firstErr = filepath.Abs(one)
	two, secondErr = filepath.Abs(two)
	return firstErr == nil && secondErr == nil && one == two
}

func admissionHash(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }

// Gateway startup replaces the default alias with its native model ID and
// supplies the default provider's workspace. Mirror only these two native
// transformations; never mask a policy or routing difference.
func preparedRuntimeFingerprint(cfg *config.Config) (string, error) {
	expected := *cfg
	expected.ModelList = slices.Clone(cfg.ModelList)
	selected, err := cfg.GetModelConfig(cfg.Agents.Defaults.GetModelName())
	if err != nil {
		return "", ErrUnsafeTarget
	}
	for index, model := range expected.ModelList {
		if model == selected {
			copyModel := *model
			if copyModel.Workspace == "" {
				copyModel.Workspace = cfg.WorkspacePath()
			}
			expected.ModelList[index] = &copyModel
			break
		}
	}
	expected.Agents.Defaults.ModelName = selected.Model
	return config.PublicFingerprint(&expected)
}
