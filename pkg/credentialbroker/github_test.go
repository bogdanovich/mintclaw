package credentialbroker

import "testing"

func TestGitHubScopeIsImmutableAndRevisionBound(t *testing.T) {
	principal := Principal{OwnerID: "owner", ActorID: "actor", AgentID: "main", HostID: "host"}
	rules := []AccountRule{{
		Alias: "github", VaultID: "vault", ItemID: "item", Origins: []string{GitHubAPIOrigin},
		Principals: []Principal{principal}, GitHub: &GitHubReadScope{Repositories: []string{"Owner/Repo"}},
	}}
	policy, err := NewPolicy(rules)
	if err != nil {
		t.Fatal("GitHub policy unavailable")
	}
	request := Request{
		Principal:      principal,
		Account:        "github",
		Origin:         GitHubAPIOrigin,
		PolicyRevision: policy.Revision(),
	}
	rules[0].GitHub.Repositories[0] = "owner/replacement"
	if repository, scopeErr := policy.GitHubRepository(
		request,
		"OWNER/REPO",
	); scopeErr != nil ||
		repository != "owner/repo" {
		t.Fatal("configured GitHub scope changed after construction")
	}
	if _, scopeErr := policy.GitHubRepository(request, "owner/replacement"); scopeErr == nil {
		t.Fatal("mutable repository escaped scope")
	}
	replacement, err := NewPolicy(rules)
	if err != nil || replacement.Revision() == policy.Revision() {
		t.Fatal("repository change did not invalidate approval revision")
	}
	for _, repository := range []string{"other/repo", "owner/../repo", "owner/repo?token=x", "owner/repo/pulls", "owner/.."} {
		if _, scopeErr := policy.GitHubRepository(request, repository); scopeErr == nil {
			t.Fatal("unconfigured API resource admitted")
		}
	}
	request.Principal.ActorID = "other"
	if _, err = policy.GitHubRepository(request, "owner/repo"); err == nil {
		t.Fatal("GitHub request crossed actor scope")
	}
}

func TestGitHubScopeRejectsBroadOrAmbiguousConfiguration(t *testing.T) {
	for _, repositories := range [][]string{nil, {"owner/repo", "OWNER/REPO"}, {"owner/repo?x=1"}, {"owner/.."}} {
		_, err := NewPolicy([]AccountRule{{
			Alias: "github", VaultID: "vault", ItemID: "item", Origins: []string{GitHubAPIOrigin},
			Principals: []Principal{{OwnerID: "owner", ActorID: "actor", AgentID: "main", HostID: "host"}},
			GitHub:     &GitHubReadScope{Repositories: repositories},
		}})
		if err == nil {
			t.Fatal("invalid GitHub scope was accepted")
		}
	}
}
