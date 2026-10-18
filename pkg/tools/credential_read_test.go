package tools

import (
	"context"
	"strings"
	"testing"

	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type credentialReadTestSource struct{ calls int }

func (*credentialReadTestSource) CredentialReadAvailableForAgent(agentID string) bool {
	return agentID == "main"
}

func (source *credentialReadTestSource) ReadCredentialPullRequests(_ context.Context, alias, repository string) string {
	source.calls++
	if alias != "github" || repository != "owner/repo" {
		return `{"outcome":"denied"}`
	}
	return `{"alias":"github","outcome":"success","pull_requests":[]}`
}

func TestCredentialReadRejectsSecretAndEndpointArgumentsBeforePrivateSource(t *testing.T) {
	source := &credentialReadTestSource{}
	tool := NewCredentialReadTool(source)
	ctx := toolshared.WithToolSessionContext(t.Context(), "main", "session", nil)
	for _, args := range []map[string]any{
		{"account_alias": "github", "repository": "owner/repo", "token": "private-model-input-H38"},
		{"account_alias": "github", "repository": "https://other.invalid/repo"},
		{"account_alias": "github", "repository": "owner/../repo"},
		{"account_alias": "github", "repository": "owner/repo", "endpoint": "https://other.invalid"},
	} {
		result := tool.Execute(ctx, args)
		if source.calls != 0 || result == nil || result.ForLLM != `{"outcome":"denied"}` ||
			strings.Contains(result.ForLLM, "private-model-input-H38") {
			t.Fatal("unscoped connector arguments reached the private source or escaped projection")
		}
	}
	result := tool.Execute(ctx, map[string]any{"account_alias": "github", "repository": "Owner/Repo"})
	if source.calls != 1 || !strings.Contains(result.ForLLM, `"outcome":"success"`) {
		t.Fatal("configured connector was unavailable")
	}
	if result = tool.Execute(
		t.Context(),
		map[string]any{"account_alias": "github", "repository": "owner/repo"},
	); source.calls != 1 ||
		result.ForLLM != `{"outcome":"denied"}` {
		t.Fatal("connector accepted a missing agent context")
	}
}
