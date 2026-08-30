package coding

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	"github.com/bogdanovich/mintclaw/pkg/media"
)

func TestCodingAttachmentMediaMaterializesVerifiedThreadOwnedBytes(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	projectRoot := t.TempDir()
	project, err := thread.ResolveProject(t.Context(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	store, err := thread.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := thread.NewMetadata(thread.NewThreadID(), project, "attachment test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionThread(metadata.ThreadID); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(metadata); err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	source := filepath.Join(t.TempDir(), "build.log")
	if err := os.WriteFile(source, []byte("verified output"), 0o600); err != nil {
		t.Fatal(err)
	}
	attachment, err := store.AdmitAttachment(t.Context(), lease, metadata, thread.AttachmentInput{
		Path: source, At: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("replaced source"), 0o600); err != nil {
		t.Fatal(err)
	}

	resolver, err := newCodingAttachmentMediaStore(store, lease, metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	privatePath := resolver.privatePath
	path, meta, err := resolver.ResolveWithMeta(attachment.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, filepath.Clean(media.TempDir())+string(os.PathSeparator)) ||
		filepath.Dir(path) != privatePath {
		t.Fatalf("materialized path = %q, private root %q", path, privatePath)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "verified output" {
		t.Fatalf("materialized data = %q, %v", data, err)
	}
	if meta.Filename != "build.log" || meta.ContentType == "" || meta.Source != "coding-attachment" ||
		meta.CleanupPolicy != media.CleanupPolicyForgetOnly {
		t.Fatalf("materialized metadata = %+v", meta)
	}
	cachedPath, _, err := resolver.ResolveWithMeta(attachment.Ref)
	if err != nil || cachedPath != path {
		t.Fatalf("cached resolution = %q, %v", cachedPath, err)
	}
	if err := os.WriteFile(path, []byte("tampered in place"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolver.ResolveWithMeta(attachment.Ref); err == nil ||
		!strings.Contains(err.Error(), "materialized file changed") {
		t.Fatalf("in-place cache mutation error = %v", err)
	}
	movedPath := path + ".moved"
	if err := os.Rename(path, movedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("verified output"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolver.ResolveWithMeta(attachment.Ref); err == nil ||
		!strings.Contains(err.Error(), "materialized file changed") {
		t.Fatalf("replaced cache mutation error = %v", err)
	}
	if err := resolver.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(privatePath); !os.IsNotExist(err) {
		t.Fatalf("private materialization survived close: %v", err)
	}
	if _, _, err := resolver.ResolveWithMeta(attachment.Ref); err == nil ||
		!strings.Contains(err.Error(), "closed") {
		t.Fatalf("post-close resolution error = %v", err)
	}
}

func TestCodingAttachmentMediaRejectsAnotherThreadReference(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	project, err := thread.ResolveProject(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := thread.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, err := thread.NewMetadata(thread.NewThreadID(), project, "first", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	second, err := thread.NewMetadata(thread.NewThreadID(), project, "second", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, metadata := range []thread.Metadata{first, second} {
		if err := store.ProvisionThread(metadata.ThreadID); err != nil {
			t.Fatal(err)
		}
		if err := store.Save(metadata); err != nil {
			t.Fatal(err)
		}
	}
	lease, err := store.AcquireLease(first.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Release() }()
	source := filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(source, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	attachment, err := store.AdmitAttachment(t.Context(), lease, first, thread.AttachmentInput{
		Path: source, At: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	secondLease, err := store.AcquireLease(second.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = secondLease.Release() }()
	resolver, err := newCodingAttachmentMediaStore(store, secondLease, second.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resolver.Close() }()
	if _, _, err := resolver.ResolveWithMeta(attachment.Ref); !thread.IsAttachmentUnavailable(err) {
		t.Fatalf("cross-thread resolution error = %v", err)
	}
}
