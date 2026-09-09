package frontend

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	codingreview "github.com/bogdanovich/mintclaw/pkg/coding/review"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
)

func TestReviewProjectionCorrelatesEventsAndCompletedResult(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	reviewID := codingreview.NewID()
	target := codingreview.Target{Kind: codingreview.TargetCurrent}
	if err := projector.ReviewEntered(reviewID, target); err != nil {
		t.Fatal(err)
	}
	finding := codingreview.Finding{
		Severity: codingreview.SeverityMinor, Title: "Check error", Explanation: "An error is ignored.",
		Confidence: 0.8, LocationState: codingreview.LocationCurrent, Path: "main.go", StartLine: 7, EndLine: 7,
	}
	if err := projector.ReviewEvent(
		reviewID,
		codingreview.Event{Kind: codingreview.EventFinding, Finding: &finding},
	); err != nil {
		t.Fatal(err)
	}
	result := codingreview.Result{
		SchemaVersion: codingreview.SchemaVersion, ReviewID: reviewID, Target: target,
		EvidenceGeneration: "generation-1", Summary: "One finding.", Findings: []codingreview.Finding{finding},
		CompletedAt: time.Now().UTC(),
	}
	if err := projector.ReviewCompleted(result); err != nil {
		t.Fatal(err)
	}
	view := snapshotForTest(t, projector)
	if view.Activity != ActivityIdle || view.Review == nil || view.Review.Phase != codingreview.PhaseCompleted ||
		view.Review.Result == nil || view.Review.Result.Summary != result.Summary {
		t.Fatalf("completed review projection = %#v", view.Review)
	}
	view.Review.Result.Findings[0].Title = "consumer mutation"
	if stable := snapshotForTest(t, projector); stable.Review.Result.Findings[0].Title != finding.Title {
		t.Fatal("review result aliases consumer-owned state")
	}
}

func TestSteeringMovesFromPendingSurfaceToTranscriptOnInjection(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	projector.TurnStarted("turn-1", "inspect")
	projector.SteeringAccepted("turn-1", SteerInput{ID: "steer-1", Text: "focus on the parser"})

	pending := snapshotForTest(t, projector)
	if pending.Activity != ActivityRunning || len(pending.PendingInputs) != 1 ||
		pending.PendingInputs[0].Text != "focus on the parser" || len(pending.Entries) != 1 {
		t.Fatalf("pending steering snapshot = %+v", pending)
	}
	pending.PendingInputs[0].Text = "consumer mutation"
	if stable := snapshotForTest(t, projector); stable.PendingInputs[0].Text != "focus on the parser" {
		t.Fatal("pending steering aliases consumer-owned state")
	}

	projector.SteeringInjected("turn-1", []SteerInput{{ID: "steer-1", Text: "focus on the parser"}})
	injected := snapshotForTest(t, projector)
	if len(injected.PendingInputs) != 0 || len(injected.Entries) != 2 ||
		injected.Entries[1].Kind != EntryUser || injected.Entries[1].Text != "focus on the parser" {
		t.Fatalf("injected steering snapshot = %+v", injected)
	}
	projector.SteeringInjected("turn-1", []SteerInput{{ID: "steer-1", Text: "focus on the parser"}})
	if duplicate := snapshotForTest(t, projector); len(duplicate.Entries) != 2 {
		t.Fatalf("duplicate receipt duplicated transcript: %+v", duplicate.Entries)
	}
}

func TestPendingSteeringIsExcludedFromSerializedSnapshots(t *testing.T) {
	const (
		privateID   = "private-steer-id"
		privateText = "private same-turn guidance"
	)
	snapshot := ThreadSnapshot{
		ThreadID: "thread-1",
		PendingInputs: []PendingInputState{{
			ID: privateID, TurnID: "turn-1", Text: privateText, Truncated: true,
		}},
	}
	for _, value := range []any{snapshot, snapshot.PendingInputs[0]} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), privateID) || strings.Contains(string(encoded), privateText) ||
			strings.Contains(string(encoded), "pending_inputs") {
			t.Fatalf("serialized pending steering leaked presentation-only state: %s", encoded)
		}
	}
}

func TestSteeringPendingStateIsBoundedAndClearedAtTerminalTurn(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{TextBytes: 8 << 10})
	projector.TurnStarted("turn-1", "inspect")
	projector.SteeringAccepted("other-turn", SteerInput{ID: "wrong", Text: "ignore"})
	projector.SteeringAccepted("turn-1", SteerInput{ID: "steer-1", Text: strings.Repeat("x", 4<<10)})

	snapshot := snapshotForTest(t, projector)
	if len(snapshot.PendingInputs) != 1 || !snapshot.PendingInputs[0].Truncated ||
		len(snapshot.PendingInputs[0].Text) > defaultPendingTextBytes {
		t.Fatalf("bounded pending steering = %+v", snapshot.PendingInputs)
	}
	projector.TurnFailed("turn-1", "failed")
	if terminal := snapshotForTest(t, projector); len(terminal.PendingInputs) != 0 {
		t.Fatalf("terminal turn retained pending steering: %+v", terminal.PendingInputs)
	}

	projector = newTestProjector(t, ProjectionLimits{})
	projector.TurnStarted("turn-2", "inspect")
	for index := range MaxSteersPerTurn + 2 {
		projector.SteeringAccepted("turn-2", SteerInput{
			ID: fmt.Sprintf("steer-%d", index), Text: "bounded guidance",
		})
	}
	bounded := snapshotForTest(t, projector)
	if len(bounded.PendingInputs) != MaxSteersPerTurn || bounded.PendingInputs[0].ID != "steer-2" {
		t.Fatalf("pending steering count bound = %+v", bounded.PendingInputs)
	}
}

func TestReviewProjectionRejectsInvalidEventsAndIgnoresMismatchedCompletion(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	reviewID := codingreview.NewID()
	target := codingreview.Target{Kind: codingreview.TargetCurrent}
	if err := projector.ReviewEntered(reviewID, target); err != nil {
		t.Fatal(err)
	}
	if err := projector.ReviewEvent("not-a-review-id", codingreview.Event{
		Kind: codingreview.EventProgress, Progress: "working",
	}); err == nil {
		t.Fatal("ReviewEvent() accepted an invalid review ID")
	}
	result := codingreview.Result{
		SchemaVersion: codingreview.SchemaVersion, ReviewID: reviewID,
		Target:             codingreview.Target{Kind: codingreview.TargetCurrent, Instructions: "different"},
		EvidenceGeneration: "generation-1", Summary: "No findings.", CompletedAt: time.Now().UTC(),
	}
	if err := projector.ReviewCompleted(result); err != nil {
		t.Fatal(err)
	}
	view := snapshotForTest(t, projector)
	if view.Activity != ActivityReviewing || view.Review == nil || view.Review.Phase != codingreview.PhaseEntered {
		t.Fatalf("mismatched completion changed active projection = %#v", view.Review)
	}
}

