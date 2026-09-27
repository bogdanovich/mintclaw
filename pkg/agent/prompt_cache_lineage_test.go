package agent

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

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

	first := withPromptCacheLineage(nil, scope, "OpenAI", "GPT-5.4", nil, tools)
	second := withPromptCacheLineage(nil, scope, "openai", "gpt-5.4", nil, tools)
	firstKey, _ := first["prompt_cache_key"].(string)
	secondKey, _ := second["prompt_cache_key"].(string)
	if firstKey == "" || firstKey != secondKey {
		t.Fatalf("stable lineage keys differ: %q != %q", firstKey, secondKey)
	}
	prefix := "mintclaw-" + promptCacheLineageVersion + "-"
	if len(firstKey) != len(prefix)+48 {
		t.Fatalf("lineage key length = %d, want %d", len(firstKey), len(prefix)+48)
	}
	if !regexp.MustCompile(`^` + regexp.QuoteMeta(prefix) + `[0-9a-f]{48}$`).MatchString(firstKey) {
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
	basePrefix := buildPromptCachePrefixSnapshot(nil, tools)
	base := buildPromptCacheLineageKey(
		baseScope,
		"openai",
		"gpt-5.4",
		basePrefix,
	)
	if base == "" {
		t.Fatal("base lineage key is empty")
	}

	tests := map[string]struct {
		scope    promptCacheLineageScope
		provider string
		model    string
		prefix   promptCachePrefixSnapshot
	}{
		"agent": {
			scope:    promptCacheScope("other-agent", "session", "summary", promptCachePurposeTurn),
			provider: "openai",
			model:    "gpt-5.4",
			prefix:   basePrefix,
		},
		"session": {
			scope:    promptCacheScope("agent", "other-session", "summary", promptCachePurposeTurn),
			provider: "openai",
			model:    "gpt-5.4",
			prefix:   basePrefix,
		},
		"provider": {
			scope:    baseScope,
			provider: "anthropic",
			model:    "gpt-5.4",
			prefix:   basePrefix,
		},
		"model": {
			scope:    baseScope,
			provider: "openai",
			model:    "gpt-5.5",
			prefix:   basePrefix,
		},
		"prompt schema": {
			scope: baseScope, provider: "openai", model: "gpt-5.4",
			prefix: func() promptCachePrefixSnapshot {
				changed := basePrefix
				changed.PromptSchema = "agent-request-v3"
				changed.Hash = "different-prompt-schema"
				return changed
			}(),
		},
		"stable system": {
			scope: baseScope, provider: "openai", model: "gpt-5.4",
			prefix: func() promptCachePrefixSnapshot {
				changed := basePrefix
				changed.StableSystemHash = "different-stable-system"
				changed.Hash = "different-stable-prefix"
				return changed
			}(),
		},
		"tool schema": {
			scope:    baseScope,
			provider: "openai",
			model:    "gpt-5.4",
			prefix: func() promptCachePrefixSnapshot {
				changed := basePrefix
				changed.ToolSchemaHash = "different-tools"
				changed.Hash = "different-stable-prefix"
				return changed
			}(),
		},
		"compaction generation": {
			scope:    promptCacheScope("agent", "session", "new summary", promptCachePurposeTurn),
			provider: "openai",
			model:    "gpt-5.4",
			prefix:   basePrefix,
		},
		"purpose": {
			scope:    promptCacheScope("agent", "session", "summary", promptCachePurposeFinalRender),
			provider: "openai",
			model:    "gpt-5.4",
			prefix:   basePrefix,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got := buildPromptCacheLineageKey(
				test.scope,
				test.provider,
				test.model,
				test.prefix,
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
	left = append(left, providers.ToolDefinition{
		Type: "function",
		Function: providers.ToolFunctionDefinition{
			Name: "alpha", Parameters: map[string]any{"type": "object"},
		},
	})
	right = append([]providers.ToolDefinition{left[1]}, right...)
	if got, want := promptCacheToolSchemaFingerprint(left), promptCacheToolSchemaFingerprint(right); got != want {
		t.Fatalf("tool order changed canonical schema hash: %q != %q", got, want)
	}
}

func TestCanonicalProviderToolDefinitionsSortsWithoutMutatingInput(t *testing.T) {
	input := []providers.ToolDefinition{
		{
			Type: "function",
			Function: providers.ToolFunctionDefinition{
				Name: "zeta", Parameters: map[string]any{"type": "object"},
			},
		},
		{
			Type: "function",
			Function: providers.ToolFunctionDefinition{
				Name: "alpha", Parameters: map[string]any{"type": "object"},
			},
		},
	}
	canonical := canonicalProviderToolDefinitions(input)
	if len(canonical) != 2 || canonical[0].Function.Name != "alpha" || canonical[1].Function.Name != "zeta" {
		t.Fatalf("canonical tools = %#v", canonical)
	}
	if input[0].Function.Name != "zeta" || input[1].Function.Name != "alpha" {
		t.Fatalf("canonicalization mutated caller order: %#v", input)
	}
	if got := canonicalProviderToolDefinitions([]providers.ToolDefinition{}); got != nil {
		t.Fatalf("empty tool set canonicalized to %#v, want nil", got)
	}
}

func TestPromptCacheLineageRotatesOnlyForStablePrefixChanges(t *testing.T) {
	scope := promptCacheScope("agent", "session", "checkpoint", promptCachePurposeTurn)
	system := func(stable, dynamic string) []providers.Message {
		return []providers.Message{{
			Role: "system", Content: stable + dynamic,
			SystemParts: []providers.ContentBlock{
				{Type: "text", Text: stable, CacheControl: &providers.CacheControl{Type: "ephemeral"}},
				{Type: "text", Text: dynamic},
			},
		}}
	}
	tools := []providers.ToolDefinition{{
		Type: "function",
		Function: providers.ToolFunctionDefinition{
			Name: "inspect", Parameters: map[string]any{"type": "object"},
		},
	}}
	base := withPromptCacheLineage(nil, scope, "openai", "gpt-5.4", system("stable", "dynamic-a"), tools)
	dynamic := withPromptCacheLineage(nil, scope, "openai", "gpt-5.4", system("stable", "dynamic-b"), tools)
	changed := withPromptCacheLineage(nil, scope, "openai", "gpt-5.4", system("changed", "dynamic-b"), tools)
	if base["prompt_cache_key"] != dynamic["prompt_cache_key"] {
		t.Fatalf("dynamic suffix rotated stable lineage: base=%v dynamic=%v", base, dynamic)
	}
	if base["prompt_cache_key"] == changed["prompt_cache_key"] {
		t.Fatalf("stable prefix change reused lineage: base=%v changed=%v", base, changed)
	}
}

func TestPromptCacheLineageRotatesForWorkspaceInstructionChanges(t *testing.T) {
	scope := promptCacheScope("agent", "session", "checkpoint", promptCachePurposeTurn)
	lineage := func(messages []providers.Message) string {
		opts := withPromptCacheLineage(nil, scope, "openai", "gpt-5.4", messages, nil)
		key, _ := opts["prompt_cache_key"].(string)
		return key
	}

	t.Run("gateway memory", func(t *testing.T) {
		workspace := setupWorkspace(t, map[string]string{
			"AGENTS.md":        "stable agent instructions",
			"memory/MEMORY.md": "remember version one",
		})
		defer os.RemoveAll(workspace)
		builder := NewContextBuilder(workspace)
		first := lineage(builder.BuildMessagesFromPrompt(PromptBuildRequest{CurrentMessage: "first"}))

		memoryPath := filepath.Join(workspace, "memory", "MEMORY.md")
		if err := os.WriteFile(memoryPath, []byte("remember version two"), 0o644); err != nil {
			t.Fatal(err)
		}
		future := time.Now().Add(2 * time.Second)
		if err := os.Chtimes(memoryPath, future, future); err != nil {
			t.Fatal(err)
		}
		second := lineage(builder.BuildMessagesFromPrompt(PromptBuildRequest{CurrentMessage: "second"}))
		if first == "" || second == "" || first == second {
			t.Fatalf("gateway memory change did not rotate lineage: first=%q second=%q", first, second)
		}
	})

	t.Run("coding AGENTS", func(t *testing.T) {
		root := t.TempDir()
		project := filepath.Join(root, "project")
		if err := os.MkdirAll(project, 0o755); err != nil {
			t.Fatal(err)
		}
		agentsPath := filepath.Join(project, "AGENTS.md")
		writeCodingInstructionTestFile(t, agentsPath, "coding instructions version one")
		layout, err := NewCodingRuntimeLayout(
			"prompt-cache-prefix",
			project,
			filepath.Join(root, "state"),
			[]string{project},
		)
		if err != nil {
			t.Fatal(err)
		}
		builder, err := newCodingContextBuilder(layout)
		if err != nil {
			t.Fatal(err)
		}
		first := lineage(builder.BuildMessagesFromPrompt(PromptBuildRequest{CurrentMessage: "first"}))

		writeCodingInstructionTestFile(t, agentsPath, "coding instructions version two")
		future := time.Now().Add(2 * time.Second)
		if err := os.Chtimes(agentsPath, future, future); err != nil {
			t.Fatal(err)
		}
		second := lineage(builder.BuildMessagesFromPrompt(PromptBuildRequest{CurrentMessage: "second"}))
		if first == "" || second == "" || first == second {
			t.Fatalf("coding AGENTS change did not rotate lineage: first=%q second=%q", first, second)
		}
	})
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

	gateway := withPromptCacheLineage(nil, scope, "openai", "gpt-5.4", nil, tools)
	coding := withPromptCacheLineage(nil, scope, "openai", "gpt-5.4", nil, tools)
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
	exec := &turnExecution{checkpoint: &ContextCheckpoint{
		Content:    "current checkpoint",
		Generation: "checkpoint-generation",
	}}
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

func TestContextBuilderOrdersCheckpointBeforeRetainedHistory(t *testing.T) {
	for _, coding := range []bool{false, true} {
		t.Run(map[bool]string{false: "gateway", true: "coding"}[coding], func(t *testing.T) {
			builder := NewContextBuilder(t.TempDir())
			builder.codingPrompt = coding
			messages := builder.BuildMessagesFromPrompt(PromptBuildRequest{
				Checkpoint: &ContextCheckpoint{
					Content:    "CHECKPOINT_MARKER",
					Generation: "generation-1",
				},
				History: []providers.Message{
					{Role: "user", Content: "RAW_USER_MARKER"},
					{Role: "assistant", Content: "RAW_ASSISTANT_MARKER"},
				},
				CurrentMessage: "CURRENT_MARKER",
			})

			checkpointIndex := -1
			rawUserIndex := -1
			currentIndex := -1
			for index, message := range messages {
				if message.Role == "system" && strings.Contains(message.Content, "CHECKPOINT_MARKER") {
					t.Fatal("checkpoint was injected into the system prompt")
				}
				switch {
				case message.PromptSource == string(PromptSourceCheckpoint):
					checkpointIndex = index
					if message.Role != "assistant" || !strings.Contains(message.Content, "CHECKPOINT_MARKER") {
						t.Fatalf("checkpoint message = %#v", message)
					}
				case message.Content == "RAW_USER_MARKER":
					rawUserIndex = index
				case message.Content == "CURRENT_MARKER":
					currentIndex = index
				}
			}
			if checkpointIndex <= 0 || rawUserIndex <= checkpointIndex || currentIndex <= rawUserIndex {
				t.Fatalf(
					"message order checkpoint/raw/current = %d/%d/%d; messages=%#v",
					checkpointIndex,
					rawUserIndex,
					currentIndex,
					messages,
				)
			}
		})
	}
}

func TestPromptCacheScopeUsesCheckpointGeneration(t *testing.T) {
	first := promptCacheScopeForCheckpoint(
		"agent",
		"session",
		&ContextCheckpoint{Content: "one", Generation: "generation-1"},
		promptCachePurposeTurn,
	)
	repeated := promptCacheScopeForCheckpoint(
		"agent",
		"session",
		&ContextCheckpoint{Content: "one", Generation: "generation-1"},
		promptCachePurposeTurn,
	)
	changed := promptCacheScopeForCheckpoint(
		"agent",
		"session",
		&ContextCheckpoint{Content: "two", Generation: "generation-2"},
		promptCachePurposeTurn,
	)
	if first != repeated {
		t.Fatalf("unchanged checkpoint changed scope: %#v != %#v", first, repeated)
	}
	if first.CompactionGeneration == changed.CompactionGeneration {
		t.Fatalf("changed checkpoint kept generation %q", first.CompactionGeneration)
	}
}
