package providers

import (
	"time"

	cliprovider "github.com/bogdanovich/mintclaw/pkg/providers/cli"
)

type (
	ClaudeCliProvider     = cliprovider.ClaudeCliProvider
	CodexCliProvider      = cliprovider.CodexCliProvider
	CodexCliAuth          = cliprovider.CodexCliAuth
	GitHubCopilotProvider = cliprovider.GitHubCopilotProvider
)

const CodexHomeEnvVar = cliprovider.CodexHomeEnvVar

func NewClaudeCliProvider(workspace string) *ClaudeCliProvider {
	return cliprovider.NewClaudeCliProvider(workspace)
}

func NewCodexCliProvider(workspace string) *CodexCliProvider {
	return cliprovider.NewCodexCliProvider(workspace)
}

func NewGitHubCopilotProvider(uri string, model string) (*GitHubCopilotProvider, error) {
	return cliprovider.NewGitHubCopilotProvider(uri, model)
}

func ReadCodexCliCredentials() (accessToken, accountID string, expiresAt time.Time, err error) {
	return cliprovider.ReadCodexCliCredentials()
}

func CreateCodexCliTokenSource() func() (string, string, error) {
	return cliprovider.CreateCodexCliTokenSource()
}

func NormalizeToolCall(tc ToolCall) ToolCall {
	if tc.Arguments == nil {
		tc.Arguments = map[string]any{}
	}
	return tc
}
