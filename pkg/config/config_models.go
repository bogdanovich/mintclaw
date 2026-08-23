package config

import (
	"fmt"
	"strings"

	providercommon "github.com/bogdanovich/mintclaw/pkg/providers/common"
)

type HeartbeatConfig struct {
	Enabled  bool `json:"enabled"  env:"MINTCLAW_HEARTBEAT_ENABLED"`
	Interval int  `json:"interval" env:"MINTCLAW_HEARTBEAT_INTERVAL"` // minutes, min 5
}

type DevicesConfig struct {
	Enabled    bool `json:"enabled"     env:"MINTCLAW_DEVICES_ENABLED"`
	MonitorUSB bool `json:"monitor_usb" env:"MINTCLAW_DEVICES_MONITOR_USB"`
}

type NodesConfig struct {
	Enabled                bool `json:"enabled,omitempty"                  env:"MINTCLAW_NODES_ENABLED"`
	TerminalEnabled        bool `json:"terminal_enabled,omitempty"         env:"MINTCLAW_NODES_TERMINAL_ENABLED"`
	AllowLoopbackPlaintext bool `json:"allow_loopback_plaintext,omitempty" env:"MINTCLAW_NODES_ALLOW_LOOPBACK_PLAINTEXT"`
	MaxPendingPairings     int  `json:"max_pending_pairings,omitempty"     env:"MINTCLAW_NODES_MAX_PENDING_PAIRINGS"`
}

type VoiceConfig struct {
	ModelName         string `json:"model_name,omitempty"         env:"MINTCLAW_VOICE_MODEL_NAME"`
	TTSModelName      string `json:"tts_model_name,omitempty"     env:"MINTCLAW_VOICE_TTS_MODEL_NAME"`
	EchoTranscription bool   `json:"echo_transcription"           env:"MINTCLAW_VOICE_ECHO_TRANSCRIPTION"`
	ElevenLabsAPIKey  string `json:"elevenlabs_api_key,omitempty" env:"MINTCLAW_VOICE_ELEVENLABS_API_KEY"`
}

type ModelStreamingConfig struct {
	Enabled bool `json:"enabled,omitempty"`
}

func (c ModelStreamingConfig) IsZero() bool {
	return !c.Enabled
}

// ModelConfig represents a model-centric provider configuration.
// It allows adding new providers (especially OpenAI-compatible ones) via configuration only.
// The Model field may be either a plain model identifier or a provider-prefixed
// identifier such as "openai/gpt-5.4" or "nvidia/z-ai/glm-5.1".
// Supported providers include openai, anthropic, antigravity, claude-cli,
// codex-cli, github-copilot, and named OpenAI-compatible protocols such as
// groq, deepseek, modelscope, and novita.
type ModelConfig struct {
	// Required fields
	ModelName string `json:"model_name"` // User-facing alias for the model
	Provider  string `json:"provider"`   // Provider name for routing and selection. When empty, provider resolution infers it from Model.
	Model     string `json:"model"`      // Model identifier, optionally provider-prefixed.

	// HTTP-based providers
	APIBase   string   `json:"api_base,omitempty"`  // API endpoint URL
	Proxy     string   `json:"proxy,omitempty"`     // HTTP proxy URL
	Fallbacks []string `json:"fallbacks,omitempty"` // Fallback model names for failover

	// Special providers (CLI-based, OAuth, etc.)
	AuthMethod  string `json:"auth_method,omitempty"`  // Authentication method: oauth, token
	ConnectMode string `json:"connect_mode,omitempty"` // Connection mode: stdio, grpc
	Workspace   string `json:"workspace,omitempty"`    // Workspace path for CLI-based providers

	// Optional optimizations
	RPM                 int                  `json:"rpm,omitempty"`              // Requests per minute limit
	MaxTokensField      string               `json:"max_tokens_field,omitempty"` // Field name for max tokens (e.g., "max_completion_tokens")
	RequestTimeout      int                  `json:"request_timeout,omitempty"`
	ThinkingLevel       string               `json:"thinking_level,omitempty"`        // Extended thinking: off|low|medium|high|xhigh|adaptive
	ToolSchemaTransform string               `json:"tool_schema_transform,omitempty"` // Optional tool schema compatibility transform (e.g. "simple")
	Streaming           ModelStreamingConfig `json:"streaming,omitzero"`              // Opt-in for provider streaming on this model entry
	ExtraBody           map[string]any       `json:"extra_body,omitempty"`            // Additional fields to inject into request body
	CustomHeaders       map[string]string    `json:"custom_headers,omitempty"`        // Additional headers to inject into every HTTP request
	Capabilities        *ModelCapabilities   `json:"capabilities,omitempty"`          // Optional capability-specific model overrides (for example vision)

	APIKeys SecureStrings `json:"api_keys,omitzero" yaml:"api_keys,omitempty"` // API authentication keys (multiple keys for failover)

	// Enabled indicates whether this model entry is active. When omitted in
	// existing configs, the field is inferred during load: models with API keys
	// or the reserved "local-model" name are auto-enabled.
	Enabled bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	// UserAgent is the user agent string to use for HTTP requests.
	UserAgent string `json:"user_agent,omitempty" yaml:"-"`

	// isVirtual marks this model as a virtual model generated from multi-key expansion.
	// Virtual models should not be persisted to config files.
	isVirtual bool
}

