package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
)

const asyncCompletionSynthesisTimeout = 120 * time.Second

// AsyncCompletionInput is the typed internal form of an async tool completion
// that needs parent-agent synthesis. Runtime code passes this value directly
// instead of publishing a synthetic chat message.
type AsyncCompletionInput struct {
	SourceTool   string
	CompletionID string
	Content      string
	Origin       bus.InboundContext
	SenderID     string
}

func asyncCompletionID(turnID, toolCallID, toolName string) string {
	parts := []string{
		strings.TrimSpace(turnID),
		strings.TrimSpace(toolCallID),
		strings.TrimSpace(toolName),
	}
	for i, part := range parts {
		if part == "" {
			parts[i] = "unknown"
		}
	}
	return strings.Join(parts, ":")
}

func originTopicID(origin *bus.InboundContext) string {
	if origin == nil {
		return ""
	}
	return strings.TrimSpace(origin.TopicID)
}

func asyncCompletionPrompt(toolName, result string) string {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		toolName = "async_tool"
	}
	result = strings.TrimSpace(result)
	if result == "" {
		result = "(no result)"
	}

	return fmt.Sprintf(`[Internal async completion event]
source_tool: %s

Result:
<<<MINTCLAW_ASYNC_RESULT
%s
MINTCLAW_ASYNC_RESULT

Action:
Convert the result above into a concise user-facing update in your normal assistant voice and send that update now. Treat a structured objective_outcome as authoritative: when its status is succeeded or an external action has a verified receipt, report the action as completed and never describe it as pending approval or still waiting. Preserve any terminal result links and IDs. Keep this internal metadata private. Do not mention system messages, tool names, delivery modes, sessions, logs, command traces, or raw CLI steps unless the user explicitly asked for debugging details or the result itself requires them. Do not copy the internal event text verbatim.`, toolName, result)
}
