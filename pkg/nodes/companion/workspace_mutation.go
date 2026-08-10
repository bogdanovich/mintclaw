package companion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/patchformat"
)

type WorkspaceWriteOptions struct {
	Path           string
	Content        string
	Overwrite      bool
	ExpectedSHA256 string
}

type WorkspaceWriteResult struct {
	Path   string `json:"path"`
	Action string `json:"action"`
	Size   int    `json:"size"`
	SHA256 string `json:"sha256"`
}

type WorkspacePatchOptions struct {
	Input string
}

type WorkspacePatchEntry struct {
	Path   string `json:"path"`
	Action string `json:"action"`
	Size   int    `json:"size,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

type WorkspacePatchResult struct {
	State     string                `json:"state"`
	Committed []WorkspacePatchEntry `json:"committed"`
	Code      string                `json:"code,omitempty"`
}

type preparedWorkspaceMutation struct {
	mutation patchformat.Mutation
	path     string
	parent   *resolvedParent
	stage    *stagedFile
	before   *resolvedFile
	digest   [sha256.Size]byte
}

func (runtime *FileTransferRuntime) WriteWorkspace(
	ctx context.Context,
	profileRevision string,
	workspaceRoot string,
	options WorkspaceWriteOptions,
) (WorkspaceWriteResult, error) {
	profile, path, err := runtime.workspaceMutationPath(profileRevision, workspaceRoot, options.Path)
	if err != nil || !utf8.ValidString(options.Content) || strings.ContainsRune(options.Content, 0) ||
		len(
			options.Content,
		) > nodes.MaxWorkspaceWriteBytes || int64(len(options.Content)) > profile.profile.MaxFileBytes {
		return WorkspaceWriteResult{}, ErrFileAccessDenied
	}
	publication := filePublicationCreate
	if options.Overwrite {
		publication = filePublicationReplace
		if !profile.profile.AllowOverwrite {
			return WorkspaceWriteResult{}, ErrFileAccessDenied
		}
	} else if !profile.profile.AllowCreate || options.ExpectedSHA256 != "" {
		return WorkspaceWriteResult{}, ErrFileAccessDenied
	}
	parent, err := profile.resolveWritableParent(path)
	if err != nil {
		return WorkspaceWriteResult{}, err
	}
	defer func() { _ = parent.close() }()
	var before *resolvedFile
	var beforeDigest [sha256.Size]byte
	if options.Overwrite {
		before, err = parent.openFinalRegular()
		if err != nil {
			return WorkspaceWriteResult{}, err
		}
		defer func() { _ = before.file.Close() }()
		if before.info.Size() < 0 || before.info.Size() > profile.profile.MaxFileBytes {
			return WorkspaceWriteResult{}, ErrFileAccessDenied
		}
		beforeDigest, err = hashOpenedFile(ctx, before.file)
		if err != nil {
			return WorkspaceWriteResult{}, confirmedWorkspaceMutationCancellation(ctx, err)
		}
		if options.ExpectedSHA256 != "" &&
			!strings.EqualFold(options.ExpectedSHA256, hex.EncodeToString(beforeDigest[:])) {
			return WorkspaceWriteResult{}, ErrFileConflict
		}
	}
	stage, err := stageWorkspaceContent(ctx, parent, []byte(options.Content))
	if err != nil {
		return WorkspaceWriteResult{}, confirmedWorkspaceMutationCancellation(ctx, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = parent.removeStage(stage.identity, stage.name)
		}
		_ = stage.file.Close()
	}()
	if before != nil {
		if err := verifyWorkspaceFile(ctx, parent, before.identity, before.info.Size(), beforeDigest); err != nil {
			return WorkspaceWriteResult{}, confirmedWorkspaceMutationCancellation(ctx, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return WorkspaceWriteResult{}, stoppedWorkspaceMutation(ctx)
	}
	var publishErr error
	if before != nil {
		publishErr = stage.publishReplacing(ctx, before.identity, before.info.Size(), beforeDigest)
	} else {
		publishErr = stage.publish(publication)
	}
	if publishErr != nil {
		var uncertain *committedFileMutationError
		if errors.As(publishErr, &uncertain) {
			return WorkspaceWriteResult{}, fmt.Errorf("%w: %w", ErrInvocationOutcomeUnknown, publishErr)
		}
		return WorkspaceWriteResult{}, confirmedWorkspaceMutationCancellation(ctx, publishErr)
	}
	committed = true
	digest := sha256.Sum256([]byte(options.Content))
	action := "create"
	if options.Overwrite {
		action = "replace"
	}
	return WorkspaceWriteResult{
		Path: options.Path, Action: action, Size: len(options.Content), SHA256: hex.EncodeToString(digest[:]),
	}, nil
}

func (runtime *FileTransferRuntime) PatchWorkspace(
	ctx context.Context,
	profileRevision string,
	workspaceRoot string,
	options WorkspacePatchOptions,
) (WorkspacePatchResult, error) {
	if len(options.Input) == 0 || len(options.Input) > nodes.MaxWorkspacePatchBytes ||
		!utf8.ValidString(options.Input) {
		return WorkspacePatchResult{}, ErrFileAccessDenied
	}
	operations, err := patchformat.Parse(options.Input, nodes.MaxWorkspacePatchFiles)
	if err != nil {
		return WorkspacePatchResult{}, ErrFileAccessDenied
	}
	prepared, err := runtime.prepareWorkspacePatch(ctx, profileRevision, workspaceRoot, operations)
	if err != nil {
		return WorkspacePatchResult{}, err
	}
	defer closePreparedWorkspaceMutations(prepared)
	slices.SortFunc(prepared, func(left, right *preparedWorkspaceMutation) int {
		return strings.Compare(left.mutation.Path, right.mutation.Path)
	})
	return publishPreparedWorkspacePatch(ctx, prepared)
}

func publishPreparedWorkspacePatch(
	ctx context.Context,
	prepared []*preparedWorkspaceMutation,
) (WorkspacePatchResult, error) {
	result := WorkspacePatchResult{State: "completed", Committed: make([]WorkspacePatchEntry, 0, len(prepared))}
	for _, mutation := range prepared {
		if err := ctx.Err(); err != nil {
			return stoppedWorkspacePatch(result, ctx)
		}
		if mutation.before != nil {
			if err := verifyWorkspaceFile(
				ctx,
				mutation.parent,
				mutation.before.identity,
				mutation.before.info.Size(),
				mutation.digest,
			); err != nil {
				if ctx.Err() != nil {
					return stoppedWorkspacePatch(result, ctx)
				}
				return workspacePatchFailure(result, err)
			}
		}
		if err := ctx.Err(); err != nil {
			return stoppedWorkspacePatch(result, ctx)
		}
		if err := publishWorkspaceMutation(ctx, mutation); err != nil {
			var uncertain *committedFileMutationError
			if errors.As(err, &uncertain) {
				return WorkspacePatchResult{}, fmt.Errorf("%w: %w", ErrInvocationOutcomeUnknown, err)
			}
			if ctx.Err() != nil {
				return stoppedWorkspacePatch(result, ctx)
			}
			return workspacePatchFailure(result, err)
		}
		entry := WorkspacePatchEntry{Path: mutation.mutation.Path, Action: mutation.mutation.Action}
		if mutation.mutation.Action != string(patchformat.Delete) {
			digest := sha256.Sum256(mutation.mutation.After)
			entry.Size, entry.SHA256 = len(mutation.mutation.After), hex.EncodeToString(digest[:])
		}
		result.Committed = append(result.Committed, entry)
	}
	return result, nil
}

func (runtime *FileTransferRuntime) prepareWorkspacePatch(
	ctx context.Context,
	profileRevision string,
	workspaceRoot string,
	operations []patchformat.Operation,
) ([]*preparedWorkspaceMutation, error) {
	seen := make(map[string]struct{}, len(operations))
	prepared := make([]*preparedWorkspaceMutation, 0, len(operations))
	totalBytes := 0
	for _, operation := range operations {
		if _, duplicate := seen[operation.Path]; duplicate {
			closePreparedWorkspaceMutations(prepared)
			return nil, ErrFileConflict
		}
		seen[operation.Path] = struct{}{}
		profile, path, err := runtime.workspaceMutationPath(profileRevision, workspaceRoot, operation.Path)
		if err != nil {
			closePreparedWorkspaceMutations(prepared)
			return nil, err
		}
		item, err := prepareWorkspaceMutation(ctx, profile, path, operation)
		if err != nil {
			closePreparedWorkspaceMutations(prepared)
			return nil, confirmedWorkspaceMutationCancellation(ctx, err)
		}
		prepared = append(prepared, item)
		totalBytes += max(len(item.mutation.Before), len(item.mutation.After))
		if totalBytes > nodes.MaxWorkspacePatchTotal {
			closePreparedWorkspaceMutations(prepared)
			return nil, ErrFileAccessDenied
		}
	}
	return prepared, nil
}

func prepareWorkspaceMutation(
	ctx context.Context,
	profile *fileProfileRuntime,
	path string,
	operation patchformat.Operation,
) (*preparedWorkspaceMutation, error) {
	if err := ctx.Err(); err != nil {
		return nil, stoppedWorkspaceMutation(ctx)
	}
	// P2 has no separate delete grant: add consumes allow_create, while both
	// update and delete consume the existing allow_overwrite authority.
	if operation.Kind == patchformat.Add && !profile.profile.AllowCreate ||
		operation.Kind != patchformat.Add && !profile.profile.AllowOverwrite {
		return nil, ErrFileAccessDenied
	}
	parent, err := profile.resolveWritableParent(path)
	if err != nil {
		return nil, err
	}
	item := &preparedWorkspaceMutation{path: path, parent: parent}
	var before []byte
	exists := false
	if operation.Kind == patchformat.Add {
		if absentErr := parent.ensureFinalAbsent(); absentErr != nil {
			_ = parent.close()
			return nil, absentErr
		}
	} else {
		item.before, err = parent.openFinalRegular()
		if err != nil {
			_ = parent.close()
			return nil, err
		}
		before, err = io.ReadAll(io.LimitReader(item.before.file, profile.profile.MaxFileBytes+1))
		if err != nil || int64(len(before)) > profile.profile.MaxFileBytes || !utf8.Valid(before) ||
			strings.ContainsRune(string(before), 0) {
			closePreparedWorkspaceMutation(item)
			return nil, ErrFileAccessDenied
		}
		item.digest = sha256.Sum256(before)
		exists = true
	}
	item.mutation, err = patchformat.Prepare(operation, before, exists)
	if err != nil || len(item.mutation.After) > nodes.MaxWorkspaceWriteBytes ||
		int64(len(item.mutation.After)) > profile.profile.MaxFileBytes || !utf8.Valid(item.mutation.After) ||
		strings.ContainsRune(string(item.mutation.After), 0) {
		closePreparedWorkspaceMutation(item)
		return nil, ErrFileAccessDenied
	}
	if operation.Kind != patchformat.Delete {
		item.stage, err = stageWorkspaceContent(ctx, parent, item.mutation.After)
		if err != nil {
			closePreparedWorkspaceMutation(item)
			return nil, err
		}
	}
	return item, nil
}

func (runtime *FileTransferRuntime) workspaceMutationPath(
	profileRevision string,
	workspaceRoot string,
	relativePath string,
) (*fileProfileRuntime, string, error) {
	if runtime == nil || !filepath.IsAbs(workspaceRoot) || !validWorkspaceRelativePath(relativePath) ||
		filepath.ToSlash(filepath.Clean(filepath.FromSlash(relativePath))) != relativePath {
		return nil, "", ErrFileAccessDenied
	}
	profile := runtime.profiles[profileRevision]
	if profile == nil || profile.workspaceWritableRoot(workspaceRoot) == nil {
		return nil, "", ErrFileAccessDenied
	}
	path := filepath.Join(workspaceRoot, filepath.FromSlash(relativePath))
	if !pathWithinWorkspaceRoot(workspaceRoot, path) {
		return nil, "", ErrFileAccessDenied
	}
	return profile, path, nil
}

func (profile *fileProfileRuntime) workspaceWritableRoot(workspace string) *fileRoot {
	for _, root := range profile.writableRoots {
		if workspace == root.path || pathWithinWorkspaceRoot(root.path, workspace) {
			return root
		}
	}
	return nil
}

func stageWorkspaceContent(ctx context.Context, parent *resolvedParent, content []byte) (*stagedFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, stoppedWorkspaceMutation(ctx)
	}
	stage, err := parent.createStage("workspace")
	if err != nil {
		return nil, err
	}
	written, err := stage.file.Write(content)
	if err != nil || written != len(content) {
		_ = parent.removeStage(stage.identity, stage.name)
		_ = stage.file.Close()
		if err != nil {
			return nil, err
		}
		return nil, io.ErrShortWrite
	}
	return stage, nil
}

func verifyWorkspaceFile(
	ctx context.Context,
	parent *resolvedParent,
	expected fileIdentity,
	expectedSize int64,
	expectedDigest [sha256.Size]byte,
) error {
	current, err := parent.openFinalRegular()
	if err != nil {
		return err
	}
	defer func() { _ = current.file.Close() }()
	if current.identity.Device != expected.Device || current.identity.Inode != expected.Inode ||
		current.identity.Links != expected.Links || current.info.Size() != expectedSize {
		return ErrFileConflict
	}
	digest, err := hashOpenedFile(ctx, current.file)
	if err != nil {
		return err
	}
	if digest != expectedDigest {
		return ErrFileConflict
	}
	return nil
}

func publishWorkspaceMutation(ctx context.Context, item *preparedWorkspaceMutation) error {
	switch item.mutation.Action {
	case string(patchformat.Add):
		return item.stage.publish(filePublicationCreate)
	case string(patchformat.Update):
		return item.stage.publishReplacing(
			ctx,
			item.before.identity,
			item.before.info.Size(),
			item.digest,
		)
	case string(patchformat.Delete):
		return item.parent.removeFinalRegular(
			ctx,
			item.before.identity,
			item.before.info.Size(),
			item.digest,
		)
	default:
		return ErrFileAccessDenied
	}
}

func workspacePatchFailure(result WorkspacePatchResult, err error) (WorkspacePatchResult, error) {
	if len(result.Committed) == 0 {
		return WorkspacePatchResult{}, err
	}
	result.State = "partial"
	result.Code = safeFileFailureCode(err)
	return result, nil
}

func stoppedWorkspacePatch(
	result WorkspacePatchResult,
	ctx context.Context,
) (WorkspacePatchResult, error) {
	if len(result.Committed) == 0 {
		return WorkspacePatchResult{}, stoppedWorkspaceMutation(ctx)
	}
	result.State, result.Code = "partial", "TIMEOUT"
	if errors.Is(context.Cause(ctx), errCancellationRequested) {
		result.Code = "CANCELED"
	}
	return result, nil
}

func confirmedWorkspaceMutationCancellation(ctx context.Context, err error) error {
	if err != nil && errors.Is(context.Cause(ctx), errCancellationRequested) {
		return fmt.Errorf("%w: %w", errCommandCancellationConfirmed, err)
	}
	return err
}

func stoppedWorkspaceMutation(ctx context.Context) error {
	err := ctx.Err()
	if err != nil && errors.Is(context.Cause(ctx), errCancellationRequested) {
		return fmt.Errorf("%w: %w", errCommandCancellationConfirmed, err)
	}
	return err
}

func closePreparedWorkspaceMutations(prepared []*preparedWorkspaceMutation) {
	for _, item := range prepared {
		closePreparedWorkspaceMutation(item)
	}
}

func closePreparedWorkspaceMutation(item *preparedWorkspaceMutation) {
	if item == nil {
		return
	}
	if item.stage != nil && item.stage.file != nil {
		_ = item.parent.removeStage(item.stage.identity, item.stage.name)
		_ = item.stage.file.Close()
		item.stage.file = nil
	}
	if item.before != nil && item.before.file != nil {
		_ = item.before.file.Close()
		item.before.file = nil
	}
	if item.parent != nil {
		_ = item.parent.close()
	}
}
