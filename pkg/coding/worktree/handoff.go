package worktree

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	"github.com/bogdanovich/mintclaw/pkg/coding/workspace"
	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

const (
	HandoffSchemaVersion  = 1
	MaxHandoffRecordBytes = 256 << 10
	MaxHandoffPaths       = 128

	handoffFileName = "handoff.json"
	maxReasonBytes  = 2048
)

// HandoffClass distinguishes a usable result from evidence that requires
// operator inspection. A conflicted result is never resolved by this package.
type HandoffClass string

const (
	HandoffReady      HandoffClass = "ready"
	HandoffChanges    HandoffClass = "changes"
	HandoffConflicted HandoffClass = "conflicted"
	HandoffMissing    HandoffClass = "missing"
	HandoffMismatch   HandoffClass = "mismatch"
	HandoffUncertain  HandoffClass = "uncertain"
)

func (class HandoffClass) valid() bool {
	switch class {
	case HandoffReady, HandoffChanges, HandoffConflicted,
		HandoffMissing, HandoffMismatch, HandoffUncertain:
		return true
	default:
		return false
	}
}

// PathChange is a bounded path-only projection. It intentionally carries no
// file content or diff text.
type PathChange struct {
	Path         string `json:"path"`
	OriginalPath string `json:"original_path,omitempty"`
	Status       string `json:"status"`
}

// HandoffChangeset separates porcelain status classes while retaining Git's
// exact two-byte status for operator-facing evidence.
type HandoffChangeset struct {
	Staged    []PathChange `json:"staged,omitempty"`
	Unstaged  []PathChange `json:"unstaged,omitempty"`
	Untracked []PathChange `json:"untracked,omitempty"`
	Unmerged  []PathChange `json:"unmerged,omitempty"`
	Truncated bool         `json:"truncated,omitempty"`
}

func (changes HandoffChangeset) empty() bool {
	return len(changes.Staged) == 0 && len(changes.Unstaged) == 0 &&
		len(changes.Untracked) == 0 && len(changes.Unmerged) == 0
}

// GitOperations records only the presence of in-progress operation markers.
type GitOperations struct {
	Merge      bool `json:"merge,omitempty"`
	Rebase     bool `json:"rebase,omitempty"`
	CherryPick bool `json:"cherry_pick,omitempty"`
	Revert     bool `json:"revert,omitempty"`
	Bisect     bool `json:"bisect,omitempty"`
	Sequencer  bool `json:"sequencer,omitempty"`
}

func (operations GitOperations) any() bool {
	return operations.Merge || operations.Rebase || operations.CherryPick ||
		operations.Revert || operations.Bisect || operations.Sequencer
}

// Handoff is the durable, passive terminal observation for one owned
// allocation. HandoffID binds cleanup and later reporting to these exact
// observations; it is not an authorization token.
type Handoff struct {
	SchemaVersion         int              `json:"schema_version"`
	HandoffID             string           `json:"handoff_id"`
	WorktreeID            string           `json:"worktree_id"`
	TaskID                string           `json:"task_id"`
	TaskGenerationID      string           `json:"task_generation_id"`
	ThreadID              string           `json:"thread_id"`
	SourceProjectKey      string           `json:"source_project_key"`
	ExecutionProjectKey   string           `json:"execution_project_key,omitempty"`
	ExecutionRoot         string           `json:"execution_root"`
	ExecutionRootIdentity string           `json:"execution_root_identity"`
	Branch                string           `json:"branch"`
	BaseRevision          string           `json:"base_revision"`
	Head                  string           `json:"head,omitempty"`
	Dirty                 bool             `json:"dirty,omitempty"`
	StatusComplete        bool             `json:"status_complete"`
	Changes               HandoffChangeset `json:"changes"`
	Ahead                 int              `json:"ahead,omitempty"`
	Behind                int              `json:"behind,omitempty"`
	ComparisonComplete    bool             `json:"comparison_complete"`
	Operations            GitOperations    `json:"operations"`
	OperationsComplete    bool             `json:"operations_complete"`
	Class                 HandoffClass     `json:"class"`
	Reason                string           `json:"reason,omitempty"`
	CapturedAt            time.Time        `json:"captured_at"`
}

