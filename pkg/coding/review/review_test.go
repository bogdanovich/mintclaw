package review

import (
	"math"
	"strings"
	"testing"
	"time"

	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
)

func validResult() Result {
	return Result{
		SchemaVersion:      SchemaVersion,
		ReviewID:           NewID(),
		Target:             Target{Kind: TargetCurrent},
		EvidenceGeneration: "generation-1",
		Summary:            "One issue found.",
		CompletedAt:        time.Now().UTC(),
		Findings: []Finding{{
			Severity: SeverityMajor, Title: "Handle the error", Explanation: "The returned error is ignored.",
			Confidence: 0.95, LocationState: LocationCurrent, Path: "pkg/example.go", StartLine: 12, EndLine: 12,
		}},
	}
}

func TestResultValidatesAgainstExactChangedLines(t *testing.T) {
	diff := codingworkspace.DiffResult{
		SchemaVersion:       codingworkspace.RepositoryDiffSchemaV1,
		RepositoryAvailable: true,
		Target:              codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetCurrent},
		EvidenceGeneration:  "generation-1",
		Files: []codingworkspace.DiffFile{{
			Path: "pkg/example.go",
			Hunks: []codingworkspace.DiffHunk{{Lines: []codingworkspace.DiffLine{
				{Kind: "context", NewLine: 11},
				{Kind: "addition", NewLine: 12},
			}}},
		}},
	}
	if err := validResult().ValidateAgainstFrozenDiff(diff); err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func(*Result){
		"generation": func(result *Result) { result.EvidenceGeneration = "generation-2" },
		"target":     func(result *Result) { result.Target = Target{Kind: TargetBase, Ref: "main"} },
		"path":       func(result *Result) { result.Findings[0].Path = "pkg/other.go" },
		"line":       func(result *Result) { result.Findings[0].StartLine, result.Findings[0].EndLine = 11, 11 },
	} {
		t.Run(name, func(t *testing.T) {
			result := validResult()
			mutate(&result)
			if err := result.ValidateAgainstFrozenDiff(diff); err == nil {
				t.Fatal("mismatched review result was accepted")
			}
		})
	}
}

func TestResultValidatesTargetSpecificFrozenIdentity(t *testing.T) {
	tests := []struct {
		name   string
		result Result
		diff   codingworkspace.DiffResult
	}{
		{
			name:   "current",
			result: Result{Target: Target{Kind: TargetCurrent}, EvidenceGeneration: "generation-1"},
			diff: codingworkspace.DiffResult{
				SchemaVersion:       codingworkspace.RepositoryDiffSchemaV1,
				RepositoryAvailable: true,
				Target:              codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetCurrent},
				EvidenceGeneration:  "generation-1",
			},
		},
		{
			name: "base",
			result: Result{
				Target: Target{Kind: TargetBase, Ref: "main"}, EvidenceGeneration: "generation-1",
				ResolvedRevision: "base-tip", MergeBase: "merge-base",
			},
			diff: codingworkspace.DiffResult{
				SchemaVersion:       codingworkspace.RepositoryDiffSchemaV1,
				RepositoryAvailable: true,
				Target:              codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetBase, Ref: "main"},
				EvidenceGeneration:  "generation-1", ResolvedRevision: "base-tip", MergeBase: "merge-base",
			},
		},
		{
			name:   "commit",
			result: Result{Target: Target{Kind: TargetCommit, Ref: "HEAD"}, ResolvedRevision: "commit-sha"},
			diff: codingworkspace.DiffResult{
				SchemaVersion:       codingworkspace.RepositoryDiffSchemaV1,
				RepositoryAvailable: true,
				Target:              codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetCommit, Ref: "HEAD"},
				ResolvedRevision:    "commit-sha",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.result.SchemaVersion = SchemaVersion
			test.result.ReviewID = NewID()
			test.result.Summary = "No findings."
			test.result.CompletedAt = time.Now().UTC()
			if err := test.result.ValidateAgainstFrozenDiff(test.diff); err != nil {
				t.Fatal(err)
			}

			mismatched := test.result
			switch test.result.Target.Kind {
			case TargetCurrent:
				mismatched.EvidenceGeneration = "other-generation"
			case TargetBase:
				mismatched.MergeBase = "other-merge-base"
			case TargetCommit:
				mismatched.ResolvedRevision = "other-commit"
			}
			if err := mismatched.ValidateAgainstFrozenDiff(test.diff); err == nil {
				t.Fatal("mismatched frozen identity was accepted")
			}
		})
	}
}

