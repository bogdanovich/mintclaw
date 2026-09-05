package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"gopkg.in/yaml.v3"

	"github.com/bogdanovich/mintclaw/pkg/credential"
)

func saveTestConfig(path string, cfg *Config) error {
	_, err := NewRepository(path).Save(cfg)
	return err
}

// mustSetupSSHKey generates a temporary Ed25519 SSH key in t.TempDir() and sets
// MINTCLAW_SSH_KEY_PATH to its path for the duration of the test. This is required
// whenever a test exercises encryption/decryption via credential.Encrypt or Repository.Save.
func mustSetupSSHKey(t *testing.T) {
	t.Helper()
	keyPath := filepath.Join(t.TempDir(), "mintclaw_ed25519.key")
	if err := credential.GenerateSSHKey(keyPath); err != nil {
		t.Fatalf("mustSetupSSHKey: %v", err)
	}
	t.Setenv("MINTCLAW_SSH_KEY_PATH", keyPath)
}

func TestAgentModelConfig_RejectsString(t *testing.T) {
	var m AgentModelConfig
	if err := json.Unmarshal([]byte(`"gpt-4"`), &m); err == nil {
		t.Fatal("unmarshal accepted string model config")
	}
}

func TestAgentModelConfig_UnmarshalObject(t *testing.T) {
	var m AgentModelConfig
	data := `{"primary": "claude-opus", "fallbacks": ["gpt-4o-mini", "haiku"]}`
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		t.Fatalf("unmarshal object: %v", err)
	}
	if m.Primary != "claude-opus" {
		t.Errorf("Primary = %q, want 'claude-opus'", m.Primary)
	}
	if len(m.Fallbacks) != 2 {
		t.Fatalf("Fallbacks len = %d, want 2", len(m.Fallbacks))
	}
	if m.Fallbacks[0] != "gpt-4o-mini" || m.Fallbacks[1] != "haiku" {
		t.Errorf("Fallbacks = %v", m.Fallbacks)
	}
}

func TestAgentModelConfig_MarshalObjectWithoutFallbacks(t *testing.T) {
	m := AgentModelConfig{Primary: "gpt-4"}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(data) != `{"primary":"gpt-4"}` {
		t.Errorf("marshal = %s, want object", string(data))
	}
}

func TestAgentModelConfig_MarshalObject(t *testing.T) {
	m := AgentModelConfig{Primary: "claude-opus", Fallbacks: []string{"haiku"}}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var result map[string]any
	_ = json.Unmarshal(data, &result)
	if result["primary"] != "claude-opus" {
		t.Errorf("primary = %v", result["primary"])
	}
}

func TestLoadConfigRejectsStringAgentModelWithoutRewriting(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	before := []byte(`{
  "version": 4,
  "agents": {
    "defaults": {"model_name": "primary"},
    "list": [{"id": "main", "default": true, "model": "primary"}]
  },
  "model_list": [
    {"model_name": "primary", "provider": "openai", "model": "gpt-5.4", "enabled": true}
  ]
}`)
	if err := os.WriteFile(configPath, before, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadConfig(configPath); err == nil {
		t.Fatal("LoadConfig() accepted string agent model")
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("LoadConfig() rewrote rejected config:\n%s", after)
	}
}

func TestAgentConfig_FullParse(t *testing.T) {
	jsonData := `{
		"agents": {
			"defaults": {
				"workspace": "~/.mintclaw/workspace",
				"model_name": "glm-4.7",
				"max_tokens": 8192,
				"max_tool_iterations": 20
			},
			"list": [
				{
					"id": "sales",
					"default": true,
					"name": "Sales Bot",
					"model": {"primary": "gpt-4"},
					"max_parallel_turns": 1
				},
			{
				"id": "support",
				"name": "Support Bot",
				"model": {
					"primary": "claude-opus",
					"fallbacks": ["haiku"]
				},
				"subagents": {
					"allow_agents": ["sales"]
				}
			}
			]
		},
		"session": {
			"dimensions": ["sender"],
			"identity_links": {
				"john": ["telegram:123", "discord:john#1234"]
			}
		}
	}`

	cfg := DefaultConfig()
	if err := json.Unmarshal([]byte(jsonData), cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(cfg.Agents.List) != 2 {
		t.Fatalf("agents.list len = %d, want 2", len(cfg.Agents.List))
	}

	sales := cfg.Agents.List[0]
	if sales.ID != "sales" || !sales.Default || sales.Name != "Sales Bot" {
		t.Errorf("sales = %+v", sales)
	}
	if sales.Model == nil || sales.Model.Primary != "gpt-4" {
		t.Errorf("sales.Model = %+v", sales.Model)
	}
	if sales.MaxParallelTurns != 1 {
		t.Errorf("sales.MaxParallelTurns = %d, want 1", sales.MaxParallelTurns)
	}

	support := cfg.Agents.List[1]
	if support.ID != "support" || support.Name != "Support Bot" {
		t.Errorf("support = %+v", support)
	}
	if support.Model == nil || support.Model.Primary != "claude-opus" {
		t.Errorf("support.Model = %+v", support.Model)
	}
	if len(support.Model.Fallbacks) != 1 || support.Model.Fallbacks[0] != "haiku" {
		t.Errorf("support.Model.Fallbacks = %v", support.Model.Fallbacks)
	}
	if support.Subagents == nil || len(support.Subagents.AllowAgents) != 1 {
		t.Errorf("support.Subagents = %+v", support.Subagents)
	}

	if len(cfg.Session.Dimensions) != 1 || cfg.Session.Dimensions[0] != "sender" {
		t.Errorf("Session.Dimensions = %v", cfg.Session.Dimensions)
	}
	if len(cfg.Session.IdentityLinks) != 1 {
		t.Errorf("Session.IdentityLinks = %v", cfg.Session.IdentityLinks)
	}
	links := cfg.Session.IdentityLinks["john"]
	if len(links) != 2 {
		t.Errorf("john links = %v", links)
	}
}

func TestTurnProfileConfig_ParseAndResolve(t *testing.T) {
	jsonData := `{
		"agents": {
			"defaults": {
				"turn_profile": {
					"enabled": true,
					"history": {"mode": "off"},
					"system_prompt": {"mode": "off"},
					"skills": {"mode": "off"},
					"tools": {
						"mode": "custom",
						"allow": ["web_search", "web_fetch"]
					}
				}
			}
		}
	}`

	cfg := DefaultConfig()
	if err := json.Unmarshal([]byte(jsonData), cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := cfg.ValidateTurnProfile(); err != nil {
		t.Fatalf("ValidateTurnProfile() error = %v", err)
	}

	profile, ok, err := cfg.Agents.Defaults.ResolveTurnProfile()
	if err != nil {
		t.Fatalf("ResolveTurnProfile() error = %v", err)
	}
	if !ok {
		t.Fatal("ResolveTurnProfile() ok = false, want true")
	}
	if profile.HistoryMode != TurnProfileModeOff ||
		profile.SystemPromptMode != TurnProfileModeOff ||
		profile.SkillsMode != TurnProfileModeOff ||
		profile.ToolsMode != TurnProfileModeCustom {
		t.Fatalf("resolved clean_web modes = %+v", profile)
	}
	assert.Equal(t, []string{"web_search", "web_fetch"}, profile.AllowedTools)
}

func TestTurnProfileConfig_DisabledOrMissingIsNoop(t *testing.T) {
	cfg := DefaultConfig()

	profile, ok, err := cfg.Agents.Defaults.ResolveTurnProfile()
	if err != nil {
		t.Fatalf("ResolveTurnProfile(missing) error = %v", err)
	}
	if ok {
		t.Fatal("ResolveTurnProfile(missing) ok = true, want false")
	}
	if profile.Enabled {
		t.Fatalf("ResolveTurnProfile(missing) profile.Enabled = true, want false")
	}

	cfg.Agents.Defaults.TurnProfile = TurnProfileConfig{
		Enabled: false,
		History: TurnProfileBlock{
			Mode: TurnProfileModeOff,
		},
	}
	profile, ok, err = cfg.Agents.Defaults.ResolveTurnProfile()
	if err != nil {
		t.Fatalf("ResolveTurnProfile(disabled) error = %v", err)
	}
	if ok || profile.Enabled {
		t.Fatalf("disabled profile = (%+v, %v), want no-op", profile, ok)
	}

	cfg.Agents.Defaults.TurnProfile = TurnProfileConfig{
		Enabled: false,
		History: TurnProfileBlock{
			Mode: TurnProfileModeCustom,
		},
		Tools: TurnProfileBlock{
			Mode: TurnProfileMode("sometimes"),
		},
	}
	if err := cfg.ValidateTurnProfile(); err != nil {
		t.Fatalf("ValidateTurnProfile(disabled unsupported modes) error = %v, want nil", err)
	}
}

func TestTurnProfileConfig_ValidationRejectsUnsupportedModes(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "history custom unsupported",
			raw:  `{"agents":{"defaults":{"turn_profile":{"enabled":true,"history":{"mode":"custom"}}}}}`,
			want: "history.mode",
		},
		{
			name: "system prompt custom unsupported",
			raw:  `{"agents":{"defaults":{"turn_profile":{"enabled":true,"system_prompt":{"mode":"custom"}}}}}`,
			want: "system_prompt.mode",
		},
		{
			name: "unknown mode",
			raw:  `{"agents":{"defaults":{"turn_profile":{"enabled":true,"tools":{"mode":"sometimes"}}}}}`,
			want: "unsupported mode",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			if err := json.Unmarshal([]byte(tt.raw), cfg); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			err := cfg.ValidateTurnProfile()
			if err == nil {
				t.Fatal("ValidateTurnProfile() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateTurnProfile() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestDefaultConfig_MCPMaxInlineTextChars(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Tools.MCP.GetMaxInlineTextChars() != DefaultMCPMaxInlineTextChars {
		t.Fatalf(
			"DefaultConfig().Tools.MCP.GetMaxInlineTextChars() = %d, want %d",
			cfg.Tools.MCP.GetMaxInlineTextChars(),
			DefaultMCPMaxInlineTextChars,
		)
	}
}

func TestDefaultConfig_MediaRetentionSupportsHistoricalReferences(t *testing.T) {
	cfg := DefaultConfig()
	if got := cfg.Tools.MediaCleanup.MaxAge; got != 7*24*60 {
		t.Fatalf("DefaultConfig().Tools.MediaCleanup.MaxAge = %d, want seven days", got)
	}
}

func TestLoadConfig_MCPMaxInlineTextChars(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	raw := `{
		"version": 4,
		"tools": {
			"mcp": {
				"enabled": true,
				"max_inline_text_chars": 2048
			}
		}
	}`
	if err := os.WriteFile(configPath, []byte(raw), 0o644); err != nil {
		t.Fatalf("WriteFile(configPath): %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if got := cfg.Tools.MCP.GetMaxInlineTextChars(); got != 2048 {
		t.Fatalf("cfg.Tools.MCP.GetMaxInlineTextChars() = %d, want 2048", got)
	}
}

func TestLoadConfigResultRetention(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	raw := `{
		"version": 4,
		"tools": {
			"result_retention": {
				"log_meal": {
					"mode": "durable",
					"receipt": "Meal saved."
				}
			}
		}
	}`
	if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
		t.Fatalf("WriteFile(configPath): %v", err)
	}

	cfg, err := LoadConfigReadOnly(configPath)
	if err != nil {
		t.Fatalf("LoadConfigReadOnly() error: %v", err)
	}
	rule := cfg.Tools.ResultRetention["log_meal"]
	if rule.Mode != "durable" || rule.Receipt != "Meal saved." {
		t.Fatalf("retention rule = %#v", rule)
	}
}

func TestLoadConfigRejectsInvalidResultRetention(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	raw := `{
		"version": 4,
		"tools": {
			"result_retention": {
				"log_meal": {"mode": "durable"}
			}
		}
	}`
	if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
		t.Fatalf("WriteFile(configPath): %v", err)
	}

	_, err := LoadConfigReadOnly(configPath)
	if err == nil || !strings.Contains(err.Error(), "invalid tools.result_retention") {
		t.Fatalf("LoadConfigReadOnly() error = %v", err)
	}
}

func TestLoadConfigRejectsContextManagerOwnedResultRetention(t *testing.T) {
	for _, legacyKey := range []string{"toolResultRetention", "ToolResultRetention"} {
		t.Run(legacyKey, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "config.json")
			raw := fmt.Sprintf(`{
				"version": 4,
				"agents": {
					"defaults": {
						"context_manager": "seahorse",
						"context_manager_config": {
							%q: {
								"log_meal": {"mode": "transient"}
							}
						}
					}
				}
			}`, legacyKey)
			if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
				t.Fatalf("WriteFile(configPath): %v", err)
			}

			_, err := LoadConfigReadOnly(configPath)
			if err == nil || !strings.Contains(err.Error(), "use tools.result_retention") {
				t.Fatalf("LoadConfigReadOnly() error = %v", err)
			}
		})
	}
}

func TestLoadConfigRejectsMalformedContextManagerConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	raw := `{
		"version": 4,
		"agents": {
			"defaults": {
				"context_manager": "seahorse",
				"context_manager_config": "not-an-object"
			}
		}
	}`
	if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
		t.Fatalf("WriteFile(configPath): %v", err)
	}

	_, err := LoadConfigReadOnly(configPath)
	if err == nil || !strings.Contains(err.Error(), "invalid agents.defaults.context_manager_config") {
		t.Fatalf("LoadConfigReadOnly() error = %v", err)
	}
}

func TestDefaultConfigUsesSeahorseContextManager(t *testing.T) {
	if got := DefaultConfig().Agents.Defaults.ContextManager; got != "seahorse" {
		t.Fatalf("default context manager = %q, want seahorse", got)
	}
}

func TestLoadConfigRejectsLegacyContextManager(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	raw := `{
		"version": 4,
		"agents": {"defaults": {"context_manager": "legacy"}}
	}`
	if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
		t.Fatalf("WriteFile(configPath): %v", err)
	}

	_, err := LoadConfigReadOnly(configPath)
	if err == nil || !strings.Contains(err.Error(), `use "seahorse" or "none"`) {
		t.Fatalf("LoadConfigReadOnly() error = %v", err)
	}
}

func TestLoadConfigAcceptsDisabledContextManager(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	raw := `{
		"version": 4,
		"agents": {"defaults": {"context_manager": "none"}}
	}`
	if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
		t.Fatalf("WriteFile(configPath): %v", err)
	}

	cfg, err := LoadConfigReadOnly(configPath)
	if err != nil {
		t.Fatalf("LoadConfigReadOnly() error = %v", err)
	}
	if got := cfg.Agents.Defaults.ContextManager; got != "none" {
		t.Fatalf("context manager = %q, want none", got)
	}
}

func TestToolApprovalConfigDefaultsToRequired(t *testing.T) {
	cfg := DefaultConfig()
	if got := cfg.Tools.Approval.EffectiveMode(); got != ToolApprovalModeRequired {
		t.Fatalf("approval mode = %q, want %q", got, ToolApprovalModeRequired)
	}
	if cfg.Tools.Approval.AllowAll() {
		t.Fatal("default approval mode unexpectedly allows all")
	}
}

