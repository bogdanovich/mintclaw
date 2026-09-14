package agent

import (
	"fmt"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

type interactionContinuationPromptContext struct {
	Kind    interactions.Kind
	Outcome interactions.Outcome
}

func newInteractionContinuationPromptContext(
	record interactions.Record,
) interactionContinuationPromptContext {
	return interactionContinuationPromptContext{
		Kind:    record.Kind,
		Outcome: record.Outcome,
	}
}

func (context interactionContinuationPromptContext) promptContent() string {
	kind := strings.TrimSpace(string(context.Kind))
	outcome := strings.TrimSpace(string(context.Outcome))
	if kind == "" || outcome == "" {
		return ""
	}

	return fmt.Sprintf(`# Active human-interaction continuation

This turn is the live continuation of the same suspended user request.
The matching interaction tool result in the conversation history is authoritative.
Interaction kind: %s. Accepted outcome: %s.

- Treat an answered question as the user's decision and continue the accumulated request.
  Do not ask for the same choice or an extra conversational confirmation unless new user input materially changes it.
- Invoke the tool selected by the user when execution is still required.
  Runtime approval policy, not the model, decides whether a protected tool needs a separate durable approval.
- While this turn is running, its durable interaction is expected to remain "resuming" until final delivery.
  Never report that status alone as a stuck continuation, missed restart, or evidence that this turn did not launch.
- A non-empty [voice: ...] or [voice transcript: ...] marker is a successful transcription of the user's audio.
  Use that text and do not claim transcription failed or was empty.`, kind, outcome)
}
