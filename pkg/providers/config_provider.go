// MintClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package providers

import (
	"fmt"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

// CreateProvider resolves the configured default model through model_list and
// constructs its provider. It returns the provider and its native model ID.
func CreateProvider(cfg *config.Config) (LLMProvider, string, error) {
	model := cfg.Agents.Defaults.GetModelName()
	if len(cfg.ModelList) == 0 {
		return nil, "", fmt.Errorf("no models configured: add an entry to model_list")
	}

	modelCfg, err := cfg.GetModelConfig(model)
	if err != nil {
		return nil, "", fmt.Errorf("model %q not found in model_list: %w", model, err)
	}
	if modelCfg.Workspace == "" {
		modelCfg.Workspace = cfg.WorkspacePath()
	}

	provider, modelID, err := CreateProviderFromConfig(modelCfg)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create provider for model %q: %w", model, err)
	}
	return provider, modelID, nil
}
