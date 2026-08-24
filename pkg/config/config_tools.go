package config

import (
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"time"

	toolpolicy "github.com/bogdanovich/mintclaw/pkg/tools/policy"
)

type ToolDiscoveryConfig struct {
	Enabled          bool `json:"enabled"            env:"MINTCLAW_TOOLS_DISCOVERY_ENABLED"`
	TTL              int  `json:"ttl"                env:"MINTCLAW_TOOLS_DISCOVERY_TTL"`
	MaxSearchResults int  `json:"max_search_results" env:"MINTCLAW_MAX_SEARCH_RESULTS"`
	UseBM25          bool `json:"use_bm25"           env:"MINTCLAW_TOOLS_DISCOVERY_USE_BM25"`
	UseRegex         bool `json:"use_regex"          env:"MINTCLAW_TOOLS_DISCOVERY_USE_REGEX"`
}

type ToolConfig struct {
	Enabled bool `json:"enabled" yaml:"-" env:"ENABLED"`
}

type MessageToolsConfig struct {
	ToolConfig   `     yaml:"-" envPrefix:"MINTCLAW_TOOLS_MESSAGE_"`
	MediaEnabled bool `yaml:"-"                                     json:"media_enabled" env:"MINTCLAW_TOOLS_MESSAGE_MEDIA_ENABLED"`
}

type RequestUserInputToolsConfig struct {
	Enabled               bool `json:"enabled"                 yaml:"-" env:"MINTCLAW_TOOLS_REQUEST_USER_INPUT_ENABLED"` //nolint:golines
	DefaultTimeoutSeconds int  `json:"default_timeout_seconds"          env:"MINTCLAW_TOOLS_REQUEST_USER_INPUT_DEFAULT_TIMEOUT_SECONDS"`
	MaxTimeoutSeconds     int  `json:"max_timeout_seconds"              env:"MINTCLAW_TOOLS_REQUEST_USER_INPUT_MAX_TIMEOUT_SECONDS"`
	RetentionHours        int  `json:"retention_hours"                  env:"MINTCLAW_TOOLS_REQUEST_USER_INPUT_RETENTION_HOURS"`
}

func (c RequestUserInputToolsConfig) DefaultTimeout() time.Duration {
	if c.DefaultTimeoutSeconds == 0 {
		return time.Hour
	}
	return time.Duration(c.DefaultTimeoutSeconds) * time.Second
}

func (c RequestUserInputToolsConfig) MaxTimeout() time.Duration {
	if c.MaxTimeoutSeconds == 0 {
		return 24 * time.Hour
	}
	return time.Duration(c.MaxTimeoutSeconds) * time.Second
}

func (c RequestUserInputToolsConfig) Retention() time.Duration {
	if c.RetentionHours == 0 {
		return 7 * 24 * time.Hour
	}
	return time.Duration(c.RetentionHours) * time.Hour
}

type ImageGenerateToolsConfig struct {
	ToolConfig `       yaml:"-" envPrefix:"MINTCLAW_TOOLS_IMAGE_GENERATE_"`
	Model      string `yaml:"-"                                            json:"model,omitempty"      env:"MINTCLAW_TOOLS_IMAGE_GENERATE_MODEL"`
	OutputDir  string `yaml:"-"                                            json:"output_dir,omitempty" env:"MINTCLAW_TOOLS_IMAGE_GENERATE_OUTPUT_DIR"`
}

func (c ImageGenerateToolsConfig) EffectiveModel() string {
	if model := strings.TrimSpace(c.Model); model != "" {
		return model
	}
	return "gpt-image-2"
}

type BraveConfig struct {
	Enabled    bool          `json:"enabled"           yaml:"-"                  env:"MINTCLAW_TOOLS_WEB_BRAVE_ENABLED"`
	APIKeys    SecureStrings `json:"api_keys,omitzero" yaml:"api_keys,omitempty" env:"MINTCLAW_TOOLS_WEB_BRAVE_API_KEYS"`
	MaxResults int           `json:"max_results"       yaml:"-"                  env:"MINTCLAW_TOOLS_WEB_BRAVE_MAX_RESULTS"`
}

// APIKey returns the Brave API key
func (c *BraveConfig) APIKey() string {
	if len(c.APIKeys) == 0 {
		return ""
	}
	return c.APIKeys[0].String()
}

