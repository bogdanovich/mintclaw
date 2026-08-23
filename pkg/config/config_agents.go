package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"time"
)

type AgentsConfig struct {
	Defaults AgentDefaults   `json:"defaults"`
	List     []AgentConfig   `json:"list,omitempty"`
	Dispatch *DispatchConfig `json:"dispatch,omitempty"`
}

// AgentModelConfig supports both string and structured model config.
// String format: "gpt-4" (just primary, no fallbacks)
// Object format: {"primary": "gpt-4", "fallbacks": ["claude-haiku"]}
type AgentModelConfig struct {
	Primary   string   `json:"primary,omitempty"`
	Fallbacks []string `json:"fallbacks,omitempty"`
}

func (m *AgentModelConfig) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		m.Primary = s
		m.Fallbacks = nil
		return nil
	}
	type raw struct {
		Primary   string   `json:"primary"`
		Fallbacks []string `json:"fallbacks"`
	}
	var r raw
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	m.Primary = r.Primary
	m.Fallbacks = r.Fallbacks
	return nil
}

func (m AgentModelConfig) MarshalJSON() ([]byte, error) {
	if len(m.Fallbacks) == 0 && m.Primary != "" {
		return json.Marshal(m.Primary)
	}
	type raw struct {
		Primary   string   `json:"primary,omitempty"`
		Fallbacks []string `json:"fallbacks,omitempty"`
	}
	return json.Marshal(raw(m))
}

type AgentConfig struct {
	ID               string            `json:"id"`
	Default          bool              `json:"default,omitempty"`
	Name             string            `json:"name,omitempty"`
	Workspace        string            `json:"workspace,omitempty"`
	Model            *AgentModelConfig `json:"model,omitempty"`
	Skills           []string          `json:"skills,omitempty"`
	Subagents        *SubagentsConfig  `json:"subagents,omitempty"`
	TargetPolicy     *TargetPolicy     `json:"target_policy,omitempty"`
	MaxParallelTurns int               `json:"max_parallel_turns,omitempty"`
}

type SubagentsConfig struct {
	AllowAgents              []string          `json:"allow_agents,omitempty"`
	Model                    *AgentModelConfig `json:"model,omitempty"`
	SessionModelOverrideMode string            `json:"session_model_override_mode,omitempty"`
}

type DispatchConfig struct {
	Rules []DispatchRule `json:"rules,omitempty"`
}

type DispatchRule struct {
	Name              string           `json:"name,omitempty"`
	Agent             string           `json:"agent"`
	When              DispatchSelector `json:"when"`
	SessionDimensions []string         `json:"session_dimensions,omitempty"`
}

type DispatchSelector struct {
	Channel   string `json:"channel,omitempty"`
	Account   string `json:"account,omitempty"`
	Space     string `json:"space,omitempty"`
	Chat      string `json:"chat,omitempty"`
	Topic     string `json:"topic,omitempty"`
	Sender    string `json:"sender,omitempty"`
	Mentioned *bool  `json:"mentioned,omitempty"`
}

type SessionConfig struct {
	Dimensions    []string                `json:"dimensions,omitempty"`
	IdentityLinks map[string][]string     `json:"identity_links,omitempty"`
	DmScope       string                  `json:"dm_scope,omitempty"`
	Lifecycle     *SessionLifecycleConfig `json:"lifecycle,omitempty"`
}

type SessionLifecycleConfig struct {
	Strategy           string `json:"strategy,omitempty"`
	Period             string `json:"period,omitempty"`
	Timezone           string `json:"timezone,omitempty"`
	IdleTimeoutMinutes int    `json:"idle_timeout_minutes,omitempty"`
	MaxAgeMinutes      int    `json:"max_age_minutes,omitempty"`
}

func (s *SessionLifecycleConfig) Enabled() bool {
	if s == nil {
		return false
	}
	strategy := strings.ToLower(strings.TrimSpace(s.Strategy))
	return strategy != "" && strategy != "never"
}

