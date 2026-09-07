package tools

import (
	"fmt"
	"slices"
	"strings"
)

const modelOverrideDescription = "Optional exact model_name for this child task. " +
	"Choose a different configured model when it is materially better suited, faster, or cheaper, " +
	"or when the user requests it. The override ends with the child task; the parent conversation " +
	"automatically continues on its current model."

func normalizeAvailableModels(models []string) []string {
	seen := make(map[string]struct{}, len(models))
	normalized := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		normalized = append(normalized, model)
	}
	slices.SortFunc(normalized, func(left, right string) int {
		return strings.Compare(strings.ToLower(left), strings.ToLower(right))
	})
	return normalized
}

func modelOverrideParameter(availableModels []string) map[string]any {
	parameter := map[string]any{
		"type":        "string",
		"description": modelOverrideDescription,
	}
	if len(availableModels) > 0 {
		parameter["enum"] = append([]string(nil), availableModels...)
	}
	return parameter
}

func parseModelOverride(raw any, availableModels []string) (string, error) {
	if raw == nil {
		return "", nil
	}
	model, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("model must be a string")
	}
	if model == "" {
		return "", nil
	}
	if model != strings.TrimSpace(model) {
		return "", fmt.Errorf("model must exactly match a configured model_name without surrounding whitespace")
	}
	if slices.Contains(availableModels, model) {
		return model, nil
	}
	if len(availableModels) == 0 {
		return "", fmt.Errorf(
			"model %q is not available; no configured model_name values are available",
			model,
		)
	}
	return "", fmt.Errorf(
		"model %q is not available; choose one of: %s",
		model,
		strings.Join(availableModels, ", "),
	)
}
