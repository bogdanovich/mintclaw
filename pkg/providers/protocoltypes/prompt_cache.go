package protocoltypes

import "strings"

const promptCachePlanOptionKey = "__mintclaw_prompt_cache_plan"

// SetPromptCachePlan stores a detached plan in the internal provider options
// channel. Callers retain ownership of the input plan and its slices.
func SetPromptCachePlan(options map[string]any, plan PromptCachePlan) {
	if options == nil {
		return
	}
	plan.BreakpointMessageIndexes = append([]int(nil), plan.BreakpointMessageIndexes...)
	options[promptCachePlanOptionKey] = plan
}

// ClearPromptCachePlan removes any hook- or caller-supplied plan before the
// trusted runtime derives a replacement.
func ClearPromptCachePlan(options map[string]any) {
	delete(options, promptCachePlanOptionKey)
}

// PromptCachePlanOptionPresent reports whether the reserved typed-plan option
// was supplied, independently of whether its value is valid. Adapters use this
// distinction to retain legacy fallback only when no typed contract was
// supplied; a malformed or future contract must fail closed.
func PromptCachePlanOptionPresent(options map[string]any) bool {
	if options == nil {
		return false
	}
	_, present := options[promptCachePlanOptionKey]
	return present
}

// PromptCachePlanFromOptions returns a validated, detached cache plan. Unknown
// versions and malformed policies fail closed so adapters emit no cache-only
// fields for an untrusted or newer contract.
func PromptCachePlanFromOptions(options map[string]any) (PromptCachePlan, bool) {
	if options == nil {
		return PromptCachePlan{}, false
	}
	plan, ok := options[promptCachePlanOptionKey].(PromptCachePlan)
	if !ok || plan.Version != PromptCachePlanVersion1 || strings.TrimSpace(plan.LineageKey) == "" {
		return PromptCachePlan{}, false
	}
	switch plan.WritePolicy {
	case PromptCacheWriteReuse:
	case PromptCacheWriteNoWrite:
		plan.BreakpointMessageIndexes = nil
	default:
		return PromptCachePlan{}, false
	}
	indexes := make([]int, 0, len(plan.BreakpointMessageIndexes))
	seen := make(map[int]struct{}, len(plan.BreakpointMessageIndexes))
	for _, index := range plan.BreakpointMessageIndexes {
		if index < 0 {
			return PromptCachePlan{}, false
		}
		if _, exists := seen[index]; exists {
			continue
		}
		seen[index] = struct{}{}
		indexes = append(indexes, index)
	}
	plan.BreakpointMessageIndexes = indexes
	return plan, true
}
