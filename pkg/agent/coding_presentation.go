package agent

import (
	"strings"

	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
)

// emitCodingAssistantMessageCommitted publishes provider text only after its
// successful canonical admission point, or for an admitted history-free
// coding turn. Coding runtimes do not admit hooks, and diagnostic logging
// redacts the raw fields carried to the local frontend.
func (p *Pipeline) emitCodingAssistantMessageCommitted(
	ts *turnState,
	messageID string,
	phase AssistantMessagePhase,
	content, reasoningContent string,
) {
	if p == nil || ts == nil || ts.opts.mode != turnModeCoding {
		return
	}
	if strings.TrimSpace(content) == "" && strings.TrimSpace(reasoningContent) == "" {
		return
	}
	p.emitEvent(
		runtimeevents.KindAgentAssistantMessageCommitted,
		ts.eventMeta("runTurn", "turn.assistant_message.committed"),
		AssistantMessageCommittedPayload{
			MessageID:        strings.TrimSpace(messageID),
			Phase:            phase,
			Content:          content,
			ReasoningContent: reasoningContent,
			ContentLen:       len(content),
			ReasoningLen:     len(reasoningContent),
		},
	)
}