func TestValidateToolApprovalConfig(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Execution.Targets = map[string]ExecutionTarget{
		"vpn": {Type: "node", Node: "vpn-node"},
	}
	cfg.Tools.Approval.Mode = " ALLOW_ALL "
	if err := cfg.ValidateToolApprovalConfig(); err != nil {
		t.Fatalf("ValidateToolApprovalConfig() error = %v", err)
	}
	if cfg.Tools.Approval.Mode != ToolApprovalModeAllowAll || !cfg.Tools.Approval.AllowAll() {
		t.Fatalf("normalized approval config = %#v", cfg.Tools.Approval)
	}

	cfg.Tools.Approval.Mode = "sometimes"
	err := cfg.ValidateToolApprovalConfig()
	if err == nil || !strings.Contains(err.Error(), "tools.approval.mode") {
		t.Fatalf("ValidateToolApprovalConfig() error = %v", err)
	}

	cfg.Tools.Approval.Mode = ToolApprovalModeRequired
	cfg.Tools.Approval.BypassNodeTargets = []string{"vpn"}
	if err = cfg.ValidateToolApprovalConfig(); err != nil {
		t.Fatalf("ValidateToolApprovalConfig() scoped bypass error = %v", err)
	}
	if !cfg.Tools.Approval.BypassesNodeTarget("vpn") || cfg.Tools.Approval.BypassesNodeTarget("other") {
		t.Fatalf("scoped approval config = %#v", cfg.Tools.Approval)
	}

	cfg.Tools.Approval.BypassNodeTargets = []string{"missing"}
	err = cfg.ValidateToolApprovalConfig()
	if err == nil || !strings.Contains(err.Error(), `references unknown target "missing"`) {
		t.Fatalf("ValidateToolApprovalConfig() unknown target error = %v", err)
	}

	cfg.Tools.Approval.BypassNodeTargets = []string{"vpn", "vpn"}
	err = cfg.ValidateToolApprovalConfig()
	if err == nil || !strings.Contains(err.Error(), `contains duplicate target "vpn"`) {
		t.Fatalf("ValidateToolApprovalConfig() duplicate target error = %v", err)
	}

	cfg.Tools.Approval.Mode = ToolApprovalModeAllowAll
	cfg.Tools.Approval.BypassNodeTargets = []string{"vpn"}
	err = cfg.ValidateToolApprovalConfig()
	if err == nil || !strings.Contains(err.Error(), "cannot be set when mode is allow_all") {
		t.Fatalf("ValidateToolApprovalConfig() redundant bypass error = %v", err)
	}
}

func TestImageGenerateToolsConfig_EffectiveModel(t *testing.T) {
	if got := (ImageGenerateToolsConfig{}).EffectiveModel(); got != "gpt-image-2" {
		t.Fatalf("default model = %q, want gpt-image-2", got)
	}

	cfg := ImageGenerateToolsConfig{Model: "openai-codex/gpt-image-2"}
	if got := cfg.EffectiveModel(); got != "openai-codex/gpt-image-2" {
		t.Fatalf("tool model = %q, want openai-codex/gpt-image-2", got)
	}
}

func TestDecodeCurrentConfigRejectsRemovedAgentImageModelFields(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		field string
		value string
	}{
		{field: "image_model", value: `"removed"`},
		{field: "image_model_fallbacks", value: `["removed"]`},
	} {
		t.Run(test.field, func(t *testing.T) {
			t.Parallel()

			raw := fmt.Sprintf(
				`{"version":%d,"agents":{"defaults":{%q:%s}}}`,
				CurrentVersion,
				test.field,
				test.value,
			)
			cfg := DefaultConfig()
			err := DecodeCurrentConfig([]byte(raw), cfg)
			if err == nil || !strings.Contains(err.Error(), test.field) {
				t.Fatalf("DecodeCurrentConfig() error = %v, want removed field %q rejected", err, test.field)
			}
		})
	}
}

func TestDecodeCurrentConfigConsumesLegacyModelConnectMode(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		encoded string
	}{
		{name: "empty", encoded: `""`},
		{name: "grpc", encoded: `"grpc"`},
		{name: "null", encoded: `null`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			raw := fmt.Sprintf(`{
				"version":%d,
				"model_list":[{
					"model_name":"copilot","provider":"github-copilot","model":"gpt-5",
					"connect_mode":%s,"enabled":false
				}]
			}`, CurrentVersion, test.encoded)
			var cfg Config
			if err := DecodeCurrentConfig([]byte(raw), &cfg); err != nil {
				t.Fatalf("DecodeCurrentConfig() error = %v", err)
			}
			encoded, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "connect_mode") {
				t.Fatalf("legacy connect_mode survived current config projection: %s", encoded)
			}
		})
	}
}

func TestDecodeCurrentConfigRejectsUnsupportedLegacyModelConnectMode(t *testing.T) {
	t.Parallel()

	raw := fmt.Sprintf(`{
		"version":%d,
		"model_list":[{
			"model_name":"copilot","provider":"github-copilot","model":"gpt-5",
			"connect_mode":"stdio","enabled":false
		}]
	}`, CurrentVersion)
	var cfg Config
	err := DecodeCurrentConfig([]byte(raw), &cfg)
	if err == nil || !strings.Contains(err.Error(), "model_list[0].connect_mode") ||
		!strings.Contains(err.Error(), "no longer supported") {
		t.Fatalf("DecodeCurrentConfig() error = %v, want removed connect-mode rejection", err)
	}
}

func TestLoadConfig_ImageGenerateModel(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	raw := `{
		"version": 4,
		"tools": {
			"image_generate": {
				"enabled": true,
				"model": "openai-codex/gpt-image-2",
				"output_dir": "tmp/generated-images"
			}
		}
	}`
	if err := os.WriteFile(configPath, []byte(raw), 0o644); err != nil {
		t.Fatalf("WriteFile(configPath): %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if !cfg.Tools.ImageGenerate.Enabled {
		t.Fatal("cfg.Tools.ImageGenerate.Enabled should be true")
	}
	if got := cfg.Tools.ImageGenerate.Model; got != "openai-codex/gpt-image-2" {
		t.Fatalf("cfg.Tools.ImageGenerate.Model = %q, want openai-codex/gpt-image-2", got)
	}
	if got := cfg.Tools.ImageGenerate.OutputDir; got != "tmp/generated-images" {
		t.Fatalf("cfg.Tools.ImageGenerate.OutputDir = %q, want tmp/generated-images", got)
	}
}

func TestConfig_DefaultAgentWhenListIsOmitted(t *testing.T) {
	jsonData := `{
		"agents": {
			"defaults": {
				"workspace": "~/.mintclaw/workspace",
				"model": "glm-4.7",
				"max_tokens": 8192,
				"max_tool_iterations": 20
			}
		}
	}`

	cfg := DefaultConfig()
	if err := json.Unmarshal([]byte(jsonData), cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(cfg.Agents.List) != 1 {
		t.Fatalf("agents.list len = %d, want the current default agent", len(cfg.Agents.List))
	}
	agent := cfg.Agents.List[0]
	if agent.ID != "main" || !agent.Default || agent.Name != "mintclaw" || agent.Description == "" {
		t.Fatalf("default agent = %#v", agent)
	}
}

func TestAgentCapabilityPolicyValidate(t *testing.T) {
	tests := []struct {
		name    string
		policy  AgentCapabilityPolicy
		wantErr bool
	}{
		{
			name: "default allow with deny list",
			policy: AgentCapabilityPolicy{
				Default: AgentCapabilityDefaultAllow,
				Deny:    []string{"exec", "mcp_legacy_*"},
			},
		},
		{
			name: "default deny with allow and deny lists",
			policy: AgentCapabilityPolicy{
				Default: AgentCapabilityDefaultDeny,
				Allow:   []string{"read_*", "mcp_*"},
				Deny:    []string{"mcp_legacy_*"},
			},
		},
		{
			name:    "missing default",
			policy:  AgentCapabilityPolicy{},
			wantErr: true,
		},
		{
			name: "redundant allow list",
			policy: AgentCapabilityPolicy{
				Default: AgentCapabilityDefaultAllow,
				Allow:   []string{"read_file"},
			},
			wantErr: true,
		},
		{
			name: "invalid glob",
			policy: AgentCapabilityPolicy{
				Default: AgentCapabilityDefaultDeny,
				Allow:   []string{"["},
			},
			wantErr: true,
		},
		{
			name: "duplicate pattern",
			policy: AgentCapabilityPolicy{
				Default: AgentCapabilityDefaultAllow,
				Deny:    []string{"exec", "exec"},
			},
			wantErr: true,
		},
		{
			name: "mixed case pattern",
			policy: AgentCapabilityPolicy{
				Default: AgentCapabilityDefaultDeny,
				Allow:   []string{"GitHub"},
			},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.policy.Validate("agents.list[0].tool_policy")
			if (err != nil) != test.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
}

func TestDecodeCurrentConfigExplicitAgentsDoNotInheritDefaultAgentFields(t *testing.T) {
	raw := fmt.Sprintf(`{
		"version": %d,
		"agents": {
			"list": [
				{"id": "support"},
				{"id": "main", "default": true}
			]
		}
	}`, CurrentVersion)

	var cfg Config
	if err := DecodeCurrentConfig([]byte(raw), &cfg); err != nil {
		t.Fatalf("DecodeCurrentConfig() error = %v", err)
	}
	if len(cfg.Agents.List) != 2 {
		t.Fatalf("agents.list len = %d, want 2", len(cfg.Agents.List))
	}
	support := cfg.Agents.List[0]
	if support.ID != "support" || support.Default || support.Name != "" || support.Description != "" {
		t.Fatalf("support agent inherited default fields: %#v", support)
	}
	main := cfg.Agents.List[1]
	if main.ID != "main" || !main.Default {
		t.Fatalf("main agent = %#v", main)
	}
}

func TestDecodeCurrentConfigRejectsInvalidAgentPolicy(t *testing.T) {
	raw := fmt.Sprintf(`{
		"version": %d,
		"agents": {
			"list": [{
				"id": "main",
				"default": true,
				"tool_policy": {"default": "deny", "allow": ["["]}
			}]
		}
	}`, CurrentVersion)

	var cfg Config
	err := DecodeCurrentConfig([]byte(raw), &cfg)
	if err == nil || !strings.Contains(err.Error(), "agents.list[0].tool_policy.allow[0] is invalid") {
		t.Fatalf("DecodeCurrentConfig() error = %v, want invalid agent policy rejection", err)
	}
}

func TestLoadConfigRejectsExplicitEmptyAgentsList(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	raw := fmt.Sprintf(`{"version":%d,"agents":{"list":[]}}`, CurrentVersion)
	if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := LoadConfig(configPath)
	if err == nil || !strings.Contains(err.Error(), "agents.list must contain at least one agent") {
		t.Fatalf("LoadConfig() error = %v, want empty agents.list rejection", err)
	}
}

func TestAgentConfig_ParsesDispatchRules(t *testing.T) {
	jsonData := `{
		"agents": {
			"defaults": {
				"workspace": "~/.mintclaw/workspace",
				"model": "glm-4.7"
			},
			"list": [
				{ "id": "main", "default": true },
				{ "id": "support" }
			],
			"dispatch": {
				"rules": [
					{
						"name": "support-vip",
						"agent": "support",
						"when": {
							"channel": "telegram",
							"chat": "group:-100123",
							"sender": "12345",
							"mentioned": true
						},
						"session_dimensions": ["chat", "sender"]
					}
				]
			}
		}
	}`

	cfg := DefaultConfig()
	if err := json.Unmarshal([]byte(jsonData), cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Agents.Dispatch == nil {
		t.Fatal("Agents.Dispatch should not be nil")
	}
	if len(cfg.Agents.Dispatch.Rules) != 1 {
		t.Fatalf("Dispatch.Rules len = %d, want 1", len(cfg.Agents.Dispatch.Rules))
	}
	rule := cfg.Agents.Dispatch.Rules[0]
	if rule.Name != "support-vip" || rule.Agent != "support" {
		t.Fatalf("rule = %+v", rule)
	}
	if rule.When.Channel != "telegram" || rule.When.Chat != "group:-100123" || rule.When.Sender != "12345" {
		t.Fatalf("rule.When = %+v", rule.When)
	}
	if rule.When.Mentioned == nil || !*rule.When.Mentioned {
		t.Fatalf("rule.When.Mentioned = %+v, want true", rule.When.Mentioned)
	}
	if got := rule.SessionDimensions; len(got) != 2 || got[0] != "chat" || got[1] != "sender" {
		t.Fatalf("rule.SessionDimensions = %v, want [chat sender]", got)
	}
}

func TestLoadConfig_RejectsLegacyBindings(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	raw := `{
		"version": 4,
		"bindings": [
			{
				"agent_id": "support",
				"match": {
					"channel": "telegram",
					"peer": { "kind": "direct", "id": "123" }
				}
			}
		]
	}`
	if err := os.WriteFile(configPath, []byte(raw), 0o644); err != nil {
		t.Fatalf("WriteFile(configPath): %v", err)
	}

	_, err := LoadConfig(configPath)
	if err == nil || !strings.Contains(err.Error(), "unknown field(s): bindings") {
		t.Fatalf("LoadConfig() error = %v, want legacy bindings rejection", err)
	}
}

// TestDefaultConfig_HeartbeatEnabled verifies heartbeat is enabled by default
func TestDefaultConfig_HeartbeatEnabled(t *testing.T) {
	cfg := DefaultConfig()

	if !cfg.Heartbeat.Enabled {
		t.Error("Heartbeat should be enabled by default")
	}
}

// TestDefaultConfig_WorkspacePath verifies workspace path is correctly set
func TestDefaultConfig_WorkspacePath(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Agents.Defaults.Workspace == "" {
		t.Error("Workspace should not be empty")
	}
}

// TestDefaultConfig_AnthropicModelsUseClaudeAPIIDs verifies that first-party
// Anthropic defaults use Claude API model IDs, not dotted display names or
// Bedrock-style provider prefixes. See:
// https://platform.claude.com/docs/en/about-claude/models/model-ids-and-versions
func TestDefaultConfig_AnthropicModelsUseClaudeAPIIDs(t *testing.T) {
	cfg := DefaultConfig()

	checked := 0
	for _, model := range cfg.ModelList {
		if model.Provider != "anthropic" {
			continue
		}
		checked++
		if strings.Contains(model.Model, ".") {
			t.Fatalf("Anthropic default model %q uses dotted ID %q", model.ModelName, model.Model)
		}
	}

	if checked == 0 {
		t.Fatal("DefaultConfig() missing Anthropic models")
	}
}

// TestDefaultConfig_MaxTokens verifies max tokens has default value
func TestDefaultConfig_MaxTokens(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Agents.Defaults.MaxTokens == 0 {
		t.Error("MaxTokens should not be zero")
	}
}

// TestDefaultConfig_MaxToolIterations verifies max tool iterations has default value
func TestDefaultConfig_MaxToolIterations(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Agents.Defaults.MaxToolIterations == 0 {
		t.Error("MaxToolIterations should not be zero")
	}
}

func TestDefaultConfig_ToolLoopDetection(t *testing.T) {
	cfg := DefaultConfig().Tools.LoopDetection
	if !cfg.Enabled || !cfg.WarningsEnabled || cfg.HardStopsEnabled {
		t.Fatalf("unexpected loop detection switches: %#v", cfg)
	}
	if cfg.ExactFailureWarn != 2 || cfg.ExactFailureBlock != 5 ||
		cfg.SameToolFailureWarn != 3 || cfg.SameToolFailureHalt != 8 ||
		cfg.NoProgressWarn != 2 || cfg.NoProgressBlock != 5 ||
		cfg.IdenticalCallWarn != 2 || cfg.IdenticalCallHalt != 4 || cfg.MaxSignatures <= 0 {
		t.Fatalf("unexpected loop detection thresholds: %#v", cfg)
	}
}

// TestDefaultConfig_Temperature verifies temperature has default value
func TestDefaultConfig_Temperature(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Agents.Defaults.Temperature != nil {
		t.Error("Temperature should be nil when not provided")
	}
}

// TestDefaultConfig_Gateway verifies gateway defaults
func TestDefaultConfig_Gateway(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Gateway.Host != "localhost" {
		t.Error("Gateway host should have default value")
	}
	if cfg.Gateway.Port == 0 {
		t.Error("Gateway port should have default value")
	}
	if cfg.Gateway.HotReload {
		t.Error("Gateway hot reload should be disabled by default")
	}
}

// TestDefaultConfig_Channels verifies channels are disabled by default
func TestDefaultConfig_Channels(t *testing.T) {
	cfg := DefaultConfig()

	for name, bc := range cfg.Channels {
		if bc.Enabled {
			t.Errorf("Channel %q should be disabled by default", name)
		}
	}
}

func TestDefaultConfig_ChannelStreamingDisabled(t *testing.T) {
	cfg := DefaultConfig()

	telegram := cfg.Channels.Get(ChannelTelegram)
	if telegram == nil {
		t.Fatal("DefaultConfig() missing telegram channel")
	}
	decoded, err := telegram.GetDecoded()
	if err != nil {
		t.Fatalf("telegram GetDecoded() error = %v", err)
	}
	settings, ok := decoded.(*TelegramSettings)
	if !ok {
		t.Fatalf("telegram settings type = %T, want *TelegramSettings", decoded)
	}
	if settings.Streaming.Enabled {
		t.Fatal("DefaultConfig().telegram.settings.streaming.enabled should be false")
	}

	mintclaw := cfg.Channels.Get(ChannelMintClaw)
	if mintclaw == nil {
		t.Fatal("DefaultConfig() missing mintclaw channel")
	}
	decoded, err = mintclaw.GetDecoded()
	if err != nil {
		t.Fatalf("mintclaw GetDecoded() error = %v", err)
	}
	mintclawSettings, ok := decoded.(*MintClawSettings)
	if !ok {
		t.Fatalf("mintclaw settings type = %T, want *MintClawSettings", decoded)
	}
	if !mintclawSettings.Streaming.Enabled {
		t.Fatal("DefaultConfig().mintclaw.settings.streaming.enabled should be true")
	}
}

func TestDefaultConfig_DeltaChatExample(t *testing.T) {
	cfg := DefaultConfig()

	deltachat := cfg.Channels.Get(ChannelDeltaChat)
	if deltachat == nil {
		t.Fatal("DefaultConfig() missing deltachat channel")
	}
	if deltachat.Enabled {
		t.Fatal("DefaultConfig().deltachat should be disabled")
	}
	if !deltachat.GroupTrigger.MentionOnly {
		t.Fatal("DefaultConfig().deltachat should use mention-only group trigger")
	}
	decoded, err := deltachat.GetDecoded()
	if err != nil {
		t.Fatalf("deltachat GetDecoded() error = %v", err)
	}
	settings, ok := decoded.(*DeltaChatSettings)
	if !ok {
		t.Fatalf("deltachat settings type = %T, want *DeltaChatSettings", decoded)
	}
	if settings.Email != "@nine.testrun.org" {
		t.Fatalf("DefaultConfig().deltachat.settings.email = %q, want @nine.testrun.org", settings.Email)
	}
	if settings.DisplayName == "" {
		t.Fatal("DefaultConfig().deltachat.settings.display_name should be populated")
	}
}

func TestLoadConfigRejectsRemovedDeltaChatMailboxSettings(t *testing.T) {
	for _, field := range []string{"password", "imap_server", "imap_port", "smtp_server", "smtp_port"} {
		t.Run(field, func(t *testing.T) {
			raw := fmt.Sprintf(
				`{"version":%d,"channel_list":{"deltachat":{"type":"deltachat",`+
					`"settings":{"email":"bot@example.org",%q:null}}}}`,
				CurrentVersion,
				field,
			)
			configPath := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
				t.Fatalf("WriteFile() error: %v", err)
			}

			_, err := LoadConfig(configPath)
			want := "unknown field(s): channel_list.deltachat.settings." + field
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("LoadConfig() error = %v, want %q", err, want)
			}
		})
	}
}

func TestLoadConfigRejectsRemovedDeltaChatPasswordFromSecurityOverlay(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	raw := fmt.Sprintf(
		`{"version":%d,"channel_list":{"deltachat":{"type":"deltachat",`+
			`"settings":{"email":"bot@example.org"}}}}`,
		CurrentVersion,
	)
	if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
		t.Fatalf("WriteFile(config.json) error: %v", err)
	}
	securityPath := filepath.Join(directory, SecurityConfigFile)
	security := "channel_list:\n  deltachat:\n    settings:\n      password: removed-secret\n"
	if err := os.WriteFile(securityPath, []byte(security), 0o600); err != nil {
		t.Fatalf("WriteFile(%s) error: %v", SecurityConfigFile, err)
	}

	_, err := LoadConfig(configPath)
	want := "unknown field(s): channel_list.deltachat.settings.password"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("LoadConfig() error = %v, want %q", err, want)
	}
}

func TestLoadConfigRejectsSecurityOverlayChannelTypeOverride(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	raw := fmt.Sprintf(
		`{"version":%d,"channel_list":{"deltachat":{"type":"deltachat",`+
			`"settings":{"email":"bot@example.org"}}}}`,
		CurrentVersion,
	)
	if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
		t.Fatalf("WriteFile(config.json) error: %v", err)
	}
	securityPath := filepath.Join(directory, SecurityConfigFile)
	security := "channel_list:\n  deltachat:\n    type: irc\n    settings:\n      password: removed-secret\n"
	if err := os.WriteFile(securityPath, []byte(security), 0o600); err != nil {
		t.Fatalf("WriteFile(%s) error: %v", SecurityConfigFile, err)
	}

	_, err := LoadConfig(configPath)
	want := "unknown field(s): channel_list.deltachat.type"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("LoadConfig() error = %v, want %q", err, want)
	}
}

