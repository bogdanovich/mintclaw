package agent

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/commands"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

func (al *AgentLoop) bindModelCommandCapabilities(
	rt *commands.Runtime,
	cfg *config.Config,
	modelBinding effectiveModelBinding,
	opts *turnSpec,
	agent *AgentInstance,
) {
	if agent == nil {
		return
	}
	currentModelSelection := func() commands.ModelSelectionInfo {
		return selectionInfoForInspection(al.buildModelSelectionInspection(cfg, modelBinding))
	}
	rt.GetModelInfo = func() (string, string) {
		info := currentModelSelection()
		return info.EffectiveName, info.EffectiveProvider
	}
	rt.GetModelSelection = currentModelSelection
	rt.ListModels = func() []commands.ConfiguredModelInfo {
		return configuredModelsForCommand(cfg, currentModelSelection())
	}
	rt.SetSessionModel = func(value string) error {
		if modelBinding.RouteSessionKey == "" {
			return fmt.Errorf("conversation key not available")
		}
		modelName, err := canonicalModelOverrideValue(cfg, value)
		if err != nil {
			return err
		}
		if routeSessionKey := commandRouteSessionKey(opts); routeSessionKey != "" {
			if err = al.clearAutoModelSelection(routeSessionKey); err != nil {
				return err
			}
		}
		return al.setSessionModelOverride(modelBinding.RouteSessionKey, modelName)
	}
	rt.ClearSessionModel = func() error {
		if modelBinding.RouteSessionKey == "" {
			return fmt.Errorf("conversation key not available")
		}
		if routeSessionKey := commandRouteSessionKey(opts); routeSessionKey != "" {
			if err := al.clearAutoModelSelection(routeSessionKey); err != nil {
				return err
			}
		}
		return al.clearSessionModelOverride(modelBinding.RouteSessionKey)
	}
}

func configuredModelsForCommand(
	cfg *config.Config,
	current commands.ModelSelectionInfo,
) []commands.ConfiguredModelInfo {
	if cfg == nil || len(cfg.ModelList) == 0 {
		return nil
	}
	type targetAggregate struct {
		target commands.ConfiguredModelTarget
		order  int
	}
	type modelAggregate struct {
		info    commands.ConfiguredModelInfo
		order   int
		targets map[string]*targetAggregate
	}
	modelsByName := make(map[string]*modelAggregate)
	for index, modelCfg := range cfg.ModelList {
		if modelCfg == nil || modelCfg.IsVirtual() || !modelCfg.Enabled {
			continue
		}
		entry, ok := modelsByName[modelCfg.ModelName]
		if !ok {
			entry = &modelAggregate{
				info: commands.ConfiguredModelInfo{
					Name:    modelCfg.ModelName,
					Current: modelCfg.ModelName == current.EffectiveName,
				},
				order:   index,
				targets: map[string]*targetAggregate{},
			}
			modelsByName[modelCfg.ModelName] = entry
		} else if modelCfg.ModelName == current.EffectiveName {
			entry.info.Current = true
		}
		targetKey := strings.Join([]string{modelCfg.Provider, modelCfg.Model, modelCfg.Workspace}, "\x00")
		targetEntry, ok := entry.targets[targetKey]
		if !ok {
			targetEntry = &targetAggregate{
				target: commands.ConfiguredModelTarget{
					Provider:  modelCfg.Provider,
					Model:     modelCfg.Model,
					Workspace: modelCfg.Workspace,
					Count:     1,
				},
				order: len(entry.targets),
			}
			entry.targets[targetKey] = targetEntry
		} else {
			targetEntry.target.Count++
		}
	}

	orderedModels := make([]*modelAggregate, 0, len(modelsByName))
	for _, item := range modelsByName {
		orderedModels = append(orderedModels, item)
	}
	slices.SortFunc(orderedModels, func(a, b *modelAggregate) int {
		if comparison := cmp.Compare(strings.ToLower(a.info.Name), strings.ToLower(b.info.Name)); comparison != 0 {
			return comparison
		}
		return cmp.Compare(a.order, b.order)
	})

	models := make([]commands.ConfiguredModelInfo, 0, len(orderedModels))
	for _, item := range orderedModels {
		orderedTargets := make([]*targetAggregate, 0, len(item.targets))
		for _, target := range item.targets {
			orderedTargets = append(orderedTargets, target)
		}
		slices.SortFunc(orderedTargets, func(a, b *targetAggregate) int {
			left := strings.ToLower(a.target.Provider + "\x00" + a.target.Model + "\x00" + a.target.Workspace)
			right := strings.ToLower(b.target.Provider + "\x00" + b.target.Model + "\x00" + b.target.Workspace)
			if comparison := cmp.Compare(left, right); comparison != 0 {
				return comparison
			}
			return cmp.Compare(a.order, b.order)
		})
		item.info.Targets = make([]commands.ConfiguredModelTarget, 0, len(orderedTargets))
		for _, target := range orderedTargets {
			item.info.Targets = append(item.info.Targets, target.target)
		}
		models = append(models, item.info)
	}
	return models
}