// SetAPIKey sets the Brave API key
func (c *BraveConfig) SetAPIKey(key string) {
	c.APIKeys = SimpleSecureStrings(key)
}

func (c *BraveConfig) SetAPIKeys(keys []string) {
	c.APIKeys = SimpleSecureStrings(keys...)
}

type TavilyConfig struct {
	Enabled    bool          `json:"enabled"           yaml:"-"                  env:"MINTCLAW_TOOLS_WEB_TAVILY_ENABLED"`
	APIKeys    SecureStrings `json:"api_keys,omitzero" yaml:"api_keys,omitempty" env:"MINTCLAW_TOOLS_WEB_TAVILY_API_KEYS"`
	BaseURL    string        `json:"base_url"          yaml:"-"                  env:"MINTCLAW_TOOLS_WEB_TAVILY_BASE_URL"`
	MaxResults int           `json:"max_results"       yaml:"-"                  env:"MINTCLAW_TOOLS_WEB_TAVILY_MAX_RESULTS"`
}

// APIKey returns the Tavily API key
func (c *TavilyConfig) APIKey() string {
	if len(c.APIKeys) == 0 {
		return ""
	}
	return c.APIKeys[0].String()
}

// SetAPIKey sets the Tavily API key
func (c *TavilyConfig) SetAPIKey(key string) {
	c.APIKeys = SimpleSecureStrings(key)
}

// SetAPIKeys sets the Tavily API keys
func (c *TavilyConfig) SetAPIKeys(keys []string) {
	c.APIKeys = make(SecureStrings, len(keys))
	for i, k := range keys {
		c.APIKeys[i] = NewSecureString(k)
	}
}

type KagiConfig struct {
	Enabled    bool          `json:"enabled"           yaml:"-"                  env:"MINTCLAW_TOOLS_WEB_KAGI_ENABLED"`
	APIKeys    SecureStrings `json:"api_keys,omitzero" yaml:"api_keys,omitempty" env:"MINTCLAW_TOOLS_WEB_KAGI_API_KEYS"`
	BaseURL    string        `json:"base_url"          yaml:"-"                  env:"MINTCLAW_TOOLS_WEB_KAGI_BASE_URL"`
	MaxResults int           `json:"max_results"       yaml:"-"                  env:"MINTCLAW_TOOLS_WEB_KAGI_MAX_RESULTS"`
}

// APIKey returns the Kagi API key
func (c *KagiConfig) APIKey() string {
	if len(c.APIKeys) == 0 {
		return ""
	}
	return c.APIKeys[0].String()
}

// SetAPIKey sets the Kagi API key
func (c *KagiConfig) SetAPIKey(key string) {
	c.APIKeys = SimpleSecureStrings(key)
}

// SetAPIKeys sets the Kagi API keys
func (c *KagiConfig) SetAPIKeys(keys []string) {
	c.APIKeys = SimpleSecureStrings(keys...)
}

type DuckDuckGoConfig struct {
	Enabled    bool `json:"enabled"     env:"MINTCLAW_TOOLS_WEB_DUCKDUCKGO_ENABLED"`
	MaxResults int  `json:"max_results" env:"MINTCLAW_TOOLS_WEB_DUCKDUCKGO_MAX_RESULTS"`
}

type SogouConfig struct {
	Enabled    bool `json:"enabled"     env:"MINTCLAW_TOOLS_WEB_SOGOU_ENABLED"`
	MaxResults int  `json:"max_results" env:"MINTCLAW_TOOLS_WEB_SOGOU_MAX_RESULTS"`
}

type GeminiSearchConfig struct {
	Enabled    bool         `json:"enabled"          yaml:"-"                 env:"MINTCLAW_TOOLS_WEB_GEMINI_ENABLED"`
	APIKey     SecureString `json:"api_key,omitzero" yaml:"api_key,omitempty" env:"MINTCLAW_TOOLS_WEB_GEMINI_API_KEY"`
	Model      string       `json:"model"            yaml:"-"                 env:"MINTCLAW_TOOLS_WEB_GEMINI_MODEL"`
	MaxResults int          `json:"max_results"      yaml:"-"                 env:"MINTCLAW_TOOLS_WEB_GEMINI_MAX_RESULTS"`
}

