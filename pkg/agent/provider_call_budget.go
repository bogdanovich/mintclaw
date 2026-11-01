package agent

import (
	"fmt"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

// Construction runs before the context manager captures a provider and before
// shared tools are registered. Existing provider ownership remains unchanged:
// wrappers do not add another cleanup owner for registry-created delegates.
func (al *AgentLoop) applyProviderCallBudget(
	injected providers.LLMProvider,
) (providers.LLMProvider, error) {
	if al.cfg.Gateway.HotReload {
		return nil, fmt.Errorf("%w: bounded runtime cannot enable reload", providers.ErrCallBudget)
	}
	wrapped, err := al.providerBudget.Wrap(injected)
	if err != nil {
		return nil, err
	}
	for _, agentID := range al.registry.ListAgentIDs() {
		instance, ok := al.registry.GetAgent(agentID)
		if !ok || instance == nil {
			return nil, fmt.Errorf("%w: missing registry agent", providers.ErrCallBudget)
		}
		instance.Provider, err = al.providerBudget.Wrap(instance.Provider)
		if err != nil {
			return nil, err
		}
		if instance.LightProvider != nil {
			instance.LightProvider, err = al.providerBudget.Wrap(instance.LightProvider)
			if err != nil {
				return nil, err
			}
		}
		for key, candidate := range instance.CandidateProviders {
			instance.CandidateProviders[key], err = al.providerBudget.Wrap(candidate)
			if err != nil {
				return nil, err
			}
		}
	}
	al.providerFactory = budgetedModelProviderFactory(al.providerBudget, al.providerFactory)
	return wrapped, nil
}

func budgetedModelProviderFactory(
	budget *providers.CallBudget,
	factory modelProviderFactory,
) modelProviderFactory {
	return func(model *config.ModelConfig) (providers.LLMProvider, string, error) {
		created, modelID, err := factory(model)
		if err != nil {
			return nil, "", err
		}
		wrapped, err := budget.Wrap(created)
		if err != nil {
			if !runtimeDependencyIsNil(created) {
				closeProviderIfStateful(created)
			}
			return nil, "", err
		}
		return wrapped, modelID, nil
	}
}
