package fstools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/tools/loopguard"
)

const (
	defaultSearchFilesLimit       = 100
	maxSearchFilesLimit           = 500
	maxSearchFilesContext         = 10
	defaultSearchFilesMaxFileSize = 1024 * 1024
	maxSearchFilesInputBytes      = 64 * 1024 * 1024
	maxSearchFilesResultBytes     = 16000
	searchFilesTruncationReserve  = 1024
)

// SearchFilesTool searches workspace files without requiring shell grep/rg.
type SearchFilesTool struct {
	fs          fileSystem
	maxFileSize int
	limits      searchSourceLimits
}

type searchSourceLimits struct {
	files   int
	bytes   int64
	entries int
}

func NewSearchFilesTool(
	workspace string,
	restrict bool,
	maxFileSize int,
	allowPaths ...[]*regexp.Regexp,
) *SearchFilesTool {
	var patterns []*regexp.Regexp
	if len(allowPaths) > 0 {
		patterns = allowPaths[0]
	}
	if maxFileSize <= 0 {
		maxFileSize = defaultSearchFilesMaxFileSize
	}
	if maxFileSize > maxSearchFilesInputBytes {
		maxFileSize = maxSearchFilesInputBytes
	}
	return &SearchFilesTool{
		fs:          buildFs(workspace, restrict, patterns),
		maxFileSize: maxFileSize,
	}
}

// NewBoundedSearchFilesTool constructs a search whose traversal and source
// reads stop at aggregate per-call limits. It is intended for constrained
// callers such as the native coding reviewer; the regular constructor keeps
// its existing behavior.
func NewBoundedSearchFilesTool(
	workspace string,
	restrict bool,
	maxFileSize int,
	maxFiles int,
	maxBytes int64,
	maxEntries int,
) *SearchFilesTool {
	tool := NewSearchFilesTool(workspace, restrict, maxFileSize)
	tool.limits = searchSourceLimits{
		files:   max(1, maxFiles),
		bytes:   max(1, maxBytes),
		entries: max(1, maxEntries),
	}
	return tool
}

func (t *SearchFilesTool) Name() string {
	return "search_files"
}

func (t *SearchFilesTool) ToolLoopSemantics() loopguard.Semantics {
	return loopguard.SemanticsReadOnlyIdempotent
}

func (t *SearchFilesTool) Description() string {
	return "Search file contents or find files by name within the configured workspace. Respects .gitignore by default; set include_ignored for explicit ignored-file searches. Use this instead of shell grep/rg/find/ls for routine file discovery."
}

func (t *SearchFilesTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Regex pattern for content search, or glob/name pattern for files search.",
			},
			"target": map[string]any{
				"type":        "string",
				"enum":        []string{"content", "files"},
				"description": "Search file contents or file paths.",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Directory or file to search. Defaults to current workspace.",
			},
			"file_glob": map[string]any{
				"type":        "string",
				"description": "Optional glob to restrict content search to matching file names, for example *.go.",
			},
			"output_mode": map[string]any{
				"type":        "string",
				"enum":        []string{"content", "files_only", "count"},
				"description": "For content search: matching lines, file paths only, or match counts.",
			},
			"context": map[string]any{
				"type":        "integer",
				"description": "Number of context lines before and after each content match. Default 0, max 10.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum number of returned matches/files. Default 100, max 500.",
			},
			"include_ignored": map[string]any{
				"type":        "boolean",
				"description": "Include files ignored by .gitignore and default noisy directories. Default false. Use only when explicitly inspecting ignored env/config/runtime files.",
			},
		},
		"required": []string{"pattern"},
	}
}

func (t *SearchFilesTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	opts, err := parseSearchFilesOptions(args)
	if err != nil {
		return ErrorResult(err.Error())
	}

	switch opts.target {
	case "files":
		return t.searchFileNames(ctx, opts)
	default:
		return t.searchContent(ctx, opts)
	}
}

type searchFilesOptions struct {
	pattern        string
	target         string
	path           string
	fileGlob       string
	outputMode     string
	context        int
	limit          int
	includeIgnored bool
}

type contentMatch struct {
	path       string
	lineNumber int
	line       string
	before     []numberedLine
	after      []numberedLine
}

type numberedLine struct {
	number int
	text   string
}

type searchTruncationInfo struct {
	truncated          bool
	reasons            map[string]bool
	returned           int
	limit              int
	omitted            int
	omittedKnown       bool
	omittedUnknown     bool
	countLimitOmitted  int
	countLimitSet      bool
	countLimitUnknown  bool
	renderedOmitted    int
	renderedOmittedSet bool
	skippedIgnored     int
	skippedMaxFileSize int
}