type PerplexityConfig struct {
	Enabled    bool          `json:"enabled"           yaml:"-"                  env:"MINTCLAW_TOOLS_WEB_PERPLEXITY_ENABLED"`
	APIKeys    SecureStrings `json:"api_keys,omitzero" yaml:"api_keys,omitempty" env:"MINTCLAW_TOOLS_WEB_PERPLEXITY_API_KEYS"`
	MaxResults int           `json:"max_results"       yaml:"-"                  env:"MINTCLAW_TOOLS_WEB_PERPLEXITY_MAX_RESULTS"`
}

// APIKey returns the Perplexity API key
func (c *PerplexityConfig) APIKey() string {
	if len(c.APIKeys) == 0 {
		return ""
	}
	return c.APIKeys[0].String()
}

// SetAPIKey sets the Perplexity API key
func (c *PerplexityConfig) SetAPIKey(key string) {
	c.APIKeys = SimpleSecureStrings(key)
}

type SearXNGConfig struct {
	Enabled    bool   `json:"enabled"     env:"MINTCLAW_TOOLS_WEB_SEARXNG_ENABLED"`
	BaseURL    string `json:"base_url"    env:"MINTCLAW_TOOLS_WEB_SEARXNG_BASE_URL"`
	MaxResults int    `json:"max_results" env:"MINTCLAW_TOOLS_WEB_SEARXNG_MAX_RESULTS"`
}

type GLMSearchConfig struct {
	Enabled bool         `json:"enabled"          yaml:"-"                 env:"MINTCLAW_TOOLS_WEB_GLM_ENABLED"`
	APIKey  SecureString `json:"api_key,omitzero" yaml:"api_key,omitempty" env:"MINTCLAW_TOOLS_WEB_GLM_API_KEY"`
	BaseURL string       `json:"base_url"         yaml:"-"                 env:"MINTCLAW_TOOLS_WEB_GLM_BASE_URL"`
	// SearchEngine specifies the search backend: "search_std" (default),
	// "search_pro", "search_pro_sogou", or "search_pro_quark".
	SearchEngine string `json:"search_engine" yaml:"-" env:"MINTCLAW_TOOLS_WEB_GLM_SEARCH_ENGINE"`
	MaxResults   int    `json:"max_results"   yaml:"-" env:"MINTCLAW_TOOLS_WEB_GLM_MAX_RESULTS"`
}

type BaiduSearchConfig struct {
	Enabled    bool         `json:"enabled"          yaml:"-"                 env:"MINTCLAW_TOOLS_WEB_BAIDU_ENABLED"`
	APIKey     SecureString `json:"api_key,omitzero" yaml:"api_key,omitempty" env:"MINTCLAW_TOOLS_WEB_BAIDU_API_KEY"`
	BaseURL    string       `json:"base_url"         yaml:"-"                 env:"MINTCLAW_TOOLS_WEB_BAIDU_BASE_URL"`
	MaxResults int          `json:"max_results"      yaml:"-"                 env:"MINTCLAW_TOOLS_WEB_BAIDU_MAX_RESULTS"`
}

