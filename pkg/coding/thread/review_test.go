package thread

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	codingreview "github.com/bogdanovich/mintclaw/pkg/coding/review"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
)

func TestStorePublishesImmutableReviewResultUnderThreadLease(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	project, err := ResolveProject(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := NewMetadata(NewThreadID(), project, "review", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionThread(metadata.ThreadID); err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	result := codingreview.Result{
		SchemaVersion: codingreview.SchemaVersion, ReviewID: codingreview.NewID(),
		Target: codingreview.Target{Kind: codingreview.TargetCurrent}, EvidenceGeneration: "generation-1",
		Summary: "No findings.", CompletedAt: time.Now().UTC(),
	}
	diff := codingworkspace.DiffResult{
		SchemaVersion:       codingworkspace.RepositoryDiffSchemaV1,
		RepositoryAvailable: true,
		Target: codingworkspace.DiffTarget{
			Kind: codingworkspace.DiffTargetCurrent,
		},
		EvidenceGeneration: "generation-1",
	}
	invalid := result.Clone()
	invalid.ReviewID = codingreview.NewID()
	invalid.Findings = []codingreview.Finding{{
		Severity: codingreview.SeverityMajor, Title: "Invalid location", Explanation: "No changed line exists.",
		Confidence: 1, LocationState: codingreview.LocationCurrent, Path: "missing.go", StartLine: 1, EndLine: 1,
	}}
	if err := store.PublishReviewResult(t.Context(), lease, metadata, invalid, diff); err == nil {
		t.Fatal("review publication accepted an unproven current location")
	}
	partial := result.Clone()
	partial.ReviewID = codingreview.NewID()
	partialDiff := diff
	partialDiff.Truncated = true
	if err := store.PublishReviewResult(t.Context(), lease, metadata, partial, partialDiff); err == nil {
		t.Fatal("review publication presented truncated evidence as complete")
	}
	if err := store.PublishReviewResult(t.Context(), lease, metadata, result, diff); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadReviewResultWithLease(t.Context(), lease, metadata, result.ReviewID)
	if err != nil || loaded.ReviewID != result.ReviewID || loaded.Summary != result.Summary {
		t.Fatalf("loaded review = %#v, %v", loaded, err)
	}
	latest, ok, err := store.LoadLatestReviewResultWithLease(t.Context(), lease, metadata)
	if err != nil || !ok || !reflect.DeepEqual(latest, result) {
		t.Fatalf("latest review = %#v, ok=%t, error=%v", latest, ok, err)
	}
	if err := store.PublishReviewResult(
		t.Context(),
		lease,
		metadata,
		result,
		diff,
	); !errors.Is(
		err,
		ErrReviewResultExists,
	) {
		t.Fatalf("second publication error = %v", err)
	}
	path := filepath.Join(
		root,
		"coding",
		"threads",
		metadata.ThreadID,
		"repository",
		"reviews",
		result.ReviewID+".json",
	)
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("review result file = %#v, %v", info, err)
	}
}

func TestStoreLatestReviewPointerAdvancesAndSurvivesCompactionMetadata(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	project, err := ResolveProject(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, time.September, 5, 1, 0, 0, 0, time.UTC)
	metadata, err := NewMetadata(NewThreadID(), project, "review recovery", createdAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionThread(metadata.ThreadID); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(metadata); err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	diff := codingworkspace.DiffResult{
		SchemaVersion: codingworkspace.RepositoryDiffSchemaV1, RepositoryAvailable: true,
		Target:             codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetCurrent},
		EvidenceGeneration: "generation-1",
	}
	first := codingreview.Result{
		SchemaVersion: codingreview.SchemaVersion, ReviewID: codingreview.NewID(),
		Target: codingreview.Target{Kind: codingreview.TargetCurrent}, EvidenceGeneration: "generation-1",
		Summary: "First result.", CompletedAt: createdAt.Add(time.Minute),
	}
	second := first.Clone()
	second.ReviewID = codingreview.NewID()
	second.Summary = "Second result."
	second.CompletedAt = createdAt.Add(2 * time.Minute)
	for _, result := range []codingreview.Result{first, second} {
		if err := store.PublishReviewResult(t.Context(), lease, metadata, result, diff); err != nil {
			t.Fatal(err)
		}
	}
	metadata.Compaction = &Compaction{At: createdAt.Add(3 * time.Minute), Revision: 7}
	metadata.UpdatedAt = metadata.Compaction.At
	if err := store.Save(metadata); err != nil {
		t.Fatal(err)
	}
	latest, ok, err := store.LoadLatestReviewResultWithLease(t.Context(), lease, metadata)
	if err != nil || !ok || !reflect.DeepEqual(latest, second) {
		t.Fatalf("latest review after compaction = %#v, ok=%t, error=%v", latest, ok, err)
	}
	pointerPath := filepath.Join(
		root, "coding", "threads", metadata.ThreadID, repositoryDirectory, reviewDirectory, reviewLatestFileName,
	)
	if err := os.WriteFile(pointerPath, bytes.Repeat([]byte{'x'}, maxReviewIndexBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.LoadLatestReviewResultWithLease(t.Context(), lease, metadata); err == nil ||
		!strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized latest review pointer error = %v", err)
	}
}

func TestStoreLatestReviewIsAbsentUntilCompletedResultPublication(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	project, err := ResolveProject(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := NewMetadata(NewThreadID(), project, "no completed review", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionThread(metadata.ThreadID); err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	result, ok, err := store.LoadLatestReviewResultWithLease(t.Context(), lease, metadata)
	if err != nil || ok || result.ReviewID != "" {
		t.Fatalf("review before completion = %#v, ok=%t, error=%v", result, ok, err)
	}
}

func TestStoreRejectsAmbiguousReviewResultJSON(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	project, err := ResolveProject(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := NewMetadata(NewThreadID(), project, "corrupt review", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionThread(metadata.ThreadID); err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	result := codingreview.Result{
		SchemaVersion: codingreview.SchemaVersion, ReviewID: codingreview.NewID(),
		Target: codingreview.Target{Kind: codingreview.TargetCurrent}, EvidenceGeneration: "generation-1",
		Summary: "One finding.", CompletedAt: time.Now().UTC(),
		Findings: []codingreview.Finding{{
			Severity: codingreview.SeverityMinor, Title: "Explain behavior", Explanation: "The behavior is unclear.",
			Confidence: 0.75, LocationState: codingreview.LocationUnlocated,
		}},
	}
	diff := codingworkspace.DiffResult{
		SchemaVersion:       codingworkspace.RepositoryDiffSchemaV1,
		RepositoryAvailable: true,
		Target: codingworkspace.DiffTarget{
			Kind: codingworkspace.DiffTargetCurrent,
		},
		EvidenceGeneration: "generation-1",
	}
	if err := store.PublishReviewResult(t.Context(), lease, metadata, result, diff); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(
		root, "coding", "threads", metadata.ThreadID, "repository", "reviews", result.ReviewID+".json",
	)
	valid, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadReviewResultWithLease(t.Context(), lease, metadata, result.ReviewID)
	if err != nil || !reflect.DeepEqual(loaded, result) {
		t.Fatalf("loaded review with required confidence = %#v, %v", loaded, err)
	}
	missingConfidence := bytes.Replace(valid, []byte(`"confidence": 0.75,`), nil, 1)
	if bytes.Equal(missingConfidence, valid) {
		t.Fatal("confidence fixture was not found")
	}
	tests := map[string][]byte{
		"duplicate root member": bytes.Replace(
			valid, []byte(`"summary":`), []byte(`"summary": "shadow", "summary":`), 1,
		),
		"duplicate nested member": bytes.Replace(
			valid, []byte(`"kind": "current"`), []byte(`"kind": "current", "kind": "current"`), 1,
		),
		"case-folded root alias": bytes.Replace(valid, []byte(`"summary":`), []byte(`"SUMMARY":`), 1),
		"case-folded nested alias": bytes.Replace(
			valid, []byte(`"kind": "current"`), []byte(`"KIND": "current"`), 1,
		),
		"missing confidence": missingConfidence,
		"null confidence": bytes.Replace(
			valid, []byte(`"confidence": 0.75`), []byte(`"confidence": null`), 1,
		),
		"invalid utf8": bytes.Replace(valid, []byte("One finding."), []byte{'O', 'n', 0xff}, 1),
	}
	for name, corrupted := range tests {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, corrupted, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.LoadReviewResultWithLease(
				t.Context(), lease, metadata, result.ReviewID,
			); err == nil {
				t.Fatal("ambiguous review JSON was accepted")
			}
		})
	}
}

func TestStoreRejectsReplacedWriterAuthorityAfterReviewRead(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	project, err := ResolveProject(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := NewMetadata(NewThreadID(), project, "review authority", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionThread(metadata.ThreadID); err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	result := codingreview.Result{
		SchemaVersion: codingreview.SchemaVersion, ReviewID: codingreview.NewID(),
		Target: codingreview.Target{Kind: codingreview.TargetCurrent}, EvidenceGeneration: "generation-1",
		Summary: "No findings.", CompletedAt: time.Now().UTC(),
	}
	diff := codingworkspace.DiffResult{
		SchemaVersion:       codingworkspace.RepositoryDiffSchemaV1,
		RepositoryAvailable: true,
		Target: codingworkspace.DiffTarget{
			Kind: codingworkspace.DiffTargetCurrent,
		},
		EvidenceGeneration: "generation-1",
	}
	if err := store.PublishReviewResult(t.Context(), lease, metadata, result, diff); err != nil {
		t.Fatal(err)
	}
	threadRoot, err := store.ThreadRoot(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(threadRoot, leaseFileName)
	store.afterReviewResultRead = func() {
		if err := os.Rename(lockPath, lockPath+"-replaced"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(lockPath, []byte("replacement\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.LoadReviewResultWithLease(
		t.Context(), lease, metadata, result.ReviewID,
	); err == nil || !strings.Contains(err.Error(), "held lease") {
		t.Fatalf("review load with replaced writer authority error = %v", err)
	}
}

func TestStrictReviewJSONRejectsExcessiveNesting(t *testing.T) {
	data := bytes.Repeat([]byte{'['}, maxReviewJSONNesting+2)
	data = append(data, '0')
	data = append(data, bytes.Repeat([]byte{']'}, maxReviewJSONNesting+2)...)
	if err := validateStrictReviewJSON(data); err == nil {
		t.Fatal("excessively nested review JSON was accepted")
	}
}

func TestStoreRoundTripsBaseAndCommitReviewEvidenceIdentity(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	project, err := ResolveProject(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := NewMetadata(NewThreadID(), project, "review targets", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionThread(metadata.ThreadID); err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })

	tests := []struct {
		name   string
		result codingreview.Result
		diff   codingworkspace.DiffResult
	}{
		{
			name: "base",
			result: codingreview.Result{
				Target:             codingreview.Target{Kind: codingreview.TargetBase, Ref: "main"},
				EvidenceGeneration: "generation-1", ResolvedRevision: "base-tip", MergeBase: "merge-base",
			},
			diff: codingworkspace.DiffResult{
				SchemaVersion:       codingworkspace.RepositoryDiffSchemaV1,
				RepositoryAvailable: true,
				Target:              codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetBase, Ref: "main"},
				EvidenceGeneration:  "generation-1", ResolvedRevision: "base-tip", MergeBase: "merge-base",
			},
		},
		{
			name: "commit",
			result: codingreview.Result{
				Target:           codingreview.Target{Kind: codingreview.TargetCommit, Ref: "HEAD"},
				ResolvedRevision: "commit-sha",
			},
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
			test.result.SchemaVersion = codingreview.SchemaVersion
			test.result.ReviewID = codingreview.NewID()
			test.result.Summary = "No findings."
			test.result.CompletedAt = time.Now().UTC()
			if err := store.PublishReviewResult(t.Context(), lease, metadata, test.result, test.diff); err != nil {
				t.Fatal(err)
			}
			loaded, err := store.LoadReviewResultWithLease(t.Context(), lease, metadata, test.result.ReviewID)
			if err != nil || !reflect.DeepEqual(loaded, test.result) {
				t.Fatalf("loaded review = %#v, %v", loaded, err)
			}
		})
	}
}

func TestStoreRejectsReviewResultWithoutOwningLease(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	project, err := ResolveProject(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := NewMetadata(NewThreadID(), project, "review", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	result := codingreview.Result{
		SchemaVersion: codingreview.SchemaVersion, ReviewID: codingreview.NewID(),
		Target: codingreview.Target{Kind: codingreview.TargetCurrent}, EvidenceGeneration: "generation-1",
		Summary: "No findings.", CompletedAt: time.Now().UTC(),
	}
	diff := codingworkspace.DiffResult{
		SchemaVersion:       codingworkspace.RepositoryDiffSchemaV1,
		RepositoryAvailable: true,
		Target: codingworkspace.DiffTarget{
			Kind: codingworkspace.DiffTargetCurrent,
		},
		EvidenceGeneration: "generation-1",
	}
	if err := store.PublishReviewResult(t.Context(), nil, metadata, result, diff); err == nil {
		t.Fatal("review publication accepted a nil lease")
	}
}
