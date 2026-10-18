package eval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/seahorse"
)

func TestRawEscapesFindsContiguousMixedNestedAndSurrogateCanaries(t *testing.T) {
	for _, value := range []string{"private-canary", "private-\U0001f680-canary"} {
		for _, data := range []string{
			value,
			strings.ReplaceAll(value, "-", `\u002d`),
			strings.ReplaceAll(value, "-", `\\u002d`),
			strings.ReplaceAll(value, "\U0001f680", `\ud83d\ude80`),
		} {
			scan := privateContentScan{facts: map[string]string{"value": value}, tokens: 1000}
			image := append(append([]byte{0xff, 0}, []byte(data)...), 0, 0xfe)
			before := sha256.Sum256(image)
			if !scan.rawEscapes(t.Context(), image) || scan.uncertain || sha256.Sum256(image) != before {
				t.Fatal("known raw escaped canary was lost or original image changed")
			}
		}
	}
	credential := privateContentScan{filter: strings.NewReplacer("private-key", "[redacted]"), tokens: 1000}
	if !credential.rawEscapes(t.Context(), []byte(`private\u002dkey`)) || credential.uncertain {
		t.Fatal("encoded credential did not reach the literal guard")
	}
}

func TestRawEscapeGuardBoundsDoNotBecomeAbsenceProof(t *testing.T) {
	for _, kind := range []string{"tokens", "span", "layers", "bytes", "canceled", "nil-context"} {
		t.Run(kind, func(t *testing.T) {
			scan := privateContentScan{facts: map[string]string{"value": "private-canary"}, tokens: 100000}
			data := []byte(`safe\u002dvalue`)
			ctx := t.Context()
			switch kind {
			case "tokens":
				scan.tokens = 0
			case "span":
				data = []byte(strings.Repeat(`\u0078`, MaxInputBytes/6+1))
			case "layers":
				data = []byte(strings.Repeat(`\`, 1024) + "n")
			case "bytes":
				data = make([]byte, maxPrivateScanFileBytes+1)
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "nil-context":
				ctx = nil
			}
			if !scan.rawEscapes(ctx, data) || !scan.uncertain {
				t.Fatal("raw scan budget exhaustion became a passing predicate")
			}
		})
	}
	scan := privateContentScan{facts: map[string]string{"value": "private-canary"}, tokens: 1000}
	if scan.rawEscapes(t.Context(), []byte("\xff\x00public\\q\\uZZZZ\x00")) || scan.uncertain {
		t.Fatal("unsupported binary bytes became a claimed canary match")
	}
}

func sqlitePrivateFixture(t *testing.T, content string, cancel bool) (string, []byte) {
	t.Helper()
	home := t.TempDir()
	name := filepath.Join(home, filepath.FromSlash(privateSQLitePath))
	engine, err := seahorse.NewEngine(t.Context(), seahorse.Config{DBPath: name}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = engine.Ingest(t.Context(), "private-fixture-session", []seahorse.Message{
		{Role: "user", Content: content, TokenCount: 20},
	}); err != nil {
		_ = engine.Close()
		t.Fatal(err)
	}
	if cancel {
		if err = engine.ClearSession(t.Context(), "private-fixture-session"); err != nil {
			_ = engine.Close()
			t.Fatal(err)
		}
	}
	if engine.Close() != nil {
		t.Fatal("native fixture close failed")
	}
	image, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return home, image
}

func TestPrivateSQLiteObservesNativeCurrentTextAndKeepsRawFileUnchanged(t *testing.T) {
	home, image := sqlitePrivateFixture(t, "Public synthetic conversation.", false)
	result := observePrivateSurfaces(t.Context(), home, map[string]string{"name": "private-sqlite-canary"}, nil, nil)
	after, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(privateSQLitePath)))
	if err != nil || !bytes.Equal(image, after) || !result.valid() || result.Code != "observed" ||
		result.SQLite == nil || result.SQLite.Code != "observed" || result.SQLite.Rows == 0 || result.OpaqueFiles != 0 {
		t.Fatal("native SQLite witness was absent, mutated input or fabricated unknown coverage", result, err)
	}
}

func TestPrivateSQLiteFailsEncodedResidualAfterNativeClear(t *testing.T) {
	value := "PRIVATE_SQLITE_RESIDUAL_CANARY"
	var encoded strings.Builder
	for _, character := range value {
		fmt.Fprintf(&encoded, "\\u%04x", character)
	}
	home, image := sqlitePrivateFixture(t, `{"name":"`+encoded.String()+`"}`, true)
	if bytes.Contains(image, []byte(value)) || !bytes.Contains(image, []byte(encoded.String())) {
		t.Fatal("native encoded-residual negative fixture unavailable")
	}
	result := observePrivateSurfaces(t.Context(), home, map[string]string{"name": value}, nil, nil)
	if !result.valid() || result.Code != "leak_observed" || !result.LeakObserved || result.verdict() != Fail ||
		result.SQLite == nil || result.SQLite.Code != "leak_observed" {
		t.Fatal("empty logical rows concealed an encoded raw canary", result)
	}
}

func TestPrivateSQLiteRefusesSidecarsCorruptionAndForeignLocation(t *testing.T) {
	for _, kind := range []string{"wal", "shm", "journal", "corrupt", "foreign", "text", "document", "directory"} {
		t.Run(kind, func(t *testing.T) {
			home, _ := sqlitePrivateFixture(t, "Public synthetic conversation.", false)
			name := filepath.Join(home, filepath.FromSlash(privateSQLitePath))
			var documents map[string]bool
			switch kind {
			case "wal", "shm", "journal":
				if err := os.WriteFile(name+"-"+kind, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			case "foreign":
				if err := os.Rename(name, filepath.Join(home, "other.db")); err != nil {
					t.Fatal(err)
				}
			case "corrupt", "text":
				data := []byte("SQLite format 3\x00broken")
				if kind == "text" {
					data = []byte("not a native SQLite image")
				}
				if err := os.WriteFile(name, data, 0o600); err != nil {
					t.Fatal(err)
				}
			case "document":
				data := []byte("%PDF-1.7\nsynthetic authorized source")
				documents = map[string]bool{digest(data): true}
				if err := os.WriteFile(name, data, 0o600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(name, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			result := observePrivateSurfaces(
				t.Context(),
				home,
				map[string]string{"name": "private-sqlite-canary"},
				nil,
				documents,
			)
			wantOpaque := 1
			if kind == "directory" {
				wantOpaque = 0
			}
			if !result.valid() || result.Code != "unavailable" || result.LeakObserved ||
				result.OpaqueFiles != wantOpaque {
				t.Fatal("incomplete or unadmitted native image became complete coverage", kind, result)
			}
		})
	}
}

func TestSQLiteSurfaceReceiptRejectsInconsistentAndOverflowedCounts(t *testing.T) {
	for _, invalid := range []SQLiteSurfaceObservation{
		{Scope: "other", Code: "observed"},
		{Scope: "seahorse_text_and_raw_json_escapes_v1", Code: "invalid"},
		{Scope: "seahorse_text_and_raw_json_escapes_v1", Code: "observed", Rows: -1},
		{Scope: "seahorse_text_and_raw_json_escapes_v1", Code: "observed", TextValues: int(^uint(0) >> 1)},
		{Scope: "seahorse_text_and_raw_json_escapes_v1", Code: "observed", IndexValues: int(^uint(0) >> 1)},
	} {
		if invalid.valid() {
			t.Fatal("invalid native value counts admitted")
		}
	}
	for _, code := range []string{"unavailable", "leak_observed", "observed"} {
		result := PrivateSurfaceObservation{
			Scope: "owned_profile_files_v1", Code: "observed",
			SQLite: &SQLiteSurfaceObservation{Scope: "seahorse_text_and_raw_json_escapes_v1", Code: code},
		}
		if result.valid() {
			t.Fatal("missing file count or non-passing child witness became an observed receipt")
		}
	}
}
