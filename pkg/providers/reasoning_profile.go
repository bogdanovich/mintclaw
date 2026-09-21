package providers

import (
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/reasoning"
)

// ReasoningProfile returns the verified user-facing reasoning controls for one
// concrete configured route. Unknown routes deliberately return an empty
// profile rather than inheriting a provider-wide ladder that a model may reject.
func ReasoningProfile(model *config.ModelConfig) reasoning.Profile {
	if model == nil {
		return reasoning.Profile{}
	}
	if model.Reasoning != nil {
		profile, err := model.Reasoning.Profile()
		if err == nil {
			return withConfiguredReasoningDefault(profile, model.ThinkingLevel)
		}
		return reasoning.Profile{}
	}

	protocol, modelID := ExtractProtocol(model)
	authMethod := strings.ToLower(strings.TrimSpace(model.AuthMethod))
	if protocol == "openai" && (authMethod == "oauth" || authMethod == "token") {
		if catalogModel, ok := BundledCodexModel(modelID); ok {
			profile := codexReasoningProfile(catalogModel)
			return withConfiguredReasoningDefault(profile, model.ThinkingLevel)
		}
	}

	// A legacy configured level is one verified working value, not evidence for
	// a full provider-wide ladder.
	if effort, ok := reasoning.Parse(model.ThinkingLevel); ok {
		profile, err := reasoning.NewProfile(
			[]reasoning.Effort{effort},
			effort,
			effort != reasoning.EffortOff,
			"legacy",
		)
		if err == nil {
			return profile
		}
	}
	return reasoning.Profile{}
}

func codexReasoningProfile(model CodexModelInfo) reasoning.Profile {
	efforts := make([]reasoning.Effort, 0, len(model.SupportedReasoningLevels))
	descriptions := make(map[reasoning.Effort]string, len(model.SupportedReasoningLevels))
	for _, level := range model.SupportedReasoningLevels {
		effort, ok := reasoning.Parse(level.Effort)
		if !ok {
			// Codex "ultra" also changes orchestration policy. MintClaw must not
			// advertise it as an ordinary provider effort until it implements that
			// policy, even when the remote catalog includes it.
			continue
		}
		efforts = append(efforts, effort)
		descriptions[effort] = strings.TrimSpace(level.Description)
	}
	defaultEffort, _ := reasoning.Parse(model.DefaultReasoningLevel)
	profile, err := reasoning.NewProfile(efforts, defaultEffort, true, "bundled_codex_catalog")
	if err != nil {
		return reasoning.Profile{}
	}
	for index := range profile.Options {
		profile.Options[index].Description = descriptions[profile.Options[index].ID]
	}
	return profile
}

func withConfiguredReasoningDefault(profile reasoning.Profile, configured string) reasoning.Profile {
	effort, ok := reasoning.Parse(configured)
	if ok && profile.Supports(effort) {
		profile.Default = effort
	}
	return profile
}
