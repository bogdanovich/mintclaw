package worktree

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
)

func TestManagerAllocatesIdempotentIsolatedWorktreeFromDirtySource(t *testing.T) {
	fixture := newGitFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.repository, "local.txt"), []byte("uncommitted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := fixture.request("task-1", "generation-1", thread.NewThreadID())

	allocation, err := fixture.manager.Allocate(t.Context(), request)
	if err != nil {
		t.Fatalf("Allocate() error = %v", err)
	}
	if allocation.State != StateReady || allocation.Execution == nil {
		t.Fatalf("Allocate() = %#v", allocation)
	}
	if !allocation.SourceDirty || !allocation.SourceStatusComplete {
		t.Fatalf("source status = dirty %t, complete %t", allocation.SourceDirty, allocation.SourceStatusComplete)
	}
	if allocation.Execution.ProjectRoot != allocation.ExecutionRoot ||
		allocation.Execution.GitCommonDir != request.Source.GitCommonDir ||
		allocation.Execution.GitDir == request.Source.GitDir ||
		allocation.Execution.GitBranch != allocation.Branch ||
		allocation.Execution.GitHead != request.BaseRevision {
		t.Fatalf("execution identity = %#v", allocation.Execution)
	}
	if _, err := os.Stat(filepath.Join(allocation.ExecutionRoot, "local.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source-only file leaked into allocation: %v", err)
	}

	again, err := fixture.manager.Allocate(t.Context(), request)
	if err != nil {
		t.Fatalf("idempotent Allocate() error = %v", err)
	}
	if again.WorktreeID != allocation.WorktreeID || again.ExecutionRoot != allocation.ExecutionRoot {
		t.Fatalf("idempotent allocation changed identity: %#v / %#v", allocation, again)
	}
	worktrees := runGitTest(t, fixture.repository, "worktree", "list", "--porcelain")
	if strings.Count(worktrees, "worktree ") != 2 {
		t.Fatalf("worktree list = %q", worktrees)
	}
}

func TestManagerAllowsDetachedSourceWithExplicitBase(t *testing.T) {
	fixture := newGitFixture(t)
	runGitTest(t, fixture.repository, "checkout", "--detach", fixture.head)
	fixture.source = resolveProjectTest(t, fixture.repository)
	request := fixture.request("task-detached", "generation-1", thread.NewThreadID())

	allocation, err := fixture.manager.Allocate(t.Context(), request)
	if err != nil {
		t.Fatalf("Allocate(detached) error = %v", err)
	}
	if allocation.State != StateReady || allocation.Source.GitBranch != "" {
		t.Fatalf("detached allocation = %#v", allocation)
	}
}

func TestManagerMapsNestedInvocationDirectoryIntoExecutionWorktree(t *testing.T) {
	fixture := newGitFixture(t)
	nested := filepath.Join(fixture.repository, "cmd", "fixture")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, fixture.repository, "add", "cmd/fixture/main.go")
	runGitTest(t, fixture.repository, "commit", "-m", "add nested cwd")
	fixture.head = strings.TrimSpace(runGitTest(t, fixture.repository, "rev-parse", "HEAD"))
	fixture.source = resolveProjectTest(t, nested)

	allocation, err := fixture.manager.Allocate(
		t.Context(),
		fixture.request("task-nested", "generation-1", thread.NewThreadID()),
	)
	wantCWD := filepath.Join(allocation.ExecutionRoot, "cmd", "fixture")
	if err != nil || allocation.Execution == nil || allocation.Execution.InvocationCWD != wantCWD {
		t.Fatalf("Allocate(nested cwd) = %#v, %v; want cwd %q", allocation, err, wantCWD)
	}
}