func TestLoadConfigRejectsNonObjectSecurityOverlayChannels(t *testing.T) {
	tests := []struct {
		name     string
		channel  string
		value    string
		wantText string
	}{
		{
			name:    "unknown null channel",
			channel: "mqtt", value: "null",
			wantText: "unknown field(s): channel_list.mqtt",
		},
		{
			name:    "current null channel",
			channel: "deltachat", value: "null",
			wantText: "channel entries must be objects: channel_list.deltachat",
		},
		{
			name:    "current scalar channel",
			channel: "deltachat", value: "removed-secret",
			wantText: "channel entries must be objects: channel_list.deltachat",
		},
		{
			name:    "current list channel",
			channel: "deltachat", value: "[]",
			wantText: "channel entries must be objects: channel_list.deltachat",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			configPath := filepath.Join(directory, "config.json")
			raw := fmt.Sprintf(
				`{"version":%d,"channel_list":{"deltachat":{"settings":{"email":"bot@example.org"}}}}`,
				CurrentVersion,
			)
			if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
				t.Fatalf("WriteFile(config.json) error: %v", err)
			}
			securityPath := filepath.Join(directory, SecurityConfigFile)
			security := fmt.Sprintf("channel_list:\n  %s: %s\n", test.channel, test.value)
			if err := os.WriteFile(securityPath, []byte(security), 0o600); err != nil {
				t.Fatalf("WriteFile(%s) error: %v", SecurityConfigFile, err)
			}

			_, err := LoadConfig(configPath)
			if err == nil || !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("LoadConfig() error = %v, want %q", err, test.wantText)
			}
		})
	}
}

func TestLoadConfigRejectsNonObjectSecurityOverlayChannelList(t *testing.T) {
	for _, value := range []string{"null", "removed-secret", "[]"} {
		t.Run(value, func(t *testing.T) {
			directory := t.TempDir()
			configPath := filepath.Join(directory, "config.json")
			raw := fmt.Sprintf(
				`{"version":%d,"channel_list":{"telegram":{"settings":{}}}}`,
				CurrentVersion,
			)
			if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
				t.Fatalf("WriteFile(config.json) error: %v", err)
			}
			securityPath := filepath.Join(directory, SecurityConfigFile)
			security := fmt.Sprintf("channel_list: %s\n", value)
			if err := os.WriteFile(securityPath, []byte(security), 0o600); err != nil {
				t.Fatalf("WriteFile(%s) error: %v", SecurityConfigFile, err)
			}

			_, err := LoadConfig(configPath)
			want := "channel_list must be an object"
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("LoadConfig() error = %v, want %q", err, want)
			}
		})
	}
}

func TestValidateSingletonChannels_RejectsMultipleInstances(t *testing.T) {
	channels := ChannelsConfig{
		"mintclaw1": &Channel{Enabled: true, Type: ChannelMintClaw},
		"mintclaw2": &Channel{Enabled: true, Type: ChannelMintClaw},
	}
	err := validateSingletonChannels(channels)
	if err == nil {
		t.Fatal("expected error for multiple mintclaw channels, got nil")
	}
	if !strings.Contains(err.Error(), "singleton") {
		t.Fatalf("expected singleton error, got: %v", err)
	}
}

func TestValidateSingletonChannels_AllowsSingleInstance(t *testing.T) {
	channels := ChannelsConfig{
		"mintclaw1": &Channel{Enabled: true, Type: ChannelMintClaw},
	}
	err := validateSingletonChannels(channels)
	if err != nil {
		t.Fatalf("expected no error for single mintclaw channel, got: %v", err)
	}
}

func TestValidateSingletonChannels_IgnoresDisabledInstances(t *testing.T) {
	channels := ChannelsConfig{
		"mintclaw1": &Channel{Enabled: true, Type: ChannelMintClaw},
		"mintclaw2": &Channel{Enabled: false, Type: ChannelMintClaw},
	}
	err := validateSingletonChannels(channels)
	if err != nil {
		t.Fatalf("expected no error when only one mintclaw channel is enabled, got: %v", err)
	}
}

func TestValidateSingletonChannels_AllowsMultiInstanceTypes(t *testing.T) {
	channels := ChannelsConfig{
		"tg1": &Channel{Enabled: true, Type: ChannelTelegram},
		"tg2": &Channel{Enabled: true, Type: ChannelTelegram},
	}
	err := validateSingletonChannels(channels)
	if err != nil {
		t.Fatalf("telegram should allow multiple instances, got error: %v", err)
	}
}

// TestDefaultConfig_WebTools verifies web tools config
func TestDefaultConfig_WebTools(t *testing.T) {
	cfg := DefaultConfig()

	// Verify web tools defaults
	if cfg.Tools.Web.Brave.MaxResults != 5 {
		t.Error("Expected Brave MaxResults 5, got ", cfg.Tools.Web.Brave.MaxResults)
	}
	if len(cfg.Tools.Web.Brave.APIKeys) != 0 {
		t.Error("Brave API key should be empty by default")
	}
	if cfg.Tools.Web.DuckDuckGo.MaxResults != 5 {
		t.Error("Expected DuckDuckGo MaxResults 5, got ", cfg.Tools.Web.DuckDuckGo.MaxResults)
	}
}

