package tools

import (
	"context"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker/intent"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	"github.com/bogdanovich/mintclaw/pkg/tools/loopguard"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

// BrowserCredentialToolSource owns caller policy, approval, vault consumption
// and private browser submission. Tools provide only aliases and current refs.
type BrowserCredentialToolSource interface {
	CredentialLoginAvailable() bool
	LoginCredential(context.Context, browser.CredentialLoginRequest) browser.CredentialLoginResult
}

type BrowserLoginTool struct{ runtime *browserToolRuntime }

func NewBrowserLoginTool(options BrowserToolOptions, source BrowserToolSource) *BrowserLoginTool {
	return &BrowserLoginTool{runtime: newBrowserToolRuntime(options, source)}
}

func (*BrowserLoginTool) Name() string { return "browser_login" }

func (*BrowserLoginTool) RuntimeCapabilities() []runtimecap.CapabilityID {
	return []runtimecap.CapabilityID{runtimecap.CapabilityBrowserAct}
}

func (*BrowserLoginTool) Description() string {
	return "Log in using a configured account alias and the current observed username/password/submit refs. " +
		"Values stay inside the trusted runtime. Phone approval is required when no current grant exists. " +
		"Never supply passwords or tokens. Do not replay an outcome_unknown submission. " +
		"For approval_required, wait for phone approval before retrying."
}

func (*BrowserLoginTool) Parameters() map[string]any {
	properties := make(map[string]any)
	for _, key := range []string{
		"account_alias", "browser_session_id", "tab_id", "snapshot_id", "username_ref", "password_ref", "submit_ref",
	} {
		properties[key] = map[string]any{"type": "string", "minLength": 1, "maxLength": 128}
	}
	properties["snapshot_generation"] = map[string]any{"type": "integer", "minimum": 1}
	return map[string]any{
		"type": "object", "properties": properties, "additionalProperties": false,
		"required": []string{
			"account_alias", "browser_session_id", "tab_id", "snapshot_id", "snapshot_generation",
			"username_ref", "password_ref", "submit_ref",
		},
	}
}

func (tool *BrowserLoginTool) ToolEnabledForAgent(agentID string) bool {
	if tool == nil || tool.runtime == nil || !tool.runtime.enabledForAgent(agentID) {
		return false
	}
	source, ok := tool.runtime.source.(BrowserCredentialToolSource)
	return ok && source.CredentialLoginAvailable()
}

func (*BrowserLoginTool) ToolLoopSemantics() loopguard.Semantics { return loopguard.SemanticsMutating }

func (*BrowserLoginTool) DurableArguments(args map[string]any) (map[string]any, error) {
	if len(args) != 8 {
		return nil, browser.ErrInvalid
	}
	projected := make(map[string]any, 8)
	for _, key := range []string{
		"account_alias", "browser_session_id", "tab_id", "snapshot_id", "username_ref", "password_ref", "submit_ref",
	} {
		value, ok := args[key].(string)
		if !ok || value == "" || len(value) > 128 || strings.TrimSpace(value) != value {
			return nil, browser.ErrInvalid
		}
		projected[key] = value
	}
	generation, ok := browserInteger(args["snapshot_generation"])
	if !ok || generation < 1 || !intent.ValidAlias(projected["account_alias"].(string)) {
		return nil, browser.ErrInvalid
	}
	projected["snapshot_generation"] = generation
	return projected, nil
}

func (tool *BrowserLoginTool) Execute(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	denied := func() *toolshared.ToolResult {
		return toolshared.SilentResult(`{"outcome":"denied"}`)
	}
	if !tool.ToolEnabledForAgent(toolshared.ToolAgentID(ctx)) {
		return denied()
	}
	values, err := tool.DurableArguments(args)
	if err != nil {
		return denied()
	}
	owner, err := browserOwnerFromContext(ctx)
	if err != nil {
		return denied()
	}
	request := browser.CredentialLoginRequest{
		Owner: owner, Account: values["account_alias"].(string),
		SessionID: values["browser_session_id"].(string), TabID: values["tab_id"].(string),
		SnapshotID: values["snapshot_id"].(string), SnapshotGeneration: uint64(values["snapshot_generation"].(int)),
		Fields: browser.CredentialFields{
			UsernameRef: values["username_ref"].(string), PasswordRef: values["password_ref"].(string),
			SubmitRef: values["submit_ref"].(string),
		},
	}
	source := tool.runtime.source.(BrowserCredentialToolSource)
	result := source.LoginCredential(ctx, request)
	// The source supplies only fixed outcome codes. Defend the public tool
	// projection against an accidental future source returning private detail.
	switch result.Outcome {
	case "logged_in", "approval_required", "handoff_required", "denied", "failed", "outcome_unknown":
	default:
		return denied()
	}
	result.Alias = request.Account
	return tool.runtime.result(result)
}
