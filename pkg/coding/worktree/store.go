package worktree

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

const (
	storeDirectory       = "worktrees"
	allocationsDirectory = "allocations"
	allocationFileName   = "allocation.json"
	ownerFileName        = "owner.lock"
	catalogFileName      = "catalog.lock"
	emptyHooksDirectory  = "empty-hooks"
)

// Config fixes the two independently owned roots used by one allocation
// manager. StateRoot is MintClaw-owned; WorktreeParent is operator-selected.
type Config struct {
	StateRoot      string
	WorktreeParent string
}

// Manager owns durable allocation records and the only Git-mutating creation
// command in P7.3. It is safe for concurrent use.
type Manager struct {
	stateRoot       string
	storeRoot       string
	allocationsRoot string
	worktreeParent  string
	emptyHooksRoot  string
	stateInfo       os.FileInfo
	storeInfo       os.FileInfo
	allocationsInfo os.FileInfo
	hooksInfo       os.FileInfo
	parentInfo      os.FileInfo
	now             func() time.Time
	runGit          gitRunner
	renameCleanup   cleanupRootRenamer
}

func NewManager(config Config) (*Manager, error) {
	return newManager(config, true)
}

// OpenManager opens an already initialized allocation store without creating
// paths. Workers use it so an unauthenticated binding cannot publish state.
func OpenManager(config Config) (*Manager, error) {
	return newManager(config, false)
}

func newManager(config Config, create bool) (*Manager, error) {
	stateRoot, stateInfo, err := canonicalDirectory(config.StateRoot, create)
	if err != nil {
		return nil, fmt.Errorf("coding worktree: state root: %w", err)
	}
	worktreeParent, parentInfo, err := canonicalDirectory(config.WorktreeParent, create)
	if err != nil {
		return nil, fmt.Errorf("coding worktree: worktree parent: %w", err)
	}
	overlaps, err := pathsOverlap(stateRoot, worktreeParent)
	if err != nil {
		return nil, fmt.Errorf("coding worktree: compare configured roots: %w", err)
	}
	if overlaps {
		return nil, fmt.Errorf("coding worktree: worktree parent must be separate from durable state")
	}
	storeRoot := filepath.Join(stateRoot, storeDirectory)
	allocationsRoot := filepath.Join(storeRoot, allocationsDirectory)
	emptyHooksRoot := filepath.Join(storeRoot, emptyHooksDirectory)
	for _, path := range []string{storeRoot, allocationsRoot, emptyHooksRoot} {
		if create {
			if mkdirErr := os.MkdirAll(path, 0o700); mkdirErr != nil {
				return nil, fmt.Errorf("coding worktree: create state directory: %w", mkdirErr)
			}
		}
		if validationErr := requireDirectDirectory(path); validationErr != nil {
			return nil, validationErr
		}
	}
	storeInfo, err := os.Stat(storeRoot)
	if err != nil {
		return nil, err
	}
	allocationsInfo, err := os.Stat(allocationsRoot)
	if err != nil {
		return nil, err
	}
	hooksInfo, err := os.Stat(emptyHooksRoot)
	if err != nil {
		return nil, err
	}
	return &Manager{
		stateRoot: stateRoot, storeRoot: storeRoot, allocationsRoot: allocationsRoot,
		worktreeParent: worktreeParent, emptyHooksRoot: emptyHooksRoot,
		stateInfo: stateInfo, storeInfo: storeInfo, allocationsInfo: allocationsInfo,
		hooksInfo: hooksInfo, parentInfo: parentInfo, now: time.Now, runGit: runGit,
		renameCleanup: renameCleanupRootNoReplace,
	}, nil
}

func (manager *Manager) StateRoot() string {
	if manager == nil {
		return ""
	}
	return manager.stateRoot
}

func (manager *Manager) WorktreeParent() string {
	if manager == nil {
		return ""
	}
	return manager.worktreeParent
}