type WebToolsConfig struct {
	ToolConfig  `                   yaml:"-"                      envPrefix:"MINTCLAW_TOOLS_WEB_"`
	Brave       BraveConfig        `yaml:"brave,omitempty"                                        json:"brave"`
	Tavily      TavilyConfig       `yaml:"tavily,omitempty"                                       json:"tavily"`
	Kagi        KagiConfig         `yaml:"kagi,omitempty"                                         json:"kagi"`
	Sogou       SogouConfig        `yaml:"-"                                                      json:"sogou"`
	DuckDuckGo  DuckDuckGoConfig   `yaml:"-"                                                      json:"duckduckgo"`
	Gemini      GeminiSearchConfig `yaml:"gemini,omitempty"                                       json:"gemini"`
	Perplexity  PerplexityConfig   `yaml:"perplexity,omitempty"                                   json:"perplexity"`
	SearXNG     SearXNGConfig      `yaml:"-"                                                      json:"searxng"`
	GLMSearch   GLMSearchConfig    `yaml:"glm_search,omitempty"                                   json:"glm_search"`
	BaiduSearch BaiduSearchConfig  `yaml:"baidu_search,omitempty"                                 json:"baidu_search"`
	Provider    string             `yaml:"-"                                                      json:"provider,omitempty" env:"MINTCLAW_TOOLS_WEB_PROVIDER"`
	// PreferNative controls whether to use provider-native web search when
	// the active LLM supports it (e.g. OpenAI web_search_preview). When true,
	// the client-side web_search tool is hidden to avoid duplicate search surfaces,
	// and the provider's built-in search is used instead. Falls back to client-side
	// search when the provider does not support native search.
	PreferNative bool `yaml:"-" json:"prefer_native" env:"MINTCLAW_TOOLS_WEB_PREFER_NATIVE"`
	// Proxy is an optional proxy URL for web tools (http/https/socks5/socks5h).
	// For authenticated proxies, prefer HTTP_PROXY/HTTPS_PROXY env vars instead of embedding credentials in config.
	Proxy                string   `yaml:"-" json:"proxy,omitempty"                  env:"MINTCLAW_TOOLS_WEB_PROXY"`
	FetchLimitBytes      int64    `yaml:"-" json:"fetch_limit_bytes,omitempty"      env:"MINTCLAW_TOOLS_WEB_FETCH_LIMIT_BYTES"`
	Format               string   `yaml:"-" json:"format,omitempty"                 env:"MINTCLAW_TOOLS_WEB_FORMAT"`
	PrivateHostWhitelist []string `yaml:"-" json:"private_host_whitelist,omitempty" env:"MINTCLAW_TOOLS_WEB_PRIVATE_HOST_WHITELIST"`
}

type CronToolsConfig struct {
	ToolConfig `envPrefix:"MINTCLAW_TOOLS_CRON_"`
	// 0 means no timeout.
	ExecTimeoutMinutes    int      `json:"exec_timeout_minutes"    env:"MINTCLAW_TOOLS_CRON_EXEC_TIMEOUT_MINUTES"`
	AllowCommand          bool     `json:"allow_command"           env:"MINTCLAW_TOOLS_CRON_ALLOW_COMMAND"`
	CommandAllowedRemotes []string `json:"command_allowed_remotes" env:"MINTCLAW_TOOLS_CRON_COMMAND_ALLOWED_REMOTES"`
}

type ExecConfig struct {
	ToolConfig          `         envPrefix:"MINTCLAW_TOOLS_EXEC_"`
	EnableDenyPatterns  bool     `                                 json:"enable_deny_patterns"  env:"MINTCLAW_TOOLS_EXEC_ENABLE_DENY_PATTERNS"`
	AllowRemote         bool     `                                 json:"allow_remote"          env:"MINTCLAW_TOOLS_EXEC_ALLOW_REMOTE"`
	PermissionMode      string   `                                 json:"permission_mode"       env:"MINTCLAW_TOOLS_EXEC_PERMISSION_MODE"`
	CustomDenyPatterns  []string `                                 json:"custom_deny_patterns"  env:"MINTCLAW_TOOLS_EXEC_CUSTOM_DENY_PATTERNS"`
	CustomAllowPatterns []string `                                 json:"custom_allow_patterns" env:"MINTCLAW_TOOLS_EXEC_CUSTOM_ALLOW_PATTERNS"`
	TimeoutSeconds      int      `                                 json:"timeout_seconds"       env:"MINTCLAW_TOOLS_EXEC_TIMEOUT_SECONDS"` // 0 means use default (60s)
}

type SkillsToolsConfig struct {
	ToolConfig            `                       yaml:"-"                    envPrefix:"MINTCLAW_TOOLS_SKILLS_"`
	Registries            SkillsRegistriesConfig `yaml:"registries,omitempty"                                    json:"registries,omitzero"`
	MaxConcurrentSearches int                    `yaml:"-"                                                       json:"max_concurrent_searches" env:"MINTCLAW_TOOLS_SKILLS_MAX_CONCURRENT_SEARCHES"`
	SearchCache           SearchCacheConfig      `yaml:"-"                                                       json:"search_cache"`
}

type MediaCleanupConfig struct {
	ToolConfig `    envPrefix:"MINTCLAW_MEDIA_CLEANUP_"`
	MaxAge     int `                                    json:"max_age_minutes"  env:"MINTCLAW_MEDIA_CLEANUP_MAX_AGE"`
	Interval   int `                                    json:"interval_minutes" env:"MINTCLAW_MEDIA_CLEANUP_INTERVAL"`
}

