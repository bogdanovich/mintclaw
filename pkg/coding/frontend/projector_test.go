package frontend

import (
	"context"
	"reflect"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
)

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
	if updated.Activity != ActivityRunning || len(updated.Entries) != 1 || updated.Entries[0].Text != "fix it" {
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
	if view.Activity != ActivityIdle || view.LastTurn == nil || view.LastTurn.Outcome != TurnOutcomeCompleted {
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
		Status: CommandFailed, Background: true, ExitCode: &exitCode,
	})
	projector.ToolCompleted("turn-1", "call-1", "exec", "", 0, false, nil)
	tools := snapshotForTest(t, projector).Tools
	if len(tools) != 1 || tools[0].Status != ToolFailed {
		t.Fatalf("completed background command tools = %+v", tools)
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
