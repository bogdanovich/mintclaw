package thread

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/providers/messageutil"
)

const (
	DefaultHistoricalSearchThreadLimit  = 200
	DefaultHistoricalSearchTotalBytes   = 64 << 20
	DefaultHistoricalSearchSnippetBytes = 512
	MaxHistoricalSearchMetadataBytes    = 1 << 20
)

// HistoricalSearchOptions bounds catalog, transcript, and result work. Custom
// values may only tighten the defaults.
type HistoricalSearchOptions struct {
	Catalog               CatalogOptions
	TranscriptThreadLimit int
	TotalTranscriptBytes  int64
	SnippetBytes          int
}

func (o HistoricalSearchOptions) withDefaults() (HistoricalSearchOptions, error) {
	resolvedCatalog, err := o.Catalog.withDefaults()
	if err != nil {
		return HistoricalSearchOptions{}, err
	}
	o.Catalog = resolvedCatalog
	if o.TranscriptThreadLimit == 0 {
		o.TranscriptThreadLimit = DefaultHistoricalSearchThreadLimit
	}
	if o.TotalTranscriptBytes == 0 {
		o.TotalTranscriptBytes = DefaultHistoricalSearchTotalBytes
	}
	if o.SnippetBytes == 0 {
		o.SnippetBytes = DefaultHistoricalSearchSnippetBytes
	}
	if o.TranscriptThreadLimit < 1 || o.TranscriptThreadLimit > DefaultHistoricalSearchThreadLimit {
		return HistoricalSearchOptions{}, fmt.Errorf(
			"coding thread search: transcript thread limit must be within 1..%d",
			DefaultHistoricalSearchThreadLimit,
		)
	}
	if o.TotalTranscriptBytes < 1 || o.TotalTranscriptBytes > DefaultHistoricalSearchTotalBytes {
		return HistoricalSearchOptions{}, fmt.Errorf(
			"coding thread search: transcript byte limit must be within 1..%d",
			DefaultHistoricalSearchTotalBytes,
		)
	}
	if o.SnippetBytes < 1 || o.SnippetBytes > DefaultHistoricalSearchSnippetBytes {
		return HistoricalSearchOptions{}, fmt.Errorf(
			"coding thread search: snippet limit must be within 1..%d",
			DefaultHistoricalSearchSnippetBytes,
		)
	}
	return o, nil
}

// HistoricalMatchKind identifies the source that matched the query.
type HistoricalMatchKind string

const (
	HistoricalMatchThreadID   HistoricalMatchKind = "thread_id"
	HistoricalMatchTitle      HistoricalMatchKind = "title"
	HistoricalMatchPreview    HistoricalMatchKind = "preview"
	HistoricalMatchProject    HistoricalMatchKind = "project"
	HistoricalMatchBranch     HistoricalMatchKind = "branch"
	HistoricalMatchTranscript HistoricalMatchKind = "transcript"
)

// HistoricalSearchQuery selects one bounded result page. All is the only way
// to widen discovery beyond ProjectKey.
type HistoricalSearchQuery struct {
	ProjectKey string
	All        bool
	Archived   bool
	Text       string
	Offset     int
	Limit      int
}

// HistoricalSearchMatch is a bounded presentation-safe search result.
type HistoricalSearchMatch struct {
	Metadata  Metadata            `json:"metadata"`
	Kind      HistoricalMatchKind `json:"kind"`
	Snippet   string              `json:"snippet"`
	MatchedAt time.Time           `json:"matched_at"`
	Message   int                 `json:"message,omitempty"`
}

// HistoricalSearchPage reports bounded catalog and transcript coverage.
type HistoricalSearchPage struct {
	Matches               []HistoricalSearchMatch `json:"matches"`
	Skipped               []SkippedEntry          `json:"skipped,omitempty"`
	SkippedTotal          int                     `json:"skipped_total,omitempty"`
	Scanned               int                     `json:"scanned"`
	Matched               int                     `json:"matched"`
	ScanTruncated         bool                    `json:"scan_truncated"`
	ContentThreadsScanned int                     `json:"content_threads_scanned"`
	ContentBytesScanned   int64                   `json:"content_bytes_scanned"`
	ContentScanTruncated  bool                    `json:"content_scan_truncated"`
	HasMore               bool                    `json:"has_more"`
	NextOffset            int                     `json:"next_offset,omitempty"`
}

// HistoricalSearcher searches metadata and stable canonical transcript
// snapshots without acquiring a writer lease or invoking history recovery.
type HistoricalSearcher struct {
	catalog *Catalog
	options HistoricalSearchOptions
}

