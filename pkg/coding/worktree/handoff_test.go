package worktree

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
)

func TestOwnerCapturesAndLoadsCleanHandoffBeforeRelease(t *testing.T) {
	fixture := newGitFixture(t)
	request := fixture.request("task-handoff", "task-generation", thread.NewThreadID())
	allocation, err := fixture.manager.Allocate(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	ownerRequest := ownerRequestForAllocation(allocation, "worker-generation")
	owner, err := fixture.manager.AcquireOwner(t.Context(), ownerRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Release() }()

	active, err := fixture.manager.RequireActiveOwner(t.Context(), ownerRequest)
	if err != nil || active.ExecutionRoot != allocation.ExecutionRoot {
		t.Fatalf("RequireActiveOwner() = %#v, %v", active, err)
	}
	handoff, err := owner.CaptureHandoff(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if handoff.Class != HandoffReady || handoff.Dirty || !handoff.StatusComplete ||
		!handoff.ComparisonComplete || !handoff.OperationsComplete || handoff.Head != allocation.BaseRevision {
		t.Fatalf("clean handoff = %#v", handoff)
	}
	loaded, err := fixture.manager.LoadHandoff(t.Context(), allocation.WorktreeID)
	if err != nil || !reflect.DeepEqual(loaded, handoff) {
		t.Fatalf("LoadHandoff() = %#v, %v; want %#v", loaded, err, handoff)
	}
	record, err := fixture.manager.Load(t.Context(), allocation.WorktreeID)
	if err != nil || record.State != StateRetained {
		t.Fatalf("retained allocation = %#v, %v", record, err)
	}
	if err := owner.Validate(ownerRequest); err != nil {
		t.Fatalf("owner released before handoff consumer: %v", err)
	}
}

func TestHandoffSeparatesChangesAndCommittedHead(t *testing.T) {
	t.Run("working tree changes", func(t *testing.T) {
		fixture, allocation, owner := ownedGitFixture(t)
		if err := os.WriteFile(
			filepath.Join(allocation.ExecutionRoot, "README.md"),
			[]byte("unstaged\n"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(allocation.ExecutionRoot, "staged.txt"),
			[]byte("staged\n"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		runGitTest(t, allocation.ExecutionRoot, "add", "staged.txt")
		if err := os.WriteFile(
			filepath.Join(allocation.ExecutionRoot, "untracked.txt"),
			[]byte("local\n"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}

		handoff, err := owner.CaptureHandoff(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if handoff.Class != HandoffChanges || !handoff.Dirty ||
			!containsPath(handoff.Changes.Staged, "staged.txt") ||
			!containsPath(handoff.Changes.Unstaged, "README.md") ||
			!containsPath(handoff.Changes.Untracked, "untracked.txt") {
			t.Fatalf("changed handoff = %#v", handoff)
		}
		_ = fixture
	})

	t.Run("committed head", func(t *testing.T) {
		_, allocation, owner := ownedGitFixture(t)
		if err := os.WriteFile(
			filepath.Join(allocation.ExecutionRoot, "committed.txt"),
			[]byte("commit\n"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		runGitTest(t, allocation.ExecutionRoot, "add", "committed.txt")
		runGitTest(t, allocation.ExecutionRoot, "-c", "user.email=mintclaw@example.invalid",
			"-c", "user.name=MintClaw Test", "commit", "-m", "worker change")

		handoff, err := owner.CaptureHandoff(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if handoff.Class != HandoffChanges || handoff.Dirty || handoff.Head == allocation.BaseRevision ||
			handoff.Ahead != 1 || handoff.Behind != 0 {
			t.Fatalf("committed handoff = %#v", handoff)
		}
	})
}

func TestHandoffSurfacesConflictWithoutResolvingIt(t *testing.T) {
	fixture := newGitFixture(t)
	runGitTest(t, fixture.repository, "switch", "-c", "conflict-side")
	if err := os.WriteFile(filepath.Join(fixture.repository, "README.md"), []byte("side\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, fixture.repository, "commit", "-am", "side")
	runGitTest(t, fixture.repository, "switch", "main")
	if err := os.WriteFile(filepath.Join(fixture.repository, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, fixture.repository, "commit", "-am", "base")
	fixture.head = strings.TrimSpace(runGitTest(t, fixture.repository, "rev-parse", "HEAD"))
	fixture.source = resolveProjectTest(t, fixture.repository)
	allocation, err := fixture.manager.Allocate(
		t.Context(),
		fixture.request("task-conflict", "task-generation", thread.NewThreadID()),
	)
	if err != nil {
		t.Fatal(err)
	}
	ownerRequest := ownerRequestForAllocation(allocation, "worker-conflict")
	owner, err := fixture.manager.AcquireOwner(t.Context(), ownerRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Release() }()
	command := exec.Command("git", "-C", allocation.ExecutionRoot, "merge", "conflict-side")
	command.Env = append(os.Environ(), "LC_ALL=C")
	if output, mergeErr := command.CombinedOutput(); mergeErr == nil {
		t.Fatalf("merge unexpectedly succeeded: %s", output)
	}

	handoff, err := owner.CaptureHandoff(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if handoff.Class != HandoffConflicted || !handoff.Operations.Merge ||
		len(handoff.Changes.Unmerged) == 0 {
		t.Fatalf("conflicted handoff = %#v", handoff)
	}
	if _, statErr := os.Stat(filepath.Join(allocation.Execution.GitDir, "MERGE_HEAD")); statErr != nil {
		t.Fatalf("handoff resolved or aborted the conflict: %v", statErr)
	}
}

func TestHandoffPersistsMissingExecutionAsUncertainAllocation(t *testing.T) {
	fixture, allocation, owner := ownedGitFixture(t)
	runGitTest(t, fixture.repository, "worktree", "remove", allocation.ExecutionRoot)

	handoff, err := owner.CaptureHandoff(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if handoff.Class != HandoffMissing || handoff.Reason == "" {
		t.Fatalf("missing handoff = %#v", handoff)
	}
	record, err := fixture.manager.Load(t.Context(), allocation.WorktreeID)
	if err != nil || record.State != StateUncertain {
		t.Fatalf("uncertain allocation = %#v, %v", record, err)
	}
}

func TestHandoffFailsClosedWhenRepositoryChangesDuringObservation(t *testing.T) {
	fixture, allocation, owner := ownedGitFixture(t)
	original := fixture.manager.runGit
	var once sync.Once
	fixture.manager.runGit = func(ctx context.Context, cwd string, args ...string) (gitOutput, error) {
		if slicesContain(args, "rev-list") {
			once.Do(func() {
				if err := os.WriteFile(
					filepath.Join(allocation.ExecutionRoot, "concurrent.txt"),
					[]byte("changed\n"),
					0o600,
				); err != nil {
					t.Errorf("create concurrent change: %v", err)
				}
			})
		}
		return original(ctx, cwd, args...)
	}

	handoff, err := owner.CaptureHandoff(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if handoff.Class != HandoffUncertain || handoff.Reason != "repository changed during handoff observation" {
		t.Fatalf("unstable handoff = %#v", handoff)
	}
}

func TestHandoffFailsClosedWhenHeadChangesBeforeFinalIdentityProbe(t *testing.T) {
	fixture, allocation, owner := ownedGitFixture(t)
	original := fixture.manager.runGit
	operationsCalls := 0
	fixture.manager.runGit = func(ctx context.Context, cwd string, args ...string) (gitOutput, error) {
		if slicesContain(args, "--git-path") {
			operationsCalls++
			if operationsCalls == 2 {
				if err := os.WriteFile(
					filepath.Join(allocation.ExecutionRoot, "late-commit.txt"),
					[]byte("changed after final status\n"),
					0o600,
				); err != nil {
					return gitOutput{}, err
				}
				if _, err := original(ctx, allocation.ExecutionRoot, "add", "late-commit.txt"); err != nil {
					return gitOutput{}, err
				}
				if _, err := original(
					ctx,
					allocation.ExecutionRoot,
					"-c", "user.email=mintclaw@example.invalid",
					"-c", "user.name=MintClaw Test",
					"commit", "-m", "late commit",
				); err != nil {
					return gitOutput{}, err
				}
			}
		}
		return original(ctx, cwd, args...)
	}

	handoff, err := owner.CaptureHandoff(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if operationsCalls != 2 || handoff.Class != HandoffUncertain ||
		handoff.Reason != "repository changed during handoff observation" {
		t.Fatalf("late-commit handoff = %#v; operations calls = %d", handoff, operationsCalls)
	}
}

func TestLoadHandoffRejectsTamperedRecord(t *testing.T) {
	fixture, allocation, owner := ownedGitFixture(t)
	handoff, err := owner.CaptureHandoff(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fixture.manager.allocationRoot(allocation.WorktreeID), handoffFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	record["reason"] = "tampered after capture"
	data, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.LoadHandoff(t.Context(), allocation.WorktreeID); err == nil ||
		!strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("LoadHandoff(tampered %s) error = %v", handoff.HandoffID, err)
	}
}

func TestLoadHandoffRejectsReplayedValidRecord(t *testing.T) {
	fixture, allocation, owner := ownedGitFixture(t)
	first, err := owner.CaptureHandoff(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fixture.manager.allocationRoot(allocation.WorktreeID), handoffFileName)
	oldRecord, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(allocation.ExecutionRoot, "new-worker-output.txt"),
		[]byte("newer handoff\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	second, err := owner.CaptureHandoff(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if first.HandoffID == second.HandoffID {
		t.Fatalf("successive handoff IDs are equal: %s", first.HandoffID)
	}
	if err := os.WriteFile(path, oldRecord, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := fixture.manager.LoadHandoff(t.Context(), allocation.WorktreeID); !errors.Is(
		err,
		ErrAllocationConflict,
	) {
		t.Fatalf("LoadHandoff(replayed %s over %s) error = %v", first.HandoffID, second.HandoffID, err)
	}
}

func TestHandoffFailureInvalidatesPreviouslyPersistedHandoff(t *testing.T) {
	fixture, allocation, owner := ownedGitFixture(t)
	handoff, err := owner.CaptureHandoff(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fixture.manager.allocationRoot(allocation.WorktreeID), handoffFileName)
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if err := fixture.manager.markHandoffFailure(t.Context(), owner, owner.Allocation()); err != nil {
		t.Fatal(err)
	}

	record, err := fixture.manager.Load(t.Context(), allocation.WorktreeID)
	if err != nil {
		t.Fatal(err)
	}
	if record.State != StateUncertain || record.RetentionReason == "" || record.HandoffID != "" {
		t.Fatalf("quarantined allocation = %#v", record)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("old handoff evidence was not retained: %v", err)
	}
	if _, err := fixture.manager.LoadHandoff(t.Context(), allocation.WorktreeID); !errors.Is(
		err,
		ErrAllocationUncertain,
	) {
		t.Fatalf("LoadHandoff(stale %s) error = %v, want %v", handoff.HandoffID, err, ErrAllocationUncertain)
	}
}

func TestRequireActiveOwnerRejectsReleasedOrDifferentGeneration(t *testing.T) {
	fixture, allocation, owner := ownedGitFixture(t)
	request := ownerRequestForAllocation(allocation, "worker-generation")
	wrong := request
	wrong.WorkerGenerationID = "worker-other"
	if _, err := fixture.manager.RequireActiveOwner(t.Context(), wrong); !errors.Is(err, ErrOwnerInactive) {
		t.Fatalf("different generation error = %v", err)
	}
	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.RequireActiveOwner(t.Context(), request); !errors.Is(err, ErrOwnerInactive) {
		t.Fatalf("released owner error = %v", err)
	}
}

func TestOwnerReleaseWaitsForHandoffPersistence(t *testing.T) {
	fixture, _, owner := ownedGitFixture(t)
	original := fixture.manager.runGit
	entered := make(chan struct{})
	proceed := make(chan struct{})
	var once sync.Once
	fixture.manager.runGit = func(ctx context.Context, cwd string, args ...string) (gitOutput, error) {
		if slicesContain(args, "--git-path") {
			once.Do(func() {
				close(entered)
				<-proceed
			})
		}
		return original(ctx, cwd, args...)
	}
	type captureResult struct {
		handoff Handoff
		err     error
	}
	captured := make(chan captureResult, 1)
	go func() {
		handoff, err := owner.CaptureHandoff(context.Background())
		captured <- captureResult{handoff: handoff, err: err}
	}()
	<-entered
	released := make(chan error, 1)
	go func() { released <- owner.Release() }()
	select {
	case err := <-released:
		t.Fatalf("Release() completed before handoff persistence: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(proceed)
	result := <-captured
	if result.err != nil || result.handoff.Class != HandoffReady {
		t.Fatalf("CaptureHandoff() = %#v, %v", result.handoff, result.err)
	}
	if err := <-released; err != nil {
		t.Fatal(err)
	}
}

func TestOwnerLifecycleQuarantinesHandoffPersistenceFailureBeforeRelease(t *testing.T) {
	fixture, allocation, owner := ownedGitFixture(t)
	request := ownerRequestForAllocation(allocation, "worker-generation")
	lifecycle, err := owner.BeginLifecycle(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	handoffPath := filepath.Join(fixture.manager.allocationRoot(allocation.WorktreeID), handoffFileName)
	if err := os.Mkdir(handoffPath, 0o700); err != nil {
		t.Fatal(err)
	}

	if _, err := lifecycle.Finish(t.Context()); err == nil {
		t.Fatal("Finish() succeeded with an unwritable handoff target")
	}
	record, err := fixture.manager.Load(t.Context(), allocation.WorktreeID)
	if err != nil || record.State != StateUncertain || record.RetentionReason == "" {
		t.Fatalf("quarantined allocation = %#v, %v", record, err)
	}
	if err := owner.Validate(request); err == nil {
		t.Fatal("owner remained usable after durable quarantine and release")
	}
	if contender, err := fixture.manager.AcquireOwner(
		t.Context(),
		ownerRequestForAllocation(allocation, "worker-after-failed-handoff"),
	); !errors.Is(err, ErrAllocationUncertain) {
		if contender != nil {
			_ = contender.Release()
		}
		t.Fatalf("successor after failed handoff error = %v, want %v", err, ErrAllocationUncertain)
	}
}

func TestOwnerLifecycleRetainsLockUntilFailedPersistenceCanBeRetried(t *testing.T) {
	fixture, allocation, owner := ownedGitFixture(t)
	request := ownerRequestForAllocation(allocation, "worker-generation")
	lifecycle, err := owner.BeginLifecycle(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	recordPath := fixture.manager.recordPath(allocation.WorktreeID)
	recordData, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	repairNeeded := false
	t.Cleanup(func() {
		if repairNeeded {
			_ = os.Remove(recordPath)
			_ = os.WriteFile(recordPath, recordData, 0o600)
		}
		_, _ = lifecycle.Finish(context.Background())
	})
	repairNeeded = true
	if err := os.Remove(recordPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(recordPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Finish(t.Context()); !errors.Is(err, ErrFinalizationPending) {
		t.Fatalf("Finish() error = %v, want %v", err, ErrFinalizationPending)
	}
	if err := owner.Validate(request); err != nil {
		t.Fatalf("owner lock was released after total persistence failure: %v", err)
	}
	if err := os.Remove(recordPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordPath, recordData, 0o600); err != nil {
		t.Fatal(err)
	}
	repairNeeded = false
	if contender, err := fixture.manager.AcquireOwner(
		t.Context(),
		ownerRequestForAllocation(allocation, "worker-before-retry"),
	); !errors.Is(err, ErrOwnerBusy) {
		if contender != nil {
			_ = contender.Release()
		}
		t.Fatalf("successor before finalization retry error = %v, want %v", err, ErrOwnerBusy)
	}
	handoff, err := lifecycle.Finish(t.Context())
	if err != nil || handoff.Class != HandoffReady {
		t.Fatalf("Finish(retry) = %#v, %v", handoff, err)
	}
	successor, err := fixture.manager.AcquireOwner(
		t.Context(),
		ownerRequestForAllocation(allocation, "worker-after-retry"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := successor.Release(); err != nil {
		t.Fatal(err)
	}
}

func ownedGitFixture(t *testing.T) (*gitFixture, Allocation, *Owner) {
	t.Helper()
	fixture := newGitFixture(t)
	request := fixture.request("task-handoff", "task-generation", thread.NewThreadID())
	allocation, err := fixture.manager.Allocate(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := fixture.manager.AcquireOwner(
		t.Context(),
		ownerRequestForAllocation(allocation, "worker-generation"),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Release() })
	return fixture, allocation, owner
}

func ownerRequestForAllocation(allocation Allocation, workerGeneration string) OwnerRequest {
	return OwnerRequest{
		WorktreeID: allocation.WorktreeID, TaskID: allocation.TaskID,
		TaskGenerationID: allocation.TaskGenerationID, ThreadID: allocation.ThreadID,
		WorkerGenerationID: workerGeneration,
	}
}

func containsPath(paths []PathChange, wanted string) bool {
	for _, path := range paths {
		if path.Path == wanted {
			return true
		}
	}
	return false
}
