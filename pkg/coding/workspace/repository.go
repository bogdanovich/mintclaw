package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	RepositoryStatusSchemaV1 = "mintclaw.repository_status.v1"
	RepositoryDiffSchemaV1   = "mintclaw.repository_diff.v1"
)

type DiffTargetKind string

const (
	DiffTargetCurrent DiffTargetKind = "current"
	DiffTargetBase    DiffTargetKind = "base"
	DiffTargetCommit  DiffTargetKind = "commit"
)

type DiffTarget struct {
	Kind DiffTargetKind `json:"kind"`
	Ref  string         `json:"ref,omitempty"`
}

type StatusResult struct {
	SchemaVersion string            `json:"schema_version"`
	Snapshot      Snapshot          `json:"snapshot"`
	BaselineID    string            `json:"baseline_id,omitempty"`
	Provenance    *ProvenanceResult `json:"provenance,omitempty"`
	Stale         bool              `json:"stale,omitempty"`
}

type DiffLine struct {
	Kind    string `json:"kind"`
	OldLine int    `json:"old_line,omitempty"`
	NewLine int    `json:"new_line,omitempty"`
	Text    string `json:"text"`
}

type DiffHunk struct {
	OldStart  int        `json:"old_start"`
	OldLines  int        `json:"old_lines"`
	NewStart  int        `json:"new_start"`
	NewLines  int        `json:"new_lines"`
	Header    string     `json:"header,omitempty"`
	Lines     []DiffLine `json:"lines,omitempty"`
	Truncated bool       `json:"truncated,omitempty"`
}

type DiffFile struct {
	Path             string         `json:"path"`
	OriginalPath     string         `json:"original_path,omitempty"`
	Status           string         `json:"status"`
	Binary           bool           `json:"binary,omitempty"`
	Submodule        bool           `json:"submodule,omitempty"`
	Symlink          bool           `json:"symlink,omitempty"`
	Omitted          string         `json:"omitted,omitempty"`
	Additions        int            `json:"additions,omitempty"`
	Deletions        int            `json:"deletions,omitempty"`
	Hunks            []DiffHunk     `json:"hunks,omitempty"`
	Truncated        bool           `json:"truncated,omitempty"`
	Provenance       ProvenanceKind `json:"provenance,omitempty"`
	ProvenanceReason string         `json:"provenance_reason,omitempty"`
}

type DiffResult struct {
	SchemaVersion       string            `json:"schema_version"`
	Target              DiffTarget        `json:"target"`
	ResolvedRevision    string            `json:"resolved_revision,omitempty"`
	MergeBase           string            `json:"merge_base,omitempty"`
	RepositoryAvailable bool              `json:"repository_available"`
	Head                string            `json:"head,omitempty"`
	Branch              string            `json:"branch,omitempty"`
	Generation          string            `json:"generation,omitempty"`
	Files               []DiffFile        `json:"files,omitempty"`
	Additions           int               `json:"additions,omitempty"`
	Deletions           int               `json:"deletions,omitempty"`
	BinaryFiles         int               `json:"binary_files,omitempty"`
	Truncated           bool              `json:"truncated,omitempty"`
	Stale               bool              `json:"stale,omitempty"`
	UnavailableReason   string            `json:"unavailable_reason,omitempty"`
	Warning             string            `json:"warning,omitempty"`
	BaselineID          string            `json:"baseline_id,omitempty"`
	Provenance          *ProvenanceResult `json:"provenance,omitempty"`
}

// Repository is the sole passive Git evidence boundary for one coding
// project. It owns command construction, environment sanitization, bounds,
// and concurrency; callers select typed operations rather than assembling Git
// commands.
type Repository struct {
	projectRoot string
	cwd         string
	limits      Limits
	slots       chan struct{}
	baseline    *RepositoryBaseline
}

func NewRepositoryWithBaseline(
	projectRoot, cwd string,
	limits Limits,
	baseline RepositoryBaseline,
) (*Repository, error) {
	if err := baseline.Validate(); err != nil {
		return nil, err
	}
	repository := NewRepository(projectRoot, cwd, limits)
	canonicalProjectRoot, err := filepath.EvalSymlinks(repository.projectRoot)
	if err != nil {
		return nil, fmt.Errorf("repository baseline authority: resolve project root: %w", err)
	}
	canonicalProjectRoot = filepath.Clean(canonicalProjectRoot)
	if baseline.RepositoryAvailable && canonicalProjectRoot != baseline.TopLevel {
		return nil, fmt.Errorf(
			"repository baseline authority mismatch: project root %q does not match baseline root %q",
			canonicalProjectRoot,
			baseline.TopLevel,
		)
	}
	copy := baseline
	copy.Paths = append([]BaselinePath(nil), baseline.Paths...)
	repository.baseline = &copy
	return repository, nil
}