func (s *SessionLifecycleConfig) Validate() error {
	if s == nil {
		return nil
	}
	strategy := strings.ToLower(strings.TrimSpace(s.Strategy))
	switch strategy {
	case "":
		return fmt.Errorf("session.lifecycle.strategy is required")
	case "never":
		return nil
	case "calendar":
		period := strings.ToLower(strings.TrimSpace(s.Period))
		if period != "day" && period != "week" && period != "month" {
			return fmt.Errorf("session.lifecycle.period must be day, week, or month")
		}
		if strings.TrimSpace(s.Timezone) == "" {
			return fmt.Errorf("session.lifecycle.timezone is required for calendar strategy")
		}
		if _, err := time.LoadLocation(s.Timezone); err != nil {
			return fmt.Errorf("invalid session.lifecycle.timezone %q: %w", s.Timezone, err)
		}
		return nil
	case "idle":
		if s.IdleTimeoutMinutes <= 0 {
			return fmt.Errorf("session.lifecycle.idle_timeout_minutes must be positive")
		}
		return nil
	case "max_age":
		if s.MaxAgeMinutes <= 0 {
			return fmt.Errorf("session.lifecycle.max_age_minutes must be positive")
		}
		return nil
	default:
		return fmt.Errorf("unsupported session.lifecycle.strategy %q", s.Strategy)
	}
}

// ApplyDmScope translates the user-facing dm_scope value into the internal
// dimensions array that the routing layer consumes. It is a no-op when
// DmScope is empty or when Dimensions is already set (explicit Dimensions
// take precedence over the derived value).
func (s *SessionConfig) ApplyDmScope() {
	if s.DmScope == "" || len(s.Dimensions) > 0 {
		return
	}
	switch s.DmScope {
	case "per-channel-peer":
		s.Dimensions = []string{"chat", "sender"}
	case "per-channel":
		s.Dimensions = []string{"chat"}
	case "per-peer":
		s.Dimensions = []string{"sender"}
	case "global":
		s.Dimensions = nil
	}
}

// DeriveDmScope sets DmScope based on Dimensions when DmScope is empty.
// This handles legacy/fresh configs that only have explicit Dimensions
// without a corresponding DmScope value, ensuring the API response always
// includes a dm_scope that matches the actual runtime dimensions.
func (s *SessionConfig) DeriveDmScope() {
	if s.DmScope != "" || len(s.Dimensions) == 0 {
		return
	}
	switch {
	case slices.Equal(s.Dimensions, []string{"chat", "sender"}):
		s.DmScope = "per-channel-peer"
	case slices.Equal(s.Dimensions, []string{"chat"}):
		s.DmScope = "per-channel"
	case slices.Equal(s.Dimensions, []string{"sender"}):
		s.DmScope = "per-peer"
	}
	// Dimensions not matching any known scope mapping (custom array)
	// is fine — DmScope stays empty and the UI can handle it.
}

// RoutingConfig controls the intelligent model routing feature.
// When enabled, each incoming message is scored against structural features
// (message length, code blocks, tool call history, conversation depth, attachments).
// Messages scoring below Threshold are sent to LightModel; all others use the
// agent's primary model. This reduces cost and latency for simple tasks without
// requiring any keyword matching — all scoring is language-agnostic.
type RoutingConfig struct {
	Enabled    bool    `json:"enabled"`
	LightModel string  `json:"light_model"` // model_name from model_list to use for simple tasks
	Threshold  float64 `json:"threshold"`   // complexity score in [0,1]; score >= threshold → primary model
}

