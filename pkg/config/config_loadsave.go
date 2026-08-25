package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
	"gopkg.in/yaml.v3"

	"github.com/bogdanovich/mintclaw/pkg"
	"github.com/bogdanovich/mintclaw/pkg/fileutil"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

func LoadConfig(path string) (*Config, error) {
	snapshot, err := NewRepository(path).ReadOnly()
	if err != nil {
		return nil, err
	}
	return snapshot.Config, nil
}

// LoadConfigReadOnly loads and validates the current configuration without
// applying repository transaction recovery.
func LoadConfigReadOnly(path string) (*Config, error) {
	return loadConfigReadOnly(path, true)
}

func loadConfigForUpdate(path string) (*Config, error) {
	return loadConfigReadOnly(path, false)
}

func loadConfigReadOnly(path string, applyRuntimeOverrides bool) (*Config, error) {
	updateResolver(filepath.Dir(path))

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			logger.WarnF(
				"config file not found, using default config",
				map[string]any{"path": path},
			)
			return DefaultConfig(), nil
		}
		return nil, err
	}

	cfg, err := loadConfig(data)
	if err != nil {
		logger.ErrorCF(
			"config",
			formatDiagnosticLogMessage("Failed to load config", err),
			map[string]any{"path": path},
		)
		return nil, err
	}

	secPath := securityPath(path)
	if err = loadSecurityConfig(cfg, secPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("failed to load security config: %w", err)
	}

	if err = finalizeLoadedConfig(cfg, applyRuntimeOverrides); err != nil {
		return nil, err
	}

	return cfg, nil
}

func loadConfig(data []byte) (*Config, error) {
	return decodeCurrentConfigWithDefaults(data, "config.json")
}