func TestRepositorySave_FilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permission bits are not enforced on Windows")
	}

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.json")

	cfg := DefaultConfig()
	if err := saveTestConfig(path, cfg); err != nil {
		t.Fatalf("Repository.Save failed: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}

	perm := info.Mode().Perm()
	if perm != 0o600 {
		t.Errorf("config file has permission %04o, want 0600", perm)
	}
}

func TestRepositorySave_IncludesEmptyLegacyModelField(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.json")

	cfg := DefaultConfig()
	if err := saveTestConfig(path, cfg); err != nil {
		t.Fatalf("Repository.Save failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	if !strings.Contains(string(data), `"model_name": ""`) {
		t.Fatalf("saved config should include empty legacy model_name field, got: %s", string(data))
	}
}

func TestRepositorySave_PreservesDisabledTelegramPlaceholder(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.json")

	cfg := DefaultConfig()
	if bc := cfg.Channels.Get("telegram"); bc != nil {
		bc.Placeholder.Enabled = false
	}

	if err := saveTestConfig(path, cfg); err != nil {
		t.Fatalf("Repository.Save failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if !strings.Contains(string(data), `"placeholder": {`) {
		t.Fatalf("saved config should include telegram placeholder config, got: %s", string(data))
	}
	if !strings.Contains(string(data), `"enabled": false`) {
		t.Fatalf("saved config should persist placeholder.enabled=false, got: %s", string(data))
	}

	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	bc := loaded.Channels.Get("telegram")
	if bc != nil && bc.Placeholder.Enabled {
		t.Fatal("telegram placeholder should remain disabled after Repository.Save/LoadConfig round-trip")
	}
}

func TestRepositorySave_PreservesExplicitDisabledMintClawStreaming(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.json")

	cfg := DefaultConfig()
	mintclaw := cfg.Channels.Get(ChannelMintClaw)
	if mintclaw == nil {
		t.Fatal("DefaultConfig() missing mintclaw channel")
	}
	mintclaw.Settings = RawNode(`{"streaming":{"enabled":false}}`)

	if err := saveTestConfig(path, cfg); err != nil {
		t.Fatalf("Repository.Save failed: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if !strings.Contains(string(data), `"streaming"`) || !strings.Contains(string(data), `"enabled": false`) {
		t.Fatalf("saved config should preserve explicit disabled mintclaw streaming, got:\n%s", string(data))
	}

	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	loadedMintClaw := loaded.Channels.Get(ChannelMintClaw)
	if loadedMintClaw == nil {
		t.Fatal("loaded config missing mintclaw channel")
	}
	decoded, err := loadedMintClaw.GetDecoded()
	if err != nil {
		t.Fatalf("mintclaw GetDecoded() error = %v", err)
	}
	settings, ok := decoded.(*MintClawSettings)
	if !ok {
		t.Fatalf("mintclaw settings type = %T, want *MintClawSettings", decoded)
	}
	if settings.Streaming.Enabled {
		t.Fatal(
			"explicit disabled mintclaw streaming should remain disabled after Repository.Save/LoadConfig round-trip",
		)
	}
}

// TestRepositorySave_FiltersVirtualModels verifies that Repository.Save does not write
// virtual models (generated by expandMultiKeyModels) to the config file.
func TestRepositorySave_FiltersVirtualModels(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.json")

	cfg := DefaultConfig()

	// Manually add a virtual model to ModelList (simulating what expandMultiKeyModels does)
	primaryModel := &ModelConfig{
		ModelName: "gpt-4", Provider: "openai", Model: "gpt-4o",
		APIKeys: SimpleSecureStrings("key1"),
	}
	virtualModel := &ModelConfig{
		ModelName: "gpt-4__key_1", Provider: "openai", Model: "gpt-4o",
		APIKeys:   SimpleSecureStrings("key2"),
		isVirtual: true,
	}
	cfg.ModelList = []*ModelConfig{primaryModel, virtualModel}

	// Repository.Save should filter out virtual models
	if err := saveTestConfig(path, cfg); err != nil {
		t.Fatalf("Repository.Save failed: %v", err)
	}

	// Reload and verify
	reloaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	// Should only have the primary model, not the virtual one
	if len(reloaded.ModelList) != 1 {
		t.Fatalf("expected 1 model after reload, got %d", len(reloaded.ModelList))
	}

	if reloaded.ModelList[0].ModelName != "gpt-4" {
		t.Errorf("expected model_name 'gpt-4', got %q", reloaded.ModelList[0].ModelName)
	}

	// Verify virtual model was not persisted
	for _, m := range reloaded.ModelList {
		if m.ModelName == "gpt-4__key_1" {
			t.Errorf("virtual model gpt-4__key_1 should not have been saved")
		}
	}

	// Verify the saved file does not contain the virtual model name
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if strings.Contains(string(data), "gpt-4__key_1") {
		t.Errorf("saved config should not contain virtual model name 'gpt-4__key_1'")
	}
}

// TestConfig_Complete verifies all config fields are set
func TestConfig_Complete(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Agents.Defaults.Workspace == "" {
		t.Error("Workspace should not be empty")
	}
	if cfg.Agents.Defaults.Temperature != nil {
		t.Error("Temperature should be nil when not provided")
	}
	if cfg.Agents.Defaults.MaxTokens == 0 {
		t.Error("MaxTokens should not be zero")
	}
	if cfg.Agents.Defaults.MaxToolIterations == 0 {
		t.Error("MaxToolIterations should not be zero")
	}
	if cfg.Gateway.Host != "localhost" {
		t.Error("Gateway host should have default value")
	}
	if cfg.Gateway.Port == 0 {
		t.Error("Gateway port should have default value")
	}
	if !cfg.Heartbeat.Enabled {
		t.Error("Heartbeat should be enabled by default")
	}
	if !cfg.Tools.Exec.AllowRemote {
		t.Error("Exec.AllowRemote should be true by default")
	}
	if cfg.Tools.Exec.PermissionMode != "" {
		t.Errorf("Exec.PermissionMode = %q, want empty default", cfg.Tools.Exec.PermissionMode)
	}
}

func TestDefaultConfig_WebPreferNativeEnabled(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Tools.Web.PreferNative {
		t.Fatal("DefaultConfig().Tools.Web.PreferNative should be true")
	}
}

func TestDefaultConfig_WebProviderIsAuto(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Tools.Web.Provider != "auto" {
		t.Fatalf("DefaultConfig().Tools.Web.Provider = %q, want auto", cfg.Tools.Web.Provider)
	}
}

func TestConfigExample_WebProviderIsAuto(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "config", "config.example.json"))
	if err != nil {
		t.Fatalf("ReadFile(config.example.json) error: %v", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("Unmarshal(config.example.json) error: %v", err)
	}
	if cfg.Tools.Web.Provider != "auto" {
		t.Fatalf("config.example.json tools.web.provider = %q, want auto", cfg.Tools.Web.Provider)
	}
	if err := cfg.ValidateMCPConfig(); err != nil {
		t.Fatalf("config.example.json MCP contract error: %v", err)
	}
	if len(cfg.Agents.List) != 2 {
		t.Fatalf("config.example.json agents.list len = %d, want 2", len(cfg.Agents.List))
	}
	for index := range cfg.Agents.List {
		agent := &cfg.Agents.List[index]
		if agent.Name == "" || agent.Description == "" {
			t.Fatalf("config.example.json agents.list[%d] identity = %#v", index, agent)
		}
		if err := agent.ToolPolicy.Validate(fmt.Sprintf("agents.list[%d].tool_policy", index)); err != nil {
			t.Fatalf("config.example.json agent policy error: %v", err)
		}
	}
}

func TestDefaultConfig_ToolFeedbackDisabled(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Agents.Defaults.ToolFeedback.Enabled {
		t.Fatal("DefaultConfig().Agents.Defaults.ToolFeedback.Enabled should be false")
	}
	if !cfg.Agents.Defaults.IsSubagentToolFeedbackEnabled() {
		t.Fatal("DefaultConfig().Agents.Defaults.IsSubagentToolFeedbackEnabled() should default to true")
	}
	if !cfg.Agents.Defaults.ToolFeedback.Subagents {
		t.Fatal("DefaultConfig().Agents.Defaults.ToolFeedback.Subagents should be true")
	}
	if cfg.Agents.Defaults.ToolFeedback.SeparateMessages {
		t.Fatal("DefaultConfig().Agents.Defaults.ToolFeedback.SeparateMessages should be false")
	}
	if got := cfg.Agents.Defaults.GetToolFeedbackStyle(); got != "" {
		t.Fatalf("DefaultConfig().Agents.Defaults.GetToolFeedbackStyle() = %q, want empty/raw default", got)
	}
	if got := cfg.Agents.Defaults.GetToolFeedbackAnimationInterval(); got != 3*time.Second {
		t.Fatalf("DefaultConfig().Agents.Defaults.GetToolFeedbackAnimationInterval() = %v, want 3s", got)
	}
	if got := cfg.Agents.Defaults.GetToolFeedbackEditMinInterval(); got != 0 {
		t.Fatalf("DefaultConfig().Agents.Defaults.GetToolFeedbackEditMinInterval() = %v, want 0", got)
	}
}

func TestDefaultConfig_ResponseFooterEnabled(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Agents.Defaults.IsResponseFooterEnabled() {
		t.Fatal("DefaultConfig().Agents.Defaults.ResponseFooter.Enabled should be true")
	}
}

func TestLoadConfig_ResponseFooterCanBeDisabled(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(
		configPath,
		[]byte(`{"version":4,"agents":{"defaults":{"workspace":"./workspace","response_footer":{"enabled":false}}}}`),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if cfg.Agents.Defaults.IsResponseFooterEnabled() {
		t.Fatal("agents.defaults.response_footer.enabled should be false when explicitly disabled")
	}
}

func TestLoadConfig_ResponseFooterDefaultsEnabledWhenOmitted(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(
		configPath,
		[]byte(`{"version":4,"agents":{"defaults":{"workspace":"./workspace"}}}`),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if !cfg.Agents.Defaults.IsResponseFooterEnabled() {
		t.Fatal("agents.defaults.response_footer.enabled should default to true when omitted")
	}
}

func TestDefaultConfig_MemoryToolEnabled(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Tools.Memory.Enabled || !cfg.Tools.IsToolEnabled("memory") {
		t.Fatal("memory tool should be enabled by default")
	}
}

func TestDefaultConfig_PromptMemoryIsBounded(t *testing.T) {
	cfg := DefaultConfig().Agents.Defaults.PromptMemory
	if got := cfg.EffectiveLongTermMaxBytes(); got != DefaultPromptMemoryLongTermMaxBytes {
		t.Fatalf("long-term max bytes = %d, want %d", got, DefaultPromptMemoryLongTermMaxBytes)
	}
	if got := cfg.EffectiveDailyNotesMaxBytes(); got != DefaultPromptMemoryDailyNotesMaxBytes {
		t.Fatalf("daily-note max bytes = %d, want %d", got, DefaultPromptMemoryDailyNotesMaxBytes)
	}
	if got := cfg.EffectiveRecentDays(); got != DefaultPromptMemoryRecentDays {
		t.Fatalf("recent days = %d, want %d", got, DefaultPromptMemoryRecentDays)
	}
}

func TestPromptMemoryConfigValidate(t *testing.T) {
	tests := []struct {
		name string
		cfg  PromptMemoryConfig
		ok   bool
	}{
		{name: "defaults", cfg: PromptMemoryConfig{}, ok: true},
		{
			name: "custom",
			cfg:  PromptMemoryConfig{LongTermMaxBytes: 1024, DailyNotesMaxBytes: 512, RecentDays: 7},
			ok:   true,
		},
		{name: "negative long term", cfg: PromptMemoryConfig{LongTermMaxBytes: -1}},
		{name: "negative daily", cfg: PromptMemoryConfig{DailyNotesMaxBytes: -1}},
		{name: "negative days", cfg: PromptMemoryConfig{RecentDays: -1}},
		{name: "too many days", cfg: PromptMemoryConfig{RecentDays: maxPromptMemoryRecentDays + 1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err == nil) != tt.ok {
				t.Fatalf("Validate() error = %v, want success %t", err, tt.ok)
			}
		})
	}
}

func TestLoadConfig_ToolFeedbackDefaultsFalseWhenUnset(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(
		configPath,
		[]byte(`{"version":4,"agents":{"defaults":{"workspace":"./workspace"}}}`),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if cfg.Agents.Defaults.ToolFeedback.Enabled {
		t.Fatal(
			"agents.defaults.tool_feedback.enabled should remain false when unset in config file",
		)
	}
	if !cfg.Agents.Defaults.IsSubagentToolFeedbackEnabled() {
		t.Fatal("agents.defaults.tool_feedback.subagents should default to true when unset")
	}
	if cfg.Agents.Defaults.ToolFeedback.SeparateMessages {
		t.Fatal("agents.defaults.tool_feedback.separate_messages should remain false when unset in config file")
	}
	if got := cfg.Agents.Defaults.GetToolFeedbackStyle(); got != "" {
		t.Fatalf("agents.defaults.tool_feedback.style = %q, want empty/raw default when unset", got)
	}
	if got := cfg.Agents.Defaults.GetToolFeedbackAnimationInterval(); got != 3*time.Second {
		t.Fatalf("agents.defaults.tool_feedback.animation_interval_secs = %v, want default 3s", got)
	}
	if got := cfg.Agents.Defaults.GetToolFeedbackEditMinInterval(); got != 0 {
		t.Fatalf("agents.defaults.tool_feedback.edit_min_interval_seconds = %v, want default 0", got)
	}
}

func TestLoadConfig_ToolFeedbackStyle(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(
		configPath,
		[]byte(`{"version":4,"agents":{"defaults":{"tool_feedback":{"enabled":true,"style":"working_summary"}}}}`),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if got := cfg.Agents.Defaults.GetToolFeedbackStyle(); got != "working_summary" {
		t.Fatalf("agents.defaults.tool_feedback.style = %q, want working_summary", got)
	}
}

func TestLoadConfig_ToolFeedbackSubagentsFalse(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(
		configPath,
		[]byte(`{"version":4,"agents":{"defaults":{"tool_feedback":{"enabled":true,"subagents":false}}}}`),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if cfg.Agents.Defaults.IsSubagentToolFeedbackEnabled() {
		t.Fatal("agents.defaults.tool_feedback.subagents = true, want false")
	}
}

func TestToolFeedbackSubagentsFalseRoundTrips(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Agents.Defaults.ToolFeedback.Subagents = false

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal(config) error = %v", err)
	}
	decoded := DefaultConfig()
	if err = json.Unmarshal(data, decoded); err != nil {
		t.Fatalf("Unmarshal(config) error = %v", err)
	}
	if decoded.Agents.Defaults.ToolFeedback.Subagents {
		t.Fatal("agents.defaults.tool_feedback.subagents did not preserve explicit false")
	}
}

func TestLoadConfig_ToolFeedbackThrottleIntervals(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(
		configPath,
		[]byte(
			`{"version":4,"agents":{"defaults":{"tool_feedback":{"enabled":true,"animation_interval_secs":5,"edit_min_interval_seconds":10}}}}`,
		),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if got := cfg.Agents.Defaults.GetToolFeedbackAnimationInterval(); got != 5*time.Second {
		t.Fatalf("agents.defaults.tool_feedback.animation_interval_secs = %v, want 5s", got)
	}
	if got := cfg.Agents.Defaults.GetToolFeedbackEditMinInterval(); got != 10*time.Second {
		t.Fatalf("agents.defaults.tool_feedback.edit_min_interval_seconds = %v, want 10s", got)
	}
}

func TestLoadConfig_WebPreferNativeDefaultsTrueWhenUnset(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"version":4,"tools":{"web":{"enabled":true}}}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if !cfg.Tools.Web.PreferNative {
		t.Fatal("PreferNative should remain true when unset in config file")
	}
}

func TestLoadConfig_WebPreferNativeCanBeDisabled(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(
		configPath,
		[]byte(`{"version":4,"tools":{"web":{"prefer_native":false}}}`),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if cfg.Tools.Web.PreferNative {
		t.Fatal("PreferNative should be false when disabled in config file")
	}
}

func TestLoadConfig_SyntaxErrorReportsLineAndColumn(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	raw := "{\n  \"version\": 4,\n  \"tools\": {\n    \"web\": {\n      \"enabled\": true,,\n      \"format\": \"markdown\"\n    }\n  }\n}\n"
	if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	_, err := LoadConfig(configPath)
	if err == nil {
		t.Fatal("expected syntax error, got nil")
	}
	if !strings.Contains(err.Error(), "syntax error at line 5, column 23") {
		t.Fatalf("expected line/column diagnostic, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "\"enabled\": true,,") {
		t.Fatalf("expected source snippet in diagnostic, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "^") {
		t.Fatalf("expected caret marker in diagnostic, got %q", err.Error())
	}
}

func TestLoadConfig_TypeErrorReportsFieldPath(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	raw := "{\n  \"version\": 4,\n  \"tools\": {\n    \"web\": {\n      \"fetch_limit_bytes\": \"oops\"\n    }\n  }\n}\n"
	if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	_, err := LoadConfig(configPath)
	if err == nil {
		t.Fatal("expected type error, got nil")
	}
	if !strings.Contains(err.Error(), "type error at line 5, column 33") {
		t.Fatalf("expected line/column diagnostic, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "fetch_limit_bytes") {
		t.Fatalf("expected field name in diagnostic, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "\"fetch_limit_bytes\": \"oops\"") {
		t.Fatalf("expected source snippet in diagnostic, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "^") {
		t.Fatalf("expected caret marker in diagnostic, got %q", err.Error())
	}
}

func TestLoadConfig_UnknownFieldsReportsExactPaths(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	raw := "{\n  \"version\": 4,\n  \"tools\": {\n    \"weeb\": {\n      \"enabled\": true\n    },\n    \"web\": {\n      \"fatch_limit_bytes\": 123\n    }\n  }\n}\n"
	if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	_, err := LoadConfig(configPath)
	if err == nil {
		t.Fatal("expected unknown field error, got nil")
	}
	if !strings.Contains(err.Error(), "tools.weeb") || !strings.Contains(err.Error(), "tools.web.fatch_limit_bytes") {
		t.Fatalf("expected exact unknown field paths, got %q", err.Error())
	}
}

func TestLoadConfig_RejectsUnknownChannelSettingsWithoutRewriting(t *testing.T) {
	tests := []struct {
		name        string
		channelName string
		channelType string
	}{
		{name: "standard_name", channelName: ChannelTelegram, channelType: ChannelTelegram},
		{name: "aliased_instance", channelName: "telegram_alerts", channelType: ChannelTelegram},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			typeField := ""
			if test.channelType != "" {
				typeField = fmt.Sprintf(`"type":%q,`, test.channelType)
			}
			raw := fmt.Sprintf(
				`{"version":%d,"channel_list":{%q:{%s"settings":{"removed_setting":null}}}}`,
				CurrentVersion,
				test.channelName,
				typeField,
			)
			dir := t.TempDir()
			configPath := filepath.Join(dir, "config.json")
			if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
				t.Fatalf("WriteFile() error: %v", err)
			}

			_, err := LoadConfig(configPath)
			wantField := fmt.Sprintf("channel_list.%s.settings.removed_setting", test.channelName)
			if err == nil || !strings.Contains(err.Error(), "unknown field(s): "+wantField) {
				t.Fatalf("LoadConfig() error = %v, want unknown field %q", err, wantField)
			}
			stored, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("ReadFile() error: %v", err)
			}
			if string(stored) != raw {
				t.Fatalf("LoadConfig() rewrote rejected config to %q", stored)
			}
		})
	}
}

func TestLoadConfigRejectsNonStringArrayItemsWithoutRewriting(t *testing.T) {
	tests := []struct {
		name     string
		document string
		wantPath string
	}{
		{
			name: "ordinary config field",
			document: `{
				"version": 4,
				"tools": {"web": {"private_host_whitelist": ["localhost", null]}}
			}`,
			wantPath: "tools.web.private_host_whitelist[1]",
		},
		{
			name: "channel common field",
			document: `{
				"version": 4,
				"channel_list": {
					"telegram": {"type": "telegram", "allow_from": ["trusted", null]}
				}
			}`,
			wantPath: "channel_list.telegram.allow_from[1]",
		},
		{
			name: "placeholder text",
			document: `{
				"version": 4,
				"channel_list": {
					"telegram": {"type": "telegram", "placeholder": {"text": ["Wait", null]}}
				}
			}`,
			wantPath: "channel_list.telegram.placeholder.text[1]",
		},
		{
			name: "registered channel setting",
			document: `{
				"version": 4,
				"channel_list": {
					"irc": {"type": "irc", "settings": {"channels": ["#ops", null]}}
				}
			}`,
			wantPath: "channel_list.irc.settings.channels[1]",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(configPath, []byte(test.document), 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			_, err := LoadConfig(configPath)
			if err == nil || !strings.Contains(err.Error(), test.wantPath) {
				t.Fatalf("LoadConfig() error = %v, want path %q", err, test.wantPath)
			}
			stored, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			if string(stored) != test.document {
				t.Fatalf("LoadConfig() rewrote rejected config to %q", stored)
			}
		})
	}
}

func TestDecodeCurrentConfigAllowsNullStringArrayFields(t *testing.T) {
	cfg := DefaultConfig()
	err := DecodeCurrentConfig([]byte(`{
		"version": 4,
		"tools": {"web": {"private_host_whitelist": null}},
		"channel_list": {
			"irc": {
				"type": "irc",
				"allow_from": null,
				"placeholder": {"text": null},
				"settings": {"channels": null}
			}
		}
	}`), cfg)
	if err != nil {
		t.Fatalf("DecodeCurrentConfig() error = %v", err)
	}
}

func TestLoadConfig_RejectsDuplicateFieldsWithoutRewriting(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantField string
	}{
		{
			name: "top_level_version",
			raw: fmt.Sprintf(
				`{"version":%d,"version":%d}`,
				CurrentVersion+1,
				CurrentVersion,
			),
			wantField: "version",
		},
		{
			name: "nested_field",
			raw: fmt.Sprintf(
				`{"version":%d,"tools":{"web":{"fetch_limit_bytes":1,"fetch_limit_bytes":2}}}`,
				CurrentVersion,
			),
			wantField: "tools.web.fetch_limit_bytes",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "config.json")
			if err := os.WriteFile(configPath, []byte(test.raw), 0o600); err != nil {
				t.Fatalf("WriteFile() error: %v", err)
			}

			_, err := LoadConfig(configPath)
			if err == nil || !strings.Contains(err.Error(), "duplicate field: "+test.wantField) {
				t.Fatalf("LoadConfig() error = %v, want duplicate field %q", err, test.wantField)
			}
			stored, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("ReadFile() error: %v", err)
			}
			if string(stored) != test.raw {
				t.Fatalf("LoadConfig() rewrote rejected config to %q", stored)
			}
			backups, err := filepath.Glob(configPath + ".*.bak")
			if err != nil || len(backups) != 0 {
				t.Fatalf("LoadConfig() backups = %v, %v", backups, err)
			}
		})
	}
}

func TestLoadConfig_RejectsDeprecatedEditFileTool(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	raw := fmt.Sprintf(`{
  "version": %d,
  "tools": {
    "edit_file": {"enabled": true},
    "apply_patch": {"enabled": false}
  }
}`, CurrentVersion)
	if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	_, err := LoadConfig(configPath)
	if err == nil || !strings.Contains(err.Error(), "unknown field(s): tools.edit_file") {
		t.Fatalf("LoadConfig() error = %v, want deprecated field rejection", err)
	}
}

func TestLoadConfig_RejectsRemovedSkillRegistryShapes(t *testing.T) {
	tests := map[string]string{
		"github sibling": `{"version":4,"tools":{"skills":{"github":{"token":"old"}}}}`,
		"registry list":  `{"version":4,"tools":{"skills":{"registries":[{"name":"github"}]}}}`,
		"embedded name":  `{"version":4,"tools":{"skills":{"registries":{"github":{"name":"github"}}}}}`,
		"token field":    `{"version":4,"tools":{"skills":{"registries":{"github":{"token":"old"}}}}}`,
		"nested params":  `{"version":4,"tools":{"skills":{"registries":{"github":{"param":{"proxy":"old"}}}}}}`,
	}

	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "config.json")
			if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
				t.Fatalf("WriteFile() error: %v", err)
			}

			if _, err := LoadConfig(configPath); err == nil {
				t.Fatal("LoadConfig() error = nil, want removed shape rejection")
			}
		})
	}
}

