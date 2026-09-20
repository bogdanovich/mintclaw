package agent

import (
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

func TestInteractionContinuationPromptContextRequiresTerminalDecision(t *testing.T) {
	tests := []struct {
		name        string
		context     interactionContinuationPromptContext
		want        string
		doesNotWant string
	}{
		{name: "empty", context: interactionContinuationPromptContext{}},
		{
			name: "missing outcome",
			context: interactionContinuationPromptContext{
				Kind: interactions.KindQuestion,
			},
		},
		{
			name: "invalid question denial",
			context: interactionContinuationPromptContext{
				Kind: interactions.KindQuestion, Outcome: interactions.OutcomeDenied,
			},
		},
		{
			name: "answered question",
			context: interactionContinuationPromptContext{
				Kind: interactions.KindQuestion, Outcome: interactions.OutcomeAnswered,
				PromptLanguage: "ru-ru",
			},
			want: "It may continue, redirect, or end the",
		},
		{
			name: "allowed approval",
			context: interactionContinuationPromptContext{
				Kind: interactions.KindApproval, Outcome: interactions.OutcomeAllowed,
			},
			want: "The user allowed the protected operation. Invoke it",
		},
		{
			name: "denied approval",
			context: interactionContinuationPromptContext{
				Kind: interactions.KindApproval, Outcome: interactions.OutcomeDenied,
			},
			want:        "The user denied the protected operation. Do not invoke it",
			doesNotWant: "Invoke it when execution is still required.",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			content := test.context.promptContent()
			if test.want == "" && content != "" {
				t.Fatalf("prompt content = %q, want empty", content)
			}
			if test.want != "" && !strings.Contains(content, test.want) {
				t.Fatalf("prompt content = %q, want %q", content, test.want)
			}
			if test.doesNotWant != "" && strings.Contains(content, test.doesNotWant) {
				t.Fatalf("prompt content = %q, do not want %q", content, test.doesNotWant)
			}
			if test.context.PromptLanguage != "" &&
				!strings.Contains(content, `Preserve BCP-47 language "ru-ru"`) {
				t.Fatalf("prompt content omitted interaction language: %q", content)
			}
		})
	}
}

func TestInteractionContinuationPromptContextScopesTheFinalResponse(t *testing.T) {
	tests := []interactionContinuationPromptContext{
		{Kind: interactions.KindQuestion, Outcome: interactions.OutcomeAnswered},
		{Kind: interactions.KindApproval, Outcome: interactions.OutcomeAllowed},
		{Kind: interactions.KindApproval, Outcome: interactions.OutcomeDenied},
	}
	for _, test := range tests {
		content := test.promptContent()
		for _, required := range []string{
			"Complete and report only the suspended request associated with this interaction.",
			"Shared conversation history is context, not a queue of work to finish or summarize.",
			"Do not append status for unrelated tasks, background work, browser sessions, or older requests",
		} {
			if !strings.Contains(content, required) {
				t.Fatalf("prompt content for %s/%s omitted %q: %s", test.Kind, test.Outcome, required, content)
			}
		}
	}
}
