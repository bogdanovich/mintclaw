package credentialbroker

import (
	"errors"
	"strings"
	"testing"
)

func testLoginFlow() LoginFlow {
	return LoginFlow{
		NavigationURL: "https://signin.ebay.com/login", EntryOrigin: "https://signin.ebay.com",
		FormID: "login", UsernameID: "username", PasswordID: "password", UsernameType: "email",
		SubmitURL: "https://signin.ebay.com/session", SuccessURL: "https://www.ebay.com/home",
		SuccessMarkerID: "authenticated", HandoffURL: "https://signin.ebay.com/mfa", HandoffMarkerID: "mfa-required",
	}
}

func flowRules() []AccountRule {
	rules := testRules()
	flow := testLoginFlow()
	rules[0].Login = &flow
	return rules
}

func TestLoginFlowIsImmutableAndDefaultDenied(t *testing.T) {
	rules := flowRules()
	policy := mustPolicy(t, rules)
	request := testRequest(policy)
	flow, err := policy.LoginFlow(request)
	if err != nil || flow != testLoginFlow() {
		t.Fatal("configured flow unavailable")
	}
	rules[0].Login.PasswordID = "changed"
	flow.PasswordID = "changed-again"
	if copied, copyErr := policy.LoginFlow(request); copyErr != nil || copied != testLoginFlow() {
		t.Fatal("caller mutation changed policy flow")
	}
	legacy := mustPolicy(t, testRules())
	if _, err = legacy.LoginFlow(testRequest(legacy)); !errors.Is(err, ErrDenied) {
		t.Fatal("legacy origin policy authorized browser login without a flow")
	}
	for _, change := range []func(*Request){
		func(request *Request) { request.Origin = "https://www.ebay.com" },
		func(request *Request) { request.Principal.ActorID = "other" },
		func(request *Request) { request.PolicyRevision = legacy.Revision() },
		func(request *Request) { request.Account = "missing" },
	} {
		changed := request
		change(&changed)
		if _, err = policy.LoginFlow(changed); !errors.Is(err, ErrDenied) {
			t.Fatal("flow lookup admitted mismatched current authority")
		}
	}
	if _, err = (*Policy)(nil).LoginFlow(request); !errors.Is(err, ErrDenied) {
		t.Fatal("nil policy admitted flow lookup")
	}
}

func TestLoginFlowCanonicalRevisionAndGrantInvalidation(t *testing.T) {
	rules := flowRules()
	policy := mustPolicy(t, rules)
	equivalent := flowRules()
	equivalent[0].Login.EntryOrigin = "https://SIGNIN.EBAY.COM:443/"
	equivalent[0].Login.NavigationURL = "https://SIGNIN.EBAY.COM:443/login"
	if mustPolicy(t, equivalent).Revision() != policy.Revision() {
		t.Fatal("equivalent normalized flow changed revision")
	}
	changes := []struct {
		name   string
		change func(*LoginFlow)
	}{
		{"navigation", func(flow *LoginFlow) { flow.NavigationURL += "-other" }},
		{"form", func(flow *LoginFlow) { flow.FormID += "-other" }},
		{"username", func(flow *LoginFlow) { flow.UsernameID += "-other" }},
		{"password", func(flow *LoginFlow) { flow.PasswordID += "-other" }},
		{"username type", func(flow *LoginFlow) { flow.UsernameType = "text" }},
		{"submission", func(flow *LoginFlow) { flow.SubmitURL += "-other" }},
		{"success", func(flow *LoginFlow) { flow.SuccessURL += "-other" }},
		{"success marker", func(flow *LoginFlow) { flow.SuccessMarkerID += "-other" }},
		{"handoff", func(flow *LoginFlow) { flow.HandoffURL += "-other" }},
		{"handoff marker", func(flow *LoginFlow) { flow.HandoffMarkerID += "-other" }},
		{"handoff removal", func(flow *LoginFlow) { flow.HandoffURL = ""; flow.HandoffMarkerID = "" }},
		{"entry", func(flow *LoginFlow) {
			flow.EntryOrigin = "https://www.ebay.com"
			flow.NavigationURL = "https://www.ebay.com/login"
			flow.SubmitURL = "https://www.ebay.com/session"
		}},
	}
	for _, change := range changes {
		t.Run(change.name, func(t *testing.T) {
			changed := flowRules()
			change.change(changed[0].Login)
			replacement := mustPolicy(t, changed)
			if replacement.Revision() == policy.Revision() {
				t.Fatal("flow authority change retained approval revision")
			}
			fixture := newGrantFixture(t)
			authority := fixture.authority
			if authority.ReplacePolicy(policy) != nil {
				t.Fatal("initial flow policy unavailable")
			}
			grant := mustApprove(t, authority, Approval{
				Principal: testPrincipal(), Accounts: []string{"ebay"},
				PolicyRevision: policy.Revision(), Lifetime: MaxGrantLifetime,
			})
			if authority.ReplacePolicy(replacement) != nil || authority.ReplacePolicy(policy) != nil {
				t.Fatal("flow replacement failed")
			}
			if authority.Authorize(grant.ID, testRequest(policy)) == nil {
				t.Fatal("flow A-B-A resurrected approval")
			}
		})
	}
	removed := flowRules()
	removed[0].Login = nil
	if mustPolicy(t, removed).Revision() == policy.Revision() {
		t.Fatal("flow removal retained revision")
	}
}

