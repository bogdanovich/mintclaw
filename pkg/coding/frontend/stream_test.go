package frontend

import (
	"context"
	"errors"
	"reflect"
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
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Text != "hello 🌿" ||
		!snapshot.Entries[0].Complete || !utf8.ValidString(snapshot.Entries[0].Text) {
		t.Fatalf("coalesced stream snapshot = %+v", snapshot)
	}
}

func TestSlowStreamSubscriberConvergesToLatestView(t *testing.T) {
	projector, err := NewProjector("thread-1", ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	subscriptionCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, updates, err := projector.Subscribe(subscriptionCtx)
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
	got := <-updates
	if len(got.Entries) != 1 || got.Entries[0].Text != "abcde" {
		t.Fatalf("latest stream view = %+v", got)
	}
}

func TestStreamCancelDoesNotClaimTurnInterruption(t *testing.T) {
	projector, err := NewProjector("thread-1", ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.TurnStarted("turn-1", "fix it")
	projector.ReasoningAccumulated("turn-1", "reasoning from an earlier tool round", true)
	streamer, ok := NewStreamDelegate(projector, "thread-1").GetStreamer(
		t.Context(), "coding", "thread-1", "thread-1", "", runtimeevents.NewTraceScope("/repo", "turn-1"),
	)
	if !ok {
		t.Fatal("matching stream was rejected")
	}
	reasoning := streamer.(bus.ReasoningStreamer)
	if err = reasoning.UpdateReasoning(t.Context(), "failed provider reasoning"); err != nil {
		t.Fatal(err)
	}
	if err = reasoning.FinalizeReasoning(t.Context(), "failed provider reasoning"); err != nil {
		t.Fatal(err)
	}
	streamer.Cancel(t.Context())
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Activity != ActivityRunning || snapshot.LastTurn != nil ||
		len(snapshot.Entries) != 2 || snapshot.Entries[0].Kind != EntryUser ||
		snapshot.Entries[1].Text != "reasoning from an earlier tool round" || !snapshot.Entries[1].Complete {
		t.Fatalf("stream cancel changed turn lifecycle = %+v", snapshot)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err = streamer.Update(canceled, "ignored"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled update error = %v, want context.Canceled", err)
	}
}

func TestStreamCancelDoesNotRollbackLaterEntryWriter(t *testing.T) {
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
	reasoning := streamer.(bus.ReasoningStreamer)
	if err = reasoning.FinalizeReasoning(t.Context(), "discarded reasoning"); err != nil {
		t.Fatal(err)
	}
	projector.ReasoningAccumulated("turn-1", "newer writer reasoning", true)
	beforeCancel, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	streamer.Cancel(t.Context())

	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot, beforeCancel) || len(snapshot.Entries) != 2 ||
		snapshot.Entries[1].Text != "newer writer reasoning" {
		t.Fatalf("later writer after stream cancel = %+v", snapshot)
	}
}

func TestStreamCancelDoesNotRollbackIdenticalLaterStreamer(t *testing.T) {
	projector, err := NewProjector("thread-1", ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	delegate := NewStreamDelegate(projector, "thread-1")
	traceScope := runtimeevents.NewTraceScope("/repo", "turn-1")
	first, ok := delegate.GetStreamer(t.Context(), "coding", "thread-1", "thread-1", "", traceScope)
	if !ok {
		t.Fatal("first matching stream was rejected")
	}
	if err = first.Finalize(t.Context(), "accepted answer"); err != nil {
		t.Fatal(err)
	}
	second, ok := delegate.GetStreamer(t.Context(), "coding", "thread-1", "thread-1", "", traceScope)
	if !ok {
		t.Fatal("second matching stream was rejected")
	}
	if err = second.Finalize(t.Context(), "accepted answer"); err != nil {
		t.Fatal(err)
	}
	beforeCancel, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	first.Cancel(t.Context())
	second.Cancel(t.Context())
	first.Cancel(t.Context()) // Repeated cancellation must not reclaim restored ownership.

	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot, beforeCancel) || len(snapshot.Entries) != 1 ||
		snapshot.Entries[0].Text != "accepted answer" || !snapshot.Entries[0].Complete {
		t.Fatalf("identical later streamer after earlier cancel = %+v", snapshot)
	}
}
