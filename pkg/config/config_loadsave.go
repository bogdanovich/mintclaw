package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"

	"github.com/bogdanovich/mintclaw/pkg"
	"github.com/bogdanovich/mintclaw/pkg/fileutil"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

func LoadConfig(path string) (*Config, error) {
	repository := NewRepository(path)
	var cfg *Config
	err := repository.withLock(func() error {
		if _, recoverErr := repository.recoverLocked(); recoverErr != nil {
			return recoverErr
		}
		var loadErr error
		cfg, loadErr = loadConfigWithMigration(path, func(documents configDocuments) error {
			_, saveErr := repository.saveDocumentsLocked(documents)
			return saveErr
		})
		return loadErr
	})
	return cfg, err
}

func loadConfigWithMigration(path string, persistMigration func(configDocuments) error) (*Config, error) {
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

	// First, try to detect config version by reading the version field
	var versionInfo struct {
		Version int `json:"version"`
	}
	if e := json.Unmarshal(data, &versionInfo); e != nil {
		e = wrapJSONError(data, e, "config.json")
		logger.ErrorCF(
			"config",
			formatDiagnosticLogMessage("Malformed config file", e),
			map[string]any{"path": path},
		)
		return nil, e
	}
	if len(data) <= 10 {
		logger.Warn(fmt.Sprintf("content is [%s]", string(data)))
		return DefaultConfig(), nil
	}

	data, err = removeDeprecatedConfigFields(data)
	if err != nil {
		return nil, fmt.Errorf("failed to migrate deprecated config fields: %w", err)
	}

	// Load config based on detected version
	var cfg *Config
	migrationFrom := -1
	switch versionInfo.Version {
	case 0:
		migrationFrom = versionInfo.Version
		logger.InfoF(
			"config migrate start",
			map[string]any{"from": versionInfo.Version, "to": CurrentVersion},
		)
		if err = validateLegacyConfigDiagnostics(data); err != nil {
			logger.ErrorCF(
				"config",
				formatDiagnosticLogMessage("Failed to load config", err),
				map[string]any{"path": path},
			)
			return nil, err
		}

		var m map[string]any
		m, err = loadConfigMapData(path, data)
		if err != nil {
			logger.ErrorCF(
				"config",
				formatDiagnosticLogMessage("Failed to load config", err),
				map[string]any{"path": path},
			)
			return nil, err
		}

		migrateErr := migrateV0ToV1(m)
		if migrateErr != nil {
			return nil, fmt.Errorf("V0→V1 migration failed: %w", migrateErr)
		}
		migrateErr = migrateV1ToV2(m)
		if migrateErr != nil {
			return nil, fmt.Errorf("V1→V2 migration failed: %w", migrateErr)
		}
		migrateErr = migrateV2ToV3(m)
		if migrateErr != nil {
			return nil, fmt.Errorf("V2→V3 migration failed: %w", migrateErr)
		}

		var migrated []byte
		migrated, err = json.Marshal(m)
		if err != nil {
			return nil, err
		}

		cfg, err = loadConfig(migrated)
		if err != nil {
			return nil, err
		}

	case 1:
		migrationFrom = versionInfo.Version
		// V1→V3 migration: rename channels→channel_list, infer Enabled, migrate channel configs
		logger.InfoF(
			"config migrate start",
			map[string]any{"from": versionInfo.Version, "to": CurrentVersion},
		)
		if err = validateLegacyConfigDiagnostics(data); err != nil {
			logger.ErrorCF(
				"config",
				formatDiagnosticLogMessage("Failed to load config", err),
				map[string]any{"path": path},
			)
			return nil, err
		}

		var m map[string]any
		m, err = loadConfigMapData(path, data)
		if err != nil {
			logger.ErrorCF(
				"config",
				formatDiagnosticLogMessage("Failed to load config", err),
				map[string]any{"path": path},
			)
			return nil, err
		}

		migrateErr := migrateV1ToV2(m)
		if migrateErr != nil {
			return nil, fmt.Errorf("V1→V2 migration failed: %w", migrateErr)
		}
		migrateErr = migrateV2ToV3(m)
		if migrateErr != nil {
			return nil, fmt.Errorf("V2→V3 migration failed: %w", migrateErr)
		}

		var migrated []byte
		migrated, err = json.Marshal(m)
		if err != nil {
			return nil, err
		}

		cfg, err = loadConfig(migrated)
		if err != nil {
			return nil, err
		}

	case 2:
		migrationFrom = versionInfo.Version
		// V2→V3 migration: rename channels→channel_list, convert flat→nested
		logger.InfoF(
			"config migrate start",
			map[string]any{"from": versionInfo.Version, "to": CurrentVersion},
		)
		if err = validateLegacyConfigDiagnostics(data); err != nil {
			logger.ErrorCF(
				"config",
				formatDiagnosticLogMessage("Failed to load config", err),
				map[string]any{"path": path},
			)
			return nil, err
		}
		var m map[string]any
		m, err = loadConfigMapData(path, data)
		if err != nil {
			logger.ErrorCF(
				"config",
				formatDiagnosticLogMessage("Failed to load config", err),
				map[string]any{"path": path},
			)
			return nil, err
		}
		migrateErr := migrateV2ToV3(m)
		if migrateErr != nil {
			return nil, fmt.Errorf("V2→V3 migration failed: %w", migrateErr)
		}

		var migrated []byte
		migrated, err = json.Marshal(m)
		if err != nil {
			return nil, err
		}

		cfg, err = loadConfig(migrated)
		if err != nil {
			return nil, err
		}

	case CurrentVersion:
		// Current version
		cfg, err = loadConfig(data)
		if err != nil {
			logger.ErrorCF(
				"config",
				formatDiagnosticLogMessage("Failed to load config", err),
				map[string]any{"path": path},
			)
			return nil, err
		}
		// Load security configuration
		secPath := securityPath(path)
		err = loadSecurityConfig(cfg, secPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("failed to load security config: %w", err)
		}

	default:
		return nil, fmt.Errorf("unsupported config version: %d", versionInfo.Version)
	}

	applyLegacyBindingsMigration(data, cfg)
	var migrationDocuments configDocuments
	if migrationFrom >= 0 {
		if err = initChannelList(cfg.Channels, false); err != nil {
			return nil, err
		}
		migrationDocuments, err = marshalConfigDocuments(cfg)
		if err != nil {
			return nil, fmt.Errorf("prepare migrated configuration: %w", err)
		}
	}

	if err = finalizeLoadedConfig(cfg, true); err != nil {
		return nil, err
	}

	if migrationFrom >= 0 {
		if err = MakeBackup(path); err != nil {
			return nil, err
		}
		if err = persistMigration(migrationDocuments); err != nil {
			return nil, fmt.Errorf("persist migrated configuration: %w", err)
		}
		logger.InfoF(
			"config migrate success",
			map[string]any{"from": migrationFrom, "to": CurrentVersion},
		)
	}

	return cfg, nil
}

