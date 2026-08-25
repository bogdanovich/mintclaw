package config

import (
	"encoding/json"
	"sync/atomic"
)

// rrCounter is a global counter for round-robin load balancing across models.
var rrCounter atomic.Uint64

// CurrentVersion is the only config schema version accepted at runtime.
const CurrentVersion = 4

func init() {
	initChannel()
}

// Config is the current configuration schema.
type Config struct {
	// Version identifies the required config schema.
	Version     int               `json:"version"                 yaml:"-"`
	Isolation   IsolationConfig   `json:"isolation,omitempty"     yaml:"-"`
	Agents      AgentsConfig      `json:"agents"                  yaml:"-"`
	Session     SessionConfig     `json:"session"                 yaml:"-"`
	Diagnostics DiagnosticsConfig `json:"diagnostics,omitempty"   yaml:"-"`
	Tasks       TaskConfig        `json:"task_registry,omitempty" yaml:"-"`
	Execution   ExecutionConfig   `json:"execution,omitempty"     yaml:"-"`
	Channels    ChannelsConfig    `json:"channel_list"            yaml:"channel_list"`
	ModelList   SecureModelList   `json:"model_list"              yaml:"model_list"` // New model-centric provider configuration
	Gateway     GatewayConfig     `json:"gateway"                 yaml:"-"`
	Events      EventsConfig      `json:"events,omitempty"        yaml:"-"`
	Hooks       HooksConfig       `json:"hooks,omitempty"         yaml:"-"`
	Tools       ToolsConfig       `json:"tools"                   yaml:",inline"`
	Heartbeat   HeartbeatConfig   `json:"heartbeat"               yaml:"-"`
	Devices     DevicesConfig     `json:"devices"                 yaml:"-"`
	Nodes       NodesConfig       `json:"nodes,omitempty"         yaml:"-"`
	Voice       VoiceConfig       `json:"voice"                   yaml:"-"`
	// BuildInfo contains build-time version information
	BuildInfo BuildInfo `json:"build_info,omitempty" yaml:"-"`

	// cache for sensitive values and compiled regex (computed once)
	sensitiveCache *SensitiveDataCache
}

// IsolationConfig controls subprocess isolation for commands started by MintClaw.
// It is applied by the isolation package rather than by sandboxing the main process.
type IsolationConfig struct {
	Enabled     bool         `json:"enabled,omitempty"`
	ExposePaths []ExposePath `json:"expose_paths,omitempty"`
}

// ExposePath describes a host path that should remain visible inside the isolated
// child-process environment. This is currently implemented on Linux only.
type ExposePath struct {
	Source string `json:"source"`
	Target string `json:"target,omitempty"`
	Mode   string `json:"mode"`
}

// FilterSensitiveData filters sensitive values from content before sending to LLM.
// This prevents the LLM from seeing its own credentials.
// Uses strings.Replacer for O(n+m) performance (computed once per SecurityConfig).
// Short content (below FilterMinLength) is returned unchanged for performance.
func (c *Config) FilterSensitiveData(content string) string {
	if c == nil {
		return content
	}
	// Check if filtering is enabled (default: true)
	if !c.Tools.IsFilterSensitiveDataEnabled() {
		return content
	}
	// Fast path: skip filtering for short content
	if len(content) < c.Tools.GetFilterMinLength() {
		return content
	}
	return c.SensitiveDataReplacer().Replace(content)
}

type HooksConfig struct {
	Enabled   bool                         `json:"enabled"`
	Defaults  HookDefaultsConfig           `json:"defaults,omitempty"`
	Builtins  map[string]BuiltinHookConfig `json:"builtins,omitempty"`
	Processes map[string]ProcessHookConfig `json:"processes,omitempty"`
}

type HookDefaultsConfig struct {
	ObserverTimeoutMS    int `json:"observer_timeout_ms,omitempty"`
	InterceptorTimeoutMS int `json:"interceptor_timeout_ms,omitempty"`
	ApprovalTimeoutMS    int `json:"approval_timeout_ms,omitempty"`
}

type BuiltinHookConfig struct {
	Enabled  bool            `json:"enabled"`
	Priority int             `json:"priority,omitempty"`
	Config   json.RawMessage `json:"config,omitempty"`
}

type ProcessHookConfig struct {
	Enabled   bool              `json:"enabled"`
	Priority  int               `json:"priority,omitempty"`
	Transport string            `json:"transport,omitempty"`
	Command   []string          `json:"command,omitempty"`
	Dir       string            `json:"dir,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Observe   []string          `json:"observe,omitempty"`
	Intercept []string          `json:"intercept,omitempty"`
}

// BuildInfo contains build-time version information
type BuildInfo struct {
	Version   string `json:"version"`
	GitCommit string `json:"git_commit"`
	BuildTime string `json:"build_time"`
	GoVersion string `json:"go_version"`
}
