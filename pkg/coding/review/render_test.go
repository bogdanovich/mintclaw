package review

import (
	"strings"
	"testing"
	"time"
)

func TestRenderResultPlainPrioritizesAndLabelsFindingLocations(t *testing.T) {
	result := Result{
		Target:             Target{Kind: TargetBase, Ref: "main", Instructions: "focus on races"},
		EvidenceGeneration: "generation-1",
		ResolvedRevision:   "revision-1",
		MergeBase:          "merge-base-1",
		Summary:            "Two issues found.",
		Stale:              true,
		Truncated:          true,
		Diagnostic:         "repository changed",
		CompletedAt:        time.Unix(1, 0).UTC(),
		Findings: []Finding{
			{
				Severity: SeverityMinor, Title: "Minor", Explanation: "minor detail", Confidence: 0.5,
				LocationState: LocationUnlocated,
			},
			{
				Severity: SeverityCritical, Title: "Critical", Explanation: "critical detail", Confidence: 0.95,
				LocationState: LocationStale, Path: "current.go",
			},
		},
	}
	rendered := RenderResultPlain(result)
	for _, want := range []string{
		"target: base main\ninstructions: focus on races",
		"stale: true",
		"truncated: true",
		"current.go (stale; no current line claimed)",
		"location: unlocated",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered review missing %q:\n%s", want, rendered)
		}
	}
	if strings.Index(rendered, "[critical]") > strings.Index(rendered, "[minor]") {
		t.Fatalf("findings were not prioritized:\n%s", rendered)
	}
}

func TestRenderStatePlainShowsProgressAndPartialFindings(t *testing.T) {
	state := State{
		Target:   Target{Kind: TargetCurrent},
		Phase:    PhaseProgress,
		Progress: "inspecting tests",
		Findings: []Finding{{
			Severity: SeverityMajor, Title: "Race", Explanation: "unsafe publication", Confidence: 0.8,
			LocationState: LocationCurrent, Path: "runtime.go", StartLine: 8, EndLine: 10,
		}},
	}
	rendered := RenderStatePlain(state)
	for _, want := range []string{"phase: progress", "progress: inspecting tests", "runtime.go:8-10"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered review missing %q:\n%s", want, rendered)
		}
	}
}
