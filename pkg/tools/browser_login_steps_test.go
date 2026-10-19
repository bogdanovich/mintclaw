package tools

import (
	"encoding/json"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/browser"
)

func websiteStepArguments(step string) map[string]any {
	args := map[string]any{
		"operation": step, "browser_session_id": "session_fixture", "tab_id": "tab_fixture",
		"snapshot_id": "snapshot_fixture", "snapshot_generation": 2,
	}
	for _, field := range map[string][]string{
		"discover": {}, "credentials": {"username_ref", "password_ref", "submit_ref"},
		"username": {"username_ref", "submit_ref"}, "password": {"password_ref", "submit_ref"},
		"totp": {"otp_ref", "submit_ref"}, "verify": {"authenticated_ref"},
	}[step] {
		args[field] = "ref_" + field
	}
	return args
}

func TestBrowserLoginWebsiteStepsUseOnlyCurrentReferencesAndSafeMetadata(t *testing.T) {
	for _, step := range []string{"discover", "credentials", "username", "password", "totp", "verify"} {
		t.Run(step, func(t *testing.T) {
			outcome := "submitted"
			if step == "discover" {
				outcome = "found"
			}
			if step == "verify" {
				outcome = "logged_in"
			}
			source := &credentialBrowserToolFixture{
				credentialAvailable: true, result: browser.CredentialLoginResult{
					Alias: "vault-logins", CredentialID: "item", Outcome: outcome,
				},
			}
			if step == "discover" {
				source.result.Candidates = []browser.CredentialCandidate{{
					ID: "item", Name: "Example personal", Domains: []string{"example.com"},
				}}
			}
			tool := NewBrowserLoginTool(browserToolTestConfig(), source)
			args := websiteStepArguments(step)
			if step != "discover" {
				args["credential_id"] = "item"
			}
			result := tool.Execute(browserToolTestContext(), args)
			var output struct {
				browser.CredentialLoginResult
				ReceiptID string `json:"receipt_id"`
			}
			if result == nil || json.Unmarshal([]byte(result.ForLLM), &output) != nil ||
				output.Outcome != outcome || source.calls != 1 || source.request.Fields.Step != step ||
				source.request.Account != "" || source.request.Principal.OwnerID != "" {
				t.Fatal("website step lost trusted admission or its safe public outcome")
			}
			if (len(result.WriteAudit) == 1) != (step == "verify") {
				t.Fatal("a submission or metadata lookup fabricated an authentication receipt")
			}
			if step == "verify" {
				if output.ReceiptID == "" || output.ReceiptID != result.WriteAudit[0].Metadata["invocation_id"] {
					t.Fatal("verified website login hid its runtime receipt from the model")
				}
			} else if output.ReceiptID != "" {
				t.Fatal("unconfirmed website step exposed an authentication receipt")
			}
		})
	}
}

func TestBrowserLoginWebsiteArgumentsRejectValuesAndWrongStepReferences(t *testing.T) {
	for _, extra := range []string{"password", "username", "otp", "token", "principal", "grant_id", "success_url", "password_ref"} {
		args := websiteStepArguments("totp")
		args[extra] = "unaccepted-input"
		if _, err := projectBrowserLoginStep(args); err == nil {
			t.Fatal("website login accepted values, authority or a wrong-stage reference")
		}
	}
	args := websiteStepArguments("credentials")
	delete(args, "operation")
	projected, err := projectBrowserLoginStep(args)
	if err != nil || projected["operation"] != "credentials" {
		t.Fatal("website login without a fixed alias did not default to a generic form")
	}
	args = websiteStepArguments("totp")
	delete(args, "otp_ref")
	if _, err = projectBrowserLoginStep(args); err == nil {
		t.Fatal("one-time-code operation omitted its live destination")
	}
}

func TestWebsiteSubmissionCannotCertifyAuthentication(t *testing.T) {
	source := &credentialBrowserToolFixture{
		credentialAvailable: true,
		result:              browser.CredentialLoginResult{Alias: "vault-logins", Outcome: "logged_in"},
	}
	result := NewBrowserLoginTool(browserToolTestConfig(), source).
		Execute(browserToolTestContext(), websiteStepArguments("credentials"))
	if result == nil || result.ForLLM != `{"outcome":"denied"}` || len(result.WriteAudit) != 0 {
		t.Fatal("generic submission fabricated a confirmed-login receipt")
	}
}
