package companion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/patchformat"
)

type workspaceReadRuntime struct {
	files       *FileTransferRouter
	systemExec  SystemExecPolicy
	descriptors map[string]nodes.CommandDescriptor
}

type workspaceReadHandler struct {
	runtime *workspaceReadRuntime
}

type workspaceSearchHandler struct {
	runtime *workspaceReadRuntime
}

type workspaceWriteHandler struct {
	runtime *workspaceReadRuntime
}

type workspacePatchHandler struct {
	runtime *workspaceReadRuntime
}

type workspaceReadInput struct {
	ProfileRevision   string  `json:"profile_revision"`
	WorkspaceRevision string  `json:"workspace_revision"`
	WorkingScope      string  `json:"working_scope"`
	Path              string  `json:"path"`
	Offset            float64 `json:"offset,omitempty"`
	Length            float64 `json:"length,omitempty"`
	StartLine         float64 `json:"start_line,omitempty"`
	MaxLines          float64 `json:"max_lines,omitempty"`
}

type workspaceSearchInput struct {
	ProfileRevision   string  `json:"profile_revision"`
	WorkspaceRevision string  `json:"workspace_revision"`
	WorkingScope      string  `json:"working_scope"`
	Pattern           string  `json:"pattern"`
	Target            string  `json:"target,omitempty"`
	Path              string  `json:"path,omitempty"`
	FileGlob          string  `json:"file_glob,omitempty"`
	OutputMode        string  `json:"output_mode,omitempty"`
	Context           float64 `json:"context,omitempty"`
	Limit             float64 `json:"limit,omitempty"`
	IncludeIgnored    bool    `json:"include_ignored,omitempty"`
}

type workspaceWriteInput struct {
	ProfileRevision   string `json:"profile_revision"`
	WorkspaceRevision string `json:"workspace_revision"`
	WorkingScope      string `json:"working_scope"`
	Path              string `json:"path"`
	Content           string `json:"content"`
	Overwrite         bool   `json:"overwrite"`
	ExpectedSHA256    string `json:"expected_sha256,omitempty"`
}

type workspacePatchInput struct {
	ProfileRevision   string `json:"profile_revision"`
	WorkspaceRevision string `json:"workspace_revision"`
	WorkingScope      string `json:"working_scope"`
	Input             string `json:"input"`
}

func newWorkspaceReadRuntime(
	files *FileTransferRouter,
	systemExec SystemExecPolicy,
) (*workspaceReadRuntime, error) {
	if files == nil || len(files.WorkspaceProfileRevisions()) == 0 {
		return nil, errors.New("workspace read requires an unprivileged file profile")
	}
	if len(systemExec.workingScopeAliases) == 0 {
		return nil, errors.New("workspace read requires system-exec working-scope discovery")
	}
	scopes := sortedSystemExecMapKeys(systemExec.workingScopeAliases)
	descriptors, err := nodes.WorkspaceDescriptors(files.WorkspaceProfiles(), scopes)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]nodes.CommandDescriptor, len(descriptors))
	for _, descriptor := range descriptors {
		byName[descriptor.Name] = descriptor
	}
	return &workspaceReadRuntime{files: files, systemExec: systemExec, descriptors: byName}, nil
}

func (runtime *workspaceReadRuntime) handlers() []commandHandler {
	if runtime == nil {
		return nil
	}
	handlers := make([]commandHandler, 0, len(runtime.descriptors))
	if _, available := runtime.descriptors[nodes.WorkspaceCommandRead]; available {
		handlers = append(handlers, workspaceReadHandler{runtime: runtime})
	}
	if _, available := runtime.descriptors[nodes.WorkspaceCommandSearch]; available {
		handlers = append(handlers, workspaceSearchHandler{runtime: runtime})
	}
	if _, available := runtime.descriptors[nodes.WorkspaceCommandWrite]; available {
		handlers = append(handlers, workspaceWriteHandler{runtime: runtime})
	}
	if _, available := runtime.descriptors[nodes.WorkspaceCommandPatch]; available {
		handlers = append(handlers, workspacePatchHandler{runtime: runtime})
	}
	return handlers
}

func (handler workspaceWriteHandler) descriptor() nodes.CommandDescriptor {
	return handler.runtime.descriptors[nodes.WorkspaceCommandWrite]
}