func TestManagerRejectsInvocationDirectoryAbsentFromBaseBeforeGitEffects(t *testing.T) {
	fixture := newGitFixture(t)
	nested := filepath.Join(fixture.repository, "untracked-directory")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "local.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.source = resolveProjectTest(t, nested)
	request := fixture.request("task-untracked-cwd", "generation-1", thread.NewThreadID())

	allocation, err := fixture.manager.Allocate(t.Context(), request)
	if err == nil || !strings.Contains(err.Error(), "invocation cwd is absent from the base commit") {
		t.Fatalf("Allocate(untracked cwd) = %#v, %v", allocation, err)
	}
	if _, statErr := os.Stat(filepath.Join(
		fixture.manager.WorktreeParent(),
		directoryName(IDForThread(request.ThreadID)),
	)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("rejected allocation created an execution root: %v", statErr)
	}
	if _, loadErr := fixture.manager.Load(
		t.Context(),
		IDForThread(request.ThreadID),
	); !errors.Is(
		loadErr,
		os.ErrNotExist,
	) {
		t.Fatalf("rejected allocation published a record: %v", loadErr)
	}
}

func TestManagerReturnsUncertainWhenGitCompletesWithoutReadyExecutionCWD(t *testing.T) {
	fixture := newGitFixture(t)
	nested := filepath.Join(fixture.repository, "tracked-directory")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "tracked.txt"), []byte("tracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, fixture.repository, "add", "tracked-directory/tracked.txt")
	runGitTest(t, fixture.repository, "commit", "-m", "add tracked cwd")
	fixture.head = strings.TrimSpace(runGitTest(t, fixture.repository, "rev-parse", "HEAD"))
	fixture.source = resolveProjectTest(t, nested)
	request := fixture.request("task-missing-after-git", "generation-1", thread.NewThreadID())
	executionRoot := filepath.Join(
		fixture.manager.WorktreeParent(),
		directoryName(IDForThread(request.ThreadID)),
	)
	original := fixture.manager.runGit
	fixture.manager.runGit = func(ctx context.Context, cwd string, args ...string) (gitOutput, error) {
		result, err := original(ctx, cwd, args...)
		if err == nil && slicesContain(args, "worktree") && slicesContain(args, "add") {
			if renameErr := os.Rename(
				filepath.Join(executionRoot, "tracked-directory"),
				filepath.Join(executionRoot, "tracked-directory-moved"),
			); renameErr != nil {
				return result, renameErr
			}
		}
		return result, err
	}

	allocation, err := fixture.manager.Allocate(t.Context(), request)
	if !errors.Is(err, ErrAllocationUncertain) || allocation.State != StateUncertain ||
		allocation.Execution != nil {
		t.Fatalf("Allocate(missing post-Git cwd) = %#v, %v", allocation, err)
	}
}

func TestManagerIgnoresAmbientGitRepositoryOverrides(t *testing.T) {
	fixture := newGitFixture(t)
	decoy := filepath.Join(fixture.root, "decoy")
	if err := os.Mkdir(decoy, 0o700); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, decoy, "init", "-b", "main")
	t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
	t.Setenv("GIT_WORK_TREE", decoy)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(decoy, ".git", "index"))
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
	t.Setenv("GIT_CONFIG_VALUE_0", filepath.Join(decoy, "hooks"))

	allocation, err := fixture.manager.Allocate(
		t.Context(),
		fixture.request("task-environment", "generation-1", thread.NewThreadID()),
	)
	if err != nil || allocation.State != StateReady ||
		allocation.Execution.GitCommonDir != fixture.source.GitCommonDir {
		t.Fatalf("Allocate(ambient Git) = %#v, %v", allocation, err)
	}
}

func TestManagerSeparatesConcurrentTaskAllocations(t *testing.T) {
	fixture := newGitFixture(t)
	requests := []Request{
		fixture.request("task-a", "generation-1", thread.NewThreadID()),
		fixture.request("task-b", "generation-1", thread.NewThreadID()),
	}
	type result struct {
		allocation Allocation
		err        error
	}
	results := make(chan result, len(requests))
	for _, request := range requests {
		go func() {
			allocation, err := fixture.manager.Allocate(context.Background(), request)
			results <- result{allocation: allocation, err: err}
		}()
	}
	first := <-results
	second := <-results
	for _, current := range []result{first, second} {
		if current.err != nil || current.allocation.State != StateReady {
			t.Fatalf("concurrent allocation = %#v, %v", current.allocation, current.err)
		}
	}
	if first.allocation.WorktreeID == second.allocation.WorktreeID ||
		first.allocation.ExecutionRoot == second.allocation.ExecutionRoot ||
		first.allocation.Branch == second.allocation.Branch {
		t.Fatalf("allocations were not isolated: %#v / %#v", first.allocation, second.allocation)
	}
}

