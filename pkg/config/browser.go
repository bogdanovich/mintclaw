package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
)

const (
	BrowserDriverPlaywrightMCP    = "playwright_mcp"
	BrowserPlacementGateway       = "gateway"
	BrowserPlacementNode          = "node"
	BrowserProfileManaged         = "managed"
	BrowserNetworkExactOrigins    = "exact_origins"
	BrowserNetworkPublicWeb       = "public_web"
	BrowserNetworkAnyHTTP         = "any_http"
	BrowserCapabilityFullAccess   = browserpolicy.CapabilityFullAccess
	BrowserCapabilityRestricted   = browserpolicy.CapabilityRestricted
	BrowserApprovalNone           = browserpolicy.ApprovalNone
	BrowserApprovalModelRequested = browserpolicy.ApprovalModelRequested
	BrowserApprovalAlwaysCommit   = browserpolicy.ApprovalAlwaysCommit
	BrowserApprovalPolicy         = browserpolicy.ApprovalPolicy

	BrowserMaxSessions          = 1
	BrowserMaxTabs              = 4
	BrowserMaxSessionSeconds    = 60 * 60
	BrowserMaxIdleSeconds       = 10 * 60
	BrowserMaxActionSeconds     = 60
	BrowserMaxSnapshotBytes     = 256 * 1024
	BrowserMaxScreenshotBytes   = 8 * 1024 * 1024
	BrowserMaxUploadBytes       = 32 * 1024 * 1024
	BrowserMaxDownloadBytes     = 32 * 1024 * 1024
	BrowserMaxSnapshotRefs      = 500
	BrowserMaxTextInputBytes    = 16 * 1024
	BrowserMaxToolResultBytes   = 320 * 1024
	BrowserMaxRetentionSeconds  = 7 * 24 * 60 * 60
	BrowserMaxPreparedSeconds   = 5 * 60
	BrowserDefaultTarget        = "gateway"
	BrowserDefaultProfile       = "managed"
	BrowserMaxConfiguredOrigins = 64
)

// BrowserToolResultEnvelopeBytes reserves encoded space for bounded page and
// dialog metadata, opaque authority IDs, tab metadata, limits, and the
// browser_act wrapper. Snapshot content is budgeted separately.
const BrowserToolResultEnvelopeBytes = 64 * 1024