// LoadConfigReadOnly loads configuration without creating backups, migrating files,
// saving config/security documents, or otherwise mutating local state.
//
// It intentionally preserves LoadConfig behavior for callers that expect automatic
// migration persistence; new read-only callers should use this helper instead.
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

	var versionInfo struct {
		Version int `json:"version"`
	}
	if e := json.Unmarshal(data, &versionInfo); e != nil {
		e = wrapJSONError(data, e, "config.json")
		logger.ErrorCF(
			"config",
			formatDiagnosticLogMessage("Malformed config file", e),
			map[string]any{"path": path},
		)
		return nil, e
	}
	if len(data) <= 10 {
		logger.Warn(fmt.Sprintf("content is [%s]", string(data)))
		return DefaultConfig(), nil
	}

	data, err = removeDeprecatedConfigFields(data)
	if err != nil {
		return nil, fmt.Errorf("failed to migrate deprecated config fields: %w", err)
	}

	var cfg *Config
	switch versionInfo.Version {
	case 0:
		if err = validateLegacyConfigDiagnostics(data); err != nil {
			logger.ErrorCF(
				"config",
				formatDiagnosticLogMessage("Failed to load config", err),
				map[string]any{"path": path},
			)
			return nil, err
		}
		var m map[string]any
		m, err = loadConfigMapData(path, data)
		if err != nil {
			logger.ErrorCF(
				"config",
				formatDiagnosticLogMessage("Failed to load config", err),
				map[string]any{"path": path},
			)
			return nil, err
		}
		if migrateErr := migrateV0ToV1(m); migrateErr != nil {
			return nil, fmt.Errorf("V0→V1 migration failed: %w", migrateErr)
		}
		if migrateErr := migrateV1ToV2(m); migrateErr != nil {
			return nil, fmt.Errorf("V1→V2 migration failed: %w", migrateErr)
		}
		if migrateErr := migrateV2ToV3(m); migrateErr != nil {
			return nil, fmt.Errorf("V2→V3 migration failed: %w", migrateErr)
		}
		var migrated []byte
		migrated, err = json.Marshal(m)
		if err != nil {
			return nil, err
		}
		cfg, err = loadConfig(migrated)
	case 1:
		if err = validateLegacyConfigDiagnostics(data); err != nil {
			logger.ErrorCF(
				"config",
				formatDiagnosticLogMessage("Failed to load config", err),
				map[string]any{"path": path},
			)
			return nil, err
		}
		var m map[string]any
		m, err = loadConfigMapData(path, data)
		if err != nil {
			logger.ErrorCF(
				"config",
				formatDiagnosticLogMessage("Failed to load config", err),
				map[string]any{"path": path},
			)
			return nil, err
		}
		if migrateErr := migrateV1ToV2(m); migrateErr != nil {
			return nil, fmt.Errorf("V1→V2 migration failed: %w", migrateErr)
		}
		if migrateErr := migrateV2ToV3(m); migrateErr != nil {
			return nil, fmt.Errorf("V2→V3 migration failed: %w", migrateErr)
		}
		var migrated []byte
		migrated, err = json.Marshal(m)
		if err != nil {
			return nil, err
		}
		cfg, err = loadConfig(migrated)
	case 2:
		if err = validateLegacyConfigDiagnostics(data); err != nil {
			logger.ErrorCF(
				"config",
				formatDiagnosticLogMessage("Failed to load config", err),
				map[string]any{"path": path},
			)
			return nil, err
		}
		var m map[string]any
		m, err = loadConfigMapData(path, data)
		if err != nil {
			logger.ErrorCF(
				"config",
				formatDiagnosticLogMessage("Failed to load config", err),
				map[string]any{"path": path},
			)
			return nil, err
		}
		if migrateErr := migrateV2ToV3(m); migrateErr != nil {
			return nil, fmt.Errorf("V2→V3 migration failed: %w", migrateErr)
		}
		var migrated []byte
		migrated, err = json.Marshal(m)
		if err != nil {
			return nil, err
		}
		cfg, err = loadConfig(migrated)
	case CurrentVersion:
		cfg, err = loadConfig(data)
	default:
		return nil, fmt.Errorf("unsupported config version: %d", versionInfo.Version)
	}
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

	applyLegacyBindingsMigration(data, cfg)

	if err = finalizeLoadedConfig(cfg, applyRuntimeOverrides); err != nil {
		return nil, err
	}

	return cfg, nil
}