func decodeCurrentConfigWithDefaults(data []byte, label string) (*Config, error) {
	cfg := DefaultConfig()
	// Go's JSON decoder reuses existing slice elements. Decode once into an
	// empty value so user-supplied model entries cannot inherit default fields.
	var provided Config
	if err := decodeCurrentConfig(data, &provided, label); err != nil {
		return nil, err
	}
	if len(provided.ModelList) > 0 {
		cfg.ModelList = nil
	}
	if provided.Agents.List != nil {
		cfg.Agents.List = nil
	}
	if err := decodeCurrentConfig(data, cfg, label); err != nil {
		return nil, err
	}
	if err := cfg.ValidateAgents(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// DecodeCurrentConfig decodes a configuration document using the same defaults
// and strict schema rules as the file loader.
func DecodeCurrentConfig(data []byte, target *Config) error {
	if target == nil {
		return errors.New("config decode target must not be nil")
	}
	decoded, err := decodeCurrentConfigWithDefaults(data, "config")
	if err != nil {
		return err
	}
	*target = *decoded
	return nil
}

func decodeCurrentConfig(data []byte, target *Config, label string) error {
	if target == nil {
		return errors.New("config decode target must not be nil")
	}
	if err := decodeJSONWithDiagnostics(data, target, label); err != nil {
		return err
	}
	if target.Version != CurrentVersion {
		return fmt.Errorf(
			"unsupported config version: %d; current version is %d",
			target.Version,
			CurrentVersion,
		)
	}
	return nil
}

func finalizeLoadedConfig(cfg *Config, applyRuntimeOverrides bool) error {
	gatewayHostBeforeEnv := cfg.Gateway.Host
	if applyRuntimeOverrides {
		if err := env.Parse(cfg); err != nil {
			return err
		}
		applySkillsRegistryEnvOverrides(cfg)
	}
	if err := initChannelList(cfg.Channels, applyRuntimeOverrides); err != nil {
		return err
	}
	if err := cfg.ValidateTurnProfile(); err != nil {
		return err
	}
	if err := cfg.ValidateExecConfig(); err != nil {
		return err
	}
	if err := cfg.ValidateToolApprovalConfig(); err != nil {
		return err
	}
	if err := cfg.ValidateRequestUserInputConfig(); err != nil {
		return err
	}
	if err := cfg.ValidateExecutionTargets(); err != nil {
		return err
	}
	if err := cfg.ValidateMCPConfig(); err != nil {
		return err
	}
	if err := cfg.ValidateBrowserConfig(); err != nil {
		return err
	}
	if err := cfg.Tools.ResultRetention.Validate(); err != nil {
		return fmt.Errorf("invalid tools.result_retention: %w", err)
	}
	if err := cfg.Agents.Defaults.validateContextManagerSelection(); err != nil {
		return err
	}
	if err := cfg.Agents.Defaults.validateResultRetentionOwnership(); err != nil {
		return err
	}
	if err := cfg.Agents.Defaults.PromptMemory.Validate(); err != nil {
		return err
	}
	if err := cfg.Session.Lifecycle.Validate(); err != nil {
		return err
	}
	if applyRuntimeOverrides {
		gatewayHost, err := resolveGatewayHostFromEnv(gatewayHostBeforeEnv)
		if err != nil {
			return fmt.Errorf("invalid gateway host: %w", err)
		}
		cfg.Gateway.Host = gatewayHost
	}
	if applyRuntimeOverrides {
		cfg.ModelList = expandMultiKeyModels(cfg.ModelList)
	}
	if err := cfg.ValidateModelList(); err != nil {
		return err
	}
	if err := cfg.ValidateModelReferences(); err != nil {
		return err
	}
	if cfg.Agents.Defaults.Workspace == "" {
		cfg.Agents.Defaults.Workspace = filepath.Join(GetHome(), pkg.WorkspaceName)
	}
	return nil
}

func applySkillsRegistryEnvOverrides(cfg *Config) {
	if cfg == nil {
		return
	}

	registryCfg, applyClawHub := registryForEnvOverrides(
		&cfg.Tools.Skills.Registries,
		"clawhub",
		envSkillsClawHubEnabled,
		envSkillsClawHubBaseURL,
		envSkillsClawHubAuthToken,
		envSkillsClawHubSearchPath,
		envSkillsClawHubSkillsPath,
		envSkillsClawHubDownloadPath,
		envSkillsClawHubTimeout,
		envSkillsClawHubMaxZipSize,
		envSkillsClawHubMaxResponseSize,
	)

	if applyClawHub {
		if raw, envSet := os.LookupEnv(envSkillsClawHubEnabled); envSet {
			if value, err := strconv.ParseBool(strings.TrimSpace(raw)); err == nil {
				registryCfg.Enabled = value
			}
		}
		if value, envSet := os.LookupEnv(envSkillsClawHubBaseURL); envSet {
			registryCfg.BaseURL = value
		}
		if value, envSet := os.LookupEnv(envSkillsClawHubAuthToken); envSet {
			registryCfg.AuthToken = *NewSecureString(value)
		}
		if value, envSet := os.LookupEnv(envSkillsClawHubSearchPath); envSet {
			registryCfg.Param["search_path"] = value
		}
		if value, envSet := os.LookupEnv(envSkillsClawHubSkillsPath); envSet {
			registryCfg.Param["skills_path"] = value
		}
		if value, envSet := os.LookupEnv(envSkillsClawHubDownloadPath); envSet {
			registryCfg.Param["download_path"] = value
		}
		if raw, envSet := os.LookupEnv(envSkillsClawHubTimeout); envSet {
			if value, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
				registryCfg.Param["timeout"] = value
			}
		}
		if raw, envSet := os.LookupEnv(envSkillsClawHubMaxZipSize); envSet {
			if value, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
				registryCfg.Param["max_zip_size"] = value
			}
		}
		if raw, envSet := os.LookupEnv(envSkillsClawHubMaxResponseSize); envSet {
			if value, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
				registryCfg.Param["max_response_size"] = value
			}
		}
		cfg.Tools.Skills.Registries.Set("clawhub", registryCfg)
	}

	githubCfg, applyGitHub := registryForEnvOverrides(
		&cfg.Tools.Skills.Registries,
		"github",
		envSkillsGitHubEnabled,
		envSkillsGitHubBaseURL,
		envSkillsGitHubAuthToken,
		envSkillsGitHubProxy,
	)

	if applyGitHub {
		if raw, envSet := os.LookupEnv(envSkillsGitHubEnabled); envSet {
			if value, err := strconv.ParseBool(strings.TrimSpace(raw)); err == nil {
				githubCfg.Enabled = value
			}
		}
		if value, envSet := os.LookupEnv(envSkillsGitHubBaseURL); envSet {
			githubCfg.BaseURL = value
		}
		if value, envSet := os.LookupEnv(envSkillsGitHubAuthToken); envSet {
			githubCfg.AuthToken = *NewSecureString(value)
		}
		if value, envSet := os.LookupEnv(envSkillsGitHubProxy); envSet {
			githubCfg.Param["proxy"] = value
		}
		cfg.Tools.Skills.Registries.Set("github", githubCfg)
	}
}

