package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	defaultChangedPathLimit = 128
	defaultCommandBytes     = 512 << 10
	defaultPromptBytes      = 24 << 10
	defaultCommandTimeout   = 5 * time.Second
	defaultDiffFiles        = 64
	defaultDiffHunks        = 256
	defaultDiffLines        = 4000
	defaultDiffLineBytes    = 8 << 10
	defaultDiffBytes        = 512 << 10
	defaultUntrackedBytes   = 128 << 10
	defaultConcurrentOps    = 2
)

type Limits struct {
	ChangedPaths         int
	CommandBytes         int
	PromptBytes          int
	Timeout              time.Duration
	DiffFiles            int
	DiffHunks            int
	DiffLines            int
	DiffLineBytes        int
	DiffBytes            int
	UntrackedBytes       int
	ConcurrentOperations int
}

func (limits Limits) normalized() Limits {
	if limits.ChangedPaths <= 0 {
		limits.ChangedPaths = defaultChangedPathLimit
	}
	if limits.CommandBytes <= 0 {
		limits.CommandBytes = defaultCommandBytes
	}
	if limits.PromptBytes <= 0 {
		limits.PromptBytes = defaultPromptBytes
	}
	if limits.Timeout <= 0 {
		limits.Timeout = defaultCommandTimeout
	}
	if limits.DiffFiles <= 0 {
		limits.DiffFiles = defaultDiffFiles
	}
	if limits.DiffHunks <= 0 {
		limits.DiffHunks = defaultDiffHunks
	}
	if limits.DiffLines <= 0 {
		limits.DiffLines = defaultDiffLines
	}
	if limits.DiffLineBytes <= 0 {
		limits.DiffLineBytes = defaultDiffLineBytes
	}
	if limits.DiffBytes <= 0 {
		limits.DiffBytes = defaultDiffBytes
	}
	if limits.UntrackedBytes <= 0 {
		limits.UntrackedBytes = defaultUntrackedBytes
	}
	if limits.ConcurrentOperations <= 0 {
		limits.ConcurrentOperations = defaultConcurrentOps
	}
	return limits
}

type ChangedPath struct {
	Path         string `json:"path"`
	OriginalPath string `json:"original_path,omitempty"`
	Status       string `json:"status"`
}

type DiffStat struct {
	Files       int `json:"files"`
	Additions   int `json:"additions"`
	Deletions   int `json:"deletions"`
	BinaryFiles int `json:"binary_files,omitempty"`
}

