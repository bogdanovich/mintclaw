package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const cleanupReconcileTimeout = 30 * time.Second

// CleanupResult describes only cleanup effects and retained recovery evidence.
// A retained branch is never treated as an error when its commit differs from
// the accepted base; preserving that deliverable is the required outcome.
type CleanupResult struct {
	Allocation      Allocation
	Handoff         Handoff
	WorktreeRemoved bool
	BranchDeleted   bool
	BranchRetained  bool
	Reason          string
}

type registeredWorktree struct {
	path     string
	head     string
	branch   string
	detached bool
	bare     bool
	locked   bool
	prunable bool
}

type cleanupTargetState struct {
	present      bool
	absent       bool
	registration registeredWorktree
	reason       string
}

// Cleanup performs conservative, idempotent cleanup while the exact owner
// operation gate is held. expectedHandoffID proves that the caller references
// the latest durable terminal evidence; Cleanup refreshes that evidence before
// any Git mutation and never uses force or recursive filesystem removal.
func (owner *Owner) Cleanup(
	ctx context.Context,
	expectedHandoffID string,
) (CleanupResult, error) {
	if owner == nil {
		return CleanupResult{}, ErrOwnerInactive
	}
	if ctx == nil {
		return CleanupResult{}, fmt.Errorf("coding worktree: cleanup context is required")
	}
	if len(expectedHandoffID) != 64 || !validObjectID(expectedHandoffID) {
		return CleanupResult{}, fmt.Errorf("coding worktree: valid handoff ID is required")
	}
	owner.operation.Lock()
	defer owner.operation.Unlock()

	allocation, err := owner.cleanupAllocation()
	if err != nil {
		return CleanupResult{}, err
	}
	if allocation.HandoffID != expectedHandoffID {
		return CleanupResult{}, ErrAllocationConflict
	}
	handoff, err := owner.manager.LoadHandoff(ctx, allocation.WorktreeID)
	if err != nil {
		return CleanupResult{}, err
	}
	if handoff.HandoffID != expectedHandoffID {
		return CleanupResult{}, ErrAllocationConflict
	}
	if allocation.State == StateReleased {
		return cleanupResult(allocation, handoff, true, false, false, allocation.RetentionReason), nil
	}
	if sourceErr := owner.manager.validateSourceAuthority(ctx, allocation); sourceErr != nil {
		state := StateUncertain
		if allocation.State == StateCleanupPending {
			state = StateCleanupPending
		}
		return owner.cleanupRefusal(
			context.Background(),
			allocation,
			handoff,
			state,
			"source repository identity is unavailable or changed",
			sourceErr,
		)
	}
	if allocation.State == StateCleanupPending {
		return owner.completePendingCleanup(ctx, allocation, handoff)
	}
	if handoff.Class != HandoffReady {
		return owner.cleanupRefusal(
			ctx,
			allocation,
			handoff,
			retentionStateForHandoff(handoff),
			"latest handoff is not clean and unchanged",
			nil,
		)
	}

	fresh, err := owner.manager.captureHandoff(ctx, owner, allocation)
	if err != nil {
		return CleanupResult{}, err
	}
	allocation = owner.Allocation()
	if fresh.Class != HandoffReady {
		return owner.cleanupRefusal(
			ctx,
			allocation,
			fresh,
			retentionStateForHandoff(fresh),
			"repository changed since cleanup was authorized",
			nil,
		)
	}
	target := owner.manager.inspectCleanupTarget(ctx, allocation)
	if !target.present || target.registration.head != allocation.BaseRevision ||
		target.registration.branch != "refs/heads/"+allocation.Branch {
		reason := target.reason
		if reason == "" {
			reason = "Git worktree registration does not match the accepted base and branch"
		}
		return owner.cleanupRefusal(ctx, allocation, fresh, StateUncertain, reason, nil)
	}
	if target.reason != "" {
		return owner.cleanupRefusal(ctx, allocation, fresh, StateRetained, target.reason, nil)
	}
	if cancellationErr := context.Cause(ctx); cancellationErr != nil {
		return owner.cleanupRefusal(
			context.Background(),
			allocation,
			fresh,
			StateRetained,
			"cleanup was canceled before Git removal",
			cancellationErr,
		)
	}
	hasIgnoredPaths, ignoredErr := owner.manager.hasIgnoredPaths(ctx, allocation)
	if ignoredErr != nil {
		return owner.cleanupRefusal(
			context.Background(),
			allocation,
			fresh,
			StateUncertain,
			"ignored-file evidence is unavailable or incomplete",
			ignoredErr,
		)
	}
	if hasIgnoredPaths {
		return owner.cleanupRefusal(
			ctx,
			allocation,
			fresh,
			StateRetained,
			"worktree contains ignored files",
			nil,
		)
	}
	hasUnsafeIndexFlags, indexFlagsErr := owner.manager.hasUnsafeIndexFlags(ctx, allocation)
	if indexFlagsErr != nil {
		return owner.cleanupRefusal(
			context.Background(),
			allocation,
			fresh,
			StateUncertain,
			"tracked-index flag evidence is unavailable or incomplete",
			indexFlagsErr,
		)
	}
	if hasUnsafeIndexFlags {
		return owner.cleanupRefusal(
			ctx,
			allocation,
			fresh,
			StateRetained,
			"worktree contains tracked paths hidden by index flags",
			nil,
		)
	}
	allocation, err = owner.manager.setCleanupState(
		ctx,
		owner,
		allocation,
		StateCleanupPending,
		fresh.HandoffID,
		"",
	)
	if err != nil {
		return CleanupResult{}, err
	}
	return owner.completePendingCleanup(ctx, allocation, fresh)
}

