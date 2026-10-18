package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/browser"
)

type credentialBrowserToolFixture struct {
	fakeBrowserToolSource
	credentialAvailable bool
	request             browser.CredentialLoginRequest
	result              browser.CredentialLoginResult
	calls               int
}

func (source *credentialBrowserToolFixture) CredentialLoginAvailable() bool {
	return source.credentialAvailable
}

func (source *credentialBrowserToolFixture) LoginCredential(_ context.Context,
	request browser.CredentialLoginRequest,
) browser.CredentialLoginResult {
	source.calls++
	source.request = request
	return source.result
}

func credentialBrowserToolArguments() map[string]any {
	return map[string]any{
		"account_alias": "ebay", "browser_session_id": "session_fixture", "tab_id": "tab_fixture",
		"snapshot_id": "snapshot_fixture", "snapshot_generation": 2,
		"username_ref": "ref_username", "password_ref": "ref_password", "submit_ref": "ref_submit",
	}
}

func TestBrowserLoginToolUsesAuthenticatedOwnerAndOnlyAliasReferences(t *testing.T) {
	source := &credentialBrowserToolFixture{
		credentialAvailable: true, result: browser.CredentialLoginResult{Alias: "unexpected", Outcome: "logged_in"},
	}
	tool := NewBrowserLoginTool(browserToolTestConfig(), source)
	ctx := browserToolTestContext()
	result := tool.Execute(ctx, credentialBrowserToolArguments())
	var output browser.CredentialLoginResult
	if result == nil || json.Unmarshal([]byte(result.ForLLM), &output) != nil ||
		output.Alias != "ebay" || output.Outcome != "logged_in" || source.calls != 1 {
		t.Fatal("alias login did not return its fixed public result")
	}
	owner, err := browserOwnerFromContext(ctx)
	if err != nil || source.request.Owner != owner || source.request.Principal.OwnerID != "" ||
		source.request.PolicyRevision != "" || source.request.Fields.SuccessURL != "" ||
		source.request.Fields.PasswordRef != "ref_password" {
		t.Fatal("tool invented authority or lost its current field references")
	}
	encoded, err := json.Marshal(tool.Parameters())
	if err != nil || strings.Contains(string(encoded), "vault_id") || strings.Contains(string(encoded), "grant_id") {
		t.Fatal("model schema exposed private credential bindings")
	}
}

func TestBrowserLoginToolRejectsValuesAndUnqualifiedRuntime(t *testing.T) {
	for _, mode := range []string{"extra-password", "missing-ref", "invalid-generation", "disabled", "private-outcome"} {
		t.Run(mode, func(t *testing.T) {
			source := &credentialBrowserToolFixture{
				credentialAvailable: true, result: browser.CredentialLoginResult{Outcome: "logged_in"},
			}
			tool := NewBrowserLoginTool(browserToolTestConfig(), source)
			args := credentialBrowserToolArguments()
			switch mode {
			case "extra-password":
				args["password"] = "synthetic-private-tool-value"
			case "missing-ref":
				delete(args, "password_ref")
			case "invalid-generation":
				args["snapshot_generation"] = 0
			case "disabled":
				source.credentialAvailable = false
			case "private-outcome":
				source.result.Outcome = "synthetic-private-tool-value"
			}
			result := tool.Execute(browserToolTestContext(), args)
			if result == nil || result.ForLLM != `{"outcome":"denied"}` || result.ForUser != "" ||
				(mode != "private-outcome" && source.calls != 0) {
				t.Fatal("invalid credential tool invocation escaped its fixed denial")
			}
		})
	}
}
