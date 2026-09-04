package tools

import (
	"context"
	"encoding/json"
	"testing"

	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
	"github.com/bogdanovich/mintclaw/pkg/tools/loopguard"
)

type fakeRepositoryEvidence struct {
	status codingworkspace.StatusResult
	diff   codingworkspace.DiffResult
	target codingworkspace.DiffTarget
}

func (fake *fakeRepositoryEvidence) Status(context.Context) codingworkspace.StatusResult {
	return fake.status
}

func (fake *fakeRepositoryEvidence) Diff(
	_ context.Context,
	target codingworkspace.DiffTarget,
) codingworkspace.DiffResult {
	fake.target = target
	return fake.diff
}

func TestRepositoryStatusToolReturnsSchemaVersionedEvidenceSilently(t *testing.T) {
	fake := &fakeRepositoryEvidence{status: codingworkspace.StatusResult{
		SchemaVersion: codingworkspace.RepositoryStatusSchemaV1,
	}}
	tool := NewRepositoryStatusTool(fake)
	result := tool.Execute(t.Context(), nil)
	if result.IsError || result.ForUser != "" || tool.ToolLoopSemantics() != loopguard.SemanticsReadOnlyIdempotent {
		t.Fatalf("result = %#v", result)
	}
	var decoded codingworkspace.StatusResult
	if err := json.Unmarshal([]byte(result.ForLLM), &decoded); err != nil ||
		decoded.SchemaVersion != codingworkspace.RepositoryStatusSchemaV1 {
		t.Fatalf("decoded status = %#v / %v", decoded, err)
	}
}

func TestRepositoryDiffToolValidatesAndForwardsTypedTargets(t *testing.T) {
	fake := &fakeRepositoryEvidence{diff: codingworkspace.DiffResult{
		SchemaVersion: codingworkspace.RepositoryDiffSchemaV1,
	}}
	tool := NewRepositoryDiffTool(fake)
	result := tool.Execute(t.Context(), map[string]any{"target": "base", "ref": "main"})
	if result.IsError || fake.target.Kind != codingworkspace.DiffTargetBase || fake.target.Ref != "main" ||
		tool.ToolLoopSemantics() != loopguard.SemanticsReadOnlyIdempotent {
		t.Fatalf("result/target = %#v / %#v", result, fake.target)
	}
	for _, args := range []map[string]any{
		{"target": "base"},
		{"target": "current", "ref": "main"},
		{"target": "unsupported"},
	} {
		if result := tool.Execute(t.Context(), args); !result.IsError {
			t.Fatalf("invalid args accepted: %#v => %#v", args, result)
		}
	}
}