func NewRepository(projectRoot, cwd string, limits Limits) *Repository {
	limits = limits.normalized()
	return &Repository{
		projectRoot: filepath.Clean(projectRoot),
		cwd:         filepath.Clean(cwd),
		limits:      limits,
		slots:       make(chan struct{}, limits.ConcurrentOperations),
	}
}

func (repository *Repository) Status(ctx context.Context) StatusResult {
	result := StatusResult{SchemaVersion: RepositoryStatusSchemaV1}
	if repository == nil {
		result.Snapshot.Git.UnavailableReason = "repository evidence service is unavailable"
		return result
	}
	if !repository.acquire(ctx) {
		result.Snapshot.Git.UnavailableReason = contextError(ctx).Error()
		return result
	}
	func() {
		defer repository.release()
		result.Snapshot = captureSnapshot(ctx, repository.projectRoot, repository.cwd, repository.limits)
	}()
	repository.attachStatusProvenance(ctx, &result)
	return result
}

func (repository *Repository) Diff(ctx context.Context, target DiffTarget) DiffResult {
	result := repository.diff(ctx, target)
	if result.Target.Kind == DiffTargetCurrent {
		repository.attachDiffProvenance(ctx, &result)
	}
	return result
}

func (repository *Repository) diff(ctx context.Context, target DiffTarget) DiffResult {
	result := DiffResult{SchemaVersion: RepositoryDiffSchemaV1, Target: target}
	if repository == nil {
		result.UnavailableReason = "repository evidence service is unavailable"
		return result
	}
	if !repository.acquire(ctx) {
		result.UnavailableReason = contextError(ctx).Error()
		return result
	}
	defer repository.release()

	commandCtx, cancel := context.WithTimeout(contextOrBackground(ctx), repository.limits.Timeout)
	defer cancel()
	before := captureSnapshot(commandCtx, repository.projectRoot, repository.cwd, repository.limits)
	result.RepositoryAvailable = before.Git.Available
	result.Head = before.Git.Head
	result.Branch = before.Git.Branch
	result.Generation = before.Identity()
	if !before.Git.Available {
		result.UnavailableReason = before.Git.UnavailableReason
		result.Truncated = before.Truncated
		return result
	}
	if !before.Git.StatusAvailable {
		result.UnavailableReason = "Git status is unavailable"
		result.Warning = before.Warning
		result.Truncated = before.Truncated
		return result
	}
	if target.Kind == "" {
		result.Target.Kind = DiffTargetCurrent
	}
	filters, warning, safe, truncated := passiveFilterOverrides(
		commandCtx,
		before.ProjectRoot,
		repository.limits.CommandBytes,
	)
	result.Warning = joinWarning(before.Warning, warning)
	result.Truncated = before.Truncated || truncated
	if !safe {
		result.UnavailableReason = "Git content-filter configuration could not be made passive"
		return result
	}

	spec, unavailable := repository.resolveDiffSpec(commandCtx, before, result.Target, filters)
	if unavailable != "" {
		result.UnavailableReason = unavailable
		return result
	}
	result.ResolvedRevision = spec.resolvedRevision
	result.MergeBase = spec.mergeBase

	paths, pathsTruncated, pathsWarning := repository.diffPaths(commandCtx, before, spec, filters)
	result.Truncated = result.Truncated || pathsTruncated
	result.Warning = joinWarning(result.Warning, pathsWarning)
	if len(paths) > repository.limits.DiffFiles {
		paths = paths[:repository.limits.DiffFiles]
		result.Truncated = true
	}

	budget := diffBudget{
		bytes: repository.limits.DiffBytes, hunks: repository.limits.DiffHunks,
		lines: repository.limits.DiffLines,
	}
	for _, changed := range paths {
		file := repository.diffFile(commandCtx, before, spec, filters, changed, &budget)
		result.Files = append(result.Files, file)
		result.Additions += file.Additions
		result.Deletions += file.Deletions
		if file.Binary {
			result.BinaryFiles++
		}
		result.Truncated = result.Truncated || file.Truncated
		if budget.exhausted() {
			result.Truncated = true
			break
		}
	}

	after := captureSnapshot(commandCtx, repository.projectRoot, repository.cwd, repository.limits)
	result.Stale = before.Identity() != after.Identity()
	if result.Stale {
		result.Warning = joinWarning(result.Warning, "repository changed while diff evidence was captured")
	}
	if err := commandCtx.Err(); err != nil {
		result.Truncated = true
		result.Warning = joinWarning(result.Warning, "repository diff capture: "+err.Error())
	}
	return result
}