type searchWalkStats struct {
	ignoredSkipped   int
	maxFileSkipped   int
	binarySkipped    int
	readSkipped      int
	filesVisited     int
	bytesRead        int64
	entriesVisited   int
	sourceLimit      string
	limits           searchSourceLimits
	ignoredCandidate func(path string, entry os.DirEntry) bool
}

func parseSearchFilesOptions(args map[string]any) (searchFilesOptions, error) {
	pattern, _ := args["pattern"].(string)
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return searchFilesOptions{}, fmt.Errorf("pattern is required")
	}

	target, _ := args["target"].(string)
	if target == "" {
		target = "content"
	}
	if target != "content" && target != "files" {
		return searchFilesOptions{}, fmt.Errorf("target must be content or files")
	}

	path, _ := args["path"].(string)
	if strings.TrimSpace(path) == "" {
		path = "."
	}

	outputMode, _ := args["output_mode"].(string)
	if outputMode == "" {
		outputMode = "content"
	}
	if outputMode != "content" && outputMode != "files_only" && outputMode != "count" {
		return searchFilesOptions{}, fmt.Errorf("output_mode must be content, files_only, or count")
	}

	contextLines := intArg(args["context"], 0)
	if contextLines < 0 {
		contextLines = 0
	}
	if contextLines > maxSearchFilesContext {
		contextLines = maxSearchFilesContext
	}

	limit := intArg(args["limit"], defaultSearchFilesLimit)
	if limit <= 0 {
		limit = defaultSearchFilesLimit
	}
	if limit > maxSearchFilesLimit {
		limit = maxSearchFilesLimit
	}

	fileGlob, _ := args["file_glob"].(string)
	return searchFilesOptions{
		pattern:        pattern,
		target:         target,
		path:           path,
		fileGlob:       strings.TrimSpace(fileGlob),
		outputMode:     outputMode,
		context:        contextLines,
		limit:          limit,
		includeIgnored: boolArg(args["include_ignored"], false),
	}, nil
}

func intArg(value any, fallback int) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return fallback
	}
}

func boolArg(value any, fallback bool) bool {
	switch v := value.(type) {
	case bool:
		return v
	default:
		return fallback
	}
}

func (t *SearchFilesTool) searchFileNames(
	ctx context.Context,
	opts searchFilesOptions,
) *ToolResult {
	var matches []string
	truncation := newSearchTruncationInfo(opts.limit)
	stats := &searchWalkStats{
		limits: t.limits,
		ignoredCandidate: func(path string, entry os.DirEntry) bool {
			if entry.IsDir() {
				return false
			}
			matched, err := matchFilePattern(opts.pattern, path, entry.Name())
			return err == nil && matched
		},
	}

	err := walkSearchFilesWithStats(
		ctx,
		t.fs,
		opts.path,
		opts.includeIgnored,
		t.maxFileSize,
		stats,
		func(path string, entry os.DirEntry) error {
			if entry.IsDir() {
				return nil
			}
			name := entry.Name()
			matched, err := matchFilePattern(opts.pattern, path, name)
			if err != nil {
				return err
			}
			if matched {
				matches = append(matches, cleanDisplayPath(path))
				if len(matches) >= opts.limit {
					truncation.mark("count_limit", len(matches), 0, false)
					return errSearchLimitReached
				}
			}
			return nil
		},
	)
	if err != nil && !errors.Is(err, errSearchLimitReached) && !errors.Is(err, errSearchSourceLimitReached) {
		return ErrorResult(err.Error())
	}
	sort.Strings(matches)
	return formatFileNameSearchResult(matches, opts, stats, truncation)
}

func formatFileNameSearchResult(
	matches []string,
	opts searchFilesOptions,
	stats *searchWalkStats,
	truncation *searchTruncationInfo,
) *ToolResult {
	truncation.addWalkStats(stats, opts.includeIgnored)

	if len(matches) == 0 {
		out := "No files matched."
		if truncation.truncated {
			truncation.setReturned(0)
			out += "\n" + truncationBlock(truncation, opts)
		}
		return NewToolResult(out)
	}
	truncation.setReturned(len(matches))

	var b strings.Builder
	fmt.Fprintf(&b, "Matched files: %d", len(matches))
	if truncation.hasReason("count_limit") {
		fmt.Fprintf(&b, " (truncated at limit %d)", opts.limit)
	}
	b.WriteString("\n\n")
	appendSearchResultLines(&b, matches, truncation)
	appendSearchTruncationBlock(&b, truncation, opts)
	return NewToolResult(strings.TrimRight(b.String(), "\n"))
}

