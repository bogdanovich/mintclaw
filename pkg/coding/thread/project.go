package thread

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// ProjectKind identifies the root used to match coding threads.
type ProjectKind string

const (
	ProjectKindDirectory   ProjectKind = "directory"
	ProjectKindGitWorktree ProjectKind = "git_worktree"

	gitOriginMaxBytes = 2048
	gitRefMaxBytes    = 1024
)

// ProjectIdentity is a restart-stable snapshot of the invocation project.
// ProjectKey intentionally keys Git projects by worktree root, so separate
// worktrees remain separate projects even when they share GitCommonDir.
type ProjectIdentity struct {
	Kind            ProjectKind `json:"kind"`
	ProjectKey      string      `json:"project_key"`
	ProjectRoot     string      `json:"project_root"`
	InvocationCWD   string      `json:"invocation_cwd"`
	GitWorktreeRoot string      `json:"git_worktree_root,omitempty"`
	GitCommonDir    string      `json:"git_common_dir,omitempty"`
	GitOrigin       string      `json:"git_origin,omitempty"`
	GitBranch       string      `json:"git_branch,omitempty"`
	GitHead         string      `json:"git_head,omitempty"`
}

// Validate checks persisted project identity without consulting current disk
// state. Availability and movement are inspected separately on resume.
func (p ProjectIdentity) Validate() error {
	switch p.Kind {
	case ProjectKindDirectory:
		if p.GitWorktreeRoot != "" || p.GitCommonDir != "" || p.GitOrigin != "" ||
			p.GitBranch != "" || p.GitHead != "" {
			return fmt.Errorf("non-Git project contains Git metadata")
		}
	case ProjectKindGitWorktree:
		if p.GitWorktreeRoot == "" || p.GitCommonDir == "" || p.ProjectRoot != p.GitWorktreeRoot {
			return fmt.Errorf("git project requires matching worktree root and common directory")
		}
	default:
		return fmt.Errorf("unsupported kind %q", p.Kind)
	}
	if !filepath.IsAbs(p.ProjectRoot) || filepath.Clean(p.ProjectRoot) != p.ProjectRoot {
		return fmt.Errorf("project root must be a clean absolute path")
	}
	if !filepath.IsAbs(p.InvocationCWD) || filepath.Clean(p.InvocationCWD) != p.InvocationCWD {
		return fmt.Errorf("invocation cwd must be a clean absolute path")
	}
	inside, err := filepath.Rel(p.ProjectRoot, p.InvocationCWD)
	if err != nil || (inside != "." && !filepath.IsLocal(inside)) {
		return fmt.Errorf("invocation cwd must be inside project root")
	}
	if p.ProjectKey != projectKey(p.Kind, p.ProjectRoot) {
		return fmt.Errorf("project key does not match canonical root")
	}
	if p.GitCommonDir != "" && !filepath.IsAbs(p.GitCommonDir) {
		return fmt.Errorf("git common directory must be absolute")
	}
	if p.GitOrigin != strings.TrimSpace(p.GitOrigin) || !utf8.ValidString(p.GitOrigin) ||
		len(p.GitOrigin) > gitOriginMaxBytes {
		return fmt.Errorf("git origin must be valid UTF-8 within %d bytes", gitOriginMaxBytes)
	}
	if p.GitBranch != strings.TrimSpace(p.GitBranch) || !utf8.ValidString(p.GitBranch) ||
		len(p.GitBranch) > gitRefMaxBytes {
		return fmt.Errorf("git branch must be valid UTF-8 within %d bytes", gitRefMaxBytes)
	}
	if p.GitHead != "" {
		if (len(p.GitHead) != 40 && len(p.GitHead) != 64) || !isHex(p.GitHead) {
			return fmt.Errorf("git HEAD must be a 40- or 64-character object ID")
		}
	}
	return nil
}