func TestReviewRestoredProjectsCompletedStateWithoutLiveAuthority(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	result := codingreview.Result{
		SchemaVersion:      codingreview.SchemaVersion,
		ReviewID:           codingreview.NewID(),
		Target:             codingreview.Target{Kind: codingreview.TargetCurrent},
		EvidenceGeneration: "generation-1",
		Summary:            "Restored result.",
		CompletedAt:        time.Now().UTC(),
	}
	if err := projector.ReviewRestored(result); err != nil {
		t.Fatal(err)
	}
	snapshot := snapshotForTest(t, projector)
	if snapshot.Activity != ActivityIdle || snapshot.Review == nil ||
		snapshot.Review.Phase != codingreview.PhaseCompleted || snapshot.Review.Result == nil ||
		snapshot.Review.Result.ReviewID != result.ReviewID {
		t.Fatalf("restored review projection = %#v", snapshot.Review)
	}
	result.Summary = "caller mutation"
	if stable := snapshotForTest(t, projector); stable.Review.Result.Summary != "Restored result." {
		t.Fatal("restored review aliases caller state")
	}
}

func TestWorkspaceUpdateDoesNotAliasCallerOrConsumerState(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	workspace := codingworkspace.Snapshot{
		ProjectRoot: "/repo",
		CWD:         "/repo/subdir",
		Git:         codingworkspace.GitState{Available: true, Branch: "main", Dirty: true},
		ChangedPaths: []codingworkspace.ChangedPath{
			{Path: "changed.go", Status: " M"},
		},
	}
	projector.WorkspaceUpdated(workspace)
	workspace.ChangedPaths[0].Path = "caller-mutated.go"

	view := snapshotForTest(t, projector)
	if view.Workspace == nil || view.Workspace.ChangedPaths[0].Path != "changed.go" {
		t.Fatalf("workspace view = %+v", view.Workspace)
	}
	view.Workspace.ChangedPaths[0].Path = "consumer-mutated.go"
	stable := snapshotForTest(t, projector)
	if stable.Workspace.ChangedPaths[0].Path != "changed.go" {
		t.Fatalf("projector workspace was aliased: %+v", stable.Workspace)
	}
}

func TestRuntimeStatusProjectionIsBoundedNormalizedAndIndependent(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{TextBytes: 64})
	sources := make([]InstructionSource, maxInstructionSources+2)
	for index := range sources {
		sources[index] = InstructionSource{
			Path:  fmt.Sprintf("/project/scope-%02d/AGENTS.md", index),
			Scope: fmt.Sprintf("/project/scope-%02d", index),
			Label: "AGENTS.md",
		}
	}
	status := RuntimeStatus{
		Version:                 "v1.2.3",
		Resumed:                 true,
		ReasoningEffort:         "medium",
		ReasoningConfigured:     true,
		Permission:              PermissionFullAccess,
		Autonomy:                AutonomyYolo,
		InstructionSources:      sources,
		InstructionWarningCount: -1,
		Account: &ProviderAccount{
			Provider: "openai", AuthMethod: "oauth", State: ProviderAccountAuthenticated,
		},
	}
	projector.RuntimeStatusUpdated(status)
	status.InstructionSources[0].Path = "caller mutation"
	status.Account.Provider = "caller mutation"

	view := snapshotForTest(t, projector)
	if view.Runtime == nil || !view.Runtime.Resumed || view.Runtime.Permission != PermissionFullAccess ||
		view.Runtime.Autonomy != AutonomyYolo || len(view.Runtime.InstructionSources) != maxInstructionSources ||
		!view.Runtime.InstructionSourcesTruncated || view.Runtime.InstructionWarningCount != 0 ||
		view.Runtime.InstructionSources[0].Path == "caller mutation" ||
		view.Runtime.Account == nil || view.Runtime.Account.Provider != "openai" {
		t.Fatalf("runtime status projection = %+v", view.Runtime)
	}
	view.Runtime.InstructionSources[0].Path = "consumer mutation"
	view.Runtime.Account.Provider = "consumer mutation"
	stable := snapshotForTest(t, projector)
	if stable.Runtime.InstructionSources[0].Path == "consumer mutation" ||
		stable.Runtime.Account.Provider != "openai" {
		t.Fatalf("runtime status aliases consumer state = %+v", stable.Runtime)
	}

	projector.RuntimeStatusUpdated(RuntimeStatus{
		Permission: "forged", Autonomy: "prompt_every_time",
		Account: &ProviderAccount{State: "forged"},
	})
	view = snapshotForTest(t, projector)
	if view.Runtime.Permission != "" || view.Runtime.Autonomy != "" || view.Runtime.Account.State != "" {
		t.Fatalf("invalid runtime enums survived normalization = %+v", view.Runtime)
	}
}

func TestRepositoryEvidenceUpdatesDoNotAliasNestedState(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	status := codingworkspace.StatusResult{
		SchemaVersion: codingworkspace.RepositoryStatusSchemaV1,
		Provenance: &codingworkspace.ProvenanceResult{Paths: []codingworkspace.ProvenancePath{{
			Path: "status.go", Provenance: codingworkspace.ProvenancePreExisting,
		}}},
	}
	diff := codingworkspace.DiffResult{
		SchemaVersion: codingworkspace.RepositoryDiffSchemaV1,
		Files: []codingworkspace.DiffFile{{Path: "diff.go", Hunks: []codingworkspace.DiffHunk{{
			Lines: []codingworkspace.DiffLine{{Kind: "addition", Text: "new"}},
		}}}},
	}
	projector.RepositoryStatusUpdated(status)
	projector.RepositoryDiffUpdated(diff)
	status.Provenance.Paths[0].Path = "caller-status.go"
	diff.Files[0].Hunks[0].Lines[0].Text = "caller"

	view := snapshotForTest(t, projector)
	if view.RepositoryStatus.Provenance.Paths[0].Path != "status.go" ||
		view.RepositoryDiff.Files[0].Hunks[0].Lines[0].Text != "new" {
		t.Fatalf("repository view = %#v / %#v", view.RepositoryStatus, view.RepositoryDiff)
	}
	view.RepositoryStatus.Provenance.Paths[0].Path = "consumer-status.go"
	view.RepositoryDiff.Files[0].Hunks[0].Lines[0].Text = "consumer"
	stable := snapshotForTest(t, projector)
	if stable.RepositoryStatus.Provenance.Paths[0].Path != "status.go" ||
		stable.RepositoryDiff.Files[0].Hunks[0].Lines[0].Text != "new" {
		t.Fatalf(
			"repository projection aliased consumer state = %#v / %#v",
			stable.RepositoryStatus,
			stable.RepositoryDiff,
		)
	}
}