type ModelCapabilities struct {
	Vision *ModelCapabilityOverride `json:"vision,omitempty"`
}

type ModelCapabilityOverride struct {
	Model     string   `json:"model,omitempty"`
	Fallbacks []string `json:"fallbacks,omitempty"`
}

// IsEffectivelyEnabled reports whether a model entry should be treated as active.
// For backward compatibility, models with API keys or the reserved "local-model"
// alias remain active even when callers construct Config directly without
// materializing Enabled=true first.
func (c *ModelConfig) IsEffectivelyEnabled() bool {
	if c == nil {
		return false
	}
	if c.Enabled {
		return true
	}
	if len(c.APIKeys) > 0 {
		return true
	}
	return strings.TrimSpace(c.ModelName) == "local-model"
}

// APIKey returns the first API key from apiKeys
func (c *ModelConfig) APIKey() string {
	if len(c.APIKeys) > 0 {
		return c.APIKeys[0].String()
	}
	return ""
}

// IsVirtual returns true if this model was generated from multi-key expansion.
func (c *ModelConfig) IsVirtual() bool {
	return c.isVirtual
}

// Validate checks if the ModelConfig has all required fields.
func (c *ModelConfig) Validate() error {
	if c.ModelName == "" {
		return fmt.Errorf("model_name is required")
	}
	if c.ModelName != strings.TrimSpace(c.ModelName) {
		return fmt.Errorf("model_name must not have surrounding whitespace")
	}
	if c.Model == "" {
		return fmt.Errorf("model is required")
	}
	if _, err := providercommon.NormalizeToolSchemaTransform(c.ToolSchemaTransform); err != nil {
		return err
	}

	// Reject whitespace in model identifier
	if strings.ContainsAny(c.Model, " \t\n\r") {
		return fmt.Errorf("model identifier contains whitespace")
	}

	// Reject leading slash
	if strings.HasPrefix(c.Model, "/") {
		return fmt.Errorf("model identifier must not start with /")
	}

	// Reject consecutive slashes
	if strings.Contains(c.Model, "//") {
		return fmt.Errorf("model identifier must not contain //")
	}
	return nil
}

func (c *ModelConfig) SetAPIKey(value string) {
	if len(c.APIKeys) > 0 {
		c.APIKeys[0].Set(value)
	} else {
		c.APIKeys = append(c.APIKeys, NewSecureString(value))
	}
}
