package thread

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

func TestMetadataAtomicRoundTrip(t *testing.T) {
	projectRoot := t.TempDir()
	project, err := ResolveProject(t.Context(), projectRoot)
	if err != nil {
		t.Fatalf("ResolveProject() error = %v", err)
	}
	now := time.Date(2026, time.August, 9, 12, 30, 0, 0, time.FixedZone("fixture", -7*60*60))
	metadata, err := NewMetadata(uuid.NewString(), project, "  Fix\n the parser\twithout a model title call.  ", now)
	if err != nil {
		t.Fatalf("NewMetadata() error = %v", err)
	}
	metadata.Model = "gpt-fixture"
	metadata.Provider = "fixture"

	storeRoot := filepath.Join(t.TempDir(), "coding")
	store, err := NewStore(storeRoot)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if _, statErr := os.Stat(storeRoot); !os.IsNotExist(statErr) {
		t.Fatalf("NewStore created state: %v", statErr)
	}
	if err := store.Save(metadata); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, err := store.Load(metadata.ThreadID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !reflect.DeepEqual(loaded, metadata) {
		t.Fatalf("round trip = %#v, want %#v", loaded, metadata)
	}
	if loaded.CreatedAt.Location() != time.UTC || loaded.UpdatedAt.Location() != time.UTC {
		t.Fatalf("timestamps are not UTC: created=%v updated=%v", loaded.CreatedAt, loaded.UpdatedAt)
	}
	if loaded.Title != "Fix the parser without a model title call." || loaded.Preview != loaded.Title {
		t.Fatalf("display metadata = %q / %q", loaded.Title, loaded.Preview)
	}
	metadataPath := filepath.Join(storeRoot, "threads", metadata.ThreadID, metadataFileName)
	info, err := os.Stat(metadataPath)
	if err != nil {
		t.Fatalf("stat metadata: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("metadata mode = %o, want 600", info.Mode().Perm())
	}
}

func TestStoreCanonicalizesSymlinkedExternalRoot(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	store, err := NewStore(filepath.Join(link, "coding"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	canonicalTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatalf("EvalSymlinks() error = %v", err)
	}
	if want := filepath.Join(canonicalTarget, "coding"); store.Root() != want {
		t.Fatalf("store root = %q, want %q", store.Root(), want)
	}
}

func TestMetadataFailedAtomicReplacementPreservesOriginal(t *testing.T) {
	project, err := ResolveProject(t.Context(), t.TempDir())
	if err != nil {
		t.Fatalf("ResolveProject() error = %v", err)
	}
	metadata, err := NewMetadata(uuid.NewString(), project, "original request", time.Now())
	if err != nil {
		t.Fatalf("NewMetadata() error = %v", err)
	}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if err := store.Save(metadata); err != nil {
		t.Fatalf("initial Save() error = %v", err)
	}

	wantErr := errors.New("injected pre-commit failure")
	store.writeAtomic = func(string, []byte, os.FileMode) error { return wantErr }
	changed := metadata
	changed.Title = "replacement"
	changed.UpdatedAt = changed.UpdatedAt.Add(time.Minute)
	if err := store.Save(changed); !errors.Is(err, wantErr) {
		t.Fatalf("replacement Save() error = %v, want %v", err, wantErr)
	}
	loaded, err := store.Load(metadata.ThreadID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !reflect.DeepEqual(loaded, metadata) {
		t.Fatalf("failed replacement changed metadata: %#v", loaded)
	}
}

func TestDisplayFromRequestBoundsUTF8(t *testing.T) {
	request := strings.Repeat("界", 100) + " tail"
	title, preview, err := DisplayFromRequest(request)
	if err != nil {
		t.Fatalf("DisplayFromRequest() error = %v", err)
	}
	for name, value := range map[string]string{"title": title, "preview": preview} {
		if !utf8.ValidString(value) {
			t.Fatalf("%s is invalid UTF-8: %q", name, value)
		}
	}
	if len(title) > titleMaxBytes || len(preview) > previewMaxBytes {
		t.Fatalf("display exceeds bounds: title=%d preview=%d", len(title), len(preview))
	}
	if !strings.HasSuffix(title, "…") || !strings.HasSuffix(preview, "…") {
		t.Fatalf("truncated display lacks ellipsis: %q / %q", title, preview)
	}
}

func TestMetadataRejectsPathAndIdentitySubstitution(t *testing.T) {
	project, err := ResolveProject(t.Context(), t.TempDir())
	if err != nil {
		t.Fatalf("ResolveProject() error = %v", err)
	}
	metadata, err := NewMetadata(uuid.NewString(), project, "request", time.Now())
	if err != nil {
		t.Fatalf("NewMetadata() error = %v", err)
	}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	for _, id := range []string{"../escape", strings.ToUpper(metadata.ThreadID), "not-a-uuid"} {
		if _, err := store.ThreadRoot(id); err == nil {
			t.Fatalf("ThreadRoot(%q) succeeded", id)
		}
	}

	metadata.SessionKey = SessionKey(uuid.NewString())
	if err := store.Save(metadata); err == nil {
		t.Fatal("Save() accepted a substituted session key")
	}
}

func TestLoadRejectsUnknownAndOversizedMetadata(t *testing.T) {
	project, err := ResolveProject(t.Context(), t.TempDir())
	if err != nil {
		t.Fatalf("ResolveProject() error = %v", err)
	}
	metadata, err := NewMetadata(uuid.NewString(), project, "request", time.Now())
	if err != nil {
		t.Fatalf("NewMetadata() error = %v", err)
	}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if err := store.Save(metadata); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	path, err := store.metadataPath(metadata.ThreadID)
	if err != nil {
		t.Fatalf("metadataPath() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	data = bytes.Replace(data, []byte(`"schema_version": 1,`), []byte(`"schema_version": 1, "unknown": true,`), 1)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile(unknown) error = %v", err)
	}
	if _, err := store.Load(metadata.ThreadID); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Load(unknown) error = %v", err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), MaxMetadataBytes+1), 0o600); err != nil {
		t.Fatalf("WriteFile(oversized) error = %v", err)
	}
	if _, err := store.Load(metadata.ThreadID); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Load(oversized) error = %v", err)
	}
}