func TestLoginFlowRejectsIncompleteAmbiguousAndOutOfScopeConfiguration(t *testing.T) {
	changes := []struct {
		name   string
		change func(*LoginFlow)
	}{
		{"missing form", func(flow *LoginFlow) { flow.FormID = "" }},
		{"missing username", func(flow *LoginFlow) { flow.UsernameID = "" }},
		{"missing password", func(flow *LoginFlow) { flow.PasswordID = "" }},
		{"missing success marker", func(flow *LoginFlow) { flow.SuccessMarkerID = "" }},
		{"missing handoff marker", func(flow *LoginFlow) { flow.HandoffMarkerID = "" }},
		{"orphan handoff marker", func(flow *LoginFlow) { flow.HandoffURL = "" }},
		{"selector", func(flow *LoginFlow) { flow.PasswordID = "input[type=password]" }},
		{"large ID", func(flow *LoginFlow) { flow.FormID = strings.Repeat("a", 129) }},
		{"same inputs", func(flow *LoginFlow) { flow.UsernameID = flow.PasswordID }},
		{"same form and input", func(flow *LoginFlow) { flow.FormID = flow.UsernameID }},
		{"password username type", func(flow *LoginFlow) { flow.UsernameType = "password" }},
		{"outside entry", func(flow *LoginFlow) { flow.EntryOrigin = "https://attacker.example" }},
		{"trailing-dot entry", func(flow *LoginFlow) { flow.EntryOrigin += "." }},
		{"cross-origin navigation", func(flow *LoginFlow) { flow.NavigationURL = "https://www.ebay.com/login" }},
		{"cross-origin submit", func(flow *LoginFlow) { flow.SubmitURL = "https://www.ebay.com/session" }},
		{"outside success", func(flow *LoginFlow) { flow.SuccessURL = "https://attacker.example/home" }},
		{"outside handoff", func(flow *LoginFlow) { flow.HandoffURL = "https://attacker.example/mfa" }},
		{"success matches initial", func(flow *LoginFlow) { flow.SuccessURL = flow.NavigationURL }},
		{"handoff matches success", func(flow *LoginFlow) { flow.HandoffURL = flow.SuccessURL }},
	}
	for _, raw := range []string{
		"", "http://signin.ebay.com/login", "https://user:pass@signin.ebay.com/login",
		"https://signin.ebay.com/login?", "https://signin.ebay.com/login?next=other",
		"https://signin.ebay.com/login#", "https://signin.ebay.com/login#other",
		"https://signin.ebay.com/a/../login", "https://signin.ebay.com/a/./login",
		"https://signin.ebay.com/%6cogin", "https://signin.ebay.com/login%3fnext",
		"https://signin.ebay.com/login%2fother", "https://signin.ebay.com/login\\other",
		"https://signin.ebay.com/login\n", "https://signin.ebay.com./login",
		"https://signin.ebay.com/login<>\"", "https://127.0.0.1/login",
		"https://signin.ebay.com/" + strings.Repeat("a", 2048),
	} {
		changes = append(changes, struct {
			name   string
			change func(*LoginFlow)
		}{
			name: "URL " + raw, change: func(flow *LoginFlow) { flow.NavigationURL = raw },
		})
	}
	for _, change := range changes {
		t.Run(change.name, func(t *testing.T) {
			rules := flowRules()
			change.change(rules[0].Login)
			if policy, err := NewPolicy(rules); policy != nil || !errors.Is(err, ErrInvalidPolicy) {
				t.Fatal("unsafe or incomplete login flow accepted")
			}
		})
	}
}
