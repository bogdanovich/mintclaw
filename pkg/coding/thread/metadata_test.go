package thread

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
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

func TestMetadataRenameAndArchiveLifecycle(t *testing.T) {
	project, err := ResolveProject(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)
	metadata, err := NewMetadata(NewThreadID(), project, "initial title", created)
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := metadata.Rename("  focused parser work  ", created.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Title != "focused parser work" || !renamed.UpdatedAt.Equal(created.Add(time.Minute)) ||
		metadata.Title != "initial title" {
		t.Fatalf("rename result = %+v; original = %+v", renamed, metadata)
	}
	archived, err := renamed.SetArchived(true, created.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	active, err := archived.SetArchived(false, created.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if archived.Status != StatusArchived || active.Status != StatusActive ||
		!active.UpdatedAt.Equal(created.Add(3*time.Minute)) {
		t.Fatalf("lifecycle results = archived %+v active %+v", archived, active)
	}
	if _, err := metadata.Rename("   ", created); err == nil {
		t.Fatal("Rename() accepted an empty title")
	}
	if _, err := metadata.SetArchived(true, time.Time{}); err == nil {
		t.Fatal("SetArchived() accepted a zero timestamp")
	}
	clockRegressed, err := active.Rename("monotonic", created)
	if err != nil {
		t.Fatal(err)
	}
	if !clockRegressed.UpdatedAt.Equal(active.UpdatedAt) {
		t.Fatalf("clock regression moved updated_at backwards: %v", clockRegressed.UpdatedAt)
	}
}

func TestPendingMetadataPersistsAndRenameClaimsTitle(t *testing.T) {
	project, err := ResolveProject(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, time.August, 29, 17, 0, 0, 0, time.UTC)
	metadata, err := NewPendingMetadata(NewThreadID(), project, created)
	if err != nil {
		t.Fatal(err)
	}
	if !metadata.PendingFirstPrompt || metadata.Title != PendingThreadTitle ||
		metadata.Preview != PendingThreadTitle {
		t.Fatalf("pending metadata = %+v", metadata)
	}
	renamed, err := metadata.Rename("Investigate parser", created.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if renamed.PendingFirstPrompt || renamed.Title != "Investigate parser" {
		t.Fatalf("renamed pending metadata = %+v", renamed)
	}
}

func TestProvisionThreadDoesNotPublishMetadata(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "coding"))
	if err != nil {
		t.Fatal(err)
	}
	threadID := uuid.NewString()
	if err := store.ProvisionThread(threadID); err != nil {
		t.Fatalf("ProvisionThread() error = %v", err)
	}
	threadRoot, err := store.ThreadRoot(threadID)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(threadRoot)
	if err != nil || !info.IsDir() {
		t.Fatalf("provisioned root = %#v, %v", info, err)
	}
	if _, err := store.Load(threadID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load(provisioned-only) error = %v, want not exist", err)
	}
	catalog, err := NewCatalog(store, CatalogOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Query(t.Context(), CatalogQuery{ThreadID: threadID}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Query(provisioned-only) error = %v, want not exist", err)
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

func TestSavePreservesCommittedDirectorySyncFailure(t *testing.T) {
	project, err := ResolveProject(t.Context(), t.TempDir())
	if err != nil {
		t.Fatalf("ResolveProject() error = %v", err)
	}
	metadata, err := NewMetadata(uuid.NewString(), project, "request", time.Now())
	if err != nil {
		t.Fatalf("NewMetadata() error = %v", err)
	}
	storeRoot := filepath.Join(t.TempDir(), "missing", "coding")
	store, err := NewStore(storeRoot)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	wantErr := &fileutil.CommittedWriteError{Err: errors.New("injected directory sync failure")}
	var gotRoot, gotRelative string
	store.mkdirDurable = func(root, relative string, mode os.FileMode) error {
		gotRoot, gotRelative = root, relative
		if mode != 0o700 {
			t.Fatalf("directory mode = %o, want 700", mode)
		}
		return wantErr
	}
	store.writeAtomic = func(string, []byte, os.FileMode) error {
		t.Fatal("metadata write ran after directory durability failure")
		return nil
	}
	if err := store.Save(metadata); !fileutil.IsCommittedWriteError(err) {
		t.Fatalf("Save() error = %v, want committed-write classification", err)
	}
	if gotRoot == "" || gotRelative == "" || !strings.HasSuffix(gotRelative, metadata.ThreadID) {
		t.Fatalf("durable directory call = root %q relative %q", gotRoot, gotRelative)
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

func TestMetadataValidatesForkProvenance(t *testing.T) {
	project, err := ResolveProject(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := NewMetadata(uuid.NewString(), project, "fork", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	metadata.Fork = &ForkPoint{
		SourceRevision: 1, SourceMessageID: strings.Repeat("a", 64),
		SourceMessageIndex: 0, SourceTurn: 1, CopiedMessages: 1,
	}
	if err := metadata.Validate(); err == nil || !strings.Contains(err.Error(), "parent") {
		t.Fatalf("fork without parent error = %v", err)
	}
	metadata.ParentThread = uuid.NewString()
	if err := metadata.Validate(); err != nil {
		t.Fatalf("valid fork metadata error = %v", err)
	}
	metadata.Fork.SourceMessageID = "not-a-digest"
	if err := metadata.Validate(); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("invalid source identity error = %v", err)
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

func TestStoreRejectsCredentialBearingGitOriginOnSaveAndLoad(t *testing.T) {
	projectRoot := t.TempDir()
	project := ProjectIdentity{
		Kind:            ProjectKindGitWorktree,
		ProjectRoot:     projectRoot,
		InvocationCWD:   projectRoot,
		GitWorktreeRoot: projectRoot,
		GitDir:          projectRoot,
		GitCommonDir:    projectRoot,
		GitOrigin:       "https://example.com/owner/repo.git",
	}
	project.ProjectKey = projectKey(project.Kind, project.ProjectRoot)
	metadata, err := NewMetadata(uuid.NewString(), project, "request", time.Now())
	if err != nil {
		t.Fatalf("NewMetadata() error = %v", err)
	}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if err := store.Save(metadata); err != nil {
		t.Fatalf("Save(safe) error = %v", err)
	}

	metadata.Project.GitOrigin = "https://user:password@example.com/owner/repo.git?token=secret"
	if err := store.Save(metadata); err == nil || !strings.Contains(err.Error(), "credential-free canonical form") {
		t.Fatalf("Save(credential origin) error = %v", err)
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	path, err := store.metadataPath(metadata.ThreadID)
	if err != nil {
		t.Fatalf("metadataPath() error = %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := store.Load(metadata.ThreadID); err == nil ||
		!strings.Contains(err.Error(), "credential-free canonical form") {
		t.Fatalf("Load(credential origin) error = %v", err)
	}
}

func TestGitProjectWithoutGitDirIsRejectedOnCreateSaveAndLoad(t *testing.T) {
	projectRoot := t.TempDir()
	project := ProjectIdentity{
		Kind:            ProjectKindGitWorktree,
		ProjectRoot:     projectRoot,
		InvocationCWD:   projectRoot,
		GitWorktreeRoot: projectRoot,
		GitDir:          projectRoot,
		GitCommonDir:    projectRoot,
	}
	project.ProjectKey = projectKey(project.Kind, project.ProjectRoot)
	metadata, err := NewMetadata(uuid.NewString(), project, "request", time.Now())
	if err != nil {
		t.Fatalf("NewMetadata(valid) error = %v", err)
	}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if err := store.Save(metadata); err != nil {
		t.Fatalf("Save(valid) error = %v", err)
	}

	metadata.Project.GitDir = ""
	if _, err := NewMetadata(uuid.NewString(), metadata.Project, "request", time.Now()); err == nil ||
		!strings.Contains(err.Error(), "Git directory") {
		t.Fatalf("NewMetadata(missing GitDir) error = %v", err)
	}
	if err := store.Save(metadata); err == nil || !strings.Contains(err.Error(), "Git directory") {
		t.Fatalf("Save(missing GitDir) error = %v", err)
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	path, err := store.metadataPath(metadata.ThreadID)
	if err != nil {
		t.Fatalf("metadataPath() error = %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := store.Load(metadata.ThreadID); err == nil || !strings.Contains(err.Error(), "Git directory") {
		t.Fatalf("Load(missing GitDir) error = %v", err)
	}
}
