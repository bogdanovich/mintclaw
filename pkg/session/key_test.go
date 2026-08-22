package session

import "testing"

type testScopeReader struct {
	scope *SessionScope
}

func (r testScopeReader) GetSessionScope(sessionKey string) *SessionScope {
	return CloneScope(r.scope)
}

func TestIsExplicitSessionKey(t *testing.T) {
	currentKey := BuildOpaqueSessionKey("current session")
	tests := []struct {
		key  string
		want bool
	}{
		{currentKey, true},
		{"sk_v1_abc", false},
		{"agent:main:direct:user123", false},
		{"custom-key", false},
		{"", false},
	}

	for _, tt := range tests {
		if got := IsExplicitSessionKey(tt.key); got != tt.want {
			t.Fatalf("IsExplicitSessionKey(%q) = %v, want %v", tt.key, got, tt.want)
		}
	}
}

func TestBuildMainSessionKey(t *testing.T) {
	got := BuildMainSessionKey("Main")
	const want = "sk_v1_6d9217fe77c7f11d9cc992aabe81a2d09604e9c48babbda8fdad3791f9c19f3b"
	if got != want {
		t.Fatalf("BuildMainSessionKey() = %q, want %q", got, want)
	}
}

func TestResolveAgentID_PrefersSessionScope(t *testing.T) {
	store := testScopeReader{
		scope: &SessionScope{
			Version: ScopeVersionV1,
			AgentID: "Support",
			Channel: "slack",
		},
	}

	if got := ResolveAgentID(store, "sk_v1_anything"); got != "support" {
		t.Fatalf("ResolveAgentID() = %q, want support", got)
	}
}

func TestResolveAgentIDRequiresSessionScope(t *testing.T) {
	if got := ResolveAgentID(nil, "agent:Sales:telegram:direct:user123"); got != "" {
		t.Fatalf("ResolveAgentID() = %q, want empty", got)
	}
}
