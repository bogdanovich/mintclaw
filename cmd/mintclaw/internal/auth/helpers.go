package auth

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/cmd/mintclaw/internal"
	"github.com/bogdanovich/mintclaw/pkg/auth"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

const (
	supportedProvidersMsg = "supported providers: openai, anthropic, google-antigravity, antigravity"
	defaultAnthropicModel = "claude-sonnet-4.6"
)

func authLoginCmd(provider string, useDeviceCode bool, useOauth bool, noBrowser bool) error {
	switch provider {
	case "openai":
		return authLoginOpenAI(useDeviceCode, noBrowser)
	case "anthropic":
		return authLoginAnthropic(useOauth)
	case "google-antigravity", "antigravity":
		return authLoginGoogleAntigravity(noBrowser)
	default:
		return fmt.Errorf("unsupported provider: %s (%s)", provider, supportedProvidersMsg)
	}
}

func authLoginOpenAI(useDeviceCode bool, noBrowser bool) error {
	cfg := auth.OpenAIOAuthConfig()

	var cred *auth.AuthCredential
	var err error

	if useDeviceCode {
		cred, err = auth.LoginDeviceCode(cfg)
	} else {
		cred, err = auth.LoginBrowserWithOptions(cfg, auth.LoginBrowserOptions{NoBrowser: noBrowser})
	}

	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	if err = auth.SetCredential("openai", cred); err != nil {
		return fmt.Errorf("failed to save credentials: %w", err)
	}

	model := preferredOpenAIModel(cred)
	appCfg, err := updateAuthConfig(func(cfg *config.Config) error {
		configureOpenAIAuth(cfg, "oauth", model)
		return nil
	})
	if err != nil {
		return fmt.Errorf("could not update config: %w", err)
	}

	fmt.Println("Login successful!")
	if cred.AccountID != "" {
		fmt.Printf("Account: %s\n", cred.AccountID)
	}
	fmt.Printf("Default model set to: %s\n", configuredDefaultModel(appCfg, model.Slug))

	return nil
}

func configuredDefaultModel(cfg *config.Config, fallback string) string {
	if cfg != nil {
		if model := strings.TrimSpace(cfg.Agents.Defaults.GetModelName()); model != "" {
			return model
		}
	}
	return strings.TrimSpace(fallback)
}

func authLoginGoogleAntigravity(noBrowser bool) error {
	cfg := auth.GoogleAntigravityOAuthConfig()

	cred, err := auth.LoginBrowserWithOptions(cfg, auth.LoginBrowserOptions{NoBrowser: noBrowser})
	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	cred.Provider = "google-antigravity"

	// Fetch user email from Google userinfo
	email, err := fetchGoogleUserEmail(context.Background(), cred.AccessToken)
	if err != nil {
		fmt.Printf("Warning: could not fetch email: %v\n", err)
	} else {
		cred.Email = email
		fmt.Printf("Email: %s\n", email)
	}

	// Fetch Cloud Code Assist project ID
	projectID, err := providers.FetchAntigravityProjectIDWithContext(context.Background(), cred.AccessToken)
	if err != nil {
		fmt.Printf("Warning: could not fetch project ID: %v\n", err)
		fmt.Println("You may need Google Cloud Code Assist enabled on your account.")
	} else {
		cred.ProjectID = projectID
		fmt.Printf("Project: %s\n", projectID)
	}

	if err = auth.SetCredential("google-antigravity", cred); err != nil {
		return fmt.Errorf("failed to save credentials: %w", err)
	}

	if _, err = updateAuthConfig(func(cfg *config.Config) error {
		configureAntigravityAuth(cfg, "oauth")
		return nil
	}); err != nil {
		fmt.Printf("Warning: could not update config: %v\n", err)
	}

	fmt.Println("\n✓ Google Antigravity login successful!")
	fmt.Println("Default model set to: gemini-flash")
	fmt.Println("Try it: mintclaw agent -m \"Hello world\"")

	return nil
}

