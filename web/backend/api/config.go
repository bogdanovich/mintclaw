package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

// registerConfigRoutes binds configuration management endpoints to the ServeMux.
func (h *Handler) registerConfigRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/config", h.handleGetConfig)
	mux.HandleFunc("PUT /api/config", h.handleUpdateConfig)
	mux.HandleFunc("PATCH /api/config", h.handlePatchConfig)
	mux.HandleFunc("POST /api/config/reset", h.handleResetConfig)
	mux.HandleFunc("POST /api/config/test-command-patterns", h.handleTestCommandPatterns)
}

func (h *Handler) applyRuntimeLogLevel() {
	if h.debug {
		logger.SetLevel(logger.DEBUG)
		return
	}
	logger.SetLevelFromString(config.ResolveGatewayLogLevel(h.configPath))
}

// handleGetConfig returns the complete system configuration.
//
//	GET /api/config
func (h *Handler) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	snapshot, err := h.readConfigSnapshot()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	publicCfg, err := config.ProjectPublicConfig(snapshot.Config)
	if err != nil {
		http.Error(w, "Failed to project response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeConfigRevision(w, snapshot.Revision)
	if err := json.NewEncoder(w).Encode(publicCfg); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}

// handleUpdateConfig updates the complete system configuration.
//
//	PUT /api/config
func (h *Handler) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	expectedRevision, err := expectedConfigRevision(r)
	if errors.Is(err, errConfigPreconditionRequired) {
		http.Error(w, err.Error(), http.StatusPreconditionRequired)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer func() { _ = r.Body.Close() }()

	var raw map[string]any
	if err = json.Unmarshal(body, &raw); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	if err = config.ValidateConfigJSON(body); err != nil {
		http.Error(w, fmt.Sprintf("Invalid config: %v", err), http.StatusBadRequest)
		return
	}
	if err = normalizeConfigStringArrayFields(raw, nil); err != nil {
		http.Error(w, fmt.Sprintf("Invalid string array field: %v", err), http.StatusBadRequest)
		return
	}
	normalizedBody, err := json.Marshal(raw)
	if err != nil {
		http.Error(w, "Failed to normalize config payload", http.StatusBadRequest)
		return
	}
	var cfg config.Config
	if err = config.DecodeCurrentConfig(normalizedBody, &cfg); err != nil {
		http.Error(w, fmt.Sprintf("Invalid config: %v", err), http.StatusBadRequest)
		return
	}
	if execAllowRemoteOmitted(body) {
		cfg.Tools.Exec.AllowRemote = config.DefaultConfig().Tools.Exec.AllowRemote
	}

	// Copy security credentials from the same durable revision the client
	// replaced, while allowing the replacement to remove registry entries.
	current, err := h.configRepository().ReadDurable()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	if current.Revision != expectedRevision {
		writeConfigConflict(w, &config.ConflictError{Expected: expectedRevision, Actual: current.Revision})
		return
	}
	err = cfg.SecurityCopyForReplacement(h.configPath, current.Config)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to apply security config: %v", err), http.StatusInternalServerError)
		return
	}
	if err = applyConfigSecretsFromMap(&cfg, raw, h.configPath); err != nil {
		http.Error(w, fmt.Sprintf("Failed to resolve config secrets: %v", err), http.StatusBadRequest)
		return
	}

	if errs := validateConfig(&cfg); len(errs) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "validation_error",
			"errors": errs,
		})
		return
	}

	snapshot, err := h.configRepository().Replace(expectedRevision, &cfg)
	var conflict *config.ConflictError
	if errors.As(err, &conflict) {
		writeConfigConflict(w, conflict)
		return
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}

	h.applyRuntimeLogLevel()
	logger.Infof("configuration updated successfully")

	w.Header().Set("Content-Type", "application/json")
	writeConfigRevision(w, snapshot.Revision)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func execAllowRemoteOmitted(body []byte) bool {
	var raw struct {
		Tools *struct {
			Exec *struct {
				AllowRemote *bool `json:"allow_remote"`
			} `json:"exec"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return false
	}
	return raw.Tools == nil || raw.Tools.Exec == nil || raw.Tools.Exec.AllowRemote == nil
}

// handlePatchConfig partially updates the system configuration using JSON Merge Patch (RFC 7396).
// Only the fields present in the request body will be updated; all other fields remain unchanged.
//
//	PATCH /api/config
func (h *Handler) handlePatchConfig(w http.ResponseWriter, r *http.Request) {
	patchBody, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer func() { _ = r.Body.Close() }()

	// Validate the patch is valid JSON
	var patch map[string]any
	if err = json.Unmarshal(patchBody, &patch); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	if err = config.ValidateConfigJSON(patchBody); err != nil {
		http.Error(w, fmt.Sprintf("Invalid config patch: %v", err), http.StatusBadRequest)
		return
	}
	var validationErrors []string
	snapshot, err := h.updateConfig(func(cfg *config.Config) error {
		if validateErr := config.ValidateConfigPatchJSON(patchBody, cfg); validateErr != nil {
			return &configPatchRequestError{err: fmt.Errorf("invalid config patch: %w", validateErr)}
		}
		updated, updateErr := applyConfigMergePatch(cfg, patch, h.configPath)
		if updateErr != nil {
			return updateErr
		}
		if validationErrors = validateConfig(updated); len(validationErrors) > 0 {
			return errors.New("config validation failed")
		}
		*cfg = *updated
		return nil
	})
	if len(validationErrors) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "validation_error",
			"errors": validationErrors,
		})
		return
	}
	var requestErr *configPatchRequestError
	if errors.As(err, &requestErr) {
		http.Error(w, requestErr.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}

	h.applyRuntimeLogLevel()
	logger.Infof("configuration updated successfully")

	w.Header().Set("Content-Type", "application/json")
	writeConfigRevision(w, snapshot.Revision)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

type configPatchRequestError struct {
	err error
}

func (e *configPatchRequestError) Error() string {
	return e.err.Error()
}

func (e *configPatchRequestError) Unwrap() error {
	return e.err
}

func applyConfigMergePatch(current *config.Config, patch map[string]any, configPath string) (*config.Config, error) {
	if err := normalizeConfigStringArrayFields(patch, current); err != nil {
		return nil, &configPatchRequestError{err: fmt.Errorf("invalid string array field: %w", err)}
	}
	publicCurrent, err := config.ProjectPublicConfig(current)
	if err != nil {
		return nil, fmt.Errorf("project current config: %w", err)
	}
	existing, err := json.Marshal(publicCurrent)
	if err != nil {
		return nil, fmt.Errorf("serialize current config: %w", err)
	}
	var base map[string]any
	if err = json.Unmarshal(existing, &base); err != nil {
		return nil, fmt.Errorf("parse current config: %w", err)
	}
	mergeMap(base, patch)
	merged, err := json.Marshal(base)
	if err != nil {
		return nil, fmt.Errorf("serialize merged config: %w", err)
	}
	var updated config.Config
	if err = config.DecodeCurrentConfig(merged, &updated); err != nil {
		return nil, &configPatchRequestError{err: fmt.Errorf("decode merged config: %w", err)}
	}
	if err = updated.SecurityCopyForReplacement(configPath, current); err != nil {
		return nil, fmt.Errorf("apply security config: %w", err)
	}
	if err = applyConfigSecretsFromMap(&updated, base, configPath); err != nil {
		return nil, fmt.Errorf("resolve config secrets: %w", err)
	}
	return &updated, nil
}

// handleResetConfig resets the configuration to factory defaults.
// API keys and security credentials are preserved.
//
//	POST /api/config/reset
func (h *Handler) handleResetConfig(w http.ResponseWriter, r *http.Request) {
	snapshot, err := h.updateConfig(func(cfg *config.Config) error {
		if backupErr := config.MakeBackup(h.configPath); backupErr != nil {
			return fmt.Errorf("backup before reset: %w", backupErr)
		}
		defaults := config.DefaultConfig()
		if securityErr := defaults.SecurityCopyForReplacement(h.configPath, cfg); securityErr != nil {
			return fmt.Errorf("preserve security config: %w", securityErr)
		}
		*cfg = *defaults
		return nil
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to reset config: %v", err), http.StatusInternalServerError)
		return
	}

	h.applyRuntimeLogLevel()
	logger.Infof("configuration reset to factory defaults")

	// Restart gateway if running
	status := h.gatewayStatusData()
	gatewayStatus, _ := status["gateway_status"].(string)
	if gatewayStatus == "running" {
		if _, err := h.RestartGateway(); err != nil {
			logger.ErrorF("failed to restart gateway after config reset", map[string]any{"error": err.Error()})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	writeConfigRevision(w, snapshot.Revision)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleTestCommandPatterns tests a command against whitelist and blacklist patterns.
//
//	POST /api/config/test-command-patterns
func (h *Handler) handleTestCommandPatterns(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer func() { _ = r.Body.Close() }()

	var req struct {
		AllowPatterns []string `json:"allow_patterns"`
		DenyPatterns  []string `json:"deny_patterns"`
		Command       string   `json:"command"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	lower := strings.ToLower(strings.TrimSpace(req.Command))

	type result struct {
		Allowed          bool    `json:"allowed"`
		Blocked          bool    `json:"blocked"`
		MatchedWhitelist *string `json:"matched_whitelist,omitempty"`
		MatchedBlacklist *string `json:"matched_blacklist,omitempty"`
	}

	resp := result{Allowed: false, Blocked: false}

	// Check whitelist first
	for _, pattern := range req.AllowPatterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			continue // skip invalid patterns
		}
		if re.MatchString(lower) {
			resp.Allowed = true
			resp.MatchedWhitelist = &pattern
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
	}

	// Check blacklist
	for _, pattern := range req.DenyPatterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			continue
		}
		if re.MatchString(lower) {
			resp.Blocked = true
			resp.MatchedBlacklist = &pattern
			break
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// validateConfig checks the config for common errors before saving.
// Returns a list of human-readable error strings; empty means valid.
func validateConfig(cfg *config.Config) []string {
	var errs []string

	// Validate model_list entries
	if err := cfg.ValidateModelList(); err != nil {
		errs = append(errs, err.Error())
	}
	if err := cfg.ValidateModelReferences(); err != nil {
		errs = append(errs, err.Error())
	}

	if err := cfg.ValidateTurnProfile(); err != nil {
		errs = append(errs, err.Error())
	}

	if err := cfg.Session.Lifecycle.Validate(); err != nil {
		errs = append(errs, err.Error())
	}
	if err := cfg.ValidateMCPConfig(); err != nil {
		errs = append(errs, err.Error())
	}

	// Gateway port range
	if cfg.Gateway.Port != 0 && (cfg.Gateway.Port < 1 || cfg.Gateway.Port > 65535) {
		errs = append(errs, fmt.Sprintf("gateway.port %d is out of valid range (1-65535)", cfg.Gateway.Port))
	}

	for name, bc := range cfg.Channels {
		streaming, ok := channelStreamingConfig(bc)
		if !ok {
			continue
		}
		if streaming.ThrottleSeconds < 0 {
			errs = append(errs, fmt.Sprintf("channel %q streaming.throttle_seconds must be >= 0", name))
		}
		if streaming.MinGrowthChars < 0 {
			errs = append(errs, fmt.Sprintf("channel %q streaming.min_growth_chars must be >= 0", name))
		}
	}

	// MintClaw channel: token required when enabled
	{
		bc := cfg.Channels.GetByType(config.ChannelMintClaw)
		if bc != nil && bc.Enabled {
			if decoded, err := bc.GetDecoded(); err == nil && decoded != nil {
				if c, ok := decoded.(*config.MintClawSettings); ok && c.Token.String() == "" {
					errs = append(errs, "channels.mintclaw.token is required when mintclaw channel is enabled")
				}
			}
		}
	}

	// Telegram: token required when enabled
	{
		bc := cfg.Channels.GetByType(config.ChannelTelegram)
		if bc != nil && bc.Enabled {
			if decoded, err := bc.GetDecoded(); err == nil && decoded != nil {
				if c, ok := decoded.(*config.TelegramSettings); ok && c.Token.String() == "" {
					errs = append(errs, "channels.telegram.token is required when telegram channel is enabled")
				}
			}
		}
	}

	// Discord: token required when enabled
	{
		bc := cfg.Channels.GetByType(config.ChannelDiscord)
		if bc != nil && bc.Enabled {
			if decoded, err := bc.GetDecoded(); err == nil && decoded != nil {
				if c, ok := decoded.(*config.DiscordSettings); ok && c.Token.String() == "" {
					errs = append(errs, "channels.discord.token is required when discord channel is enabled")
				}
			}
		}
	}

	{
		bc := cfg.Channels.GetByType(config.ChannelWeCom)
		if bc != nil && bc.Enabled {
			if decoded, err := bc.GetDecoded(); err == nil && decoded != nil {
				if c, ok := decoded.(*config.WeComSettings); ok {
					if c.BotID == "" {
						errs = append(errs, "channels.wecom.bot_id is required when wecom channel is enabled")
					}
					if c.Secret.String() == "" {
						errs = append(errs, "channels.wecom.secret is required when wecom channel is enabled")
					}
				}
			}
		}
	}

	if cfg.Tools.Exec.Enabled {
		if cfg.Tools.Exec.EnableDenyPatterns {
			errs = append(
				errs,
				validateRegexPatterns("tools.exec.custom_deny_patterns", cfg.Tools.Exec.CustomDenyPatterns)...)
		}
		errs = append(
			errs,
			validateRegexPatterns("tools.exec.custom_allow_patterns", cfg.Tools.Exec.CustomAllowPatterns)...)
	}

	return errs
}

func validateRegexPatterns(field string, patterns []string) []string {
	var errs []string
	for index, pattern := range patterns {
		if _, err := regexp.Compile(pattern); err != nil {
			errs = append(errs, fmt.Sprintf("%s[%d] is not a valid regular expression: %v", field, index, err))
		}
	}
	return errs
}

// mergeMap recursively merges src into dst (JSON Merge Patch semantics).
// - If a key in src has a null value, it is deleted from dst.
// - If both dst and src have a nested object for the same key, merge recursively.
// - Otherwise the value from src overwrites dst.
func mergeMap(dst, src map[string]any) {
	for key, srcVal := range src {
		if srcVal == nil {
			delete(dst, key)
			continue
		}
		srcMap, srcIsMap := srcVal.(map[string]any)
		dstMap, dstIsMap := dst[key].(map[string]any)
		if srcIsMap && dstIsMap {
			mergeMap(dstMap, srcMap)
		} else {
			dst[key] = srcVal
		}
	}
}

func asMapField(value map[string]any, key string) (map[string]any, bool) {
	raw, exists := value[key]
	if !exists {
		return nil, false
	}
	m, isMap := raw.(map[string]any)
	return m, isMap
}

var (
	allowFromHiddenCharsRe = regexp.MustCompile("[\u200B\u200C\u200D\u200E\u200F\u202A-\u202E\u2060-\u2069\uFEFF]")
	allowFromSplitRe       = regexp.MustCompile("[,\uFF0C、;；\r\n\t]+")
	conservativeSplitRe    = regexp.MustCompile("[,\uFF0C\r\n\t]+")
)

type stringArrayParserOptions struct {
	stripHiddenChars bool
}

func normalizeConfigStringArrayFields(raw map[string]any, current *config.Config) error {
	if toolsMap, hasTools := asMapField(raw, "tools"); hasTools {
		if webMap, hasWeb := asMapField(toolsMap, "web"); hasWeb {
			if rawWhitelist, exists := webMap["private_host_whitelist"]; exists {
				normalized, err := normalizeStringArrayValue(rawWhitelist, stringArrayParserOptions{})
				if err != nil {
					return fmt.Errorf("tools.web.private_host_whitelist: %w", err)
				}
				webMap["private_host_whitelist"] = normalized
			}
		}
	}

	return normalizeChannelArrayFields(raw, current)
}

func normalizeChannelArrayFields(raw map[string]any, current *config.Config) error {
	channelsMap, hasChannels := asMapField(raw, "channel_list")
	if !hasChannels {
		return nil
	}

	defaultCfg := config.DefaultConfig()
	for channelName, rawChannel := range channelsMap {
		chMap, ok := rawChannel.(map[string]any)
		if !ok {
			continue
		}

		if rawAllowFrom, exists := chMap["allow_from"]; exists {
			normalized, err := normalizeStringArrayValue(rawAllowFrom, stringArrayParserOptions{
				stripHiddenChars: true,
			})
			if err != nil {
				return fmt.Errorf("channel_list.%s.allow_from: %w", channelName, err)
			}
			chMap["allow_from"] = normalized
		}

		if groupTrigger, ok := asMapField(chMap, "group_trigger"); ok {
			if rawPrefixes, exists := groupTrigger["prefixes"]; exists {
				normalized, err := normalizeStringArrayValue(rawPrefixes, stringArrayParserOptions{})
				if err != nil {
					return fmt.Errorf("channel_list.%s.group_trigger.prefixes: %w", channelName, err)
				}
				groupTrigger["prefixes"] = normalized
			}
		}

		if placeholder, ok := asMapField(chMap, "placeholder"); ok {
			if rawText, exists := placeholder["text"]; exists {
				normalized, err := normalizeLiteralStringArrayValue(rawText)
				if err != nil {
					return fmt.Errorf("channel_list.%s.placeholder.text: %w", channelName, err)
				}
				placeholder["text"] = normalized
			}
		}

		settingsMap, hasSettings := asMapField(chMap, "settings")
		if !hasSettings {
			continue
		}

		settingsType := channelSettingsType(defaultCfg, current, channelName, chMap)
		if settingsType == nil {
			continue
		}

		for i := range settingsType.NumField() {
			field := settingsType.Field(i)
			if !field.IsExported() || !isStringSliceType(field.Type) {
				continue
			}
			jsonKey := strings.Split(field.Tag.Get("json"), ",")[0]
			if jsonKey == "" || jsonKey == "-" {
				continue
			}
			rawValue, exists := settingsMap[jsonKey]
			if !exists {
				continue
			}

			options := stringArrayParserOptions{}
			if jsonKey == "allow_from" {
				options.stripHiddenChars = true
			}
			normalized, err := normalizeStringArrayValue(rawValue, options)
			if err != nil {
				return fmt.Errorf("channel_list.%s.settings.%s: %w", channelName, jsonKey, err)
			}
			settingsMap[jsonKey] = normalized
		}
	}
	return nil
}

func channelSettingsType(
	defaultCfg *config.Config,
	current *config.Config,
	channelName string,
	channelMap map[string]any,
) reflect.Type {
	if channelType, _ := channelMap["type"].(string); channelType != "" {
		if bc := defaultCfg.Channels.GetByType(channelType); bc != nil {
			if decoded, err := bc.GetDecoded(); err == nil && decoded != nil {
				return derefType(reflect.TypeOf(decoded))
			}
		}
	}

	if current != nil {
		if bc := current.Channels.Get(channelName); bc != nil {
			if decoded, err := bc.GetDecoded(); err == nil && decoded != nil {
				return derefType(reflect.TypeOf(decoded))
			}
		}
	}

	if bc := defaultCfg.Channels.Get(channelName); bc != nil {
		if decoded, err := bc.GetDecoded(); err == nil && decoded != nil {
			return derefType(reflect.TypeOf(decoded))
		}
	}

	return nil
}

func derefType(typ reflect.Type) reflect.Type {
	for typ != nil && typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	return typ
}

func isStringSliceType(typ reflect.Type) bool {
	typ = derefType(typ)
	return typ != nil && typ.Kind() == reflect.Slice && typ.Elem().Kind() == reflect.String
}

func normalizeStringArrayValue(value any, options stringArrayParserOptions) ([]string, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case string:
		return parseStringArrayValue(typed, options), nil
	case float64:
		return normalizeStringArrayItems([]string{fmt.Sprintf("%.0f", typed)}, options), nil
	case []string:
		return append([]string(nil), typed...), nil
	case []any:
		items := make([]string, len(typed))
		for i, item := range typed {
			raw, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("unsupported list item type %T", item)
			}
			items[i] = raw
		}
		return items, nil
	default:
		return nil, fmt.Errorf("unsupported list field type %T", value)
	}
}