func NewHistoricalSearcher(store *Store, options HistoricalSearchOptions) (*HistoricalSearcher, error) {
	resolved, err := options.withDefaults()
	if err != nil {
		return nil, err
	}
	catalog, err := NewCatalog(store, resolved.Catalog)
	if err != nil {
		return nil, err
	}
	return &HistoricalSearcher{catalog: catalog, options: resolved}, nil
}

func (s *HistoricalSearcher) Query(
	ctx context.Context,
	query HistoricalSearchQuery,
) (HistoricalSearchPage, error) {
	if s == nil || s.catalog == nil {
		return HistoricalSearchPage{}, fmt.Errorf("coding thread historical search is nil")
	}
	if ctx == nil {
		return HistoricalSearchPage{}, fmt.Errorf("coding thread search: context is required")
	}
	if err := ctx.Err(); err != nil {
		return HistoricalSearchPage{}, err
	}
	if query.Limit == 0 {
		query.Limit = s.catalog.options.PageSize
	}
	if err := s.validateQuery(query); err != nil {
		return HistoricalSearchPage{}, err
	}
	catalogQuery := CatalogQuery{ProjectKey: query.ProjectKey, All: query.All, Archived: query.Archived}
	catalogPage, candidates, err := s.catalog.scan(ctx, catalogQuery, s.catalog.options.ScanLimit)
	if err != nil {
		return HistoricalSearchPage{}, err
	}
	sort.Slice(candidates, func(left, right int) bool {
		return metadataComesBefore(candidates[left], candidates[right])
	})
	page := HistoricalSearchPage{
		Skipped:       append([]SkippedEntry(nil), catalogPage.Skipped...),
		SkippedTotal:  catalogPage.SkippedTotal,
		Scanned:       catalogPage.Scanned,
		ScanTruncated: catalogPage.ScanTruncated,
	}
	matches := make([]HistoricalSearchMatch, 0, min(len(candidates), query.Offset+query.Limit))
	remainingBytes := s.options.TotalTranscriptBytes
	for _, metadata := range candidates {
		if err := ctx.Err(); err != nil {
			return HistoricalSearchPage{}, err
		}
		if match, ok := metadataHistoricalMatch(metadata, query.Text, s.options.SnippetBytes); ok {
			matches = append(matches, match)
			continue
		}
		if page.ContentThreadsScanned >= s.options.TranscriptThreadLimit || remainingBytes <= 0 {
			page.ContentScanTruncated = true
			continue
		}
		page.ContentThreadsScanned++
		match, bytesRead, truncated, searchErr := s.searchTranscript(ctx, metadata, query.Text, remainingBytes)
		remainingBytes -= bytesRead
		page.ContentBytesScanned += bytesRead
		if truncated {
			page.ContentScanTruncated = true
		}
		if searchErr != nil {
			if !errors.Is(searchErr, fs.ErrNotExist) {
				page.addSkipped(
					s.catalog.options.SkipReportLimit,
					metadata.ThreadID,
					"transcript_unreadable_or_unstable",
				)
			}
			continue
		}
		if match != nil {
			matches = append(matches, *match)
		}
	}
	page.Matched = len(matches)
	start := min(query.Offset, len(matches))
	end := min(start+query.Limit, len(matches))
	page.Matches = append([]HistoricalSearchMatch(nil), matches[start:end]...)
	page.HasMore = end < page.Matched
	if page.HasMore {
		page.NextOffset = end
	}
	sort.Slice(page.Skipped, func(left, right int) bool {
		return skippedEntryLess(page.Skipped[left], page.Skipped[right])
	})
	return page, nil
}

func (s *HistoricalSearcher) validateQuery(query HistoricalSearchQuery) error {
	if query.Text != strings.TrimSpace(query.Text) || query.Text == "" || !utf8.ValidString(query.Text) ||
		len(query.Text) > MaxCatalogSearchBytes {
		return fmt.Errorf(
			"coding thread search: query must be non-empty trimmed valid UTF-8 within %d bytes",
			MaxCatalogSearchBytes,
		)
	}
	if !query.All && strings.TrimSpace(query.ProjectKey) == "" {
		return fmt.Errorf("coding thread search: project key is required unless all projects are selected")
	}
	if query.All && query.ProjectKey != "" {
		return fmt.Errorf("coding thread search: all-project scope cannot include a project key")
	}
	if query.ProjectKey != strings.TrimSpace(query.ProjectKey) ||
		query.ProjectKey != "" && !validProjectKey(query.ProjectKey) {
		return fmt.Errorf("coding thread search: project key is invalid")
	}
	if query.Offset < 0 || query.Offset > s.catalog.options.ScanLimit {
		return fmt.Errorf("coding thread search: offset is outside the scan bound")
	}
	if query.Limit < 1 || query.Limit > s.catalog.options.MaxPageSize {
		return fmt.Errorf(
			"coding thread search: page limit must be within 1..%d",
			s.catalog.options.MaxPageSize,
		)
	}
	return nil
}

