package frontend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestPresentationItemsPreserveCausalOrderAndStableLifecycle(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	now := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.FixedZone("test", -7*60*60))
	projector.now = func() time.Time { return now }

	projector.AssistantAccumulated("turn-1", "working", false)
	now = now.Add(2 * time.Second)
	projector.ToolStarted("turn-1", "call-1", "exec", "fields: command")
	projector.Warning("turn-1", "retry-1", "retrying")
	projector.TurnStarted("turn-1", "fix it")

	view := snapshotForTest(t, projector)
	assertPresentationSequences(t, view.Items, []uint64{1, 2, 3, 4})
	if got := []PresentationKind{
		view.Items[0].Kind,
		view.Items[1].Kind,
		view.Items[2].Kind,
		view.Items[3].Kind,
	}; !reflect.DeepEqual(got, []PresentationKind{
		PresentationUserMessage,
		PresentationAssistantMessage,
		PresentationToolCall,
		PresentationWarning,
	}) {
		t.Fatalf("presentation order = %v", got)
	}
	assistant := view.Items[1]
	if assistant.Lifecycle != PresentationActive || assistant.Revision != 1 ||
		assistant.CreatedAt.Location() != time.UTC {
		t.Fatalf("active assistant = %+v", assistant)
	}

	now = now.Add(3 * time.Second)
	projector.AssistantAccumulated("turn-1", "done", true)
	projector.ToolCompleted("turn-1", "call-1", "exec", "ok", 2500*time.Millisecond, false, nil)
	completed := snapshotForTest(t, projector)
	assistant = completed.Items[1]
	if assistant.ID != view.Items[1].ID || assistant.Sequence != 2 || assistant.Revision != 2 ||
		assistant.Lifecycle != PresentationCompleted || assistant.CompletedAt == nil ||
		assistant.Duration != 5*time.Second {
		t.Fatalf("completed assistant = %+v", assistant)
	}
	tool := completed.Items[2]
	if tool.Lifecycle != PresentationCompleted || tool.CompletedAt == nil ||
		tool.Duration != 2500*time.Millisecond || tool.Tool == nil || tool.Tool.Status != ToolSucceeded {
		t.Fatalf("completed tool = %+v", tool)
	}
	if len(completed.Entries) != 3 || completed.Entries[0].Kind != EntryUser ||
		completed.Entries[1].Kind != EntryAssistant || completed.Entries[2].Kind != EntryWarning ||
		len(completed.Tools) != 1 || completed.Tools[0].CallID != "call-1" {
		t.Fatalf("compatibility projection = %+v", completed)
	}

	now = now.Add(time.Minute)
	projector.ToolCompleted("turn-1", "call-1", "exec", "ok", 2500*time.Millisecond, false, nil)
	idempotent := snapshotForTest(t, projector)
	if !reflect.DeepEqual(idempotent.Items[2], tool) {
		t.Fatalf("duplicate completion changed item: before=%+v after=%+v", tool, idempotent.Items[2])
	}
}

func TestPresentationIDsAreCollisionSafeAcrossTurnsAndCallIDs(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	projector.ToolStarted("a:b", "c", "first", "")
	projector.ToolStarted("a", "b:c", "second", "")
	projector.ToolStarted("turn-3", "c", "third", "")

	items := snapshotForTest(t, projector).Items
	if len(items) != 3 {
		t.Fatalf("items = %+v", items)
	}
	ids := map[string]struct{}{}
	for _, item := range items {
		if _, duplicate := ids[item.ID]; duplicate {
			t.Fatalf("duplicate presentation ID %q in %+v", item.ID, items)
		}
		ids[item.ID] = struct{}{}
	}
}

func TestTaggedLiteralIdentityCannotCollideWithLongRawIdentity(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	callID := strings.Repeat("call", 1024)
	digest := sha256.Sum256([]byte(callID))
	literalDigest := "~h:" + hex.EncodeToString(digest[:])
	projector.ToolStarted("turn-1", callID, "long", "")
	projector.ToolStarted("turn-1", literalDigest, "literal", "")

	items := snapshotForTest(t, projector).Items
	if len(items) != 2 || items[0].ID == items[1].ID || items[0].Tool.CallID != literalDigest ||
		items[1].Tool.CallID != "~r:"+literalDigest {
		t.Fatalf("domain-separated identities = %+v", items)
	}
	for _, item := range items {
		if len(item.ID) > maxPresentationIdentityBytes || len(item.Tool.CallID) > maxPresentationIdentityBytes {
			t.Fatalf("unbounded presentation identity = %+v", item)
		}
	}
}