func normalizeLiteralStringArrayValue(value any) ([]string, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case string:
		return []string{typed}, nil
	case []string:
		return append([]string(nil), typed...), nil
	case []any:
		items := make([]string, len(typed))
		for i, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("unsupported list item type %T", item)
			}
			items[i] = text
		}
		return items, nil
	default:
		return nil, fmt.Errorf("unsupported list field type %T", value)
	}
}

func parseStringArrayValue(raw string, options stringArrayParserOptions) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{}
	}
	splitRe := conservativeSplitRe
	if options.stripHiddenChars {
		splitRe = allowFromSplitRe
	}
	return normalizeStringArrayItems(splitRe.Split(raw, -1), options)
}

func normalizeStringArrayItems(items []string, options stringArrayParserOptions) []string {
	result := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		normalized := item
		if options.stripHiddenChars {
			normalized = allowFromHiddenCharsRe.ReplaceAllString(normalized, "")
		}
		normalized = strings.TrimSpace(normalized)
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	if len(result) == 0 {
		return []string{}
	}
	return result
}

func getSecretString(m map[string]any, key string) (string, bool) {
	if raw, exists := m[key]; exists {
		s, isString := raw.(string)
		if isString {
			return s, true
		}
	}
	if raw, exists := m["_"+key]; exists {
		s, isString := raw.(string)
		if isString {
			return s, true
		}
	}
	return "", false
}