// ResolveProject resolves cwd and Git observations without writing to either
// the project or MintClaw state.
func ResolveProject(ctx context.Context, cwd string) (ProjectIdentity, error) {
	canonicalCWD, err := canonicalExistingDirectory(cwd)
	if err != nil {
		return ProjectIdentity{}, fmt.Errorf("resolve coding project cwd: %w", err)
	}

	worktreeRoot, git, err := gitWorktreeRoot(ctx, canonicalCWD)
	if err != nil {
		return ProjectIdentity{}, err
	}
	if !git {
		identity := ProjectIdentity{
			Kind:          ProjectKindDirectory,
			ProjectRoot:   canonicalCWD,
			InvocationCWD: canonicalCWD,
		}
		identity.ProjectKey = projectKey(identity.Kind, identity.ProjectRoot)
		return identity, identity.Validate()
	}

	worktreeRoot, err = canonicalExistingDirectory(resolveGitPath(canonicalCWD, worktreeRoot))
	if err != nil {
		return ProjectIdentity{}, fmt.Errorf("resolve Git worktree root: %w", err)
	}
	commonDir, err := gitRequired(ctx, canonicalCWD, "rev-parse", "--git-common-dir")
	if err != nil {
		return ProjectIdentity{}, err
	}
	commonDir, err = canonicalExistingDirectory(resolveGitPath(canonicalCWD, commonDir))
	if err != nil {
		return ProjectIdentity{}, fmt.Errorf("resolve Git common directory: %w", err)
	}
	origin, err := gitOptional(ctx, canonicalCWD, 1, "config", "--get", "remote.origin.url")
	if err != nil {
		return ProjectIdentity{}, err
	}
	branch, err := gitOptional(ctx, canonicalCWD, 1, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return ProjectIdentity{}, err
	}
	head, err := gitHead(ctx, canonicalCWD)
	if err != nil {
		return ProjectIdentity{}, err
	}

	identity := ProjectIdentity{
		Kind:            ProjectKindGitWorktree,
		ProjectRoot:     worktreeRoot,
		InvocationCWD:   canonicalCWD,
		GitWorktreeRoot: worktreeRoot,
		GitCommonDir:    commonDir,
		GitOrigin:       sanitizeGitRemote(origin),
		GitBranch:       branch,
		GitHead:         head,
	}
	identity.ProjectKey = projectKey(identity.Kind, identity.ProjectRoot)
	return identity, identity.Validate()
}

// LocationState is the explicit resume-time relationship between persisted
// metadata and current disk state.
type LocationState string

const (
	LocationAvailable LocationState = "available"
	LocationMissing   LocationState = "missing"
	LocationMoved     LocationState = "moved"
	LocationMismatch  LocationState = "mismatch"
)

// LocationInspection never rebinds persisted metadata. candidateCWD is
// optional; when supplied after the stored root disappeared it is reported as
// a proposed move requiring explicit caller action.
type LocationInspection struct {
	State     LocationState
	Persisted ProjectIdentity
	Current   *ProjectIdentity
}

