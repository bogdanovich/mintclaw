package utils

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func BuildVisibleToolCalls(
	toolCalls []providers.ToolCall,
	maxArgsLen int,
) []bus.OutboundToolCall {
	if len(toolCalls) == 0 {
		return nil
	}

	visible := make([]bus.OutboundToolCall, 0, len(toolCalls))
	for _, tc := range toolCalls {
		name, _ := VisibleToolCallNameAndArguments(tc)
		argsPreview := VisibleToolCallArgumentsPreview(tc, maxArgsLen)
		explanation := ""
		if tc.ExtraContent != nil {
			explanation = strings.TrimSpace(tc.ExtraContent.ToolFeedbackExplanation)
		}
		if name == "" && explanation == "" && argsPreview == "" {
			continue
		}

		visibleCall := bus.OutboundToolCall{
			ID:   strings.TrimSpace(tc.ID),
			Type: strings.TrimSpace(tc.Type),
		}
		if visibleCall.Type == "" {
			visibleCall.Type = "function"
		}
		if name != "" || argsPreview != "" {
			visibleCall.Function = &bus.OutboundToolCallFunction{
				Name:      name,
				Arguments: argsPreview,
			}
		}
		if explanation != "" {
			visibleCall.ExtraContent = &bus.OutboundToolCallExtraContent{
				ToolFeedbackExplanation: explanation,
			}
		}

		visible = append(visible, visibleCall)
	}

	if len(visible) == 0 {
		return nil
	}
	return visible
}

func VisibleToolCallNameAndArguments(tc providers.ToolCall) (string, string) {
	name := strings.TrimSpace(tc.Name)
	argsJSON := ""
	if tc.Function != nil {
		if name == "" {
			name = strings.TrimSpace(tc.Function.Name)
		}
		argsJSON = strings.TrimSpace(tc.Function.Arguments)
	}
	if argsJSON == "" && len(tc.Arguments) > 0 {
		if encodedArgs, err := json.Marshal(tc.Arguments); err == nil {
			argsJSON = string(encodedArgs)
		}
	}
	return name, strings.TrimSpace(argsJSON)
}

func VisibleToolCallArgumentsPreview(tc providers.ToolCall, maxLen int) string {
	_, argsJSON := VisibleToolCallNameAndArguments(tc)
	if argsJSON == "" {
		return ""
	}

	var pretty bytes.Buffer
	if err := json.Indent(&pretty, []byte(argsJSON), "", "  "); err == nil {
		argsJSON = pretty.String()
	}
	if maxLen > 0 {
		return Truncate(argsJSON, maxLen)
	}
	return argsJSON
}