func (p *HistoricalSearchPage) addSkipped(limit int, entry, reason string) {
	p.SkippedTotal++
	candidate := SkippedEntry{
		Entry:  boundedCatalogText(entry, skipEntryMaxBytes),
		Reason: boundedCatalogText(reason, skipReasonMaxBytes),
	}
	if len(p.Skipped) < limit {
		p.Skipped = append(p.Skipped, candidate)
		return
	}
	largest := 0
	for index := 1; index < len(p.Skipped); index++ {
		if skippedEntryLess(p.Skipped[largest], p.Skipped[index]) {
			largest = index
		}
	}
	if skippedEntryLess(candidate, p.Skipped[largest]) {
		p.Skipped[largest] = candidate
	}
}

func (s *HistoricalSearcher) searchTranscript(
	ctx context.Context,
	metadata Metadata,
	query string,
	remainingBytes int64,
) (*HistoricalSearchMatch, int64, bool, error) {
	storeRoot, err := openCatalogRoot(s.catalog.store.Root())
	if err != nil {
		return nil, 0, false, err
	}
	defer func() { _ = storeRoot.Close() }()
	threadsRoot, err := openCatalogChildDirectory(storeRoot, "threads")
	if err != nil {
		return nil, 0, false, err
	}
	defer func() { _ = threadsRoot.Close() }()
	threadRoot, err := openCatalogChildDirectory(threadsRoot, metadata.ThreadID)
	if err != nil {
		return nil, 0, false, err
	}
	defer func() { _ = threadRoot.Close() }()
	loaded, err := loadCatalogMetadataFromDirectory(threadRoot, metadata.ThreadID, openCatalogMetadataFile)
	if err != nil {
		return nil, 0, false, err
	}
	if loaded.Project.ProjectKey != metadata.Project.ProjectKey || loaded.SessionKey != metadata.SessionKey ||
		loaded.Status != metadata.Status {
		return nil, 0, false, fmt.Errorf("coding thread search: thread identity changed during search")
	}
	metadata = loaded
	sessionsRoot, err := openCatalogChildDirectory(threadRoot, "sessions")
	if err != nil {
		return nil, 0, false, err
	}
	defer func() { _ = sessionsRoot.Close() }()
	stem := "coding_" + metadata.ThreadID
	metaFile, err := openCatalogFile(sessionsRoot, stem+".meta.json")
	if err != nil {
		return nil, 0, false, err
	}
	defer func() { _ = metaFile.Close() }()
	jsonlFile, err := openCatalogFile(sessionsRoot, stem+".jsonl")
	if err != nil {
		return nil, 0, false, err
	}
	defer func() { _ = jsonlFile.Close() }()
	metaInfo, err := metaFile.Stat()
	if err != nil {
		return nil, 0, false, err
	}
	jsonlInfo, err := jsonlFile.Stat()
	if err != nil {
		return nil, 0, false, err
	}
	needed := metaInfo.Size() + jsonlInfo.Size()
	if metaInfo.Size() > MaxHistoricalSearchMetadataBytes || jsonlInfo.Size() > MaxForkTranscriptBytes ||
		needed > remainingBytes {
		return nil, 0, true, nil
	}
	metaData, _, err := readPinnedSearchFile(ctx, metaFile, metaInfo.Size())
	if err != nil {
		return nil, needed, false, err
	}
	jsonlData, _, err := readPinnedSearchFile(ctx, jsonlFile, jsonlInfo.Size())
	if err != nil {
		return nil, needed, false, err
	}
	bytesRead := int64(len(metaData) + len(jsonlData))
	if bytesRead > remainingBytes {
		return nil, bytesRead, true, nil
	}
	sessionMeta, err := memory.DecodeSessionMeta(metaData)
	if err != nil {
		return nil, bytesRead, false, err
	}
	if sessionMeta.Key != metadata.SessionKey || sessionMeta.HistoryDirty {
		return nil, bytesRead, false, fmt.Errorf(
			"coding thread search: transcript metadata is not a clean matching session",
		)
	}
	visible := sessionMeta.Count - sessionMeta.Skip
	if sessionMeta.Count < 0 || sessionMeta.Skip < 0 || visible < 0 || visible > MaxForkMessages {
		return nil, bytesRead, true, nil
	}
	match, rawCount, visibleCount, err := searchHistoricalJSONL(
		ctx,
		jsonlData,
		sessionMeta.Skip,
		query,
		metadata,
		s.options.SnippetBytes,
	)
	if err != nil {
		return nil, bytesRead, false, err
	}
	if rawCount != sessionMeta.Count || visibleCount != visible {
		return nil, bytesRead, false, fmt.Errorf("coding thread search: transcript does not match metadata")
	}
	return match, bytesRead, false, nil
}

