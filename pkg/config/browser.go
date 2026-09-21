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
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
)

const (
	BrowserDriverPlaywrightMCP     = "playwright_mcp"
	BrowserDriverPlaywrightLibrary = "playwright_library"
	BrowserProviderLocal           = "local"
	BrowserProviderSteel           = "steel"
	BrowserPlacementGateway        = "gateway"
	BrowserPlacementNode           = "node"
	BrowserPlacementCloud          = "cloud"
	BrowserProfileManaged          = "managed"
	BrowserProfileEphemeral        = "ephemeral"
	BrowserProfileAttachedUser     = "attached_user"
	BrowserAttachedPlaywright      = "playwright_extension"
	BrowserAttachedConsentSession  = "per_session"
	BrowserAttachedOriginExact     = "exact_origins"
	BrowserAttachedOriginAnyHTTP   = "any_http"
	BrowserNetworkExactOrigins     = "exact_origins"
	BrowserNetworkPublicWeb        = "public_web"
	BrowserNetworkAnyHTTP          = "any_http"
	BrowserCapabilityFullAccess    = browserpolicy.CapabilityFullAccess
	BrowserCapabilityRestricted    = browserpolicy.CapabilityRestricted
	BrowserApprovalNone            = browserpolicy.ApprovalNone
	BrowserApprovalModelRequested  = browserpolicy.ApprovalModelRequested
	BrowserApprovalAlwaysCommit    = browserpolicy.ApprovalAlwaysCommit
	BrowserApprovalPolicy          = browserpolicy.ApprovalPolicy

	BrowserMaxSessions                  = 1
	BrowserMaxTabs                      = 4
	BrowserMaxSessionSeconds            = 60 * 60
	BrowserMaxIdleSeconds               = 10 * 60
	BrowserMaxActionSeconds             = 60
	BrowserMaxSnapshotBytes             = 256 * 1024
	BrowserMaxScreenshotBytes           = 8 * 1024 * 1024
	BrowserMaxUploadBytes               = 32 * 1024 * 1024
	BrowserMaxDownloadBytes             = 32 * 1024 * 1024
	BrowserMaxSnapshotRefs              = 500
	BrowserMaxTextInputBytes            = 16 * 1024
	BrowserMaxToolResultBytes           = 320 * 1024
	BrowserMaxRetentionSeconds          = 7 * 24 * 60 * 60
	BrowserMaxPreparedSeconds           = 5 * 60
	BrowserDefaultTarget                = "gateway"
	BrowserDefaultProfile               = "managed"
	BrowserEphemeralLifecycleLockSuffix = browserpolicy.EphemeralLifecycleLockSuffix
	BrowserMaxConfiguredOrigins         = 64
	BrowserMaxAttachConsentSeconds      = BrowserMaxPreparedSeconds
	BrowserMaxExecuteSourceBytes        = 64 * 1024
	BrowserMaxExecuteRuntimeSeconds     = 60
	BrowserMaxExecuteOutputBytes        = 256 * 1024
	BrowserMaxExecuteActions            = 256
	BrowserMaxExecuteMemoryMB           = 256
	BrowserMaxExecuteNetworkRequests    = 256
	BrowserMaxExecuteArtifacts          = 8
	BrowserMaxExecuteArtifactBytes      = BrowserMaxScreenshotBytes
	BrowserMaxExecuteConcurrent         = 1
	// BrowserMaxCloudConcurrency follows the broker's global session bound.
	// Raise both limits together when the broker supports parallel sessions;
	// accepting a larger provider value here would advertise capacity that the
	// first-party lifecycle cannot currently honor.
	BrowserMaxCloudConcurrency          = BrowserMaxSessions
	BrowserMinCloudSessionSeconds       = 15
	BrowserMaxSteelLaunchSessionSeconds = 15 * 60
)

// BrowserToolResultEnvelopeBytes reserves encoded space for bounded page and
// dialog metadata, opaque authority IDs, tab metadata, limits, and the
// browser_act wrapper. Snapshot content is budgeted separately.
const BrowserToolResultEnvelopeBytes = 64 * 1024