func finalizeLoadedConfig(cfg *Config, applyRuntimeOverrides bool) error {
	gatewayHostBeforeEnv := cfg.Gateway.Host
	if applyRuntimeOverrides {
		if err := env.Parse(cfg); err != nil {
			return err
		}
		applySkillsRegistryEnvCompat(cfg)
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
	cfg.ModelList = expandMultiKeyModels(cfg.ModelList)
	if err := cfg.ValidateModelList(); err != nil {
		return err
	}
	if cfg.Agents.Defaults.Workspace == "" {
		cfg.Agents.Defaults.Workspace = filepath.Join(GetHome(), pkg.WorkspaceName)
	}
	cfg.Session.ApplyDmScope()
	cfg.Session.DeriveDmScope()
	return nil
}

func applySkillsRegistryEnvCompat(cfg *Config) {
	if cfg == nil {
		return
	}

	registryCfg, foundClawHub := cfg.Tools.Skills.Registries.Get("clawhub")
	if !foundClawHub {
		registryCfg = SkillRegistryConfig{
			Name:  "clawhub",
			Param: map[string]any{},
		}
	}
	if registryCfg.Param == nil {
		registryCfg.Param = map[string]any{}
	}

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

	githubCfg, foundGitHub := cfg.Tools.Skills.Registries.Get("github")
	if !foundGitHub {
		githubCfg = SkillRegistryConfig{
			Name:  "github",
			Param: map[string]any{},
		}
	}
	if githubCfg.Param == nil {
		githubCfg.Param = map[string]any{}
	}

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

// GetModelConfig returns the ModelConfig for the given model name.
// If multiple configs exist with the same model_name, it uses round-robin
// selection for load balancing. Returns an error if the model is not found.
func (c *Config) GetModelConfig(modelName string) (*ModelConfig, error) {
	matches := c.findMatches(modelName)
	if len(matches) == 0 {
		return nil, fmt.Errorf("model %q not found in model_list or providers", modelName)
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
		if c.ModelList[i].ModelName == modelName {
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

// ResetToDefaults backs up the current config, creates a default config,
// preserves security credentials from the existing config, and saves it.
func ResetToDefaults(configPath string) error {
	if err := MakeBackup(configPath); err != nil {
		return fmt.Errorf("backup before reset: %w", err)
	}
	cfg := DefaultConfig()
	cfg.Session.ApplyDmScope()
	cfg.Session.DeriveDmScope()
	if err := cfg.SecurityCopyFrom(configPath); err != nil {
		logger.WarnF("could not preserve security config", map[string]any{"error": err})
	}
	return SaveConfig(configPath, cfg)
}

func expandMultiKeyModels(models []*ModelConfig) []*ModelConfig {
	var expanded []*ModelConfig

	for _, m := range models {
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

			// Create a copy for the additional key
			additionalEntry := &ModelConfig{
				ModelName:           expandedName,
				Provider:            m.Provider,
				Model:               m.Model,
				APIBase:             m.APIBase,
				APIKeys:             SimpleSecureStrings(keys[i]),
				Proxy:               m.Proxy,
				AuthMethod:          m.AuthMethod,
				ConnectMode:         m.ConnectMode,
				Workspace:           m.Workspace,
				RPM:                 m.RPM,
				MaxTokensField:      m.MaxTokensField,
				RequestTimeout:      m.RequestTimeout,
				ThinkingLevel:       m.ThinkingLevel,
				ToolSchemaTransform: m.ToolSchemaTransform,
				Streaming:           m.Streaming,
				ExtraBody:           m.ExtraBody,
				CustomHeaders:       m.CustomHeaders,
				Capabilities:        m.Capabilities,
				UserAgent:           m.UserAgent,
				isVirtual:           true,
			}
			expanded = append(expanded, additionalEntry)
			fallbackNames = append(fallbackNames, expandedName)
		}

		// Create the primary entry with first key and fallbacks
		primaryEntry := &ModelConfig{
			ModelName:           originalName,
			Provider:            m.Provider,
			Model:               m.Model,
			APIBase:             m.APIBase,
			Proxy:               m.Proxy,
			AuthMethod:          m.AuthMethod,
			ConnectMode:         m.ConnectMode,
			Workspace:           m.Workspace,
			RPM:                 m.RPM,
			MaxTokensField:      m.MaxTokensField,
			RequestTimeout:      m.RequestTimeout,
			ThinkingLevel:       m.ThinkingLevel,
			ToolSchemaTransform: m.ToolSchemaTransform,
			Streaming:           m.Streaming,
			ExtraBody:           m.ExtraBody,
			CustomHeaders:       m.CustomHeaders,
			Capabilities:        m.Capabilities,
			UserAgent:           m.UserAgent,
			APIKeys:             SimpleSecureStrings(keys[0]),
		}

		// Prepend new fallbacks to existing ones
		if len(fallbackNames) > 0 {
			primaryEntry.Fallbacks = append(fallbackNames, m.Fallbacks...)
		} else if len(m.Fallbacks) > 0 {
			primaryEntry.Fallbacks = m.Fallbacks
		}

		expanded = append(expanded, primaryEntry)
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
	case "spawn_status":
		return t.SpawnStatus.Enabled
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
