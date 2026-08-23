package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/audio/asr"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/utils"
)

// registerModelRoutes binds model list management endpoints to the ServeMux.
func (h *Handler) registerModelRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/models", h.handleListModels)
	mux.HandleFunc("POST /api/models/fetch", h.handleFetchModels)
	mux.HandleFunc("GET /api/models/catalog", h.handleListCatalogs)
	mux.HandleFunc("DELETE /api/models/catalog/{id}", h.handleDeleteCatalog)
	mux.HandleFunc("POST /api/models", h.handleAddModel)
	mux.HandleFunc("POST /api/models/default", h.handleSetDefaultModel)
	mux.HandleFunc("PUT /api/models/{index}", h.handleUpdateModel)
	mux.HandleFunc("DELETE /api/models/{index}", h.handleDeleteModel)
	mux.HandleFunc("POST /api/models/{index}/test", h.handleTestModel)
	mux.HandleFunc("POST /api/models/test-inline", h.handleTestInlineModel)
}

// modelResponse is the JSON structure returned for each model in the list.
// All ModelConfig fields are included so the frontend can display and edit them.
type modelResponse struct {
	Index      int    `json:"index"`
	ModelName  string `json:"model_name"`
	Provider   string `json:"provider,omitempty"`
	Model      string `json:"model"`
	APIBase    string `json:"api_base,omitempty"`
	APIKey     string `json:"api_key"`
	Proxy      string `json:"proxy,omitempty"`
	AuthMethod string `json:"auth_method,omitempty"`
	// Advanced fields
	ConnectMode         string                      `json:"connect_mode,omitempty"`
	Workspace           string                      `json:"workspace,omitempty"`
	RPM                 int                         `json:"rpm,omitempty"`
	MaxTokensField      string                      `json:"max_tokens_field,omitempty"`
	RequestTimeout      int                         `json:"request_timeout,omitempty"`
	ThinkingLevel       string                      `json:"thinking_level,omitempty"`
	ToolSchemaTransform string                      `json:"tool_schema_transform,omitempty"`
	Streaming           config.ModelStreamingConfig `json:"streaming,omitempty"`
	ExtraBody           map[string]any              `json:"extra_body,omitempty"`
	CustomHeaders       map[string]string           `json:"custom_headers,omitempty"`
	// Meta
	Enabled             bool   `json:"enabled"`
	Available           bool   `json:"available"`
	Status              string `json:"status"`
	IsDefault           bool   `json:"is_default"`
	IsVirtual           bool   `json:"is_virtual"`
	DefaultModelAllowed bool   `json:"default_model_allowed"`
}

func normalizeIncomingModelConfig(mc *config.ModelConfig) {
	if mc == nil {
		return
	}

	mc.Provider = providers.NormalizeProvider(mc.Provider)
	mc.AuthMethod = strings.ToLower(strings.TrimSpace(mc.AuthMethod))
	if mc.Provider == "antigravity" && mc.AuthMethod == "" {
		mc.AuthMethod = "oauth"
	}
}

func createAllowedForProvider(provider string) bool {
	normalized := providers.NormalizeProvider(provider)
	switch normalized {
	case "bedrock":
		// Bedrock currently authenticates through the AWS SDK credential chain
		// (env vars, shared profiles, IAM roles, etc.), and this Web layer does
		// not yet have a reliable preflight check for those credential sources.
		// Keep it creatable in the catalog and let provider construction/runtime
		// return the concrete AWS error when the environment is incomplete.
		return true
	case "claude-cli", "codex-cli":
		return cliProviderCreateAllowedFromCurrentStatus(normalized)
	default:
		return providers.IsCreatableModelProvider(normalized)
	}
}

// cliProviderCreateAllowedFromCurrentStatus intentionally reuses the existing
// local model status pipeline so provider catalog gating follows the same CLI
// executable probe used by launcher readiness.
func cliProviderCreateAllowedFromCurrentStatus(provider string) bool {
	status := modelConfigurationStatus(&config.ModelConfig{
		Provider: provider,
		Model:    provider,
	})
	return status.Available
}