type GitState struct {
	Available         bool   `json:"available"`
	StatusAvailable   bool   `json:"status_available,omitempty"`
	TopLevel          string `json:"top_level,omitempty"`
	GitDir            string `json:"git_dir,omitempty"`
	CommonDir         string `json:"common_dir,omitempty"`
	Branch            string `json:"branch,omitempty"`
	Head              string `json:"head,omitempty"`
	Detached          bool   `json:"detached,omitempty"`
	Unborn            bool   `json:"unborn,omitempty"`
	Worktree          bool   `json:"worktree,omitempty"`
	Dirty             bool   `json:"dirty,omitempty"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

type Snapshot struct {
	ProjectRoot                   string        `json:"project_root"`
	CWD                           string        `json:"cwd"`
	Git                           GitState      `json:"git"`
	ChangedPaths                  []ChangedPath `json:"changed_paths,omitempty"`
	DiffStat                      DiffStat      `json:"diff_stat"`
	DiffStatAvailable             bool          `json:"diff_stat_available,omitempty"`
	SubmoduleWorktreeStateIgnored bool          `json:"submodule_worktree_state_ignored,omitempty"`
	Truncated                     bool          `json:"truncated,omitempty"`
	Warning                       string        `json:"warning,omitempty"`
}

func (snapshot Snapshot) Identity() string {
	var builder strings.Builder
	fields := []string{
		snapshot.ProjectRoot,
		snapshot.CWD,
		strconv.FormatBool(snapshot.Git.Available),
		strconv.FormatBool(snapshot.Git.StatusAvailable),
		snapshot.Git.TopLevel,
		snapshot.Git.GitDir,
		snapshot.Git.CommonDir,
		snapshot.Git.Branch,
		snapshot.Git.Head,
		strconv.FormatBool(snapshot.Git.Detached),
		strconv.FormatBool(snapshot.Git.Unborn),
		strconv.FormatBool(snapshot.Git.Worktree),
		strconv.FormatBool(snapshot.Git.Dirty),
		snapshot.Git.UnavailableReason,
		strconv.Itoa(snapshot.DiffStat.Files),
		strconv.Itoa(snapshot.DiffStat.Additions),
		strconv.Itoa(snapshot.DiffStat.Deletions),
		strconv.Itoa(snapshot.DiffStat.BinaryFiles),
		strconv.FormatBool(snapshot.DiffStatAvailable),
		strconv.FormatBool(snapshot.SubmoduleWorktreeStateIgnored),
		strconv.FormatBool(snapshot.Truncated),
		snapshot.Warning,
	}
	for _, field := range fields {
		builder.WriteString(strconv.Quote(field))
		builder.WriteByte('\n')
	}
	for _, path := range snapshot.ChangedPaths {
		builder.WriteString(strconv.Quote(path.Status))
		builder.WriteByte('\t')
		builder.WriteString(strconv.Quote(path.Path))
		builder.WriteByte('\t')
		builder.WriteString(strconv.Quote(path.OriginalPath))
		builder.WriteByte('\n')
	}
	hash := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(hash[:])
}

type Observer struct {
	limits     Limits
	repository *Repository

	mu           sync.Mutex
	initialized  bool
	current      Snapshot
	emitted      string
	forcePending bool
}

func NewObserver(projectRoot, cwd string, limits Limits) *Observer {
	return &Observer{
		limits:     limits.normalized(),
		repository: NewRepository(projectRoot, cwd, limits),
	}
}

func (observer *Observer) Refresh(ctx context.Context) Snapshot {
	return observer.refresh(ctx, false)
}

// RefreshPending refreshes current state and guarantees one later
// PendingUpdate observation, even when bounded snapshot identity is unchanged.
func (observer *Observer) RefreshPending(ctx context.Context) Snapshot {
	return observer.refresh(ctx, true)
}

func (observer *Observer) refresh(ctx context.Context, forcePending bool) Snapshot {
	if observer == nil {
		return Snapshot{}
	}
	snapshot := observer.repository.Status(ctx).Snapshot
	observer.mu.Lock()
	observer.current = snapshot
	observer.initialized = true
	observer.forcePending = observer.forcePending || forcePending
	observer.mu.Unlock()
	return snapshot
}

func (observer *Observer) Current(ctx context.Context) Snapshot {
	if observer == nil {
		return Snapshot{}
	}
	observer.mu.Lock()
	if observer.initialized {
		snapshot := cloneSnapshot(observer.current)
		observer.mu.Unlock()
		return snapshot
	}
	observer.mu.Unlock()
	return observer.Refresh(ctx)
}

func (observer *Observer) RenderCurrent(ctx context.Context) string {
	if observer == nil {
		return ""
	}
	return RenderPrompt(observer.Current(ctx), observer.limits.PromptBytes)
}

func (observer *Observer) PendingUpdate(ctx context.Context) (Snapshot, bool) {
	if observer == nil {
		return Snapshot{}, false
	}
	snapshot := observer.Current(ctx)
	identity := snapshot.Identity()
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if identity == observer.emitted && !observer.forcePending {
		return Snapshot{}, false
	}
	observer.emitted = identity
	observer.forcePending = false
	return cloneSnapshot(snapshot), true
}

func captureSnapshot(ctx context.Context, projectRoot, cwd string, limits Limits) Snapshot {
	limits = limits.normalized()
	snapshot := Snapshot{
		ProjectRoot: filepath.Clean(projectRoot),
		CWD:         filepath.Clean(cwd),
	}
	commandCtx, cancel := context.WithTimeout(contextOrBackground(ctx), limits.Timeout)
	defer cancel()

	metadata, metadataErr := runGit(commandCtx, snapshot.ProjectRoot, limits.CommandBytes,
		"rev-parse", "--path-format=absolute", "--show-toplevel", "--absolute-git-dir", "--git-common-dir")
	if metadataErr != nil {
		snapshot.Git.UnavailableReason = gitUnavailableReason(metadataErr, metadata.stderr)
		if metadata.truncated {
			snapshot.Truncated = true
		}
		return snapshot
	}
	metadataLines := nonemptyLines(string(metadata.stdout))
	if len(metadataLines) < 3 {
		snapshot.Git.UnavailableReason = "Git metadata was incomplete"
		return snapshot
	}
	snapshot.Git.Available = true
	snapshot.Git.TopLevel = filepath.Clean(metadataLines[0])
	snapshot.Git.GitDir = filepath.Clean(metadataLines[1])
	snapshot.Git.CommonDir = filepath.Clean(metadataLines[2])
	snapshot.Git.Worktree = snapshot.Git.GitDir != snapshot.Git.CommonDir
	snapshot.SubmoduleWorktreeStateIgnored = true
	snapshot.Truncated = metadata.truncated
	filterOverrides, filterWarning, filtersSafe, filterTruncated := passiveFilterOverrides(
		commandCtx,
		snapshot.ProjectRoot,
		limits.CommandBytes,
	)
	snapshot.Warning = joinWarning(snapshot.Warning, filterWarning)
	snapshot.Truncated = snapshot.Truncated || filterTruncated

	branch, branchErr := runGitWithConfig(commandCtx, snapshot.ProjectRoot, limits.CommandBytes, filterOverrides,
		"symbolic-ref", "--quiet", "--short", "HEAD")
	if branchErr == nil {
		snapshot.Git.Branch = strings.TrimSpace(string(branch.stdout))
	}
	head, headErr := runGitWithConfig(commandCtx, snapshot.ProjectRoot, limits.CommandBytes, filterOverrides,
		"rev-parse", "--verify", "HEAD")
	if headErr == nil {
		snapshot.Git.Head = strings.TrimSpace(string(head.stdout))
		snapshot.Git.Detached = snapshot.Git.Branch == ""
	} else if snapshot.Git.Branch != "" && isUnbornHeadError(headErr, head.stderr) {
		snapshot.Git.Unborn = true
	} else {
		snapshot.Warning = joinWarning(
			snapshot.Warning,
			boundedWarning("Could not resolve Git HEAD", headErr, head.stderr),
		)
	}
	snapshot.Truncated = snapshot.Truncated || branch.truncated || head.truncated
	if !filtersSafe {
		return snapshot
	}

	status, statusErr := runGitWithConfig(commandCtx, snapshot.ProjectRoot, limits.CommandBytes, filterOverrides,
		"status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=dirty")
	if statusErr != nil {
		snapshot.Warning = joinWarning(
			snapshot.Warning,
			boundedWarning("Could not inspect Git status", statusErr, status.stderr),
		)
	} else {
		snapshot.ChangedPaths = parseStatus(status.stdout)
		snapshot.Git.StatusAvailable = true
		snapshot.Git.Dirty = len(snapshot.ChangedPaths) > 0
		if len(snapshot.ChangedPaths) > limits.ChangedPaths {
			snapshot.ChangedPaths = snapshot.ChangedPaths[:limits.ChangedPaths]
			snapshot.Truncated = true
		}
	}
	snapshot.Truncated = snapshot.Truncated || status.truncated

	diffStat, diffStatAvailable, diffWarning, diffTruncated := captureDiffStat(
		commandCtx,
		snapshot.ProjectRoot,
		snapshot.Git.Unborn,
		limits.CommandBytes,
		filterOverrides,
	)
	snapshot.DiffStat = diffStat
	snapshot.DiffStatAvailable = diffStatAvailable
	snapshot.Warning = joinWarning(snapshot.Warning, diffWarning)
	snapshot.Truncated = snapshot.Truncated || diffTruncated
	return snapshot
}

func captureDiffStat(
	ctx context.Context,
	root string,
	unborn bool,
	limit int,
	config []string,
) (DiffStat, bool, string, bool) {
	if !unborn {
		diff, err := runGitWithConfig(
			ctx,
			root,
			limit,
			config,
			"diff",
			"--no-ext-diff",
			"--no-textconv",
			"--ignore-submodules=dirty",
			"--numstat",
			"--no-renames",
			"-z",
			"HEAD",
		)
		if err != nil {
			return DiffStat{}, false,
				boundedWarning("Could not inspect Git diff stat", err, diff.stderr), diff.truncated
		}
		return parseDiffStat(diff.stdout), true, "", diff.truncated
	}

	// An unborn branch has no HEAD. Combine the index-to-empty and
	// worktree-to-index views so staged and unstaged changes are both visible.
	// Untracked paths remain represented by ChangedPaths without reading their
	// contents or implicitly treating their full size as a diff.
	staged, stagedErr := runGitWithConfig(
		ctx,
		root,
		limit,
		config,
		"diff",
		"--no-ext-diff",
		"--no-textconv",
		"--ignore-submodules=dirty",
		"--cached",
		"--numstat",
		"--no-renames",
		"-z",
	)
	unstaged, unstagedErr := runGitWithConfig(
		ctx,
		root,
		limit,
		config,
		"diff",
		"--no-ext-diff",
		"--no-textconv",
		"--ignore-submodules=dirty",
		"--numstat",
		"--no-renames",
		"-z",
	)
	warning := ""
	if stagedErr != nil {
		warning = joinWarning(
			warning,
			boundedWarning("Could not inspect staged Git diff stat", stagedErr, staged.stderr),
		)
	}
	if unstagedErr != nil {
		warning = joinWarning(
			warning,
			boundedWarning("Could not inspect unstaged Git diff stat", unstagedErr, unstaged.stderr),
		)
	}
	if stagedErr != nil || unstagedErr != nil {
		return DiffStat{}, false, warning, staged.truncated || unstaged.truncated
	}
	details := make(map[string]diffStatEntry)
	mergeDiffStatDetails(details, parseDiffStatDetails(staged.stdout))
	mergeDiffStatDetails(details, parseDiffStatDetails(unstaged.stdout))
	return summarizeDiffStat(details), true, warning, staged.truncated || unstaged.truncated
}

type diffStatEntry struct {
	additions int
	deletions int
	binary    bool
}

func RenderPrompt(snapshot Snapshot, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = defaultPromptBytes
	}
	var builder strings.Builder
	builder.WriteString("# Live workspace snapshot\n\n")
	builder.WriteString("This is a fresh deterministic observation. It supersedes older narrative or compacted claims ")
	builder.WriteString("about repository state. Inspect files or diffs with tools when more detail is needed.\n\n")
	builder.WriteString("Project root: ")
	builder.WriteString(snapshot.ProjectRoot)
	builder.WriteString("\nWorking directory: ")
	builder.WriteString(snapshot.CWD)
	if !snapshot.Git.Available {
		builder.WriteString("\nGit: unavailable")
		if snapshot.Git.UnavailableReason != "" {
			builder.WriteString(" (")
			builder.WriteString(snapshot.Git.UnavailableReason)
			builder.WriteString(")")
		}
	} else {
		builder.WriteString("\nGit top level: ")
		builder.WriteString(snapshot.Git.TopLevel)
		builder.WriteString("\nBranch: ")
		switch {
		case snapshot.Git.Unborn:
			builder.WriteString(snapshot.Git.Branch + " (unborn)")
		case snapshot.Git.Detached:
			builder.WriteString("detached HEAD")
		default:
			builder.WriteString(snapshot.Git.Branch)
		}
		builder.WriteString("\nHEAD: ")
		if snapshot.Git.Head == "" {
			builder.WriteString("unavailable")
		} else {
			builder.WriteString(snapshot.Git.Head)
		}
		builder.WriteString("\nWorktree: ")
		builder.WriteString(strconv.FormatBool(snapshot.Git.Worktree))
		if snapshot.SubmoduleWorktreeStateIgnored {
			builder.WriteString("\nSubmodule worktree state: not inspected (passive capture)")
		}
		builder.WriteString("\nStatus: ")
		if !snapshot.Git.StatusAvailable {
			builder.WriteString("unavailable")
		} else if snapshot.Git.Dirty {
			builder.WriteString("dirty")
		} else {
			builder.WriteString("clean")
		}
		if !snapshot.DiffStatAvailable {
			builder.WriteString("\nDiff stat: unavailable")
		} else {
			fmt.Fprintf(
				&builder,
				"\nDiff stat: %d file(s), +%d/-%d",
				snapshot.DiffStat.Files,
				snapshot.DiffStat.Additions,
				snapshot.DiffStat.Deletions,
			)
			if snapshot.DiffStat.BinaryFiles > 0 {
				fmt.Fprintf(&builder, ", %d binary", snapshot.DiffStat.BinaryFiles)
			}
		}
		if len(snapshot.ChangedPaths) > 0 {
			builder.WriteString("\nChanged paths:")
			for _, changed := range snapshot.ChangedPaths {
				builder.WriteString("\n- ")
				builder.WriteString(changed.Status)
				builder.WriteString(" ")
				builder.WriteString(strconv.Quote(changed.Path))
				if changed.OriginalPath != "" {
					builder.WriteString(" <- ")
					builder.WriteString(strconv.Quote(changed.OriginalPath))
				}
			}
		}
	}
	if snapshot.Truncated {
		builder.WriteString("\nSnapshot status: truncated to configured bounds")
	}
	if snapshot.Warning != "" {
		builder.WriteString("\nSnapshot warning: ")
		builder.WriteString(snapshot.Warning)
	}
	return boundPromptRecords(builder.String(), maxBytes)
}

type commandOutput struct {
	stdout    []byte
	stderr    []byte
	truncated bool
}

type limitedBuffer struct {
	buffer    bytes.Buffer
	remaining int
	truncated bool
}

func (buffer *limitedBuffer) Write(data []byte) (int, error) {
	written := len(data)
	if len(data) > buffer.remaining {
		data = data[:max(0, buffer.remaining)]
		buffer.truncated = true
	}
	if len(data) > 0 {
		_, _ = buffer.buffer.Write(data)
		buffer.remaining -= len(data)
	}
	return written, nil
}

func runGit(ctx context.Context, root string, limit int, args ...string) (commandOutput, error) {
	return runGitWithConfig(ctx, root, limit, nil, args...)
}

func runGitWithConfig(
	ctx context.Context,
	root string,
	limit int,
	config []string,
	args ...string,
) (commandOutput, error) {
	stdout := &limitedBuffer{remaining: limit}
	stderr := &limitedBuffer{remaining: min(limit, 4096)}
	commandArgs := []string{"-C", root, "-c", "core.fsmonitor=false"}
	for _, override := range config {
		commandArgs = append(commandArgs, "-c", override)
	}
	commandArgs = append(commandArgs, args...)
	command := exec.CommandContext(ctx, "git", commandArgs...)
	command.Env = sanitizedGitEnvironment()
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		err = ctxErr
	}
	return commandOutput{
		stdout:    append([]byte(nil), stdout.buffer.Bytes()...),
		stderr:    append([]byte(nil), stderr.buffer.Bytes()...),
		truncated: stdout.truncated || stderr.truncated,
	}, err
}

func passiveFilterOverrides(
	ctx context.Context,
	root string,
	limit int,
) ([]string, string, bool, bool) {
	result, err := runGit(ctx, root, limit, "config", "--null", "--name-only", "--get-regexp", "^filter\\.")
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && len(result.stderr) == 0 {
			return nil, "", true, result.truncated
		}
		return nil, boundedWarning("Could not inspect Git content-filter configuration", err, result.stderr),
			false, result.truncated
	}
	if result.truncated {
		return nil, "Git content-filter configuration exceeded the capture byte limit", false, true
	}

	if len(result.stdout) > 0 && result.stdout[len(result.stdout)-1] != 0 {
		return nil, "Git content-filter configuration returned an incomplete record", false, true
	}
	drivers := make(map[string]struct{})
	for _, rawKey := range completeNULRecords(result.stdout) {
		key := string(rawKey)
		lowerKey := strings.ToLower(key)
		if !strings.HasPrefix(lowerKey, "filter.") {
			continue
		}
		for _, suffix := range []string{".clean", ".process"} {
			if strings.HasSuffix(lowerKey, suffix) && len(key) > len("filter.")+len(suffix) {
				drivers[key[:len(key)-len(suffix)]] = struct{}{}
			}
		}
	}
	bases := make([]string, 0, len(drivers))
	for base := range drivers {
		bases = append(bases, base)
	}
	sort.Strings(bases)
	overrides := make([]string, 0, len(bases)*3)
	for _, base := range bases {
		if strings.Contains(base, "=") {
			return nil, "Git content-filter name cannot be passively overridden", false, false
		}
		overrides = append(overrides, base+".clean=", base+".process=", base+".required=false")
	}
	return overrides, "", true, false
}

func sanitizedGitEnvironment() []string {
	environment := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		upperKey := strings.ToUpper(key)
		if strings.HasPrefix(upperKey, "GIT_") || upperKey == "LC_ALL" {
			continue
		}
		environment = append(environment, entry)
	}
	return append(
		environment,
		"LC_ALL=C",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_LITERAL_PATHSPECS=1",
	)
}

func parseStatus(data []byte) []ChangedPath {
	records := completeNULRecords(data)
	paths := make([]ChangedPath, 0, len(records))
	for index := 0; index < len(records); index++ {
		record := records[index]
		if len(record) < 4 {
			continue
		}
		status := string(record[:2])
		path := string(record[3:])
		changed := ChangedPath{Path: path, Status: status}
		if strings.ContainsAny(status, "RC") {
			if index+1 >= len(records) {
				break
			}
			index++
			changed.OriginalPath = string(records[index])
		}
		paths = append(paths, changed)
	}
	sort.Slice(paths, func(left, right int) bool {
		if paths[left].Path != paths[right].Path {
			return paths[left].Path < paths[right].Path
		}
		if paths[left].OriginalPath != paths[right].OriginalPath {
			return paths[left].OriginalPath < paths[right].OriginalPath
		}
		return paths[left].Status < paths[right].Status
	})
	return paths
}

func completeNULRecords(data []byte) [][]byte {
	lastTerminator := bytes.LastIndexByte(data, 0)
	if lastTerminator < 0 {
		return nil
	}
	return bytes.Split(data[:lastTerminator], []byte{0})
}

func parseDiffStat(data []byte) DiffStat {
	return summarizeDiffStat(parseDiffStatDetails(data))
}

func parseDiffStatDetails(data []byte) map[string]diffStatEntry {
	details := make(map[string]diffStatEntry)
	for _, record := range bytes.Split(data, []byte{0}) {
		fields := bytes.SplitN(record, []byte{'\t'}, 3)
		if len(fields) != 3 {
			continue
		}
		entry := diffStatEntry{}
		if string(fields[0]) == "-" || string(fields[1]) == "-" {
			entry.binary = true
		} else {
			entry.additions, _ = strconv.Atoi(string(fields[0]))
			entry.deletions, _ = strconv.Atoi(string(fields[1]))
		}
		details[string(fields[2])] = entry
	}
	return details
}

func mergeDiffStatDetails(destination, source map[string]diffStatEntry) {
	for path, entry := range source {
		current := destination[path]
		current.additions += entry.additions
		current.deletions += entry.deletions
		current.binary = current.binary || entry.binary
		destination[path] = current
	}
}

func summarizeDiffStat(details map[string]diffStatEntry) DiffStat {
	stat := DiffStat{}
	for _, entry := range details {
		stat.Files++
		if entry.binary {
			stat.BinaryFiles++
			continue
		}
		stat.Additions += entry.additions
		stat.Deletions += entry.deletions
	}
	return stat
}

func nonemptyLines(value string) []string {
	var result []string
	for _, line := range strings.Split(value, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			result = append(result, line)
		}
	}
	return result
}

func gitUnavailableReason(err error, stderr []byte) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "Git inspection timed out"
	}
	message := strings.TrimSpace(string(stderr))
	if strings.Contains(strings.ToLower(message), "not a git repository") {
		return "not a Git worktree"
	}
	if message == "" {
		return "Git metadata unavailable"
	}
	return truncateUTF8(message, 512)
}

func isUnbornHeadError(err error, stderr []byte) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 128 &&
		strings.Contains(strings.ToLower(string(stderr)), "needed a single revision")
}

func boundedWarning(prefix string, err error, stderr []byte) string {
	detail := strings.TrimSpace(string(stderr))
	if detail == "" && err != nil {
		detail = err.Error()
	}
	if detail == "" {
		return prefix
	}
	return truncateUTF8(prefix+": "+detail, 1024)
}

func joinWarning(existing, next string) string {
	if existing == "" {
		return next
	}
	if next == "" {
		return existing
	}
	return truncateUTF8(existing+"; "+next, 2048)
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	snapshot.ChangedPaths = append([]ChangedPath(nil), snapshot.ChangedPaths...)
	return snapshot
}

func truncateUTF8(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	data := []byte(value[:limit])
	for len(data) > 0 && !utf8.Valid(data) {
		data = data[:len(data)-1]
	}
	return string(data)
}

func boundPromptRecords(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	const marker = "\nSnapshot status: prompt truncated to byte limit"
	if limit <= len(marker) {
		return truncateUTF8(strings.TrimPrefix(marker, "\n"), limit)
	}
	budget := limit - len(marker)
	var builder strings.Builder
	for _, record := range strings.SplitAfter(value, "\n") {
		if builder.Len()+len(record) > budget {
			break
		}
		builder.WriteString(record)
	}
	return strings.TrimRight(builder.String(), "\n") + marker
}
