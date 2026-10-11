package eval

import (
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func TestCompactTokenDisplayTruncatesAndPreservesRawCounts(t *testing.T) {
	for count, expected := range map[int64]string{
		-1: "unknown", 0: "0", 999: "999", 1000: "1k", 10252: "10.2k", 99999: "99.9k",
		1000000: "1m", 1025252: "1m", 1200252: "1.2m", 1000000000: "1b",
	} {
		if observed := CompactTokens(count); observed != expected {
			t.Fatal("compact count mismatch", count, observed, expected)
		}
	}
	calls := []RoleCall{
		{
			Identity: RoleIdentity{Role: "user"}, Attempted: true, UsageSource: "provider_response",
			Usage: &providers.UsageInfo{PromptTokens: 10252, CompletionTokens: 32, TotalTokens: 10284},
		},
		{Identity: RoleIdentity{Role: "user"}, Attempted: true, UsageSource: "unknown"},
		{
			Identity: RoleIdentity{Role: "judge"}, Attempted: true, UsageSource: "provider_response",
			Usage: &providers.UsageInfo{PromptTokens: 2000, CompletionTokens: 20, TotalTokens: 2020},
		},
		{Identity: RoleIdentity{Role: "user"}, Attempted: false, UsageSource: "unknown"},
	}
	result := summarizeRoleCalls("user", calls)
	if result.Calls != 3 || result.AttemptedCalls != 2 || result.ReturnedUsage != 1 ||
		result.InputTokens != 10252 || result.OutputTokens != 32 || result.TotalTokens != 10284 ||
		result.Coverage != "partial_returned_calls" || result.Attempts != "unknown" ||
		summarizeRoleCalls("judge", calls).InputTokens != 2000 {
		t.Fatal("usage lost a call, rounded raw counts, mixed roles, or fabricated coverage", result)
	}
}
