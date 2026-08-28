package thread

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	DefaultCatalogScanLimit       = 10_000
	DefaultCatalogPageSize        = 100
	DefaultCatalogMaxPageSize     = 500
	DefaultCatalogSkipReportLimit = 50
	MaxCatalogSearchBytes         = 256

	catalogReadBatchSize = 128
	skipEntryMaxBytes    = 128
	skipReasonMaxBytes   = 256
)

// CatalogOptions bounds catalog work and result memory.
type CatalogOptions struct {
	ScanLimit       int
	PageSize        int
	MaxPageSize     int
	SkipReportLimit int
}

func (o CatalogOptions) withDefaults() (CatalogOptions, error) {
	if o.ScanLimit == 0 {
		o.ScanLimit = DefaultCatalogScanLimit
	}
	if o.PageSize == 0 {
		o.PageSize = DefaultCatalogPageSize
	}
	if o.MaxPageSize == 0 {
		o.MaxPageSize = DefaultCatalogMaxPageSize
	}
	if o.SkipReportLimit == 0 {
		o.SkipReportLimit = DefaultCatalogSkipReportLimit
	}
	if o.ScanLimit < 1 || o.PageSize < 1 || o.MaxPageSize < 1 || o.SkipReportLimit < 1 {
		return CatalogOptions{}, fmt.Errorf("coding thread catalog: limits must be positive")
	}
	if o.ScanLimit > DefaultCatalogScanLimit || o.PageSize > DefaultCatalogPageSize ||
		o.MaxPageSize > DefaultCatalogMaxPageSize ||
		o.SkipReportLimit > DefaultCatalogSkipReportLimit {
		return CatalogOptions{}, fmt.Errorf("coding thread catalog: custom limits may only tighten defaults")
	}
	if o.PageSize > o.MaxPageSize {
		return CatalogOptions{}, fmt.Errorf("coding thread catalog: default page size exceeds maximum")
	}
	if o.MaxPageSize > o.ScanLimit {
		return CatalogOptions{}, fmt.Errorf("coding thread catalog: maximum page size exceeds scan limit")
	}
	if o.SkipReportLimit > o.ScanLimit {
		return CatalogOptions{}, fmt.Errorf("coding thread catalog: skip report limit exceeds scan limit")
	}
	return o, nil
}

// Catalog discovers bounded thread metadata without opening transcripts.
type Catalog struct {
	store   *Store
	options CatalogOptions
}

// NewCatalog creates a read-only catalog view over a metadata store.
func NewCatalog(store *Store, options CatalogOptions) (*Catalog, error) {
	if store == nil {
		return nil, fmt.Errorf("coding thread catalog: store is required")
	}
	resolved, err := options.withDefaults()
	if err != nil {
		return nil, err
	}
	return &Catalog{store: store, options: resolved}, nil
}

// CatalogQuery selects an exact thread or a bounded sorted page.
// ProjectKey is required by default; All deliberately widens the scope.
type CatalogQuery struct {
	ThreadID   string
	ProjectKey string
	All        bool
	Archived   bool
	Last       bool
	Search     string
	Offset     int
	Limit      int
}

// SkippedEntry is one bounded diagnostic for an unreadable catalog entry.
type SkippedEntry struct {
	Entry  string
	Reason string
}

// CatalogPage is a bounded catalog response suitable for a TUI picker.
type CatalogPage struct {
	Threads       []Metadata
	Skipped       []SkippedEntry
	SkippedTotal  int
	Scanned       int
	Matched       int
	ScanTruncated bool
	HasMore       bool
	NextOffset    int
}

