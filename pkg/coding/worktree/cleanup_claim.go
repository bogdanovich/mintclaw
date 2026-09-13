package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
)

type cleanupRootRenamer func(*os.Root, string, string) error

type cleanupClaim struct {
	path string
	root *os.Root
	info os.FileInfo
}

type cleanupRemovalAttempt struct {
	removed bool
	state   State
	reason  string
	cause   error
}

func (manager *Manager) removeCleanupTarget(ctx context.Context, allocation Allocation) cleanupRemovalAttempt {
	attempt := cleanupRemovalAttempt{state: StateCleanupPending}
	lockErr := manager.withCatalog(ctx, func() error {
		if err := manager.validateRoots(); err != nil {
			attempt.state = StateUncertain
			attempt.reason = "cleanup roots are unavailable or changed"
			attempt.cause = err
			return nil
		}
		if err := manager.validateSourceAuthority(ctx, allocation); err != nil {
			attempt.reason = "source repository identity is unavailable or changed during cleanup"
			attempt.cause = err
			return nil
		}
		claim, absent, err := manager.openOrClaimCleanupRoot(ctx, allocation)
		if err != nil {
			attempt.state = StateUncertain
			attempt.reason = "cleanup could not atomically bind the allocated execution root"
			attempt.cause = err
			return nil
		}
		if absent {
			attempt.removed = true
			return nil
		}
		defer func() { _ = claim.root.Close() }()

		if _, repairErr := manager.runGit(
			ctx,
			allocation.Source.ProjectRoot,
			"worktree",
			"repair",
			claim.path,
		); repairErr != nil {
			attempt.reason = "claimed worktree registration could not be repaired"
			attempt.cause = repairErr
			return nil
		}
		if sourceErr := manager.validateSourceAuthority(ctx, allocation); sourceErr != nil {
			attempt.reason = "source repository identity changed after cleanup claim"
			attempt.cause = sourceErr
			return nil
		}
		registration, valid := manager.claimedRegistration(ctx, allocation, claim.path)
		if !valid || registration.head != allocation.BaseRevision ||
			registration.branch != "refs/heads/"+allocation.Branch || registration.detached ||
			registration.bare || registration.locked || registration.prunable {
			attempt.state = StateUncertain
			attempt.reason = "Git registration does not prove the claimed worktree authority"
			return nil
		}
		relocated, err := relocatedCleanupAllocation(ctx, allocation, claim.path)
		if err != nil {
			attempt.state = StateUncertain
			attempt.reason = "claimed worktree project identity is unavailable or changed"
			attempt.cause = err
			return nil
		}
		observed := manager.observeHandoff(ctx, relocated)
		if observed.Class != HandoffReady {
			attempt.reason = "claimed worktree is not clean and unchanged"
			return nil
		}
		if ignored, err := manager.hasIgnoredPaths(ctx, relocated); err != nil || ignored {
			attempt.reason = "claimed worktree has ignored paths or incomplete ignored-file evidence"
			attempt.cause = err
			return nil
		}
		if flagged, err := manager.hasUnsafeIndexFlags(ctx, relocated); err != nil || flagged {
			attempt.reason = "claimed worktree has unsafe index flags or incomplete index evidence"
			attempt.cause = err
			return nil
		}
		if err := claim.validate(allocation); err != nil {
			attempt.state = StateUncertain
			attempt.reason = "claimed execution root changed before Git removal"
			attempt.cause = err
			return nil
		}
		_, commandErr := manager.runGit(
			ctx,
			allocation.Source.ProjectRoot,
			"-c", "core.hooksPath="+manager.emptyHooksRoot,
			"-c", "submodule.recurse=false",
			"worktree", "remove", "--", claim.path,
		)
		reconcileCtx, cancel := context.WithTimeout(context.Background(), cleanupReconcileTimeout)
		defer cancel()
		if manager.cleanupPlacementAbsent(reconcileCtx, allocation, claim.path) {
			attempt.removed = true
			return nil
		}
		attempt.reason = "normal Git removal left the claimed worktree present or ambiguous"
		attempt.cause = commandErr
		if err := claim.validate(allocation); err != nil {
			attempt.state = StateUncertain
			attempt.cause = errors.Join(commandErr, err)
		}
		return nil
	})
	if lockErr != nil {
		attempt.cause = errors.Join(attempt.cause, lockErr)
		if attempt.reason == "" {
			attempt.reason = "cleanup catalog coordination failed"
		}
	}
	return attempt
}

