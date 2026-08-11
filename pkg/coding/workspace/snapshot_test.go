package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestCaptureNonGitIsAvailableAsBoundedState(t *testing.T) {
	root := t.TempDir()
	snapshot := Capture(t.Context(), root, root, Limits{})
	if snapshot.Git.Available || snapshot.Git.UnavailableReason == "" {
		t.Fatalf("non-Git snapshot = %#v", snapshot)
	}
	rendered := RenderPrompt(snapshot, 4096)
	if !strings.Contains(rendered, "Git: unavailable") || !strings.Contains(rendered, "supersedes") {
		t.Fatalf("non-Git prompt = %q", rendered)
	}
}

func TestRenderPromptDoesNotCallUnavailableGitFieldsClean(t *testing.T) {
	rendered := RenderPrompt(Snapshot{
		ProjectRoot: "/repo",
		CWD:         "/repo",
		Git:         GitState{Available: true, Branch: "main"},
	}, 4096)
	if !strings.Contains(rendered, "Status: unavailable") ||
		!strings.Contains(rendered, "Diff stat: unavailable") || strings.Contains(rendered, "Status: clean") {
		t.Fatalf("unavailable Git fields prompt = %q", rendered)
	}
}

func TestCaptureDirtyRepositoryIsDeterministicAndBounded(t *testing.T) {
	root := initGitRepository(t)
	tracked := filepath.Join(root, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("changed\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"z-untracked.txt", "a-untracked.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("untracked secret body\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	limits := Limits{ChangedPaths: 2, PromptBytes: 4096}
	first := Capture(t.Context(), root, root, limits)
	second := Capture(t.Context(), root, root, limits)
	if first.Identity() != second.Identity() {
		t.Fatalf("stable repository produced different identities:\n%#v\n%#v", first, second)
	}
	if !first.Git.Available || !first.Git.Dirty || first.Git.Branch != "main" || first.Git.Head == "" {
		t.Fatalf("dirty Git state = %#v", first.Git)
	}
	if len(first.ChangedPaths) != 2 || first.ChangedPaths[0].Path > first.ChangedPaths[1].Path || !first.Truncated {
		t.Fatalf("bounded changed paths = %#v, truncated=%v", first.ChangedPaths, first.Truncated)
	}
	if first.DiffStat.Files != 1 || first.DiffStat.Additions == 0 || first.DiffStat.Deletions == 0 {
		t.Fatalf("diff stat = %#v", first.DiffStat)
	}
	rendered := RenderPrompt(first, limits.PromptBytes)
	if strings.Contains(rendered, "untracked secret body") || !strings.Contains(rendered, "Status: dirty") ||
		!strings.Contains(rendered, "Diff stat:") {
		t.Fatalf("dirty prompt = %q", rendered)
	}
	if len(rendered) > limits.PromptBytes {
		t.Fatalf("prompt bytes = %d, limit %d", len(rendered), limits.PromptBytes)
	}
}

func TestCaptureDetachedUnbornAndLinkedWorktree(t *testing.T) {
	t.Run("unborn", func(t *testing.T) {
		root := t.TempDir()
		runGitTest(t, root, "init", "-b", "main")
		if err := os.WriteFile(filepath.Join(root, "staged.txt"), []byte("staged\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runGitTest(t, root, "add", "staged.txt")
		if err := os.WriteFile(filepath.Join(root, "staged.txt"), []byte("staged\nunstaged\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		snapshot := Capture(t.Context(), root, root, Limits{})
		if !snapshot.Git.Available || !snapshot.Git.Unborn || snapshot.Git.Branch != "main" ||
			snapshot.Git.Head != "" {
			t.Fatalf("unborn snapshot = %#v", snapshot.Git)
		}
		if !snapshot.Git.Dirty || snapshot.DiffStat.Files != 1 || snapshot.DiffStat.Additions != 2 {
			t.Fatalf("unborn changes = paths %#v, stat %#v", snapshot.ChangedPaths, snapshot.DiffStat)
		}
	})

	t.Run("detached", func(t *testing.T) {
		root := initGitRepository(t)
		runGitTest(t, root, "checkout", "--detach", "HEAD")
		snapshot := Capture(t.Context(), root, root, Limits{})
		if !snapshot.Git.Detached || snapshot.Git.Unborn || snapshot.Git.Head == "" {
			t.Fatalf("detached snapshot = %#v", snapshot.Git)
		}
	})

	t.Run("linked worktree", func(t *testing.T) {
		root := initGitRepository(t)
		linked := filepath.Join(t.TempDir(), "linked")
		runGitTest(t, root, "worktree", "add", "--detach", linked, "HEAD")
		snapshot := Capture(t.Context(), linked, linked, Limits{})
		if !snapshot.Git.Available || !snapshot.Git.Worktree || snapshot.Git.GitDir == snapshot.Git.CommonDir {
			t.Fatalf("linked worktree snapshot = %#v", snapshot.Git)
		}
	})
}

func TestObserverPublishesOnlyChangedSnapshots(t *testing.T) {
	root := initGitRepository(t)
	observer := NewObserver(root, root, Limits{PromptBytes: 80})
	observer.Refresh(t.Context())
	if prompt := observer.RenderCurrent(t.Context()); len(prompt) != 80 || !utf8.ValidString(prompt) {
		t.Fatalf("bounded observer prompt = %q (%d bytes)", prompt, len(prompt))
	}
	first, changed := observer.PendingUpdate(t.Context())
	if !changed || !first.Git.Available {
		t.Fatalf("first update = %#v, %v", first, changed)
	}
	if _, changed = observer.PendingUpdate(t.Context()); changed {
		t.Fatal("unchanged snapshot was published twice")
	}
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	observer.Refresh(t.Context())
	second, changed := observer.PendingUpdate(t.Context())
	if !changed || !second.Git.Dirty || second.Identity() == first.Identity() {
		t.Fatalf("changed update = %#v, %v", second, changed)
	}
}

func TestCaptureTimeoutIsBounded(t *testing.T) {
	root := initGitRepository(t)
	snapshot := Capture(context.Background(), root, root, Limits{Timeout: time.Nanosecond})
	if snapshot.Git.Available && snapshot.Warning == "" && !snapshot.Truncated {
		t.Fatalf("timeout was not surfaced: %#v", snapshot)
	}
}

func initGitRepository(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	root := t.TempDir()
	runGitTest(t, root, "init", "-b", "main")
	runGitTest(t, root, "config", "user.email", "mintclaw-tests@example.invalid")
	runGitTest(t, root, "config", "user.name", "MintClaw Tests")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, root, "add", "tracked.txt")
	runGitTest(t, root, "commit", "-m", "initial")
	return root
}

func runGitTest(t *testing.T, root string, args ...string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}