func TestRepositoryStatusAdvanceClearsObsoleteMutableDiff(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	before := codingworkspace.Snapshot{ProjectRoot: "/repo", CWD: "/repo"}
	projector.RepositoryStatusUpdated(codingworkspace.StatusResult{Snapshot: before})
	projector.RepositoryDiffUpdated(codingworkspace.DiffResult{
		Target:     codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetCurrent},
		Generation: before.Identity(),
	})
	after := before
	after.Git.Dirty = true
	projector.RepositoryStatusUpdated(codingworkspace.StatusResult{Snapshot: after})
	if snapshot := snapshotForTest(t, projector); snapshot.RepositoryDiff != nil {
		t.Fatalf("obsolete current diff was retained = %#v", snapshot.RepositoryDiff)
	}
	projector.RepositoryDiffUpdated(codingworkspace.DiffResult{
		Target:     codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetCurrent},
		Generation: after.Identity(), EvidenceGeneration: "old-content",
	})
	projector.RepositoryStatusUpdated(codingworkspace.StatusResult{
		Snapshot: after,
		Provenance: &codingworkspace.ProvenanceResult{
			CurrentEvidenceGeneration: "new-content",
		},
	})
	if snapshot := snapshotForTest(t, projector); snapshot.RepositoryDiff != nil {
		t.Fatalf("content-stale current diff was retained = %#v", snapshot.RepositoryDiff)
	}
	projector.RepositoryDiffUpdated(codingworkspace.DiffResult{
		Target:     codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetBase, Ref: "main"},
		Generation: before.Identity(),
	})
	projector.RepositoryStatusUpdated(codingworkspace.StatusResult{Snapshot: after})
	if snapshot := snapshotForTest(t, projector); snapshot.RepositoryDiff != nil {
		t.Fatalf("obsolete base diff was retained = %#v", snapshot.RepositoryDiff)
	}
	projector.RepositoryDiffUpdated(codingworkspace.DiffResult{
		Target:     codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetBase, Ref: "main"},
		Generation: after.Identity(), EvidenceGeneration: "old-base-content",
	})
	projector.RepositoryStatusUpdated(codingworkspace.StatusResult{
		Snapshot: after,
		Provenance: &codingworkspace.ProvenanceResult{
			CurrentEvidenceGeneration: "new-base-content",
		},
	})
	if snapshot := snapshotForTest(t, projector); snapshot.RepositoryDiff != nil {
		t.Fatalf("content-stale base diff was retained = %#v", snapshot.RepositoryDiff)
	}

	projector.RepositoryDiffUpdated(codingworkspace.DiffResult{
		Target:     codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetCommit, Ref: "HEAD"},
		Generation: before.Identity(),
	})
	projector.RepositoryStatusUpdated(codingworkspace.StatusResult{Snapshot: before})
	if snapshot := snapshotForTest(t, projector); snapshot.RepositoryDiff == nil {
		t.Fatal("immutable commit diff was cleared by status refresh")
	}
}

func TestToolRepositoryDiffRemainsHistoricalAcrossCurrentDiffRefresh(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	projector.ToolStarted("turn-1", "call-1", "repository_diff", "target:string")
	historical := codingworkspace.DiffResult{
		SchemaVersion: codingworkspace.RepositoryDiffSchemaV1,
		Target:        codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetCurrent},
		Generation:    "historical-generation",
		Files: []codingworkspace.DiffFile{{
			Path: "historical.go",
			Hunks: []codingworkspace.DiffHunk{{Lines: []codingworkspace.DiffLine{{
				Kind: "addition", NewLine: 1, Text: "historical",
			}}}},
		}},
		Additions: 1,
	}
	projector.ToolRepositoryDiff("turn-1", "call-1", historical)
	projector.ToolCompleted("turn-1", "call-1", "repository_diff", "", time.Second, false, nil)
	projector.RepositoryDiffUpdated(codingworkspace.DiffResult{
		SchemaVersion: codingworkspace.RepositoryDiffSchemaV1,
		Target:        codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetCurrent},
		Generation:    "current-generation",
		Files:         []codingworkspace.DiffFile{{Path: "current.go"}},
	})

	snapshot := snapshotForTest(t, projector)
	if snapshot.RepositoryDiff == nil || snapshot.RepositoryDiff.Files[0].Path != "current.go" ||
		len(snapshot.Tools) != 1 || snapshot.Tools[0].RepositoryDiff == nil ||
		snapshot.Tools[0].RepositoryDiff.Generation != "historical-generation" ||
		snapshot.Tools[0].RepositoryDiff.Files[0].Path != "historical.go" {
		t.Fatalf("current/historical repository diff = %#v / %#v", snapshot.RepositoryDiff, snapshot.Tools)
	}
	snapshot.Tools[0].RepositoryDiff.Files[0].Path = "consumer.go"
	stable := snapshotForTest(t, projector)
	if stable.Tools[0].RepositoryDiff.Files[0].Path != "historical.go" {
		t.Fatalf("historical repository diff aliases consumer: %#v", stable.Tools[0].RepositoryDiff)
	}
}

