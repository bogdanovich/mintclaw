package agent

import (
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/skills"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

func newSkillCompatibilityEnvironment(
	cfg *config.Config,
	runtimeProduct skills.SkillRuntime,
	toolPolicy *config.AgentCapabilityPolicy,
	mcpPolicy *config.AgentCapabilityPolicy,
	registry *tools.ToolRegistry,
) skills.SkillCompatibilityEnvironment {
	environment := skills.NewSkillCompatibilityEnvironment(runtimeProduct)
	environment.ToolState = func(name string) skills.SkillRequirementState {
		name = strings.ToLower(strings.TrimSpace(name))
		configuredState := configuredSkillToolState(cfg, runtimeProduct, name)
		if registry != nil {
			if !registry.HasRegistered(name) {
				if configuredState == skills.SkillRequirementPolicyDisabled {
					return configuredState
				}
				return skills.SkillRequirementMissing
			}
			if !toolAllowedByPolicy(toolPolicy, name) {
				return skills.SkillRequirementPolicyDisabled
			}
			return skills.SkillRequirementAvailable
		}
		if configuredState == skills.SkillRequirementMissing {
			return configuredState
		}
		if !toolAllowedByPolicy(toolPolicy, name) ||
			configuredState == skills.SkillRequirementPolicyDisabled {
			return skills.SkillRequirementPolicyDisabled
		}
		return configuredState
	}
	environment.MCPServerState = func(name string) skills.SkillRequirementState {
		name = normalizeMCPServerName(name)
		if cfg == nil {
			return skills.SkillRequirementMissing
		}
		configured := false
		enabled := false
		for configuredName, server := range cfg.Tools.MCP.Servers {
			if normalizeMCPServerName(configuredName) != name {
				continue
			}
			configured = true
			enabled = server.Enabled
			break
		}
		if !configured {
			return skills.SkillRequirementMissing
		}
		if runtimeProduct == skills.SkillRuntimeCoding || !cfg.Tools.MCP.Enabled || !enabled ||
			!toolAllowedByPolicy(mcpPolicy, name) {
			return skills.SkillRequirementPolicyDisabled
		}
		return skills.SkillRequirementAvailable
	}
	return environment
}

// ConfiguredSkillCompatibilityEnvironment returns a read-only compatibility
// view for CLI diagnostics. It does not construct tools, start MCP servers, or
// mutate runtime state.
func ConfiguredSkillCompatibilityEnvironment(
	cfg *config.Config,
	runtimeProduct skills.SkillRuntime,
) skills.SkillCompatibilityEnvironment {
	var toolPolicy, mcpPolicy *config.AgentCapabilityPolicy
	if runtimeProduct == skills.SkillRuntimeCoding {
		mcpPolicy = &config.AgentCapabilityPolicy{Default: config.AgentCapabilityDefaultDeny}
	} else if selected := defaultConfiguredAgent(cfg); selected != nil {
		toolPolicy = selected.ToolPolicy
		mcpPolicy = selected.MCPServerPolicy
	}
	return newSkillCompatibilityEnvironment(cfg, runtimeProduct, toolPolicy, mcpPolicy, nil)
}

func defaultConfiguredAgent(cfg *config.Config) *config.AgentConfig {
	if cfg == nil {
		return nil
	}
	for index := range cfg.Agents.List {
		if cfg.Agents.List[index].Default {
			return &cfg.Agents.List[index]
		}
	}
	for index := range cfg.Agents.List {
		if normalizeMCPServerName(cfg.Agents.List[index].ID) == "main" {
			return &cfg.Agents.List[index]
		}
	}
	if len(cfg.Agents.List) == 0 {
		return nil
	}
	return &cfg.Agents.List[0]
}

func configuredSkillToolState(
	cfg *config.Config,
	runtimeProduct skills.SkillRuntime,
	name string,
) skills.SkillRequirementState {
	if runtimeProduct == skills.SkillRuntimeCoding {
		if slices.Contains([]string{
			"append_file",
			"apply_patch",
			"exec",
			"list_dir",
			"read_file",
			"repository_diff",
			"repository_status",
			"search_files",
			"update_plan",
			"write_file",
		}, name) {
			return skills.SkillRequirementAvailable
		}
		if name == "request_user_input" && cfg != nil {
			if cfg.Tools.RequestUserInput.Enabled {
				return skills.SkillRequirementAvailable
			}
			return skills.SkillRequirementPolicyDisabled
		}
		return skills.SkillRequirementMissing
	}
	if cfg == nil {
		return skills.SkillRequirementMissing
	}
	if slices.Contains([]string{
		"browser_act",
		"browser_capture",
		"browser_contexts",
		"browser_diagnostics",
		"browser_execute",
		"browser_observe",
		"browser_session",
		"browser_targets",
	}, name) {
		selected := defaultConfiguredAgent(cfg)
		if !cfg.Tools.Browser.Enabled || selected == nil ||
			!slices.Contains(cfg.Tools.Browser.Agents, selected.ID) {
			return skills.SkillRequirementPolicyDisabled
		}
		return skills.SkillRequirementAvailable
	}
	switch name {
	case "append_file", "apply_patch", "document", "exec", "find_skills", "i2c", "image_generate",
		"install_skill", "list_dir", "load_image", "memory", "message", "read_file", "request_user_input",
		"search_files", "send_file", "send_tts", "serial", "spawn", "spi", "subagent", "update_plan",
		"web_fetch", "write_file":
		if cfg.Tools.IsToolEnabled(name) {
			return skills.SkillRequirementAvailable
		}
		return skills.SkillRequirementPolicyDisabled
	default:
		return skills.SkillRequirementMissing
	}
}
