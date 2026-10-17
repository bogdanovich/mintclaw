package tools

import (
	"context"
	"encoding/json"

	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker/intent"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	"github.com/bogdanovich/mintclaw/pkg/tools/loopguard"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

// CredentialReadSource retains authentication, approval and HTTP inside trusted
// runtime code. Its only output is an already protected public JSON projection.
type CredentialReadSource interface {
	CredentialReadAvailableForAgent(string) bool
	ReadCredentialPullRequests(context.Context, string, string) string
}

type CredentialReadTool struct{ source CredentialReadSource }

func NewCredentialReadTool(source CredentialReadSource) *CredentialReadTool {
	return &CredentialReadTool{source: source}
}

func (*CredentialReadTool) Name() string { return "github_pull_requests" }
func (*CredentialReadTool) RuntimeCapabilities() []runtimecap.CapabilityID {
	return []runtimecap.CapabilityID{runtimecap.CapabilityCredentialRead}
}

func (*CredentialReadTool) Description() string {
	return "List up to 20 open pull requests from an operator-configured GitHub repository. " +
		"Use only an account alias and owner/repository name; authentication stays private. " +
		"Phone approval is required without a current grant. For approval_required, wait for approval before retrying."
}

func (*CredentialReadTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"account_alias": map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
			"repository":    map[string]any{"type": "string", "minLength": 3, "maxLength": 140},
		},
		"required": []string{"account_alias", "repository"},
	}
}

func (tool *CredentialReadTool) ToolEnabledForAgent(agentID string) bool {
	return tool != nil && tool.source != nil && tool.source.CredentialReadAvailableForAgent(agentID)
}

func (*CredentialReadTool) ToolLoopSemantics() loopguard.Semantics {
	return loopguard.SemanticsMutating
}

func (*CredentialReadTool) DurableArguments(args map[string]any) (map[string]any, error) {
	alias, ok := args["account_alias"].(string)
	repository, repoOK := args["repository"].(string)
	canonical, err := credentialbroker.NormalizeGitHubRepository(repository)
	if len(args) != 2 || !ok || !repoOK || !intent.ValidAlias(alias) || err != nil {
		return nil, credentialbroker.ErrDenied
	}
	return map[string]any{"account_alias": alias, "repository": canonical}, nil
}

func (tool *CredentialReadTool) Execute(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	denied := func() *toolshared.ToolResult { return toolshared.SilentResult(`{"outcome":"denied"}`) }
	if !tool.ToolEnabledForAgent(toolshared.ToolAgentID(ctx)) {
		return denied()
	}
	values, err := tool.DurableArguments(args)
	if err != nil {
		return denied()
	}
	result := tool.source.ReadCredentialPullRequests(
		ctx,
		values["account_alias"].(string),
		values["repository"].(string),
	)
	if len(result) > 64*1024 || !json.Valid([]byte(result)) {
		return denied()
	}
	return toolshared.SilentResult(result)
}
