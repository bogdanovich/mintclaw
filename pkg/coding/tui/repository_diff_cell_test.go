package tui

import (
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
)

func TestRepositoryDiffCellIsProvenanceSafeAndKeepsFullEvidence(t *testing.T) {
	item := semanticToolItem("repository-diff", 1, 1, frontend.PresentationCompleted, frontend.ToolSucceeded)
	item.Tool.Name = "repository_diff"
	item.Tool.RepositoryDiff = &codingworkspace.DiffResult{
		SchemaVersion: codingworkspace.RepositoryDiffSchemaV1,
		Target:        codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetCurrent},
		Files: []codingworkspace.DiffFile{
			{
				Path: "pkg/current.go", Status: " M", Additions: 1, Deletions: 1,
				Provenance: codingworkspace.ProvenancePreExisting,
				Hunks: []codingworkspace.DiffHunk{{
					OldStart: 7, OldLines: 1, NewStart: 7, NewLines: 1, Header: "func current()",
					Lines: []codingworkspace.DiffLine{
						{Kind: "deletion", OldLine: 7, Text: "return old"},
						{Kind: "addition", NewLine: 7, Text: "return new"},
					},
				}},
			},
			{
				Path: "assets/image.png", Status: "??", Binary: true,
				Provenance: codingworkspace.ProvenanceFirstObservedDuringThread,
			},
		},
		Additions: 1, Deletions: 1, BinaryFiles: 1,
	}
	cell := newPresentationCell(item)
	item.Tool.RepositoryDiff.Files[0].Hunks[0].Lines[0].Text = "mutated after cell creation"

	compact := cell.Render(cellRenderContext{Width: 120}, cellRenderCompact).plainText()
	if !strings.Contains(compact, "Repository diff observed (current) · 2 files · +1 -1") ||
		!strings.Contains(compact, "pre-existing") || !strings.Contains(compact, "first observed during thread") ||
		strings.Contains(strings.ToLower(compact), "edited") {
		t.Fatalf("compact repository diff = %q", compact)
	}
	full := cell.Render(cellRenderContext{Width: 120}, cellRenderFull).plainText()
	for _, want := range []string{"return old", "return new", "[binary]", "pre_existing"} {
		if !strings.Contains(full, want) {
			t.Fatalf("full repository diff omits %q: %q", want, full)
		}
	}
	if strings.Contains(full, "mutated after cell creation") {
		t.Fatalf("historical repository diff aliases caller: %q", full)
	}
}

func TestRepositoryDiffCellShowsIncompleteAndUnavailableStates(t *testing.T) {
	item := semanticToolItem("repository-diff", 1, 1, frontend.PresentationCompleted, frontend.ToolSucceeded)
	item.Tool.Name = "repository_diff"
	item.Tool.RepositoryDiff = &codingworkspace.DiffResult{
		SchemaVersion:     codingworkspace.RepositoryDiffSchemaV1,
		Target:            codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetCurrent},
		UnavailableReason: "Git status is unavailable",
		Truncated:         true,
		Stale:             true,
	}
	document := newPresentationCell(item).Render(cellRenderContext{Width: 80}, cellRenderCompact)
	text := document.plainText()
	if !strings.Contains(text, "Repository diff unavailable") || !strings.Contains(text, "incomplete or stale") ||
		!document.Truncated || !document.TruncationVisible {
		t.Fatalf("unavailable repository diff = %q / %+v", text, document)
	}
}
