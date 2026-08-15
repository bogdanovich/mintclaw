package frontend

import (
	"context"
	"errors"
	"testing"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
)

func TestStreamCoalescesFinalStateAndKeepsUnicodeValid(t *testing.T) {
	projector, err := NewProjector("thread-1", ProjectionLimits{TextBytes: 32})
	if err != nil {
		t.Fatal(err)
	}
	streamer, ok := NewStreamDelegate(projector, "thread-1").GetStreamer(
		t.Context(), "coding", "thread-1", "thread-1", "", runtimeevents.NewTraceScope("/repo", "turn-1"),
	)
	if !ok {
		t.Fatal("matching stream was rejected")
	}
	if err = streamer.Update(t.Context(), string([]byte{0xf0, 0x9f})); err != nil {
		t.Fatal(err)
	}
	if err = streamer.Update(t.Context(), "hello 🌿"); err != nil {
		t.Fatal(err)
	}
	if err = streamer.Finalize(t.Context(), "hello 🌿"); err != nil {
		t.Fatal(err)
	}
	if err = streamer.Finalize(t.Context(), "hello 🌿"); err != nil {
		t.Fatal(err)
	}
	withUsage := streamer.(bus.ContextUsageStreamer)
	usage := &bus.ContextUsage{UsedTokens: 12, TotalTokens: 100}
	if err = withUsage.FinalizeWithContext(t.Context(), "hello 🌿", usage); err != nil {
		t.Fatal(err)
	}
	if err = withUsage.FinalizeWithContext(t.Context(), "hello 🌿", usage); err != nil {
		t.Fatal(err)
	}

	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision != 4 || len(snapshot.Entries) != 1 || snapshot.Entries[0].Text != "hello 🌿" ||
		!snapshot.Entries[0].Complete || !utf8.ValidString(snapshot.Entries[0].Text) {
		t.Fatalf("coalesced stream snapshot = %+v", snapshot)
	}
}

func TestStreamOverflowRecoversFromAuthoritativeSnapshot(t *testing.T) {
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
	watchCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	watch, err := projector.Watch(watchCtx, 0)
	if err != nil {
		t.Fatal(err)
	}
	streamer, ok := NewStreamDelegate(projector, "thread-1").GetStreamer(
		t.Context(), "coding", "thread-1", "thread-1", "", runtimeevents.NewTraceScope("/repo", "turn-1"),
	)
	if !ok {
		t.Fatal("matching stream was rejected")
	}
	for _, content := range []string{"a", "ab", "abc", "abcd", "abcde"} {
		if err = streamer.Update(t.Context(), content); err != nil {
			t.Fatal(err)
		}
	}
	if err = reducer.Apply(<-watch); !errors.Is(err, ErrRevisionGap) {
		t.Fatalf("overflow apply error = %v, want ErrRevisionGap", err)
	}
	if err = reducer.CatchUp(t.Context(), projector); err != nil {
		t.Fatal(err)
	}
	got := reducer.State()
	if got.Revision != 5 || len(got.Entries) != 1 || got.Entries[0].Text != "abcde" {
		t.Fatalf("resynchronized stream = %+v", got)
	}
}

func TestStreamCancelDoesNotClaimTurnInterruption(t *testing.T) {
	projector, err := NewProjector("thread-1", ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.TurnStarted("turn-1", "fix it")
	streamer, ok := NewStreamDelegate(projector, "thread-1").GetStreamer(
		t.Context(), "coding", "thread-1", "thread-1", "", runtimeevents.NewTraceScope("/repo", "turn-1"),
	)
	if !ok {
		t.Fatal("matching stream was rejected")
	}
	streamer.Cancel(t.Context())
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision != 1 || snapshot.Activity != ActivityRunning || snapshot.LastTurn != nil {
		t.Fatalf("stream cancel changed turn lifecycle = %+v", snapshot)
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err = streamer.Update(canceled, "ignored"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled update error = %v, want context.Canceled", err)
	}
}