func authLoginAnthropic(useOauth bool) error {
	if useOauth {
		return authLoginAnthropicSetupToken()
	}

	fmt.Println("Anthropic login method:")
	fmt.Println("  1) Setup token (from `claude setup-token`) (Recommended)")
	fmt.Println("  2) API key (from console.anthropic.com)")

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("Choose [1]: ")
		choice := "1"
		if scanner.Scan() {
			text := strings.TrimSpace(scanner.Text())
			if text != "" {
				choice = text
			}
		}

		switch choice {
		case "1":
			return authLoginAnthropicSetupToken()
		case "2":
			return authLoginPasteToken("anthropic")
		default:
			fmt.Printf("Invalid choice: %s. Please enter 1 or 2.\n", choice)
		}
	}
}

func authLoginAnthropicSetupToken() error {
	cred, err := auth.LoginSetupToken(os.Stdin)
	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	if err = auth.SetCredential("anthropic", cred); err != nil {
		return fmt.Errorf("failed to save credentials: %w", err)
	}

	if _, err = updateAuthConfig(func(cfg *config.Config) error {
		configureAnthropicAuth(cfg, "oauth", false)
		return nil
	}); err != nil {
		return fmt.Errorf("could not update config: %w", err)
	}

	fmt.Println("Setup token saved for Anthropic!")

	return nil
}

func fetchGoogleUserEmail(ctx context.Context, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://www.googleapis.com/oauth2/v2/userinfo", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading userinfo response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("userinfo request failed: %s", string(body))
	}

	var userInfo struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(body, &userInfo); err != nil {
		return "", err
	}
	return userInfo.Email, nil
}

