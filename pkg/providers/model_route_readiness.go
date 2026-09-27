package providers

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

// ModelRouteReadiness describes whether a configured model route has the local
// prerequisites needed to be selected. It deliberately does not contact the
// remote provider, so an available route can still fail later because of an
// invalid key, an unavailable endpoint, or exhausted quota.
type ModelRouteReadiness struct {
	Available bool
	Reason    string
	SetupHint string
}

var modelRouteLookPath = exec.LookPath

// CheckModelRouteReadiness performs a secret-free, network-free readiness
// check for one configured model route.
func CheckModelRouteReadiness(cfg *config.ModelConfig) ModelRouteReadiness {
	if cfg == nil {
		return modelRouteNeedsSetup("Invalid model route", "Add a complete model_list entry.")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return modelRouteNeedsSetup("Model ID required", "Set model on this model route.")
	}
	protocol := NormalizeProvider(cfg.Provider)
	if protocol == "" {
		return modelRouteNeedsSetup("Provider required", "Set provider on this model route.")
	}

	authMethod := strings.ToLower(strings.TrimSpace(cfg.AuthMethod))
	switch protocol {
	case "openai":
		if authMethod == "oauth" || authMethod == "token" {
			return oauthModelRouteReadiness("openai", "openai")
		}
		return apiKeyOrEndpointReadiness(cfg)
	case "anthropic":
		if authMethod == "oauth" || authMethod == "token" {
			return oauthModelRouteReadiness("anthropic", "anthropic")
		}
		if cfg.APIKey() == "" {
			return modelRouteNeedsSetup(
				"API key required",
				"Add api_keys to this Anthropic model route.",
			)
		}
		return availableModelRoute()
	case "antigravity":
		return oauthModelRouteReadiness("google-antigravity", "google-antigravity")
	case "azure":
		if strings.TrimSpace(cfg.APIBase) == "" {
			return modelRouteNeedsSetup(
				"Azure endpoint required",
				"Set api_base to the Azure OpenAI deployment endpoint.",
			)
		}
		// Azure may use either api_keys or the ambient DefaultAzureCredential chain.
		return availableModelRoute()
	case "bedrock":
		// Bedrock resolves ambient AWS credentials when the provider is constructed.
		return availableModelRoute()
	case "gemini", "minimax":
		return apiKeyOrEndpointReadiness(cfg)
	case "anthropic-messages", "alibaba-coding-anthropic":
		if cfg.APIKey() == "" {
			return modelRouteNeedsSetup(
				"API key required",
				fmt.Sprintf("Add api_keys to this %s model route.", protocol),
			)
		}
		return availableModelRoute()
	case "claude-cli":
		return cliModelRouteReadiness("claude", "Install and authenticate the Claude CLI.")
	case "codex-cli":
		return cliModelRouteReadiness("codex", "Install and authenticate the Codex CLI.")
	case "github-copilot":
		// The local bridge is contacted only when a turn starts. Avoid adding a
		// startup network probe to the model picker.
		return availableModelRoute()
	case "litellm", "lmstudio", "gpt4free", "openrouter", "groq", "zhipu", "nvidia", "venice",
		"nearai", "ollama", "moonshot", "shengsuanyun", "siliconflow", "deepseek", "cerebras",
		"vivgrid", "volcengine", "vllm", "qwen-portal", "qwen-intl", "qwen-us", "mistral",
		"avian", "longcat", "modelscope", "novita", "alibaba-coding", "zai", "mimo":
		if cfg.APIKey() == "" && strings.TrimSpace(cfg.APIBase) == "" && !isEmptyAPIKeyAllowed(protocol) {
			return modelRouteNeedsSetup(
				"API key or endpoint required",
				"Add api_keys or api_base to this model route.",
			)
		}
		return availableModelRoute()
	default:
		return modelRouteNeedsSetup(
			"Unsupported coding provider",
			fmt.Sprintf("Change provider %q to a supported coding provider.", protocol),
		)
	}
}

func apiKeyOrEndpointReadiness(cfg *config.ModelConfig) ModelRouteReadiness {
	if cfg.APIKey() == "" && strings.TrimSpace(cfg.APIBase) == "" {
		return modelRouteNeedsSetup(
			"API key or endpoint required",
			"Add api_keys or api_base to this model route.",
		)
	}
	return availableModelRoute()
}

func oauthModelRouteReadiness(credentialProvider, loginProvider string) ModelRouteReadiness {
	credential, err := getCredential(credentialProvider)
	if err != nil {
		return modelRouteNeedsSetup(
			"Authentication status unavailable",
			"Check the MintClaw auth store permissions and retry.",
		)
	}
	if credential == nil || strings.TrimSpace(credential.AccessToken) == "" {
		return modelRouteNeedsSetup(
			"Login required",
			fmt.Sprintf("Run mintclaw auth login --provider %s.", loginProvider),
		)
	}
	if credential.IsExpired() && strings.TrimSpace(credential.RefreshToken) == "" {
		return modelRouteNeedsSetup(
			"Login expired",
			fmt.Sprintf("Run mintclaw auth login --provider %s again.", loginProvider),
		)
	}
	return availableModelRoute()
}

func cliModelRouteReadiness(command, hint string) ModelRouteReadiness {
	if _, err := modelRouteLookPath(command); err != nil {
		return modelRouteNeedsSetup(strings.ToUpper(command[:1])+command[1:]+" CLI not found", hint)
	}
	return availableModelRoute()
}

func availableModelRoute() ModelRouteReadiness {
	return ModelRouteReadiness{Available: true}
}

func modelRouteNeedsSetup(reason, hint string) ModelRouteReadiness {
	return ModelRouteReadiness{Reason: reason, SetupHint: hint}
}