func (repository *Repository) attachStatusProvenance(ctx context.Context, result *StatusResult) {
	if repository == nil || repository.baseline == nil || result == nil {
		return
	}
	result.BaselineID = repository.baseline.BaselineID
	provenance, err := repository.Provenance(ctx, *repository.baseline, time.Now().UTC())
	if err != nil {
		provenance = unavailableProvenance(repository.baseline.BaselineID, "provenance refresh is unavailable")
	}
	if result.Snapshot.Identity() != provenance.CurrentGeneration {
		result.Stale = true
		provenance = unavailableProvenance(
			repository.baseline.BaselineID,
			"repository changed between status and provenance observations",
		)
	}
	result.Provenance = &provenance
}

func (repository *Repository) attachDiffProvenance(ctx context.Context, result *DiffResult) {
	if repository == nil || repository.baseline == nil || result == nil {
		return
	}
	result.BaselineID = repository.baseline.BaselineID
	provenance, err := repository.Provenance(ctx, *repository.baseline, time.Now().UTC())
	if err != nil {
		provenance = unavailableProvenance(repository.baseline.BaselineID, "provenance refresh is unavailable")
	}
	if result.Stale || result.Generation != provenance.CurrentGeneration {
		result.Stale = true
		provenance = unavailableProvenance(
			repository.baseline.BaselineID,
			"repository changed between diff and provenance observations",
		)
	}
	result.Provenance = &provenance
	byPath := make(map[string]ProvenancePath, len(provenance.Paths))
	for _, path := range provenance.Paths {
		byPath[evidencePathKey(path.Status, path.OriginalPath, path.Path)] = path
	}
	for index := range result.Files {
		path, ok := byPath[evidencePathKey(
			result.Files[index].Status,
			result.Files[index].OriginalPath,
			result.Files[index].Path,
		)]
		if !ok {
			result.Files[index].Provenance = ProvenanceIndeterminate
			result.Files[index].ProvenanceReason = provenance.Reason
			if result.Files[index].ProvenanceReason == "" {
				result.Files[index].ProvenanceReason = "bounded evidence does not cover this diff path"
			}
			continue
		}
		result.Files[index].Provenance = path.Provenance
		result.Files[index].ProvenanceReason = path.Reason
	}
}

func evidencePathKey(status, originalPath, path string) string {
	status = strings.TrimSpace(status)
	if status != "??" && status != "" {
		status = status[:1]
	}
	return status + "\x00" + originalPath + "\x00" + path
}

func unavailableProvenance(baselineID, reason string) ProvenanceResult {
	return ProvenanceResult{BaselineID: baselineID, Indeterminate: true, Reason: reason}
}

func (repository *Repository) acquire(ctx context.Context) bool {
	select {
	case repository.slots <- struct{}{}:
		return true
	case <-contextOrBackground(ctx).Done():
		return false
	}
}

func (repository *Repository) release() {
	<-repository.slots
}

func contextError(ctx context.Context) error {
	if ctx == nil || ctx.Err() == nil {
		return context.Canceled
	}
	return ctx.Err()
}

type diffSpec struct {
	kind             DiffTargetKind
	resolvedRevision string
	mergeBase        string
	fromRevision     string
	toRevision       string
	rootCommit       bool
	unborn           bool
}

func (spec diffSpec) appendRevisions(args []string) []string {
	if spec.fromRevision != "" {
		args = append(args, spec.fromRevision)
	}
	if spec.toRevision != "" {
		args = append(args, spec.toRevision)
	}
	return args
}