type ReadFileToolConfig struct {
	Enabled         bool   `json:"enabled"`
	Mode            string `json:"mode"`
	MaxReadFileSize int    `json:"max_read_file_size"`
}

type ToolLoopDetectionConfig struct {
	Enabled             bool `json:"enabled"                yaml:"enabled"                env:"MINTCLAW_TOOLS_LOOP_DETECTION_ENABLED"`
	WarningsEnabled     bool `json:"warnings_enabled"       yaml:"warnings_enabled"       env:"MINTCLAW_TOOLS_LOOP_DETECTION_WARNINGS_ENABLED"`
	HardStopsEnabled    bool `json:"hard_stops_enabled"     yaml:"hard_stops_enabled"     env:"MINTCLAW_TOOLS_LOOP_DETECTION_HARD_STOPS_ENABLED"`
	ExactFailureWarn    int  `json:"exact_failure_warn"     yaml:"exact_failure_warn"     env:"MINTCLAW_TOOLS_LOOP_DETECTION_EXACT_FAILURE_WARN"`
	ExactFailureBlock   int  `json:"exact_failure_block"    yaml:"exact_failure_block"    env:"MINTCLAW_TOOLS_LOOP_DETECTION_EXACT_FAILURE_BLOCK"`
	SameToolFailureWarn int  `json:"same_tool_failure_warn" yaml:"same_tool_failure_warn" env:"MINTCLAW_TOOLS_LOOP_DETECTION_SAME_TOOL_FAILURE_WARN"`
	SameToolFailureHalt int  `json:"same_tool_failure_halt" yaml:"same_tool_failure_halt" env:"MINTCLAW_TOOLS_LOOP_DETECTION_SAME_TOOL_FAILURE_HALT"`
	NoProgressWarn      int  `json:"no_progress_warn"       yaml:"no_progress_warn"       env:"MINTCLAW_TOOLS_LOOP_DETECTION_NO_PROGRESS_WARN"`
	NoProgressBlock     int  `json:"no_progress_block"      yaml:"no_progress_block"      env:"MINTCLAW_TOOLS_LOOP_DETECTION_NO_PROGRESS_BLOCK"`
	IdenticalCallWarn   int  `json:"identical_call_warn"    yaml:"identical_call_warn"    env:"MINTCLAW_TOOLS_LOOP_DETECTION_IDENTICAL_CALL_WARN"`
	IdenticalCallHalt   int  `json:"identical_call_halt"    yaml:"identical_call_halt"    env:"MINTCLAW_TOOLS_LOOP_DETECTION_IDENTICAL_CALL_HALT"`
	MaxSignatures       int  `json:"max_signatures"         yaml:"max_signatures"         env:"MINTCLAW_TOOLS_LOOP_DETECTION_MAX_SIGNATURES"`
}

type ResultRetentionConfig = toolpolicy.ResultRetentionPolicy

const (
	ReadFileModeBytes = "bytes"
	ReadFileModeLines = "lines"
)

func (c ReadFileToolConfig) EffectiveMode() string {
	switch strings.ToLower(strings.TrimSpace(c.Mode)) {
	case ReadFileModeLines:
		return ReadFileModeLines
	case "", ReadFileModeBytes:
		return ReadFileModeBytes
	default:
		return ReadFileModeBytes
	}
}

