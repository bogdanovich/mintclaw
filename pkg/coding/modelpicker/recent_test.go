package modelpicker

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestStoreRecordIsBoundedNewestFirstAndDeduplicated(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < RecentLimit+3; index++ {
		if _, err = store.Record(Route{Provider: "OpenAI", Model: fmt.Sprintf("model-%02d", index)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = store.Record(Route{Provider: "openai", Model: "MODEL-05"}); err != nil {
		t.Fatal(err)
	}
	recent, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != RecentLimit || recent[0] != (Route{Provider: "openai", Model: "MODEL-05"}) {
		t.Fatalf("recent routes = %+v", recent)
	}
	duplicates := 0
	for _, route := range recent {
		if routeKey(route) == "openai/model-05" {
			duplicates++
		}
	}
	if duplicates != 1 {
		t.Fatalf("deduplicated model count = %d in %+v", duplicates, recent)
	}
	info, err := os.Stat(filepath.Join(store.root, recentFileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("recent file mode = %o", info.Mode().Perm())
	}
}

func TestStoreLoadSkipsInvalidDuplicateAndExcessRoutes(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	data := `{"schema_version":1,"recent":[` +
		`{"provider":"openai","model":"shared"},` +
		`{"provider":"OPENAI","model":"SHARED"},` +
		`{"provider":"","model":"invalid"},` +
		`{"provider":"anthropic","model":"claude"}]}`
	if err = os.WriteFile(filepath.Join(root, recentFileName), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	recent, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 || recent[0].Provider != "openai" || recent[1].Provider != "anthropic" {
		t.Fatalf("sanitized recent routes = %+v", recent)
	}
}

func TestStoreRecordSerializesAcrossInstances(t *testing.T) {
	root := t.TempDir()
	first, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for index := 0; index < RecentLimit; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			store := first
			if index%2 == 1 {
				store = second
			}
			if _, recordErr := store.Record(Route{
				Provider: "fixture", Model: fmt.Sprintf("model-%02d", index),
			}); recordErr != nil {
				t.Errorf("Record() error: %v", recordErr)
			}
		}()
	}
	wait.Wait()
	recent, err := first.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != RecentLimit {
		t.Fatalf("concurrent recent route count = %d, want %d: %+v", len(recent), RecentLimit, recent)
	}
}

func TestStoreLoadRejectsUnsupportedOrOversizedState(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, recentFileName)
	if err = os.WriteFile(path, []byte(`{"schema_version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Load(); err == nil {
		t.Fatal("unsupported schema unexpectedly loaded")
	}
	if err = os.WriteFile(path, make([]byte, maxRecentBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Load(); err == nil {
		t.Fatal("oversized state unexpectedly loaded")
	}
}