func (repository *Repository) resolveDiffSpec(
	ctx context.Context,
	snapshot Snapshot,
	target DiffTarget,
	filters []string,
) (diffSpec, string) {
	spec := diffSpec{kind: target.Kind, unborn: snapshot.Git.Unborn}
	switch target.Kind {
	case DiffTargetCurrent:
		if !snapshot.Git.Unborn {
			if snapshot.Git.Head == "" {
				return spec, "current comparison requires a resolved HEAD"
			}
			spec.fromRevision = snapshot.Git.Head
		}
		return spec, ""
	case DiffTargetBase:
		if snapshot.Git.Head == "" {
			return spec, "a local base comparison requires a resolved HEAD"
		}
		resolved, reason := repository.resolveCommit(ctx, snapshot.ProjectRoot, target.Ref, filters)
		if reason != "" {
			return spec, reason
		}
		mergeBase, err := runGitWithConfig(ctx, snapshot.ProjectRoot, repository.limits.CommandBytes, filters,
			"merge-base", snapshot.Git.Head, resolved)
		if err != nil {
			return spec, boundedWarning("could not compute local merge base", err, mergeBase.stderr)
		}
		spec.resolvedRevision = resolved
		spec.mergeBase = strings.TrimSpace(string(mergeBase.stdout))
		if spec.mergeBase == "" {
			return spec, "local base has no merge base with HEAD"
		}
		spec.fromRevision = spec.mergeBase
		return spec, ""
	case DiffTargetCommit:
		resolved, reason := repository.resolveCommit(ctx, snapshot.ProjectRoot, target.Ref, filters)
		if reason != "" {
			return spec, reason
		}
		parents, parentReason := repository.commitParents(ctx, snapshot.ProjectRoot, resolved, filters)
		if parentReason != "" {
			return spec, parentReason
		}
		if len(parents) > 1 {
			return spec, "merge commit diff is ambiguous; select a local base or one parent explicitly"
		}
		if len(parents) == 1 {
			_, parentErr := runGitWithConfig(
				ctx,
				snapshot.ProjectRoot,
				repository.limits.CommandBytes,
				filters,
				"cat-file", "-e", parents[0]+"^{commit}",
			)
			if parentErr != nil {
				return spec, "commit parent is not available locally at a shallow or partial-clone boundary"
			}
			spec.fromRevision = parents[0]
		} else {
			spec.rootCommit = true
		}
		spec.resolvedRevision = resolved
		spec.toRevision = resolved
		return spec, ""
	default:
		return spec, fmt.Sprintf("unsupported diff target %q", target.Kind)
	}
}

func (repository *Repository) commitParents(
	ctx context.Context,
	root string,
	commit string,
	filters []string,
) ([]string, string) {
	raw, err := runGitWithConfig(
		ctx,
		root,
		repository.limits.CommandBytes,
		filters,
		"cat-file", "commit", commit,
	)
	if err != nil {
		return nil, boundedWarning("could not inspect raw commit header", err, raw.stderr)
	}
	headerEnd := bytes.Index(raw.stdout, []byte("\n\n"))
	if headerEnd < 0 && raw.truncated {
		return nil, "raw commit header exceeded the command byte limit"
	}
	if headerEnd < 0 {
		return nil, "raw commit object has no header terminator"
	}
	var parents []string
	for _, line := range strings.Split(string(raw.stdout[:headerEnd]), "\n") {
		if parent, found := strings.CutPrefix(line, "parent "); found {
			parent = strings.TrimSpace(parent)
			if parent == "" || strings.ContainsAny(parent, "\x00\r\n ") {
				return nil, "raw commit header contains an invalid parent"
			}
			parents = append(parents, parent)
		}
	}
	return parents, ""
}

func (repository *Repository) resolveCommit(
	ctx context.Context,
	root string,
	ref string,
	filters []string,
) (string, string) {
	ref = strings.TrimSpace(ref)
	if ref == "" || len(ref) > 1024 || strings.ContainsAny(ref, "\x00\r\n") {
		return "", "diff target ref is missing or invalid"
	}
	resolved, err := runGitWithConfig(ctx, root, repository.limits.CommandBytes, filters,
		"rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", boundedWarning("local diff target is unavailable", err, resolved.stderr)
	}
	oid := strings.TrimSpace(string(resolved.stdout))
	if oid == "" || strings.ContainsAny(oid, "\r\n\x00") {
		return "", "local diff target did not resolve to one commit"
	}
	return oid, ""
}

