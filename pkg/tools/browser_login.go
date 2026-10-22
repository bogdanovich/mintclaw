package tools

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker/intent"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	"github.com/bogdanovich/mintclaw/pkg/tools/loopguard"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

// BrowserCredentialToolSource owns caller policy, approval, vault consumption
// and private browser submission. Tools provide only aliases and current refs.
type BrowserCredentialToolSource interface {
	CredentialLoginAvailable() bool
	LoginCredential(context.Context, browser.CredentialLoginRequest) browser.CredentialLoginResult
}

// BrowserCredentialApprovalSource resolves a saved condition against the
// original authenticated browser owner, without a live browser or vault reads.
type BrowserCredentialApprovalSource interface {
	CredentialApprovalState(context.Context, browser.Owner, string) toolshared.ExternalWaitState
}

type BrowserLoginTool struct{ runtime *browserToolRuntime }

func NewBrowserLoginTool(options BrowserToolOptions, source BrowserToolSource) *BrowserLoginTool {
	return &BrowserLoginTool{runtime: newBrowserToolRuntime(options, source)}
}

func (*BrowserLoginTool) Name() string { return "browser_login" }

func (*BrowserLoginTool) RuntimeCapabilities() []runtimecap.CapabilityID {
	return []runtimecap.CapabilityID{runtimecap.CapabilityBrowserLogin}
}

func (*BrowserLoginTool) Description() string {
	return "Find and use saved website Logins privately. Open the HTTPS login page and use operation discover " +
		"to find matching names and credential_ids in the dedicated vault. No per-site setup is needed. " +
		"Use credentials for a combined form, username/password for separate steps, and totp for a stored " +
		"authenticator code. Supply only current observed field/submit refs and the selected credential_id; " +
		"one website match is selected automatically. Omit account_alias for discovery. " +
		"After submitted, observe the page for the next step or errors. To confirm authentication, expose a " +
		"visible Log out/Sign out control and use verify with its authenticated_ref; a submit is not proof of login. " +
		"Confirmed logged_in returns a receipt_id for reporting the completed external action. " +
		"On observe_required, no values were read or submitted: observe the current form and retry once with " +
		"fresh refs; if it repeats, hand off. Do not reuse old refs or retry denied/outcome_unknown submissions. " +
		"If an unsupported challenge appears, hand off without guessing codes or repeating failed logins. " +
		"Values stay inside the trusted runtime. Phone approval is required when no current vault grant exists. " +
		"Never supply passwords or tokens. Do not replay an outcome_unknown submission. " +
		"Phone approval suspends this task automatically; after approval open a fresh session and observe fresh fields."
}

func (*BrowserLoginTool) Parameters() map[string]any {
	properties := make(map[string]any)
	for _, key := range []string{
		"account_alias", "credential_id", "browser_session_id", "tab_id", "snapshot_id",
		"username_ref", "password_ref", "otp_ref", "submit_ref", "authenticated_ref",
	} {
		properties[key] = map[string]any{"type": "string", "minLength": 1, "maxLength": 128}
	}
	properties["operation"] = map[string]any{
		"type": "string", "enum": []string{"discover", "credentials", "username", "password", "totp", "verify"},
	}
	properties["login_name"] = map[string]any{
		"type": "string", "minLength": 1, "maxLength": 256,
		"description": "Optional title filter among website matches; never a username or password.",
	}
	properties["snapshot_generation"] = map[string]any{"type": "integer", "minimum": 1}
	return map[string]any{
		"type": "object", "properties": properties, "additionalProperties": false,
		"required": []string{
			"browser_session_id", "tab_id", "snapshot_id", "snapshot_generation",
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
	if _, explicitStep := args["operation"]; explicitStep || args["account_alias"] == nil {
		return projectBrowserLoginStep(args)
	}
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
		Owner: owner, Account: browserLoginString(values, "account_alias"),
		CredentialID: browserLoginString(values, "credential_id"), LoginName: browserLoginString(values, "login_name"),
		SessionID: values["browser_session_id"].(string), TabID: values["tab_id"].(string),
		SnapshotID: values["snapshot_id"].(string), SnapshotGeneration: uint64(values["snapshot_generation"].(int)),
		Fields: browser.CredentialFields{
			Step: browserLoginString(values, "operation"),
			UsernameRef: browserLoginString(
				values,
				"username_ref",
			),
			PasswordRef:      browserLoginString(values, "password_ref"),
			SubmitRef:        browserLoginString(values, "submit_ref"),
			OTPRef:           browserLoginString(values, "otp_ref"),
			AuthenticatedRef: browserLoginString(values, "authenticated_ref"),
		},
	}
	source := tool.runtime.source.(BrowserCredentialToolSource)
	result := source.LoginCredential(ctx, request)
	// The source supplies only fixed outcome codes. Defend the public tool
	// projection against an accidental future source returning private detail.
	switch result.Outcome {
	case "logged_in", "approval_required", "handoff_required", "denied", "failed", "outcome_unknown",
		"found", "not_found", "choice_required", "unavailable", "submitted", "unverified", "observe_required":
	default:
		return denied()
	}
	if request.Fields.Step == "" {
		result.Alias = request.Account
	} else if !validBrowserLoginProjection(result) ||
		(result.Outcome == "logged_in" && request.Fields.Step != "verify") {
		return denied()
	}
	receiptID := ""
	if result.Outcome == "logged_in" {
		receiptID = "browser_login_" + uuid.NewString()
	}
	output := tool.runtime.result(struct {
		browser.CredentialLoginResult
		ReceiptID string `json:"receipt_id,omitempty"`
	}{CredentialLoginResult: result, ReceiptID: receiptID})
	if result.Outcome == "approval_required" && result.ApprovalWait != nil {
		if _, ok := tool.runtime.source.(BrowserCredentialApprovalSource); !ok ||
			!result.ApprovalWait.Deadline.After(time.Now()) {
			return denied()
		}
		waiting := &interactions.SuspensionRequest{
			Kind:          interactions.KindExternalWait,
			ConditionRef:  result.ApprovalWait.Reference,
			Timeout:       24 * time.Hour,
			Deadline:      result.ApprovalWait.Deadline,
			PromptSummary: "Waiting for phone approval. You can switch tasks; this request continues automatically after approval. The idle browser has been closed.",
		}
		if interactions.ValidateSuspensionRequest(*waiting) != nil {
			return denied()
		}
		output.Control.Suspension = waiting
	}

	if result.Outcome == "logged_in" {
		// Only the private consumer's confirmed outcome certifies the action.
		// The public opaque ID and trusted audit must name the same evidence;
		// reporting it conveys no credential authority or private values.
		output.WithWriteAudit(toolshared.WriteAuditEntry{
			Kind: taskresult.ObjectiveKindExternalAction, Target: "account:" + result.Alias,
			Action: "login", Tool: tool.Name(), Summary: "Account login confirmed.",
			Metadata: map[string]string{
				"invocation_id": receiptID, "outcome": result.Outcome,
			},
		})
	}
	return output
}

func (tool *BrowserLoginTool) ResolveExternalWait(ctx context.Context, reference string) toolshared.ExternalWaitState {
	if !tool.ToolEnabledForAgent(toolshared.ToolAgentID(ctx)) {
		return toolshared.ExternalWaitUnavailable
	}
	source, ok := tool.runtime.source.(BrowserCredentialApprovalSource)
	if !ok {
		return toolshared.ExternalWaitUnavailable
	}
	owner, err := browserOwnerFromContext(ctx)
	if err != nil {
		return toolshared.ExternalWaitUnavailable
	}
	return source.CredentialApprovalState(ctx, owner, reference)
}