func TestPresentationIdentitiesNormalizeInvalidUTF8WithoutCollisions(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	first := string([]byte{0xff, 'a'})
	second := string([]byte{0xff, 'b'})
	projector.ToolStarted("turn-1", first, "first", "")
	projector.ToolStarted("turn-1", second, "second", "")

	items := snapshotForTest(t, projector).Items
	if len(items) != 2 || items[0].ID == items[1].ID || items[0].Tool.CallID == items[1].Tool.CallID {
		t.Fatalf("invalid identities collided = %+v", items)
	}
	for _, item := range items {
		if !strings.HasPrefix(item.Tool.CallID, "~b:") || !utf8.ValidString(item.Tool.CallID) ||
			!utf8.ValidString(item.ID) {
			t.Fatalf("invalid identity was not normalized = %+v", item)
		}
	}
}

func TestPresentationTurnAndEntryIdentitiesAreBounded(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	projector.TurnStarted(strings.Repeat("turn", 1024), "hello")

	item := snapshotForTest(t, projector).Items[0]
	if len(item.ID) > maxPresentationIdentityBytes || len(item.TurnID) > maxPresentationIdentityBytes ||
		len(item.Message.ID) > maxPresentationIdentityBytes ||
		len(item.Message.TurnID) > maxPresentationIdentityBytes {
		t.Fatalf("unbounded message identity = %+v", item)
	}
}

func TestLateUserReservationSurvivesUnrelatedMessageChurn(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{Entries: 1, Tools: 1})
	projector.ToolStarted("turn-1", "call-1", "exec", "")
	projector.TurnStarted("turn-2", "second")
	projector.TurnStarted("turn-3", "third")
	projector.TurnStarted("turn-1", "first arrives late")

	items := snapshotForTest(t, projector).Items
	if len(items) != 2 || items[0].Kind != PresentationUserMessage ||
		items[0].TurnID != "turn-1" || items[1].Kind != PresentationToolCall ||
		items[1].TurnID != "turn-1" || items[0].Sequence >= items[1].Sequence {
		t.Fatalf("late user reservation was lost = %+v", items)
	}
}

func TestEmptyTurnStartsDoNotRetainUnrepresentedOrderingState(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{Entries: 1, Tools: 1})
	for index := 0; index < 100; index++ {
		projector.TurnStarted(fmt.Sprintf("turn-%d", index), "")
	}
	if len(projector.startedTurns) != 1 || len(projector.reservedUserSequences) != 0 {
		t.Fatalf(
			"unrepresented ordering state was not pruned: started=%d reserved=%d",
			len(projector.startedTurns),
			len(projector.reservedUserSequences),
		)
	}
}

func TestTerminalOrphanToolDoesNotRegressOnLateEvents(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	now := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)
	projector.now = func() time.Time { return now }
	projector.ToolCompleted("turn-1", "call-1", "exec", "failed", 2*time.Second, true, nil)
	orphan := snapshotForTest(t, projector).Items[0]

	now = now.Add(time.Minute)
	projector.ToolStarted("turn-1", "call-1", "exec", "fields: command")
	projector.ToolSuspended("turn-1", "call-1", "exec", 5*time.Second)
	late := snapshotForTest(t, projector).Items[0]
	if late.ID != orphan.ID || late.Sequence != orphan.Sequence || late.Lifecycle != PresentationFailed ||
		late.Tool == nil || late.Tool.Status != ToolFailed || late.CompletedAt == nil ||
		!late.CompletedAt.Equal(*orphan.CompletedAt) {
		t.Fatalf("late events regressed terminal orphan: before=%+v after=%+v", orphan, late)
	}
}

func TestSuspendedToolCanResumeWithoutLosingIdentity(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	now := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)
	projector.now = func() time.Time { return now }
	projector.ToolStarted("turn-1", "call-1", "exec", "fields: command")
	now = now.Add(3 * time.Second)
	projector.ToolSuspended("turn-1", "call-1", "exec", 3*time.Second)
	suspended := snapshotForTest(t, projector).Items[0]
	if suspended.Lifecycle != PresentationSuspended || suspended.CompletedAt == nil {
		t.Fatalf("suspended item = %+v", suspended)
	}

	now = now.Add(time.Minute)
	projector.ToolStarted("turn-1", "call-1", "exec", "fields: command")
	resumed := snapshotForTest(t, projector).Items[0]
	if resumed.ID != suspended.ID || resumed.Sequence != suspended.Sequence ||
		resumed.Revision != suspended.Revision+1 || resumed.Lifecycle != PresentationActive ||
		resumed.CompletedAt != nil || resumed.Duration != 0 || resumed.Tool.Status != ToolRunning {
		t.Fatalf("resumed item = %+v", resumed)
	}
}

