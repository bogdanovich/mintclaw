package agent

import (
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	"github.com/bogdanovich/mintclaw/pkg/skills"
)

func newSkillCompatibilityEnvironment(
	cfg *config.Config,
	runtimeProduct skills.SkillRuntime,
	mcpPolicy *config.AgentCapabilityPolicy,
	reportProvider func() runtimecap.Report,
	revisionProvider ...func() uint64,
) skills.SkillCompatibilityEnvironment {
	environment := skills.NewSkillCompatibilityEnvironment(runtimeProduct)
	if len(revisionProvider) > 0 {
		environment.Revision = revisionProvider[0]
	}
	environment.ToolState = func(name string) skills.SkillRequirementState {
		name = strings.ToLower(strings.TrimSpace(name))
		if reportProvider == nil {
			return skills.SkillRequirementMissing
		}
		availability, ok := reportProvider().LookupTool(name)
		if !ok {
			return skills.SkillRequirementMissing
		}
		return skillRequirementStateFromAvailability(availability.Available, availability.Reason)
	}
	environment.CapabilityState = func(name string) skills.SkillCapabilityRequirementState {
		capability, valid := runtimecap.ParseCapabilityID(name)
		if !valid || reportProvider == nil {
			return skills.SkillCapabilityRequirementState{State: skills.SkillRequirementMissing}
		}
		availability, ok := reportProvider().Lookup(capability)
		if !ok {
			return skills.SkillCapabilityRequirementState{State: skills.SkillRequirementMissing}
		}
		result := skills.SkillCapabilityRequirementState{
			State: skillRequirementStateFromAvailability(availability.Available, availability.Reason),
		}
		if availability.Reason != nil {
			result.Reason = availability.Reason.Code
			result.Dependency = availability.Reason.Dependency
		}
		return result
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

func skillRequirementStateFromAvailability(
	available bool,
	reason *runtimecap.UnavailableReason,
) skills.SkillRequirementState {
	if available {
		return skills.SkillRequirementAvailable
	}
	if reason == nil {
		return skills.SkillRequirementMissing
	}
	switch reason.Code {
	case runtimecap.ReasonPolicyDisabled:
		return skills.SkillRequirementPolicyDisabled
	case runtimecap.ReasonRuntimeUnsupported:
		return skills.SkillRequirementIncompatible
	default:
		return skills.SkillRequirementMissing
	}
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
	report := configuredSkillAdmissionReport(cfg, runtimeProduct, toolPolicy)
	return newSkillCompatibilityEnvironment(cfg, runtimeProduct, mcpPolicy, func() runtimecap.Report {
		return report
	})
}

func configuredSkillAdmissionReport(
	cfg *config.Config,
	runtimeProduct skills.SkillRuntime,
	toolPolicy *config.AgentCapabilityPolicy,
) runtimecap.Report {
	states := configuredSkillToolStates(cfg, runtimeProduct)
	tools := make([]runtimecap.ToolAvailability, 0, len(states))
	for name, state := range states {
		if state == skills.SkillRequirementAvailable && !toolAllowedByPolicy(toolPolicy, name) {
			state = skills.SkillRequirementPolicyDisabled
			states[name] = state
		}
		switch state {
		case skills.SkillRequirementAvailable:
			tools = append(tools, runtimecap.ToolAvailable(name))
		case skills.SkillRequirementPolicyDisabled:
			tools = append(tools, runtimecap.ToolUnavailable(name, runtimecap.ReasonPolicyDisabled))
		}
	}
	kind := runtimecap.KindGateway
	if runtimeProduct == skills.SkillRuntimeCoding {
		kind = runtimecap.KindCoding
	}
	return runtimecap.NewAdmissionReport(
		kind,
		configuredSkillCapabilities(cfg, runtimeProduct, states),
		tools,
	)
}

func configuredSkillToolStates(
	cfg *config.Config,
	runtimeProduct skills.SkillRuntime,
) map[string]skills.SkillRequirementState {
	states := make(map[string]skills.SkillRequirementState)
	if runtimeProduct == skills.SkillRuntimeCoding {
		for _, name := range []string{
			"append_file", "apply_patch", "exec", "list_dir", "read_file", "repository_diff",
			"repository_status", "search_files", "update_plan", "write_file",
		} {
			states[name] = skills.SkillRequirementAvailable
		}
		if cfg != nil && cfg.Tools.RequestUserInput.Enabled {
			states["request_user_input"] = skills.SkillRequirementAvailable
		} else {
			states["request_user_input"] = skills.SkillRequirementPolicyDisabled
		}
		documentState := skills.SkillRequirementPolicyDisabled
		if cfg != nil && cfg.Coding.Capabilities.Document {
			documentState = skills.SkillRequirementAvailable
			if !documentToolAvailable() {
				documentState = skills.SkillRequirementMissing
			}
		}
		states["document"] = documentState

		browserState := skills.SkillRequirementPolicyDisabled
		authorities := configuredCodingBrowserAuthorityOperations(cfg)
		if cfg != nil && cfg.Coding.Capabilities.Browser {
			browserState = skills.SkillRequirementMissing
			if len(authorities) > 0 {
				browserState = skills.SkillRequirementAvailable
			}
		}
		states["browser_targets"] = browserState
		states["browser_session"] = configuredCodingBrowserToolState(
			browserState,
			authorities,
			"browser_open",
			"browser_status",
			"browser_close",
		)
		states["browser_contexts"] = configuredCodingBrowserToolState(
			browserState,
			authorities,
			"browser_context_list",
			"browser_context_open",
			"browser_context_select",
			"browser_context_close",
		)
		for toolName, operation := range map[string]string{
			"browser_act":         "browser_act",
			"browser_capture":     "browser_capture",
			"browser_diagnostics": "browser_diagnostics",
			"browser_observe":     "browser_observe",
		} {
			states[toolName] = configuredCodingBrowserToolState(browserState, authorities, operation)
		}
		return states
	}
	if cfg == nil {
		return states
	}
	for _, name := range []string{
		"append_file", "apply_patch", "document", "exec", "find_skills", "i2c", "image_generate",
		"install_skill", "list_dir", "load_image", "memory", "message", "read_file", "request_user_input",
		"search_files", "send_file", "send_tts", "serial", "spawn", "spi", "subagent", "update_plan",
		"web_fetch", "write_file",
	} {
		states[name] = skills.SkillRequirementPolicyDisabled
		if cfg.Tools.IsToolEnabled(name) {
			states[name] = skills.SkillRequirementAvailable
		}
	}
	if states["document"] == skills.SkillRequirementAvailable && !documentToolAvailable() {
		states["document"] = skills.SkillRequirementMissing
	}
	browserState := skills.SkillRequirementPolicyDisabled
	selected := defaultConfiguredAgent(cfg)
	if cfg.Tools.Browser.Enabled && selected != nil &&
		containsExactString(cfg.Tools.Browser.Agents, selected.ID) {
		browserState = skills.SkillRequirementAvailable
	}
	for _, name := range []string{
		"browser_act", "browser_capture", "browser_contexts", "browser_diagnostics",
		"browser_execute", "browser_observe", "browser_session", "browser_targets",
	} {
		states[name] = browserState
	}
	return states
}

func configuredSkillCapabilities(
	cfg *config.Config,
	runtimeProduct skills.SkillRuntime,
	toolStates map[string]skills.SkillRequirementState,
) []runtimecap.Availability {
	capabilities := make([]runtimecap.Availability, 0, 10)
	documentReason := runtimecap.ReasonPolicyDisabled
	if cfg != nil && (runtimeProduct == skills.SkillRuntimeGateway && cfg.Tools.IsToolEnabled("document") ||
		runtimeProduct == skills.SkillRuntimeCoding && cfg.Coding.Capabilities.Document) {
		documentReason = runtimecap.ReasonServiceUnavailable
	}
	for _, capability := range []runtimecap.CapabilityID{
		runtimecap.CapabilityDocumentInspect,
		runtimecap.CapabilityDocumentExtract,
		runtimecap.CapabilityDocumentRender,
	} {
		capabilities = append(
			capabilities,
			configuredCapabilityForTool(capability, "document", toolStates, documentReason),
		)
	}
	if runtimeProduct == skills.SkillRuntimeCoding {
		capabilities = append(capabilities, runtimecap.Unavailable(
			runtimecap.CapabilityDocumentForm,
			runtimecap.ReasonRuntimeUnsupported,
		))
	} else {
		capabilities = append(capabilities, configuredCapabilityForTool(
			runtimecap.CapabilityDocumentForm,
			"document",
			toolStates,
			documentReason,
		))
	}

	browserReason := runtimecap.ReasonPolicyDisabled
	if cfg != nil && (runtimeProduct == skills.SkillRuntimeGateway && cfg.Tools.Browser.Enabled ||
		runtimeProduct == skills.SkillRuntimeCoding && cfg.Coding.Capabilities.Browser) {
		browserReason = runtimecap.ReasonNotConfigured
	}
	workflow := configuredCapabilityForTool(
		runtimecap.CapabilityBrowserWorkflow,
		"browser_session",
		toolStates,
		browserReason,
	)
	if runtimeProduct == skills.SkillRuntimeCoding {
		workflowState := configuredCodingBrowserToolState(
			toolStates["browser_targets"],
			configuredCodingBrowserAuthorityOperations(cfg),
			"browser_open",
			"browser_status",
			"browser_close",
			"browser_observe",
			"browser_act",
		)
		workflow = configuredCapabilityForState(
			runtimecap.CapabilityBrowserWorkflow,
			workflowState,
			browserReason,
		)
	}
	capabilities = append(capabilities, workflow)
	for capability, toolName := range map[runtimecap.CapabilityID]string{
		runtimecap.CapabilityBrowserObserve: "browser_observe",
		runtimecap.CapabilityBrowserAct:     "browser_act",
		runtimecap.CapabilityBrowserCapture: "browser_capture",
	} {
		capabilities = append(
			capabilities,
			configuredCapabilityForTool(capability, toolName, toolStates, browserReason),
		)
	}
	download := runtimecap.Unavailable(runtimecap.CapabilityBrowserDownload, browserReason)
	if runtimeProduct == skills.SkillRuntimeGateway && cfg != nil && browser.PlaywrightDownloadAvailable(cfg) {
		download = configuredCapabilityForTool(
			runtimecap.CapabilityBrowserDownload,
			"browser_act",
			toolStates,
			browserReason,
		)
	}
	capabilities = append(capabilities, download)
	return capabilities
}

func configuredCapabilityForTool(
	capability runtimecap.CapabilityID,
	toolName string,
	states map[string]skills.SkillRequirementState,
	missingReason runtimecap.UnavailableReasonCode,
) runtimecap.Availability {
	return configuredCapabilityForState(capability, states[toolName], missingReason)
}

func configuredCapabilityForState(
	capability runtimecap.CapabilityID,
	state skills.SkillRequirementState,
	missingReason runtimecap.UnavailableReasonCode,
) runtimecap.Availability {
	switch state {
	case skills.SkillRequirementAvailable:
		return runtimecap.Available(capability)
	case skills.SkillRequirementPolicyDisabled:
		return runtimecap.Unavailable(capability, runtimecap.ReasonPolicyDisabled)
	default:
		return runtimecap.Unavailable(capability, missingReason)
	}
}

func configuredCodingBrowserAuthorityOperations(cfg *config.Config) []map[string]struct{} {
	if cfg == nil || !cfg.Coding.Capabilities.Browser || !cfg.Coding.Remote.Enabled {
		return nil
	}
	grant, ok := cfg.Execution.CodingRemoteGrants[cfg.Coding.Remote.Grant]
	if !ok {
		return nil
	}
	authorities := make([]map[string]struct{}, 0, len(grant.Capabilities))
	for _, alias := range grant.Capabilities {
		capability, exists := cfg.Execution.CodingRemoteCapabilities[alias]
		if !exists || capability.Kind != config.CodingRemoteCapabilityBrowser {
			continue
		}
		operations := make(map[string]struct{}, len(capability.Operations))
		for _, operation := range capability.Operations {
			operations[operation] = struct{}{}
		}
		authorities = append(authorities, operations)
	}
	return authorities
}

func configuredCodingBrowserToolState(
	base skills.SkillRequirementState,
	authorities []map[string]struct{},
	required ...string,
) skills.SkillRequirementState {
	if base != skills.SkillRequirementAvailable {
		return base
	}
	if len(required) == 0 {
		return skills.SkillRequirementMissing
	}
	for _, operations := range authorities {
		if slices.ContainsFunc(required, func(operation string) bool {
			_, ok := operations[operation]
			return !ok
		}) {
			continue
		}
		return skills.SkillRequirementAvailable
	}
	return skills.SkillRequirementMissing
}

func containsExactString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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