func TestWorkspaceAdvanceInvalidatesMutableRepositoryEvidence(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	workspace := codingworkspace.Snapshot{ProjectRoot: "/repo", CWD: "/repo"}
	projector.RepositoryStatusUpdated(codingworkspace.StatusResult{Snapshot: workspace})
	projector.RepositoryDiffUpdated(codingworkspace.DiffResult{
		Target: codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetBase, Ref: "main"},
	})
	projector.WorkspaceUpdated(workspace)
	snapshot := snapshotForTest(t, projector)
	if snapshot.RepositoryStatus != nil || snapshot.RepositoryDiff != nil {
		t.Fatalf(
			"workspace advance retained mutable evidence = %#v / %#v",
			snapshot.RepositoryStatus,
			snapshot.RepositoryDiff,
		)
	}

	projector.RepositoryStatusUpdated(codingworkspace.StatusResult{Snapshot: workspace})
	projector.RepositoryDiffUpdated(codingworkspace.DiffResult{
		Target: codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetCommit, Ref: "HEAD"},
	})
	projector.WorkspaceUpdated(workspace)
	snapshot = snapshotForTest(t, projector)
	if snapshot.RepositoryStatus != nil || snapshot.RepositoryDiff == nil {
		t.Fatalf(
			"workspace advance did not preserve only immutable diff = %#v / %#v",
			snapshot.RepositoryStatus,
			snapshot.RepositoryDiff,
		)
	}
}

func TestRepositoryStatusDoesNotValidateMutableDiffWithIncompleteEvidence(t *testing.T) {
	workspace := codingworkspace.Snapshot{ProjectRoot: "/repo", CWD: "/repo"}
	for _, test := range []struct {
		name   string
		diff   codingworkspace.DiffResult
		status codingworkspace.StatusResult
	}{
		{
			name: "missing diff evidence generation",
			diff: codingworkspace.DiffResult{
				Target:     codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetCurrent},
				Generation: workspace.Identity(),
			},
			status: completeStatusEvidence(workspace, "current"),
		},
		{
			name: "indeterminate provenance",
			diff: codingworkspace.DiffResult{
				Target:     codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetBase},
				Generation: workspace.Identity(), EvidenceGeneration: "current",
			},
			status: codingworkspace.StatusResult{
				Snapshot: workspace,
				Provenance: &codingworkspace.ProvenanceResult{
					CurrentEvidenceGeneration: "current", Indeterminate: true,
				},
			},
		},
		{
			name: "stale status",
			diff: codingworkspace.DiffResult{
				Target:     codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetCurrent},
				Generation: workspace.Identity(), EvidenceGeneration: "current",
			},
			status: func() codingworkspace.StatusResult {
				status := completeStatusEvidence(workspace, "current")
				status.Stale = true
				return status
			}(),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			projector := newTestProjector(t, ProjectionLimits{})
			projector.RepositoryDiffUpdated(test.diff)
			projector.RepositoryStatusUpdated(test.status)
			if snapshot := snapshotForTest(t, projector); snapshot.RepositoryDiff != nil {
				t.Fatalf("incomplete evidence retained mutable diff = %#v", snapshot.RepositoryDiff)
			}
		})
	}
}

func completeStatusEvidence(
	workspace codingworkspace.Snapshot,
	evidenceGeneration string,
) codingworkspace.StatusResult {
	return codingworkspace.StatusResult{
		Snapshot: workspace,
		Provenance: &codingworkspace.ProvenanceResult{
			CurrentEvidenceGeneration: evidenceGeneration,
		},
	}
}

func TestSubscribeReturnsCurrentViewAndPublishesLaterViews(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	projector.Open(false)
	ctx, cancel := context.WithCancel(t.Context())
	initial, updates, err := projector.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if initial.Status != "new coding thread" || initial.ThreadID != "thread-1" {
		t.Fatalf("initial view = %+v", initial)
	}

	projector.TurnStarted("turn-1", "fix it")
	updated := <-updates
	if updated.Activity != ActivityRunning || updated.ActiveTurnID != "turn-1" ||
		len(updated.Entries) != 1 || updated.Entries[0].Text != "fix it" {
		t.Fatalf("updated view = %+v", updated)
	}
	updated.Entries[0].Text = "consumer-mutated"
	if stable := snapshotForTest(t, projector); stable.Entries[0].Text != "fix it" {
		t.Fatalf("subscriber aliased projector state: %+v", stable.Entries)
	}

	cancel()
	if _, ok := <-updates; ok {
		t.Fatal("subscription remained open after cancellation")
	}
}

func TestSlowSubscriberConvergesToNewestView(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, updates, err := projector.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}

	projector.TurnStarted("turn-1", "fix it")
	projector.AssistantAccumulated("turn-1", "working", false)
	projector.AssistantAccumulated("turn-1", "done", true)
	projector.TurnCompleted("turn-1", "completed")

	latest := <-updates
	want := snapshotForTest(t, projector)
	if !reflect.DeepEqual(latest, want) {
		t.Fatalf("slow subscriber view = %+v, want %+v", latest, want)
	}
}

func TestLifecycleProjectsOneCurrentView(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	projector.TurnStarted("turn-1", "fix it")
	projector.ToolStarted("turn-1", "call-1", "exec", "fields: command")
	projector.Warning("turn-1", "retry-1", "model request retry 1/2 (rate_limit)")
	projector.ToolCompleted("turn-1", "call-1", "exec", "done", 0, false, []WriteAudit{{
		Kind: "file", Target: "main.go", Action: "update", Success: true,
	}})
	projector.CompactionUpdate(CompactionState{
		TurnID: "turn-1", Reason: "llm_retry", Status: CompactionRunning,
	})
	projector.CompactionUpdate(CompactionState{
		TurnID: "turn-1", Reason: "llm_retry", Status: CompactionCompleted, TokensSaved: 10,
	})
	projector.TurnCompleted("turn-1", "completed")

	view := snapshotForTest(t, projector)
	if view.Activity != ActivityIdle || view.ActiveTurnID != "" ||
		view.LastTurn == nil || view.LastTurn.Outcome != TurnOutcomeCompleted {
		t.Fatalf("terminal view = %+v", view)
	}
	if len(view.Tools) != 1 || view.Tools[0].TurnID != "turn-1" ||
		view.Tools[0].WriteAudit[0].Target != "main.go" {
		t.Fatalf("tool correlation = %+v", view.Tools)
	}
	if view.LastCompaction == nil || view.LastCompaction.Status != CompactionCompleted {
		t.Fatalf("compaction view = %+v", view.LastCompaction)
	}
}