type ToolsConfig struct {
	AllowReadPaths  []string `json:"allow_read_paths"  yaml:"-" env:"MINTCLAW_TOOLS_ALLOW_READ_PATHS"`
	AllowWritePaths []string `json:"allow_write_paths" yaml:"-" env:"MINTCLAW_TOOLS_ALLOW_WRITE_PATHS"`
	// FilterSensitiveData controls whether to filter sensitive values (API keys,
	// tokens, secrets) from tool results before sending to the LLM.
	// Default: true (enabled)
	FilterSensitiveData bool `json:"filter_sensitive_data" yaml:"-" env:"MINTCLAW_TOOLS_FILTER_SENSITIVE_DATA"`
	// FilterMinLength is the minimum content length required for filtering.
	// Content shorter than this will be returned unchanged for performance.
	// Default: 8
	FilterMinLength  int                         `json:"filter_min_length"          yaml:"-"                env:"MINTCLAW_TOOLS_FILTER_MIN_LENGTH"`
	LoopDetection    ToolLoopDetectionConfig     `json:"loop_detection"             yaml:"-"`
	ResultRetention  ResultRetentionConfig       `json:"result_retention,omitempty" yaml:"-"`
	Approval         ToolApprovalConfig          `json:"approval,omitempty"         yaml:"-"`
	Web              WebToolsConfig              `json:"web"                        yaml:"web,omitempty"`
	Cron             CronToolsConfig             `json:"cron"                       yaml:"-"`
	Exec             ExecConfig                  `json:"exec"                       yaml:"-"`
	Skills           SkillsToolsConfig           `json:"skills"                     yaml:"skills,omitempty"`
	MediaCleanup     MediaCleanupConfig          `json:"media_cleanup"              yaml:"-"`
	Browser          BrowserToolsConfig          `json:"browser,omitempty"          yaml:"-"`
	MCP              MCPConfig                   `json:"mcp"                        yaml:"-"`
	AppendFile       ToolConfig                  `json:"append_file"                yaml:"-"                                                       envPrefix:"MINTCLAW_TOOLS_APPEND_FILE_"`
	ApplyPatch       ToolConfig                  `json:"apply_patch"                yaml:"-"                                                       envPrefix:"MINTCLAW_TOOLS_APPLY_PATCH_"`
	FindSkills       ToolConfig                  `json:"find_skills"                yaml:"-"                                                       envPrefix:"MINTCLAW_TOOLS_FIND_SKILLS_"`
	I2C              ToolConfig                  `json:"i2c"                        yaml:"-"                                                       envPrefix:"MINTCLAW_TOOLS_I2C_"`
	ImageGenerate    ImageGenerateToolsConfig    `json:"image_generate"             yaml:"-"`
	InstallSkill     ToolConfig                  `json:"install_skill"              yaml:"-"                                                       envPrefix:"MINTCLAW_TOOLS_INSTALL_SKILL_"`
	ListDir          ToolConfig                  `json:"list_dir"                   yaml:"-"                                                       envPrefix:"MINTCLAW_TOOLS_LIST_DIR_"`
	LoadImage        ToolConfig                  `json:"load_image"                 yaml:"-"                                                       envPrefix:"MINTCLAW_TOOLS_LOAD_IMAGE_"`
	Memory           ToolConfig                  `json:"memory"                     yaml:"-"                                                       envPrefix:"MINTCLAW_TOOLS_MEMORY_"`
	Message          MessageToolsConfig          `json:"message"                    yaml:"-"`
	ReadFile         ReadFileToolConfig          `json:"read_file"                  yaml:"-"                                                       envPrefix:"MINTCLAW_TOOLS_READ_FILE_"`
	RequestUserInput RequestUserInputToolsConfig `json:"request_user_input"         yaml:"-"` //nolint:golines
	Serial           ToolConfig                  `json:"serial"                     yaml:"-"                                                       envPrefix:"MINTCLAW_TOOLS_SERIAL_"`
	SendFile         ToolConfig                  `json:"send_file"                  yaml:"-"                                                       envPrefix:"MINTCLAW_TOOLS_SEND_FILE_"`
	SendTTS          ToolConfig                  `json:"send_tts"                   yaml:"-"                                                       envPrefix:"MINTCLAW_TOOLS_SEND_TTS_"`
	SearchFiles      ToolConfig                  `json:"search_files"               yaml:"-"                                                       envPrefix:"MINTCLAW_TOOLS_SEARCH_FILES_"`
	Spawn            ToolConfig                  `json:"spawn"                      yaml:"-"                                                       envPrefix:"MINTCLAW_TOOLS_SPAWN_"`
	SPI              ToolConfig                  `json:"spi"                        yaml:"-"                                                       envPrefix:"MINTCLAW_TOOLS_SPI_"`
	Subagent         ToolConfig                  `json:"subagent"                   yaml:"-"                                                       envPrefix:"MINTCLAW_TOOLS_SUBAGENT_"`
	UpdatePlan       ToolConfig                  `json:"update_plan"                yaml:"-"                                                       envPrefix:"MINTCLAW_TOOLS_UPDATE_PLAN_"`
	WebFetch         ToolConfig                  `json:"web_fetch"                  yaml:"-"                                                       envPrefix:"MINTCLAW_TOOLS_WEB_FETCH_"`
	WriteFile        ToolConfig                  `json:"write_file"                 yaml:"-"                                                       envPrefix:"MINTCLAW_TOOLS_WRITE_FILE_"`
}

