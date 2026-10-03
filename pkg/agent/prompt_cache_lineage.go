package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

const (
	promptCacheLineageVersion        = "v2"
	promptCachePromptSchemaVersion   = "agent-request-v2"
	promptCachePrefixSnapshotVersion = "v1"

	promptCachePurposeTurn         = "turn"
	promptCachePurposeSideQuestion = "side-question"
	promptCachePurposeFinalRender  = "final-render"
	promptCachePurposeSeahorse     = "seahorse-summary"
)

// promptCacheLineageScope contains the runtime-owned inputs that remain stable
// across retries of the same logical request. Session and agent identities are
// only ever used as inputs to a one-way digest; they must not be logged or sent
// to a provider in plaintext.
type promptCacheLineageScope struct {
	AgentID              string
	SessionKey           string
	CompactionGeneration string
	Purpose              string
}

type promptCacheLineageInput struct {
	Version              string `json:"version"`
	Agent                string `json:"agent"`
	Session              string `json:"session"`
	Provider             string `json:"provider"`
	Model                string `json:"model"`
	PromptSchema         string `json:"prompt_schema"`
	StableSystem         string `json:"stable_system"`
	ToolSchema           string `json:"tool_schema"`
	StablePrefix         string `json:"stable_prefix"`
	CompactionGeneration string `json:"compaction_generation"`
	Purpose              string `json:"purpose"`
}

type promptCachePrefixSnapshot struct {
	Version          string `json:"version"`
	PromptSchema     string `json:"prompt_schema"`
	StableSystemHash string `json:"stable_system_hash"`
	ToolSchemaHash   string `json:"tool_schema_hash"`
	Hash             string `json:"-"`
}

func promptCacheScope(
	agentID, sessionKey, summary, purpose string,
) promptCacheLineageScope {
	return promptCacheLineageScope{
		AgentID:              strings.TrimSpace(agentID),
		SessionKey:           strings.TrimSpace(sessionKey),
		CompactionGeneration: promptCacheCompactionGeneration(summary),
		Purpose:              strings.TrimSpace(purpose),
	}
}

func promptCacheScopeForCheckpoint(
	agentID, sessionKey string,
	checkpoint *ContextCheckpoint,
	purpose string,
) promptCacheLineageScope {
	generation := "none"
	if checkpoint != nil {
		generation = strings.TrimSpace(checkpoint.Generation)
		if generation == "" && checkpoint.Content != "" {
			generation = promptCacheDigest([]byte(checkpoint.Content), 16)
		}
		if generation == "" {
			generation = "none"
		}
	}
	return promptCacheLineageScope{
		AgentID:              strings.TrimSpace(agentID),
		SessionKey:           strings.TrimSpace(sessionKey),
		CompactionGeneration: generation,
		Purpose:              strings.TrimSpace(purpose),
	}
}

func promptCacheCompactionGeneration(summary string) string {
	if summary == "" {
		return "none"
	}
	return promptCacheDigest([]byte(summary), 16)
}

func promptCacheToolSchemaFingerprint(tools []providers.ToolDefinition) string {
	visible := providerVisibleToolDefinitions(canonicalProviderToolDefinitions(tools))
	encoded, err := json.Marshal(visible)
	if err != nil {
		return ""
	}
	return promptCacheDigest(encoded, 24)
}

func promptCacheStableSystemFingerprint(messages []providers.Message) string {
	visible := make([]providers.Message, len(messages))
	for index, message := range messages {
		visible[index] = providerVisibleMessage(message)
	}
	stable, _ := promptCacheSystemSegments(visible)
	encoded, err := json.Marshal(stable)
	if err != nil {
		return ""
	}
	return promptCacheDigest(encoded, 24)
}

func buildPromptCachePrefixSnapshot(
	messages []providers.Message,
	tools []providers.ToolDefinition,
) promptCachePrefixSnapshot {
	snapshot := promptCachePrefixSnapshot{
		Version:          promptCachePrefixSnapshotVersion,
		PromptSchema:     promptCachePromptSchemaVersion,
		StableSystemHash: promptCacheStableSystemFingerprint(messages),
		ToolSchemaHash:   promptCacheToolSchemaFingerprint(tools),
	}
	encoded, err := json.Marshal(snapshot)
	if err == nil && snapshot.StableSystemHash != "" && snapshot.ToolSchemaHash != "" {
		snapshot.Hash = promptCacheDigest(encoded, 24)
	}
	return snapshot
}

func canonicalProviderToolDefinitions(
	definitions []providers.ToolDefinition,
) []providers.ToolDefinition {
	if len(definitions) == 0 {
		return nil
	}
	canonical := cloneToolDefinitions(definitions)
	slices.SortStableFunc(canonical, func(left, right providers.ToolDefinition) int {
		if compared := strings.Compare(left.Function.Name, right.Function.Name); compared != 0 {
			return compared
		}
		if compared := strings.Compare(left.Type, right.Type); compared != 0 {
			return compared
		}
		leftJSON, leftErr := json.Marshal(providerVisibleToolDefinitions([]providers.ToolDefinition{left}))
		rightJSON, rightErr := json.Marshal(providerVisibleToolDefinitions([]providers.ToolDefinition{right}))
		if leftErr != nil || rightErr != nil {
			return 0
		}
		return bytes.Compare(leftJSON, rightJSON)
	})
	return canonical
}