func (owner *Owner) completePendingCleanup(
	ctx context.Context,
	allocation Allocation,
	handoff Handoff,
) (CleanupResult, error) {
	attempt := owner.manager.removeCleanupTarget(ctx, allocation)
	reconcileCtx, cancel := context.WithTimeout(context.Background(), cleanupReconcileTimeout)
	defer cancel()
	if attempt.removed {
		return owner.manager.finishReleasedCleanup(reconcileCtx, owner, allocation, handoff)
	}
	state := attempt.state
	if !state.valid() {
		state = StateCleanupPending
	}
	reason := attempt.reason
	if reason == "" {
		reason = "claimed cleanup did not prove the worktree absent"
	}
	return owner.cleanupRefusal(reconcileCtx, allocation, handoff, state, reason, attempt.cause)
}

func (manager *Manager) hasIgnoredPaths(ctx context.Context, allocation Allocation) (bool, error) {
	result, err := manager.runGit(
		ctx,
		allocation.ExecutionRoot,
		"ls-files", "--others", "--ignored", "--exclude-standard", "-z",
	)
	if err != nil {
		return false, err
	}
	if result.truncated {
		return false, fmt.Errorf("coding worktree: ignored-file evidence exceeded its bound")
	}
	return result.stdout != "", nil
}

func (manager *Manager) hasUnsafeIndexFlags(ctx context.Context, allocation Allocation) (bool, error) {
	result, err := manager.runGit(ctx, allocation.ExecutionRoot, "ls-files", "-v", "-z", "--")
	if err != nil {
		return false, err
	}
	if result.truncated {
		return false, fmt.Errorf("coding worktree: tracked-index flag evidence exceeded its bound")
	}
	return parseUnsafeIndexFlags(result.stdout)
}

func parseUnsafeIndexFlags(output string) (bool, error) {
	if output == "" {
		return false, nil
	}
	if output[len(output)-1] != 0 {
		return false, fmt.Errorf("coding worktree: incomplete tracked-index flag evidence")
	}
	for _, record := range strings.Split(output[:len(output)-1], "\x00") {
		if len(record) < 3 || record[1] != ' ' {
			return false, fmt.Errorf("coding worktree: malformed tracked-index flag evidence")
		}
		tag := record[0]
		if tag == 'S' || (tag >= 'a' && tag <= 'z') {
			return true, nil
		}
		switch tag {
		case 'H', 'M', 'R', 'C', 'K', '?', 'U':
		default:
			return false, fmt.Errorf("coding worktree: unknown tracked-index flag evidence")
		}
	}
	return false, nil
}