func (handoff Handoff) Validate() error {
	parsedThreadID, threadErr := uuid.Parse(handoff.ThreadID)
	if handoff.SchemaVersion != HandoffSchemaVersion || !handoff.Class.valid() ||
		!validWorktreeID(handoff.WorktreeID) || handoff.WorktreeID != IDForThread(handoff.ThreadID) ||
		threadErr != nil || parsedThreadID.String() != handoff.ThreadID ||
		!validIdentifier(handoff.TaskID) || !validIdentifier(handoff.TaskGenerationID) ||
		!validPath(handoff.ExecutionRoot) ||
		handoff.ExecutionRootIdentity != RootIdentity(handoff.ExecutionRoot) ||
		handoff.Branch != branchName(DefaultBranchPrefix, handoff.WorktreeID) ||
		!validObjectID(handoff.BaseRevision) ||
		(handoff.Head != "" && !validObjectID(handoff.Head)) || handoff.CapturedAt.IsZero() {
		return fmt.Errorf("coding worktree: invalid handoff identity")
	}
	if !validGitProjectKey(handoff.SourceProjectKey) ||
		(handoff.ExecutionProjectKey != "" && !validGitProjectKey(handoff.ExecutionProjectKey)) {
		return fmt.Errorf("coding worktree: invalid handoff project identity")
	}
	if handoff.Ahead < 0 || handoff.Behind < 0 ||
		!utf8.ValidString(handoff.Reason) || len(handoff.Reason) > maxReasonBytes {
		return fmt.Errorf("coding worktree: invalid handoff evidence")
	}
	if err := handoff.Changes.validate(); err != nil {
		return err
	}
	if (handoff.Class == HandoffMissing || handoff.Class == HandoffMismatch ||
		handoff.Class == HandoffUncertain) && strings.TrimSpace(handoff.Reason) == "" {
		return fmt.Errorf("coding worktree: non-usable handoff requires a reason")
	}
	if (handoff.Class == HandoffReady || handoff.Class == HandoffChanges ||
		handoff.Class == HandoffConflicted) &&
		(handoff.ExecutionProjectKey == "" || handoff.ExecutionProjectKey == handoff.SourceProjectKey ||
			handoff.Head == "" || !handoff.StatusComplete || !handoff.ComparisonComplete ||
			!handoff.OperationsComplete) {
		return fmt.Errorf("coding worktree: usable handoff contains incomplete evidence")
	}
	if handoff.Class == HandoffReady &&
		(handoff.Dirty || !handoff.StatusComplete || !handoff.Changes.empty() ||
			handoff.Head != handoff.BaseRevision || handoff.Ahead != 0 || handoff.Behind != 0 ||
			!handoff.ComparisonComplete || handoff.Operations.any() || !handoff.OperationsComplete) {
		return fmt.Errorf("coding worktree: ready handoff contains changes or incomplete evidence")
	}
	if handoff.Class == HandoffConflicted && !handoff.Operations.any() && len(handoff.Changes.Unmerged) == 0 {
		return fmt.Errorf("coding worktree: conflicted handoff lacks conflict evidence")
	}
	if handoff.Class == HandoffChanges && !handoff.Dirty && handoff.Head == handoff.BaseRevision &&
		handoff.Ahead == 0 && handoff.Behind == 0 {
		return fmt.Errorf("coding worktree: changes handoff lacks changed repository evidence")
	}
	wantID, err := handoffDigest(handoff)
	if err != nil {
		return fmt.Errorf("coding worktree: compute handoff digest: %w", err)
	}
	if handoff.HandoffID != wantID {
		return fmt.Errorf("coding worktree: handoff digest mismatch")
	}
	return nil
}