func modelProviderOptionsForResponse() []providers.ModelProviderOption {
	options := providers.ModelProviderOptions()
	for i := range options {
		options[i].CreateAllowed = createAllowedForProvider(options[i].ID)
	}
	return options
}

func defaultModelAllowedForModelConfig(mc *config.ModelConfig) bool {
	return mc != nil && providers.IsDefaultModelProvider(mc.Provider)
}

func validateIncomingModelConfig(mc *config.ModelConfig, existing *config.ModelConfig) error {
	if mc == nil {
		return fmt.Errorf("model config is required")
	}
	if err := mc.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(mc.Provider) == "" {
		return fmt.Errorf("provider is required")
	}
	if !providers.IsSupportedModelProvider(mc.Provider) {
		return fmt.Errorf("provider %q is not supported", mc.Provider)
	}
	if mc.Provider == "elevenlabs" && strings.TrimSpace(mc.Model) != asr.ElevenLabsSupportedModelID() {
		return fmt.Errorf("provider %q only supports model %q", mc.Provider, asr.ElevenLabsSupportedModelID())
	}
	if !createAllowedForProvider(mc.Provider) {
		if existing == nil {
			return fmt.Errorf("provider %q is not available for new models", mc.Provider)
		}
		if providers.NormalizeProvider(existing.Provider) != mc.Provider {
			return fmt.Errorf("provider %q is not available for selection", mc.Provider)
		}
	}
	return nil
}

// handleListModels returns all model_list entries with masked API keys.
//
//	GET /api/models
func (h *Handler) handleListModels(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.readConfig()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	defaultModel := cfg.Agents.Defaults.GetModelName()
	modelStatuses := make([]modelConfigurationSummary, len(cfg.ModelList))

	var wg sync.WaitGroup
	wg.Add(len(cfg.ModelList))
	for i, m := range cfg.ModelList {
		go func(i int, m *config.ModelConfig) {
			defer wg.Done()
			modelStatuses[i] = modelConfigurationStatus(m)
		}(i, m)
	}
	wg.Wait()

	models := make([]modelResponse, 0, len(cfg.ModelList))
	for i, m := range cfg.ModelList {
		models = append(models, modelResponse{
			Index:               i,
			ModelName:           m.ModelName,
			Provider:            m.Provider,
			Model:               m.Model,
			APIBase:             m.APIBase,
			APIKey:              maskAPIKey(m.APIKey()),
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
			Enabled:             m.Enabled,
			Available:           modelStatuses[i].Available,
			Status:              modelStatuses[i].Status,
			IsDefault:           m.ModelName == defaultModel,
			IsVirtual:           m.IsVirtual(),
			DefaultModelAllowed: defaultModelAllowedForModelConfig(m),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"models":           models,
		"total":            len(models),
		"default_model":    defaultModel,
		"provider_options": modelProviderOptionsForResponse(),
	})
}

// handleAddModel appends a new model configuration entry.
//
//	POST /api/models
func (h *Handler) handleAddModel(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer func() { _ = r.Body.Close() }()

	type custom struct {
		config.ModelConfig
		APIKey string `json:"api_key"`
	}

	var mc custom
	if err = json.Unmarshal(body, &mc); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	normalizeIncomingModelConfig(&mc.ModelConfig)

	if err = validateIncomingModelConfig(&mc.ModelConfig, nil); err != nil {
		http.Error(w, fmt.Sprintf("Validation error: %v", err), http.StatusBadRequest)
		return
	}

	if mc.APIKey != "" {
		mc.SetAPIKey(mc.APIKey)
	}

	index := 0
	snapshot, err := h.updateConfig(func(cfg *config.Config) error {
		cfg.ModelList = append(cfg.ModelList, &mc.ModelConfig)
		index = len(cfg.ModelList) - 1
		return validateModelReferenceMutation(cfg)
	})
	if writeConfigMutationError(w, err) {
		return
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeConfigRevision(w, snapshot.Revision)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"index":  index,
	})
}