func TestVerifiedFileChangesAreDeduplicatedBoundedAndTyped(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{Tools: 2})
	projector.FilesChanged("turn-1", "call-1", []WriteAudit{
		{Kind: "file", Target: "a.go", Action: "write", Tool: "write_file", Success: true},
		{Kind: "memory", Target: "note", Action: "update", Success: true},
		{Kind: "file", Target: "failed.go", Action: "write", Success: false},
		{Kind: "file", Target: "b.go", Action: "write", Success: true},
		{Kind: "file", Target: "a.go", Action: "update", Tool: "apply_patch", Success: true},
		{Kind: "file", Target: "c.go", Action: "write", Success: true},
	})

	files := snapshotForTest(t, projector).ChangedFiles
	if len(files) != 2 || files[0].Path != "a.go" || files[0].Action != "update" ||
		files[0].Tool != "apply_patch" || files[1].Path != "c.go" {
		t.Fatalf("changed files = %+v", files)
	}
	for _, file := range files {
		if file.TurnID != "turn-1" || file.CallID != "call-1" {
			t.Fatalf("uncorrelated changed file = %+v", file)
		}
	}
}

func TestCommandExitCodeDoesNotAliasProjectorOrConsumerState(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	exitCode := 7
	projector.ToolCommandOutput("turn-1", "call-1", CommandState{
		Status: CommandFailed, ExitCode: &exitCode,
	})
	exitCode = 9

	view := snapshotForTest(t, projector)
	*view.Tools[0].Command.ExitCode = 11
	stable := snapshotForTest(t, projector)
	if got := *stable.Tools[0].Command.ExitCode; got != 7 {
		t.Fatalf("projector exit code = %d, want 7", got)
	}
}

func TestCompletedToolReflectsFailedBackgroundCommand(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	exitCode := 7
	projector.ToolCommandOutput("turn-1", "call-1", CommandState{
		Status: CommandFailed, Background: true, OwnsProcess: true, ExitCode: &exitCode,
	})
	projector.ToolCompleted("turn-1", "call-1", "exec", "", 0, false, nil)
	tools := snapshotForTest(t, projector).Tools
	if len(tools) != 1 || tools[0].Status != ToolFailed {
		t.Fatalf("completed background command tools = %+v", tools)
	}
}

func TestCommandLifecycleCorrelatesEdgesWithoutCrossCallAttachment(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	projector.ToolCommandOutput("turn-1", "late-start", CommandState{
		Status:     CommandRunning,
		Transcript: []CommandTranscriptEntry{{Sequence: 1, Stream: "stdout", Text: "before-start"}},
	})
	projector.ToolStarted("turn-1", "other", "exec", "fields: command")
	projector.ToolCommandOutput("turn-1", "other", CommandState{
		Command: "printf other", Status: CommandRunning, OwnsProcess: true,
		Transcript: []CommandTranscriptEntry{{Sequence: 1, Stream: "stdout", Text: "other-output"}},
	})
	projector.ToolStarted("turn-1", "late-start", "exec", "fields: command")
	projector.ToolCommandOutput("turn-1", "late-start", CommandState{
		Command: "printf late", Status: CommandSucceeded, OwnsProcess: true,
		Transcript: []CommandTranscriptEntry{
			{Sequence: 1, Stream: "stdout", Text: "before-start"},
			{Sequence: 2, Stream: "stdout", Text: "after-start"},
		},
	})
	projector.ToolCommandOutput("turn-1", "late-start", CommandState{
		Status: CommandRunning, OwnsProcess: true,
		Transcript: []CommandTranscriptEntry{{Sequence: 3, Stream: "stdout", Text: "after-completion"}},
	})

	tools := snapshotForTest(t, projector).Tools
	if len(tools) != 2 {
		t.Fatalf("command tools = %+v", tools)
	}
	byCall := make(map[string]ToolState, len(tools))
	for _, tool := range tools {
		byCall[tool.CallID] = tool
	}
	late := byCall["late-start"]
	other := byCall["other"]
	if late.Command == nil || late.Command.Orphan || late.Command.Command != "printf late" ||
		late.Command.Status != CommandSucceeded || len(late.Command.Transcript) != 3 ||
		!strings.Contains(late.Output, "after-start") || !strings.Contains(late.Output, "after-completion") {
		t.Fatalf("late-start command = %+v", late)
	}
	if other.Command == nil || other.Command.Command != "printf other" ||
		strings.Contains(other.Output, "before-start") || strings.Contains(other.Output, "after-start") {
		t.Fatalf("other command received unrelated output: %+v", other)
	}
}

func TestToolExplorationIsBoundedClonedAndRetainedThroughCompletion(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{TextBytes: 16})
	projector.ToolStarted("turn-1", "call-1", "read_file", "fields: path")
	exploration := ExplorationState{
		Operation: ExplorationRead, Path: strings.Repeat("p", 32), Workspace: "build",
	}
	projector.ToolExploration("turn-1", "call-1", exploration)
	projector.ToolCompleted("turn-1", "call-1", "read_file", "", time.Second, false, nil)

	tool := snapshotForTest(t, projector).Tools[0]
	if tool.Status != ToolSucceeded || tool.Exploration == nil || tool.Exploration.Operation != ExplorationRead ||
		len(tool.Exploration.Path) > 16 || !tool.Exploration.Truncated || tool.Exploration.Workspace != "build" {
		t.Fatalf("completed exploration = %+v", tool)
	}
	cloned := snapshotForTest(t, projector)
	cloned.Tools[0].Exploration.Path = "mutated"
	if got := snapshotForTest(t, projector).Tools[0].Exploration.Path; got == "mutated" {
		t.Fatal("exploration snapshot aliases projector state")
	}
}

func TestToolMCPIsBoundedClonedAndRetainedThroughCompletion(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{TextBytes: 32})
	projector.ToolStarted("turn-1", "call-1", "opaque-provider-name", "fields: query")
	projector.ToolMCPObserved("turn-1", "call-1", MCPState{
		Server: "github", Tool: "search_repositories", Purpose: strings.Repeat("purpose ", 20),
		Outcome: MCPOutcomeSucceeded, Result: strings.Repeat("result ", 20),
	})
	projector.ToolCompleted("turn-1", "call-1", "opaque-provider-name", "", time.Second, false, nil)

	tool := snapshotForTest(t, projector).Tools[0]
	if tool.Status != ToolSucceeded || tool.MCP == nil || !tool.MCP.Truncated ||
		len(tool.MCP.Purpose) > 32 || len(tool.MCP.Result) > 32 {
		t.Fatalf("completed MCP tool = %+v", tool)
	}
	cloned := snapshotForTest(t, projector)
	cloned.Tools[0].MCP.Result = "mutated"
	if got := snapshotForTest(t, projector).Tools[0].MCP.Result; got == "mutated" {
		t.Fatal("MCP snapshot aliases projector state")
	}
}

