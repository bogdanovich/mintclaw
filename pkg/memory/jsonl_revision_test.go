package memory

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func TestTruncateHistoryRepairsIndeterminatePartialAppend(t *testing.T) {
	store, storeErr := NewJSONLStore(t.TempDir())
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	ctx := context.Background()
	key := "partial-before-truncate"
	if err := store.AddMessage(ctx, key, "user", "stable"); err != nil {
		t.Fatal(err)
	}

	injectedErr := errors.New("partial append")
	store.appendWrite = func(file *os.File, data []byte) (int, error) {
		written, writeErr := file.Write(data[:len(data)/2])
		if writeErr != nil {
			return written, writeErr
		}
		return written, injectedErr
	}
	if err := store.AddMessage(ctx, key, "user", "incomplete"); !IsIndeterminateAppendError(err) {
		t.Fatalf("AddMessage(partial) error = %v", err)
	}
	store.appendWrite = nil

	if err := store.TruncateHistory(ctx, key, 1); err != nil {
		t.Fatalf("TruncateHistory: %v", err)
	}
	if err := store.AddMessage(ctx, key, "user", "after"); err != nil {
		t.Fatalf("AddMessage(after): %v", err)
	}
	history, err := store.GetHistory(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].Content != "stable" || history[1].Content != "after" {
		t.Fatalf("history after truncate recovery = %#v", history)
	}
}

func TestDirtyEqualCountReplacementUsesTargetDigest(t *testing.T) {
	for _, targetReached := range []bool{false, true} {
		name := "before-rename"
		if targetReached {
			name = "after-rename"
		}
		t.Run(name, func(t *testing.T) {
			store, storeErr := NewJSONLStore(t.TempDir())
			if storeErr != nil {
				t.Fatal(storeErr)
			}
			ctx := context.Background()
			key := "equal-count-" + name
			createdAt := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
			oldHistory := []providers.Message{
				{Role: "user", Content: "hidden-old", CreatedAt: &createdAt},
				{Role: "assistant", Content: "visible-old", CreatedAt: &createdAt},
			}
			if err := store.SetHistory(ctx, key, oldHistory); err != nil {
				t.Fatal(err)
			}
			if err := store.TruncateHistory(ctx, key, 1); err != nil {
				t.Fatal(err)
			}

			previous, err := store.readMeta(key)
			if err != nil {
				t.Fatal(err)
			}
			replacement := []providers.Message{
				{Role: "user", Content: "replacement-one", CreatedAt: &createdAt},
				{Role: "assistant", Content: "replacement-two", CreatedAt: &createdAt},
			}
			encodedReplacement, err := encodeJSONL(replacement)
			if err != nil {
				t.Fatal(err)
			}
			interrupted := previous
			interrupted.Skip = 0
			interrupted.Count = len(replacement)
			interrupted.HistoryDirty = true
			interrupted.HistoryHasPrevious = true
			interrupted.HistoryPreviousCount = previous.Count
			interrupted.HistoryPreviousSkip = previous.Skip
			interrupted.HistoryTargetDigest = digestJSONL(encodedReplacement)
			if err := store.writeMeta(key, interrupted); err != nil {
				t.Fatal(err)
			}
			if targetReached {
				if err := store.rewriteJSONLBytes(key, encodedReplacement); err != nil {
					t.Fatal(err)
				}
			}

			if err := store.AddMessage(ctx, key, "user", "after"); err != nil {
				t.Fatal(err)
			}
			history, err := store.GetHistory(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"visible-old", "after"}
			if targetReached {
				want = []string{"replacement-one", "replacement-two", "after"}
			}
			if len(history) != len(want) {
				t.Fatalf("history length = %d, want %d: %#v", len(history), len(want), history)
			}
			for i := range want {
				if history[i].Content != want[i] {
					t.Fatalf("history[%d] = %q, want %q", i, history[i].Content, want[i])
				}
			}
		})
	}
}

func TestJSONLHistoryRevisionTracksLogicalMutations(t *testing.T) {
	store, storeErr := NewJSONLStore(t.TempDir())
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	ctx := context.Background()
	key := "revision"
	initial, err := store.GetHistoryRevision(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddMessage(ctx, key, "user", "one"); err != nil {
		t.Fatal(err)
	}
	appended, _ := store.GetHistoryRevision(ctx, key)
	if appended.Revision != initial.Revision+1 || appended.Count != 1 || appended.Dirty {
		t.Fatalf("append revision = %+v", appended)
	}
	if err := store.TruncateHistory(ctx, key, 0); err != nil {
		t.Fatal(err)
	}
	truncated, _ := store.GetHistoryRevision(ctx, key)
	if truncated.Revision != appended.Revision+1 {
		t.Fatalf("truncate revision = %+v", truncated)
	}
	if err := store.Compact(ctx, key); err != nil {
		t.Fatal(err)
	}
	compacted, _ := store.GetHistoryRevision(ctx, key)
	if compacted.Revision != truncated.Revision || compacted.Skip != 0 {
		t.Fatalf("compact revision = %+v", compacted)
	}
	if err := store.SetHistory(ctx, key, []providers.Message{{Role: "user", Content: "replacement"}}); err != nil {
		t.Fatal(err)
	}
	replaced, _ := store.GetHistoryRevision(ctx, key)
	if replaced.Revision != compacted.Revision+1 || replaced.Count != 1 {
		t.Fatalf("replace revision = %+v", replaced)
	}
}

func TestJSONLHistoryRevisionRepairsDirtyMetadata(t *testing.T) {
	store, storeErr := NewJSONLStore(t.TempDir())
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	ctx := context.Background()
	key := "dirty"
	if err := store.AddMessage(ctx, key, "user", "one"); err != nil {
		t.Fatal(err)
	}
	meta, _ := store.readMeta(key)
	meta.HistoryDirty = true
	meta.Count = 99
	if err := store.writeMeta(key, meta); err != nil {
		t.Fatal(err)
	}
	revision, err := store.GetHistoryRevision(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if revision.Dirty || revision.Count != 1 {
		t.Fatalf("recovered revision = %+v", revision)
	}
}

func TestJSONLHistoryRevisionRestoresMetadataAfterInterruptedCompact(t *testing.T) {
	store, storeErr := NewJSONLStore(t.TempDir())
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	ctx := context.Background()
	key := "interrupted-compact"
	for _, content := range []string{"one", "two", "three"} {
		if err := store.AddMessage(ctx, key, "user", content); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.TruncateHistory(ctx, key, 1); err != nil {
		t.Fatal(err)
	}
	previous, _ := store.readMeta(key)
	interrupted := previous
	interrupted.Count = 1
	interrupted.Skip = 0
	interrupted.HistoryDirty = true
	interrupted.HistoryHasPrevious = true
	interrupted.HistoryPreviousCount = previous.Count
	interrupted.HistoryPreviousSkip = previous.Skip
	active, err := readMessages(store.jsonlPath(key), previous.Skip)
	if err != nil {
		t.Fatal(err)
	}
	encodedActive, err := encodeJSONL(active)
	if err != nil {
		t.Fatal(err)
	}
	interrupted.HistoryTargetDigest = digestJSONL(encodedActive)
	if err := store.writeMeta(key, interrupted); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetHistoryRevision(ctx, key); err != nil {
		t.Fatal(err)
	}
	history, historyErr := store.GetHistory(ctx, key)
	if historyErr != nil {
		t.Fatal(historyErr)
	}
	if len(history) != 1 || history[0].Content != "three" {
		t.Fatalf("interrupted compact exposed history: %#v", history)
	}
}
