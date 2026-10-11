package credentialbroker

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func testPrincipal() Principal {
	return Principal{OwnerID: "owner-1", ActorID: "actor-1", AgentID: "agent-1", HostID: "host-1"}
}

func testRules() []AccountRule {
	return []AccountRule{
		{
			Alias: "ebay", VaultID: "vault-1", ItemID: "item-1",
			Origins:    []string{"https://www.ebay.com", "https://signin.ebay.com"},
			Principals: []Principal{testPrincipal()},
		},
		{
			Alias: "craigslist", VaultID: "vault-1", ItemID: "item-2",
			Origins:    []string{"https://accounts.craigslist.org"},
			Principals: []Principal{testPrincipal()},
		},
	}
}

func mustPolicy(t *testing.T, rules []AccountRule) *Policy {
	t.Helper()
	policy, err := NewPolicy(rules)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func testRequest(policy *Policy) Request {
	return Request{
		Principal: testPrincipal(), Account: "ebay",
		Origin: "https://signin.ebay.com", PolicyRevision: policy.Revision(),
	}
}

func TestPolicyExactHTTPSOrigins(t *testing.T) {
	policy := mustPolicy(t, testRules())
	tests := []struct {
		origin string
		want   bool
	}{
		{origin: "https://signin.ebay.com", want: true},
		{origin: "https://SIGNIN.EBAY.com:443/", want: true},
		{origin: "https://www.ebay.com", want: true},
		{origin: "http://signin.ebay.com"},
		{origin: "https://signin.ebay.com:444"},
		{origin: "https://signin.ebay.com.attacker.example"},
		{origin: "https://other.ebay.com"},
		{origin: "https://ebay.com"},
		{origin: "https://signin.ebay.com/login"},
		{origin: "https://signin.ebay.com?password=test"},
		{origin: "https://signin.ebay.com#fragment"},
		{origin: "https://signin.ebay.com@attacker.example"},
		{origin: "https://user:password@signin.ebay.com"},
		{origin: "https://*.ebay.com"},
		{origin: "https://localhost"},
		{origin: "https://127.0.0.1"},
		{origin: "https://0x7f000001"},
		{origin: "https://169.254.169.254"},
		{origin: "https://[::1]"},
		{origin: " https://signin.ebay.com"},
		{origin: "javascript:alert(1)"},
		{origin: "about:blank"},
		{origin: ""},
	}
	for _, test := range tests {
		t.Run(test.origin, func(t *testing.T) {
			request := testRequest(policy)
			request.Origin = test.origin
			if got := policy.permits(request); got != test.want {
				t.Fatalf("origin permitted = %v, want %v", got, test.want)
			}
		})
	}
}

func TestPolicyIsImmutableAndRevisionsAreCanonical(t *testing.T) {
	rules := testRules()
	policy := mustPolicy(t, rules)
	revision := policy.Revision()
	rules[0].Alias = "other"
	rules[0].VaultID = "other-vault"
	rules[0].Origins[0] = "https://attacker.example"
	rules[0].Principals[0].HostID = "other-host"
	if policy.Revision() != revision || !policy.permits(testRequest(policy)) {
		t.Fatal("caller mutation changed constructed policy")
	}

	equivalent := testRules()
	slices.Reverse(equivalent)
	slices.Reverse(equivalent[1].Origins)
	equivalent[1].Origins[0] = "https://SIGNIN.EBAY.COM:443/"
	if other := mustPolicy(t, equivalent); other.Revision() != revision {
		t.Fatal("equivalent normalized policy changed revision")
	}
	if len(revision) != 64 {
		t.Fatal("revision is not a SHA-256 digest")
	}
}

func TestRevisionCoversEveryAuthorityField(t *testing.T) {
	revision := mustPolicy(t, testRules()).Revision()
	tests := []struct {
		name   string
		mutate func([]AccountRule)
	}{
		{name: "alias", mutate: func(rules []AccountRule) { rules[0].Alias = "other" }},
		{name: "vault", mutate: func(rules []AccountRule) { rules[0].VaultID = "vault-2" }},
		{name: "item", mutate: func(rules []AccountRule) { rules[0].ItemID = "item-3" }},
		{name: "origin", mutate: func(rules []AccountRule) { rules[0].Origins[0] = "https://other.example" }},
		{name: "owner", mutate: func(rules []AccountRule) { rules[0].Principals[0].OwnerID = "owner-2" }},
		{name: "actor", mutate: func(rules []AccountRule) { rules[0].Principals[0].ActorID = "actor-2" }},
		{name: "agent", mutate: func(rules []AccountRule) { rules[0].Principals[0].AgentID = "agent-2" }},
		{name: "host", mutate: func(rules []AccountRule) { rules[0].Principals[0].HostID = "host-2" }},
		{name: "additional origin", mutate: func(rules []AccountRule) {
			rules[0].Origins = append(rules[0].Origins, "https://other.example")
		}},
		{name: "additional principal", mutate: func(rules []AccountRule) {
			other := testPrincipal()
			other.AgentID = "agent-2"
			rules[0].Principals = append(rules[0].Principals, other)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rules := testRules()
			test.mutate(rules)
			if mustPolicy(t, rules).Revision() == revision {
				t.Fatal("authority change retained revision")
			}
		})
	}
	if mustPolicy(t, testRules()[:1]).Revision() == revision {
		t.Fatal("account removal retained revision")
	}
}

func TestPolicyRejectsInvalidAndOversizedConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]AccountRule) []AccountRule
	}{
		{name: "alias", mutate: func(rules []AccountRule) []AccountRule { rules[0].Alias = "../item"; return rules }},
		{
			name:   "vault missing",
			mutate: func(rules []AccountRule) []AccountRule { rules[0].VaultID = ""; return rules },
		},
		{name: "item too long", mutate: func(rules []AccountRule) []AccountRule {
			rules[0].ItemID = strings.Repeat("a", 129)
			return rules
		}},
		{name: "duplicate alias", mutate: func(rules []AccountRule) []AccountRule { return append(rules, rules[0]) }},
		{
			name:   "empty origins",
			mutate: func(rules []AccountRule) []AccountRule { rules[0].Origins = nil; return rules },
		},
		{name: "duplicate canonical origin", mutate: func(rules []AccountRule) []AccountRule {
			rules[0].Origins = []string{"https://example.com", "https://EXAMPLE.COM:443/"}
			return rules
		}},
		{name: "HTTP origin", mutate: func(rules []AccountRule) []AccountRule {
			rules[0].Origins = []string{"http://example.com"}
			return rules
		}},
		{
			name:   "empty principals",
			mutate: func(rules []AccountRule) []AccountRule { rules[0].Principals = nil; return rules },
		},
		{name: "invalid principal", mutate: func(rules []AccountRule) []AccountRule {
			rules[0].Principals[0].ActorID = ""
			return rules
		}},
		{name: "duplicate principal", mutate: func(rules []AccountRule) []AccountRule {
			rules[0].Principals = append(rules[0].Principals, rules[0].Principals[0])
			return rules
		}},
		{name: "too many accounts", mutate: func(rules []AccountRule) []AccountRule {
			return make([]AccountRule, MaxAccounts+1)
		}},
		{name: "too many origins", mutate: func(rules []AccountRule) []AccountRule {
			rules[0].Origins = make([]string, MaxOriginsPerAccount+1)
			return rules
		}},
		{name: "too many principals", mutate: func(rules []AccountRule) []AccountRule {
			rules[0].Principals = make([]Principal, MaxPrincipalsPerAccount+1)
			return rules
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy, err := NewPolicy(test.mutate(testRules()))
			if policy != nil || !errors.Is(err, ErrInvalidPolicy) {
				t.Fatalf("invalid policy result = %v, %v", policy, err)
			}
		})
	}

	rules := make([]AccountRule, MaxAccounts)
	for index := range rules {
		rules[index] = testRules()[0]
		rules[index].Alias = fmt.Sprintf("account-%d", index)
		rules[index].Origins = make([]string, MaxOriginsPerAccount)
		for originIndex := range rules[index].Origins {
			rules[index].Origins[originIndex] = fmt.Sprintf("https://host-%d.example", originIndex)
		}
		rules[index].Principals = make([]Principal, MaxPrincipalsPerAccount)
		for principalIndex := range rules[index].Principals {
			rules[index].Principals[principalIndex] = testPrincipal()
			rules[index].Principals[principalIndex].AgentID = fmt.Sprintf("agent-%d", principalIndex)
		}
	}
	mustPolicy(t, rules)
}

func TestUnconfiguredAndIncompleteAuthorityDenies(t *testing.T) {
	policy := mustPolicy(t, testRules())
	request := testRequest(policy)
	if (*Policy)(nil).permits(request) || (&Policy{}).permits(request) || mustPolicy(t, nil).permits(request) {
		t.Fatal("unconfigured policy permits access")
	}
	tests := []func(*Request){
		func(request *Request) { request.Principal.OwnerID = "" },
		func(request *Request) { request.Principal.ActorID = "other" },
		func(request *Request) { request.Principal.AgentID = "other" },
		func(request *Request) { request.Principal.HostID = "other" },
		func(request *Request) { request.Account = "missing" },
		func(request *Request) { request.PolicyRevision = "" },
		func(request *Request) { request.PolicyRevision = "obsolete" },
	}
	for _, mutate := range tests {
		changed := request
		mutate(&changed)
		if policy.permits(changed) {
			t.Fatal("incomplete or changed authority permits access")
		}
	}
}
