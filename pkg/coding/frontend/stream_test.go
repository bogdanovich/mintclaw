package frontend

import (
	"context"
	"errors"
	"fmt"
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
	if len(snapshot.Items) != 1 || snapshot.Items[0].Revision != 3 ||
		snapshot.Items[0].Lifecycle != PresentationCompleted || snapshot.Items[0].Message == nil {
		t.Fatalf("coalesced presentation item = %+v", snapshot.Items)
	}
	if len(projector.activeStreamOwners) != 0 || len(projector.entryVersions) != 0 ||
		len(projector.entryGenerations) != 0 {
		t.Fatalf("finalized stream retained rollback state")
	}
	streamer.Cancel(t.Context())
	if afterCancel := snapshotForTest(t, projector); !reflect.DeepEqual(afterCancel, snapshot) {
		t.Fatalf("cancel rolled back a finalized stream: before=%+v after=%+v", snapshot, afterCancel)
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

func TestStreamRebuildPreservesTypedPlanItems(t *testing.T) {
	for _, action := range []string{"update", "finalize", "cancel"} {
		t.Run(action, func(t *testing.T) {
			projector := newTestProjector(t, ProjectionLimits{})
			projector.PlanUpdated("turn-1", "plan-1", PlanState{
				Explanation: "Keep this plan",
				Steps:       []PlanStepState{{Step: "Verify", Status: PlanStepInProgress}},
			})
			streamer, ok := NewStreamDelegate(projector, "thread-1").GetStreamer(
				t.Context(),
				"coding",
				"thread-1",
				"thread-1",
				"",
				runtimeevents.NewTraceScope("/repo", "turn-1"),
			)
			if !ok {
				t.Fatal("matching stream was rejected")
			}

			switch action {
			case "update":
				if err := streamer.Update(t.Context(), "working"); err != nil {
					t.Fatal(err)
				}
			case "finalize":
				if err := streamer.Finalize(t.Context(), "done"); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				if err := streamer.Update(t.Context(), "discarded"); err != nil {
					t.Fatal(err)
				}
				streamer.Cancel(t.Context())
			}

			snapshot := snapshotForTest(t, projector)
			var plan *PlanState
			messageCount := 0
			for index := range snapshot.Items {
				if snapshot.Items[index].Plan != nil {
					plan = snapshot.Items[index].Plan
				}
				if snapshot.Items[index].Message != nil {
					messageCount++
				}
			}
			if plan == nil || plan.CallID != "plan-1" || plan.Explanation != "Keep this plan" ||
				len(plan.Steps) != 1 || plan.Steps[0].Step != "Verify" {
				t.Fatalf("%s removed typed plan: %+v", action, snapshot.Items)
			}
			wantMessages := 1
			if action == "cancel" {
				wantMessages = 0
			}
			if messageCount != wantMessages {
				t.Fatalf("%s message count = %d, want %d: %+v", action, messageCount, wantMessages, snapshot.Items)
			}
		})
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

func TestOverlappingStreamCancellationNeverResurrectsCanceledWriter(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	delegate := NewStreamDelegate(projector, "thread-1")
	traceScope := runtimeevents.NewTraceScope("/repo", "turn-1")
	first, ok := delegate.GetStreamer(t.Context(), "coding", "thread-1", "thread-1", "", traceScope)
	if !ok {
		t.Fatal("first stream was rejected")
	}
	if err := first.Update(t.Context(), "first attempt"); err != nil {
		t.Fatal(err)
	}
	second, ok := delegate.GetStreamer(t.Context(), "coding", "thread-1", "thread-1", "", traceScope)
	if !ok {
		t.Fatal("second stream was rejected")
	}
	if err := second.Update(t.Context(), "second attempt"); err != nil {
		t.Fatal(err)
	}

	first.Cancel(t.Context())
	afterFirstCancel := snapshotForTest(t, projector)
	if len(afterFirstCancel.Entries) != 1 || afterFirstCancel.Entries[0].Text != "second attempt" {
		t.Fatalf("first cancel removed the newer stream = %+v", afterFirstCancel)
	}
	second.Cancel(t.Context())
	afterSecondCancel := snapshotForTest(t, projector)
	if len(afterSecondCancel.Entries) != 0 || len(afterSecondCancel.Items) != 0 {
		t.Fatalf("second cancel resurrected the first stream = %+v", afterSecondCancel)
	}
	if len(projector.activeStreamOwners) != 0 || len(projector.entryVersions) != 0 ||
		len(projector.entryGenerations) != 0 {
		t.Fatalf("canceled streams retained rollback state")
	}
}

func TestAlternatingStreamWritersKeepOneVersionPerOwner(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	delegate := NewStreamDelegate(projector, "thread-1")
	traceScope := runtimeevents.NewTraceScope("/repo", "turn-1")
	first, ok := delegate.GetStreamer(t.Context(), "coding", "thread-1", "thread-1", "", traceScope)
	if !ok {
		t.Fatal("first stream was rejected")
	}
	second, ok := delegate.GetStreamer(t.Context(), "coding", "thread-1", "thread-1", "", traceScope)
	if !ok {
		t.Fatal("second stream was rejected")
	}
	for index := 0; index < 100; index++ {
		if err := first.Update(t.Context(), fmt.Sprintf("first-%d", index)); err != nil {
			t.Fatal(err)
		}
		if err := second.Update(t.Context(), fmt.Sprintf("second-%d", index)); err != nil {
			t.Fatal(err)
		}
	}

	snapshot := snapshotForTest(t, projector)
	if len(snapshot.Items) != 1 {
		t.Fatalf("alternating stream items = %+v", snapshot.Items)
	}
	versions := 0
	for version := projector.entryVersions[snapshot.Items[0].ID]; version != nil; version = version.previous {
		versions++
	}
	if versions != 2 {
		t.Fatalf("entry versions = %d, want one per active owner", versions)
	}
	first.Cancel(t.Context())
	second.Cancel(t.Context())
	if snapshot = snapshotForTest(t, projector); len(snapshot.Items) != 0 {
		t.Fatalf("alternating streams survived cancellation = %+v", snapshot)
	}
}

func TestFinalizePromotesReasoningShadowedByActiveStream(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	delegate := NewStreamDelegate(projector, "thread-1")
	traceScope := runtimeevents.NewTraceScope("/repo", "turn-1")
	first, ok := delegate.GetStreamer(t.Context(), "coding", "thread-1", "thread-1", "", traceScope)
	if !ok {
		t.Fatal("first stream was rejected")
	}
	second, ok := delegate.GetStreamer(t.Context(), "coding", "thread-1", "thread-1", "", traceScope)
	if !ok {
		t.Fatal("second stream was rejected")
	}
	firstReasoning := first.(bus.ReasoningStreamer)
	secondReasoning := second.(bus.ReasoningStreamer)
	if err := firstReasoning.UpdateReasoning(t.Context(), "R1"); err != nil {
		t.Fatal(err)
	}
	if err := secondReasoning.UpdateReasoning(t.Context(), "R2"); err != nil {
		t.Fatal(err)
	}
	if err := first.Finalize(t.Context(), "A1"); err != nil {
		t.Fatal(err)
	}
	second.Cancel(t.Context())

	snapshot := snapshotForTest(t, projector)
	if len(snapshot.Entries) != 2 || snapshot.Entries[0].Kind != EntryReasoning ||
		snapshot.Entries[0].Text != "R1" || snapshot.Entries[1].Kind != EntryAssistant ||
		snapshot.Entries[1].Text != "A1" || !snapshot.Entries[1].Complete {
		t.Fatalf("finalized shadowed reasoning = %+v", snapshot)
	}
}

func TestFinalizeCannotReplaceEntrySupersededByCommittedWriter(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	streamer, ok := NewStreamDelegate(projector, "thread-1").GetStreamer(
		t.Context(),
		"coding",
		"thread-1",
		"thread-1",
		"",
		runtimeevents.NewTraceScope("/repo", "turn-1"),
	)
	if !ok {
		t.Fatal("stream was rejected")
	}
	if err := streamer.Update(t.Context(), "provisional"); err != nil {
		t.Fatal(err)
	}
	projector.AssistantAccumulated("turn-1", "committed", true)
	if err := streamer.Finalize(t.Context(), "stale final"); err != nil {
		t.Fatal(err)
	}

	snapshot := snapshotForTest(t, projector)
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Text != "committed" ||
		!snapshot.Entries[0].Complete {
		t.Fatalf("finalize replaced newer committed writer = %+v", snapshot)
	}
}

func TestFinalizeReasoningCannotReplaceCommittedWriter(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	streamer, ok := NewStreamDelegate(projector, "thread-1").GetStreamer(
		t.Context(),
		"coding",
		"thread-1",
		"thread-1",
		"",
		runtimeevents.NewTraceScope("/repo", "turn-1"),
	)
	if !ok {
		t.Fatal("stream was rejected")
	}
	reasoning := streamer.(bus.ReasoningStreamer)
	if err := reasoning.UpdateReasoning(t.Context(), "provisional reasoning"); err != nil {
		t.Fatal(err)
	}
	projector.ReasoningAccumulated("turn-1", "committed reasoning", true)
	if err := reasoning.FinalizeReasoning(t.Context(), "stale reasoning"); err != nil {
		t.Fatal(err)
	}
	if err := streamer.Finalize(t.Context(), "accepted answer"); err != nil {
		t.Fatal(err)
	}

	snapshot := snapshotForTest(t, projector)
	if len(snapshot.Entries) != 2 || snapshot.Entries[0].Kind != EntryReasoning ||
		snapshot.Entries[0].Text != "committed reasoning" || !snapshot.Entries[0].Complete ||
		snapshot.Entries[1].Kind != EntryAssistant || snapshot.Entries[1].Text != "accepted answer" ||
		!snapshot.Entries[1].Complete {
		t.Fatalf("reasoning finalize replaced newer committed writer = %+v", snapshot)
	}
}

func TestOverlappingStreamCancellationRestoresCommittedBoundedWindow(t *testing.T) {
	for _, order := range []string{"first_then_second", "second_then_first"} {
		t.Run(order, func(t *testing.T) {
			projector := newTestProjector(t, ProjectionLimits{Entries: 2})
			projector.TurnStarted("committed", "A")
			projector.AssistantAccumulated("committed", "B", true)
			delegate := NewStreamDelegate(projector, "thread-1")
			first, ok := delegate.GetStreamer(
				t.Context(),
				"coding",
				"thread-1",
				"thread-1",
				"",
				runtimeevents.NewTraceScope("/repo", "first"),
			)
			if !ok {
				t.Fatal("first stream was rejected")
			}
			if err := first.Update(t.Context(), "C"); err != nil {
				t.Fatal(err)
			}
			second, ok := delegate.GetStreamer(
				t.Context(),
				"coding",
				"thread-1",
				"thread-1",
				"",
				runtimeevents.NewTraceScope("/repo", "second"),
			)
			if !ok {
				t.Fatal("second stream was rejected")
			}
			if err := second.Update(t.Context(), "D"); err != nil {
				t.Fatal(err)
			}

			if order == "first_then_second" {
				first.Cancel(t.Context())
				second.Cancel(t.Context())
			} else {
				second.Cancel(t.Context())
				first.Cancel(t.Context())
			}
			snapshot := snapshotForTest(t, projector)
			got := make([]string, len(snapshot.Entries))
			for index := range snapshot.Entries {
				got[index] = snapshot.Entries[index].Text
			}
			if !reflect.DeepEqual(got, []string{"A", "B"}) || snapshot.HasOlderEntries {
				t.Fatalf("restored committed window = %v; snapshot=%+v", got, snapshot)
			}
			if len(projector.rollbackCommittedMessages) != 0 {
				t.Fatalf("rollback window survived the final cancellation")
			}
		})
	}
}

func TestActiveStreamRollbackAuthorityRemainsBounded(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{Entries: 2})
	streamer, ok := NewStreamDelegate(projector, "thread-1").GetStreamer(
		t.Context(),
		"coding",
		"thread-1",
		"thread-1",
		"",
		runtimeevents.NewTraceScope("/repo", "provisional"),
	)
	if !ok {
		t.Fatal("stream was rejected")
	}
	if err := streamer.Update(t.Context(), "provisional"); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 100; index++ {
		projector.Warning("committed", fmt.Sprintf("warning-%d", index), fmt.Sprintf("committed-%d", index))
	}
	if len(projector.rollbackCommittedMessages) != 2 {
		t.Fatalf("rollback window size = %d, want 2", len(projector.rollbackCommittedMessages))
	}
	streamer.Cancel(t.Context())
	snapshot := snapshotForTest(t, projector)
	if len(snapshot.Entries) != 2 || snapshot.Entries[0].Text != "committed-98" ||
		snapshot.Entries[1].Text != "committed-99" || !snapshot.HasOlderEntries {
		t.Fatalf("bounded rollback authority = %+v", snapshot)
	}
}

func TestActiveStreamRollbackKeepsProtectedLateUser(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{Entries: 1, Tools: 1})
	projector.ToolStarted("turn-1", "call-1", "exec", "")
	streamer, ok := NewStreamDelegate(projector, "thread-1").GetStreamer(
		t.Context(),
		"coding",
		"thread-1",
		"thread-1",
		"",
		runtimeevents.NewTraceScope("/repo", "provisional"),
	)
	if !ok {
		t.Fatal("stream was rejected")
	}
	if err := streamer.Update(t.Context(), "provisional"); err != nil {
		t.Fatal(err)
	}
	projector.TurnStarted("turn-2", "unrelated")
	projector.TurnStarted("turn-1", "late user")
	streamer.Cancel(t.Context())

	items := snapshotForTest(t, projector).Items
	if len(items) != 2 || items[0].Kind != PresentationUserMessage ||
		items[0].TurnID != "turn-1" || items[1].Kind != PresentationToolCall ||
		items[1].TurnID != "turn-1" || items[0].Sequence >= items[1].Sequence {
		t.Fatalf("late user was evicted from committed rollback window = %+v", items)
	}
}

func TestStreamCancelRestoresInterleavedCommittedWriter(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	streamer, ok := NewStreamDelegate(projector, "thread-1").GetStreamer(
		t.Context(),
		"coding",
		"thread-1",
		"thread-1",
		"",
		runtimeevents.NewTraceScope("/repo", "turn-1"),
	)
	if !ok {
		t.Fatal("stream was rejected")
	}
	if err := streamer.Update(t.Context(), "first provisional value"); err != nil {
		t.Fatal(err)
	}
	projector.AssistantAccumulated("turn-1", "committed value", false)
	if err := streamer.Update(t.Context(), "second provisional value"); err != nil {
		t.Fatal(err)
	}

	streamer.Cancel(t.Context())
	snapshot := snapshotForTest(t, projector)
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Text != "committed value" {
		t.Fatalf("cancel did not restore interleaved committed writer = %+v", snapshot)
	}
}

func TestNoopStreamCannotClaimIdenticalLaterWriter(t *testing.T) {
	projector, err := NewProjector("thread-1", ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	delegate := NewStreamDelegate(projector, "thread-1")
	traceScope := runtimeevents.NewTraceScope("/repo", "turn-1")
	streamer, ok := delegate.GetStreamer(t.Context(), "coding", "thread-1", "thread-1", "", traceScope)
	if !ok {
		t.Fatal("matching stream was rejected")
	}
	projector.AssistantAccumulated("turn-1", "accepted answer", true)
	if err = streamer.Finalize(t.Context(), "accepted answer"); err != nil {
		t.Fatal(err)
	}
	beforeCancel, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	streamer.Cancel(t.Context())

	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot, beforeCancel) || len(snapshot.Entries) != 1 ||
		snapshot.Entries[0].Text != "accepted answer" || !snapshot.Entries[0].Complete {
		t.Fatalf("no-op stream reclaimed later writer = %+v", snapshot)
	}
}

func TestRejectedActiveStreamCannotClaimTerminalLaterWriter(t *testing.T) {
	projector, err := NewProjector("thread-1", ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	streamer, ok := NewStreamDelegate(projector, "thread-1").GetStreamer(
		t.Context(),
		"coding",
		"thread-1",
		"thread-1",
		"",
		runtimeevents.NewTraceScope("/repo", "turn-1"),
	)
	if !ok {
		t.Fatal("matching stream was rejected")
	}
	projector.AssistantAccumulated("turn-1", "accepted answer", true)
	if err = streamer.Update(t.Context(), "replacement attempt"); err != nil {
		t.Fatal(err)
	}
	beforeCancel, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	streamer.Cancel(t.Context())

	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot, beforeCancel) || len(snapshot.Entries) != 1 ||
		snapshot.Entries[0].Text != "accepted answer" || !snapshot.Entries[0].Complete {
		t.Fatalf("rejected active stream reclaimed terminal writer = %+v", snapshot)
	}
}

func TestStreamCancelRestoresCommittedWindowEvictedByProvisionalOutput(t *testing.T) {
	for _, test := range []struct {
		name       string
		laterWrite bool
		want       []string
	}{
		{name: "only provisional eviction", want: []string{"A", "B"}},
		{name: "preserve later committed writer", laterWrite: true, want: []string{"B", "D"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			projector, err := NewProjector("thread-1", ProjectionLimits{Entries: 2})
			if err != nil {
				t.Fatal(err)
			}
			projector.TurnStarted("turn-1", "A")
			projector.AssistantAccumulated("turn-1", "B", true)
			streamer, ok := NewStreamDelegate(projector, "thread-1").GetStreamer(
				t.Context(),
				"coding",
				"thread-1",
				"thread-1",
				"",
				runtimeevents.NewTraceScope("/repo", "turn-2"),
			)
			if !ok {
				t.Fatal("matching stream was rejected")
			}
			if err = streamer.Update(t.Context(), "C"); err != nil {
				t.Fatal(err)
			}
			if test.laterWrite {
				projector.Warning("turn-3", "later", "D")
			}
			streamer.Cancel(t.Context())

			snapshot, snapshotErr := projector.Snapshot(t.Context())
			if snapshotErr != nil {
				t.Fatal(snapshotErr)
			}
			got := make([]string, len(snapshot.Entries))
			for index := range snapshot.Entries {
				got[index] = snapshot.Entries[index].Text
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("restored entries = %v, want %v; snapshot=%+v", got, test.want, snapshot)
			}
			if test.laterWrite != snapshot.HasOlderEntries {
				t.Fatalf("has older entries = %v, want %v", snapshot.HasOlderEntries, test.laterWrite)
			}
		})
	}
}