// Query returns one exact thread or a deterministically sorted page. Exact ID
// lookup bypasses scanning and project filtering so callers can report an
// explicit project mismatch rather than hiding the thread.
func (c *Catalog) Query(ctx context.Context, query CatalogQuery) (CatalogPage, error) {
	if c == nil || c.store == nil {
		return CatalogPage{}, fmt.Errorf("coding thread catalog is nil")
	}
	if err := ctx.Err(); err != nil {
		return CatalogPage{}, err
	}
	if query.ThreadID != "" {
		if query.ProjectKey != "" || query.All || query.Archived || query.Last || query.Search != "" ||
			query.Offset != 0 ||
			query.Limit != 0 {
			return CatalogPage{}, fmt.Errorf("coding thread catalog: exact ID cannot be combined with list options")
		}
		metadata, err := c.loadExact(query.ThreadID)
		if err != nil {
			return CatalogPage{}, err
		}
		return CatalogPage{Threads: []Metadata{metadata}, Matched: 1}, nil
	}
	if !query.All && strings.TrimSpace(query.ProjectKey) == "" {
		return CatalogPage{}, fmt.Errorf(
			"coding thread catalog: project key is required unless all projects are selected",
		)
	}
	if query.All && query.ProjectKey != "" {
		return CatalogPage{}, fmt.Errorf("coding thread catalog: all-project scope cannot include a project key")
	}
	if query.ProjectKey != strings.TrimSpace(query.ProjectKey) {
		return CatalogPage{}, fmt.Errorf("coding thread catalog: project key must be trimmed")
	}
	if query.ProjectKey != "" && !validProjectKey(query.ProjectKey) {
		return CatalogPage{}, fmt.Errorf("coding thread catalog: project key is invalid")
	}
	if query.Search != strings.TrimSpace(query.Search) || !utf8.ValidString(query.Search) ||
		len(query.Search) > MaxCatalogSearchBytes {
		return CatalogPage{}, fmt.Errorf(
			"coding thread catalog: search must be trimmed valid UTF-8 within %d bytes",
			MaxCatalogSearchBytes,
		)
	}
	if query.Offset < 0 || query.Offset > c.options.ScanLimit {
		return CatalogPage{}, fmt.Errorf("coding thread catalog: offset is outside the scan bound")
	}
	if query.Last && (query.Offset != 0 || query.Limit != 0) {
		return CatalogPage{}, fmt.Errorf("coding thread catalog: last cannot be combined with pagination")
	}
	limit := query.Limit
	if query.Last {
		limit = 1
	} else if limit == 0 {
		limit = c.options.PageSize
	}
	if limit < 1 || limit > c.options.MaxPageSize {
		return CatalogPage{}, fmt.Errorf(
			"coding thread catalog: page limit must be between 1 and %d",
			c.options.MaxPageSize,
		)
	}

	retain := min(query.Offset+limit, c.options.ScanLimit)
	page, candidates, err := c.scan(ctx, query, retain)
	if err != nil {
		return CatalogPage{}, err
	}
	sort.Slice(candidates, func(left, right int) bool {
		return metadataComesBefore(candidates[left], candidates[right])
	})
	sort.Slice(page.Skipped, func(left, right int) bool {
		return skippedEntryLess(page.Skipped[left], page.Skipped[right])
	})

	start := min(query.Offset, len(candidates))
	end := min(start+limit, len(candidates))
	page.Threads = append([]Metadata(nil), candidates[start:end]...)
	page.HasMore = end < page.Matched
	if page.HasMore {
		page.NextOffset = end
	}
	return page, nil
}