var (
	browserAliasPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	browserPrincipalPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

type BrowserToolsConfig struct {
	Enabled       bool                           `json:"enabled"                  yaml:"-"`
	Agents        []string                       `json:"agents,omitempty"         yaml:"-"`
	DefaultTarget string                         `json:"default_target,omitempty" yaml:"-"`
	Targets       map[string]BrowserTargetConfig `json:"targets,omitempty"        yaml:"-"`
	Limits        BrowserLimitsConfig            `json:"limits,omitempty"         yaml:"-"`
}

// EffectiveDefaultTarget returns the configured target preference without
// deriving preference from map or presentation order. Existing single-target
// and canonical gateway configurations retain their previous behavior.
func (cfg BrowserToolsConfig) EffectiveDefaultTarget() string {
	if cfg.DefaultTarget != "" {
		return cfg.DefaultTarget
	}
	if target, ok := cfg.Targets[BrowserDefaultTarget]; ok && target.Enabled {
		return BrowserDefaultTarget
	}
	only := ""
	for name, target := range cfg.Targets {
		if !target.Enabled {
			continue
		}
		if only != "" {
			return ""
		}
		only = name
	}
	return only
}

type BrowserTargetConfig struct {
	Enabled      bool                            `json:"enabled"                 yaml:"-"`
	Placement    string                          `json:"placement,omitempty"     yaml:"-"`
	NodeTarget   string                          `json:"node_target,omitempty"   yaml:"-"`
	Driver       string                          `json:"driver,omitempty"        yaml:"-"`
	DriverServer string                          `json:"driver_server,omitempty" yaml:"-"`
	Profiles     map[string]BrowserProfileConfig `json:"profiles,omitempty"      yaml:"-"`
}

func (target BrowserTargetConfig) EffectivePlacement() string {
	if target.Placement == "" {
		return BrowserPlacementGateway
	}
	return target.Placement
}

type BrowserProfileConfig struct {
	Enabled              bool                        `json:"enabled"                          yaml:"-"`
	Revision             string                      `json:"revision,omitempty"               yaml:"-"`
	Mode                 string                      `json:"mode,omitempty"                   yaml:"-"`
	AllowedAgents        []string                    `json:"allowed_agents,omitempty"         yaml:"-"`
	AllowedActors        []string                    `json:"allowed_actors,omitempty"         yaml:"-"`
	NetworkMode          string                      `json:"network_mode,omitempty"           yaml:"-"`
	CapabilityMode       string                      `json:"capability_mode,omitempty"        yaml:"-"`
	ApprovalMode         string                      `json:"approval_mode,omitempty"          yaml:"-"`
	DryRun               bool                        `json:"dry_run"                          yaml:"-"`
	AllowApprovedActions bool                        `json:"allow_approved_actions,omitempty" yaml:"-"`
	AllowedOrigins       []string                    `json:"allowed_origins,omitempty"        yaml:"-"`
	Policy               *browserpolicy.Policy       `json:"policy,omitempty"                 yaml:"-"`
	Runtime              BrowserProfileRuntimeConfig `json:"runtime,omitempty"                yaml:"-"`
}

// BrowserProfileRuntimeConfig is execution-host-only profile authority. Its
// values are never projected into browser tool results or node catalogs.
type BrowserProfileRuntimeConfig struct {
	ProfileDirectory string `json:"profile_directory,omitempty" yaml:"-"`
	LockFile         string `json:"lock_file,omitempty"         yaml:"-"`
	Headed           bool   `json:"headed"                      yaml:"-"`
}

// CanonicalAuthority reports whether a profile uses the B4 authority schema.
// Empty authority remains temporarily readable only for the Phase 1
// production cutover and is removed by the follow-up cleanup change.
func (profile BrowserProfileConfig) CanonicalAuthority() bool {
	return profile.Revision != "" || len(profile.AllowedAgents) != 0 ||
		len(profile.AllowedActors) != 0 || profile.Runtime != (BrowserProfileRuntimeConfig{})
}

type BrowserLimitsConfig struct {
	Sessions        int `json:"sessions,omitempty"          yaml:"-"`
	Tabs            int `json:"tabs,omitempty"              yaml:"-"`
	SessionSeconds  int `json:"session_seconds,omitempty"   yaml:"-"`
	IdleSeconds     int `json:"idle_seconds,omitempty"      yaml:"-"`
	PreparedSeconds int `json:"prepared_seconds,omitempty"  yaml:"-"`
	ActionSeconds   int `json:"action_seconds,omitempty"    yaml:"-"`
	SnapshotBytes   int `json:"snapshot_bytes,omitempty"    yaml:"-"`
	ScreenshotBytes int `json:"screenshot_bytes,omitempty"  yaml:"-"`
	UploadBytes     int `json:"upload_bytes,omitempty"      yaml:"-"`
	DownloadBytes   int `json:"download_bytes,omitempty"    yaml:"-"`
	SnapshotRefs    int `json:"snapshot_refs,omitempty"     yaml:"-"`
	TextInputBytes  int `json:"text_input_bytes,omitempty"  yaml:"-"`
	ToolResultBytes int `json:"tool_result_bytes,omitempty" yaml:"-"`
	RetentionSecs   int `json:"retention_seconds,omitempty" yaml:"-"`
}

func (limits BrowserLimitsConfig) Effective() BrowserLimitsConfig {
	return BrowserLimitsConfig{
		Sessions:        effectiveBrowserLimit(limits.Sessions, BrowserMaxSessions),
		Tabs:            effectiveBrowserLimit(limits.Tabs, BrowserMaxTabs),
		SessionSeconds:  effectiveBrowserLimit(limits.SessionSeconds, BrowserMaxSessionSeconds),
		IdleSeconds:     effectiveBrowserLimit(limits.IdleSeconds, BrowserMaxIdleSeconds),
		PreparedSeconds: effectiveBrowserLimit(limits.PreparedSeconds, BrowserMaxPreparedSeconds),
		ActionSeconds:   effectiveBrowserLimit(limits.ActionSeconds, BrowserMaxActionSeconds),
		SnapshotBytes:   effectiveBrowserLimit(limits.SnapshotBytes, BrowserMaxSnapshotBytes),
		ScreenshotBytes: effectiveBrowserLimit(limits.ScreenshotBytes, BrowserMaxScreenshotBytes),
		UploadBytes:     effectiveBrowserLimit(limits.UploadBytes, BrowserMaxUploadBytes),
		DownloadBytes:   effectiveBrowserLimit(limits.DownloadBytes, BrowserMaxDownloadBytes),
		SnapshotRefs:    effectiveBrowserLimit(limits.SnapshotRefs, BrowserMaxSnapshotRefs),
		TextInputBytes:  effectiveBrowserLimit(limits.TextInputBytes, BrowserMaxTextInputBytes),
		ToolResultBytes: effectiveBrowserLimit(limits.ToolResultBytes, BrowserMaxToolResultBytes),
		RetentionSecs:   effectiveBrowserLimit(limits.RetentionSecs, BrowserMaxRetentionSeconds),
	}
}

func (cfg BrowserToolsConfig) PolicyRevision() (string, error) {
	canonical := cfg
	canonical.Targets = make(map[string]BrowserTargetConfig, len(cfg.Targets))
	for targetName, target := range cfg.Targets {
		profiles := make(map[string]BrowserProfileConfig, len(target.Profiles))
		for profileName, profile := range target.Profiles {
			if profile.Policy != nil {
				normalized, err := browserpolicy.NormalizePolicy(*profile.Policy)
				if err != nil {
					return "", fmt.Errorf("normalize browser policy for %s/%s: %w", targetName, profileName, err)
				}
				profile.Policy = &normalized
			}
			profiles[profileName] = profile
		}
		target.Profiles = profiles
		canonical.Targets[targetName] = target
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("encode browser policy revision: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func effectiveBrowserLimit(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

func (cfg *Config) ValidateBrowserConfig() error {
	if cfg == nil {
		return errors.New("browser config requires a root config")
	}
	browser := cfg.Tools.Browser
	if err := validateBrowserLimits(browser.Limits); err != nil {
		return fmt.Errorf("invalid tools.browser.limits: %w", err)
	}
	if len(browser.Agents) > 64 {
		return errors.New("invalid tools.browser.agents: exceeds 64 entries")
	}
	seenAgents := make(map[string]struct{}, len(browser.Agents))
	for _, agent := range browser.Agents {
		if !browserAliasPattern.MatchString(agent) {
			return fmt.Errorf("invalid tools.browser agent alias %q", agent)
		}
		if _, exists := seenAgents[agent]; exists {
			return fmt.Errorf("duplicate tools.browser agent alias %q", agent)
		}
		seenAgents[agent] = struct{}{}
	}
	if len(browser.Targets) > 8 {
		return errors.New("invalid tools.browser.targets: exceeds 8 entries")
	}
	for targetName, target := range browser.Targets {
		if err := cfg.validateBrowserTarget(targetName, target); err != nil {
			return err
		}
	}
	if browser.DefaultTarget != "" {
		if !browserAliasPattern.MatchString(browser.DefaultTarget) {
			return fmt.Errorf("invalid tools.browser.default_target %q", browser.DefaultTarget)
		}
		target, ok := browser.Targets[browser.DefaultTarget]
		if !ok || !target.Enabled || !hasEnabledBrowserProfile(map[string]BrowserTargetConfig{
			browser.DefaultTarget: target,
		}) {
			return fmt.Errorf(
				"tools.browser.default_target %q must reference an enabled target with an enabled profile",
				browser.DefaultTarget,
			)
		}
	}
	if browser.Enabled && len(browser.Agents) == 0 {
		return errors.New("tools.browser.enabled requires at least one agent")
	}
	if browser.Enabled && !hasEnabledBrowserProfile(browser.Targets) {
		return errors.New("tools.browser.enabled requires an enabled target and profile")
	}
	return nil
}

func (cfg *Config) validateBrowserTarget(name string, target BrowserTargetConfig) error {
	if !browserAliasPattern.MatchString(name) {
		return fmt.Errorf("invalid tools.browser target alias %q", name)
	}
	if target.Driver != "" && target.Driver != BrowserDriverPlaywrightMCP {
		return fmt.Errorf("invalid tools.browser.targets.%s.driver %q", name, target.Driver)
	}
	if len(target.Profiles) > 8 {
		return fmt.Errorf("invalid tools.browser.targets.%s.profiles: exceeds 8 entries", name)
	}
	enabledProfiles := 0
	for profileName, profile := range target.Profiles {
		if err := validateBrowserProfile(name, profileName, profile); err != nil {
			return err
		}
		if profile.Enabled {
			enabledProfiles++
		}
	}
	if enabledProfiles > 1 {
		return fmt.Errorf("browser target %q supports one enabled profile during B4 phase 1", name)
	}
	placement := target.EffectivePlacement()
	switch placement {
	case BrowserPlacementGateway:
		if target.NodeTarget != "" {
			return fmt.Errorf(
				"browser target %q cannot combine gateway placement with node_target",
				name,
			)
		}
	case BrowserPlacementNode:
		if target.Driver != "" || target.DriverServer != "" {
			return fmt.Errorf(
				"browser target %q cannot combine node placement with a local driver",
				name,
			)
		}
		if !validExecutionTargetName(target.NodeTarget) {
			return fmt.Errorf("browser target %q requires a valid node_target", name)
		}
		executionTarget, exists := cfg.Execution.Targets[target.NodeTarget]
		if !exists || executionTarget.Type != "node" {
			return fmt.Errorf(
				"browser target %q references unknown node execution target %q",
				name,
				target.NodeTarget,
			)
		}
	default:
		return fmt.Errorf("browser target %q has unsupported placement %q", name, target.Placement)
	}
	for profileName, profile := range target.Profiles {
		if !profile.Enabled || !profile.CanonicalAuthority() {
			continue
		}
		if err := validateBrowserProfileGrants(profileName, profile, cfg.Tools.Browser.Agents); err != nil {
			return err
		}
		if placement == BrowserPlacementNode {
			if profile.Runtime != (BrowserProfileRuntimeConfig{}) {
				return fmt.Errorf(
					"browser profile %q runtime must be configured on the companion host",
					profileName,
				)
			}
			continue
		}
		if err := validateGatewayBrowserProfileRuntime(profileName, profile.Runtime); err != nil {
			return err
		}
	}
	if !target.Enabled {
		return nil
	}
	if placement == BrowserPlacementNode {
		if !cfg.Nodes.Enabled {
			return fmt.Errorf("enabled node browser target %q requires nodes.enabled", name)
		}
		if !hasEnabledBrowserProfile(map[string]BrowserTargetConfig{name: target}) {
			return fmt.Errorf("enabled browser target %q requires an enabled profile", name)
		}
		return nil
	}
	if name != BrowserDefaultTarget {
		return fmt.Errorf("B1 supports only the %q browser target", BrowserDefaultTarget)
	}
	if target.Driver != BrowserDriverPlaywrightMCP {
		return fmt.Errorf("enabled browser target %q requires driver %q", name, BrowserDriverPlaywrightMCP)
	}
	if !browserAliasPattern.MatchString(target.DriverServer) {
		return fmt.Errorf("enabled browser target %q requires a valid driver_server", name)
	}
	server, ok := cfg.Tools.MCP.Servers[target.DriverServer]
	if !ok {
		return fmt.Errorf("browser target %q references unknown MCP server template", name)
	}
	if server.Enabled {
		return fmt.Errorf(
			"browser driver server %q must not be enabled in the generic MCP manager",
			target.DriverServer,
		)
	}
	if server.Type != "stdio" {
		return fmt.Errorf("browser driver server %q must use stdio", target.DriverServer)
	}
	if strings.TrimSpace(server.Command) == "" {
		return fmt.Errorf("browser driver server %q requires a command", target.DriverServer)
	}
	canonical := false
	for _, profile := range target.Profiles {
		canonical = canonical || (profile.Enabled && profile.CanonicalAuthority())
	}
	if canonical {
		if strings.TrimSpace(server.ExclusiveLockFile) != "" {
			return fmt.Errorf(
				"browser driver server %q cannot set profile-owned exclusive_lock_file",
				target.DriverServer,
			)
		}
		for _, argument := range server.Args {
			if browserProfileOwnedDriverArgument(argument) {
				return fmt.Errorf(
					"browser driver server %q contains profile-owned argument %q",
					target.DriverServer, argument,
				)
			}
		}
	} else if strings.TrimSpace(server.ExclusiveLockFile) == "" {
		return fmt.Errorf("browser driver server %q requires exclusive_lock_file", target.DriverServer)
	}
	if !hasEnabledBrowserProfile(map[string]BrowserTargetConfig{name: target}) {
		return fmt.Errorf("enabled browser target %q requires an enabled profile", name)
	}
	return nil
}

func validateBrowserProfile(targetName, name string, profile BrowserProfileConfig) error {
	if !browserAliasPattern.MatchString(name) {
		return fmt.Errorf("invalid tools.browser.targets.%s profile alias %q", targetName, name)
	}
	if !profile.Enabled && profile.CanonicalAuthority() {
		return fmt.Errorf("disabled browser profile %q cannot configure authority", name)
	}
	if profile.Mode != "" && profile.Mode != BrowserProfileManaged {
		return fmt.Errorf("browser profile %q supports only mode %q", name, BrowserProfileManaged)
	}
	switch profile.CapabilityMode {
	case BrowserCapabilityFullAccess, BrowserCapabilityRestricted:
	default:
		return fmt.Errorf("browser profile %q has unsupported capability_mode %q", name, profile.CapabilityMode)
	}
	switch profile.ApprovalMode {
	case BrowserApprovalNone, BrowserApprovalModelRequested, BrowserApprovalAlwaysCommit, BrowserApprovalPolicy:
	default:
		return fmt.Errorf("browser profile %q has unsupported approval_mode %q", name, profile.ApprovalMode)
	}
	restricted := profile.CapabilityMode == BrowserCapabilityRestricted
	policyApproval := profile.ApprovalMode == BrowserApprovalPolicy
	if restricted != policyApproval || restricted != (profile.Policy != nil) {
		return fmt.Errorf(
			"browser profile %q requires capability_mode restricted, approval_mode policy, and policy together",
			name,
		)
	}
	if profile.Policy != nil {
		if _, err := browserpolicy.NormalizePolicy(*profile.Policy); err != nil {
			return fmt.Errorf("invalid browser profile %q policy: %w", name, err)
		}
	}
	networkMode := profile.NetworkMode
	if profile.Enabled && networkMode == "" {
		return fmt.Errorf("enabled browser profile %q requires network_mode", name)
	}
	if networkMode != BrowserNetworkExactOrigins && networkMode != BrowserNetworkPublicWeb &&
		networkMode != BrowserNetworkAnyHTTP && networkMode != "" {
		return fmt.Errorf("browser profile %q has unsupported network_mode %q", name, profile.NetworkMode)
	}
	if len(profile.AllowedOrigins) > BrowserMaxConfiguredOrigins {
		return fmt.Errorf("browser profile %q exceeds %d allowed origins", name, BrowserMaxConfiguredOrigins)
	}
	seen := make(map[string]struct{}, len(profile.AllowedOrigins))
	for _, rawOrigin := range profile.AllowedOrigins {
		origin, err := NormalizeBrowserOrigin(rawOrigin)
		if err != nil {
			return fmt.Errorf("invalid browser profile %q origin: %w", name, err)
		}
		if _, exists := seen[origin]; exists {
			return fmt.Errorf("browser profile %q contains duplicate origin %q", name, origin)
		}
		seen[origin] = struct{}{}
	}
	if profile.Enabled {
		if !profile.CanonicalAuthority() && name != BrowserDefaultProfile {
			return fmt.Errorf("B1 supports only the %q browser profile", BrowserDefaultProfile)
		}
		if profile.CanonicalAuthority() && !browserPrincipalPattern.MatchString(profile.Revision) {
			return fmt.Errorf("browser profile %q requires a valid revision", name)
		}
		if profile.Mode != BrowserProfileManaged {
			return fmt.Errorf("enabled browser profile %q requires mode %q", name, BrowserProfileManaged)
		}
		if profile.DryRun == profile.AllowApprovedActions {
			return fmt.Errorf(
				"enabled browser profile %q requires exactly one of dry_run or allow_approved_actions",
				name,
			)
		}
		if networkMode == BrowserNetworkExactOrigins && len(profile.AllowedOrigins) == 0 {
			return fmt.Errorf("enabled browser profile %q requires allowed_origins", name)
		}
		if (networkMode == BrowserNetworkPublicWeb || networkMode == BrowserNetworkAnyHTTP) &&
			len(profile.AllowedOrigins) != 0 {
			return fmt.Errorf("enabled %s browser profile %q must not set allowed_origins", networkMode, name)
		}
	}
	return nil
}

func validateBrowserProfileGrants(
	profileName string,
	profile BrowserProfileConfig,
	globalAgents []string,
) error {
	if len(profile.AllowedAgents) == 0 || len(profile.AllowedActors) == 0 {
		return fmt.Errorf(
			"browser profile %q requires non-empty allowed_agents and allowed_actors",
			profileName,
		)
	}
	global := make(map[string]struct{}, len(globalAgents))
	for _, agent := range globalAgents {
		global[agent] = struct{}{}
	}
	seenAgents := make(map[string]struct{}, len(profile.AllowedAgents))
	for _, agent := range profile.AllowedAgents {
		if !browserAliasPattern.MatchString(agent) {
			return fmt.Errorf("browser profile %q contains invalid allowed agent", profileName)
		}
		if _, duplicate := seenAgents[agent]; duplicate {
			return fmt.Errorf("browser profile %q contains duplicate allowed agent", profileName)
		}
		if _, granted := global[agent]; !granted {
			return fmt.Errorf(
				"browser profile %q agent %q is not granted by tools.browser.agents",
				profileName, agent,
			)
		}
		seenAgents[agent] = struct{}{}
	}
	seenActors := make(map[string]struct{}, len(profile.AllowedActors))
	for _, actor := range profile.AllowedActors {
		if !browserPrincipalPattern.MatchString(actor) {
			return fmt.Errorf("browser profile %q contains invalid allowed actor", profileName)
		}
		if _, duplicate := seenActors[actor]; duplicate {
			return fmt.Errorf("browser profile %q contains duplicate allowed actor", profileName)
		}
		seenActors[actor] = struct{}{}
	}
	return nil
}

func validateGatewayBrowserProfileRuntime(name string, runtime BrowserProfileRuntimeConfig) error {
	if !filepath.IsAbs(runtime.ProfileDirectory) || !filepath.IsAbs(runtime.LockFile) {
		return fmt.Errorf("browser profile %q runtime paths must be absolute", name)
	}
	profileDirectory := filepath.Clean(runtime.ProfileDirectory)
	lockFile := filepath.Clean(runtime.LockFile)
	if profileDirectory == string(filepath.Separator) || lockFile == string(filepath.Separator) ||
		profileDirectory == lockFile {
		return fmt.Errorf("browser profile %q runtime paths conflict", name)
	}
	relative, err := filepath.Rel(profileDirectory, lockFile)
	if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("browser profile %q lock_file must be outside profile_directory", name)
	}
	return nil
}

func browserProfileOwnedDriverArgument(argument string) bool {
	for _, owned := range []string{
		"--extension", "--user-data-dir", "--storage-state", "--isolated", "--headless",
	} {
		if argument == owned || strings.HasPrefix(argument, owned+"=") {
			return true
		}
	}
	return false
}

func NormalizeBrowserOrigin(raw string) (string, error) {
	return browserpolicy.NormalizePublicOrigin(raw)
}

// NormalizeBrowserHTTPOrigin canonicalizes an HTTP or HTTPS origin without
// applying an address-scope policy. It is used only after an operator has
// explicitly selected the high-risk any_http mode, and for validating durable
// browser state whose network authority is checked separately.
func NormalizeBrowserHTTPOrigin(raw string) (string, error) {
	return browserpolicy.NormalizeHTTPOrigin(raw)
}

// IsPublicBrowserIP applies the browser network boundary to a resolved
// address. It denies every block in IANA's IPv4 and IPv6 special-purpose
// registries, cloud-provider metadata endpoints, and non-unicast addresses.
func IsPublicBrowserIP(ip net.IP) bool {
	return browserpolicy.IsPublicIP(ip)
}

func validateBrowserLimits(limits BrowserLimitsConfig) error {
	checks := []struct {
		name  string
		value int
		max   int
	}{
		{"sessions", limits.Sessions, BrowserMaxSessions},
		{"tabs", limits.Tabs, BrowserMaxTabs},
		{"session_seconds", limits.SessionSeconds, BrowserMaxSessionSeconds},
		{"idle_seconds", limits.IdleSeconds, BrowserMaxIdleSeconds},
		{"prepared_seconds", limits.PreparedSeconds, BrowserMaxPreparedSeconds},
		{"action_seconds", limits.ActionSeconds, BrowserMaxActionSeconds},
		{"snapshot_bytes", limits.SnapshotBytes, BrowserMaxSnapshotBytes},
		{"screenshot_bytes", limits.ScreenshotBytes, BrowserMaxScreenshotBytes},
		{"upload_bytes", limits.UploadBytes, BrowserMaxUploadBytes},
		{"download_bytes", limits.DownloadBytes, BrowserMaxDownloadBytes},
		{"snapshot_refs", limits.SnapshotRefs, BrowserMaxSnapshotRefs},
		{"text_input_bytes", limits.TextInputBytes, BrowserMaxTextInputBytes},
		{"tool_result_bytes", limits.ToolResultBytes, BrowserMaxToolResultBytes},
		{"retention_seconds", limits.RetentionSecs, BrowserMaxRetentionSeconds},
	}
	for _, check := range checks {
		if check.value < 0 || check.value > check.max {
			return fmt.Errorf("%s must be between 0 and %d", check.name, check.max)
		}
	}
	if limits.ToolResultBytes != 0 && limits.ToolResultBytes < BrowserToolResultEnvelopeBytes {
		return fmt.Errorf(
			"tool_result_bytes must be 0 or at least %d",
			BrowserToolResultEnvelopeBytes,
		)
	}
	effective := limits.Effective()
	if effective.IdleSeconds > effective.SessionSeconds {
		return errors.New("idle_seconds must not exceed session_seconds")
	}
	if effective.PreparedSeconds > effective.SessionSeconds {
		return errors.New("prepared_seconds must not exceed session_seconds")
	}
	return nil
}

func hasEnabledBrowserProfile(targets map[string]BrowserTargetConfig) bool {
	for _, target := range targets {
		if !target.Enabled {
			continue
		}
		for _, profile := range target.Profiles {
			if profile.Enabled {
				return true
			}
		}
	}
	return false
}