func (t *SearchFilesTool) searchContent(ctx context.Context, opts searchFilesOptions) *ToolResult {
	re, err := regexp.Compile(opts.pattern)
	if err != nil {
		return ErrorResult(fmt.Sprintf("invalid regex pattern: %v", err))
	}

	var matches []contentMatch
	fileCounts := map[string]int{}
	filesOnly := map[string]bool{}
	filesScanned := 0
	stats := &searchWalkStats{limits: t.limits}
	truncation := newSearchTruncationInfo(opts.limit)

	walkErr := walkSearchFilesWithStats(
		ctx,
		t.fs,
		opts.path,
		opts.includeIgnored,
		t.maxFileSize,
		stats,
		func(path string, entry os.DirEntry) error {
			if entry.IsDir() {
				return nil
			}
			if opts.fileGlob != "" && !matchGlob(opts.fileGlob, entry.Name(), path) {
				return nil
			}
			data, tooLarge, readErr := readSearchFileBoundedWithStats(
				t.fs,
				path,
				t.maxFileSize,
				stats,
			)
			if readErr != nil {
				if errors.Is(readErr, errSearchSourceLimitReached) {
					return readErr
				}
				stats.readSkipped++
				return errSearchFileSkipped
			}
			if tooLarge {
				stats.maxFileSkipped++
				return nil
			}
			if looksBinary(data) {
				stats.binarySkipped++
				return nil
			}

			filesScanned++
			lines := strings.Split(string(data), "\n")
			for i, line := range lines {
				if !re.MatchString(line) {
					continue
				}

				displayPath := cleanDisplayPath(path)
				fileCounts[displayPath]++
				filesOnly[displayPath] = true

				switch opts.outputMode {
				case "files_only", "count":
					if opts.outputMode == "files_only" && len(filesOnly) >= opts.limit {
						truncation.mark("count_limit", len(filesOnly), 0, false)
						return errSearchLimitReached
					}
				default:
					matches = append(matches, contentMatch{
						path:       displayPath,
						lineNumber: i + 1,
						line:       line,
						before:     contextBefore(lines, i, opts.context),
						after:      contextAfter(lines, i, opts.context),
					})
					if len(matches) >= opts.limit {
						truncation.mark("count_limit", len(matches), 0, false)
						return errSearchLimitReached
					}
				}
			}
			return nil
		},
	)
	if walkErr != nil && !errors.Is(walkErr, errSearchLimitReached) &&
		!errors.Is(walkErr, errSearchFileSkipped) && !errors.Is(walkErr, errSearchSourceLimitReached) {
		return ErrorResult(walkErr.Error())
	}

	return formatContentSearchResult(
		opts,
		matches,
		filesOnly,
		fileCounts,
		filesScanned,
		stats,
		truncation,
	)
}