func (c *Catalog) scan(
	ctx context.Context,
	query CatalogQuery,
	retain int,
) (page CatalogPage, candidates []Metadata, resultErr error) {
	threadsRoot, err := c.openThreadsRoot()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return CatalogPage{}, nil, nil
		}
		return CatalogPage{}, nil, fmt.Errorf("coding thread catalog: open threads root: %w", err)
	}
	defer func() {
		if closeErr := threadsRoot.Close(); closeErr != nil && resultErr == nil {
			page = CatalogPage{}
			candidates = nil
			resultErr = fmt.Errorf("coding thread catalog: close threads root: %w", closeErr)
		}
	}()

	page = CatalogPage{Skipped: make([]SkippedEntry, 0, c.options.SkipReportLimit)}
	retained := make(metadataOldestHeap, 0, retain)
	for {
		if err := ctx.Err(); err != nil {
			return CatalogPage{}, nil, err
		}
		remaining := c.options.ScanLimit - page.Scanned
		entries, readErr := threadsRoot.readDir(min(catalogReadBatchSize, remaining+1))
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return CatalogPage{}, nil, err
			}
			if page.Scanned >= c.options.ScanLimit {
				page.ScanTruncated = true
				candidates = append([]Metadata(nil), retained...)
				return page, candidates, nil
			}
			page.Scanned++
			if entry.Type()&os.ModeSymlink != 0 {
				page.addSkipped(
					c.options.SkipReportLimit,
					entry.Name(),
					"symbolic_link_not_allowed",
				)
				continue
			}
			if !entry.IsDir() {
				continue
			}
			threadID := entry.Name()
			if err := validateThreadID(threadID); err != nil {
				page.addSkipped(c.options.SkipReportLimit, threadID, "invalid_thread_id")
				continue
			}
			metadata, loadErr := loadCatalogMetadata(threadsRoot, threadID)
			if loadErr != nil {
				page.addSkipped(c.options.SkipReportLimit, threadID, "metadata_unreadable_or_invalid")
				continue
			}
			if metadata.Status == catalogQueryStatus(query) &&
				(query.All || metadata.Project.ProjectKey == query.ProjectKey) &&
				metadataMatchesSearch(metadata, query.Search) {
				page.Matched++
				if len(retained) < retain {
					heap.Push(&retained, metadata)
				} else if metadataComesBefore(metadata, retained[0]) {
					heap.Pop(&retained)
					heap.Push(&retained, metadata)
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			candidates = append([]Metadata(nil), retained...)
			return page, candidates, nil
		}
		if readErr != nil {
			return CatalogPage{}, nil, fmt.Errorf("coding thread catalog: read threads root: %w", readErr)
		}
	}
}

func catalogQueryStatus(query CatalogQuery) Status {
	if query.Archived {
		return StatusArchived
	}
	return StatusActive
}

func metadataMatchesSearch(metadata Metadata, search string) bool {
	search = strings.ToLower(search)
	if search == "" {
		return true
	}
	fields := []string{
		metadata.ThreadID,
		metadata.Title,
		metadata.Preview,
		metadata.Project.ProjectRoot,
		metadata.Project.InvocationCWD,
		metadata.Project.GitBranch,
	}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), search) {
			return true
		}
	}
	return false
}

func (c *Catalog) loadExact(threadID string) (Metadata, error) {
	if err := validateThreadID(threadID); err != nil {
		return Metadata{}, err
	}
	threadsRoot, err := c.openThreadsRoot()
	if err != nil {
		return Metadata{}, err
	}
	defer func() { _ = threadsRoot.Close() }()
	return loadCatalogMetadata(threadsRoot, threadID)
}

type catalogDirectoryOpener func(*catalogDirectory, string) (*catalogDirectory, error)

func (c *Catalog) openThreadsRoot() (*catalogDirectory, error) {
	return c.openThreadsRootWithOpener(openCatalogChildDirectory)
}

func (c *Catalog) openThreadsRootWithOpener(opener catalogDirectoryOpener) (*catalogDirectory, error) {
	storeRoot, err := openCatalogRoot(c.store.Root())
	if err != nil {
		return nil, fmt.Errorf("coding thread catalog: open store root: %w", err)
	}
	defer func() { _ = storeRoot.Close() }()
	threadsRoot, err := opener(storeRoot, "threads")
	if err != nil {
		return nil, fmt.Errorf("coding thread catalog: open threads root: %w", err)
	}
	return threadsRoot, nil
}

type catalogMetadataOpener func(*catalogDirectory) (*os.File, error)

func loadCatalogMetadata(threadsRoot *catalogDirectory, threadID string) (Metadata, error) {
	return loadCatalogMetadataWithOpeners(
		threadsRoot,
		threadID,
		openCatalogChildDirectory,
		openCatalogMetadataFile,
	)
}