func TestOrphanCommandCompletionRemainsExplicit(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	exitCode := 0
	projector.ToolCommandOutput("turn-1", "orphan", CommandState{
		Command: "true", Status: CommandSucceeded, OwnsProcess: true, ExitCode: &exitCode,
	})
	tool := snapshotForTest(t, projector).Tools[0]
	if tool.Command == nil || !tool.Command.Orphan || tool.Status != ToolSucceeded {
		t.Fatalf("orphan command = %+v", tool)
	}
}

func TestBackgroundCommandOutlivesToolAndTurnThenCompletes(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	projector.ToolStarted("turn-1", "background", "exec", "fields: command")
	projector.ToolCommandOutput("turn-1", "background", CommandState{
		Command: "sleep 1", Status: CommandRunning, Background: true, OwnsProcess: true,
		SessionID: "session-1",
	})
	projector.ToolCompleted("turn-1", "background", "exec", "", time.Millisecond, false, nil)
	projector.TurnInterrupted("turn-1", "interrupted")
	tool := snapshotForTest(t, projector).Tools[0]
	if tool.Status != ToolRunning || tool.Command == nil || tool.Command.Status != CommandRunning {
		t.Fatalf("background command was terminalized with its turn: %+v", tool)
	}
	exitCode := 0
	projector.ToolCommandOutput("turn-1", "background", CommandState{
		Status: CommandSucceeded, Background: true, OwnsProcess: true, ExitCode: &exitCode,
		Duration: time.Second,
	})
	tool = snapshotForTest(t, projector).Tools[0]
	if tool.Status != ToolSucceeded || tool.Command.Status != CommandSucceeded || tool.Duration != time.Second {
		t.Fatalf("background terminal command = %+v", tool)
	}
}

func TestTerminalBackgroundDurationSurvivesLaterToolCompletion(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	projector.ToolStarted("turn-1", "background", "exec", "fields: command")
	processDuration := 3 * time.Second
	projector.ToolCommandOutput("turn-1", "background", CommandState{
		Command: "true", Status: CommandSucceeded, Background: true, OwnsProcess: true,
		SessionID: "session-1", Duration: processDuration,
	})
	projector.ToolCompleted("turn-1", "background", "exec", "", time.Millisecond, false, nil)

	tool := snapshotForTest(t, projector).Tools[0]
	if tool.Duration != processDuration || tool.Command == nil || tool.Command.Duration != processDuration {
		t.Fatalf("terminal background duration = %+v", tool)
	}
}

func TestUnadmittedBackgroundCommandIsTerminalizedWithAbnormalTurn(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	projector.ToolStarted("turn-1", "background", "exec", "fields: command")
	projector.ToolCommandOutput("turn-1", "background", CommandState{
		Command: "sleep 1", Status: CommandRunning, Background: true, OwnsProcess: true,
	})
	projector.TurnInterrupted("turn-1", "interrupted before process admission")

	tool := snapshotForTest(t, projector).Tools[0]
	if tool.Status != ToolInterrupted || tool.Command == nil || tool.Command.Status != CommandCanceled ||
		!tool.Command.Canceled {
		t.Fatalf("unadmitted background command = %+v", tool)
	}
}

func TestFailedToolTerminalizesCommandThatNeverProducedAnOutcome(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	projector.ToolStarted("turn-1", "call-1", "exec", "fields: command")
	projector.ToolCommandOutput("turn-1", "call-1", CommandState{
		Command: "missing-binary", Status: CommandRunning, OwnsProcess: true,
	})
	projector.ToolCompleted("turn-1", "call-1", "exec", "", time.Millisecond, true, nil)
	tool := snapshotForTest(t, projector).Tools[0]
	if tool.Status != ToolFailed || tool.Command == nil || tool.Command.Status != CommandFailed {
		t.Fatalf("failed command without terminal observation = %+v", tool)
	}
}

func TestFailedToolTerminalizesUnadmittedBackgroundCommand(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	projector.ToolStarted("turn-1", "call-1", "exec", "fields: command, background")
	projector.ToolCommandOutput("turn-1", "call-1", CommandState{
		Action: "run", Command: "missing-binary", Status: CommandRunning, Background: true,
	})
	projector.ToolCompleted("turn-1", "call-1", "exec", "failed to start command", time.Millisecond, true, nil)

	tool := snapshotForTest(t, projector).Tools[0]
	if tool.Status != ToolFailed || tool.Command == nil || tool.Command.Status != CommandFailed ||
		tool.Command.OwnsProcess || tool.Command.SessionID != "" {
		t.Fatalf("failed unadmitted background command = %+v", tool)
	}
}

func TestSuccessfulTerminalInteractionDoesNotOwnTargetProcessOutcome(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	projector.ToolStarted("turn-1", "kill-call", "exec", "fields: action, sessionId")
	projector.ToolCommandOutput("turn-1", "kill-call", CommandState{
		Action: "kill", Command: "sleep 30", Status: CommandCanceled, Background: true,
	})
	projector.ToolCompleted("turn-1", "kill-call", "exec", "", time.Millisecond, false, nil)
	tool := snapshotForTest(t, projector).Tools[0]
	if tool.Status != ToolSucceeded || tool.Command == nil || tool.Command.Status != CommandCanceled ||
		tool.Command.OwnsProcess {
		t.Fatalf("terminal interaction lifecycle = %+v", tool)
	}
}

