package thread

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestStorePresentationCheckpointIsBoundedReadOnlyDiagnosticState(t *testing.T) {
	root := t.TempDir()
	store, metadata, lease := newCurrentPlanTestThread(t, root)
	checkpoint := PresentationCheckpoint{
		SchemaVersion:        PresentationCheckpointSchemaVersion,
		ThreadID:             metadata.ThreadID,
		CapturedAt:           metadata.CreatedAt.Add(time.Minute),
		PresentationRevision: 9,
		Activity:             "idle",
		LastTurn:             &SafeTurnState{TurnID: "turn-safe", Outcome: "completed"},
		ItemCount:            2,
		MaximumSequence:      7,
		MaximumItemRevision:  3,
		KindCounts:           map[string]int{"user_message": 1, "final_answer": 1},
		LifecycleCounts:      map[string]int{"completed": 2},
		ContextUsedTokens:    120,
		ContextLimitTokens:   4096,
		HasOlderEntries:      true,
		LastCompaction: &SafeCompactionState{
			Status: "completed", TranscriptRevision: 4, TranscriptCount: 11,
			TokensBefore: 3000, TokensAfter: 1000, TokenCountsObserved: true, SummariesCreated: 1,
		},
	}
	if err := store.SavePresentationCheckpoint(t.Context(), lease, metadata, checkpoint); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(
		root,
		"coding",
		"threads",
		metadata.ThreadID,
		presentationDirectory,
		presentationCheckpointFileName,
	)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(before, []byte("private prompt")) {
		t.Fatal("checkpoint contains private content")
	}
	loaded, ok, err := store.LoadPresentationCheckpoint(t.Context(), metadata.ThreadID)
	if err != nil || !ok || !reflect.DeepEqual(loaded, checkpoint) {
		t.Fatalf("loaded checkpoint = (%#v, %v, %v)", loaded, ok, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("read-only checkpoint inspection changed durable state")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("checkpoint mode = (%#v, %v)", info, err)
	}
}

func TestPresentationCheckpointAbsentAndInvalidState(t *testing.T) {
	root := t.TempDir()
	store, metadata, lease := newCurrentPlanTestThread(t, root)
	if checkpoint, ok, err := store.LoadPresentationCheckpoint(t.Context(), metadata.ThreadID); err != nil || ok ||
		!reflect.DeepEqual(checkpoint, PresentationCheckpoint{}) {
		t.Fatalf("absent checkpoint = (%#v, %v, %v)", checkpoint, ok, err)
	}
	initial := PresentationCheckpoint{
		SchemaVersion: PresentationCheckpointSchemaVersion, ThreadID: metadata.ThreadID,
		CapturedAt: metadata.CreatedAt.Add(time.Minute), Activity: "idle",
	}
	if err := store.SavePresentationCheckpoint(t.Context(), lease, metadata, initial); err != nil {
		t.Fatalf("initial revision checkpoint: %v", err)
	}
	invalid := PresentationCheckpoint{
		SchemaVersion: PresentationCheckpointSchemaVersion, ThreadID: metadata.ThreadID,
		CapturedAt: metadata.CreatedAt.Add(time.Minute), PresentationRevision: 1,
		Activity: "idle", ItemCount: 1,
		KindCounts: map[string]int{"private prompt": 1}, LifecycleCounts: map[string]int{"completed": 1},
	}
	if err := store.SavePresentationCheckpoint(t.Context(), lease, metadata, invalid); err == nil {
		t.Fatal("unsafe presentation kind was accepted")
	}
	invalid.ItemCount = 0
	invalid.KindCounts = map[string]int{
		"user_message": int(^uint(0) >> 1),
		"final_answer": int(^uint(0) >> 1),
		"warning":      2,
	}
	invalid.LifecycleCounts = nil
	if err := store.SavePresentationCheckpoint(t.Context(), lease, metadata, invalid); err == nil {
		t.Fatal("overflowing presentation counts were accepted")
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	invalid.ItemCount = 0
	invalid.KindCounts = nil
	invalid.LifecycleCounts = nil
	if err := store.SavePresentationCheckpoint(t.Context(), lease, metadata, invalid); err == nil {
		t.Fatal("released lease saved presentation checkpoint")
	}
}