var (
	browserAliasPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	browserPrincipalPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

type BrowserToolsConfig struct { //nolint:recvcheck // YAML merge requires a pointer receiver.
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
	Enabled          bool                            `json:"enabled"                     yaml:"-"`
	Placement        string                          `json:"placement,omitempty"         yaml:"-"`
	NodeTarget       string                          `json:"node_target,omitempty"       yaml:"-"`
	Provider         string                          `json:"provider,omitempty"          yaml:"-"`
	Steel            BrowserSteelProviderConfig      `json:"steel,omitempty"             yaml:"-"`
	Driver           string                          `json:"driver,omitempty"            yaml:"-"`
	DriverServer     string                          `json:"driver_server,omitempty"     yaml:"-"`
	DriverExecutable string                          `json:"driver_executable,omitempty" yaml:"-"`
	DriverArguments  []string                        `json:"driver_arguments,omitempty"  yaml:"-"`
	DefaultProfile   string                          `json:"default_profile,omitempty"   yaml:"-"`
	Profiles         map[string]BrowserProfileConfig `json:"profiles,omitempty"          yaml:"-"`
}

// BrowserSteelProviderConfig is gateway-private authority for one Steel cloud
// target. APIKeyRef is kept as a SecureString so public configuration
// projections, logs, and ordinary JSON serialization cannot expose it.
type BrowserSteelProviderConfig struct {
	APIKeyRef                SecureString `json:"api_key_ref,omitzero"                 yaml:"api_key_ref,omitempty"`
	Concurrency              int          `json:"concurrency,omitempty"                yaml:"-"`
	SessionTimeoutSeconds    int          `json:"session_timeout_seconds,omitempty"    yaml:"-"`
	InactivityTimeoutSeconds int          `json:"inactivity_timeout_seconds,omitempty" yaml:"-"`
	MaxBillableSeconds       int          `json:"max_billable_seconds,omitempty"       yaml:"-"`
}

type browserSteelSecurityConfig struct {
	APIKeyRef *SecureString `yaml:"api_key_ref,omitempty"`
}

type browserTargetSecurityConfig struct {
	Steel browserSteelSecurityConfig `yaml:"steel,omitempty"`
}

type browserToolsSecurityConfig struct {
	Targets map[string]browserTargetSecurityConfig `yaml:"targets,omitempty"`
}

// IsZero reports whether the browser subtree has anything to persist in the
// private security document. Public browser policy remains in config.json.
func (cfg BrowserToolsConfig) IsZero() bool {
	for _, target := range cfg.Targets {
		if target.Steel.APIKeyRef.raw != "" || target.Steel.APIKeyRef.resolved != "" {
			return false
		}
	}
	return true
}

// MarshalYAML emits only provider credentials. Browser topology, policy, and
// runtime paths belong to the public configuration document.
func (cfg BrowserToolsConfig) MarshalYAML() (any, error) {
	secure := browserToolsSecurityConfig{}
	for name, target := range cfg.Targets {
		if target.Steel.APIKeyRef.raw == "" && target.Steel.APIKeyRef.resolved == "" {
			continue
		}
		if secure.Targets == nil {
			secure.Targets = make(map[string]browserTargetSecurityConfig)
		}
		key := target.Steel.APIKeyRef
		secure.Targets[name] = browserTargetSecurityConfig{
			Steel: browserSteelSecurityConfig{APIKeyRef: &key},
		}
	}
	return secure, nil
}

// UnmarshalYAML merges provider credentials into targets already declared in
// config.json. A security document cannot create targets or alter policy.
func (cfg *BrowserToolsConfig) UnmarshalYAML(value *yaml.Node) error {
	if value == nil || value.Kind == 0 {
		return nil
	}
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("browser security config must be an object")
	}
	if err := validateBrowserSecurityMapping(value, "browser", "targets"); err != nil {
		return err
	}

	targetsNode := yamlMappingValue(value, "targets")
	if targetsNode == nil {
		return nil
	}
	if targetsNode.Kind != yaml.MappingNode {
		return fmt.Errorf("browser security config targets must be an object")
	}
	seen := make(map[string]struct{}, len(targetsNode.Content)/2)
	for index := 0; index+1 < len(targetsNode.Content); index += 2 {
		name := targetsNode.Content[index].Value
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("duplicate browser security target %q", name)
		}
		seen[name] = struct{}{}
		target, ok := cfg.Targets[name]
		if !ok {
			return fmt.Errorf("browser security config references unconfigured target %q", name)
		}

		targetNode := targetsNode.Content[index+1]
		if targetNode.Kind != yaml.MappingNode {
			return fmt.Errorf("browser security target %q must be an object", name)
		}
		if err := validateBrowserSecurityMapping(targetNode, "browser target "+name, "steel"); err != nil {
			return err
		}
		steelNode := yamlMappingValue(targetNode, "steel")
		if steelNode == nil {
			continue
		}
		if steelNode.Kind != yaml.MappingNode {
			return fmt.Errorf("browser security target %q steel must be an object", name)
		}
		if err := validateBrowserSecurityMapping(
			steelNode,
			"browser target "+name+" steel",
			"api_key_ref",
		); err != nil {
			return err
		}
		keyNode := yamlMappingValue(steelNode, "api_key_ref")
		if keyNode == nil {
			continue
		}
		var key SecureString
		if err := keyNode.Decode(&key); err != nil {
			return fmt.Errorf("decode browser target %q Steel API key reference: %w", name, err)
		}
		target.Steel.APIKeyRef = key
		cfg.Targets[name] = target
	}
	return nil
}

func validateBrowserSecurityMapping(node *yaml.Node, label string, allowed ...string) error {
	known := make(map[string]struct{}, len(allowed))
	for _, field := range allowed {
		known[field] = struct{}{}
	}
	seen := make(map[string]struct{}, len(node.Content)/2)
	for index := 0; index+1 < len(node.Content); index += 2 {
		field := node.Content[index].Value
		if _, duplicate := seen[field]; duplicate {
			return fmt.Errorf("duplicate %s field %q", label, field)
		}
		seen[field] = struct{}{}
		if _, ok := known[field]; !ok {
			return fmt.Errorf("unknown %s field %q", label, field)
		}
	}
	return nil
}

