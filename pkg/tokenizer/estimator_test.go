package tokenizer

import (
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func TestEstimateMessageTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		msg  providers.Message
		want int
	}{
		{
			name: "empty message",
			msg:  providers.Message{},
			want: 4,
		},
		{
			name: "plain content",
			msg:  providers.Message{Content: "hello world"},
			want: 9,
		},
		{
			name: "content with multibyte runes counts runes",
			msg:  providers.Message{Content: "héllo"},
			want: 6,
		},
		{
			name: "system parts larger than content",
			msg: providers.Message{
				Content:     "short",
				SystemParts: []providers.ContentBlock{{Type: "text", Text: "a fairly long system instruction block"}},
			},
			want: 28,
		},
		{
			name: "content larger than system parts",
			msg: providers.Message{
				Content:     "a very long user content payload that exceeds the system block",
				SystemParts: []providers.ContentBlock{{Type: "text", Text: "small"}},
			},
			want: 29,
		},
		{
			name: "reasoning content adds to estimate",
			msg:  providers.Message{Content: "answer", ReasoningContent: "let me think step by step"},
			want: 17,
		},
		{
			name: "tool call with function",
			msg: providers.Message{
				Content: "use the tool",
				ToolCalls: []providers.ToolCall{
					{ID: "id1", Type: "function", Name: "search", Arguments: map[string]any{"q": "x"}},
				},
			},
			want: 20,
		},
		{
			name: "tool call without function uses top-level name",
			msg: providers.Message{
				ToolCalls: []providers.ToolCall{{ID: "id2", Type: "function", Name: "lookup"}},
			},
			want: 11,
		},
		{
			name: "tool call id adds overhead",
			msg:  providers.Message{Content: "hi", ToolCallID: "call_123"},
			want: 8,
		},
		{
			name: "media items add fixed per-item estimate",
			msg: providers.Message{
				Content: "describe",
				Media:   []string{"data:image/png;base64,AAAA", "data:image/png;base64,BBBB"},
			},
			want: 520,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := EstimateMessageTokens(tc.msg); got != tc.want {
				t.Fatalf("EstimateMessageTokens() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestEstimateToolDefsTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		defs []providers.ToolDefinition
		want int
	}{
		{name: "nil defs", defs: nil, want: 0},
		{name: "empty defs", defs: []providers.ToolDefinition{}, want: 0},
		{
			name: "single def with parameters",
			defs: []providers.ToolDefinition{{
				Function: providers.ToolFunctionDefinition{
					Name:        "get_weather",
					Description: "Get the weather",
					Parameters:  map[string]any{"type": "object"},
				},
			}},
			want: 25,
		},
		{
			name: "single def without parameters",
			defs: []providers.ToolDefinition{{
				Function: providers.ToolFunctionDefinition{Name: "f", Description: "d"},
			}},
			want: 8,
		},
		{
			name: "multiple defs accumulate",
			defs: []providers.ToolDefinition{
				{Function: providers.ToolFunctionDefinition{Name: "f1", Description: "d1"}},
				{Function: providers.ToolFunctionDefinition{Name: "f2", Description: "d2"}},
			},
			want: 19,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := EstimateToolDefsTokens(tc.defs); got != tc.want {
				t.Fatalf("EstimateToolDefsTokens() = %d, want %d", got, tc.want)
			}
		})
	}
}
