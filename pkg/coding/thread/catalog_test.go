package thread

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

func TestCatalogProjectScopeAllLastExplicitAndPagination(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	projectA := catalogFixtureProject(t, filepath.Join(root, "project-a"))
	projectB := catalogFixtureProject(t, filepath.Join(root, "project-b"))
	base := time.Date(2026, time.August, 10, 10, 0, 0, 0, time.UTC)
	threads := []Metadata{
		catalogFixtureMetadata(t, projectA, "a-old", base.Add(time.Minute)),
		catalogFixtureMetadata(t, projectB, "b-newest", base.Add(5*time.Minute)),
		catalogFixtureMetadata(t, projectA, "a-newest", base.Add(4*time.Minute)),
		catalogFixtureMetadata(t, projectA, "a-middle", base.Add(3*time.Minute)),
	}
	for _, metadata := range threads {
		if err := store.Save(metadata); err != nil {
			t.Fatalf("Save(%s) error = %v", metadata.Title, err)
		}
	}
	transcriptRoot, err := store.ThreadRoot(threads[2].ThreadID)
	if err != nil {
		t.Fatalf("ThreadRoot() error = %v", err)
	}
	transcriptPath := filepath.Join(transcriptRoot, "sessions", "deliberately-invalid.jsonl")
	if err := os.MkdirAll(filepath.Dir(transcriptPath), 0o700); err != nil {
		t.Fatalf("MkdirAll(transcript) error = %v", err)
	}
	if err := os.WriteFile(transcriptPath, []byte("not transcript JSON\n"), 0o000); err != nil {
		t.Fatalf("WriteFile(transcript) error = %v", err)
	}

	catalog, err := NewCatalog(store, CatalogOptions{PageSize: 2})
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	first, err := catalog.Query(t.Context(), CatalogQuery{ProjectKey: projectA.ProjectKey})
	if err != nil {
		t.Fatalf("Query(project first page) error = %v", err)
	}
	if got := catalogTitles(first.Threads); !reflect.DeepEqual(got, []string{"a-newest", "a-middle"}) {
		t.Fatalf("project first page = %v", got)
	}
	if !first.HasMore || first.NextOffset != 2 || first.ScanTruncated {
		t.Fatalf("project first pagination = %#v", first)
	}
	second, err := catalog.Query(t.Context(), CatalogQuery{ProjectKey: projectA.ProjectKey, Offset: 2})
	if err != nil {
		t.Fatalf("Query(project second page) error = %v", err)
	}
	if got := catalogTitles(second.Threads); !reflect.DeepEqual(got, []string{"a-old"}) {
		t.Fatalf("project second page = %v", got)
	}
	if second.HasMore || second.NextOffset != 0 {
		t.Fatalf("project second pagination = %#v", second)
	}

	all, err := catalog.Query(t.Context(), CatalogQuery{All: true, Limit: 2})
	if err != nil {
		t.Fatalf("Query(all) error = %v", err)
	}
	if got := catalogTitles(all.Threads); !reflect.DeepEqual(got, []string{"b-newest", "a-newest"}) {
		t.Fatalf("all projects = %v", got)
	}
	last, err := catalog.Query(t.Context(), CatalogQuery{ProjectKey: projectA.ProjectKey, Last: true})
	if err != nil {
		t.Fatalf("Query(last project) error = %v", err)
	}
	if got := catalogTitles(last.Threads); !reflect.DeepEqual(got, []string{"a-newest"}) {
		t.Fatalf("last project = %v", got)
	}
	lastAll, err := catalog.Query(t.Context(), CatalogQuery{All: true, Last: true})
	if err != nil {
		t.Fatalf("Query(last all) error = %v", err)
	}
	if got := catalogTitles(lastAll.Threads); !reflect.DeepEqual(got, []string{"b-newest"}) {
		t.Fatalf("last all = %v", got)
	}
	exact, err := catalog.Query(t.Context(), CatalogQuery{ThreadID: threads[1].ThreadID})
	if err != nil {
		t.Fatalf("Query(exact) error = %v", err)
	}
	if got := catalogTitles(exact.Threads); !reflect.DeepEqual(got, []string{"b-newest"}) {
		t.Fatalf("exact thread = %v", got)
	}
	searched, err := catalog.Query(t.Context(), CatalogQuery{
		ProjectKey: projectA.ProjectKey,
		Search:     "MIDDLE",
	})
	if err != nil {
		t.Fatalf("Query(search) error = %v", err)
	}
	if got := catalogTitles(searched.Threads); !reflect.DeepEqual(got, []string{"a-middle"}) {
		t.Fatalf("searched project page = %v", got)
	}
	searchedAll, err := catalog.Query(t.Context(), CatalogQuery{All: true, Search: "project-b"})
	if err != nil {
		t.Fatalf("Query(all search) error = %v", err)
	}
	if got := catalogTitles(searchedAll.Threads); !reflect.DeepEqual(got, []string{"b-newest"}) {
		t.Fatalf("searched all-project page = %v", got)
	}
}

