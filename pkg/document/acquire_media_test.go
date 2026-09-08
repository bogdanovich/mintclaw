package document

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/media"
)

func TestAcquireOwnedMediaPreservesSafeIdentity(t *testing.T) {
	root := directTempDir(t)
	inputPath := filepath.Join(root, "inbound.pdf")
	data := []byte("%PDF-1.7\nowned media fixture\n%%EOF\n")
	writeFixture(t, inputPath, data)
	store := media.NewFileMediaStore()
	ref, err := store.Store(inputPath, media.MediaMeta{
		Filename: "../../unsafe\\inbound.pdf", ContentType: "application/pdf", Source: "telegram",
	}, "inbound")
	if err != nil {
		t.Fatal(err)
	}
	owner := testMediaOwner(t)
	if err := store.BindOwner(ref, owner); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(root, "protected")
	snapshot, report := acquireMediaWithWorker(
		t.Context(), store, ref, owner, AcquireOptions{ScratchRoot: scratch}, "linux", "amd64", acceptingWorker{},
	)
	if snapshot == nil || report.State != StateSucceeded || report.Input == nil {
		t.Fatalf("report = %#v, want succeeded input", report)
	}
	if report.Input.SourceRef != ref || report.Input.SourceKind != "inbound_media" ||
		report.Input.OriginalFilename != "inbound.pdf" {
		t.Fatalf("source identity = %#v", report.Input)
	}
	if report.Input.Authority != documentAuthority(owner) {
		t.Fatalf("authority = %#v, want %#v", report.Input.Authority, documentAuthority(owner))
	}
	if report.Input.Size != int64(len(data)) || len(report.Input.SHA256) != 64 {
		t.Fatalf("immutable identity = %#v", report.Input)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{inputPath, root, snapshot.Path(), "owned media fixture", "telegram"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("report leaked %q: %s", forbidden, encoded)
		}
	}
	if err := snapshot.Close(); err != nil {
		t.Fatalf("close snapshot: %v", err)
	}
	assertEmptyDirectory(t, scratch)
}

func TestAcquireOwnedMediaDeniesEveryAuthorityMismatchBeforeSnapshot(t *testing.T) {
	root := directTempDir(t)
	inputPath := filepath.Join(root, "owned.pdf")
	writeFixture(t, inputPath, []byte("%PDF-1.7\nowned\n%%EOF\n"))
	store := media.NewFileMediaStore()
	ref, err := store.Store(inputPath, media.MediaMeta{Filename: "owned.pdf"}, "inbound")
	if err != nil {
		t.Fatal(err)
	}
	owner := testMediaOwner(t)
	if err := store.BindOwner(ref, owner); err != nil {
		t.Fatal(err)
	}

	tests := map[string]media.MediaOwner{}
	for _, field := range []string{"workspace", "agent", "actor", "route", "session"} {
		other := owner
		switch field {
		case "workspace":
			other.WorkspaceID = "workspace_other"
		case "agent":
			other.AgentID = "agent_other"
		case "actor":
			other.ActorID = "actor_other"
		case "route":
			other.RouteID = "route_other"
		case "session":
			other.SessionID = "session_other"
		}
		tests[field] = other
	}
	for name, other := range tests {
		t.Run(name, func(t *testing.T) {
			scratch := filepath.Join(root, "protected-"+name)
			snapshot, report := acquireMediaWithWorker(
				t.Context(), store, ref, other, AcquireOptions{ScratchRoot: scratch},
				"linux", "amd64", acceptingWorker{},
			)
			if snapshot != nil {
				t.Fatal("authority mismatch returned a snapshot")
			}
			assertFailure(t, report, StateDenied, FailureSourceUnauthorized)
			assertPathAbsent(t, scratch)
		})
	}
}

