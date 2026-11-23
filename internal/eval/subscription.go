package eval

import (
	"errors"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/auth"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

const (
	modelAccessSubscription = "subscription"
	modelAccessAPIKey       = "api-key"
	subscriptionAuthPath    = ".security/subscription-auth.json"
	subscriptionRunTimeout  = 6 * time.Minute
)

var ErrSubscriptionProfile = errors.New(
	"eval: subscription requires a fresh OAuth profile and one scripted General case without a judge",
)

func validSubscriptionPreset(preset Preset) bool {
	if preset.TargetCallBudget == nil || preset.TargetCallBudget.MaxCalls > 8 ||
		preset.UserModel != "" || preset.JudgeModel != "" || preset.ActiveTool != nil ||
		!slices.Equal(preset.Capabilities, []string{"conversation"}) {
		return false
	}
	_, err := providers.NewSubscriptionCallBudget(*preset.TargetCallBudget)
	return err == nil
}

func validModelAccess(cfg *config.Config, preset Preset) bool {
	switch preset.ModelAccess {
	case modelAccessSubscription:
		if len(cfg.ModelList) != 1 || cfg.Credentials.Enabled || cfg.Tools.Computer.Enabled {
			return false
		}
		model := cfg.ModelList[0]
		return model.Enabled && model.Provider == "openai" && model.AuthMethod == "oauth" &&
			len(model.APIKeys) == 0 && model.APIBase == "" && model.Proxy == "" &&
			len(model.CustomHeaders) == 0 && len(model.Fallbacks) == 0 && model.Capabilities == nil &&
			(model.ToolSchemaTransform == "" || model.ToolSchemaTransform == "off")
	case modelAccessAPIKey:
		for _, model := range cfg.ModelList {
			if model.Enabled && model.AuthMethod != "" {
				return false
			}
		}
	}
	return true
}

// The gateway gets only the explicit private auth store. The parent never
// resolves the ambient MintClaw/Codex login or refreshes the admitted token.
func subscriptionEvidenceFilter(profile preparedProfile) (*strings.Replacer, error) {
	root, err := os.OpenRoot(profile.home)
	if err != nil {
		return nil, ErrSubscriptionProfile
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(".security")
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return nil, ErrSubscriptionProfile
	}
	data, _, err := readCensusFile(root, subscriptionAuthPath, MaxManifestBytes)
	if err != nil {
		return nil, ErrSubscriptionProfile
	}
	defer clear(data)
	store, err := DecodeManifest[auth.AuthStore](data)
	cred := store.Credentials["openai"]
	if err != nil || len(store.Credentials) != 1 || cred == nil || cred.Provider != "openai" ||
		cred.AuthMethod != "oauth" || len(cred.AccessToken) < 24 || len(cred.AccessToken) > 16*1024 ||
		strings.ContainsAny(cred.AccessToken, " \t\r\n") ||
		!cred.ExpiresAt.After(time.Now().Add(subscriptionRunTimeout)) {
		return nil, ErrSubscriptionProfile
	}
	return profile.config.SensitiveDataReplacerWith(
		cred.AccessToken,
		cred.RefreshToken,
		cred.AccountID,
		cred.Email,
	), nil
}
