package worktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
)

func TestCleanupRemovesOnlyCleanUnchangedWorktreeAndBranch(t *testing.T) {
	fixture, allocation, owner := ownedGitFixture(t)
	handoff, err := owner.CaptureHandoff(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.Cleanup(t.Context(), handoff.HandoffID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.WorktreeRemoved || !result.BranchDeleted || result.BranchRetained ||
		result.Allocation.State != StateReleased || result.Handoff.Class != HandoffReady {
		t.Fatalf("Cleanup() = %#v", result)
	}
	if _, err := os.Lstat(allocation.ExecutionRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("execution root still exists: %v", err)
	}
	if _, found, err := fixture.manager.branchHead(t.Context(), allocation); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("unchanged owned branch still exists")
	}
	loaded, err := fixture.manager.LoadHandoff(t.Context(), allocation.WorktreeID)
	if err != nil || loaded.HandoffID != result.Handoff.HandoffID {
		t.Fatalf("last handoff = %#v, %v", loaded, err)
	}
	repeated, err := owner.Cleanup(t.Context(), result.Handoff.HandoffID)
	if err != nil || !repeated.WorktreeRemoved || repeated.Allocation.State != StateReleased {
		t.Fatalf("repeated Cleanup() = %#v, %v", repeated, err)
	}
	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}
	successor, err := fixture.manager.AcquireOwner(
		t.Context(),
		ownerRequestForAllocation(allocation, "cleanup-successor"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = successor.Release() }()
	recovered, err := successor.Cleanup(t.Context(), result.Handoff.HandoffID)
	if err != nil || recovered.Allocation.State != StateReleased {
		t.Fatalf("restart Cleanup() = %#v, %v", recovered, err)
	}
}

func TestCleanupRetainsDirtyAndCommittedWork(t *testing.T) {
	tests := []struct {
		name   string
		change func(*testing.T, Allocation)
	}{
		{
			name: "untracked",
			change: func(t *testing.T, allocation Allocation) {
				t.Helper()
				if err := os.WriteFile(
					filepath.Join(allocation.ExecutionRoot, "keep.txt"),
					[]byte("keep\n"),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "commit",
			change: func(t *testing.T, allocation Allocation) {
				t.Helper()
				if err := os.WriteFile(
					filepath.Join(allocation.ExecutionRoot, "keep.txt"),
					[]byte("keep\n"),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
				runGitTest(t, allocation.ExecutionRoot, "add", "keep.txt")
				runGitTest(
					t,
					allocation.ExecutionRoot,
					"-c", "user.email=mintclaw@example.invalid",
					"-c", "user.name=MintClaw Test",
					"commit", "-m", "keep",
				)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, allocation, owner := ownedGitFixture(t)
			test.change(t, allocation)
			handoff, err := owner.CaptureHandoff(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			result, err := owner.Cleanup(t.Context(), handoff.HandoffID)
			if !errors.Is(err, ErrCleanupRefused) || result.WorktreeRemoved ||
				result.Allocation.State != StateRetained || result.Reason == "" {
				t.Fatalf("Cleanup() = %#v, %v", result, err)
			}
			if _, statErr := os.Stat(filepath.Join(allocation.ExecutionRoot, "keep.txt")); statErr != nil {
				t.Fatalf("retained work is unavailable: %v", statErr)
			}
			if _, found, branchErr := fixture.manager.branchHead(t.Context(), allocation); branchErr != nil || !found {
				t.Fatalf("retained branch found=%t, error=%v", found, branchErr)
			}
		})
	}
}

func TestCleanupRetainsIgnoredFiles(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*testing.T, *gitFixture)
	}{
		{
			name: "repository ignore",
			configure: func(t *testing.T, fixture *gitFixture) {
				t.Helper()
				if err := os.WriteFile(
					filepath.Join(fixture.repository, ".gitignore"),
					[]byte("*.secret\n"),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
				runGitTest(t, fixture.repository, "add", ".gitignore")
				runGitTest(t, fixture.repository, "commit", "-m", "ignore secrets")
				fixture.head = strings.TrimSpace(runGitTest(t, fixture.repository, "rev-parse", "HEAD"))
				fixture.source = resolveProjectTest(t, fixture.repository)
			},
		},
		{
			name: "configured excludes file",
			configure: func(t *testing.T, fixture *gitFixture) {
				t.Helper()
				excludes := filepath.Join(fixture.root, "global-excludes")
				if err := os.WriteFile(excludes, []byte("*.secret\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				runGitTest(t, fixture.repository, "config", "core.excludesFile", excludes)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGitFixture(t)
			test.configure(t, fixture)
			request := fixture.request("task-ignored", "task-generation", thread.NewThreadID())
			allocation, err := fixture.manager.Allocate(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			owner, err := fixture.manager.AcquireOwner(
				t.Context(),
				ownerRequestForAllocation(allocation, "worker-ignored"),
			)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = owner.Release() }()
			ignoredPath := filepath.Join(allocation.ExecutionRoot, "credentials.secret")
			if err := os.WriteFile(ignoredPath, []byte("keep me\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			handoff, err := owner.CaptureHandoff(t.Context())
			if err != nil || handoff.Class != HandoffReady {
				t.Fatalf("ignored-only handoff = %#v, %v", handoff, err)
			}

			result, err := owner.Cleanup(t.Context(), handoff.HandoffID)
			if !errors.Is(err, ErrCleanupRefused) || result.WorktreeRemoved ||
				result.Allocation.State != StateRetained || !strings.Contains(result.Reason, "ignored") {
				t.Fatalf("Cleanup(ignored file) = %#v, %v", result, err)
			}
			if data, readErr := os.ReadFile(ignoredPath); readErr != nil || string(data) != "keep me\n" {
				t.Fatalf("ignored file = %q, %v", data, readErr)
			}
		})
	}
}

func TestCleanupRetainsWorktreeWhenIgnoredFileEvidenceIsIncomplete(t *testing.T) {
	fixture, allocation, owner := ownedGitFixture(t)
	handoff, err := owner.CaptureHandoff(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	original := fixture.manager.runGit
	fixture.manager.runGit = func(ctx context.Context, cwd string, args ...string) (gitOutput, error) {
		if slicesContain(args, "ls-files") && slicesContain(args, "--ignored") {
			return gitOutput{stdout: "ignored.bin\x00", truncated: true}, nil
		}
		return original(ctx, cwd, args...)
	}

	result, err := owner.Cleanup(t.Context(), handoff.HandoffID)
	if !errors.Is(err, ErrCleanupRefused) || result.WorktreeRemoved ||
		result.Allocation.State != StateUncertain || !strings.Contains(result.Reason, "incomplete") {
		t.Fatalf("Cleanup(incomplete ignored evidence) = %#v, %v", result, err)
	}
	if _, statErr := os.Stat(allocation.ExecutionRoot); statErr != nil {
		t.Fatalf("worktree with incomplete evidence was removed: %v", statErr)
	}
}

func TestCleanupRetainsTrackedPathsHiddenByIndexFlags(t *testing.T) {
	for _, flag := range []string{"--assume-unchanged", "--skip-worktree"} {
		t.Run(flag, func(t *testing.T) {
			_, allocation, owner := ownedGitFixture(t)
			path := filepath.Join(allocation.ExecutionRoot, "README.md")
			runGitTest(t, allocation.ExecutionRoot, "update-index", flag, "--", "README.md")
			if err := os.WriteFile(path, []byte("hidden change\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			handoff, err := owner.CaptureHandoff(t.Context())
			if err != nil || handoff.Class != HandoffReady {
				t.Fatalf("index-hidden handoff = %#v, %v", handoff, err)
			}

			result, err := owner.Cleanup(t.Context(), handoff.HandoffID)
			if !errors.Is(err, ErrCleanupRefused) || result.WorktreeRemoved ||
				result.Allocation.State != StateRetained || !strings.Contains(result.Reason, "index flags") {
				t.Fatalf("Cleanup(%s) = %#v, %v", flag, result, err)
			}
			if data, readErr := os.ReadFile(path); readErr != nil || string(data) != "hidden change\n" {
				t.Fatalf("index-hidden file = %q, %v", data, readErr)
			}
		})
	}
}

func TestCleanupRetainsWorktreeWhenIndexFlagEvidenceIsIncomplete(t *testing.T) {
	fixture, allocation, owner := ownedGitFixture(t)
	handoff, err := owner.CaptureHandoff(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	original := fixture.manager.runGit
	fixture.manager.runGit = func(ctx context.Context, cwd string, args ...string) (gitOutput, error) {
		if slicesContain(args, "ls-files") && slicesContain(args, "-v") {
			return gitOutput{stdout: "H README.md\x00", truncated: true}, nil
		}
		return original(ctx, cwd, args...)
	}

	result, err := owner.Cleanup(t.Context(), handoff.HandoffID)
	if !errors.Is(err, ErrCleanupRefused) || result.WorktreeRemoved ||
		result.Allocation.State != StateUncertain || !strings.Contains(result.Reason, "incomplete") {
		t.Fatalf("Cleanup(incomplete index evidence) = %#v, %v", result, err)
	}
	if _, statErr := os.Stat(allocation.ExecutionRoot); statErr != nil {
		t.Fatalf("worktree with incomplete index evidence was removed: %v", statErr)
	}
}

func TestCleanupPreservesCommitCreatedDuringNormalRemoval(t *testing.T) {
	fixture, allocation, owner := ownedGitFixture(t)
	handoff, err := owner.CaptureHandoff(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	original := fixture.manager.runGit
	committed := false
	fixture.manager.runGit = func(ctx context.Context, cwd string, args ...string) (gitOutput, error) {
		if !committed && slicesContain(args, "worktree") && slicesContain(args, "remove") {
			committed = true
			claimedRoot := args[len(args)-1]
			if err := os.WriteFile(
				filepath.Join(claimedRoot, "late-commit.txt"),
				[]byte("preserve\n"),
				0o600,
			); err != nil {
				return gitOutput{}, err
			}
			if _, err := original(ctx, claimedRoot, "add", "late-commit.txt"); err != nil {
				return gitOutput{}, err
			}
			if _, err := original(
				ctx,
				claimedRoot,
				"-c", "user.email=mintclaw@example.invalid",
				"-c", "user.name=MintClaw Test",
				"commit", "-m", "late commit",
			); err != nil {
				return gitOutput{}, err
			}
		}
		return original(ctx, cwd, args...)
	}

	result, err := owner.Cleanup(t.Context(), handoff.HandoffID)
	if err != nil {
		t.Fatal(err)
	}
	if !committed || !result.WorktreeRemoved || result.BranchDeleted || !result.BranchRetained ||
		result.Allocation.State != StateReleased || result.Reason == "" {
		t.Fatalf("Cleanup(late commit) = %#v", result)
	}
	head, found, err := fixture.manager.branchHead(t.Context(), allocation)
	if err != nil || !found || head == allocation.BaseRevision {
		t.Fatalf("preserved branch head = %q, found=%t, error=%v", head, found, err)
	}
}

func TestCleanupRefusesReplacedOrLockedRoot(t *testing.T) {
	t.Run("replaced", func(t *testing.T) {
		fixture, allocation, owner := ownedGitFixture(t)
		handoff, err := owner.CaptureHandoff(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		runGitTest(t, fixture.repository, "worktree", "remove", allocation.ExecutionRoot)
		if err := os.Mkdir(allocation.ExecutionRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		userPath := filepath.Join(allocation.ExecutionRoot, "user.txt")
		if err := os.WriteFile(userPath, []byte("keep\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		result, err := owner.Cleanup(t.Context(), handoff.HandoffID)
		if !errors.Is(err, ErrCleanupRefused) || result.WorktreeRemoved ||
			result.Allocation.State != StateUncertain {
			t.Fatalf("Cleanup(replaced) = %#v, %v", result, err)
		}
		if data, readErr := os.ReadFile(userPath); readErr != nil || string(data) != "keep\n" {
			t.Fatalf("replacement content = %q, %v", data, readErr)
		}
	})

	t.Run("locked", func(t *testing.T) {
		_, allocation, owner := ownedGitFixture(t)
		handoff, err := owner.CaptureHandoff(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		runGitTest(t, allocation.ExecutionRoot, "worktree", "lock", allocation.ExecutionRoot)
		result, err := owner.Cleanup(t.Context(), handoff.HandoffID)
		if !errors.Is(err, ErrCleanupRefused) || result.WorktreeRemoved ||
			result.Allocation.State != StateRetained || !strings.Contains(result.Reason, "locked") {
			t.Fatalf("Cleanup(locked) = %#v, %v", result, err)
		}
		if _, statErr := os.Stat(allocation.ExecutionRoot); statErr != nil {
			t.Fatalf("locked worktree was removed: %v", statErr)
		}
	})
}

func TestCleanupRefusesSamePathExecutionRootReplacement(t *testing.T) {
	fixture, allocation, owner := ownedGitFixture(t)
	handoff, err := owner.CaptureHandoff(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	movedRoot := filepath.Join(fixture.manager.WorktreeParent(), "moved-"+allocation.WorktreeID)
	if err := os.Rename(allocation.ExecutionRoot, movedRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(allocation.ExecutionRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".git", "README.md"} {
		data, readErr := os.ReadFile(filepath.Join(movedRoot, name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if writeErr := os.WriteFile(filepath.Join(allocation.ExecutionRoot, name), data, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}

	replacement, err := thread.ResolveProject(t.Context(), allocation.ExecutionRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !executionMatchesAllocation(replacement, allocation) {
		t.Fatal("same-path replacement did not preserve the semantic project identity used by prior cleanup checks")
	}
	if status := runGitTest(
		t,
		allocation.ExecutionRoot,
		"status",
		"--porcelain=v1",
		"--untracked-files=all",
	); status != "" {
		t.Fatalf("replacement worktree status = %q, want clean", status)
	}
	registration, found, complete := fixture.manager.registeredWorktree(t.Context(), allocation)
	if !complete || !found || registration.path != allocation.ExecutionRoot {
		t.Fatalf("replacement registration = %#v, found=%t, complete=%t", registration, found, complete)
	}
	replacementIdentity, err := inspectDirectoryIdentity(allocation.ExecutionRoot)
	if err != nil {
		t.Fatal(err)
	}
	if replacementIdentity == allocation.ExecutionRootFileIdentity {
		t.Fatal("same-path replacement reused the original execution-root filesystem identity")
	}
	target := fixture.manager.inspectCleanupTarget(t.Context(), allocation)
	if target.present || target.absent || !strings.Contains(target.reason, "replaced") {
		t.Fatalf("inspectCleanupTarget(replacement) = %#v", target)
	}

	result, err := owner.Cleanup(t.Context(), handoff.HandoffID)
	if !errors.Is(err, ErrCleanupRefused) || result.WorktreeRemoved || result.BranchDeleted ||
		result.Allocation.State != StateUncertain || result.Handoff.Class != HandoffMismatch ||
		!strings.Contains(result.Handoff.Reason, "directory") {
		t.Fatalf("Cleanup(same-path replacement) = %#v, %v", result, err)
	}
	for _, root := range []string{allocation.ExecutionRoot, movedRoot} {
		if data, readErr := os.ReadFile(filepath.Join(root, "README.md")); readErr != nil || len(data) == 0 {
			t.Fatalf("preserved README at %q = %q, %v", root, data, readErr)
		}
	}
	if _, found, err := fixture.manager.branchHead(t.Context(), allocation); err != nil || !found {
		t.Fatalf("owned branch after refusal: found=%t, error=%v", found, err)
	}
}

func TestCleanupAtomicClaimRefusesSwapAfterOpeningExecutionRoot(t *testing.T) {
	fixture, allocation, owner := ownedGitFixture(t)
	handoff, err := owner.CaptureHandoff(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	originalRename := fixture.manager.renameCleanup
	movedRoot := filepath.Join(fixture.manager.WorktreeParent(), "moved-"+allocation.WorktreeID)
	fixture.manager.renameCleanup = func(root *os.Root, oldName, newName string) error {
		if err := os.Rename(allocation.ExecutionRoot, movedRoot); err != nil {
			return err
		}
		if err := os.Mkdir(allocation.ExecutionRoot, 0o700); err != nil {
			return err
		}
		for _, name := range []string{".git", "README.md"} {
			data, readErr := os.ReadFile(filepath.Join(movedRoot, name))
			if readErr != nil {
				return readErr
			}
			if writeErr := os.WriteFile(filepath.Join(allocation.ExecutionRoot, name), data, 0o600); writeErr != nil {
				return writeErr
			}
		}
		if err := os.WriteFile(
			filepath.Join(allocation.ExecutionRoot, "user.txt"),
			[]byte("keep\n"),
			0o600,
		); err != nil {
			return err
		}
		return originalRename(root, oldName, newName)
	}
	removeCalled := false
	repairCalled := false
	originalGit := fixture.manager.runGit
	fixture.manager.runGit = func(ctx context.Context, cwd string, args ...string) (gitOutput, error) {
		if slicesContain(args, "worktree") && slicesContain(args, "remove") {
			removeCalled = true
		}
		if slicesContain(args, "worktree") && slicesContain(args, "repair") {
			repairCalled = true
		}
		return originalGit(ctx, cwd, args...)
	}

	result, err := owner.Cleanup(t.Context(), handoff.HandoffID)
	if !errors.Is(err, ErrCleanupRefused) || result.WorktreeRemoved || removeCalled || repairCalled ||
		result.Allocation.State != StateUncertain || !strings.Contains(result.Reason, "atomically bind") {
		t.Fatalf(
			"Cleanup(racing replacement) = %#v, %v; repairCalled=%t, removeCalled=%t",
			result,
			err,
			repairCalled,
			removeCalled,
		)
	}
	for path, wanted := range map[string]string{
		filepath.Join(movedRoot, "README.md"):                   "fixture\n",
		filepath.Join(cleanupClaimPath(allocation), "user.txt"): "keep\n",
	} {
		if data, readErr := os.ReadFile(path); readErr != nil || string(data) != wanted {
			t.Fatalf("preserved file %q = %q, %v", path, data, readErr)
		}
	}
	if _, found, err := fixture.manager.branchHead(t.Context(), allocation); err != nil || !found {
		t.Fatalf("owned branch after claim refusal: found=%t, error=%v", found, err)
	}
}

func TestCleanupRecoversInterruptedRemoval(t *testing.T) {
	tests := []struct {
		name         string
		removeBefore bool
	}{
		{name: "before Git", removeBefore: false},
		{name: "after Git", removeBefore: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, allocation, owner := ownedGitFixture(t)
			handoff, err := owner.CaptureHandoff(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			pending, err := fixture.manager.setCleanupState(
				t.Context(),
				owner,
				owner.Allocation(),
				StateCleanupPending,
				handoff.HandoffID,
				"",
			)
			if err != nil {
				t.Fatal(err)
			}
			if test.removeBefore {
				runGitTest(t, fixture.repository, "worktree", "remove", allocation.ExecutionRoot)
			}
			if err := owner.Release(); err != nil {
				t.Fatal(err)
			}
			successor, err := fixture.manager.AcquireOwner(
				t.Context(),
				ownerRequestForAllocation(allocation, "cleanup-recovery"),
			)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = successor.Release() }()
			result, err := successor.Cleanup(t.Context(), pending.HandoffID)
			if err != nil || !result.WorktreeRemoved || result.Allocation.State != StateReleased {
				t.Fatalf("Cleanup(recovery) = %#v, %v", result, err)
			}
		})
	}
}

func TestCleanupRecoversInterruptedAtomicClaim(t *testing.T) {
	fixture, allocation, owner := ownedGitFixture(t)
	handoff, err := owner.CaptureHandoff(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	pending, err := fixture.manager.setCleanupState(
		t.Context(), owner, owner.Allocation(), StateCleanupPending, handoff.HandoffID, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	claimPath := cleanupClaimPath(allocation)
	if err := os.Rename(allocation.ExecutionRoot, claimPath); err != nil {
		t.Fatal(err)
	}
	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}
	successor, err := fixture.manager.AcquireOwner(
		t.Context(),
		ownerRequestForAllocation(allocation, "cleanup-claim-recovery"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = successor.Release() }()
	result, err := successor.Cleanup(t.Context(), pending.HandoffID)
	if err != nil || !result.WorktreeRemoved || result.Allocation.State != StateReleased {
		t.Fatalf("Cleanup(claim recovery) = %#v, %v", result, err)
	}
	if _, statErr := os.Lstat(claimPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("claimed worktree still exists: %v", statErr)
	}
}

func TestCleanupRecoveryDoesNotMutateReplacementSourceRepository(t *testing.T) {
	fixture, allocation, owner := ownedGitFixture(t)
	handoff, err := owner.CaptureHandoff(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	pending, err := fixture.manager.setCleanupState(
		t.Context(),
		owner,
		owner.Allocation(),
		StateCleanupPending,
		handoff.HandoffID,
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	runGitTest(t, fixture.repository, "worktree", "remove", allocation.ExecutionRoot)
	replaceFixtureSourceRepository(t, fixture, allocation)
	replacementIdentity, err := inspectDirectoryIdentity(filepath.Join(fixture.repository, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if replacementIdentity == allocation.SourceCommonDirIdentity {
		t.Fatal("replacement repository reused the original common-directory identity")
	}

	result, err := owner.Cleanup(t.Context(), pending.HandoffID)
	if !errors.Is(err, ErrCleanupRefused) || result.WorktreeRemoved || result.BranchDeleted ||
		result.Allocation.State != StateCleanupPending ||
		!strings.Contains(result.Reason, "source repository identity") {
		t.Fatalf("Cleanup(replaced source) = %#v, %v", result, err)
	}
	replacementHead := strings.TrimSpace(
		runGitTest(t, fixture.repository, "rev-parse", "--verify", "refs/heads/"+allocation.Branch),
	)
	if replacementHead != allocation.BaseRevision {
		t.Fatalf("replacement branch head = %q, want %q", replacementHead, allocation.BaseRevision)
	}
	loaded, err := fixture.manager.Load(t.Context(), allocation.WorktreeID)
	if err != nil || loaded.State != StateCleanupPending ||
		!strings.Contains(loaded.RetentionReason, "source repository identity") {
		t.Fatalf("retained recovery allocation = %#v, %v", loaded, err)
	}
}

func TestCleanupRecoveryRetainsBranchUsedByAnotherWorktree(t *testing.T) {
	fixture, allocation, owner := ownedGitFixture(t)
	handoff, err := owner.CaptureHandoff(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	pending, err := fixture.manager.setCleanupState(
		t.Context(),
		owner,
		owner.Allocation(),
		StateCleanupPending,
		handoff.HandoffID,
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	runGitTest(t, fixture.repository, "worktree", "remove", allocation.ExecutionRoot)
	replacement := filepath.Join(t.TempDir(), "replacement")
	runGitTest(t, fixture.repository, "worktree", "add", replacement, allocation.Branch)

	result, err := owner.Cleanup(t.Context(), pending.HandoffID)
	if err != nil || !result.WorktreeRemoved || result.BranchDeleted || !result.BranchRetained ||
		result.Allocation.State != StateReleased || !strings.Contains(result.Reason, "another worktree") {
		t.Fatalf("Cleanup(reattached branch) = %#v, %v", result, err)
	}
	if branch := strings.TrimSpace(
		runGitTest(t, replacement, "branch", "--show-current"),
	); branch != allocation.Branch {
		t.Fatalf("replacement branch = %q, want %q", branch, allocation.Branch)
	}
	if head, found, err := fixture.manager.branchHead(t.Context(), allocation); err != nil ||
		!found || head != allocation.BaseRevision {
		t.Fatalf("retained branch head = %q, found=%t, error=%v", head, found, err)
	}
}

func TestCleanupRecoveryRetainsBranchWhenFinalCatalogIsUnavailable(t *testing.T) {
	fixture, allocation, owner := ownedGitFixture(t)
	handoff, err := owner.CaptureHandoff(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	pending, err := fixture.manager.setCleanupState(
		t.Context(), owner, owner.Allocation(), StateCleanupPending, handoff.HandoffID, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	runGitTest(t, fixture.repository, "worktree", "remove", allocation.ExecutionRoot)
	original := fixture.manager.runGit
	listCalls := 0
	fixture.manager.runGit = func(ctx context.Context, cwd string, args ...string) (gitOutput, error) {
		if slicesContain(args, "list") && slicesContain(args, "--porcelain") {
			listCalls++
			if listCalls == 2 {
				return gitOutput{stdout: "malformed\x00\x00"}, nil
			}
		}
		return original(ctx, cwd, args...)
	}

	result, err := owner.Cleanup(t.Context(), pending.HandoffID)
	if !errors.Is(err, ErrCleanupRefused) || !result.WorktreeRemoved || result.BranchDeleted ||
		result.Allocation.State != StateCleanupPending || !strings.Contains(result.Reason, "unavailable") {
		t.Fatalf("Cleanup(unavailable final catalog) = %#v, %v", result, err)
	}
	if _, found, err := fixture.manager.branchHead(t.Context(), allocation); err != nil || !found {
		t.Fatalf("retained branch found=%t, error=%v", found, err)
	}
}

func TestCleanupRefusesAmbiguousCatalogWithoutRemovingAnything(t *testing.T) {
	fixture, allocation, owner := ownedGitFixture(t)
	handoff, err := owner.CaptureHandoff(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	original := fixture.manager.runGit
	removeCalled := false
	fixture.manager.runGit = func(ctx context.Context, cwd string, args ...string) (gitOutput, error) {
		if slicesContain(args, "list") && slicesContain(args, "--porcelain") {
			return gitOutput{stdout: "malformed\x00\x00"}, nil
		}
		if slicesContain(args, "remove") {
			removeCalled = true
		}
		return original(ctx, cwd, args...)
	}
	result, err := owner.Cleanup(t.Context(), handoff.HandoffID)
	if !errors.Is(err, ErrCleanupRefused) || result.WorktreeRemoved || removeCalled ||
		result.Allocation.State != StateUncertain {
		t.Fatalf("Cleanup(ambiguous) = %#v, %v; removeCalled=%t", result, err, removeCalled)
	}
	if _, statErr := os.Stat(allocation.ExecutionRoot); statErr != nil {
		t.Fatalf("ambiguous worktree was removed: %v", statErr)
	}
}

func TestCleanupRetainsAtomicClaimAfterFailedRemoval(t *testing.T) {
	fixture, allocation, owner := ownedGitFixture(t)
	handoff, err := owner.CaptureHandoff(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	original := fixture.manager.runGit
	fixture.manager.runGit = func(ctx context.Context, cwd string, args ...string) (gitOutput, error) {
		if slicesContain(args, "worktree") && slicesContain(args, "remove") {
			return gitOutput{}, errors.New("injected worktree removal failure")
		}
		return original(ctx, cwd, args...)
	}

	result, err := owner.Cleanup(t.Context(), handoff.HandoffID)
	if !errors.Is(err, ErrCleanupRefused) || result.WorktreeRemoved ||
		result.Allocation.State != StateCleanupPending || result.Allocation.HandoffID != result.Handoff.HandoffID ||
		result.Handoff.HandoffID == "" || !strings.Contains(result.Reason, "claimed") {
		t.Fatalf("Cleanup(failed claimed removal) = %#v, %v", result, err)
	}
	loaded, err := fixture.manager.Load(t.Context(), allocation.WorktreeID)
	if err != nil || loaded.State != StateCleanupPending || loaded.HandoffID != result.Handoff.HandoffID ||
		!strings.Contains(loaded.RetentionReason, "claimed") {
		t.Fatalf("retained cleanup allocation = %#v, %v", loaded, err)
	}
	if loadedHandoff, err := fixture.manager.LoadHandoff(t.Context(), allocation.WorktreeID); err != nil ||
		loadedHandoff.HandoffID != result.Handoff.HandoffID {
		t.Fatalf("retained handoff = %#v, %v", loadedHandoff, err)
	}
	if data, err := os.ReadFile(
		filepath.Join(cleanupClaimPath(allocation), "README.md"),
	); err != nil ||
		len(data) == 0 {
		t.Fatalf("retained cleanup claim = %q, %v", data, err)
	}
}

func TestRequireActiveOwnerRejectsCleanupRecoveryLease(t *testing.T) {
	fixture, _, owner := ownedGitFixture(t)
	handoff, err := owner.CaptureHandoff(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	pending, err := fixture.manager.setCleanupState(
		t.Context(), owner, owner.Allocation(), StateCleanupPending, handoff.HandoffID, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}
	request := OwnerRequest{
		WorktreeID: pending.WorktreeID, TaskID: pending.TaskID,
		TaskGenerationID: pending.TaskGenerationID, ThreadID: pending.ThreadID,
		WorkerGenerationID: "cleanup-only",
	}
	cleanupOwner, err := fixture.manager.AcquireOwner(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cleanupOwner.Release() }()
	if _, err := fixture.manager.RequireActiveOwner(t.Context(), request); !errors.Is(err, ErrOwnerInactive) {
		t.Fatalf("RequireActiveOwner(cleanup pending) error = %v", err)
	}
}

func TestParseRegisteredWorktreesRejectsUnknownOrEmptyEvidence(t *testing.T) {
	for _, input := range []string{
		"",
		"worktree /tmp/example\x00HEAD " + strings.Repeat("a", 40) + "\x00future field\x00\x00",
	} {
		if _, err := parseRegisteredWorktrees(input); err == nil {
			t.Fatalf("parseRegisteredWorktrees(%q) succeeded", input)
		}
	}
}

func TestReleasedAllocationCannotStartMutationWorker(t *testing.T) {
	fixture, allocation, owner := ownedGitFixture(t)
	handoff, err := owner.CaptureHandoff(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.Cleanup(t.Context(), handoff.HandoffID)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}
	request := ownerRequestForAllocation(allocation, "released-owner")
	releasedOwner, err := fixture.manager.AcquireOwner(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = releasedOwner.Release() }()
	if _, err := releasedOwner.BeginLifecycle(t.Context(), request); !errors.Is(err, ErrOwnerInactive) {
		t.Fatalf("BeginLifecycle(released) error = %v", err)
	}
	if result.Allocation.State != StateReleased {
		t.Fatalf("released result = %#v", result)
	}
}

func TestCleanupRejectsDifferentHandoffID(t *testing.T) {
	_, allocation, owner := ownedGitFixture(t)
	if _, err := owner.CaptureHandoff(t.Context()); err != nil {
		t.Fatal(err)
	}
	other := strings.Repeat("a", 64)
	if other == owner.Allocation().HandoffID {
		other = strings.Repeat("b", 64)
	}
	if _, err := owner.Cleanup(t.Context(), other); !errors.Is(err, ErrAllocationConflict) {
		t.Fatalf("Cleanup(%s for %s) error = %v", other, allocation.WorktreeID, err)
	}
}

func TestCleanupRefusalKeepsLatestHandoffDurable(t *testing.T) {
	fixture, allocation, owner := ownedGitFixture(t)
	first, err := owner.CaptureHandoff(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(allocation.ExecutionRoot, "late.txt"), []byte("late\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := owner.Cleanup(t.Context(), first.HandoffID)
	if !errors.Is(err, ErrCleanupRefused) || result.Handoff.HandoffID == first.HandoffID {
		t.Fatalf("Cleanup(late change) = %#v, %v", result, err)
	}
	latest, err := fixture.manager.LoadHandoff(t.Context(), allocation.WorktreeID)
	if err != nil || latest.HandoffID != result.Handoff.HandoffID || latest.Class != HandoffChanges {
		t.Fatalf("latest retained handoff = %#v, %v", latest, err)
	}
}

func TestCleanupUsesNormalNonRecursiveGitRemoval(t *testing.T) {
	fixture, _, owner := ownedGitFixture(t)
	handoff, err := owner.CaptureHandoff(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	original := fixture.manager.runGit
	var removal []string
	fixture.manager.runGit = func(ctx context.Context, cwd string, args ...string) (gitOutput, error) {
		if slicesContain(args, "remove") {
			removal = append([]string(nil), args...)
		}
		return original(ctx, cwd, args...)
	}
	if _, err := owner.Cleanup(t.Context(), handoff.HandoffID); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(removal, " ")
	if joined == "" || strings.Contains(joined, "--force") || strings.Contains(joined, " -f ") {
		t.Fatalf("Git removal arguments = %q", joined)
	}
}