func yamlMappingValue(node *yaml.Node, name string) *yaml.Node {
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == name {
			return node.Content[index+1]
		}
	}
	return nil
}

func (target BrowserTargetConfig) EffectivePlacement() string {
	if target.Placement == "" {
		return BrowserPlacementGateway
	}
	return target.Placement
}

func (target BrowserTargetConfig) EffectiveProvider() string {
	if target.Provider == "" {
		return BrowserProviderLocal
	}
	return target.Provider
}

// EffectiveDefaultProfile returns the configured identity preference without
// deriving preference from profile presentation order. The canonical managed
// profile remains the backward-compatible default; a sole enabled profile is
// deterministic when no canonical profile exists.
func (target BrowserTargetConfig) EffectiveDefaultProfile() string {
	if target.DefaultProfile != "" {
		return target.DefaultProfile
	}
	if profile, ok := target.Profiles[BrowserDefaultProfile]; ok && profile.Enabled {
		return BrowserDefaultProfile
	}
	only := ""
	for name, profile := range target.Profiles {
		if !profile.Enabled {
			continue
		}
		if only != "" {
			return ""
		}
		only = name
	}
	return only
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
	Attached             BrowserAttachedConfig       `json:"attached,omitempty"               yaml:"-"`
	PrivilegedExecution  BrowserExecutionConfig      `json:"privileged_execution,omitempty"   yaml:"-"`
}

// BrowserExecutionConfig is operator-owned authority for the separate
// browser_execute capability. Limits are applied by the execution host and
// cannot be increased by a tool call.
type BrowserExecutionConfig struct {
	Enabled         bool `json:"enabled"                    yaml:"-"`
	RuntimeSeconds  int  `json:"runtime_seconds,omitempty"  yaml:"-"`
	OutputBytes     int  `json:"output_bytes,omitempty"     yaml:"-"`
	Actions         int  `json:"actions,omitempty"          yaml:"-"`
	MemoryMB        int  `json:"memory_mb,omitempty"        yaml:"-"`
	NetworkRequests int  `json:"network_requests,omitempty" yaml:"-"`
	Artifacts       int  `json:"artifacts,omitempty"        yaml:"-"`
	ArtifactBytes   int  `json:"artifact_bytes,omitempty"   yaml:"-"`
	Concurrent      int  `json:"concurrent,omitempty"       yaml:"-"`
}

func (cfg BrowserExecutionConfig) Effective() BrowserExecutionConfig {
	return BrowserExecutionConfig{
		Enabled:         cfg.Enabled,
		RuntimeSeconds:  effectiveBrowserLimit(cfg.RuntimeSeconds, 15),
		OutputBytes:     effectiveBrowserLimit(cfg.OutputBytes, 64*1024),
		Actions:         effectiveBrowserLimit(cfg.Actions, 64),
		MemoryMB:        effectiveBrowserLimit(cfg.MemoryMB, 64),
		NetworkRequests: effectiveBrowserLimit(cfg.NetworkRequests, 64),
		Artifacts:       effectiveBrowserLimit(cfg.Artifacts, 4),
		ArtifactBytes:   effectiveBrowserLimit(cfg.ArtifactBytes, BrowserMaxScreenshotBytes),
		Concurrent:      effectiveBrowserLimit(cfg.Concurrent, BrowserMaxExecuteConcurrent),
	}
}

// ValidEffective reports whether cfg is a fully materialized execution
// budget within the process-enforced maxima. It is used at durable and node
// trust boundaries, where zero-value defaults are no longer accepted.
func (cfg BrowserExecutionConfig) ValidEffective() bool {
	return cfg.Enabled && cfg == cfg.Effective() &&
		cfg.RuntimeSeconds > 0 && cfg.RuntimeSeconds <= BrowserMaxExecuteRuntimeSeconds &&
		cfg.OutputBytes > 0 && cfg.OutputBytes <= BrowserMaxExecuteOutputBytes &&
		cfg.Actions > 0 && cfg.Actions <= BrowserMaxExecuteActions &&
		cfg.MemoryMB > 0 && cfg.MemoryMB <= BrowserMaxExecuteMemoryMB &&
		cfg.NetworkRequests > 0 && cfg.NetworkRequests <= BrowserMaxExecuteNetworkRequests &&
		cfg.Artifacts > 0 && cfg.Artifacts <= BrowserMaxExecuteArtifacts &&
		cfg.ArtifactBytes > 0 && cfg.ArtifactBytes <= BrowserMaxExecuteArtifactBytes &&
		cfg.Concurrent == BrowserMaxExecuteConcurrent
}

