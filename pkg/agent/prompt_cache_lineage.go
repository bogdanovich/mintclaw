package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/providers"
)

const (
	promptCacheLineageVersion      = "v1"
	promptCachePromptSchemaVersion = "agent-request-v1"

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
	ToolSchema           string `json:"tool_schema"`
	CompactionGeneration string `json:"compaction_generation"`
	Purpose              string `json:"purpose"`
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

func promptCacheCompactionGeneration(summary string) string {
	if summary == "" {
		return "none"
	}
	return promptCacheDigest([]byte(summary), 16)
}

func promptCacheToolSchemaFingerprint(tools []providers.ToolDefinition) string {
	visible := providerVisibleToolDefinitions(tools)
	encoded, err := json.Marshal(visible)
	if err != nil {
		return ""
	}
	return promptCacheDigest(encoded, 24)
}

func buildPromptCacheLineageKey(
	scope promptCacheLineageScope,
	provider, model, promptSchema, toolSchema string,
) string {
	provider = providers.NormalizeProvider(strings.TrimSpace(provider))
	model = strings.ToLower(strings.TrimSpace(model))
	promptSchema = strings.TrimSpace(promptSchema)
	toolSchema = strings.TrimSpace(toolSchema)
	if strings.TrimSpace(scope.AgentID) == "" || strings.TrimSpace(scope.SessionKey) == "" ||
		provider == "" || model == "" || promptSchema == "" || toolSchema == "" ||
		strings.TrimSpace(scope.CompactionGeneration) == "" || strings.TrimSpace(scope.Purpose) == "" {
		return ""
	}

	encoded, err := json.Marshal(promptCacheLineageInput{
		Version:              promptCacheLineageVersion,
		Agent:                scope.AgentID,
		Session:              scope.SessionKey,
		Provider:             provider,
		Model:                model,
		PromptSchema:         promptSchema,
		ToolSchema:           toolSchema,
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
	tools []providers.ToolDefinition,
) map[string]any {
	opts := shallowCloneLLMOptions(base)
	delete(opts, "prompt_cache_key")
	key := buildPromptCacheLineageKey(
		scope,
		provider,
		model,
		promptCachePromptSchemaVersion,
		promptCacheToolSchemaFingerprint(tools),
	)
	if key != "" {
		opts["prompt_cache_key"] = key
	}
	return opts
}

func promptCacheDigest(value []byte, hexChars int) string {
	sum := sha256.Sum256(value)
	digest := hex.EncodeToString(sum[:])
	if hexChars <= 0 || hexChars >= len(digest) {
		return digest
	}
	return digest[:hexChars]
}
