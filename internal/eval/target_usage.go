package eval

import (
	"encoding/json"
	"math"

	"github.com/bogdanovich/mintclaw/pkg/events"
)

// TokenUsage is a returned native turn total, not a witness of every provider
// attempt or every LLM call. Missing usage is different from an observed zero.
type TokenUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}

func (usage TokenUsage) valid() bool {
	return usage.InputTokens >= 0 && usage.OutputTokens >= 0 &&
		usage.InputTokens <= math.MaxInt64-usage.OutputTokens &&
		usage.TotalTokens == usage.InputTokens+usage.OutputTokens
}

func returnedTokenUsage(raw any) *TokenUsage {
	// WebSocket JSON is decoded into float64. Reject counts beyond its exact
	// integer range rather than retaining an already rounded token count.
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	usage, err := decodeDocument[TokenUsage](data, 512)
	const maximumExactInteger = 1<<53 - 1
	if err != nil || !usage.valid() || usage.TotalTokens > maximumExactInteger {
		return nil
	}
	return &usage
}

// TargetUsage deliberately summarizes returned final events only. The native
// wire does not expose call/attempt completeness, including interrupted turns.
type TargetUsage struct {
	Scope         string `json:"scope"`
	Coverage      string `json:"coverage"`
	AllCalls      string `json:"all_call_coverage"`
	Finals        int    `json:"finals"`
	ReturnedUsage int    `json:"returned_usage_finals"`
	InputTokens   int64  `json:"input_tokens"`
	OutputTokens  int64  `json:"output_tokens"`
	TotalTokens   int64  `json:"total_tokens"`
}

func newTargetUsage() *TargetUsage {
	return &TargetUsage{Scope: "returned_final_events_v1", Coverage: "unknown", AllCalls: "unknown"}
}

func (usage *TargetUsage) valid() bool {
	if usage == nil {
		return true // Reports predating this observation remain readable.
	}
	if usage.Scope != "returned_final_events_v1" || usage.AllCalls != "unknown" ||
		usage.Finals < 0 || usage.Finals > MaxCorpusCases*256 ||
		usage.ReturnedUsage < 0 || usage.ReturnedUsage > usage.Finals ||
		!(TokenUsage{usage.InputTokens, usage.OutputTokens, usage.TotalTokens}).valid() {
		return false
	}
	switch usage.Coverage {
	case "unknown":
		return usage.ReturnedUsage == 0 && usage.TotalTokens == 0
	case "overflow", "conflicting_final_events":
		return usage.TotalTokens == 0
	case "partial_returned_finals":
		return usage.ReturnedUsage > 0 && usage.ReturnedUsage < usage.Finals
	case "returned_finals_only":
		return usage.ReturnedUsage > 0 && usage.ReturnedUsage == usage.Finals
	default:
		return false
	}
}

func validEventUsage(event Event) bool {
	return event.Usage == nil || event.Kind == "final" && event.Usage.valid()
}

func (usage *TargetUsage) addTrial(trial Trial) {
	inputs, finals := make(map[string]int), make(map[string]bool)
	turns := make(map[events.TraceScope]bool)
	for _, event := range trial.Events {
		if event.Kind == "input" && event.RequestID != "" {
			inputs[event.RequestID] = event.Turn
		}
		if event.Kind != "final" || inputs[event.RequestID] != event.Turn || event.Turn < 1 ||
			event.AgentID != trial.AgentID || event.SessionKey == "" || event.SessionKey != trial.SessionKey ||
			len(event.TraceScopes) == 0 || !completeUsageScopes(event.TraceScopes) {
			continue
		}
		if finals[event.RequestID] {
			// Two finals for one request are ambiguous, even if counts match.
			usage.Coverage = "conflicting_final_events"
			continue
		}
		finals[event.RequestID] = true
		usage.Finals++
		for _, scope := range event.TraceScopes {
			if turns[scope] {
				usage.Coverage = "conflicting_final_events"
			}
		}
		for _, scope := range event.TraceScopes {
			turns[scope] = true
		}
		if event.Usage == nil || !event.Usage.valid() {
			continue
		}
		usage.ReturnedUsage++
		if usage.Coverage == "overflow" || event.Usage.InputTokens > math.MaxInt64-usage.InputTokens ||
			event.Usage.OutputTokens > math.MaxInt64-usage.OutputTokens ||
			event.Usage.TotalTokens > math.MaxInt64-usage.TotalTokens {
			usage.Coverage = "overflow"
			continue
		}
		usage.InputTokens += event.Usage.InputTokens
		usage.OutputTokens += event.Usage.OutputTokens
		usage.TotalTokens += event.Usage.TotalTokens
	}
	if usage.Coverage == "overflow" || usage.Coverage == "conflicting_final_events" {
		usage.InputTokens, usage.OutputTokens, usage.TotalTokens = 0, 0, 0
		return
	}
	if usage.ReturnedUsage > 0 {
		usage.Coverage = "partial_returned_finals"
		if usage.ReturnedUsage == usage.Finals {
			usage.Coverage = "returned_finals_only"
		}
	}
}

func completeUsageScopes(scopes []events.TraceScope) bool {
	for _, scope := range scopes {
		if !scope.Complete() {
			return false
		}
	}
	return true
}