func (handler workspaceWriteHandler) execute(
	ctx context.Context,
	invocation commandInvocation,
) (any, error) {
	var input workspaceWriteInput
	if err := json.Unmarshal(invocation.Input, &input); err != nil {
		return nil, &commandInputDeniedError{}
	}
	root, ok := handler.runtime.systemExec.workingScopeAliases[input.WorkingScope]
	if !ok || !validWorkspaceRelativePath(input.Path) {
		return nil, &commandInputDeniedError{}
	}
	prospective := WorkspaceWriteResult{
		Path: input.Path, Action: "create", Size: len(input.Content), SHA256: strings.Repeat("0", 64),
	}
	if input.Overwrite {
		prospective.Action = "replace"
	}
	if !workspaceOutputFits(prospective, handler.descriptor(), invocation.OutputLimitBytes) {
		return nil, fmt.Errorf("%w: OUTPUT_LIMIT", nodes.ErrCommandDenied)
	}
	result, err := handler.runtime.files.WriteWorkspace(
		ctx,
		input.ProfileRevision,
		root,
		WorkspaceWriteOptions{
			Path: input.Path, Content: input.Content, Overwrite: input.Overwrite,
			ExpectedSHA256: input.ExpectedSHA256,
		},
	)
	if errors.Is(err, ErrInvocationOutcomeUnknown) || errors.Is(err, errCommandCancellationConfirmed) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %s", nodes.ErrCommandDenied, safeFileFailureCode(err))
	}
	if !workspaceOutputFits(result, handler.descriptor(), invocation.OutputLimitBytes) {
		return nil, fmt.Errorf("%w: workspace write result exceeded its prepared bound", ErrInvocationOutcomeUnknown)
	}
	return result, nil
}

func (handler workspacePatchHandler) descriptor() nodes.CommandDescriptor {
	return handler.runtime.descriptors[nodes.WorkspaceCommandPatch]
}

func (handler workspacePatchHandler) execute(
	ctx context.Context,
	invocation commandInvocation,
) (any, error) {
	var input workspacePatchInput
	if err := json.Unmarshal(invocation.Input, &input); err != nil {
		return nil, &commandInputDeniedError{}
	}
	root, ok := handler.runtime.systemExec.workingScopeAliases[input.WorkingScope]
	if !ok {
		return nil, &commandInputDeniedError{}
	}
	operations, err := patchformat.Parse(input.Input, nodes.MaxWorkspacePatchFiles)
	if err != nil {
		return nil, &commandInputDeniedError{}
	}
	prospective := WorkspacePatchResult{
		State: "partial", Code: "FILE_OPERATION_FAILED",
		Committed: make([]WorkspacePatchEntry, 0, len(operations)),
	}
	for _, operation := range operations {
		prospective.Committed = append(prospective.Committed, WorkspacePatchEntry{
			Path: operation.Path, Action: string(operation.Kind),
			Size: nodes.MaxWorkspaceWriteBytes, SHA256: strings.Repeat("0", 64),
		})
	}
	if !workspaceOutputFits(prospective, handler.descriptor(), invocation.OutputLimitBytes) {
		return nil, fmt.Errorf("%w: OUTPUT_LIMIT", nodes.ErrCommandDenied)
	}
	result, err := handler.runtime.files.PatchWorkspace(
		ctx,
		input.ProfileRevision,
		root,
		WorkspacePatchOptions{Input: input.Input},
	)
	if errors.Is(err, ErrInvocationOutcomeUnknown) || errors.Is(err, errCommandCancellationConfirmed) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %s", nodes.ErrCommandDenied, safeFileFailureCode(err))
	}
	if !workspaceOutputFits(result, handler.descriptor(), invocation.OutputLimitBytes) {
		return nil, fmt.Errorf("%w: workspace patch result exceeded its prepared bound", ErrInvocationOutcomeUnknown)
	}
	return result, nil
}

func (handler workspaceSearchHandler) descriptor() nodes.CommandDescriptor {
	return handler.runtime.descriptors[nodes.WorkspaceCommandSearch]
}