func TestLoadConfig_RequiresCurrentVersionWithoutRewriting(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "missing", raw: `{}`},
		{name: "version_0", raw: `{"version":0}`},
		{name: "version_1", raw: `{"version":1}`},
		{name: "version_2", raw: `{"version":2}`},
		{name: "version_3", raw: `{"version":3}`},
		{name: "future", raw: fmt.Sprintf(`{"version":%d}`, CurrentVersion+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "config.json")
			if err := os.WriteFile(configPath, []byte(test.raw), 0o600); err != nil {
				t.Fatalf("WriteFile() error: %v", err)
			}

			_, err := LoadConfig(configPath)
			if err == nil || !strings.Contains(err.Error(), "unsupported config version") {
				t.Fatalf("LoadConfig() error = %v, want version rejection", err)
			}

			stored, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("ReadFile() error: %v", err)
			}
			if string(stored) != test.raw {
				t.Fatalf("LoadConfig() rewrote rejected config to %q", stored)
			}
			backups, err := filepath.Glob(configPath + ".*.bak")
			if err != nil || len(backups) != 0 {
				t.Fatalf("LoadConfig() backups = %v, %v", backups, err)
			}
		})
	}
}

func TestDefaultConfig_ExecAllowRemoteEnabled(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Tools.Exec.AllowRemote {
		t.Fatal("DefaultConfig().Tools.Exec.AllowRemote should be true")
	}
}

func TestDefaultConfig_ExecPermissionModeDefaultsEmpty(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Tools.Exec.PermissionMode != "" {
		t.Fatalf("DefaultConfig().Tools.Exec.PermissionMode = %q, want empty", cfg.Tools.Exec.PermissionMode)
	}
}

func TestDefaultConfig_FilterSensitiveDataEnabled(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Tools.FilterSensitiveData {
		t.Fatal("DefaultConfig().Tools.FilterSensitiveData should be true")
	}
}

func TestDefaultConfig_FilterMinLength(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Tools.FilterMinLength != 8 {
		t.Fatalf("DefaultConfig().Tools.FilterMinLength = %d, want 8", cfg.Tools.FilterMinLength)
	}
}

func TestDefaultConfig_LoadImageEnabled(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Tools.LoadImage.Enabled {
		t.Fatal("DefaultConfig().Tools.LoadImage.Enabled should be true")
	}
	if !cfg.Tools.IsToolEnabled("load_image") {
		t.Fatal("DefaultConfig().Tools.IsToolEnabled(load_image) should be true")
	}
}

func TestDefaultConfig_ApplyPatchEnabled(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Tools.ApplyPatch.Enabled {
		t.Fatal("DefaultConfig().Tools.ApplyPatch.Enabled should be true")
	}
	if !cfg.Tools.IsToolEnabled("apply_patch") {
		t.Fatal("DefaultConfig().Tools.IsToolEnabled(apply_patch) should be true")
	}
}

func TestDefaultConfig_SearchFilesEnabled(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Tools.SearchFiles.Enabled {
		t.Fatal("DefaultConfig().Tools.SearchFiles.Enabled should be true")
	}
	if !cfg.Tools.IsToolEnabled("search_files") {
		t.Fatal("DefaultConfig().Tools.IsToolEnabled(search_files) should be true")
	}
}

func TestDefaultConfig_MessageMediaDisabled(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Tools.Message.Enabled {
		t.Fatal("DefaultConfig().Tools.Message.Enabled should be true")
	}
	if cfg.Tools.Message.MediaEnabled {
		t.Fatal("DefaultConfig().Tools.Message.MediaEnabled should be false")
	}
}

func TestLoadConfig_LoadImageCanBeDisabled(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	raw := "{\n  \"version\": 4,\n  \"tools\": {\n    \"load_image\": {\n      \"enabled\": false\n    }\n  }\n}\n"
	if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if cfg.Tools.LoadImage.Enabled {
		t.Fatal("LoadConfig().Tools.LoadImage.Enabled should be false")
	}
	if cfg.Tools.IsToolEnabled("load_image") {
		t.Fatal("LoadConfig().Tools.IsToolEnabled(load_image) should be false")
	}
}

func TestLoadConfig_ApplyPatchCanBeDisabled(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	raw := "{\n  \"version\": 4,\n  \"tools\": {\n    \"apply_patch\": {\n      \"enabled\": false\n    }\n  }\n}\n"
	if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if cfg.Tools.ApplyPatch.Enabled {
		t.Fatal("LoadConfig().Tools.ApplyPatch.Enabled should be false")
	}
	if cfg.Tools.IsToolEnabled("apply_patch") {
		t.Fatal("LoadConfig().Tools.IsToolEnabled(apply_patch) should be false")
	}
}

func TestLoadConfig_SearchFilesCanBeDisabled(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	raw := "{\n  \"version\": 4,\n  \"tools\": {\n    \"search_files\": {\n      \"enabled\": false\n    }\n  }\n}\n"
	if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if cfg.Tools.SearchFiles.Enabled {
		t.Fatal("LoadConfig().Tools.SearchFiles.Enabled should be false")
	}
	if cfg.Tools.IsToolEnabled("search_files") {
		t.Fatal("LoadConfig().Tools.IsToolEnabled(search_files) should be false")
	}
}

func TestToolsConfig_GetFilterMinLength(t *testing.T) {
	tests := []struct {
		name     string
		minLen   int
		expected int
	}{
		{"zero returns default", 0, 8},
		{"negative returns default", -1, 8},
		{"positive returns value", 16, 16},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &ToolsConfig{FilterMinLength: tt.minLen}
			if got := cfg.GetFilterMinLength(); got != tt.expected {
				t.Errorf("GetFilterMinLength() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestDefaultConfig_CronAllowCommandEnabled(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Tools.Cron.AllowCommand {
		t.Fatal("DefaultConfig().Tools.Cron.AllowCommand should be true")
	}
}

func TestDefaultConfig_UpdatePlanDisabled(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Tools.UpdatePlan.Enabled {
		t.Fatal("DefaultConfig().Tools.UpdatePlan.Enabled should be false")
	}
	if cfg.Tools.IsToolEnabled("update_plan") {
		t.Fatal("DefaultConfig().Tools.IsToolEnabled(\"update_plan\") should be false")
	}
}

func TestDefaultConfig_CronCommandAllowedRemotesEmpty(t *testing.T) {
	cfg := DefaultConfig()
	if len(cfg.Tools.Cron.CommandAllowedRemotes) != 0 {
		t.Fatalf(
			"DefaultConfig().Tools.Cron.CommandAllowedRemotes = %#v, want empty",
			cfg.Tools.Cron.CommandAllowedRemotes,
		)
	}
}

func TestDefaultConfig_HooksDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Hooks.Enabled {
		t.Fatal("DefaultConfig().Hooks.Enabled should be true")
	}
	if cfg.Hooks.Defaults.ObserverTimeoutMS != 500 {
		t.Fatalf("ObserverTimeoutMS = %d, want 500", cfg.Hooks.Defaults.ObserverTimeoutMS)
	}
	if cfg.Hooks.Defaults.InterceptorTimeoutMS != 5000 {
		t.Fatalf("InterceptorTimeoutMS = %d, want 5000", cfg.Hooks.Defaults.InterceptorTimeoutMS)
	}
	if cfg.Hooks.Defaults.ApprovalTimeoutMS != 60000 {
		t.Fatalf("ApprovalTimeoutMS = %d, want 60000", cfg.Hooks.Defaults.ApprovalTimeoutMS)
	}
}

func TestDefaultConfig_LogLevel(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Gateway.LogLevel != "warn" {
		t.Errorf("LogLevel = %q, want \"fatal\"", cfg.Gateway.LogLevel)
	}
}

func TestLoadConfig_ExecAllowRemoteDefaultsTrueWhenUnset(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"version":4,"tools":{"exec":{"enable_deny_patterns":true}}}`),
		0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if !cfg.Tools.Exec.AllowRemote {
		t.Fatal("tools.exec.allow_remote should remain true when unset in config file")
	}
}

func TestLoadConfig_ExecPermissionMode(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"version":4,"tools":{"exec":{"permission_mode":" READ_ONLY "}}}`),
		0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.Tools.Exec.PermissionMode != "read_only" {
		t.Fatalf("tools.exec.permission_mode = %q, want read_only", cfg.Tools.Exec.PermissionMode)
	}
}

func TestLoadConfig_InvalidExecPermissionMode(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"version":4,"tools":{"exec":{"permission_mode":"readonly"}}}`),
		0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	_, err := LoadConfig(configPath)
	if err == nil {
		t.Fatal("expected invalid exec permission mode error")
	}
	if !strings.Contains(err.Error(), "tools.exec.permission_mode") {
		t.Fatalf("expected tools.exec.permission_mode error, got %q", err.Error())
	}
}

func TestLoadConfig_InvalidExecPermissionModeFromEnv(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"version":4}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}
	t.Setenv("MINTCLAW_TOOLS_EXEC_PERMISSION_MODE", "readonly")

	_, err := LoadConfig(configPath)
	if err == nil {
		t.Fatal("expected invalid exec permission mode error")
	}
	if !strings.Contains(err.Error(), "tools.exec.permission_mode") {
		t.Fatalf("expected tools.exec.permission_mode error, got %q", err.Error())
	}
}

func TestLoadConfig_CronAllowCommandDefaultsTrueWhenUnset(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(
		configPath,
		[]byte(`{"version":4,"tools":{"cron":{"exec_timeout_minutes":5}}}`),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if !cfg.Tools.Cron.AllowCommand {
		t.Fatal("tools.cron.allow_command should remain true when unset in config file")
	}
}

func TestLoadConfig_CronCommandAllowedRemotes(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(
		configPath,
		[]byte(`{"version":4,"tools":{"cron":{"command_allowed_remotes":["telegram:1234567890","discord"]}}}`),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	want := []string{"telegram:1234567890", "discord"}
	if len(cfg.Tools.Cron.CommandAllowedRemotes) != len(want) {
		t.Fatalf("CommandAllowedRemotes = %#v, want %#v", cfg.Tools.Cron.CommandAllowedRemotes, want)
	}
	for i := range want {
		if cfg.Tools.Cron.CommandAllowedRemotes[i] != want[i] {
			t.Fatalf("CommandAllowedRemotes = %#v, want %#v", cfg.Tools.Cron.CommandAllowedRemotes, want)
		}
	}
}

func TestLoadConfig_WebToolsProxy(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")
	configJSON := `{
	"version": 4,
  "agents": {"defaults":{"workspace":"./workspace","model_name":"gpt4","max_tokens":8192,"max_tool_iterations":20}},
  "model_list": [{"model_name":"gpt4","provider":"openai","model":"gpt-5.4","api_keys":["x"],"enabled":true}],
  "tools": {"web":{"proxy":"http://127.0.0.1:7890"}}
}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if cfg.Tools.Web.Proxy != "http://127.0.0.1:7890" {
		t.Fatalf("Tools.Web.Proxy = %q, want %q", cfg.Tools.Web.Proxy, "http://127.0.0.1:7890")
	}
}