func formatContentSearchResult(
	opts searchFilesOptions,
	matches []contentMatch,
	filesOnly map[string]bool,
	fileCounts map[string]int,
	filesScanned int,
	stats *searchWalkStats,
	truncation *searchTruncationInfo,
) *ToolResult {
	var b strings.Builder
	filesSkipped := stats.totalSkipped()
	truncation.addWalkStats(stats, opts.includeIgnored)

	switch opts.outputMode {
	case "files_only":
		paths := sortedMapKeys(filesOnly)
		if len(paths) == 0 {
			out := searchNoMatches(filesScanned, filesSkipped)
			if truncation.truncated {
				truncation.setReturned(0)
				out += "\n" + truncationBlock(truncation, opts)
			}
			return NewToolResult(out)
		}
		if len(paths) > opts.limit {
			paths = paths[:opts.limit]
			truncation.mark("count_limit", len(paths), 0, false)
		}
		truncation.setReturned(len(paths))
		fmt.Fprintf(&b, "Matched files: %d", len(paths))
		if truncation.hasReason("count_limit") {
			fmt.Fprintf(&b, " (truncated at limit %d)", opts.limit)
		}
		fmt.Fprintf(&b, "\nFiles scanned: %d, skipped: %d\n\n", filesScanned, filesSkipped)
		appendSearchResultLines(&b, paths, truncation)
	case "count":
		paths := sortedMapKeys(fileCounts)
		if len(paths) == 0 {
			out := searchNoMatches(filesScanned, filesSkipped)
			if truncation.truncated {
				truncation.setReturned(0)
				out += "\n" + truncationBlock(truncation, opts)
			}
			return NewToolResult(out)
		}
		if len(paths) > opts.limit {
			paths = paths[:opts.limit]
			if !truncation.hasReason("count_limit") {
				truncation.mark("count_limit", len(paths), len(fileCounts)-len(paths), true)
			}
		}
		truncation.setReturned(len(paths))
		fmt.Fprintf(
			&b,
			"Matched files: %d",
			len(paths),
		)
		if truncation.hasReason("count_limit") {
			fmt.Fprintf(&b, " (truncated at limit %d)", opts.limit)
		}
		fmt.Fprintf(
			&b,
			"\nFiles scanned: %d, skipped: %d\n\n",
			filesScanned,
			filesSkipped,
		)
		lines := make([]string, 0, len(paths))
		for _, path := range paths {
			lines = append(lines, fmt.Sprintf("%s: %d", path, fileCounts[path]))
		}
		appendSearchResultLines(&b, lines, truncation)
	default:
		if len(matches) == 0 {
			out := searchNoMatches(filesScanned, filesSkipped)
			if truncation.truncated {
				truncation.setReturned(0)
				out += "\n" + truncationBlock(truncation, opts)
			}
			return NewToolResult(out)
		}
		truncation.setReturned(len(matches))
		fmt.Fprintf(&b, "Matches: %d", len(matches))
		if truncation.hasReason("count_limit") {
			fmt.Fprintf(&b, " (truncated at limit %d)", opts.limit)
		}
		fmt.Fprintf(&b, "\nFiles scanned: %d, skipped: %d\n\n", filesScanned, filesSkipped)
		lines := make([]string, 0, len(matches))
		for idx, match := range matches {
			var section strings.Builder
			if idx > 0 {
				section.WriteByte('\n')
			}
			for _, line := range match.before {
				fmt.Fprintf(&section, "%s-%d-%s\n", match.path, line.number, line.text)
			}
			fmt.Fprintf(&section, "%s:%d:%s\n", match.path, match.lineNumber, match.line)
			for _, line := range match.after {
				fmt.Fprintf(&section, "%s-%d-%s\n", match.path, line.number, line.text)
			}
			lines = append(lines, strings.TrimRight(section.String(), "\n"))
		}
		appendSearchResultLines(&b, lines, truncation)
	}

	appendSearchTruncationBlock(&b, truncation, opts)
	return NewToolResult(strings.TrimRight(b.String(), "\n"))
}

func appendSearchResultLines(
	b *strings.Builder,
	lines []string,
	truncation *searchTruncationInfo,
) {
	if len(lines) == 0 {
		return
	}

	base := b.String()
	appended := make([]string, 0, len(lines))
	omitted := 0
	resultLimit := maxSearchFilesResultBytes
	for i, line := range lines {
		lineWithNewline := line + "\n"
		if b.Len()+len(lineWithNewline) > maxSearchFilesResultBytes {
			omitted = len(lines) - i
			break
		}
		b.WriteString(lineWithNewline)
		appended = append(appended, line)
	}

	if omitted == 0 {
		return
	}
	if resultLimit > searchFilesTruncationReserve {
		resultLimit -= searchFilesTruncationReserve
	}
	for len(appended) > 0 && len(base)+joinedLinesLen(appended) > resultLimit {
		appended = appended[:len(appended)-1]
		omitted++
	}
	if truncation != nil {
		truncation.mark("byte_limit", len(appended), omitted, true)
	}
	b.Reset()
	b.WriteString(base)
	for _, line := range appended {
		b.WriteString(line)
		b.WriteByte('\n')
	}
}

func newSearchTruncationInfo(limit int) *searchTruncationInfo {
	return &searchTruncationInfo{
		reasons: map[string]bool{},
		limit:   limit,
	}
}

func (i *searchTruncationInfo) mark(reason string, returned int, omitted int, omittedKnown bool) {
	if i == nil {
		return
	}
	i.truncated = true
	if i.reasons == nil {
		i.reasons = map[string]bool{}
	}
	i.reasons[reason] = true
	if returned > i.returned {
		i.returned = returned
	}
	if !omittedKnown {
		if reason == "count_limit" {
			i.countLimitUnknown = true
			i.omittedUnknown = true
			i.recomputeOmitted()
		}
		return
	}
	switch reason {
	case "count_limit":
		i.countLimitSet = true
		i.countLimitUnknown = false
		i.countLimitOmitted = omitted
	case "byte_limit":
		i.returned = returned
		i.renderedOmittedSet = true
		i.renderedOmitted = omitted
	}
	i.recomputeOmitted()
}