func registryForEnvOverrides(
	registries *SkillsRegistriesConfig,
	name string,
	envNames ...string,
) (SkillRegistryConfig, bool) {
	registry, configured := registries.Get(name)
	if !configured {
		envSet := false
		for _, envName := range envNames {
			if _, envSet = os.LookupEnv(envName); envSet {
				break
			}
		}
		if !envSet {
			return SkillRegistryConfig{}, false
		}
		registry, _ = DefaultConfig().Tools.Skills.Registries.Get(name)
	}
	registry.Param = cloneRegistryParams(registry.Param)
	if registry.Param == nil {
		registry.Param = map[string]any{}
	}
	return registry, true
}

func MakeBackup(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	dateSuffix := time.Now().Format(".20060102.bak")
	// Backup config file
	bakPath := path + dateSuffix
	if err := fileutil.CopyFile(path, bakPath, 0o600); err != nil {
		logger.ErrorF("failed to create config backup", map[string]any{"error": err})
		return fmt.Errorf("failed to create config backup: %w", err)
	}
	// Backup security config file
	secPath := securityPath(path)
	if _, err := os.Stat(secPath); err == nil {
		secBakPath := secPath + dateSuffix
		if secErr := fileutil.CopyFile(secPath, secBakPath, 0o600); secErr != nil {
			logger.ErrorF("failed to create security backup", map[string]any{"error": secErr})
			return fmt.Errorf("failed to create security backup: %w", secErr)
		}
	}
	return nil
}

func toNameIndex(list []*ModelConfig) []string {
	nameList := make([]string, 0, len(list))
	countMap := make(map[string]int)
	for _, model := range list {
		name := model.ModelName
		index := countMap[name]
		nameList = append(nameList, fmt.Sprintf("%s:%d", name, index))
		countMap[name]++
	}
	return nameList
}

func SaveConfig(path string, cfg *Config) error {
	return writeConfigDocuments(path, cfg)
}

func (c *Config) WorkspacePath() string {
	return fileutil.ExpandHome(c.Agents.Defaults.Workspace)
}

// GetModelConfig returns an enabled ModelConfig for the given model name.
// If multiple configs exist with the same model_name, it uses round-robin
// selection for load balancing. Returns an error if the model is not found.
func (c *Config) GetModelConfig(modelName string) (*ModelConfig, error) {
	matches := c.findMatches(modelName)
	if len(matches) == 0 {
		return nil, fmt.Errorf("model %q not found in model_list", modelName)
	}
	if len(matches) == 1 {
		return matches[0], nil
	}

	// Multiple configs - use round-robin for load balancing
	idx := (rrCounter.Add(1) - 1) % uint64(len(matches))
	return matches[idx], nil
}

// findMatches finds all ModelConfig entries with the given model_name.
func (c *Config) findMatches(modelName string) []*ModelConfig {
	var matches []*ModelConfig
	for i := range c.ModelList {
		if c.ModelList[i] != nil && c.ModelList[i].Enabled && c.ModelList[i].ModelName == modelName {
			matches = append(matches, c.ModelList[i])
		}
	}
	return matches
}

// ValidateModelList validates all ModelConfig entries in the model_list.
// It checks that each model config is valid.
// Note: Multiple entries with the same model_name are allowed for load balancing.
func (c *Config) ValidateModelList() error {
	for i := range c.ModelList {
		if err := c.ModelList[i].Validate(); err != nil {
			return fmt.Errorf("model_list[%d]: %w", i, err)
		}
	}
	return nil
}