func readPinnedSearchFile(ctx context.Context, file *os.File, expectedSize int64) ([]byte, os.FileInfo, error) {
	before, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if validationErr := validateCatalogMetadataFile(file, before); validationErr != nil {
		return nil, nil, validationErr
	}
	if before.Size() != expectedSize {
		return nil, nil, fmt.Errorf("coding thread search: pinned file size changed before reading")
	}
	data, err := io.ReadAll(io.LimitReader(file, expectedSize+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(data)) != expectedSize {
		return nil, nil, fmt.Errorf("coding thread search: pinned file size changed while reading")
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, nil, contextErr
	}
	after, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if before.Size() != after.Size() || before.ModTime() != after.ModTime() {
		return nil, nil, fmt.Errorf("coding thread search: pinned file changed while reading")
	}
	return data, before, nil
}

func searchHistoricalJSONL(
	ctx context.Context,
	data []byte,
	skip int,
	query string,
	metadata Metadata,
	snippetBytes int,
) (*HistoricalSearchMatch, int, int, error) {
	var match *HistoricalSearchMatch
	rawCount := 0
	visibleCount := 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), memory.MaxJSONLRecordBytes)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, 0, 0, err
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		rawCount++
		if rawCount <= skip {
			continue
		}
		var message providers.Message
		if err := json.Unmarshal(line, &message); err != nil {
			return nil, 0, 0, fmt.Errorf("coding thread search: decode JSONL line %d: %w", rawCount, err)
		}
		if messageutil.IsTransientAssistantThoughtMessage(message) {
			continue
		}
		visibleCount++
		if !containsHistoricalText(message.Content, query) {
			continue
		}
		matchedAt := metadata.UpdatedAt
		if message.CreatedAt != nil {
			matchedAt = message.CreatedAt.UTC()
		}
		match = &HistoricalSearchMatch{
			Metadata: metadata, Kind: HistoricalMatchTranscript,
			Snippet:   historicalSnippet(message.Role+": "+message.Content, query, snippetBytes),
			MatchedAt: matchedAt, Message: visibleCount,
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, 0, fmt.Errorf("coding thread search: scan JSONL: %w", err)
	}
	return match, rawCount, visibleCount, nil
}

func metadataHistoricalMatch(metadata Metadata, query string, snippetBytes int) (HistoricalSearchMatch, bool) {
	fields := []struct {
		kind  HistoricalMatchKind
		value string
	}{
		{HistoricalMatchThreadID, metadata.ThreadID},
		{HistoricalMatchTitle, metadata.Title},
		{HistoricalMatchPreview, metadata.Preview},
		{HistoricalMatchProject, metadata.Project.ProjectRoot},
		{HistoricalMatchProject, metadata.Project.InvocationCWD},
		{HistoricalMatchBranch, metadata.Project.GitBranch},
	}
	for _, field := range fields {
		if containsHistoricalText(field.value, query) {
			return HistoricalSearchMatch{
				Metadata: metadata, Kind: field.kind,
				Snippet: historicalSnippet(field.value, query, snippetBytes), MatchedAt: metadata.UpdatedAt,
			}, true
		}
	}
	return HistoricalSearchMatch{}, false
}

func containsHistoricalText(value, query string) bool {
	return strings.Contains(strings.ToLower(value), strings.ToLower(query))
}

func historicalSnippet(value, query string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= limit {
		return value
	}
	index := strings.Index(strings.ToLower(value), strings.ToLower(query))
	if index < 0 || index > len(value) {
		return truncateUTF8(value, limit)
	}
	start := max(0, index-limit/3)
	end := min(len(value), start+limit)
	for start < len(value) && !utf8.RuneStart(value[start]) {
		start++
	}
	for end > start && end < len(value) && !utf8.RuneStart(value[end]) {
		end--
	}
	snippet := value[start:end]
	if start > 0 {
		snippet = "…" + snippet
	}
	if end < len(value) {
		snippet += "…"
	}
	return snippet
}