func TestManagerRejectsConflictingIdempotencyIdentity(t *testing.T) {
	fixture := newGitFixture(t)
	threadID := thread.NewThreadID()
	request := fixture.request("task-1", "generation-1", threadID)
	if _, err := fixture.manager.Allocate(t.Context(), request); err != nil {
		t.Fatalf("Allocate() error = %v", err)
	}
	request.TaskID = "task-2"
	if _, err := fixture.manager.Allocate(t.Context(), request); !errors.Is(err, ErrAllocationConflict) {
		t.Fatalf("conflicting Allocate() error = %v", err)
	}
}

func TestManagerAdoptsExactWorktreeAfterLostGitResult(t *testing.T) {
	fixture := newGitFixture(t)
	original := fixture.manager.runGit
	fixture.manager.runGit = func(ctx context.Context, cwd string, args ...string) (gitOutput, error) {
		result, err := original(ctx, cwd, args...)
		if err == nil && slicesContain(args, "worktree") && slicesContain(args, "add") {
			return result, errors.New("simulated lost Git result")
		}
		return result, err
	}

	allocation, err := fixture.manager.Allocate(
		t.Context(),
		fixture.request("task-lost", "generation-1", thread.NewThreadID()),
	)
	if err != nil || allocation.State != StateReady {
		t.Fatalf("Allocate(lost result) = %#v, %v", allocation, err)
	}
	loaded, err := fixture.manager.Load(t.Context(), allocation.WorktreeID)
	if err != nil || loaded.State != StateReady {
		t.Fatalf("Load(adopted) = %#v, %v", loaded, err)
	}
}

func TestManagerRecoversInterruptedPreparationWithoutDuplicateWorktree(t *testing.T) {
	tests := []struct {
		name         string
		createBranch bool
	}{
		{name: "before Git"},
		{name: "after branch", createBranch: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGitFixture(t)
			request := fixture.request("task-recover", "generation-1", thread.NewThreadID())
			original := fixture.manager.runGit
			interrupted := false
			fixture.manager.runGit = func(ctx context.Context, cwd string, args ...string) (gitOutput, error) {
				if !interrupted && slicesContain(args, "worktree") && slicesContain(args, "add") {
					interrupted = true
					if test.createBranch {
						branch := branchName(DefaultBranchPrefix, IDForThread(request.ThreadID))
						if _, err := original(ctx, cwd, "branch", branch, request.BaseRevision); err != nil {
							return gitOutput{}, err
						}
					}
					return gitOutput{}, errors.New("simulated process interruption")
				}
				return original(ctx, cwd, args...)
			}
			first, err := fixture.manager.Allocate(t.Context(), request)
			if err == nil || (first.State != StateReserved && first.State != StatePreparing) {
				t.Fatalf("interrupted Allocate() = %#v, %v", first, err)
			}
			second, err := fixture.manager.Allocate(t.Context(), request)
			if err != nil || second.State != StateReady {
				t.Fatalf("recovered Allocate() = %#v, %v", second, err)
			}
			worktrees := runGitTest(t, fixture.repository, "worktree", "list", "--porcelain")
			if strings.Count(worktrees, "worktree ") != 2 {
				t.Fatalf("worktree list = %q", worktrees)
			}
		})
	}
}