func (owner *Owner) cleanupAllocation() (Allocation, error) {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.released || owner.lock == nil {
		return Allocation{}, ErrOwnerInactive
	}
	switch owner.allocation.State {
	case StateReady, StateRetained, StateCleanupPending, StateReleased:
		return owner.allocation, nil
	default:
		return Allocation{}, ErrAllocationUncertain
	}
}

func (owner *Owner) cleanupRefusal(
	ctx context.Context,
	allocation Allocation,
	handoff Handoff,
	state State,
	reason string,
	cause error,
) (CleanupResult, error) {
	updated, persistErr := owner.manager.setCleanupState(
		ctx,
		owner,
		allocation,
		state,
		handoff.HandoffID,
		reason,
	)
	if persistErr != nil {
		updated = allocation
	}
	result := cleanupResult(updated, handoff, false, false, false, reason)
	return result, errors.Join(ErrCleanupRefused, cause, persistErr)
}

func retentionStateForHandoff(handoff Handoff) State {
	switch handoff.Class {
	case HandoffReady, HandoffChanges, HandoffConflicted:
		return StateRetained
	default:
		return StateUncertain
	}
}

func cleanupResult(
	allocation Allocation,
	handoff Handoff,
	worktreeRemoved bool,
	branchDeleted bool,
	branchRetained bool,
	reason string,
) CleanupResult {
	return CleanupResult{
		Allocation: cloneAllocation(allocation), Handoff: handoff,
		WorktreeRemoved: worktreeRemoved, BranchDeleted: branchDeleted,
		BranchRetained: branchRetained, Reason: reason,
	}
}

func (manager *Manager) setCleanupState(
	ctx context.Context,
	owner *Owner,
	allocation Allocation,
	state State,
	handoffID string,
	reason string,
) (Allocation, error) {
	var updated Allocation
	err := manager.withCatalog(ctx, func() error {
		var updateErr error
		updated, updateErr = manager.setCleanupStateLocked(owner, allocation, state, handoffID, reason)
		return updateErr
	})
	return updated, err
}

func (manager *Manager) setCleanupStateLocked(
	owner *Owner,
	allocation Allocation,
	state State,
	handoffID string,
	reason string,
) (Allocation, error) {
	current, err := manager.currentCleanupAllocationLocked(owner, allocation)
	if err != nil {
		return Allocation{}, err
	}
	current.State = state
	current.HandoffID = handoffID
	current.RetentionReason = reason
	current.UpdatedAt = manager.lifecycleTime(current)
	if err := manager.saveRecord(current); err != nil {
		return Allocation{}, err
	}
	owner.updateAllocation(current)
	return current, nil
}

func (manager *Manager) currentCleanupAllocationLocked(
	owner *Owner,
	allocation Allocation,
) (Allocation, error) {
	if err := owner.validateHeldAllocation(allocation); err != nil {
		return Allocation{}, err
	}
	current, found, err := manager.loadRecord(allocation.WorktreeID)
	if err != nil {
		return Allocation{}, err
	}
	if !found || !sameAllocationIdentity(current, allocation) ||
		current.State != allocation.State || current.HandoffID != allocation.HandoffID {
		return Allocation{}, ErrAllocationConflict
	}
	return current, nil
}