func (i *searchTruncationInfo) setReturned(returned int) {
	if i == nil {
		return
	}
	i.returned = returned
}

func (i *searchTruncationInfo) recomputeOmitted() {
	i.omittedKnown = !i.omittedUnknown && !i.countLimitUnknown &&
		(i.countLimitSet || i.renderedOmittedSet)
	i.omitted = 0
	if i.omittedUnknown || i.countLimitUnknown {
		return
	}
	if i.countLimitSet {
		i.omitted += i.countLimitOmitted
	}
	if i.renderedOmittedSet {
		i.omitted += i.renderedOmitted
	}
}

func (i *searchTruncationInfo) addWalkStats(stats *searchWalkStats, includeIgnored bool) {
	if i == nil || stats == nil {
		return
	}
	if stats.maxFileSkipped > 0 {
		i.mark("max_file_size", i.returned, 0, false)
		i.omittedUnknown = true
		i.skippedMaxFileSize = stats.maxFileSkipped
		i.recomputeOmitted()
	}
	if !includeIgnored && stats.ignoredSkipped > 0 {
		i.mark("ignored_paths", i.returned, 0, false)
		i.omittedUnknown = true
		i.skippedIgnored = stats.ignoredSkipped
		i.recomputeOmitted()
	}
	if stats.sourceLimit != "" {
		i.mark(stats.sourceLimit, i.returned, 0, false)
		i.omittedUnknown = true
		i.recomputeOmitted()
	}
}

func (i *searchTruncationInfo) hasReason(reason string) bool {
	return i != nil && i.reasons != nil && i.reasons[reason]
}

func (s *searchWalkStats) totalSkipped() int {
	if s == nil {
		return 0
	}
	return s.ignoredSkipped + s.maxFileSkipped + s.binarySkipped + s.readSkipped
}

func appendSearchTruncationBlock(
	b *strings.Builder,
	truncation *searchTruncationInfo,
	opts searchFilesOptions,
) {
	if truncation == nil || !truncation.truncated {
		return
	}
	if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
		b.WriteByte('\n')
	}
	if b.Len() > 0 {
		b.WriteByte('\n')
	}
	b.WriteString(truncationBlock(truncation, opts))
	b.WriteByte('\n')
}

func truncationBlock(truncation *searchTruncationInfo, opts searchFilesOptions) string {
	reasons := truncationReasons(truncation)
	var b strings.Builder
	b.WriteString("Search truncation:\n")
	b.WriteString("  truncated=true\n")
	fmt.Fprintf(&b, "  reason=%s\n", strings.Join(reasons, ","))
	fmt.Fprintf(&b, "  returned_count=%d\n", truncation.returned)
	fmt.Fprintf(&b, "  limit=%d\n", truncation.limit)
	if truncation.omittedKnown {
		fmt.Fprintf(&b, "  omitted_count=%d\n", truncation.omitted)
	} else {
		b.WriteString("  omitted_count=unknown\n")
	}
	if truncation.countLimitSet {
		fmt.Fprintf(&b, "  count_limit_omitted_count=%d\n", truncation.countLimitOmitted)
	}
	if truncation.renderedOmittedSet {
		fmt.Fprintf(&b, "  rendered_omitted_count=%d\n", truncation.renderedOmitted)
	}
	if truncation.skippedIgnored > 0 {
		fmt.Fprintf(&b, "  skipped_ignored_count=%d\n", truncation.skippedIgnored)
	}
	if truncation.skippedMaxFileSize > 0 {
		fmt.Fprintf(&b, "  skipped_max_file_size_count=%d\n", truncation.skippedMaxFileSize)
	}
	b.WriteString("  suggested_narrowing.path=set a narrower path\n")
	b.WriteString("  suggested_narrowing.file_glob=set file_glob such as *.go or *.md\n")
	b.WriteString("  suggested_narrowing.pattern=make pattern more specific")
	if !opts.includeIgnored && truncation.hasReason("ignored_paths") {
		b.WriteString("\n  suggested_narrowing.include_ignored=true only if ignored/runtime files are needed")
	}
	return b.String()
}