// SubTurnConfig configures the SubTurn execution system.
type SubTurnConfig struct {
	MaxDepth              int `json:"max_depth"               env:"MINTCLAW_AGENTS_DEFAULTS_SUBTURN_MAX_DEPTH"`
	MaxConcurrent         int `json:"max_concurrent"          env:"MINTCLAW_AGENTS_DEFAULTS_SUBTURN_MAX_CONCURRENT"`
	DefaultTimeoutMinutes int `json:"default_timeout_minutes" env:"MINTCLAW_AGENTS_DEFAULTS_SUBTURN_DEFAULT_TIMEOUT_MINUTES"`
	DefaultTokenBudget    int `json:"default_token_budget"    env:"MINTCLAW_AGENTS_DEFAULTS_SUBTURN_DEFAULT_TOKEN_BUDGET"`
	// ConcurrencyTimeoutSec limits pre-start waits for parent and target-agent capacity.
	ConcurrencyTimeoutSec int `json:"concurrency_timeout_sec" env:"MINTCLAW_AGENTS_DEFAULTS_SUBTURN_CONCURRENCY_TIMEOUT_SEC"`
}

type ToolFeedbackConfig struct {
	Enabled                bool   `json:"enabled"                   env:"MINTCLAW_AGENTS_DEFAULTS_TOOL_FEEDBACK_ENABLED"`
	MaxArgsLength          int    `json:"max_args_length"           env:"MINTCLAW_AGENTS_DEFAULTS_TOOL_FEEDBACK_MAX_ARGS_LENGTH"`
	SeparateMessages       bool   `json:"separate_messages"         env:"MINTCLAW_AGENTS_DEFAULTS_TOOL_FEEDBACK_SEPARATE_MESSAGES"`
	Subagents              *bool  `json:"subagents,omitempty"`
	Style                  string `json:"style,omitempty"           env:"MINTCLAW_AGENTS_DEFAULTS_TOOL_FEEDBACK_STYLE"`
	AnimationIntervalSecs  int    `json:"animation_interval_secs"   env:"MINTCLAW_AGENTS_DEFAULTS_TOOL_FEEDBACK_ANIMATION_INTERVAL_SECS"`
	EditMinIntervalSeconds int    `json:"edit_min_interval_seconds" env:"MINTCLAW_AGENTS_DEFAULTS_TOOL_FEEDBACK_EDIT_MIN_INTERVAL_SECONDS"`
}

type ResponseFooterConfig struct {
	Enabled bool `json:"enabled" env:"MINTCLAW_AGENTS_DEFAULTS_RESPONSE_FOOTER_ENABLED"`
}

const (
	DefaultPromptMemoryLongTermMaxBytes   = 32 * 1024
	DefaultPromptMemoryDailyNotesMaxBytes = 16 * 1024
	DefaultPromptMemoryRecentDays         = 3
	maxPromptMemoryRecentDays             = 31
)

// PromptMemoryConfig bounds workspace Markdown injected into the system prompt.
type PromptMemoryConfig struct {
	LongTermMaxBytes   int `json:"long_term_max_bytes,omitempty"   env:"MINTCLAW_AGENTS_DEFAULTS_PROMPT_MEMORY_LONG_TERM_MAX_BYTES"`
	DailyNotesMaxBytes int `json:"daily_notes_max_bytes,omitempty" env:"MINTCLAW_AGENTS_DEFAULTS_PROMPT_MEMORY_DAILY_NOTES_MAX_BYTES"`
	RecentDays         int `json:"recent_days,omitempty"           env:"MINTCLAW_AGENTS_DEFAULTS_PROMPT_MEMORY_RECENT_DAYS"`
}

func (c PromptMemoryConfig) EffectiveLongTermMaxBytes() int {
	if c.LongTermMaxBytes > 0 {
		return c.LongTermMaxBytes
	}
	return DefaultPromptMemoryLongTermMaxBytes
}

func (c PromptMemoryConfig) EffectiveDailyNotesMaxBytes() int {
	if c.DailyNotesMaxBytes > 0 {
		return c.DailyNotesMaxBytes
	}
	return DefaultPromptMemoryDailyNotesMaxBytes
}