func (c *Config) ValidateExecConfig() error {
	mode := strings.TrimSpace(strings.ToLower(c.Tools.Exec.PermissionMode))
	switch mode {
	case "", "read_only":
		c.Tools.Exec.PermissionMode = mode
		return nil
	default:
		return fmt.Errorf(
			"tools.exec.permission_mode: unsupported value %q (allowed: \"\", \"read_only\")",
			c.Tools.Exec.PermissionMode,
		)
	}
}

func (c *Config) ValidateToolApprovalConfig() error {
	if c == nil {
		return nil
	}
	mode := c.Tools.Approval.EffectiveMode()
	switch mode {
	case ToolApprovalModeRequired, ToolApprovalModeAllowAll:
		c.Tools.Approval.Mode = mode
	default:
		return fmt.Errorf(
			"tools.approval.mode: unsupported value %q (allowed: %q, %q)",
			c.Tools.Approval.Mode,
			ToolApprovalModeRequired,
			ToolApprovalModeAllowAll,
		)
	}
	if mode == ToolApprovalModeAllowAll && len(c.Tools.Approval.BypassNodeTargets) > 0 {
		return errors.New("tools.approval.bypass_node_targets cannot be set when mode is allow_all")
	}
	seen := make(map[string]struct{}, len(c.Tools.Approval.BypassNodeTargets))
	for _, target := range c.Tools.Approval.BypassNodeTargets {
		if !validExecutionTargetName(target) {
			return fmt.Errorf("tools.approval.bypass_node_targets contains invalid target %q", target)
		}
		if _, exists := c.Execution.Targets[target]; !exists {
			return fmt.Errorf("tools.approval.bypass_node_targets references unknown target %q", target)
		}
		if _, duplicate := seen[target]; duplicate {
			return fmt.Errorf("tools.approval.bypass_node_targets contains duplicate target %q", target)
		}
		seen[target] = struct{}{}
	}
	return nil
}

func (c *Config) ValidateRequestUserInputConfig() error {
	if c == nil {
		return nil
	}
	cfg := c.Tools.RequestUserInput
	defaultTimeout := int(cfg.DefaultTimeout() / time.Second)
	maxTimeout := int(cfg.MaxTimeout() / time.Second)
	retentionHours := int(cfg.Retention() / time.Hour)
	if defaultTimeout < 60 || defaultTimeout > 86400 {
		return fmt.Errorf(
			"tools.request_user_input.default_timeout_seconds must be between 60 and 86400",
		)
	}
	if maxTimeout < defaultTimeout || maxTimeout > 86400 {
		return fmt.Errorf(
			"tools.request_user_input.max_timeout_seconds must be between default timeout and 86400",
		)
	}
	if retentionHours < 1 || retentionHours > 8760 {
		return fmt.Errorf("tools.request_user_input.retention_hours must be between 1 and 8760")
	}
	return nil
}

func (c *Config) SecurityCopyFrom(path string) error {
	return loadSecurityConfig(c, securityPath(path))
}

// SecurityCopyForReplacement preserves security fields while replacing the
// public config. Security entries for removed registries are admitted only
// while the existing overlay is decoded and are not copied into the result.
func (c *Config) SecurityCopyForReplacement(path string, current *Config) error {
	if c == nil {
		return errors.New("config is nil")
	}
	replacementChannelSecurity, err := marshalReplacementChannelSecurity(c.Channels)
	if err != nil {
		return fmt.Errorf("preserve replacement channel security: %w", err)
	}
	if c.Tools.Skills.Registries == nil {
		c.Tools.Skills.Registries = DefaultConfig().Tools.Skills.Registries
	}
	removedRegistries := make([]string, 0)
	matchingChannels := make(map[string]struct{})
	if current != nil {
		for _, name := range current.Tools.Skills.Registries.Names() {
			if _, survives := c.Tools.Skills.Registries.Get(name); survives {
				continue
			}
			c.Tools.Skills.Registries.Set(name, SkillRegistryConfig{})
			removedRegistries = append(removedRegistries, name)
		}
		for name, durable := range current.Channels {
			replacement := c.Channels.Get(name)
			if durable != nil && replacement != nil &&
				effectiveChannelType(name, durable.Type) == effectiveChannelType(name, replacement.Type) {
				matchingChannels[name] = struct{}{}
			}
		}
	}

	err = loadSecurityConfigForChannels(c, securityPath(path), matchingChannels)
	for _, name := range removedRegistries {
		delete(c.Tools.Skills.Registries, name)
	}
	if err != nil {
		return err
	}
	if replacementChannelSecurity != nil {
		if err = c.Channels.UnmarshalYAML(replacementChannelSecurity); err != nil {
			return fmt.Errorf("restore replacement channel security: %w", err)
		}
	}
	return nil
}