func TestCatalogFreshStoreIsEmpty(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "not-created", "coding"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	catalog, err := NewCatalog(store, CatalogOptions{})
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	page, err := catalog.Query(t.Context(), CatalogQuery{All: true})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(page.Threads) != 0 || page.Scanned != 0 || page.SkippedTotal != 0 || page.ScanTruncated {
		t.Fatalf("fresh-store page = %#v", page)
	}
}

func TestCatalogSeparatesActiveAndArchivedThreads(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	project := catalogFixtureProject(t, t.TempDir())
	active := catalogFixtureMetadata(t, project, "active", time.Now())
	archived := catalogFixtureMetadata(t, project, "archived", time.Now().Add(time.Minute))
	archived.Status = StatusArchived
	for _, metadata := range []Metadata{active, archived} {
		if err := store.Save(metadata); err != nil {
			t.Fatal(err)
		}
	}
	activePage, err := NewCatalog(store, CatalogOptions{})
	if err != nil {
		t.Fatal(err)
	}
	gotActive, err := activePage.Query(t.Context(), CatalogQuery{ProjectKey: project.ProjectKey})
	if err != nil {
		t.Fatal(err)
	}
	gotArchived, err := activePage.Query(
		t.Context(), CatalogQuery{ProjectKey: project.ProjectKey, Archived: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := catalogTitles(gotActive.Threads); !reflect.DeepEqual(got, []string{"active"}) {
		t.Fatalf("active titles = %v", got)
	}
	if got := catalogTitles(gotArchived.Threads); !reflect.DeepEqual(got, []string{"archived"}) {
		t.Fatalf("archived titles = %v", got)
	}
	exact, err := activePage.Query(t.Context(), CatalogQuery{ThreadID: archived.ThreadID})
	if err != nil || len(exact.Threads) != 1 || exact.Threads[0].Status != StatusArchived {
		t.Fatalf("exact archived lookup = %+v, %v", exact, err)
	}
}

func TestCatalogExactIDRejectsInRootSymlinkedPathComponents(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	project := catalogFixtureProject(t, filepath.Join(root, "project"))
	catalog, err := NewCatalog(store, CatalogOptions{})
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}

	t.Run("thread directory", func(t *testing.T) {
		metadata := catalogFixtureMetadata(t, project, "in-root-thread", time.Now())
		threadsRoot := filepath.Join(store.Root(), "threads")
		target := filepath.Join(threadsRoot, "in-root-directory-target")
		if err := os.MkdirAll(target, 0o700); err != nil {
			t.Fatalf("MkdirAll(target) error = %v", err)
		}
		data, err := json.Marshal(metadata)
		if err != nil {
			t.Fatalf("Marshal() error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(target, metadataFileName), data, 0o600); err != nil {
			t.Fatalf("WriteFile(target) error = %v", err)
		}
		if err := os.Symlink(target, filepath.Join(threadsRoot, metadata.ThreadID)); err != nil {
			t.Skipf("thread-directory symlinks unavailable: %v", err)
		}
		if _, err := catalog.Query(t.Context(), CatalogQuery{ThreadID: metadata.ThreadID}); err == nil {
			t.Fatal("exact query followed a symlinked thread directory")
		}
	})

	t.Run("metadata file", func(t *testing.T) {
		metadata := catalogFixtureMetadata(t, project, "outside-metadata", time.Now())
		threadRoot, err := store.ThreadRoot(metadata.ThreadID)
		if err != nil {
			t.Fatalf("ThreadRoot() error = %v", err)
		}
		if err := os.MkdirAll(threadRoot, 0o700); err != nil {
			t.Fatalf("MkdirAll(thread) error = %v", err)
		}
		target := filepath.Join(threadRoot, "in-root-metadata-target.json")
		data, err := json.Marshal(metadata)
		if err != nil {
			t.Fatalf("Marshal() error = %v", err)
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			t.Fatalf("WriteFile(target) error = %v", err)
		}
		if err := os.Symlink(target, filepath.Join(threadRoot, metadataFileName)); err != nil {
			t.Skipf("metadata-file symlinks unavailable: %v", err)
		}
		if _, err := catalog.Query(t.Context(), CatalogQuery{ThreadID: metadata.ThreadID}); err == nil {
			t.Fatal("exact query followed a symlinked metadata file")
		}
	})
}

func TestCatalogCorruptEntriesCannotHideHealthyThreads(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	project := catalogFixtureProject(t, filepath.Join(root, "project"))
	for index := range 2 {
		metadata := catalogFixtureMetadata(
			t,
			project,
			fmt.Sprintf("healthy-%d", index),
			time.Now().Add(time.Duration(index)),
		)
		if err := store.Save(metadata); err != nil {
			t.Fatalf("Save(healthy) error = %v", err)
		}
	}
	corruptIDs := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
	catalogWriteRawMetadata(t, store, corruptIDs[0], []byte("{not-json"))
	catalogWriteRawMetadata(t, store, corruptIDs[1], []byte(strings.Repeat("x", MaxMetadataBytes+1)))
	missingRoot, err := store.ThreadRoot(corruptIDs[2])
	if err != nil {
		t.Fatalf("ThreadRoot(missing) error = %v", err)
	}
	if err := os.MkdirAll(missingRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll(missing) error = %v", err)
	}
	invalidRoot := filepath.Join(store.Root(), "threads", "not-a-thread-id")
	if err := os.MkdirAll(invalidRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll(invalid) error = %v", err)
	}
	wantSkipped := 4
	symlink := filepath.Join(store.Root(), "threads", uuid.NewString())
	if err := os.Symlink(invalidRoot, symlink); err == nil {
		wantSkipped++
	}

	catalog, err := NewCatalog(store, CatalogOptions{SkipReportLimit: 2})
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	page, err := catalog.Query(t.Context(), CatalogQuery{All: true})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(page.Threads) != 2 {
		t.Fatalf("healthy thread count = %d, want 2", len(page.Threads))
	}
	if page.SkippedTotal != wantSkipped || len(page.Skipped) != 2 {
		t.Fatalf(
			"skipped = total %d reports %#v, want total %d reports 2",
			page.SkippedTotal,
			page.Skipped,
			wantSkipped,
		)
	}
	for _, skipped := range page.Skipped {
		if len(skipped.Entry) > skipEntryMaxBytes || len(skipped.Reason) > skipReasonMaxBytes ||
			!utf8.ValidString(skipped.Entry) || !utf8.ValidString(skipped.Reason) {
			t.Fatalf("unbounded skip diagnostic = %#v", skipped)
		}
	}
}

func TestCatalogScanAndPaginationBounds(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	project := catalogFixtureProject(t, filepath.Join(root, "project"))
	base := time.Now()
	for index := range 5 {
		catalogWriteMetadataFixture(
			t,
			store,
			catalogFixtureMetadata(t, project, fmt.Sprintf("thread-%d", index), base.Add(time.Duration(index))),
		)
	}
	catalog, err := NewCatalog(store, CatalogOptions{
		ScanLimit:       3,
		PageSize:        2,
		MaxPageSize:     2,
		SkipReportLimit: 2,
	})
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	first, err := catalog.Query(t.Context(), CatalogQuery{All: true})
	if err != nil {
		t.Fatalf("Query(first) error = %v", err)
	}
	if first.Scanned != 3 || !first.ScanTruncated || len(first.Threads) != 2 ||
		!first.HasMore || first.NextOffset != 2 {
		t.Fatalf("bounded first page = %#v", first)
	}
	second, err := catalog.Query(t.Context(), CatalogQuery{All: true, Offset: first.NextOffset})
	if err != nil {
		t.Fatalf("Query(second) error = %v", err)
	}
	if second.Scanned != 3 || !second.ScanTruncated || len(second.Threads) != 1 ||
		second.HasMore || second.NextOffset != 0 {
		t.Fatalf("bounded second page = %#v", second)
	}
}

func TestCatalogScanLimitUsesOneBoundedLookaheadAcrossBatchBoundary(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	project := catalogFixtureProject(t, filepath.Join(root, "project"))
	base := time.Now()
	for index := range catalogReadBatchSize {
		catalogWriteMetadataFixture(
			t,
			store,
			catalogFixtureMetadata(t, project, fmt.Sprintf("thread-%03d", index), base.Add(time.Duration(index))),
		)
	}
	catalog, err := NewCatalog(store, CatalogOptions{
		ScanLimit:   catalogReadBatchSize,
		MaxPageSize: catalogReadBatchSize,
	})
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	exactLimit, err := catalog.Query(t.Context(), CatalogQuery{All: true})
	if err != nil {
		t.Fatalf("Query(exact limit) error = %v", err)
	}
	if exactLimit.Scanned != catalogReadBatchSize || exactLimit.ScanTruncated {
		t.Fatalf("exact-limit page = %#v", exactLimit)
	}

	catalogWriteMetadataFixture(
		t,
		store,
		catalogFixtureMetadata(t, project, "one-lookahead", base.Add(time.Hour)),
	)
	limitPlusOne, err := catalog.Query(t.Context(), CatalogQuery{All: true})
	if err != nil {
		t.Fatalf("Query(limit plus one) error = %v", err)
	}
	if limitPlusOne.Scanned != catalogReadBatchSize || !limitPlusOne.ScanTruncated {
		t.Fatalf("limit-plus-one page = %#v", limitPlusOne)
	}
}

func TestCatalogValidatesQueriesAndCancellation(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	catalog, err := NewCatalog(store, CatalogOptions{})
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	invalid := []CatalogQuery{
		{},
		{All: true, ProjectKey: "directory:" + strings.Repeat("a", 64)},
		{ProjectKey: "invalid"},
		{All: true, Limit: DefaultCatalogMaxPageSize + 1},
		{All: true, Last: true, Offset: 1},
		{ThreadID: uuid.NewString(), All: true},
		{All: true, Search: " leading"},
		{All: true, Search: strings.Repeat("x", MaxCatalogSearchBytes+1)},
	}
	for _, query := range invalid {
		if _, err := catalog.Query(t.Context(), query); err == nil {
			t.Fatalf("Query(%#v) succeeded", query)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := catalog.Query(ctx, CatalogQuery{All: true}); err == nil {
		t.Fatal("Query() ignored canceled context")
	}
	if _, err := NewCatalog(store, CatalogOptions{ScanLimit: DefaultCatalogScanLimit + 1}); err == nil {
		t.Fatal("NewCatalog() accepted a scan limit above the hard default")
	}
	if _, err := NewCatalog(store, CatalogOptions{
		ScanLimit:       1,
		PageSize:        1,
		MaxPageSize:     1,
		SkipReportLimit: 2,
	}); err == nil {
		t.Fatal("NewCatalog() accepted skip diagnostics above the scan bound")
	}
}

func TestCatalogListsTwoThousandMetadataEntriesWithinBudget(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	project := catalogFixtureProject(t, filepath.Join(root, "project"))
	base := time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)
	threadsRoot := filepath.Join(store.Root(), "threads")
	if err := os.MkdirAll(threadsRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll(threads) error = %v", err)
	}
	for index := range 2_000 {
		catalogWritePerformanceFixture(
			t,
			threadsRoot,
			catalogFixtureMetadata(t, project, fmt.Sprintf("thread-%04d", index), base.Add(time.Duration(index))),
		)
	}
	catalog, err := NewCatalog(store, CatalogOptions{})
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	started := time.Now()
	page, err := catalog.Query(t.Context(), CatalogQuery{ProjectKey: project.ProjectKey})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if page.Scanned != 2_000 || len(page.Threads) != DefaultCatalogPageSize ||
		page.SkippedTotal != 0 || page.ScanTruncated || !page.HasMore {
		t.Fatalf("large catalog page = %#v", page)
	}
	t.Logf("listed 2,000 metadata entries in %v", elapsed)
}

func BenchmarkCatalogFirstPageTwoThousand(b *testing.B) {
	root := b.TempDir()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		b.Fatalf("NewStore() error = %v", err)
	}
	project := catalogFixtureProject(b, filepath.Join(root, "project"))
	base := time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)
	threadsRoot := filepath.Join(store.Root(), "threads")
	if err := os.MkdirAll(threadsRoot, 0o700); err != nil {
		b.Fatalf("MkdirAll(threads) error = %v", err)
	}
	for index := range 2_000 {
		catalogWritePerformanceFixture(
			b,
			threadsRoot,
			catalogFixtureMetadata(b, project, fmt.Sprintf("thread-%04d", index), base.Add(time.Duration(index))),
		)
	}
	catalog, err := NewCatalog(store, CatalogOptions{})
	if err != nil {
		b.Fatalf("NewCatalog() error = %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		page, queryErr := catalog.Query(b.Context(), CatalogQuery{ProjectKey: project.ProjectKey})
		if queryErr != nil {
			b.Fatalf("Query() error = %v", queryErr)
		}
		if page.Scanned != 2_000 || len(page.Threads) != DefaultCatalogPageSize {
			b.Fatalf("large catalog page = %#v", page)
		}
	}
}

func catalogFixtureProject(t testing.TB, root string) ProjectIdentity {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("MkdirAll(project) error = %v", err)
	}
	resolved, err := ResolveProject(t.Context(), root)
	if err != nil {
		t.Fatalf("ResolveProject() error = %v", err)
	}
	return resolved
}

func catalogFixtureMetadata(
	t testing.TB,
	project ProjectIdentity,
	title string,
	updatedAt time.Time,
) Metadata {
	t.Helper()
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(title)).String()
	metadata, err := NewMetadata(id, project, title, updatedAt.Add(-time.Hour))
	if err != nil {
		t.Fatalf("NewMetadata(%s) error = %v", title, err)
	}
	metadata.UpdatedAt = updatedAt.UTC()
	return metadata
}