func TestResultPreservesPartialFrozenEvidence(t *testing.T) {
	result := validResult()
	diff := codingworkspace.DiffResult{
		SchemaVersion:       codingworkspace.RepositoryDiffSchemaV1,
		RepositoryAvailable: true,
		Target:              codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetCurrent},
		EvidenceGeneration:  result.EvidenceGeneration,
		Truncated:           true,
		Files: []codingworkspace.DiffFile{{
			Path: "pkg/example.go",
			Hunks: []codingworkspace.DiffHunk{{Lines: []codingworkspace.DiffLine{{
				Kind: "addition", NewLine: 12,
			}}}},
		}},
	}
	if err := result.ValidateAgainstFrozenDiff(diff); err == nil {
		t.Fatal("complete review accepted truncated frozen evidence")
	}
	result.Truncated = true
	if err := result.ValidateAgainstFrozenDiff(diff); err != nil {
		t.Fatal(err)
	}

	diff.Truncated = false
	diff.Files = append(diff.Files, codingworkspace.DiffFile{Path: "linked", Omitted: "symlink not followed"})
	result.Truncated = false
	if err := result.ValidateAgainstFrozenDiff(diff); err == nil {
		t.Fatal("complete review accepted an omitted frozen file")
	}
	result.Truncated = true
	if err := result.ValidateAgainstFrozenDiff(diff); err != nil {
		t.Fatal(err)
	}
}

func TestResultRejectsUnavailableFrozenEvidence(t *testing.T) {
	result := validResult()
	diff := codingworkspace.DiffResult{
		SchemaVersion:       codingworkspace.RepositoryDiffSchemaV1,
		RepositoryAvailable: true,
		Target:              codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetCurrent},
		EvidenceGeneration:  result.EvidenceGeneration,
		Files: []codingworkspace.DiffFile{{
			Path: "pkg/example.go",
			Hunks: []codingworkspace.DiffHunk{{Lines: []codingworkspace.DiffLine{{
				Kind: "addition", NewLine: 12,
			}}}},
		}},
	}
	for name, mutate := range map[string]func(*codingworkspace.DiffResult){
		"schema":      func(diff *codingworkspace.DiffResult) { diff.SchemaVersion = "unknown" },
		"repository":  func(diff *codingworkspace.DiffResult) { diff.RepositoryAvailable = false },
		"unavailable": func(diff *codingworkspace.DiffResult) { diff.UnavailableReason = "Git status unavailable" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := diff
			mutate(&candidate)
			if err := result.ValidateAgainstFrozenDiff(candidate); err == nil {
				t.Fatal("review accepted unavailable frozen evidence")
			}
		})
	}
}

func TestResultRejectsUnsafeOrUnboundedFindings(t *testing.T) {
	for name, mutate := range map[string]func(*Result){
		"absolute path": func(result *Result) { result.Findings[0].Path = "/secret.go" },
		"parent path":   func(result *Result) { result.Findings[0].Path = "../secret.go" },
		"wide range": func(result *Result) {
			result.Findings[0].StartLine, result.Findings[0].EndLine = 1, MaxFindingLocationSpan+1
		},
		"unsafe control": func(result *Result) { result.Findings[0].Title = "unsafe\x1b" },
		"nan confidence": func(result *Result) { result.Findings[0].Confidence = math.NaN() },
		"too many": func(result *Result) {
			result.Findings = make([]Finding, MaxFindings+1)
		},
		"local completion time": func(result *Result) { result.CompletedAt = time.Now() },
	} {
		t.Run(name, func(t *testing.T) {
			result := validResult()
			mutate(&result)
			if err := result.Validate(); err == nil {
				t.Fatal("unsafe review result was accepted")
			}
		})
	}
}

func TestTargetRequiresBoundedEvidenceScope(t *testing.T) {
	for _, target := range []Target{
		{Kind: TargetCurrent},
		{Kind: TargetBase, Ref: "main", Instructions: "Focus on races."},
		{Kind: TargetCommit, Ref: "abc123"},
	} {
		if err := target.Validate(); err != nil {
			t.Fatalf("valid target rejected: %v", err)
		}
	}
	for _, target := range []Target{
		{Kind: TargetCurrent, Ref: "main"},
		{Kind: TargetBase},
		{Kind: TargetKind("custom"), Instructions: "review anything"},
		{Kind: TargetCommit, Ref: "abc123", Instructions: strings.Repeat("x", MaxInstructionsBytes+1)},
	} {
		if err := target.Validate(); err == nil {
			t.Fatalf("invalid target accepted: %#v", target)
		}
	}
}

func TestStaleAndUnlocatedFindingsCannotClaimCurrentLines(t *testing.T) {
	for _, state := range []LocationState{LocationStale, LocationUnlocated} {
		finding := validResult().Findings[0]
		finding.LocationState = state
		if err := finding.Validate(); err == nil {
			t.Fatalf("%s finding claimed a current line", state)
		}
	}
	result := validResult()
	result.Stale = true
	if err := result.Validate(); err == nil {
		t.Fatal("stale result retained current finding location")
	}
}
