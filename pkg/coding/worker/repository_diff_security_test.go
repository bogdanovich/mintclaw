package worker

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
)

const repositoryDiffPrivateKeyFixture = "-----BEGIN PRIVATE KEY-----\nprivate-material\n-----END PRIVATE KEY-----"

func TestSnapshotFromFrontendRedactsEveryRepositoryDiffTextField(t *testing.T) {
	binding := testBinding(t)
	diff := &codingworkspace.DiffResult{
		SchemaVersion:      codingworkspace.RepositoryDiffSchemaV1,
		Target:             codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetBase, Ref: "sk-123456789abcdef"},
		ResolvedRevision:   repositoryDiffPrivateKeyFixture,
		MergeBase:          repositoryDiffPrivateKeyFixture,
		Head:               repositoryDiffPrivateKeyFixture,
		Branch:             repositoryDiffPrivateKeyFixture,
		Generation:         repositoryDiffPrivateKeyFixture,
		EvidenceGeneration: repositoryDiffPrivateKeyFixture,
		UnavailableReason:  repositoryDiffPrivateKeyFixture,
		Warning:            repositoryDiffPrivateKeyFixture,
		BaselineID:         repositoryDiffPrivateKeyFixture,
		Files: []codingworkspace.DiffFile{{
			Path:             "pkg/sk-123456789abcdef.go",
			OriginalPath:     repositoryDiffPrivateKeyFixture,
			Status:           "Authorization: Bearer abcdefghijklmnop",
			Omitted:          repositoryDiffPrivateKeyFixture,
			ProvenanceReason: repositoryDiffPrivateKeyFixture,
			Hunks: []codingworkspace.DiffHunk{{
				Header: repositoryDiffPrivateKeyFixture,
				Lines: []codingworkspace.DiffLine{{
					Kind: "addition", NewLine: 1, Text: repositoryDiffPrivateKeyFixture,
				}},
			}},
		}},
		Additions: 1,
		Provenance: &codingworkspace.ProvenanceResult{
			Reason: repositoryDiffPrivateKeyFixture,
		},
	}
	source := frontend.ThreadSnapshot{
		ThreadID: binding.ThreadID,
		Activity: frontend.ActivityIdle,
		Items: []frontend.PresentationItem{{
			ID: "tool:turn-1:call-secret", TurnID: "turn-1", Sequence: 1, Revision: 1,
			Kind: frontend.PresentationToolCall, Lifecycle: frontend.PresentationCompleted,
			Tool: &frontend.ToolState{
				TurnID: "turn-1", CallID: "call-secret", Name: "repository_diff",
				Status: frontend.ToolSucceeded, RepositoryDiff: diff,
			},
		}},
	}

	snapshot := SnapshotFromFrontend(source, nil)
	if err := validateSnapshot(binding.ControlIdentity(), snapshot); err != nil {
		t.Fatalf("validateSnapshot() error = %v", err)
	}
	if len(snapshot.Items) != 1 || snapshot.Items[0].Tool == nil ||
		snapshot.Items[0].Tool.RepositoryDiff == nil || !snapshot.Items[0].Tool.Truncated {
		t.Fatalf("secret-shaped repository diff projection = %#v", snapshot.Items)
	}
	encoded, err := json.Marshal(snapshot.Items[0].Tool.RepositoryDiff)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, secret := range []string{
		"private-material", "BEGIN PRIVATE KEY", "END PRIVATE KEY", "123456789abcdef", "abcdefghijklmnop",
	} {
		if strings.Contains(text, secret) {
			t.Fatalf("worker repository diff leaked %q: %s", secret, text)
		}
	}
	for _, marker := range []string{"[PRIVATE KEY REDACTED]", "[REDACTED]"} {
		if !strings.Contains(text, marker) {
			t.Fatalf("worker repository diff lacks %q: %s", marker, text)
		}
	}
}