func TestCommandTranscriptIsBoundedAcross32CorrelatedCalls(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{TextBytes: 256})
	for index := range 32 {
		callID := fmt.Sprintf("call-%02d", index)
		projector.ToolStarted("turn-1", callID, "exec", "fields: command")
		for sequence := range 40 {
			projector.ToolCommandOutput("turn-1", callID, CommandState{
				Command: fmt.Sprintf("printf call-%02d", index), Status: CommandRunning, OwnsProcess: true,
				Transcript: []CommandTranscriptEntry{{
					Sequence: uint64(sequence + 1), Stream: "stdout",
					Text: fmt.Sprintf("call-%02d-output-%02d\n", index, sequence),
				}},
			})
		}
	}
	tools := snapshotForTest(t, projector).Tools
	if len(tools) != 32 {
		t.Fatalf("command count = %d, want 32", len(tools))
	}
	for _, tool := range tools {
		if tool.Command == nil || !strings.Contains(tool.Command.Command, tool.CallID) {
			t.Fatalf("uncorrelated command identity = %+v", tool)
		}
		total := 0
		var transcript strings.Builder
		for _, entry := range tool.Command.Transcript {
			total += len(entry.Text)
			transcript.WriteString(entry.Text)
		}
		if total > 256 || len(tool.Command.Transcript) > defaultToolLimit {
			t.Fatalf(
				"unbounded call %s transcript: entries=%d bytes=%d",
				tool.CallID,
				len(tool.Command.Transcript),
				total,
			)
		}
		if !strings.Contains(transcript.String(), tool.CallID) {
			t.Fatalf("call %s lost its own transcript: %q", tool.CallID, transcript.String())
		}
		for other := range 32 {
			otherID := fmt.Sprintf("call-%02d", other)
			if otherID != tool.CallID && strings.Contains(transcript.String(), otherID) {
				t.Fatalf("call %s received transcript from %s: %q", tool.CallID, otherID, transcript.String())
			}
		}
	}
}

func TestTerminalCommandSnapshotReplacesTruncatedLivePrefix(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	projector.ToolStarted("turn-1", "call-1", "exec", "")
	projector.ToolCommandOutput("turn-1", "call-1", CommandState{
		Status: CommandRunning, OwnsProcess: true,
		Transcript: []CommandTranscriptEntry{{Sequence: 1, Stream: "stdout", Text: "head\n"}},
	})
	projector.ToolCommandOutput("turn-1", "call-1", CommandState{
		Status: CommandSucceeded, OwnsProcess: true, Truncated: true,
		Transcript: []CommandTranscriptEntry{
			{Sequence: 1, Stream: "stdout", Text: "head\n"},
			{Stream: "system", Text: "[… omitted …]\n"},
			{Sequence: 99, Stream: "stdout", Text: "tail\n"},
		},
	})
	command := snapshotForTest(t, projector).Tools[0].Command
	if command == nil || len(command.Transcript) != 3 || command.Transcript[1].Stream != "system" ||
		command.Transcript[2].Text != "tail\n" {
		t.Fatalf("terminal command transcript = %+v", command)
	}
}

func TestRepeatedCallIDAcrossTurnsRemainsDistinct(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	projector.ToolStarted("turn-1", "call-1", "write_file", "fields: path")
	projector.ToolCompleted("turn-1", "call-1", "write_file", "done", 0, false, []WriteAudit{{
		Kind: "file", Target: "first.go", Action: "write", Success: true,
	}})
	projector.ToolStarted("turn-2", "call-1", "exec", "fields: command")
	projector.ToolCompleted("turn-2", "call-1", "exec", "done", 0, false, nil)

	tools := snapshotForTest(t, projector).Tools
	if len(tools) != 2 || tools[0].TurnID != "turn-1" ||
		tools[0].WriteAudit[0].Target != "first.go" || tools[1].TurnID != "turn-2" {
		t.Fatalf("reused call ID tools = %+v", tools)
	}
}

func TestFailedTurnTerminalizesRunningTool(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	projector.ToolStarted("turn-1", "call-1", "write_file", "fields: path")
	projector.TurnFailed("turn-1", "turn failed")
	view := snapshotForTest(t, projector)
	if len(view.Tools) != 1 || view.Tools[0].Status != ToolFailed || view.Activity != ActivityFailed {
		t.Fatalf("failed-turn view = %+v", view)
	}
}

func TestTurnOutcomesAreTypedAndCorrelated(t *testing.T) {
	tests := []struct {
		name         string
		finish       func(*Projector)
		wantActivity Activity
		wantOutcome  TurnOutcome
	}{
		{name: "completed", finish: func(p *Projector) {
			p.TurnCompleted("turn-1", "completed")
		}, wantActivity: ActivityIdle, wantOutcome: TurnOutcomeCompleted},
		{name: "suspended", finish: func(p *Projector) {
			p.TurnSuspended("turn-1", "waiting for input")
		}, wantActivity: ActivityWaitingInput, wantOutcome: TurnOutcomeSuspended},
		{name: "failed", finish: func(p *Projector) {
			p.TurnFailed("turn-1", "turn failed")
		}, wantActivity: ActivityFailed, wantOutcome: TurnOutcomeFailed},
		{name: "interrupted", finish: func(p *Projector) {
			p.TurnInterrupted("turn-1", "interrupted")
		}, wantActivity: ActivityIdle, wantOutcome: TurnOutcomeInterrupted},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projector := newTestProjector(t, ProjectionLimits{})
			projector.TurnStarted("turn-1", "fix it")
			test.finish(projector)
			view := snapshotForTest(t, projector)
			if view.Activity != test.wantActivity || view.LastTurn == nil ||
				view.LastTurn.TurnID != "turn-1" || view.LastTurn.Outcome != test.wantOutcome {
				t.Fatalf("terminal view = %+v", view)
			}
		})
	}
}

func TestStandaloneForegroundCompactionOwnsIdleActivity(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	projector.CompactionUpdate(CompactionState{Reason: "manual", Status: CompactionRunning})
	started := snapshotForTest(t, projector)
	if started.Activity != ActivityCompacting || started.LastCompaction == nil || started.LastCompaction.Background {
		t.Fatalf("standalone compaction start = %+v", started)
	}
	projector.CompactionUpdate(CompactionState{Reason: "manual", Status: CompactionNoProgress})
	completed := snapshotForTest(t, projector)
	if completed.Activity != ActivityIdle || completed.LastCompaction == nil ||
		completed.LastCompaction.Status != CompactionNoProgress {
		t.Fatalf("standalone compaction completion = %+v", completed)
	}
}