func buildPromptCacheLineageKey(
	scope promptCacheLineageScope,
	provider, model string,
	prefix promptCachePrefixSnapshot,
) string {
	provider = providers.NormalizeProvider(strings.TrimSpace(provider))
	model = strings.ToLower(strings.TrimSpace(model))
	if strings.TrimSpace(scope.AgentID) == "" || strings.TrimSpace(scope.SessionKey) == "" ||
		provider == "" || model == "" || strings.TrimSpace(prefix.Version) == "" ||
		strings.TrimSpace(prefix.PromptSchema) == "" || strings.TrimSpace(prefix.StableSystemHash) == "" ||
		strings.TrimSpace(prefix.ToolSchemaHash) == "" || strings.TrimSpace(prefix.Hash) == "" ||
		strings.TrimSpace(scope.CompactionGeneration) == "" || strings.TrimSpace(scope.Purpose) == "" {
		return ""
	}

	encoded, err := json.Marshal(promptCacheLineageInput{
		Version:              promptCacheLineageVersion,
		Agent:                scope.AgentID,
		Session:              scope.SessionKey,
		Provider:             provider,
		Model:                model,
		PromptSchema:         prefix.PromptSchema,
		StableSystem:         prefix.StableSystemHash,
		ToolSchema:           prefix.ToolSchemaHash,
		StablePrefix:         prefix.Hash,
		CompactionGeneration: scope.CompactionGeneration,
		Purpose:              scope.Purpose,
	})
	if err != nil {
		return ""
	}
	return "mintclaw-" + promptCacheLineageVersion + "-" + promptCacheDigest(encoded, 48)
}

func withPromptCacheLineage(
	base map[string]any,
	scope promptCacheLineageScope,
	provider, model string,
	messages []providers.Message,
	tools []providers.ToolDefinition,
) map[string]any {
	opts := shallowCloneLLMOptions(base)
	delete(opts, "prompt_cache_key")
	providers.ClearPromptCachePlan(opts)
	key := buildPromptCacheLineageKey(
		scope,
		provider,
		model,
		buildPromptCachePrefixSnapshot(messages, tools),
	)
	if key != "" {
		opts["prompt_cache_key"] = key
		providers.SetPromptCachePlan(opts, providers.PromptCachePlan{
			Version:                  providers.PromptCachePlanVersion1,
			LineageKey:               key,
			WritePolicy:              promptCacheWritePolicy(scope.Purpose),
			BreakpointMessageIndexes: promptCacheBreakpointMessageIndexes(messages),
		})
	}
	return opts
}

func withConfiguredPromptCacheLineage(
	enabled bool,
	base map[string]any,
	scope promptCacheLineageScope,
	provider, model string,
	messages []providers.Message,
	tools []providers.ToolDefinition,
) map[string]any {
	if enabled {
		return withPromptCacheLineage(base, scope, provider, model, messages, tools)
	}
	opts := shallowCloneLLMOptions(base)
	delete(opts, "prompt_cache_key")
	providers.DisablePromptCache(opts)
	return opts
}

func promptCacheEnabled(cfg *config.Config) bool {
	return effectivePromptCacheMode(cfg) != config.PromptCacheModeDisabled
}

func effectivePromptCacheMode(cfg *config.Config) config.PromptCacheMode {
	if cfg == nil {
		return config.PromptCacheModeEnabled
	}
	return cfg.Agents.Defaults.PromptCacheMode.Effective()
}

func promptCacheWritePolicy(purpose string) providers.PromptCacheWritePolicy {
	if strings.TrimSpace(purpose) == promptCachePurposeSeahorse {
		return providers.PromptCacheWriteNoWrite
	}
	return providers.PromptCacheWriteReuse
}

func promptCacheBreakpointMessageIndexes(messages []providers.Message) []int {
	indexes := make([]int, 0, 2)
	for index, message := range messages {
		if message.Role != "system" || strings.TrimSpace(message.Content) == "" {
			continue
		}
		indexes = append(indexes[:0], index)
	}

	tailStart, tailFound := promptCacheDynamicTailStart(messages)
	if !tailFound {
		for index := len(messages) - 1; index >= 0; index-- {
			if messages[index].RootTurnStart {
				tailStart = index
				tailFound = true
				break
			}
		}
	}
	if !tailFound {
		return indexes
	}
	for index := min(tailStart, len(messages)) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role == "system" || strings.TrimSpace(message.Content) == "" {
			continue
		}
		if len(indexes) == 0 || indexes[len(indexes)-1] != index {
			indexes = append(indexes, index)
		}
		break
	}
	return indexes
}

func promptCacheDigest(value []byte, hexChars int) string {
	sum := sha256.Sum256(value)
	digest := hex.EncodeToString(sum[:])
	if hexChars <= 0 || hexChars >= len(digest) {
		return digest
	}
	return digest[:hexChars]
}