// handleUpdateModel replaces a model configuration entry at the given index.
// If the request body omits api_key (or sends an empty string), the existing
// stored key is preserved so callers can update only api_base / proxy without
// exposing or clearing the secret.
//
//	PUT /api/models/{index}
func (h *Handler) handleUpdateModel(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		http.Error(w, "Invalid index", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer func() { _ = r.Body.Close() }()

	var rawFields map[string]json.RawMessage
	if err = json.Unmarshal(body, &rawFields); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	type custom struct {
		config.ModelConfig
		APIKey string `json:"api_key"`
	}

	var mc custom
	if err = json.Unmarshal(body, &mc); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	snapshot, err := h.updateConfig(func(cfg *config.Config) error {
		if idx < 0 || idx >= len(cfg.ModelList) {
			return &configMutationRequestError{
				status: http.StatusNotFound,
				err:    fmt.Errorf("index %d out of range (0-%d)", idx, len(cfg.ModelList)-1),
			}
		}

		// Preserve the existing API key when the caller omits it (empty string).
		// This lets the UI update api_base / proxy without clearing the stored secret.
		if mc.APIKey == "" {
			mc.SetAPIKey(cfg.ModelList[idx].APIKey())
		} else {
			mc.SetAPIKey(mc.APIKey)
		}
		// Preserve existing ExtraBody when omitted (nil), but clear it when
		// the frontend sends an empty object {} to indicate the field should
		// be removed.
		if mc.ExtraBody == nil {
			mc.ExtraBody = cfg.ModelList[idx].ExtraBody
		} else if len(mc.ExtraBody) == 0 {
			mc.ExtraBody = nil
		}
		// Preserve existing CustomHeaders when omitted (nil), but clear it when
		// the frontend sends an empty object {} to indicate the field should
		// be removed.
		if mc.CustomHeaders == nil {
			mc.CustomHeaders = cfg.ModelList[idx].CustomHeaders
		} else if len(mc.CustomHeaders) == 0 {
			mc.CustomHeaders = nil
		}
		if _, ok := rawFields["tool_schema_transform"]; !ok {
			mc.ToolSchemaTransform = cfg.ModelList[idx].ToolSchemaTransform
		}
		if _, ok := rawFields["streaming"]; !ok {
			mc.Streaming = cfg.ModelList[idx].Streaming
		}
		normalizeIncomingModelConfig(&mc.ModelConfig)
		if err = validateIncomingModelConfig(&mc.ModelConfig, cfg.ModelList[idx]); err != nil {
			return &configMutationRequestError{
				status: http.StatusBadRequest,
				err:    fmt.Errorf("validation error: %w", err),
			}
		}
		if cfg.Agents.Defaults.ModelName == cfg.ModelList[idx].ModelName &&
			!defaultModelAllowedForModelConfig(&mc.ModelConfig) {
			// Allow users to recover from legacy/invalid defaults by saving the model
			// and clearing the default chat model reference in the same write.
			cfg.Agents.Defaults.ModelName = ""
		}

		cfg.ModelList[idx] = &mc.ModelConfig
		if validationErr := validateModelReferenceMutation(cfg); validationErr != nil {
			return validationErr
		}

		logger.Debugf("update model config: %#v", mc.ModelConfig)
		return nil
	})
	if writeConfigMutationError(w, err) {
		return
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeConfigRevision(w, snapshot.Revision)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleDeleteModel removes a model configuration entry at the given index.
//
//	DELETE /api/models/{index}
func (h *Handler) handleDeleteModel(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		http.Error(w, "Invalid index", http.StatusBadRequest)
		return
	}

	snapshot, err := h.updateConfig(func(cfg *config.Config) error {
		if idx < 0 || idx >= len(cfg.ModelList) {
			return &configMutationRequestError{
				status: http.StatusNotFound,
				err:    fmt.Errorf("index %d out of range (0-%d)", idx, len(cfg.ModelList)-1),
			}
		}
		deletedModelName := cfg.ModelList[idx].ModelName
		cfg.ModelList = append(cfg.ModelList[:idx], cfg.ModelList[idx+1:]...)
		if cfg.Agents.Defaults.ModelName == deletedModelName {
			cfg.Agents.Defaults.ModelName = ""
		}
		return validateModelReferenceMutation(cfg)
	})
	if writeConfigMutationError(w, err) {
		return
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeConfigRevision(w, snapshot.Revision)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func validateModelReferenceMutation(cfg *config.Config) error {
	if err := cfg.ValidateModelReferences(); err != nil {
		return &configMutationRequestError{
			status: http.StatusBadRequest,
			err:    fmt.Errorf("validation error: %w", err),
		}
	}
	return nil
}

// handleSetDefaultModel sets the default model for all agents.
//
//	POST /api/models/default
func (h *Handler) handleSetDefaultModel(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer func() { _ = r.Body.Close() }()

	var req struct {
		ModelName string `json:"model_name"`
	}
	if err = json.Unmarshal(body, &req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	if req.ModelName == "" {
		http.Error(w, "model_name is required", http.StatusBadRequest)
		return
	}

	snapshot, err := h.updateConfig(func(cfg *config.Config) error {
		for _, model := range cfg.ModelList {
			if model.ModelName != req.ModelName {
				continue
			}
			if model.IsVirtual() {
				return &configMutationRequestError{
					status: http.StatusBadRequest,
					err:    fmt.Errorf("cannot set virtual model %q as default", req.ModelName),
				}
			}
			if !defaultModelAllowedForModelConfig(model) {
				return &configMutationRequestError{
					status: http.StatusBadRequest,
					err:    fmt.Errorf("model %q cannot be used as the default chat model", req.ModelName),
				}
			}
			cfg.Agents.Defaults.ModelName = req.ModelName
			return nil
		}
		return &configMutationRequestError{
			status: http.StatusNotFound,
			err:    fmt.Errorf("model %q not found in model_list", req.ModelName),
		}
	})
	if writeConfigMutationError(w, err) {
		return
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeConfigRevision(w, snapshot.Revision)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":        "ok",
		"default_model": req.ModelName,
	})
}

// maskAPIKey returns a masked version of an API key for safe display.
// Keys longer than 12 chars show prefix + last 4 chars: "sk-****abcd".
// Keys 9-12 chars show prefix + last 2 chars: "sk-****cd".
// Shorter keys are fully masked as "****".
// Empty keys return empty string.
// Ensure at least 40% of the key will not be displayed.
func maskAPIKey(key string) string {
	if key == "" {
		return ""
	}

	if len(key) <= 8 {
		return "****"
	}

	// Show first 3 chars and last 2 chars
	if len(key) <= 12 {
		return key[:3] + "****" + key[len(key)-2:]
	}

	// Show first 3 chars and last 4 chars
	return key[:3] + "****" + key[len(key)-4:]
}

// handleFetchModels fetches available models from an upstream provider.
//
//	POST /api/models/fetch
func (h *Handler) handleFetchModels(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer func() { _ = r.Body.Close() }()

	var req struct {
		Provider   string `json:"provider"`
		APIKey     string `json:"api_key"`
		APIBase    string `json:"api_base"`
		ModelIndex *int   `json:"model_index,omitempty"`
	}
	if err = json.Unmarshal(body, &req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	if req.Provider == "" {
		http.Error(w, "provider is required", http.StatusBadRequest)
		return
	}

	if !providers.IsModelProviderFetchable(req.Provider) {
		http.Error(w, fmt.Sprintf("provider %q does not support model listing", req.Provider), http.StatusBadRequest)
		return
	}

	apiKey := strings.TrimSpace(req.APIKey)
	apiBase := strings.TrimSpace(req.APIBase)

	if apiKey == "" && req.ModelIndex != nil {
		if stored := h.lookupStoredAPIKey(*req.ModelIndex, req.Provider, apiBase); stored != "" {
			apiKey = stored
		}
	}

	if apiBase == "" {
		apiBase = providers.DefaultAPIBaseForProtocol(req.Provider)
	}
	if apiBase == "" {
		http.Error(w, fmt.Sprintf("No default API base for provider %q", req.Provider), http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	models, err := fetchUpstreamModels(ctx, req.Provider, apiBase, apiKey)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to fetch models: %v", err), http.StatusBadGateway)
		return
	}

	// Auto-save fetched models to catalog
	catalogModels := make([]CatalogModel, len(models))
	for i, m := range models {
		catalogModels[i] = CatalogModel{ID: m.ID, OwnedBy: m.OwnedBy}
	}
	if saveErr := SaveCatalog(req.Provider, apiBase, apiKey, catalogModels); saveErr != nil {
		// Log but don't fail the request — saving catalog is non-critical
		logger.Warnf("Failed to save model catalog: %v", saveErr)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"models": models,
		"total":  len(models),
	})
}

func (h *Handler) lookupStoredAPIKey(index int, reqProvider, reqAPIBase string) string {
	cfg, err := h.readConfig()
	if err != nil || index < 0 || index >= len(cfg.ModelList) {
		return ""
	}
	stored := cfg.ModelList[index]
	storedProvider := stored.Provider
	if providers.NormalizeProvider(reqProvider) != providers.NormalizeProvider(storedProvider) {
		return ""
	}
	effectiveReqBase := strings.TrimSpace(reqAPIBase)
	if effectiveReqBase == "" {
		effectiveReqBase = providers.DefaultAPIBaseForProtocol(reqProvider)
	}
	effectiveStoredBase := strings.TrimSpace(stored.APIBase)
	if effectiveStoredBase == "" {
		effectiveStoredBase = providers.DefaultAPIBaseForProtocol(storedProvider)
	}
	if normalizeAPIBaseForCompare(effectiveReqBase) != normalizeAPIBaseForCompare(effectiveStoredBase) {
		return ""
	}
	return stored.APIKey()
}

type upstreamModel struct {
	ID      string `json:"id"`
	OwnedBy string `json:"owned_by,omitempty"`
}

var modelFetchPrivateHostWhitelist []string

func fetchUpstreamModels(ctx context.Context, provider, apiBase, apiKey string) ([]upstreamModel, error) {
	apiBase = strings.TrimRight(strings.TrimSpace(apiBase), "/")
	provider = providers.NormalizeProvider(provider)

	var fetchURL string
	switch provider {
	case "ollama":
		// Strip /v1 suffix if present to get the Ollama root
		root := apiBase
		root = strings.TrimSuffix(root, "/v1")
		root = strings.TrimRight(root, "/")
		fetchURL = root + "/api/tags"
		return fetchOllamaModels(ctx, fetchURL)
	case "nearai":
		fetchURL = apiBase + "/model/list"
		return fetchNearAIModels(ctx, fetchURL, apiKey)
	default:
		// OpenAI-compatible: /v1/models
		fetchURL = apiBase + "/models"
		return fetchOpenAICompatibleModelsForProvider(ctx, provider, fetchURL, apiKey)
	}
}

func newSafeModelFetchClient(provider, fetchURL string) (*http.Client, error) {
	whitelist, err := utils.NewPrivateHostWhitelist(modelFetchPrivateHostWhitelist)
	if err != nil {
		return nil, err
	}
	allowPrivateHosts := func() bool {
		return providers.IsLocalModelProvider(provider)
	}
	if err := utils.ValidateSafeHTTPURL(fetchURL, whitelist, allowPrivateHosts); err != nil {
		return nil, err
	}
	return utils.CreateSafeHTTPClient(utils.SafeHTTPClientOptions{
		Timeout:              15 * time.Second,
		PrivateHostWhitelist: modelFetchPrivateHostWhitelist,
		AllowPrivateHosts:    allowPrivateHosts,
	})
}

func fetchNearAIModels(ctx context.Context, fetchURL, apiKey string) ([]upstreamModel, error) {
	client, err := newSafeModelFetchClient("nearai", fetchURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
	if err != nil {
		return nil, err
	}
	if apiKey = strings.TrimSpace(apiKey); apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	utils.AllowConfiguredProxyFirstHop(req, client.Transport)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("nearai returned status %d", resp.StatusCode)
	}

	var parsed struct {
		Models []struct {
			ModelID  string `json:"modelId"`
			OwnedBy  string `json:"ownedBy"`
			Metadata struct {
				OwnedBy string `json:"ownedBy"`
			} `json:"metadata"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}

	models := make([]upstreamModel, 0, len(parsed.Models))
	for _, m := range parsed.Models {
		id := strings.TrimSpace(m.ModelID)
		if id == "" {
			continue
		}
		ownedBy := strings.TrimSpace(m.OwnedBy)
		if ownedBy == "" {
			ownedBy = strings.TrimSpace(m.Metadata.OwnedBy)
		}
		models = append(models, upstreamModel{ID: id, OwnedBy: ownedBy})
	}
	return models, nil
}

func fetchOpenAICompatibleModels(ctx context.Context, fetchURL, apiKey string) ([]upstreamModel, error) {
	return fetchOpenAICompatibleModelsForProvider(ctx, "openai", fetchURL, apiKey)
}

func fetchOpenAICompatibleModelsForProvider(
	ctx context.Context,
	provider, fetchURL, apiKey string,
) ([]upstreamModel, error) {
	client, err := newSafeModelFetchClient(provider, fetchURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
	if err != nil {
		return nil, err
	}
	if apiKey = strings.TrimSpace(apiKey); apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	utils.AllowConfiguredProxyFirstHop(req, client.Transport)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	type modelItem struct {
		ID      string `json:"id"`
		OwnedBy string `json:"owned_by"`
	}

	var envelope struct {
		Data []modelItem `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Data != nil {
		models := make([]upstreamModel, 0, len(envelope.Data))
		for _, m := range envelope.Data {
			if m.ID != "" {
				models = append(models, upstreamModel(m))
			}
		}
		return models, nil
	}

	var arr []modelItem
	if err := json.Unmarshal(body, &arr); err == nil {
		models := make([]upstreamModel, 0, len(arr))
		for _, m := range arr {
			if m.ID != "" {
				models = append(models, upstreamModel(m))
			}
		}
		return models, nil
	}

	preview := body
	if len(preview) > 256 {
		preview = preview[:256]
	}
	return nil, fmt.Errorf("decode response: unrecognized shape: %s", strings.TrimSpace(string(preview)))
}

func fetchOllamaModels(ctx context.Context, fetchURL string) ([]upstreamModel, error) {
	client, err := newSafeModelFetchClient("ollama", fetchURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
	if err != nil {
		return nil, err
	}
	utils.AllowConfiguredProxyFirstHop(req, client.Transport)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama returned status %d", resp.StatusCode)
	}

	var parsed struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}

	models := make([]upstreamModel, 0, len(parsed.Models))
	for _, m := range parsed.Models {
		id := m.Name
		if id == "" {
			id = m.Model
		}
		if id != "" {
			models = append(models, upstreamModel{ID: id})
		}
	}
	return models, nil
}

// normalizeAPIBaseForCompare normalizes an API base URL for equality comparison
// by trimming trailing slashes and lowering the scheme/host.
func normalizeAPIBaseForCompare(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	raw = strings.TrimRight(raw, "/")
	u, err := url.Parse(raw)
	if err != nil {
		return strings.ToLower(raw)
	}
	if u.Host == "" {
		u, err = url.Parse("//" + raw)
		if err != nil {
			return strings.ToLower(raw)
		}
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + strings.TrimRight(u.Path, "/")
}

// handleTestModel tests connectivity to a model endpoint.
//
//	POST /api/models/{index}/test
func (h *Handler) handleTestModel(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		http.Error(w, "Invalid index", http.StatusBadRequest)
		return
	}

	cfg, err := h.readConfig()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	if idx < 0 || idx >= len(cfg.ModelList) {
		http.Error(w, fmt.Sprintf("Index %d out of range (0-%d)", idx, len(cfg.ModelList)-1), http.StatusNotFound)
		return
	}

	m := cfg.ModelList[idx]
	start := time.Now()
	summary := modelConfigurationStatus(m)
	latency := time.Since(start).Milliseconds()

	result := map[string]any{
		"success":    summary.Available,
		"latency_ms": latency,
		"status":     summary.Status,
	}

	if !summary.Available {
		if summary.Status == modelStatusUnconfigured {
			result["error"] = "API key not configured"
		} else {
			result["error"] = "Endpoint unreachable"
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// handleTestInlineModel tests connectivity using inline (unsaved) parameters.
// Unlike handleTestModel which only checks saved config, this endpoint performs
// a real network probe (e.g. GET /models) to verify the endpoint is reachable.
//
//	POST /api/models/test-inline
func (h *Handler) handleTestInlineModel(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	var req struct {
		Provider   string `json:"provider"`
		Model      string `json:"model"`
		APIBase    string `json:"api_base"`
		APIKey     string `json:"api_key"`
		AuthMethod string `json:"auth_method"`
		ModelIndex *int   `json:"model_index"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	m := &config.ModelConfig{
		Provider:   strings.TrimSpace(req.Provider),
		Model:      strings.TrimSpace(req.Model),
		APIBase:    strings.TrimSpace(req.APIBase),
		AuthMethod: strings.TrimSpace(req.AuthMethod),
	}
	if req.APIKey != "" {
		m.SetAPIKey(req.APIKey)
	}

	// When api_key is empty and model_index is provided, fall back to stored credentials.
	// This lets the edit form test unsaved field changes while using the saved key.
	// Only reuse the stored key when the provider and effective API base match
	// the saved model, to prevent attaching a credential to a different endpoint.
	if req.APIKey == "" && req.ModelIndex != nil {
		cfg, err := h.readConfig()
		if err == nil && *req.ModelIndex >= 0 && *req.ModelIndex < len(cfg.ModelList) {
			stored := cfg.ModelList[*req.ModelIndex]
			storedProvider := stored.Provider
			reqProvider := providers.NormalizeProvider(m.Provider)
			providerMatch := reqProvider == "" || reqProvider == providers.NormalizeProvider(storedProvider)

			effectiveReqBase := strings.TrimSpace(m.APIBase)
			if effectiveReqBase == "" {
				effectiveReqBase = providers.DefaultAPIBaseForProtocol(reqProvider)
			}
			effectiveStoredBase := strings.TrimSpace(stored.APIBase)
			if effectiveStoredBase == "" {
				effectiveStoredBase = providers.DefaultAPIBaseForProtocol(storedProvider)
			}
			baseMatch := normalizeAPIBaseForCompare(effectiveReqBase) == normalizeAPIBaseForCompare(effectiveStoredBase)

			if providerMatch && baseMatch {
				if stored.APIKey() != "" {
					m.SetAPIKey(stored.APIKey())
				}
				if m.APIBase == "" && stored.APIBase != "" {
					m.APIBase = stored.APIBase
				}
			}
		}
	}

	// Check if configuration exists
	if !hasModelConfiguration(m) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success":    false,
			"latency_ms": 0,
			"status":     modelStatusUnconfigured,
			"error":      "API key not configured",
		})
		return
	}

	// Perform a real network probe
	start := time.Now()
	available := probeModelConnectivity(m)
	latency := time.Since(start).Milliseconds()

	result := map[string]any{
		"success":    available,
		"latency_ms": latency,
	}
	if available {
		result["status"] = modelStatusAvailable
	} else {
		result["status"] = modelStatusUnreachable
		result["error"] = "Endpoint unreachable"
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// probeModelConnectivity performs a real network probe to verify model endpoint reachability.
func probeModelConnectivity(m *config.ModelConfig) bool {
	apiBase := modelProbeAPIBase(m)
	protocol, modelID := splitModel(m)

	switch protocol {
	case "ollama":
		return probeOllamaModel(apiBase, modelID)
	case "vllm", "lmstudio", "gpt4free":
		return probeOpenAICompatibleModel(apiBase, modelID, m.APIKey())
	case "github-copilot":
		return probeTCPService(apiBase)
	case "claude-cli":
		return probeCommandAvailable("claude")
	case "codex-cli":
		return probeCommandAvailable("codex")
	default:
		// For remote providers (OpenAI, Anthropic, Gemini, DeepSeek, etc.),
		// make a real GET /models request to verify connectivity and credentials.
		if apiBase != "" {
			return probeOpenAICompatibleModel(apiBase, modelID, m.APIKey())
		}
		return false
	}
}
