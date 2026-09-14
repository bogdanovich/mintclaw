package agent

import (
	"fmt"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

type interactionContinuationPromptContext struct {
	Kind           interactions.Kind
	Outcome        interactions.Outcome
	PromptLanguage string
}

func newInteractionContinuationPromptContext(
	record interactions.Record,
) interactionContinuationPromptContext {
	return interactionContinuationPromptContext{
		Kind:           record.Kind,
		Outcome:        record.Outcome,
		PromptLanguage: strings.TrimSpace(record.PromptLanguage),
	}
}

func (context interactionContinuationPromptContext) promptContent() string {
	kind := strings.TrimSpace(string(context.Kind))
	outcome := strings.TrimSpace(string(context.Outcome))
	if kind == "" || outcome == "" {
		return ""
	}
	guidance := context.outcomeGuidance()
	if guidance == "" {
		return ""
	}

	presentationGuidance := ""
	if context.PromptLanguage != "" {
		presentationGuidance = fmt.Sprintf(`
- Preserve BCP-47 language %q in every new user-facing interaction prompt, even when internal task instructions are in another language.`, context.PromptLanguage)
	}

	return fmt.Sprintf(`# Active human-interaction continuation

This turn is the live continuation of the same suspended user request.
The matching interaction tool result in the conversation history is authoritative.
Interaction kind: %s. Recorded outcome: %s.

%s%s
- While this turn is running, its durable interaction is expected to remain "resuming" until final delivery.
  Never report that status alone as a stuck continuation, missed restart, or evidence that this turn did not launch.
- A non-empty [voice: ...] marker is a successful transcription of the user's audio.
  Use that text and do not claim transcription failed or was empty.`, kind, outcome, guidance, presentationGuidance)
}

func (context interactionContinuationPromptContext) outcomeGuidance() string {
	switch {
	case context.Kind == interactions.KindQuestion && context.Outcome == interactions.OutcomeAnswered:
		return `- Treat the answer as the user's decision and continue the accumulated request.
  Do not ask for the same choice or an extra conversational confirmation unless new user input materially changes it.
- Invoke the tool selected by the user when execution is still required.
  Runtime approval policy, not the model, decides whether a protected tool needs a separate durable approval.`
	case context.Kind == interactions.KindApproval && context.Outcome == interactions.OutcomeAllowed:
		return `- The user allowed the protected operation. Invoke it when execution is still required.
  Do not ask for the same approval or an extra conversational confirmation unless new user input materially changes it.`
	case context.Kind == interactions.KindApproval && context.Outcome == interactions.OutcomeDenied:
		return `- The user denied the protected operation. Do not invoke it or reinterpret the denial as permission.
  Do not ask for the same approval again unless new user input explicitly changes that decision.`
	default:
		return ""
	}
}