// BrowserAttachedConfig is the operator-owned authority for attaching one
// existing user browser tab. It never contains a native tab identifier,
// browser endpoint, extension token, or browser-profile path.
type BrowserAttachedConfig struct {
	Connector        string   `json:"connector,omitempty"          yaml:"-"`
	ConsentMode      string   `json:"consent_mode,omitempty"       yaml:"-"`
	ConsentSeconds   int      `json:"consent_seconds,omitempty"    yaml:"-"`
	ActionOriginMode string   `json:"action_origin_mode,omitempty" yaml:"-"`
	AllowedOrigins   []string `json:"allowed_origins,omitempty"    yaml:"-"`
}

// BrowserProfileRuntimeConfig is execution-host-only profile authority. Its
// values are never projected into browser tool results or node catalogs.
type BrowserProfileRuntimeConfig struct {
	ProfileDirectory  string `json:"profile_directory,omitempty"   yaml:"-"`
	EphemeralRoot     string `json:"ephemeral_root,omitempty"      yaml:"-"`
	ProviderStateFile string `json:"provider_state_file,omitempty" yaml:"-"`
	LockFile          string `json:"lock_file,omitempty"           yaml:"-"`
	Headed            bool   `json:"headed"                        yaml:"-"`
}

func browserProfileAuthorityConfigured(profile BrowserProfileConfig) bool {
	return profile.Revision != "" || len(profile.AllowedAgents) != 0 ||
		len(profile.AllowedActors) != 0 || profile.Runtime != (BrowserProfileRuntimeConfig{}) ||
		browserAttachedConfigured(profile.Attached) || profile.PrivilegedExecution != (BrowserExecutionConfig{})
}