func (c PromptMemoryConfig) EffectiveRecentDays() int {
	if c.RecentDays > 0 {
		return c.RecentDays
	}
	return DefaultPromptMemoryRecentDays
}

func (c PromptMemoryConfig) Validate() error {
	if c.LongTermMaxBytes < 0 {
		return errors.New("agents.defaults.prompt_memory.long_term_max_bytes must not be negative")
	}
	if c.DailyNotesMaxBytes < 0 {
		return errors.New("agents.defaults.prompt_memory.daily_notes_max_bytes must not be negative")
	}
	if c.RecentDays < 0 || c.RecentDays > maxPromptMemoryRecentDays {
		return fmt.Errorf(
			"agents.defaults.prompt_memory.recent_days must be between 0 and %d",
			maxPromptMemoryRecentDays,
		)
	}
	return nil
}

type AgentDefaults struct {
	Workspace                 string               `json:"workspace"                        env:"MINTCLAW_AGENTS_DEFAULTS_WORKSPACE"`
	RestrictToWorkspace       bool                 `json:"restrict_to_workspace"            env:"MINTCLAW_AGENTS_DEFAULTS_RESTRICT_TO_WORKSPACE"`
	AllowReadOutsideWorkspace bool                 `json:"allow_read_outside_workspace"     env:"MINTCLAW_AGENTS_DEFAULTS_ALLOW_READ_OUTSIDE_WORKSPACE"`
	Provider                  string               `json:"provider"                         env:"MINTCLAW_AGENTS_DEFAULTS_PROVIDER"`
	ModelName                 string               `json:"model_name"                       env:"MINTCLAW_AGENTS_DEFAULTS_MODEL_NAME"`
	ModelFallbacks            []string             `json:"model_fallbacks,omitempty"`
	ImageModel                string               `json:"image_model,omitempty"            env:"MINTCLAW_AGENTS_DEFAULTS_IMAGE_MODEL"`
	ImageModelFallbacks       []string             `json:"image_model_fallbacks,omitempty"`
	MaxTokens                 int                  `json:"max_tokens"                       env:"MINTCLAW_AGENTS_DEFAULTS_MAX_TOKENS"`
	ContextWindow             int                  `json:"context_window,omitempty"         env:"MINTCLAW_AGENTS_DEFAULTS_CONTEXT_WINDOW"`
	Temperature               *float64             `json:"temperature,omitempty"            env:"MINTCLAW_AGENTS_DEFAULTS_TEMPERATURE"`
	MaxToolIterations         int                  `json:"max_tool_iterations"              env:"MINTCLAW_AGENTS_DEFAULTS_MAX_TOOL_ITERATIONS"`
	SummarizeMessageThreshold int                  `json:"summarize_message_threshold"      env:"MINTCLAW_AGENTS_DEFAULTS_SUMMARIZE_MESSAGE_THRESHOLD"`
	SummarizeTokenPercent     int                  `json:"summarize_token_percent"          env:"MINTCLAW_AGENTS_DEFAULTS_SUMMARIZE_TOKEN_PERCENT"`
	MaxMediaSize              int                  `json:"max_media_size,omitempty"         env:"MINTCLAW_AGENTS_DEFAULTS_MAX_MEDIA_SIZE"`
	Routing                   *RoutingConfig       `json:"routing,omitempty"`
	SteeringMode              string               `json:"steering_mode,omitempty"          env:"MINTCLAW_AGENTS_DEFAULTS_STEERING_MODE"`      // "one-at-a-time" (default) or "all"
	MaxParallelTurns          int                  `json:"max_parallel_turns,omitempty"     env:"MINTCLAW_AGENTS_DEFAULTS_MAX_PARALLEL_TURNS"` // Max concurrent turns (0 or 1 = sequential)
	SubTurn                   SubTurnConfig        `json:"subturn"                                                                                      envPrefix:"MINTCLAW_AGENTS_DEFAULTS_SUBTURN_"`
	ToolFeedback              ToolFeedbackConfig   `json:"tool_feedback,omitempty"`
	ResponseFooter            ResponseFooterConfig `json:"response_footer,omitempty"`
	PromptMemory              PromptMemoryConfig   `json:"prompt_memory,omitempty"`
	FinalTurnRenderMode       string               `json:"final_turn_render_mode,omitempty" env:"MINTCLAW_AGENTS_DEFAULTS_FINAL_TURN_RENDER_MODE"`
	SplitOnMarker             bool                 `json:"split_on_marker"                  env:"MINTCLAW_AGENTS_DEFAULTS_SPLIT_ON_MARKER"` // split messages on <|[SPLIT]|> marker
	ContextManager            string               `json:"context_manager,omitempty"        env:"MINTCLAW_AGENTS_DEFAULTS_CONTEXT_MANAGER"`
	ContextManagerConfig      json.RawMessage      `json:"context_manager_config,omitempty" env:"MINTCLAW_AGENTS_DEFAULTS_CONTEXT_MANAGER_CONFIG"`
	TurnProfile               TurnProfileConfig    `json:"turn_profile,omitempty"`
	MaxLLMRetries             int                  `json:"max_llm_retries,omitempty"        env:"MINTCLAW_AGENTS_DEFAULTS_MAX_LLM_RETRIES"`
	LLMRetryBackoffSecs       int                  `json:"llm_retry_backoff_secs,omitempty" env:"MINTCLAW_AGENTS_DEFAULTS_LLM_RETRY_BACKOFF_SECS"`
	Subagents                 *SubagentsConfig     `json:"subagents,omitempty"`
	TargetPolicy              *TargetPolicy        `json:"target_policy,omitempty"`
}