func TestAcquireOwnedMediaDeniesInvalidUnknownAndReleasedReferences(t *testing.T) {
	root := directTempDir(t)
	inputPath := filepath.Join(root, "owned.pdf")
	writeFixture(t, inputPath, []byte("%PDF-1.7\nowned\n%%EOF\n"))
	store := media.NewFileMediaStore()
	ref, err := store.Store(inputPath, media.MediaMeta{Filename: "owned.pdf"}, "inbound")
	if err != nil {
		t.Fatal(err)
	}
	owner := testMediaOwner(t)
	if err := store.BindOwner(ref, owner); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseAll("inbound"); err != nil {
		t.Fatal(err)
	}

	for name, candidate := range map[string]string{
		"unsupported": "https://example.invalid/document.pdf",
		"tampered":    ref + "-tampered",
		"released":    ref,
	} {
		t.Run(name, func(t *testing.T) {
			scratch := filepath.Join(root, "protected-"+name)
			snapshot, report := acquireMediaWithWorker(
				t.Context(), store, candidate, owner, AcquireOptions{ScratchRoot: scratch},
				"linux", "amd64", acceptingWorker{},
			)
			if snapshot != nil {
				t.Fatal("unavailable reference returned a snapshot")
			}
			assertFailure(t, report, StateDenied, FailureSourceUnauthorized)
			assertPathAbsent(t, scratch)
		})
	}
}

func TestAcquireOwnedMediaDeniesUnboundReference(t *testing.T) {
	root := directTempDir(t)
	inputPath := filepath.Join(root, "unbound.pdf")
	writeFixture(t, inputPath, []byte("%PDF-1.7\nunbound\n%%EOF\n"))
	store := media.NewFileMediaStore()
	ref, err := store.Store(inputPath, media.MediaMeta{Filename: "unbound.pdf"}, "inbound")
	if err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(root, "protected")
	snapshot, report := acquireMediaWithWorker(
		t.Context(), store, ref, testMediaOwner(t), AcquireOptions{ScratchRoot: scratch},
		"linux", "amd64", acceptingWorker{},
	)
	if snapshot != nil {
		t.Fatal("unbound reference returned a snapshot")
	}
	assertFailure(t, report, StateDenied, FailureSourceUnauthorized)
	assertPathAbsent(t, scratch)
}

func TestAcquireOwnedMediaDeniesExpiredReference(t *testing.T) {
	root := directTempDir(t)
	inputPath := filepath.Join(root, "expired.pdf")
	writeFixture(t, inputPath, []byte("%PDF-1.7\nexpired\n%%EOF\n"))
	store := media.NewFileMediaStoreWithCleanup(media.MediaCleanerConfig{MaxAge: time.Nanosecond})
	ref, err := store.Store(inputPath, media.MediaMeta{
		Filename: "expired.pdf", CleanupPolicy: media.CleanupPolicyForgetOnly,
	}, "expired")
	if err != nil {
		t.Fatal(err)
	}
	owner := testMediaOwner(t)
	if err := store.BindOwner(ref, owner); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if removed := store.CleanExpired(); removed != 1 {
		t.Fatalf("expired refs removed = %d, want 1", removed)
	}

	scratch := filepath.Join(root, "protected")
	snapshot, report := acquireMediaWithWorker(
		t.Context(), store, ref, owner, AcquireOptions{ScratchRoot: scratch}, "linux", "amd64", acceptingWorker{},
	)
	if snapshot != nil {
		t.Fatal("expired reference returned a snapshot")
	}
	assertFailure(t, report, StateDenied, FailureSourceUnauthorized)
	assertPathAbsent(t, scratch)
}

