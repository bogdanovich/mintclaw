// MintClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package config

import (
	"encoding/json"
	"path/filepath"

	"github.com/bogdanovich/mintclaw/pkg"
)

const defaultMediaRetentionMinutes = 7 * 24 * 60

func DefaultAgentConfig() AgentConfig {
	return AgentConfig{
		ID:          "main",
		Default:     true,
		Name:        "mintclaw",
		Description: "The default general-purpose assistant for everyday conversation, problem solving, and workspace help.",
	}
}

// DefaultConfig returns the default configuration for MintClaw.
func DefaultConfig() *Config {
	workspacePath := filepath.Join(GetHome(), pkg.WorkspaceName)

	return &Config{
		Version: CurrentVersion,
		// Isolation is opt-in so existing installations keep their current behavior
		// until the user explicitly enables subprocess sandboxing.
		Isolation: IsolationConfig{
			Enabled: false,
		},
		Agents: AgentsConfig{
			Defaults: AgentDefaults{
				Workspace:                 workspacePath,
				RestrictToWorkspace:       true,
				Provider:                  "",
				MaxTokens:                 32768,
				Temperature:               nil, // nil means use provider default
				MaxToolIterations:         50,
				SummarizeMessageThreshold: 20,
				SummarizeTokenPercent:     75,
				SteeringMode:              "one-at-a-time",
				PromptMemory: PromptMemoryConfig{
					LongTermMaxBytes:   DefaultPromptMemoryLongTermMaxBytes,
					DailyNotesMaxBytes: DefaultPromptMemoryDailyNotesMaxBytes,
					RecentDays:         DefaultPromptMemoryRecentDays,
				},
				ToolFeedback: ToolFeedbackConfig{
					Enabled:                false,
					MaxArgsLength:          300,
					SeparateMessages:       false,
					Subagents:              true,
					AnimationIntervalSecs:  0,
					EditMinIntervalSeconds: 0,
				},
				ResponseFooter: ResponseFooterConfig{
					Enabled: true,
				},
				FinalTurnRenderMode: "",
				SplitOnMarker:       false,
				ContextManager:      "seahorse",
				MaxLLMRetries:       2,
				LLMRetryBackoffSecs: 2,
			},
			List: []AgentConfig{DefaultAgentConfig()},
		},
		Session: SessionConfig{
			Dimensions: []string{"chat"},
		},
		Channels: defaultChannels(),
		Hooks: HooksConfig{
			Enabled: true,
			Defaults: HookDefaultsConfig{
				ObserverTimeoutMS:    500,
				InterceptorTimeoutMS: 5000,
				ApprovalTimeoutMS:    60000,
			},
		},
		// Provider examples belong in config/config.example.json. Runtime defaults
		// intentionally start without selectable model routes so onboarding or an
		// explicit operator configuration owns every credential and endpoint.
		ModelList: SecureModelList{},
		Gateway: GatewayConfig{
			Host:      "localhost",
			Port:      18790,
			HotReload: false,
			LogLevel:  DefaultGatewayLogLevel,
		},
		Events: EventsConfig{
			Logging: defaultEventLoggingConfig(),
		},
		Diagnostics: defaultDiagnosticsConfig(),
		Tasks:       defaultTaskConfig(),
		Tools: ToolsConfig{
			FilterSensitiveData: true,
			FilterMinLength:     8,
			Approval: ToolApprovalConfig{
				Mode: ToolApprovalModeRequired,
			},
			LoopDetection: ToolLoopDetectionConfig{
				Enabled:             true,
				WarningsEnabled:     true,
				HardStopsEnabled:    false,
				ExactFailureWarn:    2,
				ExactFailureBlock:   5,
				SameToolFailureWarn: 3,
				SameToolFailureHalt: 8,
				NoProgressWarn:      2,
				NoProgressBlock:     5,
				IdenticalCallWarn:   2,
				IdenticalCallHalt:   4,
				MaxSignatures:       64,
			},
			MediaCleanup: MediaCleanupConfig{
				ToolConfig: ToolConfig{
					Enabled: true,
				},
				MaxAge:   defaultMediaRetentionMinutes,
				Interval: 5,
			},
			Web: WebToolsConfig{
				ToolConfig: ToolConfig{
					Enabled: true,
				},
				Provider:        "auto",
				PreferNative:    true,
				Proxy:           "",
				FetchLimitBytes: 10 * 1024 * 1024, // 10MB by default
				Format:          "plaintext",
				Brave: BraveConfig{
					Enabled:    false,
					MaxResults: 5,
				},
				Tavily: TavilyConfig{
					Enabled:    false,
					MaxResults: 5,
				},
				Kagi: KagiConfig{
					Enabled:    false,
					BaseURL:    "https://kagi.com/api/v1/search",
					MaxResults: 5,
				},
				Sogou: SogouConfig{
					Enabled:    true,
					MaxResults: 5,
				},
				DuckDuckGo: DuckDuckGoConfig{
					Enabled:    false,
					MaxResults: 5,
				},
				Gemini: GeminiSearchConfig{
					Enabled:    false,
					Model:      "gemini-2.5-flash",
					MaxResults: 5,
				},
				Perplexity: PerplexityConfig{
					Enabled:    false,
					MaxResults: 5,
				},
				SearXNG: SearXNGConfig{
					Enabled:    false,
					BaseURL:    "",
					MaxResults: 5,
				},
				GLMSearch: GLMSearchConfig{
					Enabled:      false,
					BaseURL:      "https://open.bigmodel.cn/api/paas/v4/web_search",
					SearchEngine: "search_std",
					MaxResults:   5,
				},
				BaiduSearch: BaiduSearchConfig{
					Enabled:    false,
					BaseURL:    "https://qianfan.baidubce.com/v2/ai_search/web_search",
					MaxResults: 10,
				},
			},
			Cron: CronToolsConfig{
				ToolConfig: ToolConfig{
					Enabled: true,
				},
				ExecTimeoutMinutes: 5,
				AllowCommand:       true,
			},
			Document: DocumentToolsConfig{
				ToolConfig:              ToolConfig{Enabled: true},
				MaxConcurrentOperations: 1,
				QueueTimeoutSeconds:     30,
			},
			Exec: ExecConfig{
				ToolConfig: ToolConfig{
					Enabled: true,
				},
				EnableDenyPatterns: true,
				AllowRemote:        true,
				PermissionMode:     "",
				TimeoutSeconds:     60,
			},
			Skills: SkillsToolsConfig{
				ToolConfig: ToolConfig{
					Enabled: true,
				},
				Registries: SkillsRegistriesConfig{
					"clawhub": {
						Enabled: true,
						BaseURL: "https://clawhub.ai",
						Param:   map[string]any{},
					},
					"github": {
						Enabled: true,
						BaseURL: "https://github.com",
						Param:   map[string]any{},
					},
				},
				MaxConcurrentSearches: 2,
				SearchCache: SearchCacheConfig{
					MaxSize:    50,
					TTLSeconds: 300,
				},
			},
			SendFile: ToolConfig{
				Enabled: true,
			},
			ImageGenerate: ImageGenerateToolsConfig{
				ToolConfig: ToolConfig{
					Enabled: false,
				},
			},
			SendTTS: ToolConfig{
				Enabled: false,
			},
			MCP: MCPConfig{
				ToolConfig: ToolConfig{
					Enabled: false,
				},
				Discovery: ToolDiscoveryConfig{
					Enabled:          false,
					TTL:              5,
					MaxSearchResults: 5,
					UseBM25:          true,
					UseRegex:         false,
				},
				MaxInlineTextChars: DefaultMCPMaxInlineTextChars,
				Servers:            map[string]MCPServerConfig{},
			},
			AppendFile: ToolConfig{
				Enabled: true,
			},
			ApplyPatch: ToolConfig{
				Enabled: true,
			},
			FindSkills: ToolConfig{
				Enabled: true,
			},
			I2C: ToolConfig{
				Enabled: false, // Hardware tool - Linux only
			},
			InstallSkill: ToolConfig{
				Enabled: true,
			},
			ListDir: ToolConfig{
				Enabled: true,
			},
			LoadImage: ToolConfig{
				Enabled: true,
			},
			Memory: ToolConfig{
				Enabled: true,
			},
			Message: MessageToolsConfig{
				ToolConfig: ToolConfig{
					Enabled: true,
				},
				MediaEnabled: false,
			},
			ReadFile: ReadFileToolConfig{
				Enabled:         true,
				Mode:            ReadFileModeBytes,
				MaxReadFileSize: 64 * 1024, // 64KB
			},
			RequestUserInput: RequestUserInputToolsConfig{
				Enabled:               true,
				DefaultTimeoutSeconds: 3600,
				MaxTimeoutSeconds:     86400,
				RetentionHours:        168,
			},
			Serial: ToolConfig{
				Enabled: false, // Hardware tool - requires host serial ports
			},
			SearchFiles: ToolConfig{
				Enabled: true,
			},
			Spawn: ToolConfig{
				Enabled: true,
			},
			SPI: ToolConfig{
				Enabled: false, // Hardware tool - Linux only
			},
			Subagent: ToolConfig{
				Enabled: true,
			},
			UpdatePlan: ToolConfig{
				Enabled: false,
			},
			WebFetch: ToolConfig{
				Enabled: true,
			},
			WriteFile: ToolConfig{
				Enabled: true,
			},
		},
		Heartbeat: HeartbeatConfig{
			Enabled:  true,
			Interval: 30,
		},
		Devices: DevicesConfig{
			Enabled:    false,
			MonitorUSB: true,
		},
		Voice: VoiceConfig{
			ModelName:         "",
			TTSModelName:      "",
			EchoTranscription: false,
			ElevenLabsAPIKey:  "",
		},
		BuildInfo: BuildInfo{
			Version:   Version,
			GitCommit: GitCommit,
			BuildTime: BuildTime,
			GoVersion: GoVersion,
		},
	}
}