const DefaultMaxMediaSize = 20 * 1024 * 1024 // 20 MB

func (d *AgentDefaults) GetMaxMediaSize() int {
	if d.MaxMediaSize > 0 {
		return d.MaxMediaSize
	}
	return DefaultMaxMediaSize
}

func (d *AgentDefaults) validateResultRetentionOwnership() error {
	if d.ContextManager != "seahorse" || len(d.ContextManagerConfig) == 0 {
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(d.ContextManagerConfig, &raw); err != nil {
		return fmt.Errorf("invalid agents.defaults.context_manager_config: %w", err)
	}
	for key := range raw {
		if strings.EqualFold(key, "toolResultRetention") {
			return fmt.Errorf(
				"agents.defaults.context_manager_config.%s is not supported; "+
					"use tools.result_retention",
				key,
			)
		}
	}
	return nil
}

func (d *AgentDefaults) validateContextManagerSelection() error {
	if strings.EqualFold(strings.TrimSpace(d.ContextManager), "legacy") {
		return fmt.Errorf(
			"agents.defaults.context_manager %q is no longer supported; use %q or %q",
			d.ContextManager,
			"seahorse",
			"none",
		)
	}
	if strings.EqualFold(strings.TrimSpace(d.ContextManager), "none") &&
		len(d.ContextManagerConfig) > 0 {
		return fmt.Errorf(
			"agents.defaults.context_manager_config requires context_manager %q",
			"seahorse",
		)
	}
	return nil
}

// GetToolFeedbackMaxArgsLength returns the max visible text length for tool argument previews.
func (d *AgentDefaults) GetToolFeedbackMaxArgsLength() int {
	if d.ToolFeedback.MaxArgsLength > 0 {
		return d.ToolFeedback.MaxArgsLength
	}
	return 300
}

// IsToolFeedbackEnabled returns true when tool feedback messages should be sent to the chat.
func (d *AgentDefaults) IsToolFeedbackEnabled() bool {
	return d.ToolFeedback.Enabled
}

// IsSubagentToolFeedbackEnabled returns true when subagent turns should publish
// visible tool feedback. It defaults to true for backward compatibility when
// tool_feedback itself is enabled.
func (d *AgentDefaults) IsSubagentToolFeedbackEnabled() bool {
	return d.ToolFeedback.Subagents == nil || *d.ToolFeedback.Subagents
}

// IsToolFeedbackSeparateMessagesEnabled returns true when each tool feedback
// update should be sent as its own chat message instead of editing a single
// in-place progress message.
func (d *AgentDefaults) IsToolFeedbackSeparateMessagesEnabled() bool {
	return d.ToolFeedback.SeparateMessages
}

func (d *AgentDefaults) IsResponseFooterEnabled() bool {
	return d.ResponseFooter.Enabled
}

func (d *AgentDefaults) GetToolFeedbackStyle() string {
	return strings.TrimSpace(d.ToolFeedback.Style)
}

func (d *AgentDefaults) GetToolFeedbackAnimationInterval() time.Duration {
	if d.ToolFeedback.AnimationIntervalSecs > 0 {
		return time.Duration(d.ToolFeedback.AnimationIntervalSecs) * time.Second
	}
	return 3 * time.Second
}

func (d *AgentDefaults) GetToolFeedbackEditMinInterval() time.Duration {
	if d.ToolFeedback.EditMinIntervalSeconds > 0 {
		return time.Duration(d.ToolFeedback.EditMinIntervalSeconds) * time.Second
	}
	return 0
}

func (d *AgentDefaults) UseFinalTurnRender() bool {
	return strings.EqualFold(strings.TrimSpace(d.FinalTurnRenderMode), "llm")
}

// GetModelName returns the configured default model name.
func (d *AgentDefaults) GetModelName() string {
	return d.ModelName
}

// GroupTriggerConfig controls when the bot responds in group chats.
type GroupTriggerConfig struct {
	Disabled             bool                          `json:"disabled,omitempty"`
	MentionOnly          bool                          `json:"mention_only,omitempty"`
	IgnoreNonBotMentions *bool                         `json:"ignore_non_bot_mentions,omitempty"`
	IgnoreNonBotReplies  *bool                         `json:"ignore_non_bot_replies,omitempty"`
	Prefixes             []string                      `json:"prefixes,omitempty"`
	Topics               map[string]GroupTriggerConfig `json:"topics,omitempty"`
}

// TypingConfig controls typing indicator behavior (Phase 10).
type TypingConfig struct {
	Enabled bool `json:"enabled,omitempty"`
}

// PlaceholderConfig controls placeholder message behavior (Phase 10).
type PlaceholderConfig struct {
	Enabled bool                `json:"enabled"`
	Text    FlexibleStringSlice `json:"text,omitempty"`
}

// GetRandomText returns a random placeholder text, or default if none set.
func (p *PlaceholderConfig) GetRandomText() string {
	if len(p.Text) == 0 {
		return "Thinking..."
	}
	if len(p.Text) == 1 {
		return p.Text[0]
	}
	idx := rand.IntN(len(p.Text))
	return p.Text[idx]
}

type StreamingConfig struct {
	Enabled         bool `json:"enabled,omitempty"`
	ThrottleSeconds int  `json:"throttle_seconds,omitempty"`
	MinGrowthChars  int  `json:"min_growth_chars,omitempty"`
}

func (c StreamingConfig) IsZero() bool {
	return !c.Enabled && c.ThrottleSeconds == 0 && c.MinGrowthChars == 0
}

func (c StreamingConfig) WithDefaults(throttleSeconds, minGrowthChars int) StreamingConfig {
	if c.Enabled {
		if c.ThrottleSeconds == 0 {
			c.ThrottleSeconds = throttleSeconds
		}
		if c.MinGrowthChars == 0 {
			c.MinGrowthChars = minGrowthChars
		}
	}
	return c
}