func TestRepositoryDiffValidationRejectsSecretShapedFields(t *testing.T) {
	valid := func() RepositoryDiff {
		return RepositoryDiff{
			SchemaVersion:    codingworkspace.RepositoryDiffSchemaV1,
			Target:           RepositoryDiffTarget{Kind: string(codingworkspace.DiffTargetCurrent)},
			ResolvedRevision: "revision", MergeBase: "merge-base", Head: "head", Branch: "branch",
			Generation: "generation", EvidenceGeneration: "evidence-generation",
			UnavailableReason: "unavailable", Warning: "warning", BaselineID: "baseline",
			Files: []RepositoryDiffFile{{
				Path: "safe.go", OriginalPath: "old.go", Status: "M", Omitted: "omitted",
				ProvenanceReason: "file reason",
				Hunks: []RepositoryDiffHunk{{
					Header: "header",
					Lines:  []RepositoryDiffLine{{Kind: "addition", NewLine: 1, Text: "safe"}},
				}},
			}},
			Additions:  1,
			Provenance: &RepositoryDiffProvenance{Reason: "repository reason"},
		}
	}
	if !validRepositoryDiff(valid()) {
		t.Fatal("valid repository diff fixture was rejected")
	}

	for name, mutate := range map[string]func(*RepositoryDiff){
		"target ref": func(diff *RepositoryDiff) {
			diff.Target = RepositoryDiffTarget{Kind: string(codingworkspace.DiffTargetBase), Ref: "sk-123456789abcdef"}
		},
		"resolved revision":   func(diff *RepositoryDiff) { diff.ResolvedRevision = repositoryDiffPrivateKeyFixture },
		"merge base":          func(diff *RepositoryDiff) { diff.MergeBase = repositoryDiffPrivateKeyFixture },
		"head":                func(diff *RepositoryDiff) { diff.Head = repositoryDiffPrivateKeyFixture },
		"branch":              func(diff *RepositoryDiff) { diff.Branch = repositoryDiffPrivateKeyFixture },
		"generation":          func(diff *RepositoryDiff) { diff.Generation = repositoryDiffPrivateKeyFixture },
		"evidence generation": func(diff *RepositoryDiff) { diff.EvidenceGeneration = repositoryDiffPrivateKeyFixture },
		"unavailable reason":  func(diff *RepositoryDiff) { diff.UnavailableReason = repositoryDiffPrivateKeyFixture },
		"warning":             func(diff *RepositoryDiff) { diff.Warning = repositoryDiffPrivateKeyFixture },
		"baseline id":         func(diff *RepositoryDiff) { diff.BaselineID = repositoryDiffPrivateKeyFixture },
		"repository reason":   func(diff *RepositoryDiff) { diff.Provenance.Reason = repositoryDiffPrivateKeyFixture },
		"path":                func(diff *RepositoryDiff) { diff.Files[0].Path = "sk-123456789abcdef" },
		"original path":       func(diff *RepositoryDiff) { diff.Files[0].OriginalPath = repositoryDiffPrivateKeyFixture },
		"status":              func(diff *RepositoryDiff) { diff.Files[0].Status = "Bearer abcdefghijklmnop" },
		"omitted":             func(diff *RepositoryDiff) { diff.Files[0].Omitted = repositoryDiffPrivateKeyFixture },
		"file reason": func(diff *RepositoryDiff) {
			diff.Files[0].ProvenanceReason = repositoryDiffPrivateKeyFixture
		},
		"hunk header": func(diff *RepositoryDiff) { diff.Files[0].Hunks[0].Header = repositoryDiffPrivateKeyFixture },
		"line text":   func(diff *RepositoryDiff) { diff.Files[0].Hunks[0].Lines[0].Text = repositoryDiffPrivateKeyFixture },
	} {
		t.Run(name, func(t *testing.T) {
			diff := valid()
			mutate(&diff)
			if validRepositoryDiff(diff) {
				t.Fatalf("secret-shaped %s was accepted: %#v", name, diff)
			}
		})
	}
}
