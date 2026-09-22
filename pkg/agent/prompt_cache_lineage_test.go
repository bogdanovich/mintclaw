package agent

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func TestPromptCacheLineageKeyIsOpaqueStableAndBounded(t *testing.T) {
	scope := promptCacheScope("agent-secret", "session-secret", "summary secret", promptCachePurposeTurn)
	tools := []providers.ToolDefinition{{
		Type: "function",
		Function: providers.ToolFunctionDefinition{
			Name:        "read_file",
			Description: "Read a file",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string"},
				},
			},
		},
	}}

	first := withPromptCacheLineage(nil, scope, "OpenAI", "GPT-5.4", tools)
	second := withPromptCacheLineage(nil, scope, "openai", "gpt-5.4", tools)
	firstKey, _ := first["prompt_cache_key"].(string)
	secondKey, _ := second["prompt_cache_key"].(string)
	if firstKey == "" || firstKey != secondKey {
		t.Fatalf("stable lineage keys differ: %q != %q", firstKey, secondKey)
	}
	if len(firstKey) != len("mintclaw-v1-")+48 {
		t.Fatalf("lineage key length = %d, want %d", len(firstKey), len("mintclaw-v1-")+48)
	}
	if !regexp.MustCompile(`^mintclaw-v1-[0-9a-f]{48}$`).MatchString(firstKey) {
		t.Fatalf("lineage key is not bounded lowercase hex: %q", firstKey)
	}
	for _, sensitive := range []string{"agent-secret", "session-secret", "summary secret", "gpt-5.4"} {
		if strings.Contains(firstKey, sensitive) {
			t.Fatalf("lineage key exposes %q: %q", sensitive, firstKey)
		}
	}
}

func TestPromptCacheLineageChangesForEveryRoutingDimension(t *testing.T) {
	tools := []providers.ToolDefinition{{
		Type: "function",
		Function: providers.ToolFunctionDefinition{
			Name:       "one",
			Parameters: map[string]any{"type": "object"},
		},
	}}
	baseScope := promptCacheScope("agent", "session", "summary", promptCachePurposeTurn)
	baseToolHash := promptCacheToolSchemaFingerprint(tools)
	base := buildPromptCacheLineageKey(
		baseScope,
		"openai",
		"gpt-5.4",
		promptCachePromptSchemaVersion,
		baseToolHash,
	)
	if base == "" {
		t.Fatal("base lineage key is empty")
	}

	tests := map[string]struct {
		scope        promptCacheLineageScope
		provider     string
		model        string
		promptSchema string
		toolSchema   string
	}{
		"agent": {
			scope:        promptCacheScope("other-agent", "session", "summary", promptCachePurposeTurn),
			provider:     "openai",
			model:        "gpt-5.4",
			promptSchema: promptCachePromptSchemaVersion,
			toolSchema:   baseToolHash,
		},
		"session": {
			scope:        promptCacheScope("agent", "other-session", "summary", promptCachePurposeTurn),
			provider:     "openai",
			model:        "gpt-5.4",
			promptSchema: promptCachePromptSchemaVersion,
			toolSchema:   baseToolHash,
		},
		"provider": {
			scope:        baseScope,
			provider:     "anthropic",
			model:        "gpt-5.4",
			promptSchema: promptCachePromptSchemaVersion,
			toolSchema:   baseToolHash,
		},
		"model": {
			scope:        baseScope,
			provider:     "openai",
			model:        "gpt-5.5",
			promptSchema: promptCachePromptSchemaVersion,
			toolSchema:   baseToolHash,
		},
		"prompt schema": {
			scope:    baseScope,
			provider: "openai", model: "gpt-5.4", promptSchema: "agent-request-v2", toolSchema: baseToolHash,
		},
		"tool schema": {
			scope:        baseScope,
			provider:     "openai",
			model:        "gpt-5.4",
			promptSchema: promptCachePromptSchemaVersion,
			toolSchema:   "different-tools",
		},
		"compaction generation": {
			scope:        promptCacheScope("agent", "session", "new summary", promptCachePurposeTurn),
			provider:     "openai",
			model:        "gpt-5.4",
			promptSchema: promptCachePromptSchemaVersion,
			toolSchema:   baseToolHash,
		},
		"purpose": {
			scope:        promptCacheScope("agent", "session", "summary", promptCachePurposeFinalRender),
			provider:     "openai",
			model:        "gpt-5.4",
			promptSchema: promptCachePromptSchemaVersion,
			toolSchema:   baseToolHash,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got := buildPromptCacheLineageKey(
				test.scope,
				test.provider,
				test.model,
				test.promptSchema,
				test.toolSchema,
			)
			if got == base {
				t.Fatalf("changing %s did not change lineage key %q", name, got)
			}
		})
	}
}

