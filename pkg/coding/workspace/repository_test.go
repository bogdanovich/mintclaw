package workspace

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRepositoryStatusUsesVersionedPassiveSnapshot(t *testing.T) {
	root := initGitRepository(t)
	repository := NewRepository(root, root, Limits{})
	status := repository.Status(t.Context())
	if status.SchemaVersion != RepositoryStatusSchemaV1 || !status.Snapshot.Git.Available ||
		!status.Snapshot.Git.StatusAvailable || status.Snapshot.Git.Dirty {
		t.Fatalf("Status() = %#v", status)
	}
}

func TestRepositoryDiffCurrentReturnsStructuredTrackedAndUntrackedChanges(t *testing.T) {
	root := initGitRepository(t)
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("initial\nchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("first\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := NewRepository(root, root, Limits{ChangedPaths: 1}).Diff(
		t.Context(),
		DiffTarget{Kind: DiffTargetCurrent},
	)
	if result.SchemaVersion != RepositoryDiffSchemaV1 || !result.RepositoryAvailable || result.Stale ||
		result.UnavailableReason != "" {
		t.Fatalf("Diff(current) metadata = %#v", result)
	}
	tracked := requireDiffFile(t, result, "tracked.txt")
	if tracked.Additions != 1 || len(tracked.Hunks) != 1 ||
		!hasDiffLine(tracked, "addition", 2, "changed") {
		t.Fatalf("tracked diff = %#v", tracked)
	}
	untracked := requireDiffFile(t, result, "untracked.txt")
	if untracked.Additions != 2 || len(untracked.Hunks) != 1 ||
		!hasDiffLine(untracked, "addition", 2, "second") {
		t.Fatalf("untracked diff = %#v", untracked)
	}
}

func TestRepositoryDiffSupportsLocalBaseAndCommitTargets(t *testing.T) {
	root := initGitRepository(t)
	base := strings.TrimSpace(runGitTestOutput(t, root, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("initial\ncommitted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, root, "commit", "-am", "second")
	commit := strings.TrimSpace(runGitTestOutput(t, root, "rev-parse", "HEAD"))
	if err := os.WriteFile(
		filepath.Join(root, "tracked.txt"),
		[]byte("initial\ncommitted\nworking\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	repository := NewRepository(root, root, Limits{})
	baseResult := repository.Diff(t.Context(), DiffTarget{Kind: DiffTargetBase, Ref: base})
	if baseResult.ResolvedRevision != base || baseResult.MergeBase != base || baseResult.UnavailableReason != "" ||
		!hasDiffLine(requireDiffFile(t, baseResult, "tracked.txt"), "addition", 3, "working") {
		t.Fatalf("Diff(base) = %#v", baseResult)
	}
	if requireDiffFile(t, baseResult, "untracked.txt").Additions != 1 {
		t.Fatalf("Diff(base) omitted untracked state = %#v", baseResult)
	}
	commitResult := repository.Diff(t.Context(), DiffTarget{Kind: DiffTargetCommit, Ref: commit})
	if commitResult.ResolvedRevision != commit || commitResult.MergeBase != "" ||
		commitResult.UnavailableReason != "" || commitResult.Additions != 1 || len(commitResult.Files) != 1 {
		t.Fatalf("Diff(commit) = %#v", commitResult)
	}
}

func TestRepositoryDiffReportsUnavailableTargetsWithoutMutation(t *testing.T) {
	root := initGitRepository(t)
	before := strings.TrimSpace(runGitTestOutput(t, root, "status", "--porcelain=v1"))
	repository := NewRepository(root, root, Limits{})

	baseline := repository.Diff(t.Context(), DiffTarget{Kind: DiffTargetBaseline})
	if !strings.Contains(baseline.UnavailableReason, "baseline") {
		t.Fatalf("Diff(baseline) = %#v", baseline)
	}
	missing := repository.Diff(t.Context(), DiffTarget{Kind: DiffTargetBase, Ref: "missing/ref"})
	if missing.UnavailableReason == "" {
		t.Fatalf("Diff(missing base) = %#v", missing)
	}
	after := strings.TrimSpace(runGitTestOutput(t, root, "status", "--porcelain=v1"))
	if after != before {
		t.Fatalf("passive diff mutated repository: before %q, after %q", before, after)
	}
}

func TestRepositoryDiffDoesNotFollowUntrackedSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation commonly requires additional Windows privileges")
	}
	root := initGitRepository(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("must not be read\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked.txt")); err != nil {
		t.Fatal(err)
	}

	result := NewRepository(root, root, Limits{}).Diff(t.Context(), DiffTarget{Kind: DiffTargetCurrent})
	linked := requireDiffFile(t, result, "linked.txt")
	if !linked.Symlink || !strings.Contains(linked.Omitted, "not followed") || len(linked.Hunks) != 0 {
		t.Fatalf("symlink diff = %#v", linked)
	}
}

func TestRepositoryDiffAppliesIndependentBounds(t *testing.T) {
	root := initGitRepository(t)
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("one\ntwo\nthree\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result := NewRepository(root, root, Limits{DiffFiles: 1, DiffLines: 1, DiffLineBytes: 2}).Diff(
		t.Context(),
		DiffTarget{Kind: DiffTargetCurrent},
	)
	if !result.Truncated || len(result.Files) != 1 || len(result.Files[0].Hunks) != 1 ||
		len(result.Files[0].Hunks[0].Lines) != 1 || len(result.Files[0].Hunks[0].Lines[0].Text) > 2 {
		t.Fatalf("bounded diff = %#v", result)
	}
}

func TestRepositoryDiffDisablesConfiguredGitHelpers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sentinel shell script is Unix-specific")
	}
	root := initGitRepository(t)
	sentinel := filepath.Join(t.TempDir(), "git-helper-ran")
	script := filepath.Join(t.TempDir(), "git-helper")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch \"$1\"\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, root, "config", "diff.external", script+" "+sentinel)
	runGitTest(t, root, "config", "core.fsmonitor", script+" "+sentinel)
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := NewRepository(root, root, Limits{}).Diff(t.Context(), DiffTarget{Kind: DiffTargetCurrent})
	if result.UnavailableReason != "" || len(result.Files) != 1 {
		t.Fatalf("passive diff = %#v", result)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("repository-configured helper executed: %v", err)
	}
}

func TestRepositoryDiffDisablesLazyFetchForMissingPromisorObject(t *testing.T) {
	root := initGitRepository(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	runGitTest(t, root, "clone", "--bare", root, remote)
	blob := strings.TrimSpace(runGitTestOutput(t, root, "rev-parse", "HEAD:tracked.txt"))
	objectPath := filepath.Join(root, ".git", "objects", blob[:2], blob[2:])
	if err := os.Remove(objectPath); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, root, "config", "extensions.partialClone", "origin")
	runGitTest(t, root, "config", "remote.origin.promisor", "true")
	runGitTest(t, root, "config", "remote.origin.partialclonefilter", "blob:none")
	runGitTest(t, root, "config", "remote.origin.url", remote)
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := NewRepository(root, root, Limits{}).Diff(t.Context(), DiffTarget{Kind: DiffTargetCurrent})
	if result.Warning == "" && result.UnavailableReason == "" {
		t.Fatalf("missing promisor object was not surfaced: %#v", result)
	}
	command := exec.Command("git", "-C", root, "cat-file", "-e", blob)
	command.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1", "GIT_OPTIONAL_LOCKS=0")
	if err := command.Run(); err == nil {
		t.Fatal("passive evidence lazily fetched the missing promisor object")
	}
}

func TestRepositoryDiffTreatsGitDerivedPathAsLiteral(t *testing.T) {
	root := initGitRepository(t)
	const name = ":(exclude)"
	if err := os.WriteFile(filepath.Join(root, name), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, root, "--literal-pathspecs", "add", "--", name)
	runGitTest(t, root, "commit", "-m", "add pathspec-like filename")
	if err := os.WriteFile(filepath.Join(root, name), []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := NewRepository(root, root, Limits{}).Diff(t.Context(), DiffTarget{Kind: DiffTargetCurrent})
	file := requireDiffFile(t, result, name)
	if file.Additions != 1 || file.Deletions != 1 || !hasDiffLine(file, "addition", 1, "after") {
		t.Fatalf("literal pathspec diff = %#v", file)
	}
}

func TestRepositoryDiffPreservesRenameAcrossTargets(t *testing.T) {
	root := initGitRepository(t)
	base := strings.TrimSpace(runGitTestOutput(t, root, "rev-parse", "HEAD"))
	if err := os.Rename(filepath.Join(root, "tracked.txt"), filepath.Join(root, "renamed.txt")); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, root, "add", "-A")
	repository := NewRepository(root, root, Limits{})
	assertRenameDiff(t, repository.Diff(t.Context(), DiffTarget{Kind: DiffTargetCurrent}))
	runGitTest(t, root, "commit", "-m", "rename tracked file")
	commit := strings.TrimSpace(runGitTestOutput(t, root, "rev-parse", "HEAD"))
	assertRenameDiff(t, repository.Diff(t.Context(), DiffTarget{Kind: DiffTargetBase, Ref: base}))
	assertRenameDiff(t, repository.Diff(t.Context(), DiffTarget{Kind: DiffTargetCommit, Ref: commit}))
}

func TestRepositoryDiffRejectsAmbiguousMergeCommit(t *testing.T) {
	root := initGitRepository(t)
	runGitTest(t, root, "switch", "-c", "feature")
	if err := os.WriteFile(filepath.Join(root, "feature.txt"), []byte("feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, root, "add", "feature.txt")
	runGitTest(t, root, "commit", "-m", "feature")
	runGitTest(t, root, "switch", "main")
	if err := os.WriteFile(filepath.Join(root, "main.txt"), []byte("main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, root, "add", "main.txt")
	runGitTest(t, root, "commit", "-m", "main")
	runGitTest(t, root, "merge", "--no-ff", "feature", "-m", "merge feature")
	mergeCommit := strings.TrimSpace(runGitTestOutput(t, root, "rev-parse", "HEAD"))

	result := NewRepository(root, root, Limits{}).Diff(
		t.Context(),
		DiffTarget{Kind: DiffTargetCommit, Ref: mergeCommit},
	)
	if !strings.Contains(result.UnavailableReason, "merge commit diff is ambiguous") || len(result.Files) != 0 {
		t.Fatalf("Diff(merge commit) = %#v", result)
	}
}

func TestRepositoryDiffRejectsCommitWithUnavailableShallowParent(t *testing.T) {
	source := initGitRepository(t)
	if err := os.WriteFile(filepath.Join(source, "tracked.txt"), []byte("initial\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, source, "commit", "-am", "second")
	shallow := filepath.Join(t.TempDir(), "shallow")
	remoteURL := (&url.URL{Scheme: "file", Path: filepath.ToSlash(source)}).String()
	runGitTest(t, t.TempDir(), "clone", "--depth=1", remoteURL, shallow)
	commit := strings.TrimSpace(runGitTestOutput(t, shallow, "rev-parse", "HEAD"))

	result := NewRepository(shallow, shallow, Limits{}).Diff(
		t.Context(),
		DiffTarget{Kind: DiffTargetCommit, Ref: commit},
	)
	if !strings.Contains(result.UnavailableReason, "commit parent is not available locally") ||
		len(result.Files) != 0 {
		t.Fatalf("Diff(shallow commit) = %#v", result)
	}
}

func TestParsePatchFileMarksSubmoduleMetadata(t *testing.T) {
	budget := diffBudget{bytes: 4096, hunks: 4, lines: 20}
	file := parsePatchFile(DiffFile{Path: "nested"}, []byte(
		"diff --git a/nested b/nested\n"+
			"@@ -1 +1 @@\n"+
			"-Subproject commit 1111111111111111111111111111111111111111\n"+
			"+Subproject commit 2222222222222222222222222222222222222222\n",
	), 1024, &budget)
	if !file.Submodule || file.Deletions != 1 || file.Additions != 1 {
		t.Fatalf("submodule patch = %#v", file)
	}
}

func TestRepositoryConcurrencyWaitHonorsCancellation(t *testing.T) {
	repository := NewRepository(t.TempDir(), t.TempDir(), Limits{ConcurrentOperations: 1})
	repository.slots <- struct{}{}
	ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	result := repository.Status(ctx)
	<-repository.slots
	if result.Snapshot.Git.UnavailableReason == "" {
		t.Fatalf("canceled Status() = %#v", result)
	}
}

func requireDiffFile(t *testing.T, result DiffResult, path string) DiffFile {
	t.Helper()
	for _, file := range result.Files {
		if file.Path == path {
			return file
		}
	}
	t.Fatalf("diff file %q not found in %#v", path, result)
	return DiffFile{}
}

func hasDiffLine(file DiffFile, kind string, line int, text string) bool {
	for _, hunk := range file.Hunks {
		for _, candidate := range hunk.Lines {
			candidateLine := candidate.NewLine
			if kind == "deletion" {
				candidateLine = candidate.OldLine
			}
			if candidate.Kind == kind && candidateLine == line && candidate.Text == text {
				return true
			}
		}
	}
	return false
}

func assertRenameDiff(t *testing.T, result DiffResult) {
	t.Helper()
	file := requireDiffFile(t, result, "renamed.txt")
	if !strings.HasPrefix(file.Status, "R") || file.OriginalPath != "tracked.txt" ||
		file.Additions != 0 || file.Deletions != 0 {
		t.Fatalf("rename diff = %#v in %#v", file, result)
	}
}

func runGitTestOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