func (manager *Manager) inspectCleanupTarget(ctx context.Context, allocation Allocation) cleanupTargetState {
	registration, found, complete := manager.registeredWorktree(ctx, allocation)
	entry, statErr := os.Lstat(allocation.ExecutionRoot)
	exists := statErr == nil
	missing := errors.Is(statErr, os.ErrNotExist)
	if !complete {
		return cleanupTargetState{reason: "Git worktree catalog is unavailable or ambiguous"}
	}
	if missing && !found {
		return cleanupTargetState{absent: true}
	}
	if !exists || !found {
		return cleanupTargetState{reason: "filesystem and Git worktree catalog disagree"}
	}
	if entry.Mode()&os.ModeSymlink != 0 || !entry.IsDir() {
		return cleanupTargetState{reason: "execution root was replaced or redirected"}
	}
	if err := validateExecutionRootAuthority(allocation); err != nil {
		return cleanupTargetState{reason: "execution root directory was replaced"}
	}
	if registration.detached || registration.bare || registration.prunable {
		return cleanupTargetState{reason: "Git worktree registration is detached, bare, or prunable"}
	}
	if registration.locked {
		return cleanupTargetState{
			present: true, registration: registration,
			reason: "Git worktree registration is locked",
		}
	}
	return cleanupTargetState{present: true, registration: registration}
}

func (manager *Manager) registeredWorktree(
	ctx context.Context,
	allocation Allocation,
) (registeredWorktree, bool, bool) {
	return manager.registeredWorktreeAt(ctx, allocation, allocation.ExecutionRoot)
}

func (manager *Manager) registeredWorktreeAt(
	ctx context.Context,
	allocation Allocation,
	path string,
) (registeredWorktree, bool, bool) {
	records, complete := manager.registeredWorktrees(ctx, allocation)
	if !complete {
		return registeredWorktree{}, false, false
	}
	var matched registeredWorktree
	found := false
	for _, record := range records {
		if record.path != path {
			continue
		}
		if found {
			return registeredWorktree{}, false, false
		}
		matched = record
		found = true
	}
	return matched, found, true
}

func (manager *Manager) registeredWorktrees(
	ctx context.Context,
	allocation Allocation,
) ([]registeredWorktree, bool) {
	result, err := manager.runGit(
		ctx,
		allocation.Source.ProjectRoot,
		"worktree", "list", "--porcelain", "-z",
	)
	if err != nil || result.truncated {
		return nil, false
	}
	records, err := parseRegisteredWorktrees(result.stdout)
	if err != nil {
		return nil, false
	}
	return records, true
}