// InspectLocation returns an explicit availability/move/mismatch state for a
// persisted thread. It does not mutate or silently rebind project identity.
func InspectLocation(
	ctx context.Context,
	persisted ProjectIdentity,
	candidateCWD string,
) (LocationInspection, error) {
	if err := persisted.Validate(); err != nil {
		return LocationInspection{}, fmt.Errorf("inspect coding project: %w", err)
	}
	inspection := LocationInspection{Persisted: persisted}
	rootInfo, statErr := os.Stat(persisted.ProjectRoot)
	if statErr != nil {
		if !os.IsNotExist(statErr) {
			return LocationInspection{}, fmt.Errorf("inspect coding project root: %w", statErr)
		}
		inspection.State = LocationMissing
		if strings.TrimSpace(candidateCWD) != "" {
			current, err := ResolveProject(ctx, candidateCWD)
			if err != nil {
				return LocationInspection{}, err
			}
			inspection.Current = &current
			inspection.State = LocationMoved
		}
		return inspection, nil
	}
	if !rootInfo.IsDir() {
		inspection.State = LocationMoved
		return inspection, nil
	}
	invocationInfo, statErr := os.Stat(persisted.InvocationCWD)
	if statErr != nil {
		if !os.IsNotExist(statErr) {
			return LocationInspection{}, fmt.Errorf("inspect coding invocation cwd: %w", statErr)
		}
		inspection.State = LocationMissing
		if strings.TrimSpace(candidateCWD) != "" {
			current, err := ResolveProject(ctx, candidateCWD)
			if err != nil {
				return LocationInspection{}, err
			}
			inspection.Current = &current
			inspection.State = LocationMoved
		}
		return inspection, nil
	}
	if !invocationInfo.IsDir() {
		inspection.State = LocationMoved
		return inspection, nil
	}

	current, err := ResolveProject(ctx, persisted.InvocationCWD)
	if err != nil {
		return LocationInspection{}, err
	}
	inspection.Current = &current
	if current.ProjectKey != persisted.ProjectKey {
		inspection.State = LocationMoved
		return inspection, nil
	}
	if strings.TrimSpace(candidateCWD) != "" {
		candidate, resolveErr := ResolveProject(ctx, candidateCWD)
		if resolveErr != nil {
			return LocationInspection{}, resolveErr
		}
		inspection.Current = &candidate
		if candidate.ProjectKey != persisted.ProjectKey {
			inspection.State = LocationMismatch
			return inspection, nil
		}
	}
	inspection.State = LocationAvailable
	return inspection, nil
}

func canonicalExistingDirectory(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("make path absolute: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve symlinks: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat path: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path is not a directory")
	}
	return filepath.Clean(resolved), nil
}

func projectKey(kind ProjectKind, root string) string {
	sum := sha256.Sum256([]byte(string(kind) + "\x00" + filepath.Clean(root)))
	return string(kind) + ":" + hex.EncodeToString(sum[:])
}

func resolveGitPath(cwd, value string) string {
	if filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(cwd, value)
}

func gitWorktreeRoot(ctx context.Context, cwd string) (string, bool, error) {
	value, err := execGit(ctx, cwd, "rev-parse", "--show-toplevel")
	if err == nil {
		return value, value != "", nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return "", false, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 128 &&
		strings.Contains(string(exitErr.Stderr), "not a git repository") {
		return "", false, nil
	}
	return "", false, gitCommandError([]string{"rev-parse", "--show-toplevel"}, err)
}

func gitRequired(ctx context.Context, cwd string, args ...string) (string, error) {
	value, err := execGit(ctx, cwd, args...)
	if err != nil {
		return "", gitCommandError(args, err)
	}
	if value == "" {
		return "", fmt.Errorf("resolve coding project: git %s returned no value", strings.Join(args, " "))
	}
	return value, nil
}

func gitOptional(ctx context.Context, cwd string, emptyExitCode int, args ...string) (string, error) {
	value, err := execGit(ctx, cwd, args...)
	if err == nil {
		return value, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == emptyExitCode {
		return "", nil
	}
	return "", gitCommandError(args, err)
}

func gitHead(ctx context.Context, cwd string) (string, error) {
	args := []string{"rev-parse", "--verify", "HEAD"}
	value, err := execGit(ctx, cwd, args...)
	if err == nil {
		return value, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 128 &&
		strings.Contains(string(exitErr.Stderr), "Needed a single revision") {
		return "", nil
	}
	return "", gitCommandError(args, err)
}

func execGit(ctx context.Context, cwd string, args ...string) (string, error) {
	commandArgs := append([]string{"-C", cwd}, args...)
	command := exec.CommandContext(ctx, "git", commandArgs...)
	command.Env = append(os.Environ(), "LC_ALL=C")
	output, err := command.Output()
	return strings.TrimSpace(string(output)), err
}

func gitCommandError(args []string, err error) error {
	return fmt.Errorf("resolve coding project: git %s: %w", strings.Join(args, " "), err)
}

func isHex(value string) bool {
	_, err := hex.DecodeString(value)
	return err == nil
}

func sanitizeGitRemote(remote string) string {
	remote = strings.TrimSpace(remote)
	parsed, err := url.Parse(remote)
	if err != nil || parsed.Scheme == "" {
		return remote
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}