func (handler workspaceSearchHandler) execute(
	ctx context.Context,
	invocation commandInvocation,
) (any, error) {
	var input workspaceSearchInput
	if err := json.Unmarshal(invocation.Input, &input); err != nil {
		return nil, &commandInputDeniedError{}
	}
	root, ok := handler.runtime.systemExec.workingScopeAliases[input.WorkingScope]
	if !ok || (input.Path != "" && !validWorkspaceRelativePath(input.Path)) {
		return nil, &commandInputDeniedError{}
	}
	result, err := handler.runtime.files.SearchWorkspace(
		ctx,
		input.ProfileRevision,
		root,
		WorkspaceSearchOptions{
			Pattern: input.Pattern, Target: input.Target, Path: input.Path,
			FileGlob: input.FileGlob, OutputMode: input.OutputMode, Context: int(input.Context),
			Limit: int(input.Limit), IncludeIgnored: input.IncludeIgnored,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", nodes.ErrCommandDenied, safeFileFailureCode(err))
	}
	result, err = boundWorkspaceSearchResult(result, handler.descriptor(), invocation.OutputLimitBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: OUTPUT_LIMIT", nodes.ErrCommandDenied)
	}
	return result, nil
}

func boundWorkspaceSearchResult(
	result WorkspaceSearchResult,
	descriptor nodes.CommandDescriptor,
	outputLimit int,
) (WorkspaceSearchResult, error) {
	if outputLimit <= 0 || outputLimit > nodes.MaxWorkspaceReadBytes {
		return WorkspaceSearchResult{}, nodes.ErrCommandDenied
	}
	if workspaceOutputFits(result, descriptor, outputLimit) {
		return result, nil
	}
	content := result.Result
	result.Truncated = true
	bounded, ok := boundWorkspaceText(content, func(candidate string) bool {
		result.Result = candidate
		return workspaceOutputFits(result, descriptor, outputLimit)
	})
	if !ok {
		return WorkspaceSearchResult{}, nodes.ErrCommandDenied
	}
	result.Result = bounded
	return result, nil
}

func (handler workspaceReadHandler) descriptor() nodes.CommandDescriptor {
	return handler.runtime.descriptors[nodes.WorkspaceCommandRead]
}

func (handler workspaceReadHandler) execute(
	ctx context.Context,
	invocation commandInvocation,
) (any, error) {
	var input workspaceReadInput
	if err := json.Unmarshal(invocation.Input, &input); err != nil {
		return nil, &commandInputDeniedError{}
	}
	root, ok := handler.runtime.systemExec.workingScopeAliases[input.WorkingScope]
	if !ok || !validWorkspaceRelativePath(input.Path) {
		return nil, &commandInputDeniedError{}
	}
	path := filepath.Join(root, filepath.FromSlash(input.Path))
	if !pathWithinWorkspaceRoot(root, path) {
		return nil, &commandInputDeniedError{}
	}
	result, err := handler.runtime.files.ReadWorkspace(
		ctx,
		input.ProfileRevision,
		path,
		WorkspaceReadOptions{
			Offset: int64(input.Offset), Length: int(input.Length),
			StartLine: int(input.StartLine), MaxLines: int(input.MaxLines),
		},
	)
	if err != nil {
		if errors.Is(err, ErrFileNotFound) {
			return nil, newCommandFailure(
				nodes.InvocationDispatchFileNotFound,
				"workspace file was not found",
				err,
			)
		}
		return nil, fmt.Errorf("%w: %s", nodes.ErrCommandDenied, safeFileFailureCode(err))
	}
	result.Path = filepath.ToSlash(input.Path)
	result, err = boundWorkspaceReadResult(result, handler.descriptor(), invocation.OutputLimitBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: OUTPUT_LIMIT", nodes.ErrCommandDenied)
	}
	return result, nil
}

func boundWorkspaceReadResult(
	result WorkspaceReadResult,
	descriptor nodes.CommandDescriptor,
	outputLimit int,
) (WorkspaceReadResult, error) {
	if outputLimit <= 0 || outputLimit > nodes.MaxWorkspaceReadBytes {
		return WorkspaceReadResult{}, nodes.ErrCommandDenied
	}
	if workspaceOutputFits(result, descriptor, outputLimit) {
		return result, nil
	}
	content := result.Content
	result.Truncated = true
	bounded, ok := boundWorkspaceText(content, func(candidate string) bool {
		result.Content = candidate
		return workspaceOutputFits(result, descriptor, outputLimit)
	})
	if !ok {
		return WorkspaceReadResult{}, nodes.ErrCommandDenied
	}
	result.Content = bounded
	return result, nil
}

func boundWorkspaceText(content string, fits func(string) bool) (string, bool) {
	boundaries := []int{0}
	for index := range content {
		if index > 0 {
			boundaries = append(boundaries, index)
		}
	}
	if boundaries[len(boundaries)-1] != len(content) {
		boundaries = append(boundaries, len(content))
	}
	best := -1
	low, high := 0, len(boundaries)-1
	for low <= high {
		middle := low + (high-low)/2
		contentBytes := boundaries[middle]
		if fits(content[:contentBytes]) {
			best = contentBytes
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	if best < 0 {
		return "", false
	}
	return content[:best], true
}

func workspaceOutputFits(result any, descriptor nodes.CommandDescriptor, outputLimit int) bool {
	encoded, err := json.Marshal(result)
	if err != nil {
		return false
	}
	_, err = nodes.ValidateInvocationOutputForProtocol(nodes.ProtocolV2, descriptor, encoded, outputLimit)
	return err == nil
}

func validWorkspaceRelativePath(path string) bool {
	if path == "" || strings.TrimSpace(path) != path || strings.ContainsRune(path, 0) || filepath.IsAbs(path) {
		return false
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	return clean != "." && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func pathWithinWorkspaceRoot(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != "." && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