func TestPresentationProjectionIsBoundedAndCompatibilityIsDerived(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{Entries: 2, Tools: 1})
	projector.TurnStarted("turn-1", "first")
	projector.AssistantAccumulated("turn-1", "answer", true)
	projector.ToolStarted("turn-1", "call-1", "first_tool", "")
	projector.TurnStarted("turn-2", "second")
	projector.ToolStarted("turn-2", "call-2", "second_tool", "")

	view := snapshotForTest(t, projector)
	if !view.HasOlderEntries || len(view.Items) != 3 || len(view.Entries) != 2 || len(view.Tools) != 1 {
		t.Fatalf("bounded projection = %+v", view)
	}
	if view.Entries[0].Kind != EntryAssistant || view.Entries[1].Text != "second" ||
		view.Tools[0].CallID != "call-2" {
		t.Fatalf("derived compatibility projection = %+v", view)
	}
	assertItemsStrictlyOrdered(t, view.Items)
	assertCompatibilityDerivedFromItems(t, view)
}

func TestCompactionLifecycleDoesNotReorderPresentationItems(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	projector.TurnStarted("turn-1", "fix it")
	projector.AssistantAccumulated("turn-1", "working", false)
	before := snapshotForTest(t, projector).Items

	projector.CompactionUpdate(CompactionState{
		TurnID: "turn-1", AttemptID: "attempt-1", Status: CompactionRunning,
	})
	projector.CompactionUpdate(CompactionState{
		TurnID: "turn-1", AttemptID: "attempt-1", Status: CompactionCompleted, TokensSaved: 100,
	})
	after := snapshotForTest(t, projector)
	if !reflect.DeepEqual(after.Items, before) || after.LastCompaction == nil ||
		after.LastCompaction.Status != CompactionCompleted || after.Activity != ActivityRunning {
		t.Fatalf("compaction reordered presentation items: before=%+v after=%+v", before, after)
	}
}

func TestSlowSubscriberKeepsCommittedAndLatestActivePresentationItems(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, updates, err := projector.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}

	projector.TurnStarted("turn-1", "fix it")
	projector.ToolStarted("turn-1", "call-1", "exec", "fields: command")
	projector.ToolCompleted("turn-1", "call-1", "exec", "ok", time.Second, false, nil)
	projector.AssistantAccumulated("turn-1", "wor", false)
	projector.AssistantAccumulated("turn-1", "working", false)

	latest := <-updates
	want := snapshotForTest(t, projector)
	if !reflect.DeepEqual(latest, want) || len(latest.Items) != 3 ||
		latest.Items[1].Lifecycle != PresentationCompleted || latest.Items[2].Lifecycle != PresentationActive {
		t.Fatalf("slow subscriber latest = %+v, want %+v", latest, want)
	}
}

func TestPresentationSnapshotCloneDoesNotAliasTypedPayloads(t *testing.T) {
	projector := newTestProjector(t, ProjectionLimits{})
	exitCode := 7
	projector.ToolCommandOutput("turn-1", "call-1", CommandState{
		Status: CommandFailed, ExitCode: &exitCode,
	})
	projector.ToolCompleted("turn-1", "call-1", "exec", "", time.Second, true, []WriteAudit{{
		Kind: "file", Target: "main.go", Action: "update", Success: true,
	}})

	view := snapshotForTest(t, projector)
	item := &view.Items[0]
	*item.CompletedAt = item.CompletedAt.Add(time.Hour)
	item.Tool.WriteAudit[0].Target = "consumer.go"
	*item.Tool.Command.ExitCode = 11
	stable := snapshotForTest(t, projector).Items[0]
	if stable.Tool.WriteAudit[0].Target != "main.go" || *stable.Tool.Command.ExitCode != 7 ||
		stable.CompletedAt.Equal(*item.CompletedAt) {
		t.Fatalf("presentation item aliased consumer state: %+v", stable)
	}
}

func assertPresentationSequences(t *testing.T, items []PresentationItem, want []uint64) {
	t.Helper()
	got := make([]uint64, len(items))
	for index := range items {
		got[index] = items[index].Sequence
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sequences = %v, want %v", got, want)
	}
}

func assertItemsStrictlyOrdered(t *testing.T, items []PresentationItem) {
	t.Helper()
	for index := 1; index < len(items); index++ {
		if items[index-1].Sequence >= items[index].Sequence {
			t.Fatalf("items are not strictly ordered: %+v", items)
		}
	}
}

func assertCompatibilityDerivedFromItems(t *testing.T, view ThreadSnapshot) {
	t.Helper()
	var entries []TranscriptEntry
	var tools []ToolState
	for _, item := range view.Items {
		if item.Message != nil {
			entries = append(entries, *item.Message)
		}
		if item.Tool != nil {
			tools = append(tools, *item.Tool)
		}
	}
	if !reflect.DeepEqual(entries, view.Entries) || !reflect.DeepEqual(tools, view.Tools) {
		t.Fatalf(
			"compatibility is not derived from items: items=%+v entries=%+v tools=%+v",
			view.Items,
			view.Entries,
			view.Tools,
		)
	}
}