func (manager *Manager) openOrClaimCleanupRoot(
	ctx context.Context,
	allocation Allocation,
) (*cleanupClaim, bool, error) {
	claimPath := cleanupClaimPath(allocation)
	originalInfo, originalErr := os.Lstat(allocation.ExecutionRoot)
	claimInfo, claimErr := os.Lstat(claimPath)
	originalPresent := originalErr == nil
	claimPresent := claimErr == nil
	if originalErr != nil && !errors.Is(originalErr, os.ErrNotExist) {
		return nil, false, originalErr
	}
	if claimErr != nil && !errors.Is(claimErr, os.ErrNotExist) {
		return nil, false, claimErr
	}
	if originalPresent && (!originalInfo.IsDir() || originalInfo.Mode()&os.ModeSymlink != 0) {
		return nil, false, fmt.Errorf("execution root was replaced or redirected")
	}
	if claimPresent && (!claimInfo.IsDir() || claimInfo.Mode()&os.ModeSymlink != 0) {
		return nil, false, fmt.Errorf("cleanup claim was replaced or redirected")
	}
	if originalPresent && claimPresent {
		return nil, false, fmt.Errorf("original and claimed cleanup roots both exist")
	}
	if !originalPresent && !claimPresent {
		if manager.cleanupPlacementAbsent(ctx, allocation, claimPath) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("filesystem and Git worktree catalog disagree")
	}
	if claimPresent {
		claim, err := openCleanupClaim(allocation, claimPath)
		return claim, false, err
	}
	registration, found, complete := manager.registeredWorktreeAt(ctx, allocation, allocation.ExecutionRoot)
	if !complete || !found || registration.head != allocation.BaseRevision ||
		registration.branch != "refs/heads/"+allocation.Branch || registration.detached ||
		registration.bare || registration.locked || registration.prunable {
		return nil, false, fmt.Errorf("git registration does not match the execution root")
	}
	return manager.claimExecutionRoot(allocation, claimPath)
}

func (manager *Manager) claimExecutionRoot(
	allocation Allocation,
	claimPath string,
) (*cleanupClaim, bool, error) {
	claim, err := openCleanupClaim(allocation, allocation.ExecutionRoot)
	if err != nil {
		return nil, false, err
	}
	parent, err := os.OpenRoot(allocation.WorktreeParent)
	if err != nil {
		_ = claim.root.Close()
		return nil, false, err
	}
	renameErr := manager.renameCleanup(
		parent,
		filepath.Base(allocation.ExecutionRoot),
		filepath.Base(claimPath),
	)
	closeErr := parent.Close()
	if err := errors.Join(renameErr, closeErr); err != nil {
		_ = claim.root.Close()
		return nil, false, err
	}
	claim.path = claimPath
	if err := claim.validate(allocation); err != nil {
		_ = claim.root.Close()
		return nil, false, err
	}
	return claim, false, nil
}

func openCleanupClaim(allocation Allocation, path string) (*cleanupClaim, error) {
	if identity, err := inspectDirectoryIdentity(path); err != nil {
		return nil, err
	} else if identity != allocation.ExecutionRootFileIdentity {
		return nil, fmt.Errorf("cleanup root directory was replaced")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	info, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	claim := &cleanupClaim{path: path, root: root, info: info}
	if err := claim.validate(allocation); err != nil {
		_ = root.Close()
		return nil, err
	}
	return claim, nil
}

func (claim *cleanupClaim) validate(allocation Allocation) error {
	if claim == nil || claim.root == nil {
		return fmt.Errorf("cleanup claim is unavailable")
	}
	held, err := claim.root.Stat(".")
	if err != nil {
		return err
	}
	current, err := os.Lstat(claim.path)
	if err != nil {
		return err
	}
	if !held.IsDir() || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(claim.info, held) || !os.SameFile(held, current) {
		return fmt.Errorf("cleanup claim no longer names the held directory")
	}
	identity, err := inspectDirectoryIdentity(claim.path)
	if err != nil {
		return err
	}
	if identity != allocation.ExecutionRootFileIdentity {
		return fmt.Errorf("cleanup claim directory was replaced")
	}
	return nil
}

func cleanupClaimPath(allocation Allocation) string {
	return allocation.ExecutionRoot + ".cleanup"
}

func relocatedCleanupAllocation(
	ctx context.Context,
	allocation Allocation,
	claimPath string,
) (Allocation, error) {
	if allocation.Execution == nil {
		return Allocation{}, fmt.Errorf("allocation has no execution identity")
	}
	relative, err := filepath.Rel(allocation.ExecutionRoot, allocation.Execution.InvocationCWD)
	if err != nil {
		return Allocation{}, fmt.Errorf("map claimed invocation directory: %w", err)
	}
	if relative != "." && !filepath.IsLocal(relative) {
		return Allocation{}, fmt.Errorf("map claimed invocation directory: path escapes execution root")
	}
	execution, err := thread.ResolveProject(ctx, filepath.Join(claimPath, relative))
	if err != nil {
		return Allocation{}, err
	}
	relocated := cloneAllocation(allocation)
	relocated.ExecutionRoot = claimPath
	relocated.ExecutionRootIdentity = RootIdentity(claimPath)
	relocated.Execution = &execution
	return relocated, nil
}

func (manager *Manager) claimedRegistration(
	ctx context.Context,
	allocation Allocation,
	claimPath string,
) (registeredWorktree, bool) {
	records, complete := manager.registeredWorktrees(ctx, allocation)
	if !complete {
		return registeredWorktree{}, false
	}
	var matched registeredWorktree
	count := 0
	for _, record := range records {
		if record.path == allocation.ExecutionRoot {
			return registeredWorktree{}, false
		}
		if record.path == claimPath {
			matched = record
			count++
		}
	}
	return matched, count == 1
}

func (manager *Manager) cleanupPlacementAbsent(
	ctx context.Context,
	allocation Allocation,
	claimPath string,
) bool {
	for _, path := range []string{allocation.ExecutionRoot, claimPath} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return false
		}
	}
	records, complete := manager.registeredWorktrees(ctx, allocation)
	if !complete {
		return false
	}
	for _, record := range records {
		if record.path == allocation.ExecutionRoot || record.path == claimPath {
			return false
		}
	}
	return true
}