func parseRegisteredWorktrees(output string) ([]registeredWorktree, error) {
	fields := strings.Split(output, "\x00")
	records := make([]registeredWorktree, 0, len(fields)/4)
	var current registeredWorktree
	haveRecord := false
	seen := make(map[string]bool)
	flush := func() error {
		if !haveRecord {
			return nil
		}
		if !validPath(current.path) || current.head == "" || !validObjectID(current.head) {
			return fmt.Errorf("coding worktree: invalid Git worktree registration")
		}
		records = append(records, current)
		current = registeredWorktree{}
		haveRecord = false
		seen = make(map[string]bool)
		return nil
	}
	for _, field := range fields {
		if field == "" {
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		key, value, hasValue := strings.Cut(field, " ")
		if key == "worktree" {
			if haveRecord {
				return nil, fmt.Errorf("coding worktree: unterminated Git worktree registration")
			}
			haveRecord = true
		}
		if !haveRecord || seen[key] {
			return nil, fmt.Errorf("coding worktree: malformed Git worktree registration")
		}
		seen[key] = true
		switch key {
		case "worktree":
			if !hasValue {
				return nil, fmt.Errorf("coding worktree: missing registered worktree path")
			}
			current.path = filepath.Clean(value)
		case "HEAD":
			if !hasValue {
				return nil, fmt.Errorf("coding worktree: missing registered worktree HEAD")
			}
			current.head = strings.ToLower(value)
		case "branch":
			if !hasValue || !strings.HasPrefix(value, "refs/heads/") {
				return nil, fmt.Errorf("coding worktree: invalid registered worktree branch")
			}
			current.branch = value
		case "detached":
			current.detached = true
		case "bare":
			current.bare = true
		case "locked":
			current.locked = true
		case "prunable":
			current.prunable = true
		default:
			return nil, fmt.Errorf("coding worktree: unknown Git worktree registration field")
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("coding worktree: empty Git worktree catalog")
	}
	return records, nil
}

func (manager *Manager) finishReleasedCleanup(
	ctx context.Context,
	owner *Owner,
	allocation Allocation,
	handoff Handoff,
) (CleanupResult, error) {
	var result CleanupResult
	err := manager.withCatalog(ctx, func() error {
		var finishErr error
		result, finishErr = manager.finishReleasedCleanupLocked(ctx, owner, allocation, handoff)
		return finishErr
	})
	return result, err
}

func (manager *Manager) finishReleasedCleanupLocked(
	ctx context.Context,
	owner *Owner,
	allocation Allocation,
	handoff Handoff,
) (CleanupResult, error) {
	current, err := manager.currentCleanupAllocationLocked(owner, allocation)
	if err != nil {
		return CleanupResult{}, err
	}
	if current.State != StateCleanupPending || current.HandoffID != handoff.HandoffID {
		return CleanupResult{}, ErrAllocationConflict
	}
	allocation = current
	if sourceErr := manager.validateSourceAuthority(ctx, allocation); sourceErr != nil {
		reason := "worktree removal is complete but source repository identity is unavailable or changed"
		updated := allocation
		persisted, persistErr := manager.setCleanupStateLocked(
			owner,
			allocation,
			StateCleanupPending,
			handoff.HandoffID,
			reason,
		)
		if persistErr == nil {
			updated = persisted
		}
		return cleanupResult(updated, handoff, true, false, false, reason),
			errors.Join(ErrCleanupRefused, sourceErr, persistErr)
	}
	branchDeleted := false
	reason := ""
	branchHead, branchFound, branchErr := manager.branchHead(ctx, allocation)
	if branchErr != nil {
		updated, persistErr := manager.setCleanupStateLocked(
			owner,
			allocation,
			StateCleanupPending,
			handoff.HandoffID,
			"worktree removal is complete but branch state is unavailable",
		)
		return cleanupResult(updated, handoff, true, false, false, updated.RetentionReason),
			errors.Join(branchErr, persistErr)
	}
	if branchFound && branchHead == allocation.BaseRevision {
		registrations, complete := manager.registeredWorktrees(ctx, allocation)
		if !complete {
			updated, persistErr := manager.setCleanupStateLocked(
				owner,
				allocation,
				StateCleanupPending,
				handoff.HandoffID,
				"worktree removal is complete but branch attachment state is unavailable",
			)
			return cleanupResult(updated, handoff, true, false, false, updated.RetentionReason),
				errors.Join(ErrCleanupRefused, persistErr)
		}
		for _, registration := range registrations {
			if registration.branch == "refs/heads/"+allocation.Branch {
				reason = "worktree removed; branch retained because another worktree is using it"
				break
			}
		}
	}
	if branchFound && branchHead == allocation.BaseRevision && reason == "" {
		_, deleteErr := manager.runGit(
			ctx,
			allocation.Source.ProjectRoot,
			"-c", "core.hooksPath="+manager.emptyHooksRoot,
			"update-ref", "-d", "refs/heads/"+allocation.Branch, allocation.BaseRevision,
		)
		remainingHead, remaining, inspectErr := manager.branchHead(ctx, allocation)
		switch {
		case inspectErr != nil:
			return CleanupResult{}, errors.Join(deleteErr, inspectErr)
		case !remaining:
			branchDeleted = true
		case remainingHead != allocation.BaseRevision:
			reason = "worktree removed; branch retained because it contains commits"
		default:
			updated, persistErr := manager.setCleanupStateLocked(
				owner,
				allocation,
				StateCleanupPending,
				handoff.HandoffID,
				"worktree removal is complete but unchanged branch deletion did not complete",
			)
			return cleanupResult(updated, handoff, true, false, false, updated.RetentionReason),
				errors.Join(deleteErr, persistErr)
		}
	} else if branchFound {
		if reason == "" {
			reason = "worktree removed; branch retained because it contains commits"
		}
	}
	updated, err := manager.setCleanupStateLocked(
		owner,
		allocation,
		StateReleased,
		handoff.HandoffID,
		reason,
	)
	return cleanupResult(updated, handoff, true, branchDeleted, branchFound && !branchDeleted, reason), err
}
