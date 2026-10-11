package project

import "testing"

func TestProjectHashIsStableAndDoesNotExposeProjectKey(t *testing.T) {
	const projectKey = "git_worktree:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	got := ProjectHash(projectKey)
	const want = "713ed4c911288904da4fa9d4a18f74168c58f6df04ad51d6df65bf39be468e53"
	if got != want {
		t.Fatalf("ProjectHash() = %q, want %q", got, want)
	}
	if got == projectKey {
		t.Fatal("ProjectHash() exposed the canonical project key")
	}
}