func validGitProjectKey(projectKey string) bool {
	prefix := string(thread.ProjectKindGitWorktree) + ":"
	digest, found := strings.CutPrefix(projectKey, prefix)
	return found && len(digest) == 64 && validObjectID(digest)
}

func (changes HandoffChangeset) validate() error {
	count := 0
	for _, group := range [][]PathChange{changes.Staged, changes.Unstaged, changes.Untracked, changes.Unmerged} {
		count += len(group)
		for _, changed := range group {
			if len(changed.Status) != 2 || !utf8.ValidString(changed.Status) ||
				!validRelativeGitPath(changed.Path) ||
				(changed.OriginalPath != "" && !validRelativeGitPath(changed.OriginalPath)) {
				return fmt.Errorf("coding worktree: invalid handoff changed path")
			}
		}
	}
	if count > MaxHandoffPaths*2 {
		return fmt.Errorf("coding worktree: handoff changed paths exceed bound")
	}
	return nil
}

func validRelativeGitPath(path string) bool {
	return path != "" && utf8.ValidString(path) && len(path) <= maxPathBytes && filepath.IsLocal(path)
}

func (manager *Manager) captureHandoff(ctx context.Context, owner *Owner, allocation Allocation) (Handoff, error) {
	handoff := manager.observeHandoff(ctx, allocation)
	var digestErr error
	handoff.HandoffID, digestErr = handoffDigest(handoff)
	if digestErr != nil {
		return Handoff{}, digestErr
	}
	if validationErr := handoff.Validate(); validationErr != nil {
		return Handoff{}, validationErr
	}
	data, err := json.MarshalIndent(handoff, "", "  ")
	if err != nil {
		return Handoff{}, err
	}
	data = append(data, '\n')
	if len(data) > MaxHandoffRecordBytes {
		return Handoff{}, fmt.Errorf("coding worktree: handoff exceeds %d bytes", MaxHandoffRecordBytes)
	}
	persistErr := manager.withCatalog(ctx, func() error {
		if ownerErr := owner.validateHeldAllocation(allocation); ownerErr != nil {
			return ownerErr
		}
		current, found, loadErr := manager.loadRecord(allocation.WorktreeID)
		if loadErr != nil {
			return loadErr
		}
		if !found || !sameAllocationIdentity(current, allocation) {
			return ErrAllocationConflict
		}
		if err := fileutil.WriteFileAtomic(
			filepath.Join(manager.allocationRoot(allocation.WorktreeID), handoffFileName),
			data,
			0o600,
		); err != nil {
			return err
		}
		if handoff.Class == HandoffMissing || handoff.Class == HandoffMismatch ||
			handoff.Class == HandoffUncertain {
			current.State = StateUncertain
		} else {
			current.State = StateRetained
		}
		current.HandoffID = handoff.HandoffID
		current.RetentionReason = ""
		current.UpdatedAt = manager.lifecycleTime(current)
		if err := manager.saveRecord(current); err != nil {
			return err
		}
		owner.updateAllocation(current)
		return nil
	})
	return handoff, persistErr
}

func (manager *Manager) markHandoffFailure(
	ctx context.Context,
	owner *Owner,
	allocation Allocation,
) error {
	return manager.withCatalog(ctx, func() error {
		if err := owner.validateHeldAllocation(allocation); err != nil {
			return err
		}
		current, found, err := manager.loadRecord(allocation.WorktreeID)
		if err != nil {
			return err
		}
		if !found || !sameAllocationIdentity(current, allocation) {
			return ErrAllocationConflict
		}
		current.State = StateUncertain
		current.HandoffID = ""
		current.RetentionReason = "terminal handoff persistence failed"
		current.UpdatedAt = manager.lifecycleTime(current)
		if err := manager.saveRecord(current); err != nil {
			return err
		}
		owner.updateAllocation(current)
		return nil
	})
}

