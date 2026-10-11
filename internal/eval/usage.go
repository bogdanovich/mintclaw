package eval

import (
	"fmt"
	"math"
)

type RoleUsage struct {
	Role           string `json:"role"`
	Calls          int    `json:"calls"`
	AttemptedCalls int    `json:"attempted_calls"`
	ReturnedUsage  int    `json:"returned_usage_calls"`
	InputTokens    int64  `json:"input_tokens"`
	OutputTokens   int64  `json:"output_tokens"`
	TotalTokens    int64  `json:"total_tokens"`
	Coverage       string `json:"coverage"`
	Attempts       string `json:"provider_internal_attempts"`
}

func summarizeRoleCalls(role string, calls []RoleCall) RoleUsage {
	result := RoleUsage{Role: role, Coverage: "unknown", Attempts: "unknown"}
	for _, call := range calls {
		if call.Identity.Role != role {
			continue
		}
		result.Calls++
		if !call.Attempted {
			continue
		}
		result.AttemptedCalls++
		if call.UsageSource != "provider_response" || !validRoleUsage(call.Usage) {
			continue
		}
		in, out, total := int64(
			call.Usage.PromptTokens,
		), int64(
			call.Usage.CompletionTokens,
		), int64(
			call.Usage.TotalTokens,
		)
		result.ReturnedUsage++
		if result.Coverage == "overflow" || in > math.MaxInt64-result.InputTokens ||
			out > math.MaxInt64-result.OutputTokens ||
			total > math.MaxInt64-result.TotalTokens {
			result.Coverage = "overflow"
			continue
		}
		result.InputTokens += in
		result.OutputTokens += out
		result.TotalTokens += total
	}
	if result.ReturnedUsage > 0 && result.Coverage != "overflow" {
		result.Coverage = "partial_returned_calls"
		if result.ReturnedUsage == result.AttemptedCalls {
			result.Coverage = "returned_calls_only"
		}
	}
	return result
}

// Truncate rather than round; 10,252 displays as 10.2k, not 10.3k.
func CompactTokens(count int64) string {
	if count < 0 {
		return "unknown"
	}
	for _, unit := range []struct {
		size   int64
		suffix string
	}{{1_000_000_000, "b"}, {1_000_000, "m"}, {1000, "k"}} {
		if count >= unit.size {
			whole, fraction := count/unit.size, count%unit.size/(unit.size/10)
			if fraction == 0 {
				return fmt.Sprintf("%d%s", whole, unit.suffix)
			}
			return fmt.Sprintf("%d.%d%s", whole, fraction, unit.suffix)
		}
	}
	return fmt.Sprintf("%d", count)
}
