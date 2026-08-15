package frontend

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
)

func TestWorkspaceUpdateConvergesAndDoesNotAliasCallerState(t *testing.T) {
	projector, err := NewProjector("thread-1", ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	reducer, err := NewReducer(initial)
	if err != nil {
		t.Fatal(err)
	}

	workspace := codingworkspace.Snapshot{
		ProjectRoot: "/repo",
		CWD:         "/repo/subdir",
		Git:         codingworkspace.GitState{Available: true, Branch: "main", Dirty: true},
		ChangedPaths: []codingworkspace.ChangedPath{
			{Path: "changed.go", Status: " M"},
		},
	}
	delta := projector.WorkspaceUpdated(workspace)
	workspace.ChangedPaths[0].Path = "caller-mutated.go"
	if delta.Kind != DeltaWorkspaceUpdated || delta.Workspace == nil {
		t.Fatalf("workspace delta = %+v", delta)
	}
	if err = reducer.Apply(delta); err != nil {
		t.Fatal(err)
	}

	want, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got := reducer.State()
	if !reflect.DeepEqual(got, want) || got.Workspace == nil ||
		got.Workspace.ChangedPaths[0].Path != "changed.go" {
		t.Fatalf("workspace state = %+v, want %+v", got.Workspace, want.Workspace)
	}
	got.Workspace.ChangedPaths[0].Path = "frontend-mutated.go"
	stable, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if stable.Workspace.ChangedPaths[0].Path != "changed.go" {
		t.Fatalf("projector workspace was aliased: %+v", stable.Workspace)
	}
}

func TestReducerResynchronizesAfterDroppedDelta(t *testing.T) {
	projector, err := NewProjector("thread-1", ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.Open(false)
	initial, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	reducer, err := NewReducer(initial)
	if err != nil {
		t.Fatal(err)
	}

	projector.TurnStarted("turn-1", "fix it") // Intentionally dropped.
	latestDelta := projector.AssistantAccumulated("turn-1", "working", false)
	if err = reducer.Apply(latestDelta); !errors.Is(err, ErrRevisionGap) {
		t.Fatalf("Apply() error = %v, want ErrRevisionGap", err)
	}
	if err = reducer.ApplyOrResync(t.Context(), projector, latestDelta); err != nil {
		t.Fatalf("ApplyOrResync() error = %v", err)
	}

	want, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got := reducer.State()
	if got.Revision != want.Revision || len(got.Entries) != 2 || got.Entries[1].Text != "working" {
		t.Fatalf("resynchronized state = %+v, want %+v", got, want)
	}
}

func TestLifecycleCorrelationAndSnapshotConvergence(t *testing.T) {
	projector, err := NewProjector("thread-1", ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	reducer, err := NewReducer(initial)
	if err != nil {
		t.Fatal(err)
	}
	deltas := []Delta{
		projector.TurnStarted("turn-1", "fix it"),
		projector.ToolStarted("turn-1", "call-1", "exec", "fields: command"),
		projector.Warning("turn-1", "retry-1", "model request retry 1/2 (rate_limit)"),
		projector.ToolCompleted("turn-1", "call-1", "exec", "done", 0, false, []WriteAudit{{
			Kind: "file", Target: "main.go", Action: "update", Success: true,
		}}),
		projector.CompactionStarted("turn-1", "llm_retry", false),
		projector.CompactionCompleted("turn-1", "llm_retry", 10, false, false),
		projector.TurnCompleted("turn-1", "completed"),
	}
	for i, delta := range deltas {
		if i == 2 { // Simulate an arbitrary lost progress observation.
			continue
		}
		if err = reducer.Apply(delta); err != nil {
			break
		}
	}
	if !errors.Is(err, ErrRevisionGap) {
		t.Fatalf("reducer error = %v, want ErrRevisionGap", err)
	}
	if err = reducer.CatchUp(t.Context(), projector); err != nil {
		t.Fatal(err)
	}
	want, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got := reducer.State()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resynchronized lifecycle = %+v, want %+v", got, want)
	}
	if got.Tools[0].TurnID != "turn-1" || got.Tools[0].WriteAudit[0].Target != "main.go" {
		t.Fatalf("tool correlation = %+v", got.Tools[0])
	}
	for _, delta := range deltas[:4] {
		if delta.TurnID == "" || delta.EntityID == "" {
			t.Fatalf("uncorrelated progress delta = %+v", delta)
		}
	}
}

func TestRepeatedCallIDAcrossTurnsRemainsDistinctAndConverges(t *testing.T) {
	projector, err := NewProjector("thread-1", ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	reducer, err := NewReducer(initial)
	if err != nil {
		t.Fatal(err)
	}
	deltas := []Delta{
		projector.ToolStarted("turn-1", "call-1", "write_file", "fields: path"),
		projector.ToolCompleted("turn-1", "call-1", "write_file", "done", 0, false, []WriteAudit{{
			Kind: "file", Target: "first.go", Action: "write", Success: true,
		}}),
		projector.ToolStarted("turn-2", "call-1", "exec", "fields: command"),
		projector.ToolCompleted("turn-2", "call-1", "exec", "done", 0, false, nil),
	}
	for _, delta := range deltas {
		if err = reducer.Apply(delta); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := reducer.State(); !reflect.DeepEqual(got, snapshot) {
		t.Fatalf("reduced state = %+v, want %+v", got, snapshot)
	}
	if len(snapshot.Tools) != 2 || snapshot.Tools[0].TurnID != "turn-1" ||
		snapshot.Tools[0].WriteAudit[0].Target != "first.go" || snapshot.Tools[1].TurnID != "turn-2" {
		t.Fatalf("reused call ID tools = %+v", snapshot.Tools)
	}
	if deltas[0].EntityID == deltas[2].EntityID {
		t.Fatalf("reused call ID shares entity ID %q", deltas[0].EntityID)
	}
}

func TestFailedTurnTerminalizesRunningToolAndReducerResynchronizes(t *testing.T) {
	projector, err := NewProjector("thread-1", ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	reducer, err := NewReducer(initial)
	if err != nil {
		t.Fatal(err)
	}
	started := projector.ToolStarted("turn-1", "call-1", "write_file", "fields: path")
	if err = reducer.Apply(started); err != nil {
		t.Fatal(err)
	}
	failed := projector.TurnFailed("turn-1", "turn failed")
	if !failed.RequiresSnapshot {
		t.Fatalf("failed-turn delta = %+v, want snapshot resynchronization", failed)
	}
	if err = reducer.ApplyOrResync(t.Context(), projector, failed); err != nil {
		t.Fatal(err)
	}
	want, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got := reducer.State()
	if !reflect.DeepEqual(got, want) || len(got.Tools) != 1 || got.Tools[0].Status != ToolFailed ||
		got.Activity != ActivityFailed {
		t.Fatalf("failed-turn state = %+v, want %+v", got, want)
	}
}

func TestTurnOutcomesAreTypedCorrelatedAndSnapshotRecoverable(t *testing.T) {
	tests := []struct {
		name         string
		finish       func(*Projector) Delta
		wantKind     DeltaKind
		wantActivity Activity
		wantOutcome  TurnOutcome
	}{
		{
			name: "completed", finish: func(p *Projector) Delta {
				return p.TurnCompleted("turn-1", "completed")
			}, wantKind: DeltaTurnCompleted, wantActivity: ActivityIdle, wantOutcome: TurnOutcomeCompleted,
		},
		{
			name: "suspended", finish: func(p *Projector) Delta {
				return p.TurnSuspended("turn-1", "waiting for input")
			}, wantKind: DeltaTurnSuspended, wantActivity: ActivityWaitingInput, wantOutcome: TurnOutcomeSuspended,
		},
		{
			name: "failed", finish: func(p *Projector) Delta {
				return p.TurnFailed("turn-1", "turn failed")
			}, wantKind: DeltaTurnFailed, wantActivity: ActivityFailed, wantOutcome: TurnOutcomeFailed,
		},
		{
			name: "interrupted", finish: func(p *Projector) Delta {
				return p.TurnInterrupted("turn-1", "interrupted")
			}, wantKind: DeltaTurnInterrupted, wantActivity: ActivityIdle, wantOutcome: TurnOutcomeInterrupted,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projector, err := NewProjector("thread-1", ProjectionLimits{})
			if err != nil {
				t.Fatal(err)
			}
			initial, err := projector.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			reducer, err := NewReducer(initial)
			if err != nil {
				t.Fatal(err)
			}
			if err = reducer.Apply(projector.TurnStarted("turn-1", "fix it")); err != nil {
				t.Fatal(err)
			}
			finished := tt.finish(projector)
			if finished.Kind != tt.wantKind || finished.TurnID != "turn-1" || finished.EntityID != "turn-1" ||
				finished.Activity != tt.wantActivity || finished.LastTurn == nil ||
				finished.LastTurn.TurnID != "turn-1" || finished.LastTurn.Outcome != tt.wantOutcome {
				t.Fatalf("terminal delta = %+v", finished)
			}
			if err = reducer.Apply(finished); err != nil {
				t.Fatal(err)
			}
			want, err := projector.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if got := reducer.State(); !reflect.DeepEqual(got, want) {
				t.Fatalf("reduced terminal state = %+v, want %+v", got, want)
			}
		})
	}
}

func TestTerminalOutcomeSurvivesExpiredDeltaWindowResynchronization(t *testing.T) {
	projector, err := NewProjector("thread-1", ProjectionLimits{Deltas: 1})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	reducer, err := NewReducer(initial)
	if err != nil {
		t.Fatal(err)
	}
	projector.TurnStarted("turn-1", "fix it")
	projector.TurnSuspended("turn-1", "waiting for input")
	projector.ThreadMetadataUpdated(ThreadMetadata{Title: "Still waiting"})

	if err = reducer.CatchUp(t.Context(), projector); err != nil {
		t.Fatal(err)
	}
	got := reducer.State()
	if got.LastTurn == nil || got.LastTurn.TurnID != "turn-1" ||
		got.LastTurn.Outcome != TurnOutcomeSuspended || got.Activity != ActivityWaitingInput {
		t.Fatalf("resynchronized terminal outcome = %+v", got)
	}
}

func TestCompactionLifecycleSurvivesExpiredDeltaWindowResynchronization(t *testing.T) {
	projector, err := NewProjector("thread-1", ProjectionLimits{Deltas: 1})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	reducer, err := NewReducer(initial)
	if err != nil {
		t.Fatal(err)
	}
	projector.TurnStarted("turn-1", "fix it")
	projector.CompactionStarted("turn-1", "llm_retry", false)
	projector.CompactionFailed("turn-1", "llm_retry", false)
	projector.ThreadMetadataUpdated(ThreadMetadata{Title: "After compaction"})

	if err = reducer.CatchUp(t.Context(), projector); err != nil {
		t.Fatal(err)
	}
	got := reducer.State()
	if got.LastCompaction == nil || got.LastCompaction.TurnID != "turn-1" ||
		got.LastCompaction.Status != CompactionFailed || got.Activity != ActivityRunning {
		t.Fatalf("resynchronized compaction = %+v", got)
	}
}

func TestCompactionEndPreservesNewerInterruptState(t *testing.T) {
	projector, err := NewProjector("thread-1", ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.TurnStarted("turn-1", "fix it")
	projector.CompactionStarted("turn-1", "llm_retry", false)
	projector.InterruptRequested()
	projector.CompactionFailed("turn-1", "llm_retry", false)

	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Activity != ActivityInterrupting || snapshot.Status != "interrupt requested" ||
		snapshot.LastCompaction == nil || snapshot.LastCompaction.Status != CompactionFailed {
		t.Fatalf("interrupted compaction snapshot = %+v", snapshot)
	}
}

func TestLateCompactionStartDoesNotClaimNewerTurnActivity(t *testing.T) {
	projector, err := NewProjector("thread-1", ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.TurnStarted("turn-1", "first")
	projector.TurnCompleted("turn-1", "completed")
	projector.TurnStarted("turn-2", "second")
	projector.CompactionStarted("turn-1", "llm_retry", false)

	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Activity != ActivityRunning || snapshot.Status != "running" ||
		snapshot.LastCompaction == nil || snapshot.LastCompaction.TurnID != "turn-1" {
		t.Fatalf("late prior-turn compaction snapshot = %+v", snapshot)
	}
}

func TestBackgroundCompactionDoesNotStrandForegroundActivity(t *testing.T) {
	tests := []struct {
		name string
		end  func(*Projector)
		want CompactionStatus
	}{
		{
			name: "background start",
			end: func(projector *Projector) {
				projector.CompactionStarted("", "summarize", true)
				projector.CompactionCompleted("turn-1", "llm_retry", 12, false, false)
			},
			want: CompactionCompleted,
		},
		{
			name: "background completion",
			end: func(projector *Projector) {
				projector.CompactionCompleted("", "summarize", 4, false, true)
				projector.CompactionFailed("turn-1", "llm_retry", false)
			},
			want: CompactionFailed,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projector, err := NewProjector("thread-1", ProjectionLimits{})
			if err != nil {
				t.Fatal(err)
			}
			projector.TurnStarted("turn-1", "fix it")
			projector.CompactionStarted("turn-1", "llm_retry", false)
			test.end(projector)

			snapshot, err := projector.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Activity != ActivityRunning || snapshot.LastCompaction == nil ||
				snapshot.LastCompaction.Status != test.want {
				t.Fatalf("interleaved compaction snapshot = %+v", snapshot)
			}
		})
	}
}

func TestReducerRejectsSnapshotIdentityAndRevisionRollback(t *testing.T) {
	reducer, err := NewReducer(ThreadSnapshot{
		ProtocolVersion: ProtocolVersion,
		ThreadID:        "thread-1",
		Revision:        4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = reducer.ApplySnapshot(ThreadSnapshot{
		ProtocolVersion: ProtocolVersion,
		ThreadID:        "thread-2",
		Revision:        5,
	}); !errors.Is(err, ErrThreadMismatch) {
		t.Fatalf("thread replacement error = %v, want ErrThreadMismatch", err)
	}
	if err = reducer.ApplySnapshot(ThreadSnapshot{
		ProtocolVersion: ProtocolVersion,
		ThreadID:        "thread-1",
		Revision:        3,
	}); !errors.Is(err, ErrRevisionGap) {
		t.Fatalf("revision rollback error = %v, want ErrRevisionGap", err)
	}
}

func TestReducerCatchUpFallsBackWhenDeltaWindowExpired(t *testing.T) {
	projector, err := NewProjector("thread-1", ProjectionLimits{Deltas: 2})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	reducer, err := NewReducer(initial)
	if err != nil {
		t.Fatal(err)
	}
	projector.Open(false)
	projector.TurnStarted("turn-1", "fix it")
	projector.AssistantAccumulated("turn-1", "done", true)

	if _, err = projector.ChangesSince(t.Context(), 0); !errors.Is(err, ErrRevisionUnavailable) {
		t.Fatalf("ChangesSince() error = %v, want ErrRevisionUnavailable", err)
	}
	if err = reducer.CatchUp(t.Context(), projector); err != nil {
		t.Fatalf("CatchUp() error = %v", err)
	}
	if got := reducer.State(); got.Revision != 3 || len(got.Entries) != 2 {
		t.Fatalf("caught-up state = %+v", got)
	}
}

func TestProjectorWatchBridgesRetainedAndLiveDeltas(t *testing.T) {
	projector, err := NewProjector("thread-1", ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	first := projector.Open(false)
	watchContext, cancel := context.WithCancel(t.Context())
	watch, err := projector.Watch(watchContext, 0)
	if err != nil {
		t.Fatal(err)
	}
	second := projector.TurnStarted("turn-1", "fix it")
	if got := <-watch; got.Revision != first.Revision {
		t.Fatalf("retained revision = %d, want %d", got.Revision, first.Revision)
	}
	if got := <-watch; got.Revision != second.Revision {
		t.Fatalf("live revision = %d, want %d", got.Revision, second.Revision)
	}
	cancel()
	if _, ok := <-watch; ok {
		t.Fatal("watch channel remained open after cancellation")
	}
}

func TestSlowWatcherDetectsDroppedBoundedDelta(t *testing.T) {
	projector, err := NewProjector("thread-1", ProjectionLimits{Deltas: 2})
	if err != nil {
		t.Fatal(err)
	}
	watchContext, cancel := context.WithCancel(t.Context())
	defer cancel()
	watch, err := projector.Watch(watchContext, 0)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	reducer, err := NewReducer(initial)
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		projector.Open(false)
	}
	if err = reducer.Apply(<-watch); !errors.Is(err, ErrRevisionGap) {
		t.Fatalf("post-overflow delta error = %v, want ErrRevisionGap", err)
	}
	if err = reducer.CatchUp(t.Context(), projector); err != nil {
		t.Fatal(err)
	}
	if got := reducer.State().Revision; got != 5 {
		t.Fatalf("resynchronized revision = %d, want 5", got)
	}
}

func TestLateTurnStartOrdersUserBeforeStreamedAssistant(t *testing.T) {
	projector, err := NewProjector("thread-1", ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.AssistantAccumulated("turn-1", "already streaming", false)
	projector.TurnStarted("turn-1", "fix it")
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 2 || snapshot.Entries[0].Kind != EntryUser ||
		snapshot.Entries[1].Kind != EntryAssistant {
		t.Fatalf("late turn-start ordering = %+v", snapshot.Entries)
	}
}

func TestLateTurnStartDeltaReductionConvergesWithSnapshot(t *testing.T) {
	projector, err := NewProjector("thread-1", ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	reducer, err := NewReducer(initial)
	if err != nil {
		t.Fatal(err)
	}
	for _, delta := range []Delta{
		projector.AssistantAccumulated("turn-1", "already streaming", false),
		projector.TurnStarted("turn-1", "fix it"),
	} {
		if err = reducer.Apply(delta); err != nil {
			t.Fatal(err)
		}
	}
	want, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := reducer.State(); !reflect.DeepEqual(got, want) {
		t.Fatalf("delta-reduced state = %+v, want snapshot %+v", got, want)
	}
}

func TestProjectionBoundsTextAndRequiresSnapshotOnEntityEviction(t *testing.T) {
	projector, err := NewProjector("thread-1", ProjectionLimits{Entries: 1, TextBytes: 32})
	if err != nil {
		t.Fatal(err)
	}
	projector.TurnStarted("turn-1", "first")
	delta := projector.AssistantAccumulated("turn-1", "abcdefghijklmnopqrstuvwxyz0123456789", true)
	if !delta.RequiresSnapshot {
		t.Fatal("entity eviction did not require an authoritative snapshot")
	}
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.HasOlderEntries || len(snapshot.Entries) != 1 || !snapshot.Entries[0].Truncated ||
		len(snapshot.Entries[0].Text) > 32 {
		t.Fatalf("bounded snapshot = %+v", snapshot)
	}
}

func TestStreamDelegateProjectsAnswerReasoningAndUsage(t *testing.T) {
	projector, err := NewProjector("thread-1", ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
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
	if err = streamer.Update(t.Context(), "hel"); err != nil {
		t.Fatal(err)
	}
	reasoning, ok := streamer.(bus.ReasoningStreamer)
	if !ok {
		t.Fatal("projected stream does not expose reasoning")
	}
	if err = reasoning.UpdateReasoning(t.Context(), "checking"); err != nil {
		t.Fatal(err)
	}
	withUsage, ok := streamer.(bus.ContextUsageStreamer)
	if !ok {
		t.Fatal("projected stream does not expose context usage")
	}
	if err = withUsage.FinalizeWithContext(t.Context(), "hello", &bus.ContextUsage{
		UsedTokens:  12,
		TotalTokens: 100,
	}); err != nil {
		t.Fatal(err)
	}

	snapshot, err := projector.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 2 || snapshot.Entries[0].Text != "hello" ||
		snapshot.Entries[1].Text != "checking" {
		t.Fatalf("streamed entries = %+v", snapshot.Entries)
	}
	if !snapshot.Entries[0].Complete || snapshot.ContextUsage.UsedTokens != 12 {
		t.Fatalf("final stream snapshot = %+v", snapshot)
	}
}