const (
	ToolApprovalModeRequired = "required"
	ToolApprovalModeAllowAll = "allow_all"
)

type ToolApprovalConfig struct {
	Mode              string   `json:"mode,omitempty"                yaml:"-" env:"MINTCLAW_TOOLS_APPROVAL_MODE"`
	BypassNodeTargets []string `json:"bypass_node_targets,omitempty" yaml:"-"`
}

func (c ToolApprovalConfig) EffectiveMode() string {
	mode := strings.ToLower(strings.TrimSpace(c.Mode))
	if mode == "" {
		return ToolApprovalModeRequired
	}
	return mode
}

func (c ToolApprovalConfig) AllowAll() bool {
	return c.EffectiveMode() == ToolApprovalModeAllowAll
}

func (c ToolApprovalConfig) BypassesNodeTarget(target string) bool {
	return slices.Contains(c.BypassNodeTargets, target)
}

// IsFilterSensitiveDataEnabled returns true if sensitive data filtering is enabled
func (c *ToolsConfig) IsFilterSensitiveDataEnabled() bool {
	return c.FilterSensitiveData
}

// GetFilterMinLength returns the minimum content length for filtering (default: 8)
func (c *ToolsConfig) GetFilterMinLength() int {
	if c.FilterMinLength <= 0 {
		return 8
	}
	return c.FilterMinLength
}

type SearchCacheConfig struct {
	MaxSize    int `json:"max_size"    env:"MINTCLAW_SKILLS_SEARCH_CACHE_MAX_SIZE"`
	TTLSeconds int `json:"ttl_seconds" env:"MINTCLAW_SKILLS_SEARCH_CACHE_TTL_SECONDS"`
}

type SkillsRegistriesConfig map[string]*SkillRegistryConfig

func (c *SkillsRegistriesConfig) Get(name string) (SkillRegistryConfig, bool) {
	if c == nil {
		return SkillRegistryConfig{}, false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return SkillRegistryConfig{}, false
	}
	registry, ok := (*c)[name]
	if !ok || registry == nil {
		return SkillRegistryConfig{}, false
	}
	return *registry, true
}