func (manager *Manager) observeHandoff(ctx context.Context, allocation Allocation) Handoff {
	if ctx == nil {
		ctx = context.Background()
	}
	handoff := Handoff{
		SchemaVersion: HandoffSchemaVersion,
		WorktreeID:    allocation.WorktreeID, TaskID: allocation.TaskID,
		TaskGenerationID: allocation.TaskGenerationID, ThreadID: allocation.ThreadID,
		SourceProjectKey: allocation.Source.ProjectKey,
		ExecutionRoot:    allocation.ExecutionRoot, ExecutionRootIdentity: allocation.ExecutionRootIdentity,
		Branch: allocation.Branch, BaseRevision: allocation.BaseRevision,
		Class: HandoffUncertain, CapturedAt: manager.lifecycleTime(allocation),
	}
	if err := context.Cause(ctx); err != nil {
		handoff.Reason = "handoff observation was canceled"
		return handoff
	}
	if allocation.Execution == nil {
		handoff.Reason = "allocation has no established execution identity"
		return handoff
	}
	execution, err := thread.ResolveProject(ctx, allocation.Execution.InvocationCWD)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || os.IsNotExist(err) {
			handoff.Class = HandoffMissing
			handoff.Reason = "execution root or invocation directory is missing"
			return handoff
		}
		handoff.Reason = "execution identity could not be resolved"
		return handoff
	}
	if !executionMatchesAllocation(execution, allocation) {
		handoff.Class = HandoffMismatch
		handoff.Reason = "execution identity no longer matches the allocation"
		return handoff
	}
	handoff.ExecutionProjectKey = execution.ProjectKey
	repository := workspace.NewRepository(
		execution.ProjectRoot,
		execution.InvocationCWD,
		workspace.Limits{ChangedPaths: MaxHandoffPaths},
	)
	first := repository.Status(ctx).Snapshot
	firstOperations, firstOperationsComplete := manager.observeOperations(ctx, allocation)
	ahead, behind, comparisonComplete := manager.observeAheadBehind(ctx, allocation)
	second := repository.Status(ctx).Snapshot
	secondOperations, secondOperationsComplete := manager.observeOperations(ctx, allocation)
	after, resolveErr := thread.ResolveProject(ctx, allocation.Execution.InvocationCWD)
	if resolveErr != nil {
		handoff.Class = HandoffUncertain
		handoff.Reason = "execution identity became unavailable during handoff observation"
		return handoff
	}
	if !executionMatchesAllocation(after, allocation) {
		handoff.Class = HandoffMismatch
		handoff.Reason = "execution identity changed during handoff observation"
		return handoff
	}
	if !strings.EqualFold(after.GitHead, second.Git.Head) {
		handoff.Class = HandoffUncertain
		handoff.Reason = "repository changed during handoff observation"
		return handoff
	}
	populateHandoffStatus(&handoff, second)
	handoff.Operations = secondOperations
	handoff.OperationsComplete = secondOperationsComplete
	handoff.Ahead = ahead
	handoff.Behind = behind
	handoff.ComparisonComplete = comparisonComplete
	if first.Identity() != second.Identity() || firstOperations != secondOperations ||
		firstOperationsComplete != secondOperationsComplete {
		handoff.Class = HandoffUncertain
		handoff.Reason = "repository changed during handoff observation"
		return handoff
	}
	if !second.Git.Available || !second.Git.StatusAvailable || second.Truncated ||
		!comparisonComplete || !secondOperationsComplete {
		handoff.Class = HandoffUncertain
		handoff.Reason = "repository evidence is incomplete"
		return handoff
	}
	if second.Git.TopLevel != allocation.ExecutionRoot ||
		second.Git.GitDir != allocation.Execution.GitDir ||
		second.Git.CommonDir != allocation.Execution.GitCommonDir ||
		second.Git.Branch != allocation.Branch {
		handoff.Class = HandoffMismatch
		handoff.Reason = "repository evidence does not match the allocation"
		return handoff
	}
	if handoff.Operations.any() || len(handoff.Changes.Unmerged) != 0 {
		handoff.Class = HandoffConflicted
		return handoff
	}
	if handoff.Dirty || handoff.Head != handoff.BaseRevision || handoff.Ahead != 0 || handoff.Behind != 0 {
		handoff.Class = HandoffChanges
		return handoff
	}
	handoff.Class = HandoffReady
	return handoff
}

