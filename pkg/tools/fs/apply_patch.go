package fstools

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/patchformat"
)

// ApplyPatchTool applies a small Codex-style multi-file patch.
type ApplyPatchTool struct {
	fs fileSystem
}

func NewApplyPatchTool(workspace string, restrict bool, allowPaths ...[]*regexp.Regexp) *ApplyPatchTool {
	var patterns []*regexp.Regexp
	if len(allowPaths) > 0 {
		patterns = allowPaths[0]
	}
	return &ApplyPatchTool{fs: buildFs(workspace, restrict, patterns)}
}

func (t *ApplyPatchTool) Name() string {
	return "apply_patch"
}

func (t *ApplyPatchTool) Description() string {
	return "Apply a structured patch to add, update, or delete files. " +
		"To delete a file, use a *** Delete File: path section; there is no separate delete-file tool. " +
		"Use the Codex patch format with *** Begin Patch, one or more *** Add File / *** Update File / " +
		"*** Delete File sections, and *** End Patch. Prefer this over write_file for targeted or multi-file edits."
}

func (t *ApplyPatchTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"input": map[string]any{
				"type":        "string",
				"description": "Full patch text, including *** Begin Patch and *** End Patch.",
			},
		},
		"required": []string{"input"},
	}
}

func (t *ApplyPatchTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	input, ok := args["input"].(string)
	if !ok || strings.TrimSpace(input) == "" {
		return ErrorResult("input is required")
	}

	ops, err := patchformat.Parse(input, 0)
	if err != nil {
		return ErrorResult(err.Error())
	}
	results, err := applyPatchOperations(t.fs, ops)
	if err != nil {
		result := ErrorResult(err.Error())
		addApplyPatchWriteAudit(result, results)
		return result
	}

	return formatApplyPatchResult(results)
}

type appliedPatchResult struct {
	path   string
	action string
	before []byte
	after  []byte
}

func applyPatchOperations(sysFs fileSystem, ops []patchformat.Operation) ([]appliedPatchResult, error) {
	results := make([]appliedPatchResult, 0, len(ops))
	for _, op := range ops {
		result, err := applyPatchOperation(sysFs, op)
		if err != nil {
			return results, err
		}
		results = append(results, result)
	}
	return results, nil
}

func applyPatchOperation(sysFs fileSystem, operation patchformat.Operation) (appliedPatchResult, error) {
	before, readErr := sysFs.ReadFile(operation.Path)
	exists := readErr == nil
	if readErr != nil {
		if operation.Kind != patchformat.Add || !isNotExistError(readErr) {
			return appliedPatchResult{}, readErr
		}
	}
	mutation, err := patchformat.Prepare(operation, before, exists)
	if err != nil {
		return appliedPatchResult{}, err
	}
	switch mutation.Action {
	case string(patchformat.Delete):
		err = sysFs.RemoveFile(mutation.Path)
	default:
		err = sysFs.WriteFile(mutation.Path, mutation.After)
	}
	if err != nil {
		return appliedPatchResult{}, err
	}
	return appliedPatchResult{
		path: mutation.Path, action: mutation.Action, before: mutation.Before, after: mutation.After,
	}, nil
}

func isNotExistError(err error) bool {
	return errors.Is(err, fs.ErrNotExist) ||
		(err != nil && strings.Contains(strings.ToLower(err.Error()), "file not found"))
}

func formatApplyPatchResult(results []appliedPatchResult) *ToolResult {
	var userParts []string
	var llmParts []string

	for _, result := range results {
		diff := DiffResult(result.path, result.before, result.after)
		if diff.ForUser != "" {
			userParts = append(userParts, diff.ForUser)
		}
		if diff.ForLLM != "" {
			llmParts = append(llmParts, diff.ForLLM)
		}
	}

	if len(llmParts) == 0 {
		llmParts = append(llmParts, fmt.Sprintf("Applied patch to %d file(s)", len(results)))
	}

	result := &ToolResult{
		ForLLM:  strings.Join(llmParts, "\n\n"),
		ForUser: strings.Join(userParts, "\n\n"),
	}
	addApplyPatchWriteAudit(result, results)
	return result
}

func addApplyPatchWriteAudit(result *ToolResult, results []appliedPatchResult) {
	for _, patchResult := range results {
		result.WithFileWriteAudit(patchResult.path, patchResult.action, "apply_patch")
	}
}