// Allocate idempotently creates and revalidates one isolated linked worktree.
// It holds the catalog gate across Git creation so two MintClaw processes
// cannot race the same derived root or branch.
func (manager *Manager) Allocate(ctx context.Context, request Request) (Allocation, error) {
	if manager == nil || manager.runGit == nil || manager.now == nil || manager.renameCleanup == nil {
		return Allocation{}, fmt.Errorf("coding worktree: manager is unavailable")
	}
	if ctx == nil {
		return Allocation{}, fmt.Errorf("coding worktree: context is required")
	}
	if err := request.validate(); err != nil {
		return Allocation{}, err
	}
	var result Allocation
	err := manager.withCatalog(ctx, func() error {
		if validationErr := manager.validateRoots(); validationErr != nil {
			return validationErr
		}
		worktreeID := IDForThread(request.ThreadID)
		existing, found, err := manager.loadRecord(worktreeID)
		if err != nil {
			return err
		}
		if found {
			if !existing.matches(request, manager.worktreeParent) {
				return ErrAllocationConflict
			}
			result, err = manager.ensureReady(ctx, existing)
			return err
		}
		currentSource, err := thread.ResolveProject(ctx, request.Source.InvocationCWD)
		if err != nil {
			return fmt.Errorf("coding worktree: resolve source: %w", err)
		}
		if currentSource != request.Source {
			return fmt.Errorf("coding worktree: source project identity changed before allocation")
		}
		sourceCommonDir, err := inspectDirectoryIdentity(request.Source.GitCommonDir)
		if err != nil {
			return fmt.Errorf("coding worktree: inspect source common directory identity: %w", err)
		}
		if validationErr := manager.validateSourceSeparation(request.Source); validationErr != nil {
			return validationErr
		}

		root := filepath.Join(manager.worktreeParent, directoryName(worktreeID))
		if _, statErr := os.Lstat(root); statErr == nil {
			return fmt.Errorf("coding worktree: derived execution root already exists")
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("coding worktree: inspect execution root: %w", statErr)
		}
		branch := branchName(DefaultBranchPrefix, worktreeID)
		if validationErr := manager.validateGitInputs(
			ctx,
			request.Source,
			branch,
			request.BaseRevision,
		); validationErr != nil {
			return validationErr
		}
		dirty, complete := manager.sourceDirty(ctx, request.Source.ProjectRoot)
		now := manager.now().UTC()
		result = Allocation{
			SchemaVersion: SchemaVersion,
			WorktreeID:    worktreeID, TaskID: request.TaskID,
			TaskGenerationID: request.TaskGenerationID, ThreadID: request.ThreadID,
			Source: request.Source, SourceCommonDirIdentity: sourceCommonDir,
			BaseRevision:   strings.ToLower(request.BaseRevision),
			WorktreeParent: manager.worktreeParent, ExecutionRoot: root,
			ExecutionRootIdentity: RootIdentity(root), Branch: branch,
			SourceDirty: dirty, SourceStatusComplete: complete,
			State: StateReserved, CreatedAt: now, UpdatedAt: now,
		}
		if saveErr := manager.saveRecord(result); saveErr != nil {
			return saveErr
		}
		result, err = manager.ensureReady(ctx, result)
		return err
	})
	return result, err
}

// Load reads one durable record without claiming ownership or changing it.
func (manager *Manager) Load(ctx context.Context, worktreeID string) (Allocation, error) {
	if manager == nil {
		return Allocation{}, fmt.Errorf("coding worktree: manager is unavailable")
	}
	if ctx == nil {
		return Allocation{}, fmt.Errorf("coding worktree: context is required")
	}
	if !validWorktreeID(worktreeID) {
		return Allocation{}, fmt.Errorf("coding worktree: invalid worktree ID")
	}
	var allocation Allocation
	err := manager.withCatalog(ctx, func() error {
		var found bool
		var err error
		allocation, found, err = manager.loadRecord(worktreeID)
		if err != nil {
			return err
		}
		if !found {
			return os.ErrNotExist
		}
		return nil
	})
	return allocation, err
}