const legacySecretPlaceholder = "[NOT_HERE]"

func applyConfigSecretsFromMap(cfg *config.Config, raw map[string]any, configPath string) error {
	channelsMap, _ := asMapField(raw, "channel_list")
	for chName, chData := range channelsMap {
		chMap, ok := chData.(map[string]any)
		if !ok {
			continue
		}
		bc := cfg.Channels.Get(chName)
		if bc == nil {
			continue
		}
		decoded, err := bc.GetDecoded()
		if err != nil || decoded == nil {
			continue
		}
		rv := reflect.ValueOf(decoded)
		if rv.Kind() == reflect.Ptr {
			rv = rv.Elem()
		}
		if rv.Kind() != reflect.Struct {
			continue
		}
		// Channel-specific settings live under the "settings" key in the raw map
		settingsMap := chMap
		if sm, hasSettings := asMapField(chMap, "settings"); hasSettings {
			settingsMap = sm
		}
		applySecureStringsToStruct(rv, settingsMap)
	}

	// Handle tools secrets
	tools, _ := asMapField(raw, "tools")
	skills, _ := asMapField(tools, "skills")
	registries, _ := asMapField(skills, "registries")
	for registryName, rawRegistry := range registries {
		registryMap, ok := rawRegistry.(map[string]any)
		if !ok {
			continue
		}
		if authToken, hasAuthToken := getSecretString(
			registryMap,
			"auth_token",
		); hasAuthToken &&
			authToken != legacySecretPlaceholder {
			registryCfg, _ := cfg.Tools.Skills.Registries.Get(registryName)
			registryCfg.AuthToken.Set(authToken)
			cfg.Tools.Skills.Registries.Set(registryName, registryCfg)
		}
	}
	return cfg.ResolveCredentialReferences(configPath)
}