func TestAcquireOwnedMediaRejectsBackingFileReplacement(t *testing.T) {
	for _, replacementKind := range []string{"regular", "symlink"} {
		t.Run(replacementKind, func(t *testing.T) {
			root := directTempDir(t)
			inputPath := filepath.Join(root, "owned.pdf")
			targetPath := filepath.Join(root, "replacement.pdf")
			writeFixture(t, inputPath, []byte("%PDF-1.7\noriginal\n%%EOF\n"))
			writeFixture(t, targetPath, []byte("%PDF-1.7\nreplaced\n%%EOF\n"))
			store := media.NewFileMediaStore()
			ref, err := store.Store(inputPath, media.MediaMeta{Filename: "owned.pdf"}, "inbound")
			if err != nil {
				t.Fatal(err)
			}
			owner := testMediaOwner(t)
			if err := store.BindOwner(ref, owner); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(inputPath); err != nil {
				t.Fatal(err)
			}
			switch replacementKind {
			case "regular":
				writeFixture(t, inputPath, []byte("%PDF-1.7\nreplaced\n%%EOF\n"))
			case "symlink":
				if err := os.Symlink(targetPath, inputPath); err != nil {
					t.Fatal(err)
				}
			}

			scratch := filepath.Join(root, "protected")
			worker := &countingWorker{}
			snapshot, report := acquireMediaWithWorker(
				t.Context(), store, ref, owner, AcquireOptions{ScratchRoot: scratch},
				"linux", "amd64", worker,
			)
			if snapshot != nil {
				t.Fatal("replaced backing file returned a snapshot")
			}
			assertFailure(t, report, StateDenied, FailureSourceUnauthorized)
			if worker.calls != 0 {
				t.Fatalf("worker calls = %d, want 0", worker.calls)
			}
			assertPathAbsent(t, scratch)
		})
	}
}

func TestAcquireOwnedMediaDeniesMutationAfterAuthorizedOpen(t *testing.T) {
	root := directTempDir(t)
	inputPath := filepath.Join(root, "owned.pdf")
	writeFixture(t, inputPath, []byte("%PDF-1.7\noriginal\n%%EOF\n"))
	store := media.NewFileMediaStore()
	ref, err := store.Store(inputPath, media.MediaMeta{Filename: "owned.pdf"}, "inbound")
	if err != nil {
		t.Fatal(err)
	}
	owner := testMediaOwner(t)
	if err := store.BindOwner(ref, owner); err != nil {
		t.Fatal(err)
	}
	resolver := &mutatingOwnedMediaResolver{
		FileMediaStore: store,
		path:           inputPath,
		replacement:    []byte("%PDF-1.7\nreplaced\n%%EOF\n"),
	}
	worker := &countingWorker{}
	scratch := filepath.Join(root, "protected")
	snapshot, report := acquireMediaWithWorker(
		t.Context(), resolver, ref, owner, AcquireOptions{ScratchRoot: scratch},
		"linux", "amd64", worker,
	)
	if snapshot != nil {
		t.Fatal("mutated authority-bound source returned a snapshot")
	}
	assertFailure(t, report, StateDenied, FailureSourceUnauthorized)
	if worker.calls != 0 {
		t.Fatalf("worker calls = %d, want 0", worker.calls)
	}
	assertEmptyDirectory(t, scratch)
}

func TestAcquireOwnedMediaDeniesGrowthAfterAuthorizedOpen(t *testing.T) {
	root := directTempDir(t)
	inputPath := filepath.Join(root, "owned.pdf")
	data := []byte("%PDF-1.7\noriginal\n%%EOF\n")
	writeFixture(t, inputPath, data)
	store := media.NewFileMediaStore()
	ref, err := store.Store(inputPath, media.MediaMeta{Filename: "owned.pdf"}, "inbound")
	if err != nil {
		t.Fatal(err)
	}
	owner := testMediaOwner(t)
	if err := store.BindOwner(ref, owner); err != nil {
		t.Fatal(err)
	}
	resolver := &mutatingOwnedMediaResolver{
		FileMediaStore: store,
		path:           inputPath,
		replacement:    append(append([]byte(nil), data...), bytes.Repeat([]byte("x"), 64)...),
	}
	worker := &countingWorker{}
	scratch := filepath.Join(root, "protected")
	snapshot, report := acquireMediaWithWorker(
		t.Context(), resolver, ref, owner,
		AcquireOptions{ScratchRoot: scratch, MaxBytes: int64(len(data) + 1)},
		"linux", "amd64", worker,
	)
	if snapshot != nil {
		t.Fatal("grown authority-bound source returned a snapshot")
	}
	assertFailure(t, report, StateDenied, FailureSourceUnauthorized)
	if worker.calls != 0 {
		t.Fatalf("worker calls = %d, want 0", worker.calls)
	}
	assertPathAbsent(t, scratch)
}

