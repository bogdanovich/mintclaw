package companion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
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
	descriptors, err := nodes.WorkspaceReadDescriptors(files.WorkspaceProfileRevisions(), scopes)
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
	return []commandHandler{
		workspaceReadHandler{runtime: runtime},
		workspaceSearchHandler{runtime: runtime},
	}
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
		return nil, fmt.Errorf("%w: %s", nodes.ErrCommandDenied, safeFileFailureCode(err))
	}
	result.Path = filepath.ToSlash(input.Path)
	return result, nil
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
