package providers

import (
	"cmp"
	"slices"
)

// ModelProviderOptions returns the canonical provider catalog exposed to the Web UI.
func ModelProviderOptions() []ModelProviderOption {
	options := make([]ModelProviderOption, 0, len(modelProviderOptionsByName))
	for _, option := range modelProviderOptionsByName {
		options = append(options, option)
	}
	slices.SortFunc(options, func(a, b ModelProviderOption) int {
		return cmp.Compare(a.ID, b.ID)
	})
	return options
}

// IsSupportedModelProvider reports whether provider resolves to a provider ID
// returned by ModelProviderOptions.
func IsSupportedModelProvider(provider string) bool {
	_, ok := modelProviderOptionForName(provider)
	return ok
}

// IsModelProviderFetchable reports whether provider supports upstream /models
// listing through the launcher fetch endpoint.
func IsModelProviderFetchable(provider string) bool {
	option, ok := modelProviderOptionForName(provider)
	return ok && option.SupportsFetch
}

// IsCreatableModelProvider reports whether provider can be selected for a new
// model entry from the Web UI.
func IsCreatableModelProvider(provider string) bool {
	option, ok := modelProviderOptionForName(provider)
	return ok && option.CreateAllowed
}

// IsDefaultModelProvider reports whether provider can be used as the default
// chat model. Some providers such as ASR-only entries are intentionally
// exposed in model_list management but cannot drive the gateway default model.
func IsDefaultModelProvider(provider string) bool {
	option, ok := modelProviderOptionForName(provider)
	return ok && option.DefaultModelAllowed
}

// IsLocalModelProvider reports whether the provider is expected to run on the
// local machine or private network and may legitimately use localhost defaults.
func IsLocalModelProvider(provider string) bool {
	option, ok := modelProviderOptionForName(provider)
	return ok && option.Local
}
