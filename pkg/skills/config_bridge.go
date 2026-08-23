package skills

import "github.com/bogdanovich/mintclaw/pkg/config"

func registryProvidersFromToolsConfig(cfg config.SkillsToolsConfig) []RegistryProvider {
	providers := make([]RegistryProvider, 0, len(cfg.Registries))
	for _, name := range cfg.Registries.Names() {
		registryCfg, _ := cfg.Registries.Get(name)
		provider := buildRegistryProvider(name, registryCfg)
		if provider == nil {
			continue
		}
		providers = append(providers, provider)
	}
	return providers
}

func NewRegistryManagerFromToolsConfig(cfg config.SkillsToolsConfig) *RegistryManager {
	return NewRegistryManagerFromConfig(RegistryConfig{
		Providers:             registryProvidersFromToolsConfig(cfg),
		MaxConcurrentSearches: cfg.MaxConcurrentSearches,
	})
}

func LookupRegistryFromToolsConfig(cfg config.SkillsToolsConfig, name string) SkillRegistry {
	registryCfg, ok := cfg.Registries.Get(name)
	if !ok {
		return nil
	}
	provider := buildRegistryProvider(name, registryCfg)
	if provider == nil {
		return nil
	}
	return provider.BuildRegistry()
}

func GitHubInstallDirNameFromToolsConfig(cfg config.SkillsToolsConfig, target string) (string, error) {
	registryCfg, _ := cfg.Registries.Get("github")
	return githubInstallDirNameWithBaseURL(target, registryCfg.BaseURL)
}

func NormalizeInstallTargetForRegistry(cfg config.SkillsToolsConfig, registryName, target string) string {
	if registryName == "" || target == "" {
		return target
	}
	registry := LookupRegistryFromToolsConfig(cfg, registryName)
	if registry == nil {
		return target
	}
	ghRegistry, ok := registry.(*GitHubRegistry)
	if !ok {
		return target
	}
	normalized, err := canonicalGitHubRegistrySlugWithBaseURL(target, ghRegistry.webBase)
	if err != nil || normalized == "" {
		return target
	}
	return normalized
}

func BuildInstallMetadataForRegistryInstance(registry SkillRegistry, target, version string) (string, string) {
	normalizedTarget := NormalizeInstallTargetForRegistryInstance(registry, target)
	if registry == nil {
		return normalizedTarget, ""
	}
	registryURL := registry.SkillURL(target, version)
	if registryURL == "" {
		registryURL = registry.SkillURL(normalizedTarget, version)
	}
	return normalizedTarget, registryURL
}
