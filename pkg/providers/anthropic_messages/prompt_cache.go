package anthropicmessages

import (
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/providers/protocoltypes"
)

func compilePromptCachePlan(
	requestBody map[string]any,
	sourceMessages []Message,
	apiMessages []any,
	messageSourceEndIndexes []int,
	options map[string]any,
	promptCacheSupported bool,
) {
	plan, valid := protocoltypes.PromptCachePlanFromOptions(options)
	present := protocoltypes.PromptCachePlanOptionPresent(options)
	if !promptCacheSupported || !present || !valid || plan.WritePolicy != protocoltypes.PromptCacheWriteReuse {
		// This adapter had no legacy cache-control behavior, so leaving the
		// original body untouched is also the fail-closed representation.
		return
	}

	breakpoints := make(map[int]struct{}, len(plan.BreakpointMessageIndexes))
	for _, index := range plan.BreakpointMessageIndexes {
		breakpoints[index] = struct{}{}
	}
	markSystemCacheBoundary(requestBody, sourceMessages, breakpoints)
	markToolCacheBoundary(requestBody)
	markMessageCacheBoundary(apiMessages, messageSourceEndIndexes, breakpoints)
}

func markSystemCacheBoundary(
	requestBody map[string]any,
	sourceMessages []Message,
	breakpoints map[int]struct{},
) {
	original, ok := requestBody["system"].(string)
	if !ok {
		return
	}
	blocks := make([]systemCacheBlock, 0)
	explicitMarker := -1
	fallbackMarker := -1
	seenSystem := false
	for sourceIndex, message := range sourceMessages {
		if message.Role != "system" {
			continue
		}
		if seenSystem && len(blocks) > 0 {
			blocks[len(blocks)-1].text += "\n\n"
		}
		seenSystem = true
		start := len(blocks)
		if len(message.SystemParts) > 0 {
			messageBlocks, matched := exactSystemPartBlocks(message)
			if !matched {
				// The cache marker must never change model-visible system text. If
				// structured parts cannot be mapped exactly, keep the original string.
				return
			}
			for _, block := range messageBlocks {
				blocks = append(blocks, block)
				if block.cacheable {
					explicitMarker = len(blocks) - 1
				}
			}
		} else {
			blocks = append(blocks, systemCacheBlock{text: message.Content})
		}
		if _, marked := breakpoints[sourceIndex]; marked && len(blocks) > start {
			fallbackMarker = len(blocks) - 1
		}
	}
	marker := explicitMarker
	if marker < 0 {
		marker = fallbackMarker
	}
	if marker < 0 || marker >= len(blocks) {
		return
	}
	serialized := make([]map[string]any, len(blocks))
	var reconstructed strings.Builder
	for index, block := range blocks {
		reconstructed.WriteString(block.text)
		serialized[index] = map[string]any{"type": "text", "text": block.text}
	}
	if reconstructed.String() != original {
		return
	}
	serialized[marker]["cache_control"] = ephemeralCacheControl()
	requestBody["system"] = serialized
}

type systemCacheBlock struct {
	text      string
	cacheable bool
}

func exactSystemPartBlocks(message Message) ([]systemCacheBlock, bool) {
	starts := make([]int, len(message.SystemParts))
	searchFrom := 0
	for index, part := range message.SystemParts {
		if part.Text == "" || searchFrom > len(message.Content) {
			return nil, false
		}
		relative := strings.Index(message.Content[searchFrom:], part.Text)
		if relative < 0 {
			return nil, false
		}
		starts[index] = searchFrom + relative
		searchFrom = starts[index] + len(part.Text)
	}

	blocks := make([]systemCacheBlock, len(message.SystemParts))
	for index, part := range message.SystemParts {
		start := starts[index]
		if index == 0 {
			start = 0
		}
		end := len(message.Content)
		if index+1 < len(starts) {
			end = starts[index+1]
		}
		if start > end || !strings.Contains(message.Content[start:end], part.Text) {
			return nil, false
		}
		blocks[index] = systemCacheBlock{
			text:      message.Content[start:end],
			cacheable: part.CacheControl != nil && part.CacheControl.Type == "ephemeral",
		}
	}
	return blocks, true
}

func markToolCacheBoundary(requestBody map[string]any) {
	tools, ok := requestBody["tools"].([]any)
	if !ok {
		return
	}
	for index := len(tools) - 1; index >= 0; index-- {
		tool, ok := tools[index].(map[string]any)
		if !ok {
			continue
		}
		tool["cache_control"] = ephemeralCacheControl()
		return
	}
}

func markMessageCacheBoundary(
	apiMessages []any,
	messageSourceEndIndexes []int,
	breakpoints map[int]struct{},
) {
	for messageIndex := len(messageSourceEndIndexes) - 1; messageIndex >= 0; messageIndex-- {
		if _, marked := breakpoints[messageSourceEndIndexes[messageIndex]]; !marked {
			continue
		}
		if messageIndex >= len(apiMessages) {
			continue
		}
		message, ok := apiMessages[messageIndex].(map[string]any)
		if !ok {
			continue
		}
		if markMessageContentEnd(message) {
			return
		}
	}
}

func markMessageContentEnd(message map[string]any) bool {
	switch content := message["content"].(type) {
	case string:
		message["content"] = []map[string]any{{
			"type":          "text",
			"text":          content,
			"cache_control": ephemeralCacheControl(),
		}}
		return true
	case []map[string]any:
		if len(content) == 0 {
			return false
		}
		content[len(content)-1]["cache_control"] = ephemeralCacheControl()
		message["content"] = content
		return true
	case []any:
		for index := len(content) - 1; index >= 0; index-- {
			block, ok := content[index].(map[string]any)
			if !ok {
				continue
			}
			block["cache_control"] = ephemeralCacheControl()
			message["content"] = content
			return true
		}
	}
	return false
}

func ephemeralCacheControl() map[string]any {
	return map[string]any{"type": "ephemeral"}
}
