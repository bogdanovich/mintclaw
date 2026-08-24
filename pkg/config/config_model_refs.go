package config

import (
	"fmt"
	"strings"
)

// ValidateModelReferences requires every configured model selector to name an
// enabled model_list entry exactly. ModelConfig.Model remains the provider-native
// model identifier; it is not a selector alias.
func (c *Config) ValidateModelReferences() error {
	if c == nil {
		return nil
	}

	enabled := make(map[string]struct{}, len(c.ModelList))
	for _, model := range c.ModelList {
		if model != nil && model.Enabled && model.ModelName != "" {
			enabled[model.ModelName] = struct{}{}
		}
	}

	validateOptional := func(path, ref string) error {
		if ref == "" {
			return nil
		}
		if ref != strings.TrimSpace(ref) {
			return fmt.Errorf("%s must not have surrounding whitespace", path)
		}
		if _, ok := enabled[ref]; !ok {
			return fmt.Errorf("%s references unknown or disabled model_name %q", path, ref)
		}
		return nil
	}
	validateRequired := func(path, ref string) error {
		if ref == "" {
			return fmt.Errorf("%s must not be empty", path)
		}
		return validateOptional(path, ref)
	}
	validateFallbacks := func(path string, refs []string) error {
		for index, ref := range refs {
			if err := validateRequired(fmt.Sprintf("%s[%d]", path, index), ref); err != nil {
				return err
			}
		}
		return nil
	}
	validateAgentModel := func(path string, model *AgentModelConfig) error {
		if model == nil {
			return nil
		}
		if err := validateOptional(path+".primary", model.Primary); err != nil {
			return err
		}
		return validateFallbacks(path+".fallbacks", model.Fallbacks)
	}

	for index, model := range c.ModelList {
		if model == nil {
			continue
		}
		path := fmt.Sprintf("model_list[%d]", index)
		if err := validateFallbacks(path+".fallbacks", model.Fallbacks); err != nil {
			return err
		}
		if model.Capabilities == nil || model.Capabilities.Vision == nil {
			continue
		}
		vision := model.Capabilities.Vision
		if err := validateOptional(path+".capabilities.vision.model", vision.Model); err != nil {
			return err
		}
		if err := validateFallbacks(path+".capabilities.vision.fallbacks", vision.Fallbacks); err != nil {
			return err
		}
	}

	defaults := &c.Agents.Defaults
	if err := validateOptional("agents.defaults.model_name", defaults.ModelName); err != nil {
		return err
	}
	if err := validateFallbacks("agents.defaults.model_fallbacks", defaults.ModelFallbacks); err != nil {
		return err
	}
	if defaults.Routing != nil {
		if err := validateOptional("agents.defaults.routing.light_model", defaults.Routing.LightModel); err != nil {
			return err
		}
	}
	if defaults.Subagents != nil {
		if err := validateAgentModel("agents.defaults.subagents.model", defaults.Subagents.Model); err != nil {
			return err
		}
	}

	for index := range c.Agents.List {
		agent := &c.Agents.List[index]
		path := fmt.Sprintf("agents.list[%d]", index)
		if err := validateAgentModel(path+".model", agent.Model); err != nil {
			return err
		}
		if agent.Subagents != nil {
			if err := validateAgentModel(path+".subagents.model", agent.Subagents.Model); err != nil {
				return err
			}
		}
	}

	if err := validateOptional("voice.model_name", c.Voice.ModelName); err != nil {
		return err
	}
	return validateOptional("voice.tts_model_name", c.Voice.TTSModelName)
}
