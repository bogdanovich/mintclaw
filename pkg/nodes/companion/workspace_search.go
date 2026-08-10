package companion

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

const (
	workspaceSearchMaxFileBytes = 1024 * 1024
	workspaceSearchBatchSize    = 256
	workspaceSearchMaxDepth     = 64
)

type WorkspaceSearchOptions struct {
	Pattern        string
	Target         string
	Path           string
	FileGlob       string
	OutputMode     string
	Context        int
	Limit          int
	IncludeIgnored bool
}

type WorkspaceSearchResult struct {
	Result       string `json:"result"`
	Matches      int    `json:"matches"`
	FilesVisited int    `json:"files_visited"`
	Truncated    bool   `json:"truncated"`
}

type workspaceIgnoreRule struct {
	base     string
	pattern  string
	negated  bool
	dirOnly  bool
	anchored bool
	hasSlash bool
}

type workspaceSearchState struct {
	ctx       context.Context
	profile   *fileProfileRuntime
	root      *fileRoot
	workspace string
	options   WorkspaceSearchOptions
	regex     *regexp.Regexp
	result    strings.Builder
	matches   int
	visited   int
	examined  int
	counted   int
	truncated bool
}

func (runtime *FileTransferRuntime) SearchWorkspace(
	ctx context.Context,
	profileRevision string,
	workspaceRoot string,
	options WorkspaceSearchOptions,
) (WorkspaceSearchResult, error) {
	if runtime == nil || !filepath.IsAbs(workspaceRoot) {
		return WorkspaceSearchResult{}, ErrFileAccessDenied
	}
	profile := runtime.profiles[profileRevision]
	if profile == nil {
		return WorkspaceSearchResult{}, ErrFileAccessDenied
	}
	if options.Target == "" {
		options.Target = "content"
	}
	if options.OutputMode == "" {
		options.OutputMode = "content"
	}
	if options.Limit <= 0 {
		options.Limit = 100
	}
	if options.Limit > nodes.MaxWorkspaceSearchLimit || options.Context < 0 || options.Context > 10 ||
		(options.Target != "content" && options.Target != "files") ||
		(options.OutputMode != "content" && options.OutputMode != "files_only" && options.OutputMode != "count") {
		return WorkspaceSearchResult{}, ErrFileAccessDenied
	}
	if options.FileGlob != "" {
		if _, err := filepath.Match(options.FileGlob, "candidate"); err != nil {
			return WorkspaceSearchResult{}, ErrFileAccessDenied
		}
	}
	root := profile.workspaceReadableRoot(workspaceRoot)
	if root == nil {
		return WorkspaceSearchResult{}, ErrFileAccessDenied
	}
	var pattern *regexp.Regexp
	if options.Target == "content" {
		var err error
		pattern, err = regexp.Compile(options.Pattern)
		if err != nil {
			return WorkspaceSearchResult{}, ErrFileAccessDenied
		}
	}
	state := &workspaceSearchState{
		ctx: ctx, profile: profile, root: root, workspace: workspaceRoot, options: options, regex: pattern,
	}
	start := workspaceRoot
	var inherited []workspaceIgnoreRule
	depth := 0
	if options.Path != "" {
		start = filepath.Join(workspaceRoot, filepath.FromSlash(options.Path))
		if !pathWithinWorkspaceRoot(workspaceRoot, start) {
			return WorkspaceSearchResult{}, ErrFileAccessDenied
		}
		var ignored bool
		inherited, depth, ignored = state.inheritedIgnoreRules(start)
		if ignored {
			return WorkspaceSearchResult{}, nil
		}
	}
	if err := state.walk(start, inherited, depth); err != nil {
		return WorkspaceSearchResult{}, err
	}
	return WorkspaceSearchResult{
		Result: state.result.String(), Matches: state.matches,
		FilesVisited: state.visited, Truncated: state.truncated,
	}, nil
}

