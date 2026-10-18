package seahorse

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func snapshotFixture(t *testing.T, mutate func(*Engine)) []byte {
	t.Helper()
	name := filepath.Join(t.TempDir(), "store.db")
	engine, err := NewEngine(t.Context(), Config{DBPath: name}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = engine.Ingest(t.Context(), "snapshot-session", []Message{
		{Role: "user", Content: `{"name":"snapshot\u002dtest"}`, TokenCount: 10},
	}); err != nil {
		_ = engine.Close()
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(engine)
	}
	if engine.Close() != nil {
		t.Fatal("native close failed")
	}
	image, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return image
}

func TestInspectSnapshotReadsNativeImageWithoutChangingOriginal(t *testing.T) {
	image := snapshotFixture(t, nil)
	before := sha256.Sum256(image)
	sawText, sawIndex := false, false
	stats, err := InspectSnapshot(t.Context(), image, func(value []byte, kind SnapshotValueKind) error {
		sawText = sawText || kind == SnapshotText && bytes.Contains(value, []byte("snapshot\\u002dtest"))
		sawIndex = sawIndex || kind == SnapshotIndexBlob
		return nil
	})
	if err != nil || !sawText || !sawIndex || stats.Rows == 0 || stats.TextValues == 0 || stats.BinaryValues == 0 ||
		before != sha256.Sum256(image) {
		t.Fatal("native image was modified or current value coverage was lost", stats, err)
	}
}

func TestInspectSnapshotRefusesSchemaTypesIntegrityAndBounds(t *testing.T) {
	for _, kind := range []string{"schema", "view", "blob-in-text", "large-cell", "rows", "corrupt", "header", "size", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			image := snapshotFixture(t, func(engine *Engine) {
				var err error
				switch kind {
				case "schema":
					_, err = engine.store.db.Exec("CREATE TABLE foreign_data(secret TEXT)")
				case "view":
					_, err = engine.store.db.Exec("CREATE VIEW foreign_view AS SELECT 1")
				case "blob-in-text":
					_, err = engine.store.db.Exec("UPDATE messages SET content = x'000102'")
				case "large-cell":
					_, err = engine.store.db.Exec(
						"UPDATE messages SET content = ?",
						strings.Repeat("x", SnapshotMaxValueBytes+1),
					)
				case "rows":
					_, err = engine.store.db.Exec(
						`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<8193)
						INSERT INTO context_items(conversation_id,ordinal,item_type) SELECT 1,x+100,'message' FROM n`,
					)
				}
				if err != nil {
					t.Fatal(err)
				}
			})
			switch kind {
			case "corrupt":
				clear(image[100:])
			case "header":
				image[56], image[59] = 0, 2
			case "size":
				image = make([]byte, SnapshotMaxBytes+1)
			}
			ctx := t.Context()
			if kind == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			before := sha256.Sum256(image)
			_, err := InspectSnapshot(ctx, image, func([]byte, SnapshotValueKind) error { return nil })
			if !errors.Is(err, ErrSnapshotUnavailable) || sha256.Sum256(image) != before {
				t.Fatal("invalid or incomplete native snapshot was admitted or mutated", err)
			}
		})
	}
}

func TestInspectSnapshotPreservesVisitorFailureWithoutExportingSQLiteErrors(t *testing.T) {
	image := snapshotFixture(t, nil)
	private := errors.New("synthetic private visitor failure")
	_, err := InspectSnapshot(t.Context(), image, func([]byte, SnapshotValueKind) error { return private })
	if !errors.Is(err, private) {
		t.Fatal("value guard failure was hidden")
	}
	image[100] = 0
	_, err = InspectSnapshot(t.Context(), image, func([]byte, SnapshotValueKind) error { return nil })
	if !errors.Is(err, ErrSnapshotUnavailable) || err.Error() != ErrSnapshotUnavailable.Error() {
		t.Fatal("raw SQLite diagnostic was exported", err)
	}
}

func TestInspectSnapshotVisitsReassembledOverflowText(t *testing.T) {
	text := strings.Repeat("public-overflow-text-", 1000) + `{"value":"overflow\u002dmarker"}`
	image := snapshotFixture(t, func(engine *Engine) {
		if _, err := engine.store.db.Exec("UPDATE messages SET content = ?", text); err != nil {
			t.Fatal(err)
		}
	})
	sawComplete := false
	_, err := InspectSnapshot(t.Context(), image, func(value []byte, kind SnapshotValueKind) error {
		sawComplete = sawComplete || kind == SnapshotText && string(value) == text
		return nil
	})
	if err != nil || !sawComplete {
		t.Fatal("native overflow value was truncated or ignored", err)
	}
}
