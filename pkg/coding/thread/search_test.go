package thread

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func TestHistoricalSearchScopesMetadataAndTranscriptMatches(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	current := catalogFixtureProject(t, filepath.Join(root, "current"))
	foreign := catalogFixtureProject(t, filepath.Join(root, "foreign"))
	base := time.Date(2026, time.August, 29, 10, 0, 0, 0, time.UTC)
	titleMatch := catalogFixtureMetadata(t, current, "Needle in title", base.Add(3*time.Minute))
	transcriptMatch := catalogFixtureMetadata(t, current, "Older work", base.Add(2*time.Minute))
	foreignMatch := catalogFixtureMetadata(t, foreign, "Foreign work", base.Add(time.Minute))
	for _, metadata := range []Metadata{titleMatch, transcriptMatch, foreignMatch} {
		if err := store.Save(metadata); err != nil {
			t.Fatal(err)
		}
	}
	matchedAt := base.Add(-time.Hour)
	writeForkTestHistory(t, store, transcriptMatch, []providers.Message{
		{Role: "user", Content: "find the historical needle", CreatedAt: &matchedAt, RootTurnStart: true},
	})
	writeForkTestHistory(t, store, foreignMatch, []providers.Message{
		{Role: "user", Content: "foreign needle must stay private", CreatedAt: &matchedAt, RootTurnStart: true},
	})
	searcher, err := NewHistoricalSearcher(store, HistoricalSearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	page, err := searcher.Query(t.Context(), HistoricalSearchQuery{
		ProjectKey: current.ProjectKey, Text: "NEEDLE", Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := historicalSearchIDs(page.Matches); !reflect.DeepEqual(got, []string{
		titleMatch.ThreadID,
		transcriptMatch.ThreadID,
	}) {
		t.Fatalf("current-project matches = %v", got)
	}
	if page.Matches[0].Kind != HistoricalMatchTitle ||
		page.Matches[1].Kind != HistoricalMatchTranscript ||
		!page.Matches[1].MatchedAt.Equal(matchedAt) || page.Matches[1].Message != 1 ||
		!strings.Contains(page.Matches[1].Snippet, "historical needle") {
		t.Fatalf("search matches = %+v", page.Matches)
	}
	all, err := searcher.Query(t.Context(), HistoricalSearchQuery{All: true, Text: "needle", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if got := historicalSearchIDs(all.Matches); !reflect.DeepEqual(got, []string{
		titleMatch.ThreadID,
		transcriptMatch.ThreadID,
		foreignMatch.ThreadID,
	}) {
		t.Fatalf("explicit all-project matches = %v", got)
	}
}

func TestHistoricalSearchSeparatesArchivedThreads(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	project := catalogFixtureProject(t, filepath.Join(root, "project"))
	metadata := catalogFixtureMetadata(t, project, "Archived needle", time.Now().UTC())
	metadata.Status = StatusArchived
	if err := store.Save(metadata); err != nil {
		t.Fatal(err)
	}
	searcher, err := NewHistoricalSearcher(store, HistoricalSearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	active, err := searcher.Query(t.Context(), HistoricalSearchQuery{
		ProjectKey: project.ProjectKey, Text: "needle", Limit: 20,
	})
	if err != nil || len(active.Matches) != 0 {
		t.Fatalf("active search = %+v, %v", active, err)
	}
	archived, err := searcher.Query(t.Context(), HistoricalSearchQuery{
		ProjectKey: project.ProjectKey, Archived: true, Text: "needle", Limit: 20,
	})
	if err != nil || len(archived.Matches) != 1 || archived.Matches[0].Metadata.Status != StatusArchived {
		t.Fatalf("archived search = %+v, %v", archived, err)
	}
}

func TestHistoricalSearchDoesNotOpenForeignProjectTranscriptsByDefault(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	current := catalogFixtureProject(t, filepath.Join(root, "current"))
	foreign := catalogFixtureProject(t, filepath.Join(root, "foreign"))
	metadata := catalogFixtureMetadata(t, foreign, "Foreign work", time.Now().UTC())
	if err := store.Save(metadata); err != nil {
		t.Fatal(err)
	}
	writeForkTestHistory(t, store, metadata, []providers.Message{{
		Role: "user", Content: "ordinary content", RootTurnStart: true,
	}})
	threadRoot, err := store.ThreadRoot(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	jsonlPath := filepath.Join(threadRoot, "sessions", "coding_"+metadata.ThreadID+".jsonl")
	if err := os.Remove(jsonlPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "outside.jsonl"), jsonlPath); err != nil {
		t.Fatal(err)
	}
	searcher, err := NewHistoricalSearcher(store, HistoricalSearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	page, err := searcher.Query(t.Context(), HistoricalSearchQuery{
		ProjectKey: current.ProjectKey, Text: "needle", Limit: 20,
	})
	if err != nil || page.SkippedTotal != 0 || page.ContentThreadsScanned != 0 {
		t.Fatalf("project-private search touched foreign transcript: %+v, %v", page, err)
	}
}

func TestHistoricalSearchPaginationAndContentBounds(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	project := catalogFixtureProject(t, filepath.Join(root, "project"))
	base := time.Date(2026, time.August, 29, 11, 0, 0, 0, time.UTC)
	newest := catalogFixtureMetadata(t, project, "Newest", base.Add(2*time.Minute))
	older := catalogFixtureMetadata(t, project, "Older", base.Add(time.Minute))
	for _, metadata := range []Metadata{newest, older} {
		if err := store.Save(metadata); err != nil {
			t.Fatal(err)
		}
	}
	writeForkTestHistory(t, store, newest, []providers.Message{{
		Role: "user", Content: "not a match", RootTurnStart: true,
	}})
	writeForkTestHistory(t, store, older, []providers.Message{{
		Role: "user", Content: "bounded needle", RootTurnStart: true,
	}})
	searcher, err := NewHistoricalSearcher(store, HistoricalSearchOptions{TranscriptThreadLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	page, err := searcher.Query(t.Context(), HistoricalSearchQuery{
		ProjectKey: project.ProjectKey, Text: "needle", Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Matches) != 0 || page.ContentThreadsScanned != 1 || !page.ContentScanTruncated {
		t.Fatalf("bounded content page = %+v", page)
	}

	metadataSearcher, err := NewHistoricalSearcher(store, HistoricalSearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := metadataSearcher.Query(t.Context(), HistoricalSearchQuery{
		ProjectKey: project.ProjectKey, Text: "e", Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Matches) != 1 || first.Matches[0].Metadata.ThreadID != newest.ThreadID ||
		!first.HasMore || first.NextOffset != 1 || first.Matched != 2 {
		t.Fatalf("first page = %+v", first)
	}
	second, err := metadataSearcher.Query(t.Context(), HistoricalSearchQuery{
		ProjectKey: project.ProjectKey, Text: "e", Offset: 1, Limit: 1,
	})
	if err != nil || len(second.Matches) != 1 || second.Matches[0].Metadata.ThreadID != older.ThreadID {
		t.Fatalf("second page = %+v, %v", second, err)
	}
}

func TestHistoricalSearchDoesNotRecoverDirtyHistory(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	project := catalogFixtureProject(t, filepath.Join(root, "project"))
	metadata := catalogFixtureMetadata(t, project, "Clean title", time.Now().UTC())
	if err := store.Save(metadata); err != nil {
		t.Fatal(err)
	}
	writeForkTestHistory(t, store, metadata, []providers.Message{{
		Role: "user", Content: "private needle", RootTurnStart: true,
	}})
	threadRoot, err := store.ThreadRoot(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	metaPath := filepath.Join(threadRoot, "sessions", "coding_"+metadata.ThreadID+".meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	sessionMeta, err := memory.DecodeSessionMeta(data)
	if err != nil {
		t.Fatal(err)
	}
	sessionMeta.HistoryDirty = true
	dirtyData, err := json.Marshal(sessionMeta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metaPath, dirtyData, 0o600); err != nil {
		t.Fatal(err)
	}
	searcher, err := NewHistoricalSearcher(store, HistoricalSearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	page, err := searcher.Query(t.Context(), HistoricalSearchQuery{
		ProjectKey: project.ProjectKey, Text: "needle", Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	after, readErr := os.ReadFile(metaPath)
	if len(page.Matches) != 0 || page.SkippedTotal != 1 || readErr != nil || !bytes.Equal(after, dirtyData) {
		t.Fatalf(
			"dirty search = matches %+v skipped %d unchanged %t readErr %v",
			page.Matches,
			page.SkippedTotal,
			bytes.Equal(after, dirtyData),
			readErr,
		)
	}
}

func TestHistoricalSearchRejectsLinkedTranscriptAndCancellation(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	project := catalogFixtureProject(t, filepath.Join(root, "project"))
	metadata := catalogFixtureMetadata(t, project, "Clean title", time.Now().UTC())
	if err := store.Save(metadata); err != nil {
		t.Fatal(err)
	}
	writeForkTestHistory(t, store, metadata, []providers.Message{{
		Role: "user", Content: "ordinary content", RootTurnStart: true,
	}})
	threadRoot, err := store.ThreadRoot(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	jsonlPath := filepath.Join(threadRoot, "sessions", "coding_"+metadata.ThreadID+".jsonl")
	outside := filepath.Join(root, "outside.jsonl")
	if err := os.WriteFile(outside, []byte(`{"role":"user","content":"linked needle"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(jsonlPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, jsonlPath); err != nil {
		t.Fatal(err)
	}
	searcher, err := NewHistoricalSearcher(store, HistoricalSearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	page, err := searcher.Query(t.Context(), HistoricalSearchQuery{
		ProjectKey: project.ProjectKey, Text: "needle", Limit: 20,
	})
	if err != nil || len(page.Matches) != 0 || page.SkippedTotal != 1 {
		t.Fatalf("linked transcript search = %+v, %v", page, err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := searcher.Query(canceled, HistoricalSearchQuery{
		ProjectKey: project.ProjectKey, Text: "needle", Limit: 20,
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled search error = %v", err)
	}
}

func historicalSearchIDs(matches []HistoricalSearchMatch) []string {
	ids := make([]string, len(matches))
	for index := range matches {
		ids[index] = matches[index].Metadata.ThreadID
	}
	return ids
}
