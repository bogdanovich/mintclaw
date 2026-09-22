package agent

import (
	"errors"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func TestUsageCacheOutcomePreservesKnownState(t *testing.T) {
	tests := []struct {
		name        string
		usage       *providers.UsageInfo
		wantTokens  int
		wantKnown   bool
		wantOutcome string
	}{
		{name: "no usage", wantOutcome: "unknown"},
		{name: "unreported", usage: &providers.UsageInfo{}, wantOutcome: "unknown"},
		{
			name: "known miss", usage: &providers.UsageInfo{CacheReadInputTokens: providers.KnownTokenCount(0)},
			wantKnown: true, wantOutcome: "miss",
		},
		{
			name: "known hit", usage: &providers.UsageInfo{CacheReadInputTokens: providers.KnownTokenCount(32)},
			wantTokens: 32, wantKnown: true, wantOutcome: "hit",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTokens, gotKnown := usageCacheReadInputTokens(tt.usage)
			if gotTokens != tt.wantTokens || gotKnown != tt.wantKnown {
				t.Fatalf("cache read = (%d, %v), want (%d, %v)", gotTokens, gotKnown, tt.wantTokens, tt.wantKnown)
			}
			if got := usageCacheOutcome(tt.usage); got != tt.wantOutcome {
				t.Fatalf("cache outcome = %q, want %q", got, tt.wantOutcome)
			}
		})
	}
}

func TestLLMIterationStateRecordsOnlySuccessfulResponseSource(t *testing.T) {
	state := newLLMIterationState(1)
	state.recordResponseSource("OpenAI", "gpt-primary", nil, errors.New("failed"))
	if state.responseProvider != "" || state.responseModel != "" {
		t.Fatalf("failed attempt recorded response source: %+v", state)
	}

	state.recordResponseSource("OpenAI", "gpt-fallback", &providers.LLMResponse{}, nil)
	if state.responseProvider != "openai" || state.responseModel != "gpt-fallback" {
		t.Fatalf("response source = %s/%s, want openai/gpt-fallback", state.responseProvider, state.responseModel)
	}
}