func (c *SkillsRegistriesConfig) Set(name string, cfg SkillRegistryConfig) {
	if c == nil {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	if *c == nil {
		*c = make(SkillsRegistriesConfig)
	}
	(*c)[name] = &cfg
}

func (c *SkillsRegistriesConfig) Names() []string {
	if c == nil {
		return nil
	}
	names := make([]string, 0, len(*c))
	for name, registry := range *c {
		if strings.TrimSpace(name) == "" || registry == nil {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

type SkillRegistryConfig struct {
	Enabled   bool           `json:"enabled"             yaml:"-"                    env:"-"`
	BaseURL   string         `json:"base_url"            yaml:"-"                    env:"-"`
	AuthToken SecureString   `json:"auth_token,omitzero" yaml:"auth_token,omitempty" env:"-"`
	Param     map[string]any `json:"-"                   yaml:"-"                    env:"-"`
}

const (
	envSkillsClawHubEnabled         = "MINTCLAW_SKILLS_REGISTRIES_CLAWHUB_ENABLED"
	envSkillsClawHubBaseURL         = "MINTCLAW_SKILLS_REGISTRIES_CLAWHUB_BASE_URL"
	envSkillsClawHubAuthToken       = "MINTCLAW_SKILLS_REGISTRIES_CLAWHUB_AUTH_TOKEN"
	envSkillsClawHubSearchPath      = "MINTCLAW_SKILLS_REGISTRIES_CLAWHUB_SEARCH_PATH"
	envSkillsClawHubSkillsPath      = "MINTCLAW_SKILLS_REGISTRIES_CLAWHUB_SKILLS_PATH"
	envSkillsClawHubDownloadPath    = "MINTCLAW_SKILLS_REGISTRIES_CLAWHUB_DOWNLOAD_PATH"
	envSkillsClawHubTimeout         = "MINTCLAW_SKILLS_REGISTRIES_CLAWHUB_TIMEOUT"
	envSkillsClawHubMaxZipSize      = "MINTCLAW_SKILLS_REGISTRIES_CLAWHUB_MAX_ZIP_SIZE"
	envSkillsClawHubMaxResponseSize = "MINTCLAW_SKILLS_REGISTRIES_CLAWHUB_MAX_RESPONSE_SIZE"
	envSkillsGitHubEnabled          = "MINTCLAW_SKILLS_REGISTRIES_GITHUB_ENABLED"
	envSkillsGitHubBaseURL          = "MINTCLAW_SKILLS_REGISTRIES_GITHUB_BASE_URL"
	envSkillsGitHubAuthToken        = "MINTCLAW_SKILLS_REGISTRIES_GITHUB_AUTH_TOKEN"
	envSkillsGitHubProxy            = "MINTCLAW_SKILLS_REGISTRIES_GITHUB_PROXY"
)

func (c *SkillRegistryConfig) DecodeParam(target any) error {
	if c == nil {
		return nil
	}
	if len(c.Param) == 0 {
		return nil
	}
	data, err := json.Marshal(c.Param)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

// MCPServerConfig defines configuration for a single MCP server
type MCPServerConfig struct {
	// Enabled indicates whether this MCP server is active
	Enabled bool `json:"enabled"`
	// Deferred controls whether this server's tools are registered as hidden (deferred/discovery mode).
	// When nil, the global Discovery.Enabled setting applies.
	// When explicitly set to true or false, it overrides the global setting for this server only.
	Deferred *bool `json:"deferred,omitempty"`
	// VisibleTools keeps a small allowlist of MCP tool names directly visible even when the
	// server is otherwise deferred. Hidden registration still applies to all other tools.
	VisibleTools []string `json:"visible_tools,omitempty"`
	// Command is the executable to run (e.g., "npx", "python", "/path/to/server")
	Command string `json:"command"`
	// Args are the arguments to pass to the command
	Args []string `json:"args,omitempty"`
	// Env are environment variables to set for the server process (stdio only)
	Env map[string]string `json:"env,omitempty"`
	// EnvFile is the path to a file containing environment variables (stdio only)
	EnvFile string `json:"env_file,omitempty"`
	// Type is "stdio", "sse", "http", or "streamable-http".
	// "http" and "streamable-http" both select streamable HTTP request-response
	// mode, while "sse" keeps the standalone SSE listener enabled for
	// server-initiated notifications. Defaults: stdio if command is set, sse if
	// url is set.
	Type string `json:"type,omitempty"`
	// URL is used for SSE/HTTP transport
	URL string `json:"url,omitempty"`
	// Headers are HTTP headers to send with requests (sse/http only)
	Headers map[string]string `json:"headers,omitempty"`
	// SessionLossReplay controls whether a tool call is invoked once on a
	// replacement MCP session after the original session is lost. Empty keeps
	// the backward-compatible `once` behavior.
	SessionLossReplay MCPSessionLossReplay `json:"session_loss_replay,omitempty"`
	// ExclusiveLockFile is an optional cross-process lease for stdio servers.
	// It is held for the lifetime of the managed server, including reconnects.
	ExclusiveLockFile string `json:"exclusive_lock_file,omitempty"`
}

// MCPConfig defines configuration for all MCP servers
type MCPConfig struct {
	ToolConfig `                    envPrefix:"MINTCLAW_TOOLS_MCP_"`
	Discovery  ToolDiscoveryConfig `                                json:"discovery"`
	// MaxInlineTextChars controls how much MCP text stays inline before it is saved as an artifact.
	MaxInlineTextChars int `json:"max_inline_text_chars,omitempty" env:"MINTCLAW_TOOLS_MCP_MAX_INLINE_TEXT_CHARS"`
	// Servers is a map of server name to server configuration
	Servers map[string]MCPServerConfig `json:"servers,omitempty"`
}

const DefaultMCPMaxInlineTextChars = 16 * 1024

func (c *MCPConfig) GetMaxInlineTextChars() int {
	if c.MaxInlineTextChars > 0 {
		return c.MaxInlineTextChars
	}
	return DefaultMCPMaxInlineTextChars
}