func (repository *Repository) diffPaths(
	ctx context.Context,
	snapshot Snapshot,
	spec diffSpec,
	filters []string,
) ([]ChangedPath, bool, string) {
	if spec.kind == DiffTargetCurrent && spec.unborn {
		return repository.unbornWorktreePaths(ctx, snapshot, filters)
	}
	args := []string{
		"diff", "--name-status", "-z", "--find-renames", "--no-ext-diff", "--no-textconv",
		"--ignore-submodules=dirty",
	}
	if spec.rootCommit {
		args = []string{
			"diff-tree", "--root", "--no-commit-id", "-r", "-z", "--name-status", "--find-renames",
			spec.resolvedRevision,
		}
	} else {
		args = spec.appendRevisions(args)
	}
	output, err := runGitWithConfig(ctx, snapshot.ProjectRoot, repository.limits.CommandBytes, filters, args...)
	if err != nil {
		return nil, output.truncated, boundedWarning("could not enumerate diff paths", err, output.stderr)
	}
	paths := parseNameStatus(output.stdout)
	truncated := output.truncated
	warning := ""
	if spec.kind == DiffTargetCurrent || spec.kind == DiffTargetBase {
		untracked, untrackedErr := runGitWithConfig(
			ctx,
			snapshot.ProjectRoot,
			repository.limits.CommandBytes,
			filters,
			"ls-files", "--others", "--exclude-standard", "-z",
		)
		truncated = truncated || untracked.truncated
		if untrackedErr != nil {
			warning = joinWarning(
				warning,
				boundedWarning("could not enumerate untracked paths", untrackedErr, untracked.stderr),
			)
		} else {
			for _, path := range completeNULRecords(untracked.stdout) {
				if len(path) > 0 {
					paths = append(paths, ChangedPath{Path: string(path), Status: "??"})
				}
			}
		}
	}
	return uniqueChangedPaths(paths), truncated, warning
}

func (repository *Repository) unbornWorktreePaths(
	ctx context.Context,
	snapshot Snapshot,
	filters []string,
) ([]ChangedPath, bool, string) {
	status, err := runGitWithConfig(
		ctx,
		snapshot.ProjectRoot,
		repository.limits.CommandBytes,
		filters,
		"status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=dirty",
	)
	if err != nil {
		return nil, status.truncated, boundedWarning(
			"could not enumerate current paths on unborn branch",
			err,
			status.stderr,
		)
	}
	paths := parseStatus(status.stdout)
	rootHandle, rootErr := os.OpenRoot(snapshot.Git.TopLevel)
	if rootErr != nil {
		return nil, status.truncated, "repository root could not be pinned"
	}
	defer func() { _ = rootHandle.Close() }()
	current := make([]ChangedPath, 0, len(paths))
	warning := ""
	for _, path := range paths {
		if path.Status == "??" {
			current = append(current, path)
			continue
		}
		if len(path.Status) >= 2 && path.Status[1] == 'D' {
			continue
		}
		inspection := inspectPassivePath(rootHandle, path.Path)
		if inspection.reason != "" {
			warning = joinWarning(warning, inspection.reason)
		}
		if !inspection.exists {
			continue
		}
		current = append(current, ChangedPath{Path: path.Path, Status: "A"})
	}
	return uniqueChangedPaths(current), status.truncated, warning
}

func uniqueChangedPaths(paths []ChangedPath) []ChangedPath {
	seen := make(map[string]struct{}, len(paths))
	result := make([]ChangedPath, 0, len(paths))
	for _, path := range paths {
		key := path.Status + "\x00" + path.OriginalPath + "\x00" + path.Path
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, path)
	}
	return result
}

func parseNameStatus(data []byte) []ChangedPath {
	records := completeNULRecords(data)
	paths := make([]ChangedPath, 0, len(records)/2)
	for index := 0; index < len(records); {
		if len(records[index]) == 0 {
			index++
			continue
		}
		status := string(records[index])
		index++
		if index >= len(records) || len(records[index]) == 0 {
			break
		}
		first := string(records[index])
		index++
		changed := ChangedPath{Path: first, Status: status}
		if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
			if index >= len(records) || len(records[index]) == 0 {
				break
			}
			changed.OriginalPath = first
			changed.Path = string(records[index])
			index++
		}
		paths = append(paths, changed)
	}
	return paths
}

type diffBudget struct {
	bytes int
	hunks int
	lines int
}

func (budget diffBudget) exhausted() bool {
	return budget.bytes <= 0 || budget.hunks <= 0 || budget.lines <= 0
}