func catalogWriteMetadataFixture(t *testing.T, store *Store, metadata Metadata) {
	t.Helper()
	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("Marshal(%s) error = %v", metadata.Title, err)
	}
	catalogWriteRawMetadata(t, store, metadata.ThreadID, data)
}

func catalogWritePerformanceFixture(t testing.TB, threadsRoot string, metadata Metadata) {
	t.Helper()
	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("Marshal(%s) error = %v", metadata.Title, err)
	}
	threadRoot := filepath.Join(threadsRoot, metadata.ThreadID)
	if err := os.Mkdir(threadRoot, 0o700); err != nil {
		t.Fatalf("Mkdir(%s) error = %v", metadata.ThreadID, err)
	}
	if err := os.WriteFile(filepath.Join(threadRoot, metadataFileName), data, 0o600); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", metadata.ThreadID, err)
	}
}

func catalogWriteRawMetadata(t *testing.T, store *Store, threadID string, data []byte) {
	t.Helper()
	threadRoot, err := store.ThreadRoot(threadID)
	if err != nil {
		t.Fatalf("ThreadRoot(%s) error = %v", threadID, err)
	}
	if err := os.MkdirAll(threadRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", threadID, err)
	}
	if err := os.WriteFile(filepath.Join(threadRoot, metadataFileName), data, 0o600); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", threadID, err)
	}
}

func catalogTitles(metadata []Metadata) []string {
	titles := make([]string, len(metadata))
	for index := range metadata {
		titles[index] = metadata[index].Title
	}
	return titles
}