func browserAttachedConfigured(attached BrowserAttachedConfig) bool {
	return attached.Connector != "" || attached.ConsentMode != "" ||
		attached.ConsentSeconds != 0 || attached.ActionOriginMode != "" ||
		len(attached.AllowedOrigins) != 0
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

// ValidateBrowserDriverTransition requires a new profile revision before a
// persistent browser identity moves to another driver. It is intentionally a
// comparison check: a standalone configuration cannot prove what driver last
// owned an existing profile directory.
func ValidateBrowserDriverTransition(previous, next BrowserToolsConfig) error {
	for targetName, priorTarget := range previous.Targets {
		nextTarget, exists := next.Targets[targetName]
		if !exists {
			for profileName, priorProfile := range priorTarget.Profiles {
				if priorProfile.Mode == BrowserProfileManaged {
					return fmt.Errorf(
						"removing managed browser target %q profile %q requires a gateway restart",
						targetName,
						profileName,
					)
				}
			}
			continue
		}
		for profileName, priorProfile := range priorTarget.Profiles {
			if priorProfile.Mode != BrowserProfileManaged {
				continue
			}
			nextProfile, found := nextTarget.Profiles[profileName]
			if !found || nextProfile.Mode != BrowserProfileManaged {
				return fmt.Errorf(
					"removing managed browser target %q profile %q requires a gateway restart",
					targetName,
					profileName,
				)
			}
			if priorProfile.PrivilegedExecution != nextProfile.PrivilegedExecution &&
				priorProfile.Revision == nextProfile.Revision {
				return fmt.Errorf(
					"browser target %q profile %q must change revision when privileged execution changes",
					targetName,
					profileName,
				)
			}
			if priorTarget.Driver == nextTarget.Driver &&
				priorTarget.EffectiveProvider() == nextTarget.EffectiveProvider() &&
				priorTarget.Steel == nextTarget.Steel && priorProfile.Runtime == nextProfile.Runtime {
				continue
			}
			if priorProfile.Revision == nextProfile.Revision {
				return fmt.Errorf(
					"browser target %q profile %q must change revision when driver, provider, or runtime changes",
					targetName,
					profileName,
				)
			}
		}
	}
	return nil
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
	if err := validateBrowserGatewayRuntimeIdentities(browser.Targets); err != nil {
		return err
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
	provider := target.EffectiveProvider()
	if provider != BrowserProviderLocal && provider != BrowserProviderSteel {
		return fmt.Errorf("browser target %q has unsupported provider %q", name, target.Provider)
	}
	if provider == BrowserProviderSteel {
		if err := validateBrowserSteelProvider(name, target.Steel); err != nil {
			return err
		}
	} else if target.Steel.Concurrency != 0 || target.Steel.SessionTimeoutSeconds != 0 ||
		target.Steel.InactivityTimeoutSeconds != 0 || target.Steel.MaxBillableSeconds != 0 {
		return fmt.Errorf("local browser target %q cannot configure steel", name)
	}
	if target.Driver != "" && target.Driver != BrowserDriverPlaywrightMCP &&
		target.Driver != BrowserDriverPlaywrightLibrary {
		return fmt.Errorf("invalid tools.browser.targets.%s.driver %q", name, target.Driver)
	}
	if len(target.Profiles) > 8 {
		return fmt.Errorf("invalid tools.browser.targets.%s.profiles: exceeds 8 entries", name)
	}
	for profileName, profile := range target.Profiles {
		if err := validateBrowserProfile(name, profileName, profile); err != nil {
			return err
		}
	}
	if target.DefaultProfile != "" {
		if !browserAliasPattern.MatchString(target.DefaultProfile) {
			return fmt.Errorf(
				"invalid tools.browser.targets.%s.default_profile %q",
				name,
				target.DefaultProfile,
			)
		}
		profile, ok := target.Profiles[target.DefaultProfile]
		if !ok || !profile.Enabled {
			return fmt.Errorf(
				"tools.browser.targets.%s.default_profile %q must reference an enabled profile",
				name,
				target.DefaultProfile,
			)
		}
	}
	placement := target.EffectivePlacement()
	switch placement {
	case BrowserPlacementGateway:
		if provider != BrowserProviderLocal {
			return fmt.Errorf("browser target %q gateway placement requires the local provider", name)
		}
		if target.NodeTarget != "" {
			return fmt.Errorf(
				"browser target %q cannot combine gateway placement with node_target",
				name,
			)
		}
	case BrowserPlacementNode:
		if provider != BrowserProviderLocal {
			return fmt.Errorf("browser target %q node placement requires the local provider", name)
		}
		if target.Driver != "" || target.DriverServer != "" || target.DriverExecutable != "" ||
			len(target.DriverArguments) != 0 {
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
	case BrowserPlacementCloud:
		if provider != BrowserProviderSteel {
			return fmt.Errorf("browser target %q cloud placement requires the steel provider", name)
		}
		if target.NodeTarget != "" {
			return fmt.Errorf("browser target %q cloud placement cannot configure node_target", name)
		}
	default:
		return fmt.Errorf("browser target %q has unsupported placement %q", name, target.Placement)
	}
	for profileName, profile := range target.Profiles {
		if !profile.Enabled {
			continue
		}
		if err := validateBrowserProfileGrants(profileName, profile, cfg.Tools.Browser.Agents); err != nil {
			return err
		}
		if placement == BrowserPlacementNode {
			if profile.Mode == BrowserProfileAttachedUser {
				return fmt.Errorf(
					"browser profile %q mode %q is unavailable for node placement until companion attachment is implemented",
					profileName,
					BrowserProfileAttachedUser,
				)
			}
			if profile.Runtime != (BrowserProfileRuntimeConfig{}) {
				return fmt.Errorf(
					"browser profile %q runtime must be configured on the companion host",
					profileName,
				)
			}
			continue
		}
		if placement == BrowserPlacementCloud {
			if profile.Mode != BrowserProfileManaged {
				return fmt.Errorf("cloud browser profile %q requires mode %q", profileName, BrowserProfileManaged)
			}
			if profile.NetworkMode != BrowserNetworkAnyHTTP {
				return fmt.Errorf(
					"cloud browser profile %q currently requires network_mode %q",
					profileName,
					BrowserNetworkAnyHTTP,
				)
			}
			if err := validateCloudBrowserProfileRuntime(profileName, profile); err != nil {
				return err
			}
			continue
		}
		if profile.PrivilegedExecution.Enabled && target.Driver != BrowserDriverPlaywrightLibrary {
			return fmt.Errorf(
				"browser profile %q privileged execution requires the playwright library driver",
				profileName,
			)
		}
		if err := validateGatewayBrowserProfileRuntime(profileName, profile); err != nil {
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
	if placement == BrowserPlacementCloud {
		if target.Driver != BrowserDriverPlaywrightLibrary {
			return fmt.Errorf("enabled cloud browser target %q requires the playwright library driver", name)
		}
		if target.DriverServer != "" {
			return fmt.Errorf("browser target %q direct driver cannot reference an MCP server", name)
		}
		if strings.TrimSpace(target.DriverExecutable) == "" {
			return fmt.Errorf("browser target %q requires driver_executable", name)
		}
		if err := validateBrowserDriverArguments(name, target.DriverArguments); err != nil {
			return err
		}
		if !hasEnabledBrowserProfile(map[string]BrowserTargetConfig{name: target}) {
			return fmt.Errorf("enabled browser target %q requires an enabled profile", name)
		}
		return nil
	}
	if name != BrowserDefaultTarget {
		return fmt.Errorf("B1 supports only the %q browser target", BrowserDefaultTarget)
	}
	if target.Driver == BrowserDriverPlaywrightLibrary {
		if target.DriverServer != "" {
			return fmt.Errorf("browser target %q direct driver cannot reference an MCP server", name)
		}
		if strings.TrimSpace(target.DriverExecutable) == "" {
			return fmt.Errorf("browser target %q requires driver_executable", name)
		}
		if err := validateBrowserDriverArguments(name, target.DriverArguments); err != nil {
			return err
		}
		if !hasEnabledBrowserProfile(map[string]BrowserTargetConfig{name: target}) {
			return fmt.Errorf("enabled browser target %q requires an enabled profile", name)
		}
		return nil
	}
	if target.Driver != BrowserDriverPlaywrightMCP {
		return fmt.Errorf("enabled browser target %q requires a supported driver", name)
	}
	if target.DriverExecutable != "" || len(target.DriverArguments) != 0 {
		return fmt.Errorf("browser target %q MCP driver cannot configure a direct executable", name)
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
	if !hasEnabledBrowserProfile(map[string]BrowserTargetConfig{name: target}) {
		return fmt.Errorf("enabled browser target %q requires an enabled profile", name)
	}
	return nil
}

func validateBrowserDriverArguments(name string, arguments []string) error {
	if len(arguments) > 64 {
		return fmt.Errorf("browser target %q driver_arguments exceed 64 entries", name)
	}
	for _, argument := range arguments {
		if argument == "" || len(argument) > 4096 || strings.ContainsRune(argument, 0) ||
			browserProfileOwnedDriverArgument(argument) {
			return fmt.Errorf("browser target %q contains invalid driver argument", name)
		}
	}
	return nil
}

func validateBrowserSteelProvider(name string, steel BrowserSteelProviderConfig) error {
	if steel.APIKeyRef.String() == "" {
		return fmt.Errorf("browser target %q steel provider requires api_key_ref", name)
	}
	if steel.Concurrency < 1 || steel.Concurrency > BrowserMaxCloudConcurrency {
		return fmt.Errorf(
			"browser target %q steel concurrency must be between 1 and %d",
			name, BrowserMaxCloudConcurrency,
		)
	}
	if steel.SessionTimeoutSeconds < BrowserMinCloudSessionSeconds ||
		steel.SessionTimeoutSeconds > BrowserMaxSteelLaunchSessionSeconds {
		return fmt.Errorf(
			"browser target %q steel session_timeout_seconds must be between %d and %d",
			name, BrowserMinCloudSessionSeconds, BrowserMaxSteelLaunchSessionSeconds,
		)
	}
	if steel.InactivityTimeoutSeconds < BrowserMinCloudSessionSeconds ||
		steel.InactivityTimeoutSeconds >= steel.SessionTimeoutSeconds {
		return fmt.Errorf(
			"browser target %q steel inactivity_timeout_seconds must be between %d and session_timeout_seconds-1",
			name, BrowserMinCloudSessionSeconds,
		)
	}
	if steel.MaxBillableSeconds < BrowserMinCloudSessionSeconds ||
		steel.MaxBillableSeconds > steel.SessionTimeoutSeconds {
		return fmt.Errorf(
			"browser target %q steel max_billable_seconds must be between %d and session_timeout_seconds",
			name, BrowserMinCloudSessionSeconds,
		)
	}
	return nil
}

func validateBrowserProfile(targetName, name string, profile BrowserProfileConfig) error {
	if !browserAliasPattern.MatchString(name) {
		return fmt.Errorf("invalid tools.browser.targets.%s profile alias %q", targetName, name)
	}
	if !profile.Enabled && browserProfileAuthorityConfigured(profile) {
		return fmt.Errorf("disabled browser profile %q cannot configure authority", name)
	}
	if profile.Mode != "" && profile.Mode != BrowserProfileManaged &&
		profile.Mode != BrowserProfileEphemeral && profile.Mode != BrowserProfileAttachedUser {
		return fmt.Errorf("browser profile %q has unsupported mode %q", name, profile.Mode)
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
	if err := validateBrowserExecution(name, profile.PrivilegedExecution); err != nil {
		return err
	}
	if profile.PrivilegedExecution.Enabled && profile.Mode == BrowserProfileAttachedUser {
		return fmt.Errorf("browser profile %q privileged execution cannot attach a user browser", name)
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
		if !browserPrincipalPattern.MatchString(profile.Revision) {
			return fmt.Errorf("browser profile %q requires a valid revision", name)
		}
		if profile.Mode != BrowserProfileManaged && profile.Mode != BrowserProfileEphemeral &&
			profile.Mode != BrowserProfileAttachedUser {
			return fmt.Errorf("enabled browser profile %q requires a supported mode", name)
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
	if err := validateBrowserAttachedProfile(name, profile); err != nil {
		return err
	}
	return nil
}

func validateBrowserExecution(name string, execution BrowserExecutionConfig) error {
	if !execution.Enabled {
		if execution != (BrowserExecutionConfig{}) {
			return fmt.Errorf("browser profile %q disabled privileged execution cannot configure limits", name)
		}
		return nil
	}
	limits := []struct {
		name  string
		value int
		max   int
	}{
		{"runtime_seconds", execution.RuntimeSeconds, BrowserMaxExecuteRuntimeSeconds},
		{"output_bytes", execution.OutputBytes, BrowserMaxExecuteOutputBytes},
		{"actions", execution.Actions, BrowserMaxExecuteActions},
		{"memory_mb", execution.MemoryMB, BrowserMaxExecuteMemoryMB},
		{"network_requests", execution.NetworkRequests, BrowserMaxExecuteNetworkRequests},
		{"artifacts", execution.Artifacts, BrowserMaxExecuteArtifacts},
		{"artifact_bytes", execution.ArtifactBytes, BrowserMaxExecuteArtifactBytes},
		{"concurrent", execution.Concurrent, BrowserMaxExecuteConcurrent},
	}
	for _, limit := range limits {
		if limit.value < 0 || limit.value > limit.max {
			return fmt.Errorf(
				"browser profile %q privileged execution %s must be between 0 and %d",
				name,
				limit.name,
				limit.max,
			)
		}
	}
	return nil
}

func validateBrowserAttachedProfile(name string, profile BrowserProfileConfig) error {
	attached := profile.Attached
	if profile.Mode != BrowserProfileAttachedUser {
		if browserAttachedConfigured(attached) {
			return fmt.Errorf("non-attached browser profile %q cannot configure attached authority", name)
		}
		return nil
	}
	if profile.NetworkMode != BrowserNetworkAnyHTTP {
		return fmt.Errorf(
			"attached browser profile %q requires network_mode %q",
			name, BrowserNetworkAnyHTTP,
		)
	}
	if profile.Runtime != (BrowserProfileRuntimeConfig{}) {
		return fmt.Errorf("attached browser profile %q cannot configure managed runtime paths", name)
	}
	if attached.Connector != BrowserAttachedPlaywright {
		return fmt.Errorf("attached browser profile %q requires connector %q", name, BrowserAttachedPlaywright)
	}
	if attached.ConsentMode != BrowserAttachedConsentSession {
		return fmt.Errorf("attached browser profile %q requires consent_mode %q", name, BrowserAttachedConsentSession)
	}
	if attached.ConsentSeconds <= 0 || attached.ConsentSeconds > BrowserMaxAttachConsentSeconds {
		return fmt.Errorf(
			"attached browser profile %q consent_seconds must be between 1 and %d",
			name, BrowserMaxAttachConsentSeconds,
		)
	}
	if len(attached.AllowedOrigins) > BrowserMaxConfiguredOrigins {
		return fmt.Errorf("attached browser profile %q exceeds %d allowed origins", name, BrowserMaxConfiguredOrigins)
	}
	switch attached.ActionOriginMode {
	case BrowserAttachedOriginExact:
		if len(attached.AllowedOrigins) == 0 {
			return fmt.Errorf("attached browser profile %q requires allowed_origins", name)
		}
	case BrowserAttachedOriginAnyHTTP:
		if len(attached.AllowedOrigins) != 0 {
			return fmt.Errorf("attached any_http browser profile %q must not set allowed_origins", name)
		}
	default:
		return fmt.Errorf(
			"attached browser profile %q has unsupported action_origin_mode %q",
			name,
			attached.ActionOriginMode,
		)
	}
	seen := make(map[string]struct{}, len(attached.AllowedOrigins))
	for _, rawOrigin := range attached.AllowedOrigins {
		origin, err := NormalizeBrowserHTTPOrigin(rawOrigin)
		if err != nil {
			return fmt.Errorf("invalid attached browser profile %q origin: %w", name, err)
		}
		if _, exists := seen[origin]; exists {
			return fmt.Errorf("attached browser profile %q contains duplicate origin %q", name, origin)
		}
		seen[origin] = struct{}{}
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

func validateGatewayBrowserProfileRuntime(name string, profile BrowserProfileConfig) error {
	runtime := profile.Runtime
	if runtime.ProviderStateFile != "" {
		return fmt.Errorf("local browser profile %q cannot set provider_state_file", name)
	}
	storageRoot := runtime.ProfileDirectory
	storageField := "profile_directory"
	switch profile.Mode {
	case BrowserProfileManaged:
		if runtime.EphemeralRoot != "" {
			return fmt.Errorf("managed browser profile %q cannot set ephemeral_root", name)
		}
	case BrowserProfileEphemeral:
		if runtime.ProfileDirectory != "" {
			return fmt.Errorf("ephemeral browser profile %q cannot set profile_directory", name)
		}
		storageRoot = runtime.EphemeralRoot
		storageField = "ephemeral_root"
	case BrowserProfileAttachedUser:
		if runtime != (BrowserProfileRuntimeConfig{}) {
			return fmt.Errorf("attached browser profile %q cannot configure managed runtime paths", name)
		}
		return nil
	default:
		return fmt.Errorf("browser profile %q has unsupported mode %q", name, profile.Mode)
	}
	if !filepath.IsAbs(storageRoot) || !filepath.IsAbs(runtime.LockFile) {
		return fmt.Errorf("browser profile %q runtime paths must be absolute", name)
	}
	storageRoot = filepath.Clean(storageRoot)
	lockFile := filepath.Clean(runtime.LockFile)
	if storageRoot == string(filepath.Separator) || lockFile == string(filepath.Separator) ||
		storageRoot == lockFile {
		return fmt.Errorf("browser profile %q runtime paths conflict", name)
	}
	relative, err := filepath.Rel(storageRoot, lockFile)
	if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("browser profile %q lock_file must be outside %s", name, storageField)
	}
	if profile.Mode == BrowserProfileEphemeral &&
		browserRuntimePathContains(storageRoot, lockFile+BrowserEphemeralLifecycleLockSuffix) {
		return fmt.Errorf(
			"browser profile %q lifecycle lock must be outside %s",
			name,
			storageField,
		)
	}
	return nil
}

func validateCloudBrowserProfileRuntime(name string, profile BrowserProfileConfig) error {
	runtime := profile.Runtime
	if runtime.ProfileDirectory != "" || runtime.EphemeralRoot != "" {
		return fmt.Errorf("cloud browser profile %q cannot configure local storage roots", name)
	}
	if !runtime.Headed {
		return fmt.Errorf("cloud browser profile %q requires headed runtime for live handoff", name)
	}
	if !filepath.IsAbs(runtime.ProviderStateFile) || !filepath.IsAbs(runtime.LockFile) {
		return fmt.Errorf("cloud browser profile %q runtime paths must be absolute", name)
	}
	stateFile := filepath.Clean(runtime.ProviderStateFile)
	lockFile := filepath.Clean(runtime.LockFile)
	if stateFile == string(filepath.Separator) || lockFile == string(filepath.Separator) ||
		stateFile == lockFile {
		return fmt.Errorf("cloud browser profile %q runtime paths conflict", name)
	}
	return nil
}

type browserGatewayRuntimeIdentity struct {
	target      string
	profile     string
	storageRoot string
	lockFiles   []string
}

// validateBrowserGatewayRuntimeIdentities keeps managed Chrome identities
// distinct across the whole gateway configuration. Multiple aliases may be
// enabled, but they must never expose shared storage or lock ownership.
func validateBrowserGatewayRuntimeIdentities(targets map[string]BrowserTargetConfig) error {
	identities := make([]browserGatewayRuntimeIdentity, 0)
	for targetName, target := range targets {
		placement := target.EffectivePlacement()
		if !target.Enabled || (placement != BrowserPlacementGateway && placement != BrowserPlacementCloud) {
			continue
		}
		for profileName, profile := range target.Profiles {
			if !profile.Enabled {
				continue
			}
			if profile.Mode == BrowserProfileAttachedUser {
				continue
			}
			var err error
			if placement == BrowserPlacementCloud {
				err = validateCloudBrowserProfileRuntime(profileName, profile)
			} else {
				err = validateGatewayBrowserProfileRuntime(profileName, profile)
			}
			if err != nil {
				// Per-profile validation reports malformed paths with the more
				// specific profile error.
				continue
			}
			identities = append(identities, browserGatewayRuntimeIdentity{
				target:      targetName,
				profile:     profileName,
				storageRoot: browserProfileRuntimeStorageRoot(target, profile),
				lockFiles:   browserProfileRuntimeLockFiles(profile),
			})
		}
	}
	sort.Slice(identities, func(i, j int) bool {
		if identities[i].target != identities[j].target {
			return identities[i].target < identities[j].target
		}
		return identities[i].profile < identities[j].profile
	})
	for i := range identities {
		for j := i + 1; j < len(identities); j++ {
			left, right := identities[i], identities[j]
			leftName := left.target + "/" + left.profile
			rightName := right.target + "/" + right.profile
			if browserRuntimePathContains(left.storageRoot, right.storageRoot) ||
				browserRuntimePathContains(right.storageRoot, left.storageRoot) {
				return fmt.Errorf(
					"browser profiles %q and %q have overlapping storage roots",
					leftName, rightName,
				)
			}
			for _, leftLock := range left.lockFiles {
				for _, rightLock := range right.lockFiles {
					if leftLock == rightLock {
						return fmt.Errorf(
							"browser profiles %q and %q reuse the same lock_file",
							leftName, rightName,
						)
					}
				}
				if browserRuntimePathContains(right.storageRoot, leftLock) {
					return fmt.Errorf(
						"browser profiles %q and %q have a lock_file inside another storage root",
						leftName, rightName,
					)
				}
			}
			for _, rightLock := range right.lockFiles {
				if browserRuntimePathContains(left.storageRoot, rightLock) {
					return fmt.Errorf(
						"browser profiles %q and %q have a lock_file inside another storage root",
						leftName, rightName,
					)
				}
			}
		}
	}
	return nil
}

func browserProfileRuntimeLockFiles(profile BrowserProfileConfig) []string {
	lockFile := filepath.Clean(profile.Runtime.LockFile)
	locks := []string{lockFile}
	if profile.Mode == BrowserProfileEphemeral {
		locks = append(locks, lockFile+BrowserEphemeralLifecycleLockSuffix)
	}
	return locks
}

func browserProfileRuntimeStorageRoot(target BrowserTargetConfig, profile BrowserProfileConfig) string {
	if target.EffectivePlacement() == BrowserPlacementCloud {
		return filepath.Clean(profile.Runtime.ProviderStateFile)
	}
	if profile.Mode == BrowserProfileEphemeral {
		return filepath.Clean(profile.Runtime.EphemeralRoot)
	}
	return filepath.Clean(profile.Runtime.ProfileDirectory)
}

func browserRuntimePathContains(parent, candidate string) bool {
	relative, err := filepath.Rel(parent, candidate)
	return err == nil && (relative == "." ||
		(relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))))
}

func browserProfileOwnedDriverArgument(argument string) bool {
	for _, owned := range []string{
		"--extension", "--profile-dir-name", "--user-data-dir", "--storage-state",
		"--isolated", "--headless",
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
// applying an address-scope policy. It is used for operator-authored attached
// origins and after an operator selects the high-risk any_http mode; managed
// network authority is checked separately.
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