func (repository *Repository) diffFile(
	ctx context.Context,
	snapshot Snapshot,
	spec diffSpec,
	filters []string,
	changed ChangedPath,
	budget *diffBudget,
) DiffFile {
	file := DiffFile{Path: changed.Path, OriginalPath: changed.OriginalPath, Status: changed.Status}
	if budget.exhausted() {
		file.Truncated = true
		file.Omitted = "diff budget exhausted"
		return file
	}
	if strings.HasPrefix(changed.Status, "??") || (spec.kind == DiffTargetCurrent && spec.unborn) {
		return repository.untrackedOrUnbornFile(snapshot.Git.TopLevel, file, budget)
	}

	args := []string{
		"diff", "--patch", "--no-color", "--no-ext-diff", "--no-textconv",
		"--ignore-submodules=dirty", "--find-renames", "--unified=3",
	}
	if spec.rootCommit {
		args = []string{
			"diff-tree", "--root", "--no-commit-id", "-r", "--patch", "--no-color", "--no-ext-diff",
			"--no-textconv", "--ignore-submodules=dirty", "--find-renames", "--unified=3",
			spec.resolvedRevision,
		}
	} else {
		args = spec.appendRevisions(args)
	}
	args = append(args, "--")
	if changed.OriginalPath != "" {
		args = append(args, changed.OriginalPath)
	}
	args = append(args, changed.Path)
	limit := min(repository.limits.CommandBytes, budget.bytes)
	output, err := runGitWithConfig(ctx, snapshot.ProjectRoot, limit, filters, args...)
	if err != nil {
		file.Omitted = boundedWarning("diff unavailable", err, output.stderr)
		file.Truncated = output.truncated
		return file
	}
	budget.bytes -= len(output.stdout)
	file = parsePatchFile(file, output.stdout, repository.limits.DiffLineBytes, budget)
	file.Truncated = file.Truncated || output.truncated
	return file
}

func (repository *Repository) untrackedOrUnbornFile(root string, file DiffFile, budget *diffBudget) DiffFile {
	content, info, truncated, reason := readPassiveRegularFile(root, file.Path,
		min(repository.limits.UntrackedBytes, budget.bytes))
	if reason != "" {
		file.Omitted = reason
		file.Symlink = info != nil && info.Mode()&os.ModeSymlink != 0
		return file
	}
	budget.bytes -= len(content)
	file.Truncated = truncated
	if bytes.IndexByte(content, 0) >= 0 {
		file.Binary = true
		return file
	}
	hunk := DiffHunk{OldStart: 0, NewStart: 1, NewLines: countContentLines(content), Header: "new file"}
	newLine := 1
	for _, raw := range splitContentLines(content) {
		if budget.lines <= 0 {
			hunk.Truncated = true
			file.Truncated = true
			break
		}
		line, lineTruncated := boundedDiffLine(raw, repository.limits.DiffLineBytes)
		hunk.Lines = append(hunk.Lines, DiffLine{Kind: "addition", NewLine: newLine, Text: line})
		file.Additions++
		newLine++
		budget.lines--
		file.Truncated = file.Truncated || lineTruncated
	}
	if len(hunk.Lines) > 0 && budget.hunks > 0 {
		file.Hunks = append(file.Hunks, hunk)
		budget.hunks--
	} else if len(content) > 0 {
		file.Truncated = true
	}
	return file
}

func readPassiveRegularFile(root, relative string, limit int) ([]byte, os.FileInfo, bool, string) {
	if limit <= 0 {
		return nil, nil, true, "untracked content byte budget exhausted"
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return nil, nil, false, "repository root could not be pinned"
	}
	defer func() { _ = rootHandle.Close() }()
	inspection := inspectPassivePath(rootHandle, relative)
	if inspection.reason != "" {
		return nil, inspection.info, false, inspection.reason
	}
	if !inspection.exists {
		if inspection.symlink {
			return nil, inspection.info, false, "symlink content is not followed"
		}
		return nil, nil, false, "untracked path is unavailable"
	}
	info := inspection.info
	if inspection.symlink {
		return nil, info, false, "symlink content is not followed"
	}
	if !info.Mode().IsRegular() {
		return nil, info, false, "untracked path is not a regular file"
	}
	file, err := rootHandle.Open(inspection.clean)
	if err != nil {
		return nil, info, false, "untracked file could not be opened"
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return nil, info, false, "untracked path changed before it could be read"
	}
	content, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, info, false, "untracked file could not be read"
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(opened, after) || opened.Size() != after.Size() ||
		opened.ModTime() != after.ModTime() {
		return nil, info, false, "untracked path changed while it was read"
	}
	if len(content) > limit {
		return content[:limit], info, true, ""
	}
	return content, info, false, ""
}

