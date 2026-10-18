package credentialbroker

import (
	"testing"
)

func discoveryRules() []AccountRule {
	return []AccountRule{{
		Alias: "vault-logins", VaultID: "vault", DiscoverLogins: true,
		Principals: []Principal{testPrincipal()},
	}}
}

func TestDiscoveryPolicyNeedsExplicitVaultScopeAndPublicHTTPSOrigin(t *testing.T) {
	policy := mustPolicy(t, discoveryRules())
	request := Request{
		Principal: testPrincipal(), Account: "vault-logins", PolicyRevision: policy.Revision(),
	}
	for _, origin := range []string{"https://www.example.com", "https://accounts.other.org"} {
		request.Origin = origin
		if !policy.permits(request) {
			t.Fatal("explicit vault scope did not admit a public website")
		}
	}
	for _, origin := range []string{
		"http://www.example.com", "https://localhost", "https://127.0.0.1", "https://10.0.0.1",
		"https://www.example.com.", "https://www.example.com/login", "https://user:password@example.com", "",
	} {
		request.Origin = origin
		if policy.permits(request) {
			t.Fatal("vault scope admitted an invalid live origin")
		}
	}
	other := testPrincipal()
	other.ActorID = "other"
	if policy.AllowsPrincipal(other, request.Account) || policy.AllowsPrincipal(testPrincipal(), "unknown") ||
		!policy.AllowsPrincipal(testPrincipal(), request.Account) {
		t.Fatal("vault scope lost its exact configured principal")
	}
	request.Origin, request.Principal = "https://example.com", other
	if policy.permits(request) {
		t.Fatal("another actor used a discovery scope")
	}
}

func TestDiscoveryPolicyCannotMixItemOrOriginBindings(t *testing.T) {
	for _, mutate := range []func(*AccountRule){
		func(rule *AccountRule) { rule.ItemID = "item" },
		func(rule *AccountRule) { rule.Origins = []string{"https://example.com"} },
		func(rule *AccountRule) { rule.Login = &LoginFlow{} },
		func(rule *AccountRule) { rule.GitHub = &GitHubReadScope{} },
	} {
		rules := discoveryRules()
		mutate(&rules[0])
		if _, err := NewPolicy(rules); err == nil {
			t.Fatal("ambiguous discovery and fixed-item configuration was admitted")
		}
	}
	rules := discoveryRules()
	revision := mustPolicy(t, rules).Revision()
	rules[0].VaultID = "other-vault"
	if mustPolicy(t, rules).Revision() == revision {
		t.Fatal("changing the approved discovery vault preserved authority")
	}
}