func (manager *Manager) reconcile(ctx context.Context, allocation Allocation) (Allocation, error) {
	if allocation.State == StateReleased {
		return allocation, nil
	}
	if allocation.State == StateUncertain && allocation.RetentionReason != "" {
		return allocation, fmt.Errorf(
			"%w: durable recovery evidence requires explicit inspection",
			ErrAllocationUncertain,
		)
	}
	if err := manager.validateRoots(); err != nil {
		return allocation, fmt.Errorf("%w: configured root identity changed: %w", ErrAllocationUncertain, err)
	}
	if err := manager.validateSourceAuthority(ctx, allocation); err != nil {
		return manager.markUncertain(allocation, "source repository identity changed")
	}
	relativeCWD, err := filepath.Rel(allocation.Source.ProjectRoot, allocation.Source.InvocationCWD)
	if err != nil || (relativeCWD != "." && !filepath.IsLocal(relativeCWD)) {
		return manager.markUncertain(allocation, "source invocation cwd is not local to its project")
	}
	executionCWD := filepath.Join(allocation.ExecutionRoot, relativeCWD)
	execution, err := thread.ResolveProject(ctx, executionCWD)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || os.IsNotExist(err) {
			branchHead, branchFound, branchErr := manager.branchHead(ctx, allocation)
			if branchErr != nil {
				return manager.markUncertain(allocation, "execution root is missing and branch state is unavailable")
			}
			if !branchFound {
				if allocation.Execution == nil &&
					(allocation.State == StateReserved || allocation.State == StatePreparing) {
					allocation.State = StateReserved
					allocation.UpdatedAt = manager.lifecycleTime(allocation)
					return allocation, manager.saveRecord(allocation)
				}
				return manager.markUncertain(allocation, "established execution root and branch are missing")
			}
			if branchHead == allocation.BaseRevision &&
				allocation.Execution == nil && allocation.State == StatePreparing {
				allocation.State = StatePreparing
				allocation.UpdatedAt = manager.lifecycleTime(allocation)
				return allocation, manager.saveRecord(allocation)
			}
			return manager.markUncertain(allocation, "execution root is missing but its branch still exists")
		}
		return manager.markUncertain(allocation, "execution root could not be resolved")
	}
	if allocation.Execution == nil && allocation.State == StateReserved {
		return manager.markUncertain(allocation, "execution root appeared before preparation")
	}
	initialIdentity := allocation.Execution == nil
	if execution.Kind != thread.ProjectKindGitWorktree ||
		execution.ProjectRoot != allocation.ExecutionRoot ||
		execution.InvocationCWD != executionCWD ||
		execution.GitCommonDir != allocation.Source.GitCommonDir ||
		execution.GitOrigin != allocation.Source.GitOrigin ||
		execution.GitDir == allocation.Source.GitDir ||
		execution.GitBranch != allocation.Branch ||
		execution.GitHead == "" ||
		(initialIdentity && execution.GitHead != allocation.BaseRevision) ||
		(!initialIdentity && execution.GitDir != allocation.Execution.GitDir) {
		return manager.markUncertain(allocation, "execution root identity does not match allocation")
	}
	executionRootFileIdentity, err := inspectDirectoryIdentity(allocation.ExecutionRoot)
	if err != nil {
		return manager.markUncertain(allocation, "execution root filesystem identity is unavailable")
	}
	if initialIdentity {
		allocation.ExecutionRootFileIdentity = executionRootFileIdentity
	} else if allocation.ExecutionRootFileIdentity != executionRootFileIdentity {
		return manager.markUncertain(allocation, "execution root directory was replaced")
	}
	allocation.Execution = &execution
	allocation.State = StateReady
	allocation.UpdatedAt = manager.lifecycleTime(allocation)
	if err := manager.saveRecord(allocation); err != nil {
		return allocation, err
	}
	return allocation, nil
}

func (manager *Manager) ensureReady(ctx context.Context, allocation Allocation) (Allocation, error) {
	reconciled, err := manager.reconcile(ctx, allocation)
	if err != nil {
		return reconciled, err
	}
	if reconciled.State == StateReady || reconciled.State == StateReleased {
		return reconciled, nil
	}
	if reconciled.State != StateReserved && reconciled.State != StatePreparing {
		return manager.markUncertain(reconciled, "allocation is not recoverable for creation")
	}
	createBranch := reconciled.State == StateReserved
	reconciled.State = StatePreparing
	reconciled.UpdatedAt = manager.lifecycleTime(reconciled)
	if err := manager.saveRecord(reconciled); err != nil {
		return reconciled, err
	}
	arguments := []string{
		"-c", "core.hooksPath=" + manager.emptyHooksRoot,
		"-c", "core.fsmonitor=false",
		"-c", "submodule.recurse=false",
		"worktree", "add",
	}
	if createBranch {
		arguments = append(
			arguments,
			"--no-track", "-b", reconciled.Branch,
			reconciled.ExecutionRoot, reconciled.BaseRevision,
		)
	} else {
		arguments = append(arguments, reconciled.ExecutionRoot, reconciled.Branch)
	}
	_, commandErr := manager.runGit(ctx, reconciled.Source.ProjectRoot, arguments...)
	reconciled, reconcileErr := manager.reconcile(ctx, reconciled)
	if reconciled.State == StateReady && reconcileErr == nil {
		return reconciled, nil
	}
	if commandErr != nil || reconcileErr != nil {
		return reconciled, errors.Join(commandErr, reconcileErr)
	}
	return manager.markUncertain(reconciled, "Git command completed without a ready execution root")
}