type passivePathInspection struct {
	clean   string
	info    os.FileInfo
	exists  bool
	symlink bool
	reason  string
}

func inspectPassivePath(rootHandle *os.Root, relative string) passivePathInspection {
	clean := filepath.Clean(relative)
	if clean == "." || filepath.IsAbs(clean) || clean == ".." ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return passivePathInspection{reason: "path is outside repository authority"}
	}
	components := strings.Split(filepath.ToSlash(clean), "/")
	for index := range components {
		prefix := filepath.FromSlash(strings.Join(components[:index+1], "/"))
		info, statErr := rootHandle.Lstat(prefix)
		if errors.Is(statErr, os.ErrNotExist) {
			return passivePathInspection{clean: clean}
		}
		if statErr != nil {
			return passivePathInspection{clean: clean, reason: "path could not be inspected safely"}
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return passivePathInspection{
				clean: clean, info: info, exists: index == len(components)-1, symlink: true,
			}
		}
		if index == len(components)-1 {
			return passivePathInspection{clean: clean, info: info, exists: true}
		}
	}
	return passivePathInspection{clean: clean}
}

var hunkHeaderPattern = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(?: ?(.*))?$`)

func parsePatchFile(file DiffFile, patch []byte, lineLimit int, budget *diffBudget) DiffFile {
	if bytes.Contains(patch, []byte("Binary files ")) || bytes.Contains(patch, []byte("GIT binary patch")) {
		file.Binary = true
		return file
	}
	file.Submodule = bytes.Contains(patch, []byte("Subproject commit "))
	var current *DiffHunk
	oldLine, newLine := 0, 0
	for _, raw := range bytes.Split(patch, []byte{'\n'}) {
		text := string(raw)
		match := hunkHeaderPattern.FindStringSubmatch(text)
		if match != nil {
			if budget.hunks <= 0 {
				file.Truncated = true
				break
			}
			oldStart, _ := strconv.Atoi(match[1])
			oldCount := parseOptionalCount(match[2])
			newStart, _ := strconv.Atoi(match[3])
			newCount := parseOptionalCount(match[4])
			file.Hunks = append(file.Hunks, DiffHunk{
				OldStart: oldStart, OldLines: oldCount,
				NewStart: newStart, NewLines: newCount, Header: match[5],
			})
			current = &file.Hunks[len(file.Hunks)-1]
			oldLine, newLine = oldStart, newStart
			budget.hunks--
			continue
		}
		if current == nil || len(text) == 0 || text[0] == '\\' {
			continue
		}
		if budget.lines <= 0 {
			current.Truncated = true
			file.Truncated = true
			break
		}
		line := DiffLine{}
		switch text[0] {
		case ' ':
			line.Kind, line.OldLine, line.NewLine = "context", oldLine, newLine
			oldLine++
			newLine++
		case '-':
			line.Kind, line.OldLine = "deletion", oldLine
			oldLine++
			file.Deletions++
		case '+':
			line.Kind, line.NewLine = "addition", newLine
			newLine++
			file.Additions++
		default:
			continue
		}
		var lineTruncated bool
		line.Text, lineTruncated = boundedDiffLine(text[1:], lineLimit)
		file.Truncated = file.Truncated || lineTruncated
		current.Lines = append(current.Lines, line)
		budget.lines--
	}
	return file
}

func parseOptionalCount(value string) int {
	if value == "" {
		return 1
	}
	count, _ := strconv.Atoi(value)
	return count
}

func boundedDiffLine(value string, limit int) (string, bool) {
	if len(value) <= limit {
		return value, false
	}
	return truncateUTF8(value, limit), true
}

func splitContentLines(content []byte) []string {
	if len(content) == 0 {
		return nil
	}
	lines := strings.Split(string(content), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func countContentLines(content []byte) int {
	if len(content) == 0 {
		return 0
	}
	count := bytes.Count(content, []byte{'\n'})
	if content[len(content)-1] != '\n' {
		count++
	}
	return count
}
