package providers

import "testing"

func TestNormalizeProvider(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"OpenAI", "openai"},
		{"ANTHROPIC", "anthropic"},
		{"z.ai", "zai"},
		{"z-ai", "zai"},
		{"Z.AI", "zai"},
		{"qwen", "qwen-portal"},
		{"gpt", "openai"},
		{"claude", "anthropic"},
		{"glm", "zhipu"},
		{"google", "gemini"},
		{"google-antigravity", "antigravity"},
		{"groq", "groq"},
		{"azure-openai", "azure"},
		{"claudecli", "claude-cli"},
		{"codexcli", "codex-cli"},
		{"copilot", "github-copilot"},
		{"g4f", "gpt4free"},
		// Alibaba Coding Plan aliases
		{"alibaba-coding", "alibaba-coding"},
		{"coding-plan", "alibaba-coding"},
		{"qwen-coding", "alibaba-coding"},
		{"alibaba-coding-anthropic", "alibaba-coding-anthropic"},
		{"coding-plan-anthropic", "alibaba-coding-anthropic"},
		// Qwen international aliases
		{"qwen-international", "qwen-intl"},
		{"dashscope-intl", "qwen-intl"},
		{"dashscope-us", "qwen-us"},
		{"", ""},
	}

	for _, tt := range tests {
		got := NormalizeProvider(tt.input)
		if got != tt.want {
			t.Errorf("NormalizeProvider(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestModelKey(t *testing.T) {
	tests := []struct {
		provider string
		model    string
		want     string
	}{
		{"openai", "gpt-4", "openai/gpt-4"},
		{"Anthropic", "Claude-Opus", "anthropic/claude-opus"},
		{"claude", "sonnet", "anthropic/sonnet"},
		{"z.ai", "Model-X", "zai/model-x"},
	}

	for _, tt := range tests {
		got := ModelKey(tt.provider, tt.model)
		if got != tt.want {
			t.Errorf("ModelKey(%q, %q) = %q, want %q", tt.provider, tt.model, got, tt.want)
		}
	}
}
