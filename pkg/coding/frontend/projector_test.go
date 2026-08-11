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