func truncationReasons(truncation *searchTruncationInfo) []string {
	if truncation == nil || len(truncation.reasons) == 0 {
		return []string{"unknown"}
	}
	reasons := make([]string, 0, len(truncation.reasons))
	for reason := range truncation.reasons {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	return reasons
}

func joinedLinesLen(lines []string) int {
	total := 0
	for _, line := range lines {
		total += len(line) + 1
	}
	return total
}

func searchNoMatches(filesScanned int, filesSkipped int) string {
	return fmt.Sprintf(
		"No matches found. Files scanned: %d, skipped: %d",
		filesScanned,
		filesSkipped,
	)
}

func sortedMapKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func contextBefore(lines []string, idx int, count int) []numberedLine {
	if count <= 0 {
		return nil
	}
	start := idx - count
	if start < 0 {
		start = 0
	}
	out := make([]numberedLine, 0, idx-start)
	for i := start; i < idx; i++ {
		out = append(out, numberedLine{number: i + 1, text: lines[i]})
	}
	return out
}

func contextAfter(lines []string, idx int, count int) []numberedLine {
	if count <= 0 {
		return nil
	}
	end := idx + count
	if end >= len(lines) {
		end = len(lines) - 1
	}
	out := make([]numberedLine, 0, end-idx)
	for i := idx + 1; i <= end; i++ {
		out = append(out, numberedLine{number: i + 1, text: lines[i]})
	}
	return out
}

var (
	errSearchLimitReached       = fmt.Errorf("search limit reached")
	errSearchFileSkipped        = fmt.Errorf("search file skipped")
	errSearchSourceLimitReached = fmt.Errorf("search source limit reached")
)

func walkSearchFilesWithStats(
	ctx context.Context,
	sysFs fileSystem,
	root string,
	includeIgnored bool,
	maxFileSize int,
	stats *searchWalkStats,
	fn func(path string, entry os.DirEntry) error,
) error {
	return walkSearchFilesWithIgnore(ctx, sysFs, root, includeIgnored, maxFileSize, gitIgnoreState{}, stats, fn)
}

func walkSearchFilesWithIgnore(
	ctx context.Context,
	sysFs fileSystem,
	root string,
	includeIgnored bool,
	maxFileSize int,
	ignoreState gitIgnoreState,
	stats *searchWalkStats,
	fn func(path string, entry os.DirEntry) error,
) error {
	entries, directoryTruncated, err := readSearchDirectory(sysFs, root, stats)
	if err != nil {
		if errors.Is(err, errSearchSourceLimitReached) {
			return err
		}
		file, openErr := sysFs.Open(root)
		if openErr != nil {
			return fmt.Errorf("failed to search path: %w", err)
		}
		info, statErr := file.Stat()
		_ = file.Close()
		if statErr != nil || info.IsDir() {
			return fmt.Errorf("failed to search path: %w", err)
		}
		entry := fs.FileInfoToDirEntry(info)
		if !includeIgnored {
			var ignoreSafe bool
			var ignoreErr error
			ignoreState, ignoreSafe, ignoreErr = ignoreState.withGitIgnore(
				sysFs,
				filepath.Dir(root),
				maxFileSize,
				stats,
			)
			if ignoreErr != nil {
				return ignoreErr
			}
			if !ignoreSafe {
				return fmt.Errorf("failed to search path: .gitignore exceeds the search file-size limit")
			}
		}
		if !includeIgnored && ignoreState.ignored(root, false) {
			recordIgnoredSearchSkip(stats, root, entry)
			return nil
		}
		if stats != nil && stats.limits.files > 0 {
			if stats.filesVisited >= stats.limits.files {
				stats.sourceLimit = "source_file_limit"
				return errSearchSourceLimitReached
			}
			stats.filesVisited++
		}
		return fn(root, entry)
	}

	if !includeIgnored {
		var ignoreSafe bool
		var ignoreErr error
		ignoreState, ignoreSafe, ignoreErr = ignoreState.withGitIgnore(sysFs, root, maxFileSize, stats)
		if ignoreErr != nil {
			return ignoreErr
		}
		if !ignoreSafe {
			return fmt.Errorf("failed to search path: .gitignore exceeds the search file-size limit")
		}
	}

	slices.SortFunc(entries, func(a, b fs.DirEntry) int {
		return cmp.Compare(a.Name(), b.Name())
	})

	for _, entry := range entries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if shouldSkipSearchEntry(entry, includeIgnored) {
			if entry.Name() != ".git" {
				recordIgnoredSearchSkip(stats, joinSearchPath(root, entry.Name()), entry)
			}
			continue
		}

		path := joinSearchPath(root, entry.Name())
		if !includeIgnored && ignoreState.ignored(path, entry.IsDir()) {
			recordIgnoredSearchSkip(stats, path, entry)
			continue
		}
		if !entry.IsDir() && stats != nil && stats.limits.files > 0 {
			if stats.filesVisited >= stats.limits.files {
				stats.sourceLimit = "source_file_limit"
				return errSearchSourceLimitReached
			}
			stats.filesVisited++
		}
		if err := fn(path, entry); err != nil {
			if errors.Is(err, errSearchFileSkipped) {
				continue
			}
			return err
		}
		if entry.IsDir() {
			if err := walkSearchFilesWithIgnore(
				ctx,
				sysFs,
				path,
				includeIgnored,
				maxFileSize,
				ignoreState,
				stats,
				fn,
			); err != nil {
				return err
			}
		}
	}
	if directoryTruncated {
		return errSearchSourceLimitReached
	}
	return nil
}

