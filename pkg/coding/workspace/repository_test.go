package workspace

import (
	"bytes"
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

func TestRepositoryStatusAndCurrentDiffExposeTruthfulProvenance(t *testing.T) {
	root := initGitRepository(t)
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("pre-existing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	baseline, err := NewRepository(root, root, Limits{}).CaptureBaseline(t.Context(), BaselineRequest{
		ProjectKey: "project-key", CapturedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repository, err := NewRepositoryWithBaseline(root, root, Limits{}, baseline)
	if err != nil {
		t.Fatal(err)
	}
	status := repository.Status(t.Context())
	if status.Stale || status.BaselineID != baseline.BaselineID || status.Provenance == nil ||
		provenanceForPath(t, *status.Provenance, "tracked.txt") != ProvenancePreExisting ||
		provenanceForPath(t, *status.Provenance, "new.txt") != ProvenanceFirstObservedDuringThread {
		t.Fatalf("status = %#v", status)
	}
	diff := repository.Diff(t.Context(), DiffTarget{Kind: DiffTargetCurrent})
	if diff.Stale || diff.Target.Kind != DiffTargetCurrent || diff.BaselineID != baseline.BaselineID ||
		diff.Provenance == nil || requireDiffFile(t, diff, "tracked.txt").Provenance != ProvenancePreExisting ||
		requireDiffFile(t, diff, "new.txt").Provenance != ProvenanceFirstObservedDuringThread {
		t.Fatalf("current diff = %#v", diff)
	}
}

func TestNewRepositoryWithBaselineRejectsDifferentRepositoryAuthority(t *testing.T) {
	root := initGitRepository(t)
	baseline, err := NewRepository(root, root, Limits{}).CaptureBaseline(t.Context(), BaselineRequest{
		ProjectKey: "project-key", CapturedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	other := initGitRepository(t)
	_, err = NewRepositoryWithBaseline(other, other, Limits{}, baseline)
	if err == nil || !strings.Contains(err.Error(), "repository baseline authority mismatch") {
		t.Fatalf("NewRepositoryWithBaseline() error = %v", err)
	}
}

func TestRepositoryDiffPreservesCachedDeletionAndSamePathUntrackedFile(t *testing.T) {
	root := initGitRepository(t)
	runGitTest(t, root, "rm", "--cached", "tracked.txt")

	result := NewRepository(root, root, Limits{}).Diff(t.Context(), DiffTarget{Kind: DiffTargetCurrent})
	deleted := requireDiffFileStatus(t, result, "tracked.txt", "D")
	untracked := requireDiffFileStatus(t, result, "tracked.txt", "??")
	if deleted.Deletions != 1 || untracked.Additions != 1 ||
		!hasDiffLine(untracked, "addition", 1, "initial") {
		t.Fatalf("same-path tracked/untracked evidence = %#v", result)
	}
}

func TestRepositoryProvenancePreservesCachedDeletionAndSamePathUntrackedFile(t *testing.T) {
	root := initGitRepository(t)
	baseline, err := NewRepository(root, root, Limits{}).CaptureBaseline(t.Context(), BaselineRequest{
		ProjectKey: "project-key", CapturedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	runGitTest(t, root, "rm", "--cached", "tracked.txt")
	repository, err := NewRepositoryWithBaseline(root, root, Limits{}, baseline)
	if err != nil {
		t.Fatal(err)
	}
	status := repository.Status(t.Context())
	if status.Provenance == nil {
		t.Fatalf("status provenance = %#v", status)
	}
	seen := map[string]int{}
	for _, path := range status.Provenance.Paths {
		if path.Path == "tracked.txt" {
			seen[path.Status]++
		}
	}
	if seen["D "] != 1 || seen["??"] != 1 {
		t.Fatalf("same-path status provenance = %#v", status.Provenance.Paths)
	}
	diff := repository.Diff(t.Context(), DiffTarget{Kind: DiffTargetCurrent})
	for _, expected := range []string{"D", "??"} {
		file := requireDiffFileStatus(t, diff, "tracked.txt", expected)
		if file.Provenance != ProvenanceIndeterminate || file.ProvenanceReason == "" {
			t.Fatalf("same-path diff provenance for %q = %#v", expected, file)
		}
	}
}

func TestRepositoryDiffUsesLiveProvenanceAcrossStatusTransition(t *testing.T) {
	root := initGitRepository(t)
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("staged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, root, "add", "tracked.txt")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("unstaged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	baseline, err := NewRepository(root, root, Limits{}).CaptureBaseline(t.Context(), BaselineRequest{
		ProjectKey: "project-key", CapturedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	runGitTest(t, root, "checkout", "--", "tracked.txt")
	repository, err := NewRepositoryWithBaseline(root, root, Limits{}, baseline)
	if err != nil {
		t.Fatal(err)
	}

	diff := repository.Diff(t.Context(), DiffTarget{Kind: DiffTargetCurrent})
	file := requireDiffFileStatus(t, diff, "tracked.txt", "M")
	if file.Provenance != ProvenanceFirstObservedDuringThread ||
		file.Provenance == ProvenanceResolvedSinceBaseline {
		t.Fatalf("live provenance after MM to M transition = %#v", diff)
	}
}

func TestRepositoryProvenanceRefreshFailureDoesNotClaimStaleness(t *testing.T) {
	root := initGitRepository(t)
	limits := Limits{ConcurrentOperations: 1}
	baseline, err := NewRepository(root, root, limits).CaptureBaseline(t.Context(), BaselineRequest{
		ProjectKey: "project-key", CapturedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewRepositoryWithBaseline(root, root, limits, baseline)
	if err != nil {
		t.Fatal(err)
	}
	if !repository.acquire(t.Context()) {
		t.Fatal("acquire repository slot")
	}
	defer repository.release()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	status := StatusResult{Snapshot: captureSnapshot(t.Context(), root, root, Limits{}.normalized())}
	repository.attachStatusProvenance(ctx, &status)
	if status.Stale || status.Provenance == nil ||
		status.Provenance.Reason != "provenance refresh is unavailable" {
		t.Fatalf("status after failed provenance refresh = %#v", status)
	}
	diff := DiffResult{Generation: status.Snapshot.Identity(), EvidenceGeneration: "present"}
	repository.attachDiffProvenance(ctx, &diff)
	if diff.Stale || diff.Provenance == nil || diff.Provenance.Reason != "provenance refresh is unavailable" {
		t.Fatalf("diff after failed provenance refresh = %#v", diff)
	}
}

func TestRepositoryDiffUnbornMatchesCurrentWorktree(t *testing.T) {
	root := t.TempDir()
	runGitTest(t, root, "init", "-b", "main")
	runGitTest(t, root, "config", "user.name", "Fixture")
	runGitTest(t, root, "config", "user.email", "fixture@example.com")
	if err := os.WriteFile(filepath.Join(root, "edited.txt"), []byte("staged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "deleted.txt"), []byte("deleted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, root, "add", "edited.txt", "deleted.txt")
	if err := os.WriteFile(filepath.Join(root, "edited.txt"), []byte("edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "deleted.txt")); err != nil {
		t.Fatal(err)
	}

	result := NewRepository(root, root, Limits{}).Diff(t.Context(), DiffTarget{Kind: DiffTargetCurrent})
	file := requireDiffFileStatus(t, result, "edited.txt", "A")
	if result.UnavailableReason != "" || len(result.Files) != 1 || file.Additions != 1 || file.Deletions != 0 ||
		!hasDiffLine(file, "addition", 1, "edited") {
		t.Fatalf("Diff(unborn current worktree) = %#v", result)
	}
}

func TestRepositoryDiffUnbornExcludesAbsentSparseCheckoutPathsBeforeBudget(t *testing.T) {
	root := t.TempDir()
	runGitTest(t, root, "init", "-b", "main")
	runGitTest(t, root, "config", "user.name", "Fixture")
	runGitTest(t, root, "config", "user.email", "fixture@example.com")
	for _, name := range []string{"included/keep.txt", "excluded/first.txt", "excluded/second.txt"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(name+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runGitTest(t, root, "add", "included", "excluded")
	runGitTest(t, root, "update-index", "--skip-worktree", "excluded/first.txt", "excluded/second.txt")
	if err := os.RemoveAll(filepath.Join(root, "excluded")); err != nil {
		t.Fatal(err)
	}

	status := runGitTestOutput(t, root, "status", "--porcelain=v1", "--untracked-files=all")
	if !strings.Contains(status, "A  excluded/first.txt") ||
		!strings.Contains(status, "A  excluded/second.txt") {
		t.Fatalf("fixture did not retain absent sparse index entries:\n%s", status)
	}
	result := NewRepository(root, root, Limits{DiffFiles: 1}).Diff(
		t.Context(),
		DiffTarget{Kind: DiffTargetCurrent},
	)
	file := requireDiffFile(t, result, "included/keep.txt")
	if result.UnavailableReason != "" || len(result.Files) != 1 || file.Additions != 1 ||
		file.Omitted != "" {
		t.Fatalf("Diff(unborn sparse worktree) = %#v", result)
	}
}

func TestRepositoryDiffUnbornExcludesPathBlockedBySparseCheckoutSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation commonly requires additional Windows privileges")
	}
	root := t.TempDir()
	runGitTest(t, root, "init", "-b", "main")
	runGitTest(t, root, "config", "user.name", "Fixture")
	runGitTest(t, root, "config", "user.email", "fixture@example.com")
	for _, name := range []string{"included/keep.txt", "excluded/hidden.txt"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(name+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runGitTest(t, root, "add", "included", "excluded")
	runGitTest(t, root, "update-index", "--skip-worktree", "excluded/hidden.txt")
	if err := os.RemoveAll(filepath.Join(root, "excluded")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "excluded")); err != nil {
		t.Fatal(err)
	}

	status := runGitTestOutput(t, root, "status", "--porcelain=v1", "--untracked-files=all")
	if !strings.Contains(status, "A  excluded/hidden.txt") || !strings.Contains(status, "?? excluded") {
		t.Fatalf("fixture did not retain sparse index child behind an untracked symlink:\n%s", status)
	}
	result := NewRepository(root, root, Limits{DiffFiles: 2}).Diff(
		t.Context(),
		DiffTarget{Kind: DiffTargetCurrent},
	)
	symlink := requireDiffFileStatus(t, result, "excluded", "??")
	included := requireDiffFile(t, result, "included/keep.txt")
	if result.UnavailableReason != "" || len(result.Files) != 2 || !symlink.Symlink ||
		included.Additions != 1 {
		t.Fatalf("Diff(unborn sparse symlink worktree) = %#v", result)
	}
	for _, file := range result.Files {
		if file.Path == "excluded/hidden.txt" {
			t.Fatalf("blocked sparse child consumed evidence budget: %#v", result)
		}
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
	baseline, err := NewRepository(root, root, Limits{}).CaptureBaseline(t.Context(), BaselineRequest{
		ProjectKey: "project-key", CapturedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
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

	repository, err := NewRepositoryWithBaseline(root, root, Limits{}, baseline)
	if err != nil {
		t.Fatal(err)
	}
	baseResult := repository.Diff(t.Context(), DiffTarget{Kind: DiffTargetBase, Ref: base})
	if baseResult.ResolvedRevision != base || baseResult.MergeBase != base || baseResult.UnavailableReason != "" ||
		baseResult.EvidenceGeneration == "" || baseResult.BaselineID != "" || baseResult.Provenance != nil ||
		!hasDiffLine(requireDiffFile(t, baseResult, "tracked.txt"), "addition", 3, "working") {
		t.Fatalf("Diff(base) = %#v", baseResult)
	}
	if requireDiffFile(t, baseResult, "untracked.txt").Additions != 1 {
		t.Fatalf("Diff(base) omitted untracked state = %#v", baseResult)
	}
	commitResult := repository.Diff(t.Context(), DiffTarget{Kind: DiffTargetCommit, Ref: commit})
	if commitResult.ResolvedRevision != commit || commitResult.MergeBase != "" ||
		commitResult.EvidenceGeneration != "" || commitResult.BaselineID != "" || commitResult.Provenance != nil ||
		commitResult.UnavailableReason != "" || commitResult.Additions != 1 || len(commitResult.Files) != 1 {
		t.Fatalf("Diff(commit) = %#v", commitResult)
	}
}

func TestRepositoryDiffUsesRootModeForTrueRootCommit(t *testing.T) {
	root := initGitRepository(t)
	commit := strings.TrimSpace(runGitTestOutput(t, root, "rev-parse", "HEAD"))
	runGitTest(t, root, "config", "log.showRoot", "false")

	result := NewRepository(root, root, Limits{}).Diff(
		t.Context(),
		DiffTarget{Kind: DiffTargetCommit, Ref: commit},
	)
	file := requireDiffFile(t, result, "tracked.txt")
	if result.UnavailableReason != "" || len(result.Files) != 1 || file.Additions != 1 || file.Deletions != 0 {
		t.Fatalf("Diff(root commit) = %#v", result)
	}
}

func TestRepositoryDiffDiscardsIncompleteNULFramedUntrackedPath(t *testing.T) {
	root := initGitRepository(t)
	directory := strings.Repeat("a", 180)
	nested := strings.Repeat("b", 180)
	name := strings.Repeat("c", 100)
	trackedPath := filepath.Join(directory, nested, name)
	if err := os.MkdirAll(filepath.Join(root, directory, nested), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, trackedPath), []byte("tracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, root, "add", trackedPath)
	runGitTest(t, root, "commit", "-m", "add long tracked path")
	if err := os.WriteFile(filepath.Join(root, trackedPath+"-untracked"), []byte("untracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := NewRepository(root, root, Limits{CommandBytes: len(filepath.ToSlash(trackedPath))}).Diff(
		t.Context(),
		DiffTarget{Kind: DiffTargetCurrent},
	)
	if result.UnavailableReason != "" || !result.Truncated || len(result.Files) != 0 {
		t.Fatalf("truncated NUL-framed untracked evidence = %#v", result)
	}
}

func TestRepositoryDiffReportsUnavailableTargetsWithoutMutation(t *testing.T) {
	root := initGitRepository(t)
	before := strings.TrimSpace(runGitTestOutput(t, root, "status", "--porcelain=v1"))
	repository := NewRepository(root, root, Limits{})

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

func TestRepositoryDiffFailsClosedForUnencodableFilterDriver(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sentinel shell script is Unix-specific")
	}
	root := initGitRepository(t)
	sentinel := filepath.Join(t.TempDir(), "filter-helper-ran")
	script := filepath.Join(t.TempDir(), "filter-helper")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch \"$1\"\ncat\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, ".gitattributes"),
		[]byte("tracked.txt filter=evil=driver\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, root, "config", "filter.evil=driver.clean", script+" "+sentinel)
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := NewRepository(root, root, Limits{}).Diff(t.Context(), DiffTarget{Kind: DiffTargetCurrent})
	if result.UnavailableReason == "" ||
		!strings.Contains(result.Warning, "content-filter name cannot be passively overridden") {
		t.Fatalf("unsafe filter driver was not rejected: %#v", result)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("unencodable filter helper executed: %v", err)
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

func TestRepositoryDiffUsesExplicitParentAtShallowBoundary(t *testing.T) {
	source := initGitRepository(t)
	if err := os.WriteFile(filepath.Join(source, "stable.txt"), []byte("stable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, source, "add", "stable.txt")
	runGitTest(t, source, "commit", "-m", "add stable parent content")
	parent := strings.TrimSpace(runGitTestOutput(t, source, "rev-parse", "HEAD"))
	runGitTest(t, source, "branch", "parent-boundary", parent)
	if err := os.WriteFile(filepath.Join(source, "tracked.txt"), []byte("target\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, source, "commit", "-am", "target")

	shallow := filepath.Join(t.TempDir(), "shallow")
	remoteURL := (&url.URL{Scheme: "file", Path: filepath.ToSlash(source)}).String()
	runGitTest(t, t.TempDir(), "clone", "--depth=1", remoteURL, shallow)
	target := strings.TrimSpace(runGitTestOutput(t, shallow, "rev-parse", "HEAD"))
	runGitTest(
		t,
		shallow,
		"fetch",
		"--depth=1",
		"origin",
		"parent-boundary:refs/remotes/origin/parent-boundary",
	)
	if !strings.Contains(runGitTestOutput(t, shallow, "cat-file", "-p", target), "parent "+parent) {
		t.Fatal("target raw header does not retain its parent")
	}
	shallowPath := filepath.Join(shallow, ".git", "shallow")
	shallowBefore, err := os.ReadFile(shallowPath)
	if err != nil || !strings.Contains(string(shallowBefore), target) {
		t.Fatalf("target shallow marker unavailable: %q, %v", shallowBefore, err)
	}

	result := NewRepository(shallow, shallow, Limits{}).Diff(
		t.Context(),
		DiffTarget{Kind: DiffTargetCommit, Ref: target},
	)
	if result.UnavailableReason != "" || len(result.Files) != 1 ||
		result.Files[0].Path != "tracked.txt" || requireDiffFile(t, result, "tracked.txt").Additions != 1 {
		t.Fatalf("Diff(shallow boundary with local parent) = %#v", result)
	}
	shallowAfter, err := os.ReadFile(shallowPath)
	if err != nil || !bytes.Equal(shallowAfter, shallowBefore) {
		t.Fatalf(
			"passive commit diff changed shallow state: before %q, after %q, error %v",
			shallowBefore,
			shallowAfter,
			err,
		)
	}
}

func TestRepositoryDiffAcceptsCommitWithLargeMessageAfterCompleteHeader(t *testing.T) {
	root := initGitRepository(t)
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, root, "commit", "-am", strings.Repeat("message", 1024))
	commit := strings.TrimSpace(runGitTestOutput(t, root, "rev-parse", "HEAD"))

	result := NewRepository(root, root, Limits{CommandBytes: 512}).Diff(
		t.Context(),
		DiffTarget{Kind: DiffTargetCommit, Ref: commit},
	)
	file := requireDiffFile(t, result, "tracked.txt")
	if result.UnavailableReason != "" || !hasDiffLine(file, "addition", 1, "changed") {
		t.Fatalf("Diff(large commit message) = %#v", result)
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

func requireDiffFileStatus(t *testing.T, result DiffResult, path, status string) DiffFile {
	t.Helper()
	for _, file := range result.Files {
		if file.Path == path && file.Status == status {
			return file
		}
	}
	t.Fatalf("diff file %q with status %q not found in %#v", path, status, result)
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