// applySecureStringsToStruct walks a struct and applies SecureString fields
// from the matching keys in rawMap. It recurses into nested maps and slices.
func applySecureStringsToStruct(rv reflect.Value, rawMap map[string]any) {
	rt := rv.Type()
	for jsonKey, rawVal := range rawMap {
		for i := range rt.NumField() {
			f := rt.Field(i)
			if !f.IsExported() {
				continue
			}
			tag := f.Tag.Get("json")
			name := strings.Split(tag, ",")[0]
			if name != jsonKey {
				continue
			}
			sf := rv.Field(i)
			if !sf.CanSet() {
				continue
			}
			// Direct SecureString field
			if s, ok := rawVal.(string); ok {
				if s == legacySecretPlaceholder {
					continue
				}
				if f.Type == reflect.TypeOf(config.SecureString{}) {
					sf.Set(reflect.ValueOf(*config.NewSecureString(s)))
				} else if f.Type == reflect.TypeOf(&config.SecureString{}) {
					sf.Set(reflect.ValueOf(config.NewSecureString(s)))
				}
				continue
			}
			// Recurse into nested struct
			if sf.Kind() == reflect.Struct {
				if nested, ok := rawVal.(map[string]any); ok {
					applySecureStringsToStruct(sf, nested)
				}
				continue
			}
			// Recurse into map fields (e.g., map[string]SomeStruct)
			if sf.Kind() == reflect.Map && sf.Type().Elem().Kind() == reflect.Struct {
				if nestedMap, ok := rawVal.(map[string]any); ok {
					for mapKey, mapVal := range nestedMap {
						nested, ok := mapVal.(map[string]any)
						if !ok {
							continue
						}
						elemType := sf.Type().Elem()
						// Get existing element or create a new zero value
						var elem reflect.Value
						existing := sf.MapIndex(reflect.ValueOf(mapKey))
						if existing.IsValid() {
							if existing.Kind() == reflect.Interface {
								existing = existing.Elem()
							}
							if existing.Kind() == reflect.Ptr && !existing.IsNil() {
								elem = reflect.New(elemType)
								elem.Elem().Set(existing.Elem())
							} else if existing.Kind() == reflect.Struct {
								elem = reflect.New(elemType)
								elem.Elem().Set(existing)
							}
						}
						if !elem.IsValid() {
							elem = reflect.New(elemType)
						}
						applySecureStringsToStruct(elem.Elem(), nested)
						sf.SetMapIndex(reflect.ValueOf(mapKey), elem.Elem())
					}
				}
				continue
			}
			// Recurse into slice elements that are structs
			if sf.Kind() == reflect.Slice && sf.Type().Elem().Kind() == reflect.Struct {
				if sliceRaw, ok := rawVal.([]any); ok {
					for idx, elemRaw := range sliceRaw {
						if nested, ok := elemRaw.(map[string]any); ok {
							if idx < sf.Len() {
								applySecureStringsToStruct(sf.Index(idx), nested)
							}
						}
					}
				}
			}
		}
	}
}