func readSearchDirectory(
	sysFs fileSystem,
	root string,
	stats *searchWalkStats,
) ([]os.DirEntry, bool, error) {
	if stats == nil || stats.limits.entries <= 0 {
		entries, err := sysFs.ReadDir(root)
		return entries, false, err
	}
	remaining := stats.limits.entries - stats.entriesVisited
	if remaining <= 0 {
		stats.sourceLimit = "source_entry_limit"
		return nil, false, errSearchSourceLimitReached
	}
	file, err := sysFs.Open(root)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, false, err
	}
	if !info.IsDir() {
		return nil, false, fmt.Errorf("search path is not a directory")
	}
	directory, ok := file.(fs.ReadDirFile)
	if !ok {
		return nil, false, fmt.Errorf("bounded search cannot stream directory entries")
	}
	entries, err := directory.ReadDir(remaining + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, err
	}
	truncated := len(entries) > remaining
	if truncated {
		entries = entries[:remaining]
		stats.sourceLimit = "source_entry_limit"
	}
	stats.entriesVisited += len(entries)
	return entries, truncated, nil
}

func recordIgnoredSearchSkip(stats *searchWalkStats, path string, entry os.DirEntry) {
	if stats == nil {
		return
	}
	if stats.ignoredCandidate != nil && !stats.ignoredCandidate(path, entry) {
		return
	}
	stats.ignoredSkipped++
}

func joinSearchPath(root string, name string) string {
	if root == "" || root == "." {
		return name
	}
	return filepath.Join(root, name)
}

func shouldSkipSearchEntry(entry os.DirEntry, includeIgnored bool) bool {
	name := entry.Name()
	if name == ".git" {
		return true
	}
	if !includeIgnored && (name == "node_modules" || name == ".cache" || name == "vendor") {
		return true
	}
	return entry.Type()&os.ModeSymlink != 0
}

type gitIgnoreState struct {
	rules []gitIgnoreRule
}

type gitIgnoreRule struct {
	base     string
	pattern  string
	negated  bool
	dirOnly  bool
	anchored bool
	hasSlash bool
}

func (s gitIgnoreState) withGitIgnore(
	sysFs fileSystem,
	dir string,
	maxFileSize int,
	stats *searchWalkStats,
) (gitIgnoreState, bool, error) {
	data, tooLarge, err := readSearchFileBoundedWithStats(
		sysFs,
		joinSearchPath(dir, ".gitignore"),
		maxFileSize,
		stats,
	)
	if errors.Is(err, errSearchSourceLimitReached) {
		return s, false, err
	}
	if tooLarge {
		return s, false, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return s, true, nil
	}
	if err != nil {
		return s, false, err
	}
	if len(data) == 0 {
		return s, true, nil
	}

	next := gitIgnoreState{rules: append([]gitIgnoreRule(nil), s.rules...)}
	base := cleanDisplayPath(dir)
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		negated := strings.HasPrefix(line, "!")
		if negated {
			line = strings.TrimSpace(strings.TrimPrefix(line, "!"))
			if line == "" {
				continue
			}
		}
		line = strings.TrimPrefix(line, "\\")

		dirOnly := strings.HasSuffix(line, "/")
		line = strings.TrimRight(line, "/")
		anchored := strings.HasPrefix(line, "/")
		line = strings.TrimLeft(line, "/")
		if line == "" {
			continue
		}

		pattern := filepath.ToSlash(filepath.Clean(line))
		next.rules = append(next.rules, gitIgnoreRule{
			base:     base,
			pattern:  pattern,
			negated:  negated,
			dirOnly:  dirOnly,
			anchored: anchored,
			hasSlash: strings.Contains(pattern, "/"),
		})
	}
	return next, true, nil
}

