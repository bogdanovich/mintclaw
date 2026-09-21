package agent

import (
	"encoding/json"

	"github.com/bogdanovich/mintclaw/pkg/providers"
)

// PromptCacheFingerprint describes the provider-neutral request segments that
// determine prefix-cache reuse. It intentionally contains hashes and counts
// only; prompt text, tool arguments, session identifiers, and cache keys never
// belong in this diagnostic contract.
type PromptCacheFingerprint struct {
	StableSystemHash              string
	DynamicSystemHash             string
	ToolSchemaHash                string
	HistoryHash                   string
	DynamicTailHash               string
	StableSystemParts             int
	DynamicSystemParts            int
	HistoryMessages               int
	DynamicTailMessages           int
	TailBoundaryFound             bool
	DynamicSystemBeforeTranscript bool
}

type promptCacheSystemPart struct {
	MessageIndex int                    `json:"message_index"`
	PartIndex    int                    `json:"part_index"`
	Block        providers.ContentBlock `json:"block"`
}

type promptCacheSystemMessage struct {
	MessageIndex int               `json:"message_index"`
	Message      providers.Message `json:"message"`
}

// fingerprintPromptCacheRequest projects the shared agent request shape before
// provider-specific serialization. The trace settings redact configured
// secrets before hashing and protected tool results use their durable receipt.
func fingerprintPromptCacheRequest(
	settings traceCaptureSettings,
	messages []providers.Message,
	tools []providers.ToolDefinition,
) PromptCacheFingerprint {
	projected := diagnosticPromptHashMessages(messages)
	stableSystem, dynamicSystem := promptCacheSystemSegments(projected)
	tailStart, boundaryFound := promptCacheDynamicTailStart(messages)
	history, dynamicTail := promptCacheTranscriptSegments(projected, tailStart)

	return PromptCacheFingerprint{
		StableSystemHash:              safeJSONHash(settings, stableSystem),
		DynamicSystemHash:             safeJSONHash(settings, dynamicSystem),
		ToolSchemaHash:                safeJSONHash(settings, providerVisibleToolDefinitions(tools)),
		HistoryHash:                   safeJSONHash(settings, history),
		DynamicTailHash:               safeJSONHash(settings, dynamicTail),
		StableSystemParts:             len(stableSystem),
		DynamicSystemParts:            len(dynamicSystem),
		HistoryMessages:               len(history),
		DynamicTailMessages:           len(dynamicTail),
		TailBoundaryFound:             boundaryFound,
		DynamicSystemBeforeTranscript: len(dynamicSystem) > 0,
	}
}

func promptCacheSystemSegments(
	messages []providers.Message,
) ([]any, []any) {
	stable := make([]any, 0)
	dynamic := make([]any, 0)
	stablePrefixOpen := true

	for messageIndex, message := range messages {
		if message.Role != "system" {
			continue
		}
		if len(message.SystemParts) == 0 {
			stablePrefixOpen = false
			dynamic = append(dynamic, promptCacheSystemMessage{
				MessageIndex: messageIndex,
				Message:      promptCacheProviderVisibleMessage(message),
			})
			continue
		}

		for partIndex, block := range message.SystemParts {
			item := promptCacheSystemPart{
				MessageIndex: messageIndex,
				PartIndex:    partIndex,
				Block:        block,
			}
			if stablePrefixOpen && promptCacheBlockIsStable(block) {
				stable = append(stable, item)
				continue
			}
			stablePrefixOpen = false
			dynamic = append(dynamic, item)
		}
	}

	return stable, dynamic
}

func promptCacheBlockIsStable(block providers.ContentBlock) bool {
	return block.CacheControl != nil && block.CacheControl.Type == "ephemeral"
}

func promptCacheDynamicTailStart(messages []providers.Message) (int, bool) {
	for index, message := range messages {
		if message.Role == "system" {
			continue
		}
		if message.PromptLayer == string(PromptLayerTurn) ||
			message.PromptSource == string(PromptSourceRuntime) {
			return index, true
		}
	}
	return len(messages), false
}

func promptCacheTranscriptSegments(
	messages []providers.Message,
	tailStart int,
) ([]providers.Message, []providers.Message) {
	history := make([]providers.Message, 0, len(messages))
	dynamicTail := make([]providers.Message, 0, len(messages)-min(tailStart, len(messages)))
	for index, message := range messages {
		if message.Role == "system" {
			continue
		}
		message = promptCacheProviderVisibleMessage(message)
		if index < tailStart {
			history = append(history, message)
		} else {
			dynamicTail = append(dynamicTail, message)
		}
	}
	return history, dynamicTail
}

func promptCacheProviderVisibleMessage(message providers.Message) providers.Message {
	return stripCanonicalMessageState(providerVisibleMessage(message))
}

// promptCacheRequestIsPrefix reports whether the provider-neutral canonical
// representation of previous is an exact prefix of next with identical tools.
// Provider adapters get their own wire-level assertions in a later stage.
func promptCacheRequestIsPrefix(
	previousMessages []providers.Message,
	previousTools []providers.ToolDefinition,
	nextMessages []providers.Message,
	nextTools []providers.ToolDefinition,
) bool {
	if len(previousMessages) > len(nextMessages) ||
		!promptCacheCanonicalEqual(
			providerVisibleToolDefinitions(previousTools),
			providerVisibleToolDefinitions(nextTools),
		) {
		return false
	}

	for index, message := range previousMessages {
		if !promptCacheCanonicalEqual(
			promptCacheProviderVisibleMessage(message),
			promptCacheProviderVisibleMessage(nextMessages[index]),
		) {
			return false
		}
	}
	return true
}

func promptCacheCanonicalEqual(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}
