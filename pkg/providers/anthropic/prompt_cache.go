package anthropicprovider

import (
	"github.com/anthropics/anthropic-sdk-go"

	"github.com/bogdanovich/mintclaw/pkg/providers/protocoltypes"
)

func compilePromptCachePlan(
	params *anthropic.MessageNewParams,
	sourceMessages []Message,
	messageSourceIndexes []int,
	options map[string]any,
	promptCacheSupported bool,
) {
	plan, valid := protocoltypes.PromptCachePlanFromOptions(options)
	present := protocoltypes.PromptCachePlanOptionPresent(options)
	if !promptCacheSupported || (present && (!valid || plan.WritePolicy != protocoltypes.PromptCacheWriteReuse)) {
		clearPromptCacheControls(params)
		return
	}
	if !present {
		// Preserve the pre-plan SystemParts behavior for legacy callers on the
		// native endpoint. Custom endpoints were cleared above.
		return
	}

	clearPromptCacheControls(params)
	breakpoints := make(map[int]struct{}, len(plan.BreakpointMessageIndexes))
	for _, index := range plan.BreakpointMessageIndexes {
		breakpoints[index] = struct{}{}
	}

	markSystemCacheBoundary(params, sourceMessages, breakpoints)
	markToolCacheBoundary(params)
	markMessageCacheBoundary(params, messageSourceIndexes, breakpoints)
}

func clearPromptCacheControls(params *anthropic.MessageNewParams) {
	params.CacheControl = anthropic.CacheControlEphemeralParam{}
	for index := range params.System {
		params.System[index].CacheControl = anthropic.CacheControlEphemeralParam{}
	}
	for index := range params.Tools {
		if params.Tools[index].OfTool != nil {
			params.Tools[index].OfTool.CacheControl = anthropic.CacheControlEphemeralParam{}
		}
	}
	for messageIndex := range params.Messages {
		for blockIndex := range params.Messages[messageIndex].Content {
			clearContentBlockCacheControl(&params.Messages[messageIndex].Content[blockIndex])
		}
	}
}

func markSystemCacheBoundary(
	params *anthropic.MessageNewParams,
	sourceMessages []Message,
	breakpoints map[int]struct{},
) {
	position := 0
	explicitMarker := -1
	fallbackMarker := -1
	for sourceIndex, message := range sourceMessages {
		if message.Role != "system" {
			continue
		}
		blockCount := 1
		if len(message.SystemParts) > 0 {
			blockCount = len(message.SystemParts)
			for partIndex, part := range message.SystemParts {
				if part.CacheControl != nil && part.CacheControl.Type == "ephemeral" {
					explicitMarker = position + partIndex
				}
			}
		}
		if _, marked := breakpoints[sourceIndex]; marked {
			fallbackMarker = position + blockCount - 1
		}
		position += blockCount
	}
	marker := explicitMarker
	if marker < 0 {
		marker = fallbackMarker
	}
	if marker >= 0 && marker < len(params.System) {
		params.System[marker].CacheControl = anthropic.NewCacheControlEphemeralParam()
	}
}

func markToolCacheBoundary(params *anthropic.MessageNewParams) {
	for index := len(params.Tools) - 1; index >= 0; index-- {
		if params.Tools[index].OfTool == nil {
			continue
		}
		params.Tools[index].OfTool.CacheControl = anthropic.NewCacheControlEphemeralParam()
		return
	}
}

func markMessageCacheBoundary(
	params *anthropic.MessageNewParams,
	messageSourceIndexes []int,
	breakpoints map[int]struct{},
) {
	for messageIndex := len(messageSourceIndexes) - 1; messageIndex >= 0; messageIndex-- {
		if _, marked := breakpoints[messageSourceIndexes[messageIndex]]; !marked {
			continue
		}
		if messageIndex >= len(params.Messages) {
			continue
		}
		blocks := params.Messages[messageIndex].Content
		for blockIndex := len(blocks) - 1; blockIndex >= 0; blockIndex-- {
			if markContentBlockCacheControl(&blocks[blockIndex]) {
				params.Messages[messageIndex].Content = blocks
				return
			}
		}
	}
}

func clearContentBlockCacheControl(block *anthropic.ContentBlockParamUnion) {
	switch {
	case block.OfText != nil:
		block.OfText.CacheControl = anthropic.CacheControlEphemeralParam{}
	case block.OfToolUse != nil:
		block.OfToolUse.CacheControl = anthropic.CacheControlEphemeralParam{}
	case block.OfToolResult != nil:
		block.OfToolResult.CacheControl = anthropic.CacheControlEphemeralParam{}
	}
}

func markContentBlockCacheControl(block *anthropic.ContentBlockParamUnion) bool {
	cacheControl := anthropic.NewCacheControlEphemeralParam()
	switch {
	case block.OfText != nil:
		block.OfText.CacheControl = cacheControl
	case block.OfToolUse != nil:
		block.OfToolUse.CacheControl = cacheControl
	case block.OfToolResult != nil:
		block.OfToolResult.CacheControl = cacheControl
	default:
		return false
	}
	return true
}
