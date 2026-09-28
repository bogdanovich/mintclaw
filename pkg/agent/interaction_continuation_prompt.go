package agent

import (
	"fmt"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

type interactionContinuationPromptContext struct {
	Kind             interactions.Kind
	Outcome          interactions.Outcome
	PromptLanguage   string
	OriginToolCallID string
	OriginToolName   string
	ProtectedAnswer  string
	TypedChoice      bus.InboundInteractionChoice
}

func newInteractionContinuationPromptContext(
	record interactions.Record,
) interactionContinuationPromptContext {
	return interactionContinuationPromptContext{
		Kind:             record.Kind,
		Outcome:          record.Outcome,
		PromptLanguage:   strings.TrimSpace(record.PromptLanguage),
		OriginToolCallID: strings.TrimSpace(record.Origin.ToolCallID),
		OriginToolName:   strings.TrimSpace(record.Origin.ToolName),
		ProtectedAnswer:  protectedAnswerReference(record),
		TypedChoice:      trustedTypedNavigationChoice(record),
	}
}

func trustedTypedNavigationChoice(record interactions.Record) bus.InboundInteractionChoice {
	if record.Answer == nil || !record.Answer.Superseded {
		return ""
	}
	switch record.Answer.Choice {
	case bus.InboundInteractionChoiceClarify, bus.InboundInteractionChoiceBack:
		return record.Answer.Choice
	default:
		return ""
	}
}

func protectedAnswerReference(record interactions.Record) string {
	if record.Answer == nil || record.Answer.Protected == nil {
		return ""
	}
	return strings.TrimSpace(record.Answer.Protected.Reference)
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
	protectedGuidance := ""
	if context.ProtectedAnswer != "" {
		protectedGuidance = `
- The accepted protected value is represented only by an opaque receipt. The runtime will first require its originating
  trusted tool to consume that receipt. Never repeat the question or ask for the protected value in plain conversation.`
	}
	typedChoiceGuidance := context.typedChoiceGuidance()

	return fmt.Sprintf(`# Active human-interaction continuation

This turn is the live continuation of the same suspended user request.
The matching interaction tool result in the conversation history is authoritative.
Interaction kind: %s. Recorded outcome: %s.

%s%s%s%s
- Complete and report only the suspended request associated with this interaction.
  Shared conversation history is context, not a queue of work to finish or summarize.
  Do not append status for unrelated tasks, background work, browser sessions, or older requests merely because they
  appear in that history.
- While this turn is running, its durable interaction is expected to remain "resuming" until final delivery.
  Never report that status alone as a stuck continuation, missed restart, or evidence that this turn did not launch.
- A non-empty [voice: ...] marker is a successful transcription of the user's audio.
  Use that text and do not claim transcription failed or was empty.`, kind, outcome, guidance, presentationGuidance,
		protectedGuidance, typedChoiceGuidance)
}

func (context interactionContinuationPromptContext) typedChoiceGuidance() string {
	toolName := strings.TrimSpace(context.OriginToolName)
	if toolName == "" {
		toolName = "the originating trusted tool"
	} else {
		toolName = fmt.Sprintf("the originating trusted tool %q", toolName)
	}
	switch context.TypedChoice {
	case bus.InboundInteractionChoiceClarify:
		return fmt.Sprintf(`
- The user activated the trusted typed navigation action "clarify". It is not a field value. Explain the requested
  fact and why it matters, then use %s according to its documented navigation contract to reopen the same protected
  question. Do not request the protected value in ordinary chat or merely say that you are ready to continue.`, toolName)
	case bus.InboundInteractionChoiceBack:
		return fmt.Sprintf(`
- The user activated the trusted typed navigation action "back". It is not a field value. Use %s according to its
  documented navigation contract to move to the prior editable step now. If no prior step exists, explain that
  bounded outcome. Do not proceed as though the current question was answered or merely describe a future action.`, toolName)
	default:
		return ""
	}
}

func (context interactionContinuationPromptContext) outcomeGuidance() string {
	switch {
	case context.Kind == interactions.KindQuestion && context.Outcome == interactions.OutcomeAnswered:
		return `- Treat the answer as the user's latest authoritative guidance. It may continue, redirect, or end the
  suspended request. Do not ask for the same choice again or revive superseded steps.
- Before external work resumes for a live resource, follow the runtime continuation preflight. Invoke the tool selected
  by the user only when that decision permits continued execution and execution is still required.
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
