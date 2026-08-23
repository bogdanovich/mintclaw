package agent

import (
	"fmt"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func requireExactModelName(value string) error {
	if value == "" {
		return fmt.Errorf("model_name is required")
	}
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("model_name must not have surrounding whitespace")
	}
	return nil
}

func modelConfigIdentityKey(mc *config.ModelConfig) string {
	if mc == nil {
		return ""
	}
	if name := strings.TrimSpace(mc.ModelName); name != "" {
		return "model_name:" + name
	}
	return ""
}

func modelProviderAndIDForResolution(mc *config.ModelConfig) (provider string, modelID string) {
	if mc == nil {
		return "", ""
	}
	return providers.ExtractProtocol(mc)
}

func cloneModelConfigForResolution(
	mc *config.ModelConfig,
	workspace string,
) *config.ModelConfig {
	if mc == nil {
		return nil
	}
	clone := *mc
	if clone.Workspace == "" {
		clone.Workspace = workspace
	}
	return &clone
}

func candidateFromModelConfig(
	mc *config.ModelConfig,
) (providers.FallbackCandidate, bool) {
	if mc == nil {
		return providers.FallbackCandidate{}, false
	}

	protocol, modelID := modelProviderAndIDForResolution(mc)
	if strings.TrimSpace(modelID) == "" {
		return providers.FallbackCandidate{}, false
	}

	return providers.FallbackCandidate{
		Provider:    protocol,
		Model:       modelID,
		DisplayName: strings.TrimSpace(mc.ModelName),
		RPM:         mc.RPM,
		IdentityKey: modelConfigIdentityKey(mc),
	}, true
}

func resolveModelCandidate(
	cfg *config.Config,
	modelName string,
) (providers.FallbackCandidate, bool) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" || cfg == nil {
		return providers.FallbackCandidate{}, false
	}
	mc, err := cfg.GetModelConfig(modelName)
	if err != nil || mc == nil {
		return providers.FallbackCandidate{}, false
	}
	return candidateFromModelConfig(mc)
}

func resolveModelCandidates(
	cfg *config.Config,
	primary string,
	fallbacks []string,
) []providers.FallbackCandidate {
	seen := make(map[string]bool)
	candidates := make([]providers.FallbackCandidate, 0, 1+len(fallbacks))

	addCandidate := func(raw string) {
		candidate, ok := resolveModelCandidate(cfg, raw)
		if !ok {
			return
		}

		key := candidate.StableKey()
		if seen[key] {
			return
		}
		seen[key] = true
		candidates = append(candidates, candidate)
	}

	addCandidate(primary)
	for _, fallback := range fallbacks {
		addCandidate(fallback)
	}

	return candidates
}

func resolvedCandidateModel(candidates []providers.FallbackCandidate, fallback string) string {
	if len(candidates) > 0 && strings.TrimSpace(candidates[0].Model) != "" {
		return candidates[0].Model
	}
	return fallback
}

func resolvedCandidateProvider(candidates []providers.FallbackCandidate, fallback string) string {
	if len(candidates) > 0 && strings.TrimSpace(candidates[0].Provider) != "" {
		return candidates[0].Provider
	}
	return fallback
}

func resolvedCandidateModelName(candidates []providers.FallbackCandidate, fallback string) string {
	if len(candidates) > 0 {
		if name := modelAliasFromCandidateIdentityKey(candidates[0].IdentityKey); strings.TrimSpace(name) != "" {
			return name
		}
		if displayName := strings.TrimSpace(candidates[0].DisplayName); displayName != "" {
			return displayName
		}
	}
	return strings.TrimSpace(fallback)
}

func resolvedModelConfig(cfg *config.Config, modelName, workspace string) (*config.ModelConfig, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}
	modelCfg, err := resolvedSwitchableModelConfig(cfg, strings.TrimSpace(modelName))
	if err != nil {
		return nil, err
	}

	clone := *modelCfg
	if clone.Workspace == "" {
		clone.Workspace = workspace
	}

	return &clone, nil
}

func resolvedSwitchableModelConfig(cfg *config.Config, modelName string) (*config.ModelConfig, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return nil, fmt.Errorf("model name is required")
	}
	var matches []*config.ModelConfig
	for _, modelCfg := range cfg.ModelList {
		if modelCfg == nil || modelCfg.IsVirtual() || !modelCfg.IsEffectivelyEnabled() {
			continue
		}
		if modelCfg.ModelName == modelName {
			matches = append(matches, modelCfg)
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("model %q not found in enabled model_list", modelName)
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	filtered := *cfg
	filtered.ModelList = matches
	return filtered.GetModelConfig(modelName)
}

func resolveActiveModelConfig(
	cfg *config.Config,
	workspace string,
	candidates []providers.FallbackCandidate,
	activeModel string,
) *config.ModelConfig {
	if cfg == nil {
		return nil
	}

	if len(candidates) > 0 {
		candidate := candidates[0]
		identityKey := strings.TrimSpace(candidate.IdentityKey)
		if identityKey != "" {
			for _, mc := range cfg.ModelList {
				if mc == nil || modelConfigIdentityKey(mc) != identityKey {
					continue
				}
				protocol, modelID := modelProviderAndIDForResolution(mc)
				if providers.ModelKey(protocol, modelID) == providers.ModelKey(candidate.Provider, candidate.Model) {
					return cloneModelConfigForResolution(mc, workspace)
				}
			}
		}
		return nil
	}

	if mc, err := cfg.GetModelConfig(strings.TrimSpace(activeModel)); err == nil {
		return cloneModelConfigForResolution(mc, workspace)
	}

	return nil
}