func readSearchFileBoundedWithStats(
	sysFs fileSystem,
	path string,
	maxFileSize int,
	stats *searchWalkStats,
) ([]byte, bool, error) {
	file, err := sysFs.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = file.Close() }()
	remaining := int64(maxFileSize + 1)
	if stats != nil && stats.limits.bytes > 0 {
		remaining = stats.limits.bytes - stats.bytesRead
		if remaining <= 0 {
			stats.sourceLimit = "source_byte_limit"
			return nil, false, errSearchSourceLimitReached
		}
	}
	if info, statErr := file.Stat(); statErr == nil {
		if info.Size() > int64(maxFileSize) {
			return nil, true, nil
		}
		if info.Size() > remaining {
			stats.sourceLimit = "source_byte_limit"
			return nil, false, errSearchSourceLimitReached
		}
	}
	readLimit := min(int64(maxFileSize)+1, remaining)
	data, err := io.ReadAll(io.LimitReader(file, readLimit))
	if err != nil {
		return nil, false, err
	}
	if stats != nil {
		stats.bytesRead += int64(len(data))
		if stats.limits.bytes > 0 && int64(len(data)) == remaining {
			stats.sourceLimit = "source_byte_limit"
			return nil, false, errSearchSourceLimitReached
		}
	}
	if len(data) > maxFileSize {
		return nil, true, nil
	}
	return data, false, nil
}

func (s gitIgnoreState) ignored(path string, isDir bool) bool {
	ignored := false
	for _, rule := range s.rules {
		if rule.matches(path, isDir) {
			ignored = !rule.negated
		}
	}
	return ignored
}

func (r gitIgnoreRule) matches(path string, isDir bool) bool {
	if r.dirOnly && !isDir {
		return false
	}

	rel := relativeToIgnoreBase(path, r.base)
	if rel == "" || rel == "." || strings.HasPrefix(rel, "../") {
		return false
	}

	if r.anchored || r.hasSlash {
		return matchIgnorePattern(r.pattern, rel)
	}

	for _, part := range strings.Split(rel, "/") {
		if matchIgnorePattern(r.pattern, part) {
			return true
		}
	}
	return false
}

func relativeToIgnoreBase(path string, base string) string {
	cleanPath := cleanDisplayPath(path)
	cleanBase := cleanDisplayPath(base)
	if cleanBase == "." || cleanBase == "" {
		return cleanPath
	}
	if cleanPath == cleanBase {
		return "."
	}
	prefix := cleanBase + "/"
	if strings.HasPrefix(cleanPath, prefix) {
		return strings.TrimPrefix(cleanPath, prefix)
	}
	rel, err := filepath.Rel(filepath.FromSlash(cleanBase), filepath.FromSlash(cleanPath))
	if err != nil {
		return cleanPath
	}
	return filepath.ToSlash(rel)
}

func matchIgnorePattern(pattern string, value string) bool {
	value = cleanDisplayPath(value)
	pattern = filepath.ToSlash(pattern)

	if ok, _ := filepath.Match(filepath.FromSlash(pattern), filepath.FromSlash(value)); ok {
		return true
	}
	if strings.Contains(pattern, "**") {
		simplified := strings.ReplaceAll(pattern, "**/", "")
		if ok, _ := filepath.Match(filepath.FromSlash(simplified), filepath.FromSlash(value)); ok {
			return true
		}
		if strings.HasSuffix(pattern, "/**") {
			prefix := strings.TrimSuffix(pattern, "/**")
			return value == prefix || strings.HasPrefix(value, prefix+"/")
		}
	}
	return pattern == value
}

func matchFilePattern(pattern string, path string, name string) (bool, error) {
	if matched := matchGlob(pattern, name, path); matched {
		return true, nil
	}
	if strings.ContainsAny(pattern, "*?[") {
		return false, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false, fmt.Errorf("invalid file search pattern: %w", err)
	}
	return re.MatchString(path) || re.MatchString(name), nil
}

func matchGlob(pattern string, name string, path string) bool {
	if pattern == "" {
		return true
	}
	if ok, _ := filepath.Match(pattern, name); ok {
		return true
	}
	if ok, _ := filepath.Match(pattern, path); ok {
		return true
	}
	return strings.Contains(name, pattern) || strings.Contains(path, pattern)
}

func looksBinary(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	sample := data
	if len(sample) > 8000 {
		sample = sample[:8000]
	}
	for _, b := range sample {
		if b == 0 {
			return true
		}
	}
	return false
}

func cleanDisplayPath(path string) string {
	cleaned := filepath.Clean(path)
	if cleaned == "." {
		return cleaned
	}
	return filepath.ToSlash(cleaned)
}