func TestAcquireOwnedMediaReportsLimitForPinnedInput(t *testing.T) {
	root := directTempDir(t)
	inputPath := filepath.Join(root, "owned.pdf")
	data := []byte("%PDF-1.7\noriginal\n%%EOF\n")
	writeFixture(t, inputPath, data)
	store := media.NewFileMediaStore()
	ref, err := store.Store(inputPath, media.MediaMeta{Filename: "owned.pdf"}, "inbound")
	if err != nil {
		t.Fatal(err)
	}
	owner := testMediaOwner(t)
	if err := store.BindOwner(ref, owner); err != nil {
		t.Fatal(err)
	}
	worker := &countingWorker{}
	scratch := filepath.Join(root, "protected")
	snapshot, report := acquireMediaWithWorker(
		t.Context(), store, ref, owner,
		AcquireOptions{ScratchRoot: scratch, MaxBytes: int64(len(data) - 1)},
		"linux", "amd64", worker,
	)
	if snapshot != nil {
		t.Fatal("oversized pinned source returned a snapshot")
	}
	assertFailure(t, report, StateFailed, FailureLimitExceeded)
	if worker.calls != 0 {
		t.Fatalf("worker calls = %d, want 0", worker.calls)
	}
	assertPathAbsent(t, scratch)
}

func TestAcquireOwnedMediaDeniesGrowthBetweenVerificationReads(t *testing.T) {
	root := directTempDir(t)
	inputPath := filepath.Join(root, "owned.pdf")
	data := []byte("%PDF-1.7\noriginal\n%%EOF\n")
	writeFixture(t, inputPath, data)
	store := media.NewFileMediaStore()
	ref, err := store.Store(inputPath, media.MediaMeta{Filename: "owned.pdf"}, "inbound")
	if err != nil {
		t.Fatal(err)
	}
	owner := testMediaOwner(t)
	if err := store.BindOwner(ref, owner); err != nil {
		t.Fatal(err)
	}
	source, err := store.OpenOwned(ref, owner)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	scratch := filepath.Join(root, "protected")
	report := newReport("document_operation_growth_between_reads", int64(len(data)+1))
	snapshot, report := acquireSnapshotSource(
		t.Context(),
		acquisitionSource{
			file:               source.File,
			expectedIdentity:   &source.Identity,
			authorizationBound: true,
			ref:                ref,
			filename:           "owned.pdf",
			kind:               "inbound_media",
			authority:          documentAuthority(owner),
		},
		scratch,
		report,
		func() {
			file, openErr := os.OpenFile(inputPath, os.O_WRONLY|os.O_APPEND, 0)
			if openErr != nil {
				t.Fatal(openErr)
			}
			if _, writeErr := file.Write(bytes.Repeat([]byte("x"), 64)); writeErr != nil {
				_ = file.Close()
				t.Fatal(writeErr)
			}
			if closeErr := file.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
		},
	)
	if snapshot != nil {
		t.Fatal("source grown between reads returned a snapshot")
	}
	assertFailure(t, report, StateDenied, FailureSourceUnauthorized)
	assertEmptyDirectory(t, scratch)
}

func TestAcquireOwnedMediaChecksPlatformBeforeResolving(t *testing.T) {
	resolver := &countingOwnedMediaResolver{}
	scratch := filepath.Join(directTempDir(t), "must-not-exist")
	snapshot, report := acquireMediaWithWorker(
		t.Context(), resolver, "media://never-open", testMediaOwner(t), AcquireOptions{ScratchRoot: scratch},
		"darwin", "arm64", acceptingWorker{},
	)
	if snapshot != nil || resolver.calls != 0 {
		t.Fatalf("unsupported platform resolved source: snapshot=%#v calls=%d", snapshot, resolver.calls)
	}
	assertFailure(t, report, StateUnavailable, FailureUnsupportedPlatform)
	assertPathAbsent(t, scratch)
}