func (manager *Manager) branchHead(
	ctx context.Context,
	allocation Allocation,
) (string, bool, error) {
	result, err := manager.runGit(
		ctx,
		allocation.Source.ProjectRoot,
		"rev-parse", "--verify", "refs/heads/"+allocation.Branch+"^{commit}",
	)
	if err == nil {
		if !validObjectID(result.stdout) {
			return "", false, fmt.Errorf("coding worktree: branch resolved to an invalid object ID")
		}
		return result.stdout, true, nil
	}
	if exitCode, ok := gitExitCode(err); ok && exitCode == 128 {
		return "", false, nil
	}
	return "", false, err
}

func (manager *Manager) markUncertain(allocation Allocation, reason string) (Allocation, error) {
	allocation.State = StateUncertain
	allocation.RetentionReason = reason
	allocation.UpdatedAt = manager.lifecycleTime(allocation)
	if err := manager.saveRecord(allocation); err != nil {
		return allocation, errors.Join(ErrAllocationUncertain, err)
	}
	return allocation, fmt.Errorf("%w: %s", ErrAllocationUncertain, reason)
}

func (manager *Manager) validateGitInputs(
	ctx context.Context,
	source thread.ProjectIdentity,
	branch string,
	base string,
) error {
	if _, err := manager.runGit(ctx, source.ProjectRoot, "check-ref-format", "--branch", branch); err != nil {
		return fmt.Errorf("coding worktree: validate branch: %w", err)
	}
	if _, err := manager.runGit(
		ctx,
		source.ProjectRoot,
		"cat-file",
		"-e",
		strings.ToLower(base)+"^{commit}",
	); err != nil {
		return fmt.Errorf("coding worktree: validate base commit: %w", err)
	}
	if err := manager.validateBaseInvocationCWD(ctx, source, strings.ToLower(base)); err != nil {
		return err
	}
	_, err := manager.runGit(ctx, source.ProjectRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err == nil {
		return fmt.Errorf("coding worktree: derived branch already exists")
	}
	if exitCode, ok := gitExitCode(err); !ok || exitCode != 1 {
		return fmt.Errorf("coding worktree: inspect derived branch: %w", err)
	}
	return nil
}

func (manager *Manager) validateBaseInvocationCWD(
	ctx context.Context,
	source thread.ProjectIdentity,
	base string,
) error {
	relative, err := filepath.Rel(source.ProjectRoot, source.InvocationCWD)
	if err != nil || (relative != "." && !filepath.IsLocal(relative)) {
		return fmt.Errorf("coding worktree: source invocation cwd is not local to its project")
	}
	if relative == "." {
		return nil
	}
	result, err := manager.runGit(
		ctx,
		source.ProjectRoot,
		"cat-file", "-t", base+":"+filepath.ToSlash(relative),
	)
	if err != nil {
		return fmt.Errorf("coding worktree: invocation cwd is absent from the base commit: %w", err)
	}
	if result.stdout != "tree" {
		return fmt.Errorf("coding worktree: invocation cwd is not a directory in the base commit")
	}
	return nil
}

func (manager *Manager) lifecycleTime(allocation Allocation) time.Time {
	now := manager.now().UTC()
	if now.Before(allocation.UpdatedAt) {
		return allocation.UpdatedAt
	}
	if now.Before(allocation.CreatedAt) {
		return allocation.CreatedAt
	}
	return now
}

func (manager *Manager) sourceDirty(ctx context.Context, root string) (bool, bool) {
	result, err := manager.runGit(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=normal")
	if err != nil {
		return false, false
	}
	return result.stdout != "", !result.truncated
}

func (manager *Manager) validateSourceSeparation(source thread.ProjectIdentity) error {
	for _, managed := range []struct {
		label string
		root  string
	}{
		{label: "worktree parent", root: manager.worktreeParent},
		{label: "durable state", root: manager.stateRoot},
	} {
		for _, protected := range []string{source.ProjectRoot, source.GitCommonDir} {
			overlaps, err := pathsOverlap(managed.root, protected)
			if err != nil {
				return err
			}
			if overlaps {
				return fmt.Errorf("coding worktree: %s overlaps source Git authority", managed.label)
			}
		}
	}
	return nil
}

func (manager *Manager) withCatalog(ctx context.Context, operation func() error) error {
	if err := manager.validateRoots(); err != nil {
		return err
	}
	lock, err := acquireLockedFile(ctx, filepath.Join(manager.storeRoot, catalogFileName), true)
	if err != nil {
		return fmt.Errorf("coding worktree: acquire catalog lock: %w", err)
	}
	if err := manager.validateRoots(); err != nil {
		return errors.Join(err, lock.Close())
	}
	return errors.Join(operation(), lock.Close())
}

func (manager *Manager) loadRecord(worktreeID string) (Allocation, bool, error) {
	if !validWorktreeID(worktreeID) {
		return Allocation{}, false, fmt.Errorf("coding worktree: invalid worktree ID")
	}
	path := manager.recordPath(worktreeID)
	data, err := readBoundedDirectFile(path, "allocation record", MaxRecordBytes)
	if errors.Is(err, os.ErrNotExist) {
		return Allocation{}, false, nil
	}
	if err != nil {
		return Allocation{}, false, err
	}
	var allocation Allocation
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&allocation); err != nil {
		return Allocation{}, false, fmt.Errorf("coding worktree: decode allocation: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Allocation{}, false, fmt.Errorf("coding worktree: allocation has trailing JSON content")
	}
	if err := allocation.Validate(); err != nil {
		return Allocation{}, false, err
	}
	if allocation.WorktreeID != worktreeID {
		return Allocation{}, false, fmt.Errorf("coding worktree: allocation path identity mismatch")
	}
	return allocation, true, nil
}

func (manager *Manager) saveRecord(allocation Allocation) error {
	if err := allocation.Validate(); err != nil {
		return err
	}
	directory := manager.allocationRoot(allocation.WorktreeID)
	if err := os.Mkdir(directory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := requireDirectDirectory(directory); err != nil {
		return err
	}
	data, err := json.MarshalIndent(allocation, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > MaxRecordBytes {
		return fmt.Errorf("coding worktree: allocation record exceeds %d bytes", MaxRecordBytes)
	}
	return fileutil.WriteFileAtomic(filepath.Join(directory, allocationFileName), data, 0o600)
}

func (manager *Manager) allocationRoot(worktreeID string) string {
	return filepath.Join(manager.allocationsRoot, worktreeID)
}

func (manager *Manager) recordPath(worktreeID string) string {
	return filepath.Join(manager.allocationRoot(worktreeID), allocationFileName)
}

func (manager *Manager) ownerPath(worktreeID string) string {
	return filepath.Join(manager.allocationRoot(worktreeID), ownerFileName)
}

func (manager *Manager) validateRoots() error {
	for _, root := range []struct {
		label string
		path  string
		info  os.FileInfo
	}{
		{label: "durable state root", path: manager.stateRoot, info: manager.stateInfo},
		{label: "worktree store", path: manager.storeRoot, info: manager.storeInfo},
		{label: "allocation store", path: manager.allocationsRoot, info: manager.allocationsInfo},
		{label: "empty hooks root", path: manager.emptyHooksRoot, info: manager.hooksInfo},
		{label: "worktree parent", path: manager.worktreeParent, info: manager.parentInfo},
	} {
		current, err := os.Stat(root.path)
		if err != nil || !os.SameFile(current, root.info) {
			return fmt.Errorf("coding worktree: %s was replaced: %w", root.label, err)
		}
	}
	return nil
}

func canonicalDirectory(path string, create bool) (string, os.FileInfo, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil, fmt.Errorf("path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", nil, err
	}
	absolute = filepath.Clean(absolute)
	if create {
		if mkdirErr := os.MkdirAll(absolute, 0o700); mkdirErr != nil {
			return "", nil, mkdirErr
		}
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", nil, err
	}
	resolved = filepath.Clean(resolved)
	info, err := os.Stat(resolved)
	if err != nil {
		return "", nil, err
	}
	if !info.IsDir() {
		return "", nil, fmt.Errorf("path is not a directory")
	}
	if filepath.Dir(resolved) == resolved {
		return "", nil, fmt.Errorf("filesystem root is not an allowed coding worktree directory")
	}
	return resolved, info, nil
}

func requireDirectDirectory(path string) error {
	entry, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if entry.Mode()&os.ModeSymlink != 0 || !entry.IsDir() {
		return fmt.Errorf("coding worktree: state path is not a direct directory")
	}
	return nil
}

func pathsOverlap(first, second string) (bool, error) {
	firstInSecond, err := pathWithin(first, second)
	if err != nil {
		return false, err
	}
	secondInFirst, err := pathWithin(second, first)
	return firstInSecond || secondInFirst, err
}

func pathWithin(candidate, root string) (bool, error) {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false, err
	}
	return relative == "." || filepath.IsLocal(relative), nil
}

func validWorktreeID(worktreeID string) bool {
	if !strings.HasPrefix(worktreeID, "wt-") || len(worktreeID) != 35 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(worktreeID, "wt-"))
	return err == nil
}
