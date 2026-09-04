package workspace

import (
	"strings"
	"testing"
)

func TestRenderStatusPlainIncludesProvenanceAndEscapesControls(t *testing.T) {
	status := StatusResult{
		SchemaVersion: RepositoryStatusSchemaV1,
		Snapshot: Snapshot{
			Git: GitState{
				Available:       true,
				StatusAvailable: true,
				Dirty:           true,
				TopLevel:        "/repo",
				Branch:          "main",
			},
			ChangedPaths: []ChangedPath{{Path: "unsafe\x1b.go", Status: " M"}},
		},
		Provenance: &ProvenanceResult{Paths: []ProvenancePath{{
			Path: "unsafe\x1b.go", Status: " M", Provenance: ProvenancePreExisting,
		}}},
	}
	rendered := RenderStatusPlain(status)
	if strings.ContainsRune(rendered, '\x1b') || !strings.Contains(rendered, "pre_existing") ||
		!strings.Contains(rendered, `\x1b`) {
		t.Fatalf("rendered status = %q", rendered)
	}
}

func TestRenderStatusPlainKeepsSamePathStatusesDistinct(t *testing.T) {
	status := StatusResult{
		Snapshot: Snapshot{
			Git: GitState{Available: true, StatusAvailable: true},
			ChangedPaths: []ChangedPath{
				{Path: "same.txt", Status: "D "},
				{Path: "same.txt", Status: "??"},
			},
		},
		Provenance: &ProvenanceResult{Paths: []ProvenancePath{
			{Path: "same.txt", Status: "D ", Provenance: ProvenancePreExisting},
			{Path: "same.txt", Status: "??", Provenance: ProvenanceFirstObservedDuringThread},
		}},
	}
	rendered := RenderStatusPlain(status)
	if !strings.Contains(rendered, "D  same.txt [pre_existing]") ||
		!strings.Contains(rendered, "?? same.txt [first_observed_during_thread]") {
		t.Fatalf("same-path status provenance = %q", rendered)
	}
}

func TestRenderDiffPlainIncludesBoundedHunksAndDiagnostics(t *testing.T) {
	diff := DiffResult{
		SchemaVersion: RepositoryDiffSchemaV1,
		Target:        DiffTarget{Kind: DiffTargetCurrent},
		Files: []DiffFile{{
			Path: "changed.go", Status: " M", Provenance: ProvenanceFirstObservedDuringThread,
			Hunks: []DiffHunk{{
				OldStart: 1, OldLines: 1, NewStart: 1, NewLines: 1, Truncated: true,
				Lines: []DiffLine{
					{Kind: "deletion", OldLine: 1, Text: "old"},
					{Kind: "addition", NewLine: 1, Text: "new"},
				},
			}},
		}},
		Additions: 1, Deletions: 1, Truncated: true,
	}
	rendered := RenderDiffPlain(diff)
	for _, wanted := range []string{
		"Repository diff (current)", "first_observed_during_thread", "-     1 old", "+     1 new", "hunk truncated",
		"evidence incomplete or stale",
	} {
		if !strings.Contains(rendered, wanted) {
			t.Fatalf("rendered diff missing %q: %s", wanted, rendered)
		}
	}
}

func TestRenderDiffPlainPreservesUnavailableDiagnostics(t *testing.T) {
	rendered := RenderDiffPlain(DiffResult{
		Target: DiffTarget{Kind: DiffTargetCurrent}, UnavailableReason: "not available",
		Warning: "partial evidence", Truncated: true,
	})
	for _, wanted := range []string{"unavailable: not available", "evidence incomplete", "warning: partial evidence"} {
		if !strings.Contains(rendered, wanted) {
			t.Fatalf("unavailable diff missing %q: %q", wanted, rendered)
		}
	}
}