func TestAcquireOwnedMediaConcurrentSnapshotsAreIndependent(t *testing.T) {
	root := directTempDir(t)
	inputPath := filepath.Join(root, "concurrent.pdf")
	writeFixture(t, inputPath, []byte("%PDF-1.7\nconcurrent\n%%EOF\n"))
	store := media.NewFileMediaStore()
	ref, err := store.Store(inputPath, media.MediaMeta{Filename: "concurrent.pdf"}, "inbound")
	if err != nil {
		t.Fatal(err)
	}
	owner := testMediaOwner(t)
	if err := store.BindOwner(ref, owner); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(root, "protected")

	const workers = 8
	type result struct {
		snapshot *Snapshot
		report   Report
	}
	results := make(chan result, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			snapshot, report := acquireMediaWithWorker(
				context.Background(), store, ref, owner, AcquireOptions{ScratchRoot: scratch},
				"linux", "amd64", acceptingWorker{},
			)
			results <- result{snapshot: snapshot, report: report}
		}()
	}
	group.Wait()
	close(results)

	operationIDs := make(map[string]struct{}, workers)
	var digest string
	for got := range results {
		if got.snapshot == nil || got.report.State != StateSucceeded || got.report.Input == nil {
			t.Fatalf("concurrent acquisition failed: %#v", got.report)
		}
		if _, duplicate := operationIDs[got.report.OperationID]; duplicate {
			t.Fatalf("duplicate operation ID %q", got.report.OperationID)
		}
		operationIDs[got.report.OperationID] = struct{}{}
		if digest == "" {
			digest = got.report.Input.SHA256
		} else if got.report.Input.SHA256 != digest {
			t.Fatalf("digest = %q, want %q", got.report.Input.SHA256, digest)
		}
		if err := got.snapshot.Close(); err != nil {
			t.Fatalf("close concurrent snapshot: %v", err)
		}
	}
	if len(operationIDs) != workers {
		t.Fatalf("operation count = %d, want %d", len(operationIDs), workers)
	}
	assertEmptyDirectory(t, scratch)
}

type acceptingWorker struct{}

func (acceptingWorker) Verify(_ context.Context, _ *Snapshot, input DocumentRef) WorkerResult {
	request := newWorkerRequest(input)
	return WorkerResult{
		SchemaVersion: WorkerResultSchemaVersion,
		OperationID:   request.OperationID,
		State:         StateSucceeded,
		Input:         &request.Input,
	}
}

type countingWorker struct {
	calls int
}

func (worker *countingWorker) Verify(_ context.Context, _ *Snapshot, _ DocumentRef) WorkerResult {
	worker.calls++
	return WorkerResult{}
}

type countingOwnedMediaResolver struct {
	calls int
}

type mutatingOwnedMediaResolver struct {
	*media.FileMediaStore
	path        string
	replacement []byte
}

func (resolver *mutatingOwnedMediaResolver) OpenOwned(
	ref string,
	owner media.MediaOwner,
) (*media.OwnedMediaSource, error) {
	source, err := resolver.FileMediaStore.OpenOwned(ref, owner)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(resolver.path, resolver.replacement, 0o600); err != nil {
		_ = source.Close()
		return nil, err
	}
	return source, nil
}

func (resolver *countingOwnedMediaResolver) OpenOwned(
	_ string,
	_ media.MediaOwner,
) (*media.OwnedMediaSource, error) {
	resolver.calls++
	return nil, os.ErrNotExist
}

func testMediaOwner(t *testing.T) media.MediaOwner {
	t.Helper()
	owner, err := media.NewMediaOwner(
		"/workspace/main", "main", "actor-a", "telegram:chat-1:topic-1", "session-1",
		"telegram", "chat-1", "topic-1",
	)
	if err != nil {
		t.Fatal(err)
	}
	return owner
}

func assertPathAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("path %q exists or could not be checked: %v", path, err)
	}
}

func assertEmptyDirectory(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatalf("read directory %q: %v", path, err)
	}
	if len(entries) != 0 {
		t.Fatalf("directory %q retained entries: %+v", path, entries)
	}
}