func TestLoadConfig_HooksProcessConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")
	configJSON := `{
  "version": 4,
  "hooks": {
    "processes": {
      "review-gate": {
        "enabled": true,
        "transport": "stdio",
        "command": ["uvx", "mintclaw-hook-reviewer"],
        "dir": "/tmp/hooks",
        "env": {
          "HOOK_MODE": "rewrite"
        },
        "observe": ["turn_start", "turn_end"],
        "intercept": ["before_tool", "approve_tool"]
      }
    },
    "builtins": {
      "audit": {
        "enabled": true,
        "priority": 5,
        "config": {
          "label": "audit"
        }
      }
    }
  }
}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}

	processCfg, ok := cfg.Hooks.Processes["review-gate"]
	if !ok {
		t.Fatal("expected review-gate process hook")
	}
	if !processCfg.Enabled {
		t.Fatal("expected review-gate process hook to be enabled")
	}
	if processCfg.Transport != "stdio" {
		t.Fatalf("Transport = %q, want stdio", processCfg.Transport)
	}
	if len(processCfg.Command) != 2 || processCfg.Command[0] != "uvx" {
		t.Fatalf("Command = %v", processCfg.Command)
	}
	if processCfg.Dir != "/tmp/hooks" {
		t.Fatalf("Dir = %q, want /tmp/hooks", processCfg.Dir)
	}
	if processCfg.Env["HOOK_MODE"] != "rewrite" {
		t.Fatalf("HOOK_MODE = %q, want rewrite", processCfg.Env["HOOK_MODE"])
	}
	if len(processCfg.Observe) != 2 || processCfg.Observe[1] != "turn_end" {
		t.Fatalf("Observe = %v", processCfg.Observe)
	}
	if len(processCfg.Intercept) != 2 || processCfg.Intercept[1] != "approve_tool" {
		t.Fatalf("Intercept = %v", processCfg.Intercept)
	}

	builtinCfg, ok := cfg.Hooks.Builtins["audit"]
	if !ok {
		t.Fatal("expected audit builtin hook")
	}
	if !builtinCfg.Enabled {
		t.Fatal("expected audit builtin hook to be enabled")
	}
	if builtinCfg.Priority != 5 {
		t.Fatalf("Priority = %d, want 5", builtinCfg.Priority)
	}
	if !strings.Contains(string(builtinCfg.Config), `"audit"`) {
		t.Fatalf("Config = %s", string(builtinCfg.Config))
	}
	if cfg.Hooks.Defaults.ApprovalTimeoutMS != 60000 {
		t.Fatalf("ApprovalTimeoutMS = %d, want 60000", cfg.Hooks.Defaults.ApprovalTimeoutMS)
	}
}

// TestDefaultConfig_SessionDimensions verifies the default session dimensions
// TestDefaultConfig_SummarizationThresholds verifies summarization defaults
func TestDefaultConfig_SummarizationThresholds(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Agents.Defaults.SummarizeMessageThreshold != 20 {
		t.Errorf(
			"SummarizeMessageThreshold = %d, want 20",
			cfg.Agents.Defaults.SummarizeMessageThreshold,
		)
	}
	if cfg.Agents.Defaults.SummarizeTokenPercent != 75 {
		t.Errorf("SummarizeTokenPercent = %d, want 75", cfg.Agents.Defaults.SummarizeTokenPercent)
	}
}

func TestDefaultConfig_SessionDimensions(t *testing.T) {
	cfg := DefaultConfig()

	if len(cfg.Session.Dimensions) != 1 || cfg.Session.Dimensions[0] != "chat" {
		t.Errorf("Session.Dimensions = %v, want [chat]", cfg.Session.Dimensions)
	}
}

func TestRepositorySavePreservesSessionDimensions(t *testing.T) {
	mustSetupSSHKey(t)

	for _, dimensions := range [][]string{{}, {"space", "topic"}} {
		name := "empty"
		if len(dimensions) > 0 {
			name = strings.Join(dimensions, "-")
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			cfg := DefaultConfig()
			cfg.Session.Dimensions = append([]string{}, dimensions...)
			if err := saveTestConfig(path, cfg); err != nil {
				t.Fatalf("saveTestConfig() error = %v", err)
			}

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			if len(dimensions) == 0 && !strings.Contains(string(data), `"dimensions": []`) {
				t.Fatalf("saved config does not contain explicit empty dimensions: %s", data)
			}

			loaded, err := LoadConfig(path)
			if err != nil {
				t.Fatalf("LoadConfig() error = %v", err)
			}
			if loaded.Session.Dimensions == nil ||
				strings.Join(loaded.Session.Dimensions, ",") != strings.Join(dimensions, ",") {
				t.Fatalf("Session.Dimensions = %v, want %v", loaded.Session.Dimensions, dimensions)
			}
		})
	}
}

func TestDecodeCurrentConfigRejectsPreviousSessionScopeField(t *testing.T) {
	var cfg Config
	err := DecodeCurrentConfig([]byte(`{"version":4,"session":{"dm_scope":"per-channel"}}`), &cfg)
	if err == nil || !strings.Contains(err.Error(), "session.dm_scope") {
		t.Fatalf("DecodeCurrentConfig() error = %v, want unknown session.dm_scope rejection", err)
	}
}

func TestDefaultConfig_WorkspacePath_Default(t *testing.T) {
	t.Setenv("MINTCLAW_HOME", "")

	var fakeHome string
	if runtime.GOOS == "windows" {
		fakeHome = `C:\tmp\home`
		t.Setenv("USERPROFILE", fakeHome)
	} else {
		fakeHome = "/tmp/home"
		t.Setenv("HOME", fakeHome)
	}

	cfg := DefaultConfig()
	want := filepath.Join(fakeHome, ".mintclaw", "workspace")

	if cfg.Agents.Defaults.Workspace != want {
		t.Errorf("Default workspace path = %q, want %q", cfg.Agents.Defaults.Workspace, want)
	}
}

func TestDefaultConfig_WorkspacePath_WithMintClawHome(t *testing.T) {
	t.Setenv("MINTCLAW_HOME", "/custom/mintclaw/home")

	cfg := DefaultConfig()
	want := filepath.Join("/custom/mintclaw/home", "workspace")

	if cfg.Agents.Defaults.Workspace != want {
		t.Errorf(
			"Workspace path with MINTCLAW_HOME = %q, want %q",
			cfg.Agents.Defaults.Workspace,
			want,
		)
	}
}

func TestDefaultConfig_IsolationEnabled(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Isolation.Enabled {
		t.Fatal("DefaultConfig().Isolation.Enabled should be false")
	}
}

func TestConfig_UnmarshalIsolation(t *testing.T) {
	cfg := DefaultConfig()
	raw := []byte(`{
		"isolation": {
			"enabled": false,
			"expose_paths": [
				{"source":"/src","target":"/dst","mode":"ro"}
			]
		}
	}`)
	if err := json.Unmarshal(raw, cfg); err != nil {
		t.Fatalf("json.Unmarshal isolation config: %v", err)
	}
	if cfg.Isolation.Enabled {
		t.Fatal("Isolation.Enabled should be false after unmarshal")
	}
	if len(cfg.Isolation.ExposePaths) != 1 {
		t.Fatalf("ExposePaths len = %d, want 1", len(cfg.Isolation.ExposePaths))
	}
	if got := cfg.Isolation.ExposePaths[0]; got.Source != "/src" || got.Target != "/dst" || got.Mode != "ro" {
		t.Fatalf("ExposePaths[0] = %+v, want source=/src target=/dst mode=ro", got)
	}
}

func TestChannelStringArraysRequireCanonicalJSON(t *testing.T) {
	var channel Channel
	if err := json.Unmarshal(
		[]byte(`{"allow_from":["123"],"placeholder":{"text":["Thinking..."]}}`),
		&channel,
	); err != nil {
		t.Fatalf("json.Unmarshal(canonical arrays) error = %v", err)
	}
	assert.Equal(t, []string{"123"}, channel.AllowFrom)
	assert.Equal(t, []string{"Thinking..."}, channel.Placeholder.Text)

	for name, input := range map[string]string{
		"scalar allow_from":      `{"allow_from":"123"}`,
		"numeric allow_from":     `{"allow_from":123}`,
		"mixed allow_from":       `{"allow_from":["123",456]}`,
		"scalar placeholder":     `{"placeholder":{"text":"Thinking..."}}`,
		"numeric placeholder":    `{"placeholder":{"text":123}}`,
		"mixed placeholder text": `{"placeholder":{"text":["Thinking...",123]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			var decoded Channel
			if err := json.Unmarshal([]byte(input), &decoded); err == nil {
				t.Fatalf("json.Unmarshal(%s) error = nil, want canonical string-array rejection", input)
			}
		})
	}
}

