package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestCaptureNonGitIsAvailableAsBoundedState(t *testing.T) {
	root := t.TempDir()
	snapshot := captureSnapshot(t.Context(), root, root, Limits{})
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
	first := captureSnapshot(t.Context(), root, root, limits)
	second := captureSnapshot(t.Context(), root, root, limits)
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
		snapshot := captureSnapshot(t.Context(), root, root, Limits{})
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
		snapshot := captureSnapshot(t.Context(), root, root, Limits{})
		if !snapshot.Git.Detached || snapshot.Git.Unborn || snapshot.Git.Head == "" {
			t.Fatalf("detached snapshot = %#v", snapshot.Git)
		}
	})

	t.Run("linked worktree", func(t *testing.T) {
		root := initGitRepository(t)
		linked := filepath.Join(t.TempDir(), "linked")
		runGitTest(t, root, "worktree", "add", "--detach", linked, "HEAD")
		snapshot := captureSnapshot(t.Context(), linked, linked, Limits{})
		if !snapshot.Git.Available || !snapshot.Git.Worktree || snapshot.Git.GitDir == snapshot.Git.CommonDir {
			t.Fatalf("linked worktree snapshot = %#v", snapshot.Git)
		}
	})
}

func TestObserverPublishesOnlyChangedSnapshots(t *testing.T) {
	root := initGitRepository(t)
	observer := NewObserver(root, root, Limits{PromptBytes: 80})
	observer.Refresh(t.Context())
	if prompt := observer.RenderCurrent(t.Context()); len(prompt) > 80 || !utf8.ValidString(prompt) ||
		!strings.Contains(prompt, "prompt truncated") {
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

func TestObserverRefreshPendingRearmsUnchangedObservation(t *testing.T) {
	root := initGitRepository(t)
	observer := NewObserver(root, root, Limits{})
	observer.Refresh(t.Context())
	first, changed := observer.PendingUpdate(t.Context())
	if !changed {
		t.Fatal("initial workspace observation was not pending")
	}
	refreshed := observer.RefreshPending(t.Context())
	if refreshed.Identity() != first.Identity() {
		t.Fatalf("unchanged refresh identity = %q, want %q", refreshed.Identity(), first.Identity())
	}
	pending, changed := observer.PendingUpdate(t.Context())
	if !changed || pending.Identity() != refreshed.Identity() {
		t.Fatalf("rearmed observation = changed:%v snapshot:%+v", changed, pending)
	}
	if _, changed = observer.PendingUpdate(t.Context()); changed {
		t.Fatal("rearmed observation was emitted twice")
	}
}

func TestCaptureDisablesConfiguredGitCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sentinel shell script is Unix-specific")
	}
	root := initGitRepository(t)
	sentinel := filepath.Join(t.TempDir(), "git-command-ran")
	script := filepath.Join(t.TempDir(), "git-command")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch \"$1\"\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, root, "config", "core.fsmonitor", script+" "+sentinel)
	runGitTest(t, root, "config", "diff.external", script+" "+sentinel)
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	snapshot := captureSnapshot(t.Context(), root, root, Limits{})
	if !snapshot.Git.StatusAvailable || !snapshot.DiffStatAvailable || !snapshot.Git.Dirty {
		t.Fatalf("snapshot with disabled Git extensions = %#v", snapshot)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("repository-configured Git command executed: %v", err)
	}
}

