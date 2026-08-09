package companion

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

const workspaceSearchMaxFileBytes = 1024 * 1024

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
	base    string
	pattern string
	negated bool
	dirOnly bool
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
	if options.Path != "" {
		start = filepath.Join(workspaceRoot, filepath.FromSlash(options.Path))
		if !pathWithinWorkspaceRoot(workspaceRoot, start) {
			return WorkspaceSearchResult{}, ErrFileAccessDenied
		}
	}
	if err := state.walk(start, nil); err != nil {
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

func (state *workspaceSearchState) walk(directory string, inherited []workspaceIgnoreRule) error {
	if err := state.ctx.Err(); err != nil {
		return err
	}
	entries, err := state.root.readDirectory(directory, state.profile.profile.CrossMounts)
	if err != nil {
		if source, openErr := state.profile.openReadable(directory); openErr == nil {
			defer func() { _ = source.file.Close() }()
			return state.visitFile(directory, filepath.Base(directory), inherited)
		}
		return err
	}
	rules := inherited
	if !state.options.IncludeIgnored {
		rules = append(append([]workspaceIgnoreRule(nil), inherited...), state.loadIgnore(directory)...)
	}
	for _, entry := range entries {
		if err := state.ctx.Err(); err != nil {
			return err
		}
		if state.truncated || state.visited >= nodes.MaxWorkspaceSearchFiles || state.matches >= state.options.Limit {
			state.truncated = true
			return nil
		}
		name := entry.Name()
		path := filepath.Join(directory, name)
		rel, relErr := filepath.Rel(state.workspace, path)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return ErrFileAccessDenied
		}
		if entry.Type()&os.ModeSymlink != 0 || name == ".git" ||
			(!state.options.IncludeIgnored && (name == "node_modules" || name == ".cache" || name == "vendor")) ||
			(!state.options.IncludeIgnored && workspaceIgnored(filepath.ToSlash(rel), entry.IsDir(), rules)) {
			continue
		}
		if entry.IsDir() {
			if err := state.walk(path, rules); err != nil {
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
	return nil
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
	if err != nil || source.info.Size() > workspaceSearchMaxFileBytes {
		return nil
	}
	defer func() { _ = source.file.Close() }()
	data, err := io.ReadAll(io.LimitReader(source.file, workspaceSearchMaxFileBytes+1))
	if err != nil || len(data) > workspaceSearchMaxFileBytes || !utf8.Valid(data) ||
		strings.IndexByte(string(data), 0) >= 0 {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	fileMatched := false
	for index, line := range lines {
		if !state.regex.MatchString(line) {
			continue
		}
		state.matches++
		fileMatched = true
		switch state.options.OutputMode {
		case "files_only":
			state.appendLine(rel)
			return nil
		case "count":
			// Counts are rendered after scanning the file.
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
	if fileMatched && state.options.OutputMode == "count" {
		state.appendLine(fmt.Sprintf("%s:%d", rel, state.regexCount(lines)))
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
		negated := strings.HasPrefix(line, "!")
		line = strings.TrimPrefix(line, "!")
		dirOnly := strings.HasSuffix(line, "/")
		line = strings.Trim(line, "/")
		if line != "" {
			rules = append(rules, workspaceIgnoreRule{
				base: filepath.ToSlash(base), pattern: filepath.ToSlash(line), negated: negated, dirOnly: dirOnly,
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
		rel := path
		if rule.base != "." && rule.base != "" {
			prefix := rule.base + "/"
			if !strings.HasPrefix(path, prefix) {
				continue
			}
			rel = strings.TrimPrefix(path, prefix)
		}
		matched, _ := filepath.Match(filepath.FromSlash(rule.pattern), filepath.FromSlash(rel))
		if !matched && !strings.Contains(rule.pattern, "/") {
			for _, part := range strings.Split(rel, "/") {
				if partMatch, _ := filepath.Match(rule.pattern, part); partMatch {
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