func (profile *fileProfileRuntime) workspaceReadableRoot(workspace string) *fileRoot {
	for _, root := range profile.readableRoots {
		if workspace == root.path || pathWithinWorkspaceRoot(root.path, workspace) {
			return root
		}
	}
	return nil
}

func (state *workspaceSearchState) walk(
	directory string,
	inherited []workspaceIgnoreRule,
	depth int,
) error {
	if err := state.ctx.Err(); err != nil {
		return err
	}
	if depth > workspaceSearchMaxDepth {
		state.truncated = true
		return nil
	}
	directoryHandle, err := state.root.openDirectory(directory, state.profile.profile.CrossMounts)
	if err != nil {
		if source, openErr := state.profile.openReadable(directory); openErr == nil {
			defer func() { _ = source.file.Close() }()
			return state.visitFile(directory, filepath.Base(directory), inherited)
		}
		return err
	}
	defer func() { _ = directoryHandle.Close() }()
	rules := inherited
	if !state.options.IncludeIgnored {
		rules = append(append([]workspaceIgnoreRule(nil), inherited...), state.loadIgnore(directory)...)
	}
	for {
		entries, readErr := directoryHandle.ReadDir(workspaceSearchBatchSize)
		slices.SortFunc(entries, func(left, right os.DirEntry) int {
			return strings.Compare(left.Name(), right.Name())
		})
		for _, entry := range entries {
			if err := state.ctx.Err(); err != nil {
				return err
			}
			if state.truncated || state.examined >= nodes.MaxWorkspaceSearchFiles || state.limitReached() {
				state.truncated = true
				return nil
			}
			state.examined++
			name := entry.Name()
			path := filepath.Join(directory, name)
			rel, relErr := filepath.Rel(state.workspace, path)
			if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return ErrFileAccessDenied
			}
			if entry.Type()&os.ModeSymlink != 0 || name == ".git" ||
				(!state.options.IncludeIgnored && workspaceDefaultIgnoredDirectory(name, entry.IsDir())) ||
				(!state.options.IncludeIgnored && workspaceIgnored(filepath.ToSlash(rel), entry.IsDir(), rules)) {
				continue
			}
			if entry.IsDir() {
				if err := state.walk(path, rules, depth+1); err != nil {
					return err
				}
				continue
			}
			if entry.Type().IsRegular() {
				if err := state.visitFile(path, name, rules); err != nil {
					return err
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return ErrFileAccessDenied
		}
	}
}

func (state *workspaceSearchState) inheritedIgnoreRules(
	start string,
) ([]workspaceIgnoreRule, int, bool) {
	rel, err := filepath.Rel(state.workspace, start)
	if err != nil || rel == "." || rel == "" {
		return nil, 0, false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if state.options.IncludeIgnored {
		return nil, len(parts), false
	}
	rules := state.loadIgnore(state.workspace)
	directory := state.workspace
	for index, part := range parts {
		candidate := filepath.Join(directory, filepath.FromSlash(part))
		candidateRel := filepath.ToSlash(filepath.Join(parts[:index+1]...))
		isLast := index == len(parts)-1
		isDir := !isLast
		if isLast {
			handle, openErr := state.root.openDirectory(candidate, state.profile.profile.CrossMounts)
			if openErr == nil {
				isDir = true
				_ = handle.Close()
			}
		}
		if workspaceDefaultIgnoredDirectory(part, isDir) || workspaceIgnored(candidateRel, isDir, rules) {
			return rules, len(parts), true
		}
		if !isLast {
			directory = candidate
			rules = append(rules, state.loadIgnore(directory)...)
		}
	}
	return rules, len(parts), false
}

func workspaceDefaultIgnoredDirectory(name string, directory bool) bool {
	return directory && (name == "node_modules" || name == ".cache" || name == "vendor")
}

func (state *workspaceSearchState) limitReached() bool {
	if state.options.OutputMode == "count" {
		return state.counted >= state.options.Limit
	}
	return state.matches >= state.options.Limit
}

func (state *workspaceSearchState) visitFile(path, name string, _ []workspaceIgnoreRule) error {
	rel, err := filepath.Rel(state.workspace, path)
	if err != nil {
		return ErrFileAccessDenied
	}
	rel = filepath.ToSlash(rel)
	if state.options.FileGlob != "" {
		matched, matchErr := filepath.Match(state.options.FileGlob, name)
		if matchErr != nil {
			return ErrFileAccessDenied
		}
		if !matched {
			return nil
		}
	}
	state.visited++
	if state.options.Target == "files" {
		if matchWorkspaceFilePattern(state.options.Pattern, name, rel) {
			state.addMatch(rel)
		}
		return nil
	}
	source, err := state.profile.openReadable(path)
	if err != nil || source.info.Size() > workspaceSearchMaxFileBytes ||
		source.info.Size() > state.profile.profile.MaxFileBytes {
		return nil
	}
	defer func() { _ = source.file.Close() }()
	data, err := io.ReadAll(io.LimitReader(source.file, workspaceSearchMaxFileBytes+1))
	if err != nil || len(data) > workspaceSearchMaxFileBytes || !utf8.Valid(data) ||
		strings.IndexByte(string(data), 0) >= 0 {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	if state.options.OutputMode == "count" {
		count := state.regexCount(lines)
		if count > 0 {
			state.matches += count
			state.counted++
			state.appendLine(fmt.Sprintf("%s:%d", rel, count))
		}
		return nil
	}
	for index, line := range lines {
		if !state.regex.MatchString(line) {
			continue
		}
		state.matches++
		switch state.options.OutputMode {
		case "files_only":
			state.appendLine(rel)
			return nil
		default:
			start := max(0, index-state.options.Context)
			end := min(len(lines), index+state.options.Context+1)
			for contextIndex := start; contextIndex < end; contextIndex++ {
				state.appendLine(fmt.Sprintf("%s:%d:%s", rel, contextIndex+1, lines[contextIndex]))
			}
		}
		if state.matches >= state.options.Limit {
			state.truncated = true
			return nil
		}
	}
	return nil
}

func matchWorkspaceFilePattern(pattern, name, path string) bool {
	if pattern == "" {
		return true
	}
	if matched, _ := filepath.Match(pattern, name); matched {
		return true
	}
	if matched, _ := filepath.Match(pattern, filepath.FromSlash(path)); matched {
		return true
	}
	if strings.Contains(name, pattern) || strings.Contains(path, pattern) {
		return true
	}
	if strings.ContainsAny(pattern, "*?[") {
		return false
	}
	compiled, err := regexp.Compile(pattern)
	return err == nil && (compiled.MatchString(name) || compiled.MatchString(path))
}

func (state *workspaceSearchState) regexCount(lines []string) int {
	count := 0
	for _, line := range lines {
		if state.regex.MatchString(line) {
			count++
		}
	}
	return count
}

func (state *workspaceSearchState) addMatch(path string) {
	state.matches++
	state.appendLine(path)
}

func (state *workspaceSearchState) appendLine(line string) {
	if state.result.Len()+len(line)+1 > nodes.MaxWorkspaceSearchResult {
		state.truncated = true
		return
	}
	if state.result.Len() > 0 {
		state.result.WriteByte('\n')
	}
	state.result.WriteString(line)
}

func (state *workspaceSearchState) loadIgnore(directory string) []workspaceIgnoreRule {
	path := filepath.Join(directory, ".gitignore")
	source, err := state.profile.openReadable(path)
	if err != nil || source.info.Size() > 64*1024 {
		return nil
	}
	defer func() { _ = source.file.Close() }()
	base, err := filepath.Rel(state.workspace, directory)
	if err != nil {
		return nil
	}
	var rules []workspaceIgnoreRule
	scanner := bufio.NewScanner(source.file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		escapedPrefix := strings.HasPrefix(line, `\!`) || strings.HasPrefix(line, `\#`)
		negated := !escapedPrefix && strings.HasPrefix(line, "!")
		if negated {
			line = strings.TrimSpace(strings.TrimPrefix(line, "!"))
		}
		if escapedPrefix {
			line = strings.TrimPrefix(line, `\`)
		}
		dirOnly := strings.HasSuffix(line, "/")
		line = strings.TrimRight(line, "/")
		anchored := strings.HasPrefix(line, "/")
		line = strings.TrimLeft(line, "/")
		if line != "" {
			pattern := filepath.ToSlash(filepath.Clean(line))
			rules = append(rules, workspaceIgnoreRule{
				base: filepath.ToSlash(base), pattern: pattern, negated: negated, dirOnly: dirOnly,
				anchored: anchored, hasSlash: strings.Contains(pattern, "/"),
			})
		}
	}
	return rules
}

func workspaceIgnored(path string, directory bool, rules []workspaceIgnoreRule) bool {
	ignored := false
	for _, rule := range rules {
		if rule.dirOnly && !directory {
			continue
		}
		rel := relativeToWorkspaceIgnoreBase(path, rule.base)
		if rel == "" || rel == "." || strings.HasPrefix(rel, "../") {
			continue
		}
		matched := false
		if rule.anchored || rule.hasSlash {
			matched = matchWorkspaceIgnorePattern(rule.pattern, rel)
		} else {
			for _, part := range strings.Split(rel, "/") {
				if matchWorkspaceIgnorePattern(rule.pattern, part) {
					matched = true
					break
				}
			}
		}
		if matched {
			ignored = !rule.negated
		}
	}
	return ignored
}

func relativeToWorkspaceIgnoreBase(path, base string) string {
	path = strings.TrimPrefix(filepath.ToSlash(filepath.Clean(path)), "./")
	base = strings.TrimPrefix(filepath.ToSlash(filepath.Clean(base)), "./")
	if base == "." || base == "" {
		return path
	}
	if path == base {
		return "."
	}
	prefix := base + "/"
	if !strings.HasPrefix(path, prefix) {
		return "../"
	}
	return strings.TrimPrefix(path, prefix)
}

func matchWorkspaceIgnorePattern(pattern, value string) bool {
	rawPattern := strings.Split(filepath.ToSlash(pattern), "/")
	patternParts := make([]string, 0, len(rawPattern))
	requiredParts := 0
	for _, part := range rawPattern {
		if part == "**" && len(patternParts) > 0 && patternParts[len(patternParts)-1] == "**" {
			continue
		}
		patternParts = append(patternParts, part)
		if part != "**" {
			requiredParts++
		}
	}
	valueParts := strings.Split(filepath.ToSlash(value), "/")
	if requiredParts > len(valueParts) {
		return false
	}
	type position struct{ pattern, value int }
	memo := make(map[position]bool, len(patternParts)*len(valueParts))
	seen := make(map[position]bool, len(patternParts)*len(valueParts))
	var match func(int, int) bool
	match = func(patternIndex, valueIndex int) bool {
		current := position{pattern: patternIndex, value: valueIndex}
		if seen[current] {
			return memo[current]
		}
		seen[current] = true
		if patternIndex == len(patternParts) {
			memo[current] = valueIndex == len(valueParts)
			return memo[current]
		}
		if patternParts[patternIndex] == "**" {
			memo[current] = match(patternIndex+1, valueIndex) ||
				(valueIndex < len(valueParts) && match(patternIndex, valueIndex+1))
			return memo[current]
		}
		if valueIndex == len(valueParts) {
			return false
		}
		matched, err := pathpkg.Match(patternParts[patternIndex], valueParts[valueIndex])
		memo[current] = err == nil && matched && match(patternIndex+1, valueIndex+1)
		return memo[current]
	}
	return match(0, 0)
}