func populateHandoffStatus(handoff *Handoff, snapshot workspace.Snapshot) {
	handoff.Head = strings.ToLower(snapshot.Git.Head)
	handoff.Dirty = snapshot.Git.Dirty
	handoff.StatusComplete = snapshot.Git.StatusAvailable && !snapshot.Truncated
	handoff.Changes.Truncated = snapshot.Truncated
	for _, changed := range snapshot.ChangedPaths {
		projected := PathChange{
			Path: changed.Path, OriginalPath: changed.OriginalPath, Status: changed.Status,
		}
		if unmergedStatus(changed.Status) {
			handoff.Changes.Unmerged = append(handoff.Changes.Unmerged, projected)
			continue
		}
		if changed.Status == "??" {
			handoff.Changes.Untracked = append(handoff.Changes.Untracked, projected)
			continue
		}
		if len(changed.Status) == 2 && changed.Status[0] != ' ' {
			handoff.Changes.Staged = append(handoff.Changes.Staged, projected)
		}
		if len(changed.Status) == 2 && changed.Status[1] != ' ' {
			handoff.Changes.Unstaged = append(handoff.Changes.Unstaged, projected)
		}
	}
}

func unmergedStatus(status string) bool {
	return len(status) == 2 && (strings.Contains(status, "U") || status == "AA" || status == "DD")
}

func (manager *Manager) observeAheadBehind(ctx context.Context, allocation Allocation) (int, int, bool) {
	result, err := manager.runGit(
		ctx,
		allocation.ExecutionRoot,
		"rev-list", "--left-right", "--count", allocation.BaseRevision+"...HEAD",
	)
	if err != nil || result.truncated {
		return 0, 0, false
	}
	fields := strings.Fields(result.stdout)
	if len(fields) != 2 {
		return 0, 0, false
	}
	behind, behindErr := strconv.Atoi(fields[0])
	ahead, aheadErr := strconv.Atoi(fields[1])
	if behindErr != nil || aheadErr != nil || behind < 0 || ahead < 0 {
		return 0, 0, false
	}
	return ahead, behind, true
}

func (manager *Manager) observeOperations(
	ctx context.Context,
	allocation Allocation,
) (GitOperations, bool) {
	result, err := manager.runGit(
		ctx,
		allocation.ExecutionRoot,
		"rev-parse", "--path-format=absolute",
		"--git-path", "MERGE_HEAD",
		"--git-path", "rebase-merge",
		"--git-path", "rebase-apply",
		"--git-path", "CHERRY_PICK_HEAD",
		"--git-path", "REVERT_HEAD",
		"--git-path", "BISECT_LOG",
		"--git-path", "sequencer",
	)
	if err != nil || result.truncated {
		return GitOperations{}, false
	}
	paths := strings.Split(result.stdout, "\n")
	if len(paths) != 7 {
		return GitOperations{}, false
	}
	present := make([]bool, len(paths))
	for index, path := range paths {
		path = filepath.Clean(path)
		insideGitDir, gitDirErr := pathWithin(path, allocation.Execution.GitDir)
		insideCommonDir, commonDirErr := pathWithin(path, allocation.Execution.GitCommonDir)
		if gitDirErr != nil || commonDirErr != nil || (!insideGitDir && !insideCommonDir) {
			return GitOperations{}, false
		}
		_, statErr := os.Lstat(path)
		switch {
		case statErr == nil:
			present[index] = true
		case errors.Is(statErr, os.ErrNotExist):
		default:
			return GitOperations{}, false
		}
	}
	return GitOperations{
		Merge: present[0], Rebase: present[1] || present[2],
		CherryPick: present[3], Revert: present[4], Bisect: present[5], Sequencer: present[6],
	}, true
}

