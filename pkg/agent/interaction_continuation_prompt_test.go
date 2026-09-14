package agent

import (
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

func TestInteractionContinuationPromptContextRequiresTerminalDecision(t *testing.T) {
	tests := []struct {
		name    string
		context interactionContinuationPromptContext
		want    string
	}{
		{name: "empty", context: interactionContinuationPromptContext{}},
		{
			name: "missing outcome",
			context: interactionContinuationPromptContext{
				Kind: interactions.KindQuestion,
			},
		},
		{
			name: "answered question",
			context: interactionContinuationPromptContext{
				Kind: interactions.KindQuestion, Outcome: interactions.OutcomeAnswered,
			},
			want: "Interaction kind: question. Accepted outcome: answered.",
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
		})
	}
}