func defaultChannels() ChannelsConfig {
	defs := map[string]any{
		"whatsapp": map[string]any{
			"settings": map[string]any{
				"bridge_url": "ws://localhost:3001",
			},
		},
		"telegram": map[string]any{
			"typing":      map[string]any{"enabled": true},
			"placeholder": map[string]any{"enabled": true, "text": []string{"Thinking... 💭"}},
			"settings": map[string]any{
				"use_markdown_v2":      false,
				"media_group_delay_ms": 500,
			},
		},
		"feishu":  map[string]any{},
		"discord": map[string]any{},
		"maixcam": map[string]any{
			"settings": map[string]any{"host": "0.0.0.0", "port": 18790},
		},
		"qq": map[string]any{
			"settings": map[string]any{"max_message_length": 2000},
		},
		"dingtalk": map[string]any{},
		"slack":    map[string]any{},
		"matrix": map[string]any{
			"group_trigger": map[string]any{"mention_only": true},
			"placeholder":   map[string]any{"enabled": true, "text": []string{"Thinking... 💭"}},
			"settings": map[string]any{
				"homeserver":     "https://matrix.org",
				"join_on_invite": true,
			},
		},
		"deltachat": map[string]any{
			"group_trigger": map[string]any{"mention_only": true},
			"settings": map[string]any{
				"email":        "@nine.testrun.org",
				"display_name": "MintClaw Bot",
			},
		},
		"line": map[string]any{
			"group_trigger": map[string]any{"mention_only": true},
			"settings": map[string]any{
				"webhook_host": "0.0.0.0",
				"webhook_port": 18791,
				"webhook_path": "/webhook/line",
			},
		},
		"onebot": map[string]any{
			"settings": map[string]any{
				"ws_url":             "ws://127.0.0.1:3001",
				"reconnect_interval": 5,
			},
		},
		"wecom": map[string]any{
			"settings": map[string]any{
				"websocket_url":         "wss://openws.work.weixin.qq.com",
				"send_thinking_message": true,
			},
		},
		"weixin": map[string]any{
			"settings": map[string]any{
				"base_url":     "https://ilinkai.weixin.qq.com/",
				"cdn_base_url": "https://novac2c.cdn.weixin.qq.com/c2c",
			},
		},
		"mintclaw": map[string]any{
			"settings": map[string]any{
				"ping_interval":   30,
				"read_timeout":    60,
				"write_timeout":   10,
				"max_connections": 100,
				"streaming":       map[string]any{"enabled": true},
			},
		},
		"irc": map[string]any{
			"settings": map[string]any{
				"server":   "",
				"tls":      true,
				"nick":     "mintclaw",
				"channels": []string{},
			},
		},
	}

	channels := make(ChannelsConfig, len(defs))
	for name, def := range defs {
		data, err := json.Marshal(def)
		if err != nil {
			continue
		}
		bc := &Channel{}
		if err := json.Unmarshal(data, bc); err != nil {
			continue
		}
		bc.SetName(name)
		bc.Type = name
		channels[name] = bc
	}
	return channels
}