func executionMatchesAllocation(execution thread.ProjectIdentity, allocation Allocation) bool {
	if allocation.Execution == nil {
		return false
	}
	established := *allocation.Execution
	return execution.Kind == established.Kind && execution.ProjectKey == established.ProjectKey &&
		execution.ProjectRoot == allocation.ExecutionRoot &&
		execution.InvocationCWD == established.InvocationCWD &&
		execution.GitWorktreeRoot == established.GitWorktreeRoot &&
		execution.GitDir == established.GitDir && execution.GitCommonDir == allocation.Source.GitCommonDir &&
		execution.GitOrigin == established.GitOrigin && execution.GitDir != allocation.Source.GitDir &&
		execution.GitBranch == allocation.Branch && execution.GitHead != ""
}

func sameAllocationIdentity(left, right Allocation) bool {
	return left.WorktreeID == right.WorktreeID && left.TaskID == right.TaskID &&
		left.TaskGenerationID == right.TaskGenerationID && left.ThreadID == right.ThreadID &&
		left.Source == right.Source && left.BaseRevision == right.BaseRevision &&
		left.WorktreeParent == right.WorktreeParent && left.ExecutionRoot == right.ExecutionRoot &&
		left.ExecutionRootIdentity == right.ExecutionRootIdentity && left.Branch == right.Branch
}

func handoffMatchesAllocation(handoff Handoff, allocation Allocation) bool {
	return allocation.HandoffID != "" && allocation.HandoffID == handoff.HandoffID &&
		allocation.WorktreeID == handoff.WorktreeID && allocation.TaskID == handoff.TaskID &&
		allocation.TaskGenerationID == handoff.TaskGenerationID && allocation.ThreadID == handoff.ThreadID &&
		allocation.Source.ProjectKey == handoff.SourceProjectKey &&
		allocation.ExecutionRoot == handoff.ExecutionRoot &&
		allocation.ExecutionRootIdentity == handoff.ExecutionRootIdentity &&
		allocation.Branch == handoff.Branch && allocation.BaseRevision == handoff.BaseRevision &&
		allocation.Execution != nil && allocation.Execution.ProjectKey == handoff.ExecutionProjectKey
}

func handoffDigest(handoff Handoff) (string, error) {
	handoff.HandoffID = ""
	data, err := json.Marshal(handoff)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

// LoadHandoff returns the last durable terminal observation without changing
// allocation or ownership state.
func (manager *Manager) LoadHandoff(ctx context.Context, worktreeID string) (Handoff, error) {
	if manager == nil || ctx == nil || !validWorktreeID(worktreeID) {
		return Handoff{}, fmt.Errorf("coding worktree: valid manager, context, and worktree ID are required")
	}
	var handoff Handoff
	err := manager.withCatalog(ctx, func() error {
		path := filepath.Join(manager.allocationRoot(worktreeID), handoffFileName)
		data, err := readBoundedDirectFile(path, "handoff", MaxHandoffRecordBytes)
		if err != nil {
			return err
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&handoff); err != nil {
			return err
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return fmt.Errorf("coding worktree: handoff has trailing JSON content")
		}
		if err := handoff.Validate(); err != nil {
			return err
		}
		if handoff.WorktreeID != worktreeID {
			return fmt.Errorf("coding worktree: handoff path identity mismatch")
		}
		allocation, found, loadErr := manager.loadRecord(worktreeID)
		if loadErr != nil {
			return loadErr
		}
		if !found || !handoffMatchesAllocation(handoff, allocation) {
			if found && allocation.State == StateUncertain && allocation.HandoffID == "" {
				return fmt.Errorf("%w: allocation has no current terminal handoff", ErrAllocationUncertain)
			}
			return fmt.Errorf("%w: handoff is not current for allocation", ErrAllocationConflict)
		}
		return nil
	})
	return handoff, err
}