func TestManagerRevalidatesExistingAllocationAfterSourceMoves(t *testing.T) {
	fixture := newGitFixture(t)
	request := fixture.request("task-source-moved", "generation-1", thread.NewThreadID())
	allocation, err := fixture.manager.Allocate(t.Context(), request)
	if err != nil {
		t.Fatalf("Allocate() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixture.repository, "later.txt"), []byte("later\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, fixture.repository, "add", "later.txt")
	runGitTest(t, fixture.repository, "commit", "-m", "move source")

	again, err := fixture.manager.Allocate(t.Context(), request)
	if err != nil || again.State != StateReady || again.ExecutionRoot != allocation.ExecutionRoot {
		t.Fatalf("Allocate(after source move) = %#v, %v", again, err)
	}
}

func TestManagerKeepsLifecycleTimestampMonotonicAcrossClockRollback(t *testing.T) {
	fixture := newGitFixture(t)
	request := fixture.request("task-clock", "generation-1", thread.NewThreadID())
	allocation, err := fixture.manager.Allocate(t.Context(), request)
	if err != nil {
		t.Fatalf("Allocate() error = %v", err)
	}
	fixture.manager.now = func() time.Time { return allocation.CreatedAt.Add(-time.Hour) }

	again, err := fixture.manager.Allocate(t.Context(), request)
	if err != nil {
		t.Fatalf("Allocate(after clock rollback) error = %v", err)
	}
	if again.UpdatedAt.Before(allocation.UpdatedAt) || again.UpdatedAt.Before(again.CreatedAt) {
		t.Fatalf("timestamps moved backward: before=%v after=%v created=%v",
			allocation.UpdatedAt, again.UpdatedAt, again.CreatedAt)
	}
}

func TestManagerMarksReplacedExecutionRootUncertain(t *testing.T) {
	fixture := newGitFixture(t)
	request := fixture.request("task-replaced", "generation-1", thread.NewThreadID())
	allocation, err := fixture.manager.Allocate(t.Context(), request)
	if err != nil {
		t.Fatalf("Allocate() error = %v", err)
	}
	runGitTest(t, fixture.repository, "worktree", "remove", allocation.ExecutionRoot)
	if err := os.Mkdir(allocation.ExecutionRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(allocation.ExecutionRoot, "user.txt"), []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	updated, err := fixture.manager.Allocate(t.Context(), request)
	if !errors.Is(err, ErrAllocationUncertain) || updated.State != StateUncertain {
		t.Fatalf("Allocate(replaced) = %#v, %v", updated, err)
	}
	if data, readErr := os.ReadFile(
		filepath.Join(allocation.ExecutionRoot, "user.txt"),
	); readErr != nil ||
		string(data) != "keep\n" {
		t.Fatalf("replacement was modified: %q, %v", data, readErr)
	}
}

func TestManagerNeverRecreatesAnEstablishedWorktreeAfterExternalRemoval(t *testing.T) {
	fixture := newGitFixture(t)
	request := fixture.request("task-removed", "generation-1", thread.NewThreadID())
	allocation, err := fixture.manager.Allocate(t.Context(), request)
	if err != nil {
		t.Fatalf("Allocate() error = %v", err)
	}
	runGitTest(t, fixture.repository, "worktree", "remove", allocation.ExecutionRoot)
	runGitTest(t, fixture.repository, "branch", "-D", allocation.Branch)

	updated, err := fixture.manager.Allocate(t.Context(), request)
	if !errors.Is(err, ErrAllocationUncertain) || updated.State != StateUncertain || updated.Execution == nil {
		t.Fatalf("Allocate(removed established root) = %#v, %v", updated, err)
	}
	if _, statErr := os.Stat(allocation.ExecutionRoot); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("established worktree was recreated: %v", statErr)
	}
}

func TestManagerRejectsReplacedWorktreeParent(t *testing.T) {
	fixture := newGitFixture(t)
	moved := fixture.manager.WorktreeParent() + "-moved"
	if err := os.Rename(fixture.manager.WorktreeParent(), moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(fixture.manager.WorktreeParent(), 0o700); err != nil {
		t.Fatal(err)
	}
	request := fixture.request("task-parent", "generation-1", thread.NewThreadID())
	if _, err := fixture.manager.Allocate(t.Context(), request); err == nil ||
		!strings.Contains(err.Error(), "worktree parent was replaced") {
		t.Fatalf("Allocate(replaced parent) error = %v", err)
	}
}

func TestOpenManagerDoesNotInitializeMissingStore(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	parent := filepath.Join(root, "executions")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenManager(Config{StateRoot: state, WorktreeParent: parent}); err == nil {
		t.Fatal("OpenManager initialized a missing allocation store")
	}
	if _, err := os.Stat(filepath.Join(state, storeDirectory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenManager created store state: %v", err)
	}
}

func TestManagerRejectsUnsafeSourceAndBase(t *testing.T) {
	fixture := newGitFixture(t)
	request := fixture.request("task-invalid", "generation-1", thread.NewThreadID())
	request.BaseRevision = strings.Repeat("0", len(fixture.head))
	if _, err := fixture.manager.Allocate(t.Context(), request); err == nil ||
		!strings.Contains(err.Error(), "validate base commit") {
		t.Fatalf("invalid base error = %v", err)
	}

	overlapping, err := NewManager(Config{
		StateRoot:      filepath.Join(fixture.root, "other-state"),
		WorktreeParent: filepath.Join(fixture.repository, "nested-worktrees"),
	})
	if err != nil {
		t.Fatalf("NewManager(overlap) error = %v", err)
	}
	request = fixture.request("task-overlap", "generation-1", thread.NewThreadID())
	if _, err := overlapping.Allocate(t.Context(), request); err == nil ||
		!strings.Contains(err.Error(), "overlaps source Git authority") {
		t.Fatalf("overlapping parent error = %v", err)
	}
}

func TestOwnerLeaseExcludesConcurrentAndAllowsSuccessorGeneration(t *testing.T) {
	fixture := newGitFixture(t)
	request := fixture.request("task-owner", "task-generation", thread.NewThreadID())
	allocation, err := fixture.manager.Allocate(t.Context(), request)
	if err != nil {
		t.Fatalf("Allocate() error = %v", err)
	}
	firstRequest := OwnerRequest{
		WorktreeID: allocation.WorktreeID, TaskID: request.TaskID,
		TaskGenerationID: request.TaskGenerationID, ThreadID: request.ThreadID,
		WorkerGenerationID: "worker-1",
	}
	first, err := fixture.manager.AcquireOwner(t.Context(), firstRequest)
	if err != nil {
		t.Fatalf("AcquireOwner() error = %v", err)
	}
	if err := first.Validate(firstRequest); err != nil {
		t.Fatalf("Validate(owner) error = %v", err)
	}
	inspection, err := fixture.manager.InspectOwner(t.Context(), allocation.WorktreeID)
	if err != nil || !inspection.Busy || inspection.Record == nil || !inspection.Record.matches(firstRequest) {
		t.Fatalf("InspectOwner() = %#v, %v", inspection, err)
	}

	secondRequest := firstRequest
	secondRequest.WorkerGenerationID = "worker-2"
	if _, err := fixture.manager.AcquireOwner(t.Context(), secondRequest); !errors.Is(err, ErrOwnerBusy) {
		t.Fatalf("concurrent AcquireOwner() error = %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := first.Validate(firstRequest); err == nil {
		t.Fatal("released owner still validated")
	}
	second, err := fixture.manager.AcquireOwner(t.Context(), secondRequest)
	if err != nil {
		t.Fatalf("AcquireOwner(successor) error = %v", err)
	}
	if second.Record().WorkerGenerationID != "worker-2" {
		t.Fatalf("successor record = %#v", second.Record())
	}
	if err := second.Release(); err != nil {
		t.Fatalf("Release(successor) error = %v", err)
	}
}

func TestOwnerInspectionAndRequirementDoNotCreateMissingLock(t *testing.T) {
	fixture := newGitFixture(t)
	request := fixture.request("task-passive-owner", "task-generation", thread.NewThreadID())
	allocation, err := fixture.manager.Allocate(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	ownerRequest := OwnerRequest{
		WorktreeID: allocation.WorktreeID, TaskID: request.TaskID,
		TaskGenerationID: request.TaskGenerationID, ThreadID: request.ThreadID,
		WorkerGenerationID: "worker-passive",
	}
	if _, err := fixture.manager.RequireActiveOwner(t.Context(), ownerRequest); !errors.Is(
		err,
		ErrOwnerInactive,
	) {
		t.Fatalf("RequireActiveOwner() error = %v, want %v", err, ErrOwnerInactive)
	}
	inspection, err := fixture.manager.InspectOwner(t.Context(), allocation.WorktreeID)
	if err != nil || inspection.Busy || inspection.Record != nil {
		t.Fatalf("InspectOwner() = %#v, %v", inspection, err)
	}
	if _, err := os.Lstat(fixture.manager.ownerPath(allocation.WorktreeID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("passive owner check created a lock file: %v", err)
	}
}

func TestActiveOwnerRequirementCannotAuthenticateStaleRecordDuringSuccessorAcquisition(t *testing.T) {
	fixture := newGitFixture(t)
	request := fixture.request("task-owner-transition", "task-generation", thread.NewThreadID())
	allocation, err := fixture.manager.Allocate(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	oldRequest := ownerRequestForAllocation(allocation, "worker-old")
	oldOwner, err := fixture.manager.AcquireOwner(t.Context(), oldRequest)
	if err != nil {
		t.Fatal(err)
	}
	oldRecord := oldOwner.Record()
	if err := oldOwner.Release(); err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	proceed := make(chan struct{})
	takeoverDone := make(chan error, 1)
	var successorLock *lockedFile
	go func() {
		takeoverDone <- fixture.manager.withCatalog(context.Background(), func() error {
			var lockErr error
			successorLock, lockErr = acquireExistingLockedFile(
				context.Background(),
				fixture.manager.ownerPath(allocation.WorktreeID),
				false,
			)
			if lockErr != nil {
				return lockErr
			}
			close(entered)
			<-proceed
			oldRecord.WorkerGenerationID = "worker-successor"
			oldRecord.AcquiredAt = oldRecord.AcquiredAt.Add(time.Second)
			return writeOwnerRecord(successorLock.file, oldRecord)
		})
	}()
	select {
	case <-entered:
	case err := <-takeoverDone:
		t.Fatalf("successor lock acquisition failed: %v", err)
	}

	validationDone := make(chan error, 1)
	go func() {
		_, requireErr := fixture.manager.RequireActiveOwner(context.Background(), oldRequest)
		validationDone <- requireErr
	}()
	select {
	case err := <-validationDone:
		close(proceed)
		<-takeoverDone
		_ = successorLock.Close()
		t.Fatalf("stale owner validation crossed successor publication: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(proceed)
	if err := <-takeoverDone; err != nil {
		_ = successorLock.Close()
		t.Fatal(err)
	}
	defer func() { _ = successorLock.Close() }()
	if err := <-validationDone; !errors.Is(err, ErrOwnerInactive) {
		t.Fatalf("stale owner validation error = %v, want %v", err, ErrOwnerInactive)
	}
	successorRequest := oldRequest
	successorRequest.WorkerGenerationID = "worker-successor"
	if _, err := fixture.manager.RequireActiveOwner(t.Context(), successorRequest); err != nil {
		t.Fatalf("successor owner validation error = %v", err)
	}
}

func TestActiveOwnerRequirementRejectsDifferentManagerParent(t *testing.T) {
	fixture := newGitFixture(t)
	request := fixture.request("task-parent-owner", "task-generation", thread.NewThreadID())
	allocation, err := fixture.manager.Allocate(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	ownerRequest := OwnerRequest{
		WorktreeID: allocation.WorktreeID, TaskID: request.TaskID,
		TaskGenerationID: request.TaskGenerationID, ThreadID: request.ThreadID,
		WorkerGenerationID: "worker-parent-owner",
	}
	owner, err := fixture.manager.AcquireOwner(t.Context(), ownerRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Release() }()
	otherParent := filepath.Join(fixture.root, "other-executions")
	if err := os.Mkdir(otherParent, 0o700); err != nil {
		t.Fatal(err)
	}
	other, err := OpenManager(Config{StateRoot: fixture.manager.StateRoot(), WorktreeParent: otherParent})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.RequireActiveOwner(t.Context(), ownerRequest); !errors.Is(err, ErrAllocationConflict) {
		t.Fatalf("different parent owner error = %v, want %v", err, ErrAllocationConflict)
	}
}

func TestOwnerLeaseExcludesAnotherProcess(t *testing.T) {
	fixture := newGitFixture(t)
	request := fixture.request("task-process", "task-generation", thread.NewThreadID())
	allocation, err := fixture.manager.Allocate(t.Context(), request)
	if err != nil {
		t.Fatalf("Allocate() error = %v", err)
	}
	ownerRequest := OwnerRequest{
		WorktreeID: allocation.WorktreeID, TaskID: request.TaskID,
		TaskGenerationID: request.TaskGenerationID, ThreadID: request.ThreadID,
		WorkerGenerationID: "worker-parent",
	}
	owner, err := fixture.manager.AcquireOwner(t.Context(), ownerRequest)
	if err != nil {
		t.Fatalf("AcquireOwner() error = %v", err)
	}
	defer func() { _ = owner.Release() }()

	command := exec.Command(os.Args[0], "-test.run=^TestOwnerLeaseProcessHelper$")
	command.Env = append(
		os.Environ(),
		"MINTCLAW_WORKTREE_HELPER=1",
		"MINTCLAW_WORKTREE_STATE="+fixture.manager.StateRoot(),
		"MINTCLAW_WORKTREE_PARENT="+fixture.manager.WorktreeParent(),
		"MINTCLAW_WORKTREE_ID="+allocation.WorktreeID,
		"MINTCLAW_WORKTREE_TASK="+request.TaskID,
		"MINTCLAW_WORKTREE_TASK_GENERATION="+request.TaskGenerationID,
		"MINTCLAW_WORKTREE_THREAD="+request.ThreadID,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("owner helper: %v\n%s", err, output)
	}
}

func TestOwnerLeaseProcessHelper(t *testing.T) {
	if os.Getenv("MINTCLAW_WORKTREE_HELPER") != "1" {
		return
	}
	manager, err := NewManager(Config{
		StateRoot:      os.Getenv("MINTCLAW_WORKTREE_STATE"),
		WorktreeParent: os.Getenv("MINTCLAW_WORKTREE_PARENT"),
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	_, err = manager.AcquireOwner(t.Context(), OwnerRequest{
		WorktreeID:         os.Getenv("MINTCLAW_WORKTREE_ID"),
		TaskID:             os.Getenv("MINTCLAW_WORKTREE_TASK"),
		TaskGenerationID:   os.Getenv("MINTCLAW_WORKTREE_TASK_GENERATION"),
		ThreadID:           os.Getenv("MINTCLAW_WORKTREE_THREAD"),
		WorkerGenerationID: "worker-child",
	})
	if !errors.Is(err, ErrOwnerBusy) {
		t.Fatalf("AcquireOwner(child) error = %v", err)
	}
}

type gitFixture struct {
	root       string
	repository string
	head       string
	source     thread.ProjectIdentity
	manager    *Manager
}

func newGitFixture(t *testing.T) *gitFixture {
	t.Helper()
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "init", "-b", "main")
	runGitTest(t, repository, "config", "user.email", "mintclaw@example.invalid")
	runGitTest(t, repository, "config", "user.name", "MintClaw Test")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "add", "README.md")
	runGitTest(t, repository, "commit", "-m", "fixture")
	head := strings.TrimSpace(runGitTest(t, repository, "rev-parse", "HEAD"))
	manager, err := NewManager(Config{
		StateRoot:      filepath.Join(root, "state"),
		WorktreeParent: filepath.Join(root, "execution"),
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	return &gitFixture{
		root: root, repository: repository, head: head,
		source: resolveProjectTest(t, repository), manager: manager,
	}
}

func (fixture *gitFixture) request(taskID, generationID, threadID string) Request {
	return Request{
		TaskID: taskID, TaskGenerationID: generationID, ThreadID: threadID,
		Source: fixture.source, BaseRevision: fixture.head,
	}
}

func resolveProjectTest(t *testing.T, path string) thread.ProjectIdentity {
	t.Helper()
	project, err := thread.ResolveProject(t.Context(), path)
	if err != nil {
		t.Fatalf("ResolveProject(%q) error = %v", path, err)
	}
	return project
}

func runGitTest(t *testing.T, cwd string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", cwd}, args...)...)
	command.Env = append(os.Environ(), "LC_ALL=C")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func slicesContain(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