func TestCaptureDisablesConfiguredContentFilters(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sentinel shell script is Unix-specific")
	}
	for _, filterKind := range []string{"clean", "process"} {
		t.Run(filterKind, func(t *testing.T) {
			root := initGitRepository(t)
			if err := os.WriteFile(
				filepath.Join(root, ".gitattributes"),
				[]byte("filtered.txt filter=evil\n"),
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			filtered := filepath.Join(root, "filtered.txt")
			if err := os.WriteFile(filtered, []byte("baseline\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runGitTest(t, root, "add", ".gitattributes", "filtered.txt")
			runGitTest(t, root, "commit", "-m", "add filtered file")

			sentinel := filepath.Join(t.TempDir(), "content-filter-ran")
			script := filepath.Join(t.TempDir(), "content-filter")
			if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch \"$1\"\nexit 1\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			runGitTest(t, root, "config", "filter.evil."+filterKind, script+" "+sentinel)
			runGitTest(t, root, "config", "filter.evil.required", "true")
			if err := os.WriteFile(filtered, []byte("changed\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			snapshot := captureSnapshot(t.Context(), root, root, Limits{})
			if !snapshot.Git.StatusAvailable || !snapshot.DiffStatAvailable || !snapshot.Git.Dirty {
				t.Fatalf("snapshot with disabled %s filter = %#v", filterKind, snapshot)
			}
			if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
				t.Fatalf("repository-configured %s filter executed: %v", filterKind, err)
			}
		})
	}
}

func TestCaptureDoesNotInspectDirtySubmoduleContentFilters(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sentinel shell script is Unix-specific")
	}
	for _, filterKind := range []string{"clean", "process"} {
		t.Run(filterKind, func(t *testing.T) {
			source := initGitRepository(t)
			if err := os.WriteFile(
				filepath.Join(source, ".gitattributes"),
				[]byte("filtered.txt filter=evil\n"),
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "filtered.txt"), []byte("baseline\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runGitTest(t, source, "add", ".gitattributes", "filtered.txt")
			runGitTest(t, source, "commit", "-m", "add filtered file")

			root := initGitRepository(t)
			runGitTest(t, root, "-c", "protocol.file.allow=always", "submodule", "add", source, "nested")
			runGitTest(t, root, "commit", "-am", "add submodule")
			nested := filepath.Join(root, "nested")
			sentinel := filepath.Join(t.TempDir(), "submodule-content-filter-ran")
			script := filepath.Join(t.TempDir(), "submodule-content-filter")
			if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch \"$1\"\nexit 1\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			runGitTest(t, nested, "config", "filter.evil."+filterKind, script+" "+sentinel)
			runGitTest(t, nested, "config", "filter.evil.required", "true")
			if err := os.WriteFile(filepath.Join(nested, "filtered.txt"), []byte("changed\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			snapshot := captureSnapshot(t.Context(), root, root, Limits{})
			if !snapshot.Git.StatusAvailable || !snapshot.DiffStatAvailable || snapshot.Git.Dirty ||
				!snapshot.SubmoduleWorktreeStateIgnored {
				t.Fatalf("snapshot with ignored submodule %s filter = %#v", filterKind, snapshot)
			}
			if prompt := RenderPrompt(snapshot, 4096); !strings.Contains(
				prompt,
				"Submodule worktree state: not inspected (passive capture)",
			) {
				t.Fatalf("submodule omission prompt = %q", prompt)
			}
			if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
				t.Fatalf("submodule-configured %s filter executed: %v", filterKind, err)
			}
		})
	}
}

func TestCaptureIgnoresAmbientGitRepositoryOverrides(t *testing.T) {
	root := initGitRepository(t)
	decoy := initGitRepository(t)
	if err := os.WriteFile(filepath.Join(root, "root-only.txt"), []byte("root\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(decoy, "decoy-only.txt"), []byte("decoy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
	t.Setenv("GIT_WORK_TREE", decoy)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(decoy, ".git", "index"))
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.fsmonitor")
	t.Setenv("GIT_CONFIG_VALUE_0", "false")

	snapshot := captureSnapshot(t.Context(), root, root, Limits{})
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Git.TopLevel != canonicalRoot || len(snapshot.ChangedPaths) != 1 ||
		snapshot.ChangedPaths[0].Path != "root-only.txt" {
		t.Fatalf("ambient Git variables redirected snapshot = %#v", snapshot)
	}
}

func TestRenderPromptTruncatesOnlyAtRecordBoundaryWithMarker(t *testing.T) {
	snapshot := Snapshot{
		ProjectRoot:       "/repo",
		CWD:               "/repo",
		Git:               GitState{Available: true, StatusAvailable: true, Branch: "main", Dirty: true},
		DiffStatAvailable: true,
		ChangedPaths: []ChangedPath{
			{Path: strings.Repeat("a", 200), Status: "??"},
			{Path: "must-not-appear.txt", Status: "??"},
		},
	}
	rendered := RenderPrompt(snapshot, 320)
	if len(rendered) > 320 || !utf8.ValidString(rendered) ||
		!strings.Contains(rendered, "Snapshot status: prompt truncated to byte limit") ||
		strings.Contains(rendered, "must-not-appear") || strings.Contains(rendered, strings.Repeat("a", 20)) {
		t.Fatalf("record-bounded prompt = %q (%d bytes)", rendered, len(rendered))
	}
}

func TestCaptureTimeoutIsBounded(t *testing.T) {
	root := initGitRepository(t)
	snapshot := captureSnapshot(context.Background(), root, root, Limits{Timeout: time.Nanosecond})
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