func marshalReplacementChannelSecurity(channels ChannelsConfig) (*yaml.Node, error) {
	if len(channels) == 0 {
		return nil, nil
	}
	typedChannels := make(ChannelsConfig, len(channels))
	for name, channel := range channels {
		if channel == nil {
			continue
		}
		copy := *channel
		copy.Type = effectiveChannelType(name, copy.Type)
		typedChannels[name] = &copy
	}
	var node yaml.Node
	if err := node.Encode(typedChannels); err != nil {
		return nil, err
	}
	return &node, nil
}

func expandMultiKeyModels(models []*ModelConfig) []*ModelConfig {
	var expanded []*ModelConfig

	for _, m := range models {
		// Dormant entries do not participate in runtime selection, so keep their
		// persisted shape instead of manufacturing disabled fallback aliases.
		if m == nil || !m.Enabled {
			expanded = append(expanded, m)
			continue
		}
		keys := m.APIKeys.Values()

		// Single key or no keys: keep as-is
		if len(keys) <= 1 {
			expanded = append(expanded, m)
			continue
		}

		// Multiple keys: expand
		originalName := m.ModelName

		// Create entries for additional keys (key_1, key_2, ...)
		var fallbackNames []string
		for i := 1; i < len(keys); i++ {
			suffix := fmt.Sprintf("__key_%d", i)
			expandedName := originalName + suffix

			additionalEntry := *m
			additionalEntry.ModelName = expandedName
			additionalEntry.APIKeys = SimpleSecureStrings(keys[i])
			additionalEntry.Fallbacks = nil
			additionalEntry.isVirtual = true
			expanded = append(expanded, &additionalEntry)
			fallbackNames = append(fallbackNames, expandedName)
		}

		primaryEntry := *m
		primaryEntry.APIKeys = SimpleSecureStrings(keys[0])
		primaryEntry.Fallbacks = append(fallbackNames, m.Fallbacks...)
		primaryEntry.isVirtual = false
		expanded = append(expanded, &primaryEntry)
	}

	return expanded
}

func (t *ToolsConfig) IsToolEnabled(name string) bool {
	switch name {
	case "web":
		return t.Web.Enabled
	case "cron":
		return t.Cron.Enabled
	case "exec":
		return t.Exec.Enabled
	case "skills":
		return t.Skills.Enabled
	case "media_cleanup":
		return t.MediaCleanup.Enabled
	case "append_file":
		return t.AppendFile.Enabled
	case "apply_patch":
		return t.ApplyPatch.Enabled
	case "find_skills":
		return t.FindSkills.Enabled
	case "i2c":
		return t.I2C.Enabled
	case "image_generate":
		return t.ImageGenerate.Enabled
	case "install_skill":
		return t.InstallSkill.Enabled
	case "list_dir":
		return t.ListDir.Enabled
	case "load_image":
		return t.LoadImage.Enabled
	case "memory":
		return t.Memory.Enabled
	case "message":
		return t.Message.Enabled
	case "read_file":
		return t.ReadFile.Enabled
	case "request_user_input":
		return t.RequestUserInput.Enabled
	case "serial":
		return t.Serial.Enabled
	case "search_files":
		return t.SearchFiles.Enabled
	case "spawn":
		return t.Spawn.Enabled
	case "spi":
		return t.SPI.Enabled
	case "subagent":
		return t.Subagent.Enabled
	case "update_plan":
		return t.UpdatePlan.Enabled
	case "web_fetch":
		return t.WebFetch.Enabled
	case "send_file":
		return t.SendFile.Enabled
	case "send_tts":
		return t.SendTTS.Enabled
	case "write_file":
		return t.WriteFile.Enabled
	case "mcp":
		return t.MCP.Enabled
	default:
		return true
	}
}