func TestCompactionEndPreservesNewerInterruptState(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	projector.TurnStarted("turn-1", "fix it")
	projector.CompactionUpdate(CompactionState{
		TurnID: "turn-1", Reason: "llm_retry", Status: CompactionRunning,
	})
	projector.InterruptRequested()
	projector.CompactionUpdate(CompactionState{
		TurnID: "turn-1", Reason: "llm_retry", Status: CompactionFailed,
	})
	view := snapshotForTest(t, projector)
	if view.Activity != ActivityInterrupting || view.Status != "interrupt requested" ||
		view.LastCompaction == nil || view.LastCompaction.Status != CompactionFailed {
		t.Fatalf("interrupted compaction view = %+v", view)
	}
}

func TestForegroundCompactionInterruptedReleasesActivity(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	projector.TurnStarted("turn-1", "fix it")
	projector.CompactionUpdate(CompactionState{
		TurnID: "turn-1", Reason: "llm_retry", Status: CompactionRunning,
	})
	projector.CompactionUpdate(CompactionState{
		TurnID: "turn-1", Reason: "llm_retry", Status: CompactionInterrupted,
	})
	view := snapshotForTest(t, projector)
	if view.Activity != ActivityRunning || view.Status != "context compaction interrupted" ||
		view.LastCompaction == nil || view.LastCompaction.Status != CompactionInterrupted {
		t.Fatalf("interrupted compaction view = %+v", view)
	}
}

func TestLateCompactionStartDoesNotClaimNewerTurnActivity(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	projector.TurnStarted("turn-1", "first")
	projector.TurnCompleted("turn-1", "completed")
	projector.TurnStarted("turn-2", "second")
	projector.CompactionUpdate(CompactionState{
		TurnID: "turn-1", Reason: "llm_retry", Status: CompactionRunning,
	})
	view := snapshotForTest(t, projector)
	if view.Activity != ActivityRunning || view.Status != "running" ||
		view.LastCompaction == nil || view.LastCompaction.TurnID != "turn-1" {
		t.Fatalf("late prior-turn compaction view = %+v", view)
	}
}

func TestBackgroundCompactionDoesNotStrandForegroundActivity(t *testing.T) {
	tests := []struct {
		name string
		end  func(*Projector)
		want CompactionStatus
	}{
		{name: "background start", end: func(p *Projector) {
			p.CompactionUpdate(CompactionState{
				Reason: "summarize", Status: CompactionRunning, Background: true,
			})
			p.CompactionUpdate(CompactionState{
				TurnID: "turn-1", Reason: "llm_retry", Status: CompactionCompleted, TokensSaved: 12,
			})
		}, want: CompactionCompleted},
		{name: "background completion", end: func(p *Projector) {
			p.CompactionUpdate(CompactionState{
				Reason: "summarize", Status: CompactionCompleted, TokensSaved: 4, Background: true,
			})
			p.CompactionUpdate(CompactionState{
				TurnID: "turn-1", Reason: "llm_retry", Status: CompactionFailed,
			})
		}, want: CompactionFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projector := newTestProjector(t, ProjectionLimits{})
			projector.TurnStarted("turn-1", "fix it")
			projector.CompactionUpdate(CompactionState{
				TurnID: "turn-1", Reason: "llm_retry", Status: CompactionRunning,
			})
			test.end(projector)
			view := snapshotForTest(t, projector)
			if view.Activity != ActivityRunning || view.LastCompaction == nil ||
				view.LastCompaction.Status != test.want {
				t.Fatalf("interleaved compaction view = %+v", view)
			}
		})
	}
}

func TestLateTurnStartOrdersUserBeforeStreamedAssistant(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	projector.AssistantAccumulated("turn-1", "already streaming", false)
	projector.TurnStarted("turn-1", "fix it")
	entries := snapshotForTest(t, projector).Entries
	if len(entries) != 2 || entries[0].Kind != EntryUser || entries[1].Kind != EntryAssistant {
		t.Fatalf("late turn-start ordering = %+v", entries)
	}
}

func TestProjectionBoundsTextAndEntries(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{Entries: 1, TextBytes: 32})
	projector.TurnStarted("turn-1", "first")
	projector.AssistantAccumulated("turn-1", "abcdefghijklmnopqrstuvwxyz0123456789", true)
	view := snapshotForTest(t, projector)
	if !view.HasOlderEntries || len(view.Entries) != 1 || !view.Entries[0].Truncated ||
		len(view.Entries[0].Text) > 32 {
		t.Fatalf("bounded view = %+v", view)
	}
}

func TestStreamDelegateProjectsAnswerReasoningAndUsage(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	delegate := NewStreamDelegate(projector, "thread-1")
	if _, ok := delegate.GetStreamer(
		t.Context(), "coding", "local", "other", "", runtimeevents.NewTraceScope("/repo", "turn-1"),
	); ok {
		t.Fatal("stream delegate admitted a different coding thread")
	}
	streamer, ok := delegate.GetStreamer(
		t.Context(), "coding", "local", "thread-1", "", runtimeevents.NewTraceScope("/repo", "turn-1"),
	)
	if !ok {
		t.Fatal("stream delegate rejected the matching coding thread")
	}
	if err := streamer.Update(t.Context(), "hel"); err != nil {
		t.Fatal(err)
	}
	reasoning, ok := streamer.(bus.ReasoningStreamer)
	if !ok {
		t.Fatal("projected stream does not expose reasoning")
	}
	if err := reasoning.UpdateReasoning(t.Context(), "checking"); err != nil {
		t.Fatal(err)
	}
	withUsage, ok := streamer.(bus.ContextUsageStreamer)
	if !ok {
		t.Fatal("projected stream does not expose context usage")
	}
	if err := withUsage.FinalizeWithContext(t.Context(), "hello", &bus.ContextUsage{
		UsedTokens: 12, TotalTokens: 100,
	}); err != nil {
		t.Fatal(err)
	}

	view := snapshotForTest(t, projector)
	if len(view.Entries) != 2 || view.Entries[0].Text != "hello" || view.Entries[1].Text != "checking" {
		t.Fatalf("streamed entries = %+v", view.Entries)
	}
	if !view.Entries[0].Complete || view.ContextUsage.UsedTokens != 12 {
		t.Fatalf("final stream view = %+v", view)
	}
}

func newTestProjector(t *testing.T, limits ProjectionLimits) *Projector {
	t.Helper()
	projector, err := NewProjector("thread-1", limits)
	if err != nil {
		t.Fatal(err)
	}
	return projector
}

func snapshotForTest(t *testing.T, projector *Projector) ThreadSnapshot {
	t.Helper()
	view, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return view
}