func TestPromptCacheLineageToolSchemaIsCanonical(t *testing.T) {
	left := []providers.ToolDefinition{{
		Type: "function",
		Function: providers.ToolFunctionDefinition{
			Name: "tool",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"b": map[string]any{"type": "number"},
					"a": map[string]any{"type": "string"},
				},
			},
		},
	}}
	right := []providers.ToolDefinition{{
		Type: "function",
		Function: providers.ToolFunctionDefinition{
			Name: "tool",
			Parameters: map[string]any{
				"properties": map[string]any{
					"a": map[string]any{"type": "string"},
					"b": map[string]any{"type": "number"},
				},
				"type": "object",
			},
		},
	}}
	if got, want := promptCacheToolSchemaFingerprint(left), promptCacheToolSchemaFingerprint(right); got != want {
		t.Fatalf("canonical schema hashes differ: %q != %q", got, want)
	}
}

func TestPromptCacheLineageIsSharedAcrossGatewayAndCodingProfiles(t *testing.T) {
	scope := promptCacheScope("agent", "session", "checkpoint", promptCachePurposeTurn)
	tools := []providers.ToolDefinition{{
		Type: "function",
		Function: providers.ToolFunctionDefinition{
			Name:       "read_file",
			Parameters: map[string]any{"type": "object"},
		},
	}}

	gateway := withPromptCacheLineage(nil, scope, "openai", "gpt-5.4", tools)
	coding := withPromptCacheLineage(nil, scope, "openai", "gpt-5.4", tools)
	if gateway["prompt_cache_key"] != coding["prompt_cache_key"] {
		t.Fatalf(
			"shared request inputs produced profile-specific lineages: gateway=%v coding=%v",
			gateway["prompt_cache_key"],
			coding["prompt_cache_key"],
		)
	}
}

func TestPromptCacheLineageFailsClosedAndReplacesHookKey(t *testing.T) {
	opts := withPromptCacheLineage(
		map[string]any{"prompt_cache_key": "hook-controlled", "max_tokens": 42},
		promptCacheScope("agent", "", "", promptCachePurposeTurn),
		"openai",
		"gpt-5.4",
		nil,
	)
	if _, ok := opts["prompt_cache_key"]; ok {
		t.Fatalf("missing session retained an unsafe cache key: %#v", opts)
	}
	if opts["max_tokens"] != 42 {
		t.Fatalf("unrelated option changed: %#v", opts)
	}
}

func TestFallbackAttemptUsesActualProviderAndModelLineage(t *testing.T) {
	provider := &sequenceProvider{responses: []*providers.LLMResponse{{Content: "ok"}, {Content: "ok"}}}
	pipeline := &Pipeline{}
	ts := &turnState{
		agent:      &AgentInstance{ID: "agent-main", Workspace: t.TempDir()},
		sessionKey: "session-main",
	}
	exec := &turnExecution{summary: "current checkpoint"}
	llm := &LLMIterationState{llmOpts: map[string]any{"prompt_cache_key": "hook-controlled"}}

	for _, candidate := range []providers.FallbackCandidate{
		{Provider: "openai", Model: "gpt-5.4"},
		{Provider: "anthropic", Model: "claude-sonnet-4-5"},
	} {
		if _, err := pipeline.callResolvedFallbackCandidate(
			context.Background(),
			ts,
			exec,
			llm,
			candidate,
			nil,
			provider,
			nil,
			nil,
		); err != nil {
			t.Fatalf("callResolvedFallbackCandidate(%s/%s): %v", candidate.Provider, candidate.Model, err)
		}
	}

	first, _ := provider.options[0]["prompt_cache_key"].(string)
	second, _ := provider.options[1]["prompt_cache_key"].(string)
	if first == "" || second == "" || first == second {
		t.Fatalf("fallback attempt lineages = %q and %q, want distinct opaque keys", first, second)
	}
	if first == "hook-controlled" || second == "hook-controlled" {
		t.Fatalf("runtime did not replace hook-controlled cache key: %#v", provider.options)
	}
}