func TestLoadConfig_TelegramPlaceholderTextRejectsSingleString(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	data := `{
		"version": 4,
		"agents": { "defaults": { "workspace": "", "model_name": "", "max_tokens": 0, "max_tool_iterations": 0 } },
		"session": {},
		"channel_list": {
			"telegram": {
				"type": "telegram",
				"enabled": true,
				"allow_from": [],
				"placeholder": {
					"enabled": true,
					"text": "Thinking..."
				},
				"settings": {}
			}
		},
		"model_list": [],
		"gateway": {},
		"tools": {},
		"heartbeat": {},
		"devices": {},
		"voice": {}
	}`
	if err := os.WriteFile(cfgPath, []byte(data), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if _, err := LoadConfig(cfgPath); err == nil {
		t.Fatal("LoadConfig() error = nil, want scalar placeholder.text rejection")
	}
	stored, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(stored) != data {
		t.Fatalf("LoadConfig() rewrote rejected config to %q", stored)
	}
}

// TestLoadConfigReadOnly_WarnsForPlaintextAPIKey verifies that read-only loading resolves a plaintext
// api_keys entry into memory but does NOT rewrite the config file. File writes are the sole
// responsibility of Repository.Save.
func TestLoadConfigReadOnly_WarnsForPlaintextAPIKey(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	const original = `{"version":4,"model_list":[{"model_name":"test","provider":"openai","model":"gpt-4","api_keys":["sk-plaintext"]}]}`
	if err := os.WriteFile(cfgPath, []byte(original), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	t.Setenv("MINTCLAW_KEY_PASSPHRASE", "test-passphrase")
	t.Setenv("MINTCLAW_SSH_KEY_PATH", "")

	cfg, err := LoadConfigReadOnly(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfigReadOnly: %v", err)
	}
	// In-memory value must be the resolved plaintext.
	if cfg.ModelList[0].APIKey() != "sk-plaintext" {
		t.Errorf("in-memory api_key = %q, want %q", cfg.ModelList[0].APIKey(), "sk-plaintext")
	}
	// The file on disk must remain unchanged — no need upgrade version
	raw, _ := os.ReadFile(cfgPath)
	if string(raw) != original {
		t.Errorf("LoadConfigReadOnly must not modify the config file; got:\n%s", string(raw))
	}
}

// TestRepositorySave_EncryptsPlaintextAPIKey verifies that Repository.Save writes enc:// ciphertext
// to disk and that a subsequent LoadConfig decrypts it back to the original plaintext.
func TestRepositorySave_EncryptsPlaintextAPIKey(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	t.Setenv("MINTCLAW_KEY_PASSPHRASE", "test-passphrase")
	mustSetupSSHKey(t)

	cfg := DefaultConfig()
	cfg.ModelList = []*ModelConfig{
		{ModelName: "test", Provider: "openai", Model: "gpt-4", APIKeys: SimpleSecureStrings("")},
	}
	cfg.ModelList[0].APIKeys[0].Set("sk-plaintext")

	if err := saveTestConfig(cfgPath, cfg); err != nil {
		t.Fatalf("Repository.Save: %v", err)
	}

	// Disk must contain enc://, not the raw key.
	secPath := filepath.Join(dir, SecurityConfigFile)
	raw, _ := os.ReadFile(secPath)
	if !strings.Contains(string(raw), "enc://") {
		t.Errorf("saved file should contain enc://, got:\n%s", string(raw))
	}
	if strings.Contains(string(raw), "sk-plaintext") {
		t.Errorf("saved file must not contain the plaintext key")
	}

	// A fresh load must decrypt back to the original plaintext.
	cfg2, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig after Repository.Save: %v", err)
	}
	if cfg2.ModelList[0].APIKey() != "sk-plaintext" {
		t.Errorf("loaded api_key = %q, want %q", cfg2.ModelList[0].APIKey(), "sk-plaintext")
	}
}

// TestLoadConfig_NoSealWithoutPassphrase verifies that api_key values are left
// unchanged when MINTCLAW_KEY_PASSPHRASE is not set.
func TestLoadConfig_NoSealWithoutPassphrase(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	data := `{"version":4,"model_list":[{"model_name":"test","provider":"openai","model":"gpt-4","api_keys":["sk-plaintext"]}]}`
	if err := os.WriteFile(cfgPath, []byte(data), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	t.Setenv("MINTCLAW_KEY_PASSPHRASE", "")
	t.Setenv("MINTCLAW_SSH_KEY_PATH", "")

	if _, err := LoadConfig(cfgPath); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	raw, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(raw), "enc://") {
		t.Error("config file must not be modified when no passphrase is set")
	}
}

// TestLoadConfig_FileRefNotSealed verifies that file:// api_key references are not
// converted to enc:// values (they are resolved at runtime by the Resolver).
func TestLoadConfig_FileRefNotSealed(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	keyFile := filepath.Join(dir, "openai.key")
	if err := os.WriteFile(keyFile, []byte("sk-from-file"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	data := `{"version":4,"model_list":[{"model_name":"test","provider":"openai","model":"gpt-4"}]}`
	if err := os.WriteFile(cfgPath, []byte(data), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	secPath := filepath.Join(dir, SecurityConfigFile)
	if err := saveSecurityConfig(
		secPath,
		&Config{ModelList: SecureModelList{
			&ModelConfig{
				ModelName: "test",
				Provider:  "openai",
				Model:     "gpt-4",
				APIKeys:   SimpleSecureStrings("file://openai.key"),
			},
		}}); err != nil {
		t.Fatalf("saveSecurityConfig: %v", err)
	}

	t.Setenv("MINTCLAW_KEY_PASSPHRASE", "test-passphrase")
	t.Setenv("MINTCLAW_SSH_KEY_PATH", "")

	if _, err := LoadConfig(cfgPath); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	raw, _ := os.ReadFile(secPath)
	if !strings.Contains(string(raw), "file://openai.key") {
		t.Error("file:// reference should be preserved unchanged in the config file")
	}
	if strings.Contains(string(raw), "enc://") {
		t.Error("file:// reference must not be converted to enc://")
	}
}

// TestRepositorySave_MixedKeys verifies that Repository.Save encrypts only plaintext api_keys
// and leaves already-encrypted (enc://) and file:// entries unchanged.
func TestRepositorySave_MixedKeys(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	t.Setenv("MINTCLAW_KEY_PASSPHRASE", "test-passphrase")
	mustSetupSSHKey(t)

	// Pre-encrypt one key so we have a genuine enc:// value to put in the config.
	if err := saveTestConfig(cfgPath, &Config{
		Version: CurrentVersion,
		Agents:  AgentsConfig{List: []AgentConfig{DefaultAgentConfig()}},
		ModelList: []*ModelConfig{
			{ModelName: "pre", Provider: "openai", Model: "gpt-4", APIKeys: SimpleSecureStrings("sk-already-plain")},
		},
	}); err != nil {
		t.Fatalf("setup Repository.Save: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, SecurityConfigFile))
	// Extract the enc:// value from the saved file.
	var tmp struct {
		ModelList map[string]struct {
			APIKeys []string `yaml:"api_keys"`
		} `yaml:"model_list"`
	}
	if err := yaml.Unmarshal(raw, &tmp); err != nil || len(tmp.ModelList) == 0 {
		t.Fatalf("setup: could not parse saved config: %v", err)
	}
	alreadyEncrypted := tmp.ModelList["pre:0"].APIKeys[0]
	if !strings.HasPrefix(alreadyEncrypted, "enc://") {
		t.Fatalf("setup: expected enc:// key, got %q", alreadyEncrypted)
	}

	// Build a config with three models:
	//   1. plaintext   → must be encrypted by Repository.Save
	//   2. enc://      → must be left unchanged (already encrypted)
	//   3. file://     → must be left unchanged (file reference)
	keyFile := filepath.Join(dir, "api.key")
	if err := os.WriteFile(keyFile, []byte("sk-from-file"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	cfg := &Config{
		Version: CurrentVersion,
		Agents:  AgentsConfig{List: []AgentConfig{DefaultAgentConfig()}},
		ModelList: []*ModelConfig{
			{
				ModelName: "plain", Provider: "openai", Model: "gpt-4",
				APIKeys: SimpleSecureStrings("sk-new-plaintext"),
			},
			{
				ModelName: "enc", Provider: "openai", Model: "gpt-4",
				APIKeys: SimpleSecureStrings(alreadyEncrypted),
			},
			{
				ModelName: "file", Provider: "openai", Model: "gpt-4",
				APIKeys: SimpleSecureStrings("file://api.key"),
			},
		},
	}
	if err := saveTestConfig(cfgPath, cfg); err != nil {
		t.Fatalf("Repository.Save: %v", err)
	}

	t.Logf("alreadyEncrypted: %s", alreadyEncrypted)
	raw, _ = os.ReadFile(filepath.Join(dir, SecurityConfigFile))
	s := string(raw)

	t.Logf("saved file:\n%s", s)

	// 1. Plaintext must be encrypted.
	if strings.Contains(s, "sk-new-plaintext") {
		t.Error("plaintext key must not appear in saved file")
	}
	// 2. The pre-existing enc:// value must still be present (byte-for-byte unchanged).
	if !strings.Contains(s, alreadyEncrypted) {
		t.Error("pre-existing enc:// entry must be preserved unchanged")
	}
	// 3. file:// must be preserved.
	if !strings.Contains(s, "file://api.key") {
		t.Error("file:// reference must be preserved unchanged")
	}

	// Now load and verify all three decrypt/resolve correctly.
	cfg2, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig after Repository.Save: %v", err)
	}
	byName := make(map[string]string)
	for _, m := range cfg2.ModelList {
		byName[m.ModelName] = m.APIKey()
	}
	if byName["plain"] != "sk-new-plaintext" {
		t.Errorf("plain model api_key = %q, want %q", byName["plain"], "sk-new-plaintext")
	}
	if byName["enc"] != "sk-already-plain" {
		t.Errorf("enc model api_key = %q, want %q", byName["enc"], "sk-already-plain")
	}
	if byName["file"] != "sk-from-file" {
		t.Errorf("file model api_key = %q, want %q", byName["file"], "sk-from-file")
	}
}

// TestLoadConfig_MixedKeys_NoPassphrase verifies that when MINTCLAW_KEY_PASSPHRASE
// is not set, enc:// entries cause LoadConfig to return an error, while plaintext
// and file:// entries in the same config are not affected.
func TestLoadConfig_MixedKeys_NoPassphrase(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	// First encrypt a key so we have a real enc:// value.
	t.Setenv("MINTCLAW_KEY_PASSPHRASE", "test-passphrase")
	mustSetupSSHKey(t)
	if err := saveTestConfig(cfgPath, &Config{
		Version: CurrentVersion,
		Agents:  AgentsConfig{List: []AgentConfig{DefaultAgentConfig()}},
		ModelList: []*ModelConfig{
			{ModelName: "m", Provider: "openai", Model: "gpt-4", APIKeys: SimpleSecureStrings("sk-secret")},
		},
	}); err != nil {
		t.Fatalf("setup Repository.Save: %v", err)
	}
	raw, err := LoadConfig(cfgPath)
	assert.NoError(t, err)
	encValue := raw.ModelList[0].APIKeys[0].raw
	assert.NotEmpty(t, encValue)
	assert.Equal(t, "enc://", encValue[:6])

	// Write a mixed config: enc:// + plaintext + file://
	keyFile := filepath.Join(dir, "api.key")
	if err = os.WriteFile(keyFile, []byte("sk-from-file"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	mixed, _ := json.Marshal(map[string]any{
		"version": CurrentVersion,
		"model_list": []map[string]any{
			{"model_name": "enc", "model": "openai/gpt-4", "api_keys": []string{encValue}},
			{"model_name": "plain", "model": "openai/gpt-4", "api_keys": []string{"sk-plain"}},
			{"model_name": "file", "model": "openai/gpt-4", "api_keys": []string{"file://api.key"}},
		},
	})
	if err = os.WriteFile(cfgPath, mixed, 0o600); err != nil {
		t.Fatalf("setup write: %v", err)
	}
	secs, _ := yaml.Marshal(map[string]any{
		"model_list": map[string]map[string]any{
			"enc:0":   {"api_keys": []string{encValue}},
			"plain:0": {"api_keys": []string{"sk-plain"}},
			"file:0":  {"api_keys": []string{"file://api.key"}},
		},
	})
	if err = os.WriteFile(filepath.Join(dir, SecurityConfigFile), secs, 0o600); err != nil {
		t.Fatalf("security write: %v", err)
	}

	// Now clear the passphrase — LoadConfig must fail because enc:// cannot be decrypted.
	t.Setenv("MINTCLAW_KEY_PASSPHRASE", "")

	cfg2, err := LoadConfig(cfgPath)
	if err == nil {
		t.Logf("LoadConfig: %#v", cfg2.ModelList)
		t.Fatal("LoadConfig should fail when enc:// key is present and no passphrase is set")
	}
	if !strings.Contains(err.Error(), "passphrase required") {
		t.Errorf("error should mention passphrase required, got: %v", err)
	}
}

// TestRepositorySave_UsesPassphraseProvider verifies that Repository.Save encrypts plaintext
// api_keys using credential.PassphraseProvider() rather than os.Getenv directly.
// This matters for the launcher, which clears the environment variable and redirects
// PassphraseProvider to an in-memory SecureStore.
func TestRepositorySave_UsesPassphraseProvider(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	// Ensure the env var is empty — passphrase must come from PassphraseProvider only.
	t.Setenv("MINTCLAW_KEY_PASSPHRASE", "")
	mustSetupSSHKey(t)

	// Replace PassphraseProvider with an in-memory function (simulating SecureStore).
	const testPassphrase = "provider-passphrase"
	orig := credential.PassphraseProvider
	credential.PassphraseProvider = func() string { return testPassphrase }
	t.Cleanup(func() { credential.PassphraseProvider = orig })

	cfg := DefaultConfig()
	cfg.ModelList = []*ModelConfig{
		{ModelName: "test", Provider: "openai", Model: "gpt-4", APIKeys: SimpleSecureStrings("sk-plaintext")},
	}
	if err := saveTestConfig(cfgPath, cfg); err != nil {
		t.Fatalf("Repository.Save: %v", err)
	}

	raw, _ := os.ReadFile(filepath.Join(dir, SecurityConfigFile))
	if !strings.Contains(string(raw), "enc://") {
		t.Errorf(
			"Repository.Save should have encrypted plaintext key via PassphraseProvider; got:\n%s",
			raw,
		)
	}
}

// TestLoadConfig_UsesPassphraseProvider verifies that LoadConfig decrypts enc:// keys
// using credential.PassphraseProvider() rather than os.Getenv directly.
func TestLoadConfig_UsesPassphraseProvider(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	// Ensure the env var is empty throughout.
	t.Setenv("MINTCLAW_KEY_PASSPHRASE", "")
	mustSetupSSHKey(t)

	const testPassphrase = "provider-passphrase"
	const plainKey = "sk-secret"

	// First, encrypt the key using the same passphrase.
	encrypted, err := credential.Encrypt(testPassphrase, "", plainKey)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	raw, _ := json.Marshal(map[string]any{
		"version": CurrentVersion,
		"model_list": []map[string]any{
			{"model_name": "test", "provider": "openai", "model": "gpt-4", "api_keys": []string{encrypted}},
		},
	})
	if err = os.WriteFile(cfgPath, raw, 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Redirect PassphraseProvider — env var is empty, so without this the load would fail.
	orig := credential.PassphraseProvider
	credential.PassphraseProvider = func() string { return testPassphrase }
	t.Cleanup(func() { credential.PassphraseProvider = orig })

	t.Logf("cfgPath: %s", cfgPath)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.ModelList[0].APIKey() != plainKey {
		t.Errorf("api_key = %q, want %q", cfg.ModelList[0].APIKey(), plainKey)
	}
}

func TestConfigParsesLogLevel(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	data := `{"version":4,"gateway":{"log_level":"debug"}}`
	if err := os.WriteFile(cfgPath, []byte(data), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Gateway.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want \"debug\"", cfg.Gateway.LogLevel)
	}
}

func TestConfigLogLevelEmpty(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	data := `{"version":4}`
	if err := os.WriteFile(cfgPath, []byte(data), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	// When config omits log_level, the DefaultConfig value ("fatal") is preserved.
	if cfg.Gateway.LogLevel != "warn" {
		t.Errorf("LogLevel = %q, want \"fatal\"", cfg.Gateway.LogLevel)
	}
}

func TestResolveGatewayLogLevel(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	data := `{"version":4,"gateway":{"log_level":"debug"}}`
	if err := os.WriteFile(cfgPath, []byte(data), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if got := ResolveGatewayLogLevel(cfgPath); got != "debug" {
		t.Fatalf("ResolveGatewayLogLevel() = %q, want %q", got, "debug")
	}
}

func TestResolveGatewayLogLevel_UsesEnvOverrideAndNormalizesInvalid(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	data := `{"version":4,"gateway":{"log_level":"debug"}}`
	if err := os.WriteFile(cfgPath, []byte(data), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	t.Setenv("MINTCLAW_LOG_LEVEL", "warning")
	if got := ResolveGatewayLogLevel(cfgPath); got != "warn" {
		t.Fatalf("ResolveGatewayLogLevel() with env override = %q, want %q", got, "warn")
	}

	t.Setenv("MINTCLAW_LOG_LEVEL", "garbage")
	if got := ResolveGatewayLogLevel(cfgPath); got != DefaultGatewayLogLevel {
		t.Fatalf("ResolveGatewayLogLevel() with invalid env override = %q, want %q", got, DefaultGatewayLogLevel)
	}
}

func TestLoadConfig_AppliesClawHubRegistryEnvOverrides(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	data := `{"version":4,"tools":{"skills":{"registries":{"clawhub":{"enabled":true,"base_url":"https://clawhub.ai"}}}}}`
	if err := os.WriteFile(cfgPath, []byte(data), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	t.Setenv(envSkillsClawHubBaseURL, "https://clawhub.example.com")
	t.Setenv(envSkillsClawHubAuthToken, "clawhub-token-from-env")
	t.Setenv(envSkillsClawHubEnabled, "false")
	t.Setenv(envSkillsClawHubSearchPath, "/custom/search")
	t.Setenv(envSkillsClawHubDownloadPath, "/custom/download")
	t.Setenv(envSkillsClawHubTimeout, "17")

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	clawhub, ok := cfg.Tools.Skills.Registries.Get("clawhub")
	if !ok {
		t.Fatal("clawhub registry missing")
	}
	if clawhub.BaseURL != "https://clawhub.example.com" {
		t.Fatalf("BaseURL = %q, want %q", clawhub.BaseURL, "https://clawhub.example.com")
	}
	if clawhub.AuthToken.String() != "clawhub-token-from-env" {
		t.Fatalf("AuthToken = %q, want %q", clawhub.AuthToken.String(), "clawhub-token-from-env")
	}
	if clawhub.Enabled {
		t.Fatal("Enabled = true, want false")
	}
	if got := clawhub.Param["search_path"]; got != "/custom/search" {
		t.Fatalf("search_path = %v, want %q", got, "/custom/search")
	}
	if got := clawhub.Param["download_path"]; got != "/custom/download" {
		t.Fatalf("download_path = %v, want %q", got, "/custom/download")
	}
	if got := clawhub.Param["timeout"]; got != 17 {
		t.Fatalf("timeout = %v, want %d", got, 17)
	}
}

func TestLoadConfig_AppliesGitHubRegistryEnvOverrides(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	data := `{"version":4,"tools":{"skills":{"registries":{"github":{"enabled":true,"base_url":"https://github.com"}}}}}`
	if err := os.WriteFile(cfgPath, []byte(data), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	t.Setenv(envSkillsGitHubBaseURL, "https://ghe.example.com/git")
	t.Setenv(envSkillsGitHubAuthToken, "github-token-from-env")
	t.Setenv(envSkillsGitHubEnabled, "false")
	t.Setenv(envSkillsGitHubProxy, "http://127.0.0.1:7890")

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	github, ok := cfg.Tools.Skills.Registries.Get("github")
	if !ok {
		t.Fatal("github registry missing")
	}
	if github.BaseURL != "https://ghe.example.com/git" {
		t.Fatalf("BaseURL = %q, want %q", github.BaseURL, "https://ghe.example.com/git")
	}
	if github.AuthToken.String() != "github-token-from-env" {
		t.Fatalf("AuthToken = %q, want %q", github.AuthToken.String(), "github-token-from-env")
	}
	if github.Enabled {
		t.Fatal("Enabled = true, want false")
	}
	if got := github.Param["proxy"]; got != "http://127.0.0.1:7890" {
		t.Fatalf("proxy = %v, want %q", got, "http://127.0.0.1:7890")
	}
}

func TestLoadConfig_DoesNotRestoreRemovedRegistriesWithoutEnvOverrides(t *testing.T) {
	unsetSkillsRegistryEnv(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	data := `{"version":4,"tools":{"skills":{"registries":{}}}}`
	if err := os.WriteFile(cfgPath, []byte(data), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if names := cfg.Tools.Skills.Registries.Names(); len(names) != 0 {
		t.Fatalf("registry names = %v, want none", names)
	}
}

func TestLoadConfig_EnvOverrideCreatesRemovedRegistryFromCurrentDefaults(t *testing.T) {
	unsetSkillsRegistryEnv(t)
	t.Setenv(envSkillsGitHubProxy, "http://127.0.0.1:7890")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	data := `{"version":4,"tools":{"skills":{"registries":{}}}}`
	if err := os.WriteFile(cfgPath, []byte(data), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	github, ok := cfg.Tools.Skills.Registries.Get("github")
	if !ok {
		t.Fatal("github registry missing")
	}
	if !github.Enabled || github.BaseURL != "https://github.com" {
		t.Fatalf("github registry = %#v, want current defaults", github)
	}
	if got := github.Param["proxy"]; got != "http://127.0.0.1:7890" {
		t.Fatalf("proxy = %v, want environment override", got)
	}
	if _, ok = cfg.Tools.Skills.Registries.Get("clawhub"); ok {
		t.Fatal("clawhub registry restored without an override")
	}
}

func unsetSkillsRegistryEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		envSkillsClawHubEnabled,
		envSkillsClawHubBaseURL,
		envSkillsClawHubAuthToken,
		envSkillsClawHubSearchPath,
		envSkillsClawHubSkillsPath,
		envSkillsClawHubDownloadPath,
		envSkillsClawHubTimeout,
		envSkillsClawHubMaxZipSize,
		envSkillsClawHubMaxResponseSize,
		envSkillsGitHubEnabled,
		envSkillsGitHubBaseURL,
		envSkillsGitHubAuthToken,
		envSkillsGitHubProxy,
	} {
		value, set := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("Unsetenv(%q): %v", name, err)
		}
		t.Cleanup(func() {
			if set {
				_ = os.Setenv(name, value)
			} else {
				_ = os.Unsetenv(name)
			}
		})
	}
}

func TestModelConfig_ExtraBodyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	cfg := &Config{
		Version: CurrentVersion,
		Agents:  AgentsConfig{List: []AgentConfig{DefaultAgentConfig()}},
		ModelList: []*ModelConfig{
			{
				ModelName: "test-model", Provider: "openai", Model: "test",
				APIKeys:   SimpleSecureStrings("sk-test"),
				ExtraBody: map[string]any{"custom_field": "value", "num_field": 42},
			},
		},
	}

	if err := saveTestConfig(cfgPath, cfg); err != nil {
		t.Fatalf("Repository.Save error: %v", err)
	}

	loaded, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}

	if loaded.ModelList[0].ExtraBody == nil {
		t.Fatal("ExtraBody should not be nil after round-trip")
	}
	if got := loaded.ModelList[0].ExtraBody["custom_field"]; got != "value" {
		t.Errorf("ExtraBody[custom_field] = %v, want value", got)
	}
	if got := loaded.ModelList[0].ExtraBody["num_field"]; got != float64(42) {
		t.Errorf("ExtraBody[num_field] = %v, want 42", got)
	}
}

func TestModelConfig_CustomHeadersRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	cfg := &Config{
		Version: CurrentVersion,
		Agents:  AgentsConfig{List: []AgentConfig{DefaultAgentConfig()}},
		ModelList: []*ModelConfig{
			{
				ModelName: "test-model", Provider: "openai", Model: "test",
				APIKeys:       SimpleSecureStrings("sk-test"),
				CustomHeaders: map[string]string{"X-Source": "coding-plan", "X-Agent": "openclaw"},
			},
		},
	}

	if err := saveTestConfig(cfgPath, cfg); err != nil {
		t.Fatalf("Repository.Save error: %v", err)
	}

	loaded, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}

	if loaded.ModelList[0].CustomHeaders == nil {
		t.Fatal("CustomHeaders should not be nil after round-trip")
	}
	if got := loaded.ModelList[0].CustomHeaders["X-Source"]; got != "coding-plan" {
		t.Errorf("CustomHeaders[X-Source] = %q, want coding-plan", got)
	}
	if got := loaded.ModelList[0].CustomHeaders["X-Agent"]; got != "openclaw" {
		t.Errorf("CustomHeaders[X-Agent] = %q, want openclaw", got)
	}
}

func TestModelConfig_ToolSchemaTransformRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	cfg := &Config{
		Version: CurrentVersion,
		Agents:  AgentsConfig{List: []AgentConfig{DefaultAgentConfig()}},
		ModelList: []*ModelConfig{
			{
				ModelName: "test-model", Provider: "openai", Model: "test",
				APIKeys:             SimpleSecureStrings("sk-test"),
				ToolSchemaTransform: "simple",
			},
		},
	}

	if err := saveTestConfig(cfgPath, cfg); err != nil {
		t.Fatalf("Repository.Save error: %v", err)
	}

	loaded, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}

	if got := loaded.ModelList[0].ToolSchemaTransform; got != "simple" {
		t.Fatalf("ToolSchemaTransform = %q, want %q", got, "simple")
	}
}

func TestDefaultConfig_MinimaxExtraBody(t *testing.T) {
	cfg := DefaultConfig()

	var minimaxCfg *ModelConfig
	for i := range cfg.ModelList {
		if cfg.ModelList[i].Provider == "minimax" && cfg.ModelList[i].Model == "MiniMax-M2.5" {
			minimaxCfg = cfg.ModelList[i]
			break
		}
	}
	if minimaxCfg == nil {
		t.Fatal("Minimax model not found in ModelList")
	}
	if minimaxCfg.ExtraBody == nil {
		t.Fatal("Minimax ExtraBody should not be nil")
	}
	if got, ok := minimaxCfg.ExtraBody["reasoning_split"]; !ok || got != true {
		t.Fatalf("Minimax ExtraBody[reasoning_split] = %v, want true", got)
	}
}

func TestFilterSensitiveData(t *testing.T) {
	// Test with nil security config
	cfg := &Config{}
	if got := cfg.FilterSensitiveData("hello sk-key123 world"); got != "hello sk-key123 world" {
		t.Errorf("nil security: got %q, want original", got)
	}

	// Test with empty content
	if got := cfg.FilterSensitiveData(""); got != "" {
		t.Errorf("empty content: got %q, want empty", got)
	}

	// Test short content (less than FilterMinLength=8, should skip filtering)
	cfg.ModelList = SecureModelList{
		&ModelConfig{
			ModelName: "test",
			APIKeys:   SimpleSecureStrings("sk-long-key-12345"),
			Enabled:   true,
		},
	}
	m, err := cfg.GetModelConfig("test")
	assert.NoError(t, err)
	m.APIKeys = SimpleSecureStrings("sk-long-key-12345")
	cfg.Tools.FilterSensitiveData = true
	cfg.Tools.FilterMinLength = 8

	// Debug: check if sensitive values are collected
	values := cfg.collectSensitiveValues()
	t.Logf("collected %d sensitive values: %v", len(values), values)

	if got := cfg.FilterSensitiveData("sk-key"); got != "sk-key" {
		t.Errorf("short content should not be filtered: got %q", got)
	}

	// Test filtering works
	content := "Your API key is sk-long-key-12345 and token abc123"
	// abc123 is not in sensitive values, only sk-long-key-12345 should be filtered
	expected := "Your API key is [FILTERED] and token abc123"
	if got := cfg.FilterSensitiveData(content); got != expected {
		t.Errorf("filtering failed: got %q, want %q", got, expected)
	}

	// Test disabled filtering
	cfg.Tools.FilterSensitiveData = false
	if got := cfg.FilterSensitiveData(content); got != content {
		t.Errorf("disabled filtering: got %q, want original %q", got, content)
	}
}

func TestFilterSensitiveData_MultipleKeys(t *testing.T) {
	cfg := &Config{
		Tools: ToolsConfig{
			FilterSensitiveData: true,
			FilterMinLength:     8,
		},
		ModelList: SecureModelList{
			&ModelConfig{
				ModelName: "model1", Provider: "openai", Model: "model1",
				APIKeys: SecureStrings{NewSecureString("key-one"), NewSecureString("key-two")},
			},
			&ModelConfig{
				ModelName: "model2", Provider: "openai", Model: "model2",
				APIKeys: SecureStrings{NewSecureString("key-three")},
			},
		},
	}

	content := "key-one and key-two and key-three should be filtered"
	expected := "[FILTERED] and [FILTERED] and [FILTERED] should be filtered"
	if got := cfg.FilterSensitiveData(content); got != expected {
		t.Errorf("multiple keys: got %q, want %q", got, expected)
	}
}

func TestFilterSensitiveData_AllTokenTypes(t *testing.T) {
	cfg := &Config{
		// Model API keys
		ModelList: SecureModelList{
			&ModelConfig{
				ModelName: "test-model",
				APIKeys:   SecureStrings{NewSecureString("sk-model-key-12345")},
			},
		},
		// Channel tokens
		Channels: testChannelsConfigWithTokens(),
		Tools: ToolsConfig{
			FilterSensitiveData: true,
			FilterMinLength:     8,
			// Web tool API keys
			Web: WebToolsConfig{
				Brave: BraveConfig{APIKeys: SecureStrings{NewSecureString("brave-api-key")}},
				Tavily: TavilyConfig{
					APIKeys: SecureStrings{NewSecureString("tavily-api-key")},
				},
				Perplexity: PerplexityConfig{
					APIKeys: SecureStrings{NewSecureString("perplexity-api-key")},
				},
				GLMSearch:   GLMSearchConfig{APIKey: *NewSecureString("glm-search-key")},
				BaiduSearch: BaiduSearchConfig{APIKey: *NewSecureString("baidu-search-key")},
			},
			// Skills tokens
			Skills: SkillsToolsConfig{
				Registries: SkillsRegistriesConfig{
					"github": {
						AuthToken: *NewSecureString("github-token-xyz"),
					},
					"clawhub": {
						AuthToken: *NewSecureString("clawhub-auth-token"),
					},
				},
			},
		},
	}

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "model_api_key",
			content: "Using model with key sk-model-key-12345",
			want:    "Using model with key [FILTERED]",
		},
		{
			name:    "telegram_token",
			content: "Telegram token: telegram-bot-token-abcdef",
			want:    "Telegram token: [FILTERED]",
		},
		{
			name:    "discord_token",
			content: "Discord token: discord-bot-token-xyz789",
			want:    "Discord token: [FILTERED]",
		},
		{
			name:    "slack_tokens",
			content: "Slack bot: xoxb-slack-bot-token, app: xapp-slack-app-token",
			want:    "Slack bot: [FILTERED], app: [FILTERED]",
		},
		{
			name:    "matrix_token",
			content: "Matrix access token: matrix-access-token-abc",
			want:    "Matrix access token: [FILTERED]",
		},
		{
			name:    "brave_api_key",
			content: "Brave key: brave-api-key",
			want:    "Brave key: [FILTERED]",
		},
		{
			name:    "tavily_api_key",
			content: "Tavily key: tavily-api-key",
			want:    "Tavily key: [FILTERED]",
		},
		{
			name:    "github_token",
			content: "GitHub token: github-token-xyz",
			want:    "GitHub token: [FILTERED]",
		},
		{
			name:    "irc_passwords",
			content: "IRC password: irc-password, nickserv: nickserv-pass",
			want:    "IRC password: [FILTERED], nickserv: [FILTERED]",
		},
		{
			name:    "mixed_content",
			content: "Model key sk-model-key-12345 and Telegram token telegram-bot-token-abcdef",
			want:    "Model key [FILTERED] and Telegram token [FILTERED]",
		},
		{
			name:    "short_key_not_filtered",
			content: "Key abc not filtered because length < 8",
			want:    "Key abc not filtered because length < 8",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cfg.FilterSensitiveData(tt.content); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// MakeBackup tests
// ---------------------------------------------------------------------------

// TestMakeBackup_WithDateSuffix verifies backup files include a date suffix.
func TestMakeBackup_WithDateSuffix(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"version":4}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := MakeBackup(configPath); err != nil {
		t.Fatalf("MakeBackup: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	var hasDatedBackup bool
	for _, e := range entries {
		if matched, _ := filepath.Match("config.json.20*.bak", e.Name()); matched {
			hasDatedBackup = true
			// Verify backup content matches original
			bakPath := filepath.Join(dir, e.Name())
			data, err := os.ReadFile(bakPath)
			if err != nil {
				t.Fatalf("ReadFile backup: %v", err)
			}
			if string(data) != `{"version":4}` {
				t.Errorf("backup content = %q, want original content", string(data))
			}
			break
		}
	}
	if !hasDatedBackup {
		t.Error("expected backup file with date suffix pattern config.json.20*.bak")
	}
}

// TestMakeBackup_AlsoBacksSecurityFile verifies that the security config file
// is also backed up with the same date suffix.
func TestMakeBackup_AlsoBacksSecurityFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	secPath := securityPath(configPath)

	if err := os.WriteFile(configPath, []byte(`{"version":4}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		secPath,
		[]byte(`model_list:\n  test:0:\n    api_keys:\n      - "sk-test"\n`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	if err := MakeBackup(configPath); err != nil {
		t.Fatalf("MakeBackup: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	configBackups := 0
	secBackups := 0
	for _, e := range entries {
		if matched, _ := filepath.Match("config.json.20*.bak", e.Name()); matched {
			configBackups++
		}
		if matched, _ := filepath.Match(".security.yml.20*.bak", e.Name()); matched {
			secBackups++
		}
	}
	if configBackups != 1 {
		t.Errorf("expected 1 config backup, got %d", configBackups)
	}
	if secBackups != 1 {
		t.Errorf("expected 1 security backup, got %d", secBackups)
	}
}

// TestMakeBackup_NonexistentFileSkipsBackup verifies that MakeBackup returns nil
// when the config file does not exist (no error, no panic).
func TestMakeBackup_NonexistentFileSkipsBackup(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "nonexistent.json")

	if err := MakeBackup(configPath); err != nil {
		t.Fatalf("MakeBackup on nonexistent file should return nil, got: %v", err)
	}
}

// TestMakeBackup_OnlyConfigNoSecurity verifies backup succeeds when only
// the config file exists and no security file.
func TestMakeBackup_OnlyConfigNoSecurity(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"version":4}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := MakeBackup(configPath); err != nil {
		t.Fatalf("MakeBackup: %v", err)
	}

	entries, _ := os.ReadDir(dir)
	configBackups := 0
	secBackups := 0
	for _, e := range entries {
		if matched, _ := filepath.Match("config.json.20*.bak", e.Name()); matched {
			configBackups++
		}
		if matched, _ := filepath.Match(".security.yml.20*.bak", e.Name()); matched {
			secBackups++
		}
	}
	if configBackups != 1 {
		t.Errorf("expected 1 config backup, got %d", configBackups)
	}
	if secBackups != 0 {
		t.Errorf("expected 0 security backups when no security file exists, got %d", secBackups)
	}
}

// TestMakeBackup_SameDateSuffix verifies that config and security backups
// share the same date suffix (they are created in the same MakeBackup call).
func TestMakeBackup_SameDateSuffix(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	secPath := securityPath(configPath)

	if err := os.WriteFile(configPath, []byte(`{"version":4}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secPath, []byte(`key: value`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := MakeBackup(configPath); err != nil {
		t.Fatalf("MakeBackup: %v", err)
	}

	entries, _ := os.ReadDir(dir)
	var configDate, secDate string
	for _, e := range entries {
		name := e.Name()
		// Extract date part: after the last . before .bak
		// e.g. config.json.20260330.bak → 20260330
		if strings.HasPrefix(name, "config.json.") && strings.HasSuffix(name, ".bak") {
			configDate = strings.TrimPrefix(name, "config.json.")
			configDate = strings.TrimSuffix(configDate, ".bak")
		}
		if strings.HasPrefix(name, ".security.yml.") && strings.HasSuffix(name, ".bak") {
			secDate = strings.TrimPrefix(name, ".security.yml.")
			secDate = strings.TrimSuffix(secDate, ".bak")
		}
	}
	if configDate == "" {
		t.Fatal("config backup file not found")
	}
	if secDate == "" {
		t.Fatal("security backup file not found")
	}
	if configDate != secDate {
		t.Errorf("config backup date = %q, security backup date = %q, should match", configDate, secDate)
	}
}

func testChannelsConfigWithTokens() ChannelsConfig {
	channels := make(ChannelsConfig)
	type chDef struct {
		name string
		cfg  any
	}
	defs := []chDef{
		{"telegram", TelegramSettings{Token: *NewSecureString("telegram-bot-token-abcdef")}},
		{"discord", DiscordSettings{Token: *NewSecureString("discord-bot-token-xyz789")}},
		{
			"slack",
			SlackSettings{
				BotToken: *NewSecureString("xoxb-slack-bot-token"),
				AppToken: *NewSecureString("xapp-slack-app-token"),
			},
		},
		{"matrix", MatrixSettings{AccessToken: *NewSecureString("matrix-access-token-abc")}},
		{
			"feishu",
			FeishuSettings{
				AppSecret:  *NewSecureString("feishu-app-secret-123"),
				EncryptKey: *NewSecureString("feishu-encrypt-key"),
			},
		},
		{"dingtalk", DingTalkSettings{ClientSecret: *NewSecureString("dingtalk-client-secret")}},
		{"onebot", OneBotSettings{AccessToken: *NewSecureString("onebot-access-token")}},
		{"wecom", WeComSettings{Secret: *NewSecureString("wecom-secret")}},
		{"mintclaw", MintClawSettings{Token: *NewSecureString("mintclaw-token-abc123")}},
		{
			"irc",
			IRCSettings{
				Password:         *NewSecureString("irc-password"),
				NickServPassword: *NewSecureString("nickserv-pass"),
				SASLPassword:     *NewSecureString("sasl-pass"),
			},
		},
	}
	for _, def := range defs {
		// Create Channel directly with settings to preserve SecureString values
		bc := &Channel{Type: def.name}
		_ = bc.Decode(def.cfg)
		channels[def.name] = bc
	}
	return channels
}