func loadCatalogMetadataWithOpener(
	threadsRoot *catalogDirectory,
	threadID string,
	opener catalogMetadataOpener,
) (Metadata, error) {
	return loadCatalogMetadataWithOpeners(threadsRoot, threadID, openCatalogChildDirectory, opener)
}

func loadCatalogMetadataWithDirectoryOpener(
	threadsRoot *catalogDirectory,
	threadID string,
	opener catalogDirectoryOpener,
) (Metadata, error) {
	return loadCatalogMetadataWithOpeners(threadsRoot, threadID, opener, openCatalogMetadataFile)
}

func loadCatalogMetadataWithOpeners(
	threadsRoot *catalogDirectory,
	threadID string,
	directoryOpener catalogDirectoryOpener,
	metadataOpener catalogMetadataOpener,
) (Metadata, error) {
	threadRoot, err := directoryOpener(threadsRoot, threadID)
	if err != nil {
		return Metadata{}, fmt.Errorf("coding thread catalog: open thread %q: %w", threadID, err)
	}
	defer func() { _ = threadRoot.Close() }()
	return loadCatalogMetadataFromDirectory(threadRoot, threadID, metadataOpener)
}

func loadCatalogMetadataFromDirectory(
	threadRoot *catalogDirectory,
	threadID string,
	metadataOpener catalogMetadataOpener,
) (Metadata, error) {
	file, err := metadataOpener(threadRoot)
	if err != nil {
		return Metadata{}, fmt.Errorf("coding thread catalog: open thread %q metadata: %w", threadID, err)
	}
	openedInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return Metadata{}, fmt.Errorf("coding thread catalog: inspect open thread %q metadata: %w", threadID, err)
	}
	if err := validateCatalogMetadataFile(file, openedInfo); err != nil {
		_ = file.Close()
		return Metadata{}, fmt.Errorf("coding thread catalog: open thread %q metadata: %w", threadID, err)
	}
	return loadMetadataFile(threadID, file)
}

type metadataOldestHeap []Metadata

func (h *metadataOldestHeap) Len() int { return len(*h) }

func (h *metadataOldestHeap) Less(left, right int) bool {
	return metadataComesBefore((*h)[right], (*h)[left])
}

func (h *metadataOldestHeap) Swap(left, right int) {
	(*h)[left], (*h)[right] = (*h)[right], (*h)[left]
}

func (h *metadataOldestHeap) Push(value any) {
	*h = append(*h, value.(Metadata))
}

func (h *metadataOldestHeap) Pop() any {
	old := *h
	last := len(old) - 1
	value := old[last]
	*h = old[:last]
	return value
}

func metadataComesBefore(left, right Metadata) bool {
	if !left.UpdatedAt.Equal(right.UpdatedAt) {
		return left.UpdatedAt.After(right.UpdatedAt)
	}
	if !left.CreatedAt.Equal(right.CreatedAt) {
		return left.CreatedAt.After(right.CreatedAt)
	}
	return left.ThreadID < right.ThreadID
}

func validProjectKey(key string) bool {
	kind, digest, ok := strings.Cut(key, ":")
	if !ok || len(digest) != 64 || strings.ToLower(digest) != digest || !isHex(digest) {
		return false
	}
	return ProjectKind(kind) == ProjectKindDirectory || ProjectKind(kind) == ProjectKindGitWorktree
}

func (p *CatalogPage) addSkipped(limit int, entry, reason string) {
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

func skippedEntryLess(left, right SkippedEntry) bool {
	if left.Entry != right.Entry {
		return left.Entry < right.Entry
	}
	return left.Reason < right.Reason
}

func boundedCatalogText(value string, maxBytes int) string {
	if !utf8.ValidString(value) {
		value = strings.ToValidUTF8(value, "�")
	}
	return truncateUTF8(value, maxBytes)
}