func authLoginPasteToken(provider string) error {
	cred, err := auth.LoginPasteToken(provider, os.Stdin)
	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	if err = auth.SetCredential(provider, cred); err != nil {
		return fmt.Errorf("failed to save credentials: %w", err)
	}
	var openAIModel providers.CodexModelInfo
	if provider == "openai" {
		openAIModel = preferredOpenAIModel(cred)
	}

	appCfg, err := updateAuthConfig(func(appCfg *config.Config) error {
		switch provider {
		case "anthropic":
			configureAnthropicAuth(appCfg, "token", true)
		case "openai":
			configureOpenAIAuth(appCfg, "token", openAIModel)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("could not update config: %w", err)
	}

	fmt.Printf("Token saved for %s!\n", provider)

	if appCfg != nil {
		fmt.Printf("Default model set to: %s\n", appCfg.Agents.Defaults.GetModelName())
	}

	return nil
}

func configureOpenAIAuth(cfg *config.Config, method string, selected providers.CodexModelInfo) {
	if strings.TrimSpace(selected.Slug) == "" {
		selected = providers.DefaultCodexModelInfo()
	}
	selectedID := normalizeOpenAIModelID(selected.Slug)
	for _, model := range cfg.ModelList {
		if isOpenAIModel(model) && normalizeOpenAIModelID(model.Model) == selectedID &&
			openAIModelAliasIsUnambiguous(cfg, model, selectedID) {
			model.AuthMethod = method
			model.Enabled = true
			model.ContextWindow = selected.ContextWindow
			model.MaxContextWindow = selected.MaxContextWindow
			cfg.Agents.Defaults.ModelName = model.ModelName
			return
		}
	}
	modelName := availableOpenAIModelAlias(cfg, selected.Slug)
	cfg.ModelList = append(cfg.ModelList, &config.ModelConfig{
		ModelName:        modelName,
		Provider:         "openai",
		Model:            selected.Slug,
		AuthMethod:       method,
		ContextWindow:    selected.ContextWindow,
		MaxContextWindow: selected.MaxContextWindow,
		Enabled:          true,
	})
	cfg.Agents.Defaults.ModelName = modelName
}

func normalizeOpenAIModelID(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.TrimPrefix(model, "openai/")
}

func openAIModelAliasIsUnambiguous(
	cfg *config.Config,
	candidate *config.ModelConfig,
	selectedID string,
) bool {
	alias := strings.TrimSpace(candidate.ModelName)
	if alias == "" {
		return false
	}
	for _, model := range cfg.ModelList {
		if model == nil || model == candidate || !model.Enabled || model.ModelName != alias {
			continue
		}
		if !isOpenAIModel(model) || normalizeOpenAIModelID(model.Model) != selectedID {
			return false
		}
	}
	return true
}

func availableOpenAIModelAlias(cfg *config.Config, slug string) string {
	base := strings.TrimSpace(slug)
	if base == "" {
		base = providers.CodexDefaultModel
	}
	candidates := []string{base, "openai/" + base, base + "-openai"}
	for suffix := 2; ; suffix++ {
		for _, candidate := range candidates {
			if modelAliasIsAvailable(cfg, candidate) {
				return candidate
			}
		}
		candidates = []string{fmt.Sprintf("%s-openai-%d", base, suffix)}
	}
}

func modelAliasIsAvailable(cfg *config.Config, alias string) bool {
	for _, model := range cfg.ModelList {
		if model != nil && model.Enabled && model.ModelName == alias {
			return false
		}
	}
	return true
}

func preferredOpenAIModel(credential *auth.AuthCredential) providers.CodexModelInfo {
	fallback := providers.DefaultCodexModelInfo()
	if credential == nil || strings.TrimSpace(credential.AccessToken) == "" {
		return fallback
	}
	models, err := providers.FetchCodexModels(
		context.Background(),
		credential.AccessToken,
		credential.AccountID,
	)
	if err != nil {
		fmt.Printf("Warning: could not refresh OpenAI model catalog: %v\n", err)
		return fallback
	}
	return providers.PreferredCodexModel(models)
}

func updateAuthConfig(mutate func(*config.Config) error) (*config.Config, error) {
	if !authConfigReadable() {
		return nil, nil
	}
	snapshot, err := internal.UpdateConfig(mutate)
	if err != nil {
		return nil, err
	}
	return snapshot.Config, nil
}

func authConfigReadable() bool {
	_, err := internal.LoadConfig()
	return err == nil
}

func configureAnthropicAuth(cfg *config.Config, method string, setNewDefault bool) {
	found := false
	for _, model := range cfg.ModelList {
		if isAnthropicModel(model) {
			model.AuthMethod = method
			model.Enabled = true
			found = true
			break
		}
	}
	if found {
		return
	}
	cfg.ModelList = append(cfg.ModelList, &config.ModelConfig{
		ModelName:  defaultAnthropicModel,
		Provider:   "anthropic",
		Model:      defaultAnthropicModel,
		AuthMethod: method,
		Enabled:    true,
	})
	if setNewDefault || cfg.Agents.Defaults.GetModelName() == "" {
		cfg.Agents.Defaults.ModelName = defaultAnthropicModel
	}
}

func configureAntigravityAuth(cfg *config.Config, method string) {
	found := false
	for _, model := range cfg.ModelList {
		if isAntigravityModel(model) {
			model.AuthMethod = method
			model.Enabled = true
			found = true
			break
		}
	}
	if !found {
		cfg.ModelList = append(cfg.ModelList, &config.ModelConfig{
			ModelName:  "gemini-flash",
			Provider:   "antigravity",
			Model:      "gemini-3-flash",
			AuthMethod: method,
			Enabled:    true,
		})
	}
	cfg.Agents.Defaults.ModelName = "gemini-flash"
}

func clearProviderAuth(cfg *config.Config, provider string) {
	for _, model := range cfg.ModelList {
		matches := provider == "openai" && isOpenAIModel(model) ||
			provider == "anthropic" && isAnthropicModel(model) ||
			(provider == "google-antigravity" || provider == "antigravity") && isAntigravityModel(model)
		if matches {
			model.AuthMethod = ""
		}
	}
}

func authLogoutCmd(provider string) error {
	if provider != "" {
		if err := auth.DeleteCredential(provider); err != nil {
			return fmt.Errorf("failed to remove credentials: %w", err)
		}

		if _, err := updateAuthConfig(func(cfg *config.Config) error {
			clearProviderAuth(cfg, provider)
			return nil
		}); err != nil {
			return fmt.Errorf("could not save config: %w", err)
		}

		fmt.Printf("Logged out from %s\n", provider)

		return nil
	}

	if err := auth.DeleteAllCredentials(); err != nil {
		return fmt.Errorf("failed to remove credentials: %w", err)
	}

	if _, err := updateAuthConfig(func(cfg *config.Config) error {
		for i := range cfg.ModelList {
			cfg.ModelList[i].AuthMethod = ""
		}
		return nil
	}); err != nil {
		return fmt.Errorf("could not save config: %w", err)
	}

	fmt.Println("Logged out from all providers")

	return nil
}

func authStatusCmd() error {
	store, err := auth.LoadStore()
	if err != nil {
		return fmt.Errorf("failed to load auth store: %w", err)
	}

	if len(store.Credentials) == 0 {
		fmt.Println("No authenticated providers.")
		fmt.Println("Run: mintclaw auth login --provider <name>")
		return nil
	}

	fmt.Println("\nAuthenticated Providers:")
	fmt.Println("------------------------")
	for provider, cred := range store.Credentials {
		status := "active"
		if cred.IsExpired() {
			status = "expired"
		} else if cred.NeedsRefresh() {
			status = "needs refresh"
		}

		fmt.Printf("  %s:\n", provider)
		fmt.Printf("    Method: %s\n", cred.AuthMethod)
		fmt.Printf("    Status: %s\n", status)
		if cred.AccountID != "" {
			fmt.Printf("    Account: %s\n", cred.AccountID)
		}
		if cred.Email != "" {
			fmt.Printf("    Email: %s\n", cred.Email)
		}
		if cred.ProjectID != "" {
			fmt.Printf("    Project: %s\n", cred.ProjectID)
		}
		if !cred.ExpiresAt.IsZero() {
			fmt.Printf("    Expires: %s\n", cred.ExpiresAt.Format("2006-01-02 15:04"))
		}

		if provider == "anthropic" && cred.AuthMethod == "oauth" {
			usage, err := auth.FetchAnthropicUsageWithContext(context.Background(), cred.AccessToken)
			if err != nil {
				fmt.Printf("    Usage: unavailable (%v)\n", err)
			} else {
				fmt.Printf("    Usage (5h):  %.1f%%\n", usage.FiveHourUtilization*100)
				fmt.Printf("    Usage (7d):  %.1f%%\n", usage.SevenDayUtilization*100)
			}
		}
	}

	return nil
}

func authModelsCmd() error {
	cred, err := auth.GetCredential("google-antigravity")
	if err != nil || cred == nil {
		return fmt.Errorf(
			"not logged in to Google Antigravity.\nrun: mintclaw auth login --provider google-antigravity",
		)
	}

	// Refresh token if needed
	if cred.NeedsRefresh() && cred.RefreshToken != "" {
		oauthCfg := auth.GoogleAntigravityOAuthConfig()
		refreshed, refreshErr := auth.RefreshAccessToken(cred, oauthCfg)
		if refreshErr == nil {
			cred = refreshed
			_ = auth.SetCredential("google-antigravity", cred)
		}
	}

	projectID := cred.ProjectID
	if projectID == "" {
		return fmt.Errorf("no project id stored. Try logging in again")
	}

	fmt.Printf("Fetching models for project: %s\n\n", projectID)

	models, err := providers.FetchAntigravityModelsWithContext(context.Background(), cred.AccessToken, projectID)
	if err != nil {
		return fmt.Errorf("error fetching models: %w", err)
	}

	if len(models) == 0 {
		return fmt.Errorf("no models available")
	}

	fmt.Println("Available Antigravity Models:")
	fmt.Println("-----------------------------")
	for _, m := range models {
		status := "✓"
		if m.IsExhausted {
			status = "✗ (quota exhausted)"
		}
		name := m.ID
		if m.DisplayName != "" {
			name = fmt.Sprintf("%s (%s)", m.ID, m.DisplayName)
		}
		fmt.Printf("  %s %s\n", status, name)
	}

	return nil
}

// isAntigravityModel checks if a model config belongs to an Antigravity provider.
func isAntigravityModel(modelCfg *config.ModelConfig) bool {
	protocol, _ := providers.ExtractProtocol(modelCfg)
	return protocol == "antigravity" || protocol == "google-antigravity"
}

// isOpenAIModel checks if a model config belongs to the OpenAI provider.
func isOpenAIModel(modelCfg *config.ModelConfig) bool {
	protocol, _ := providers.ExtractProtocol(modelCfg)
	return protocol == "openai"
}

// isAnthropicModel checks if a model config belongs to the Anthropic provider.
func isAnthropicModel(modelCfg *config.ModelConfig) bool {
	protocol, _ := providers.ExtractProtocol(modelCfg)
	return protocol == "anthropic"
}
