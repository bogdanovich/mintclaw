package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
	"github.com/bogdanovich/mintclaw/pkg/tools/loopguard"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type repositoryEvidence interface {
	Status(context.Context) codingworkspace.StatusResult
	Diff(context.Context, codingworkspace.DiffTarget) codingworkspace.DiffResult
}

type RepositoryStatusTool struct {
	repository repositoryEvidence
}

func NewRepositoryStatusTool(repository repositoryEvidence) *RepositoryStatusTool {
	return &RepositoryStatusTool{repository: repository}
}

func (*RepositoryStatusTool) Name() string { return "repository_status" }

func (*RepositoryStatusTool) Description() string {
	return "Inspect the current repository state through MintClaw's passive, bounded Git evidence service. " +
		"Use this instead of assembling git status commands. Provenance describes observation, never authorship."
}

func (*RepositoryStatusTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
}

func (*RepositoryStatusTool) ToolLoopSemantics() loopguard.Semantics {
	return loopguard.SemanticsReadOnlyIdempotent
}

func (tool *RepositoryStatusTool) Execute(ctx context.Context, _ map[string]any) *toolshared.ToolResult {
	if tool == nil || tool.repository == nil {
		return toolshared.ErrorResult("repository evidence service is unavailable")
	}
	return repositoryJSONResult(tool.repository.Status(ctx))
}

type RepositoryDiffTool struct {
	repository repositoryEvidence
}

func NewRepositoryDiffTool(repository repositoryEvidence) *RepositoryDiffTool {
	return &RepositoryDiffTool{repository: repository}
}

func (*RepositoryDiffTool) Name() string { return "repository_diff" }

func (*RepositoryDiffTool) Description() string {
	return "Inspect bounded repository file summaries and hunks for current changes, the thread baseline, " +
		"a local base branch, or one local commit. This read-only tool never fetches refs or mutates Git state."
}

func (*RepositoryDiffTool) Parameters() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"target": map[string]any{
				"type": "string", "enum": []string{"current", "baseline", "base", "commit"},
				"description": "Evidence scope. Defaults to current.",
			},
			"ref": map[string]any{
				"type": "string", "description": "Required local ref for base or commit targets.",
			},
		},
	}
}

func (*RepositoryDiffTool) ToolLoopSemantics() loopguard.Semantics {
	return loopguard.SemanticsReadOnlyIdempotent
}

func (tool *RepositoryDiffTool) Execute(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	if tool == nil || tool.repository == nil {
		return toolshared.ErrorResult("repository evidence service is unavailable")
	}
	target, err := repositoryDiffTarget(args)
	if err != nil {
		return toolshared.ErrorResult(err.Error())
	}
	return repositoryJSONResult(tool.repository.Diff(ctx, target))
}

func repositoryDiffTarget(args map[string]any) (codingworkspace.DiffTarget, error) {
	kind := codingworkspace.DiffTargetCurrent
	if raw, exists := args["target"]; exists {
		value, ok := raw.(string)
		if !ok {
			return codingworkspace.DiffTarget{}, fmt.Errorf("repository diff target must be a string")
		}
		kind = codingworkspace.DiffTargetKind(strings.TrimSpace(value))
	}
	ref := ""
	if raw, exists := args["ref"]; exists {
		value, ok := raw.(string)
		if !ok {
			return codingworkspace.DiffTarget{}, fmt.Errorf("repository diff ref must be a string")
		}
		ref = strings.TrimSpace(value)
	}
	if len(ref) > 4096 || !utf8.ValidString(ref) || strings.ContainsRune(ref, 0) {
		return codingworkspace.DiffTarget{}, fmt.Errorf("repository diff ref is invalid")
	}
	switch kind {
	case codingworkspace.DiffTargetCurrent, codingworkspace.DiffTargetBaseline:
		if ref != "" {
			return codingworkspace.DiffTarget{}, fmt.Errorf("repository diff %s target does not accept ref", kind)
		}
	case codingworkspace.DiffTargetBase, codingworkspace.DiffTargetCommit:
		if ref == "" {
			return codingworkspace.DiffTarget{}, fmt.Errorf("repository diff %s target requires ref", kind)
		}
	default:
		return codingworkspace.DiffTarget{}, fmt.Errorf("repository diff target %q is unsupported", kind)
	}
	return codingworkspace.DiffTarget{Kind: kind, Ref: ref}, nil
}

func repositoryJSONResult(value any) *toolshared.ToolResult {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return toolshared.ErrorResult(fmt.Sprintf("encode repository evidence: %v", err))
	}
	return toolshared.SilentResult(string(data))
}
