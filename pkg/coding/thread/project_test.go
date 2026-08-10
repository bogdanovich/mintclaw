package thread

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveProjectNonGitAndSymlinkDeterminism(t *testing.T) {
	root := t.TempDir()
	projectRoot := filepath.Join(root, "project")
	cwd := filepath.Join(projectRoot, "nested")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	direct, err := ResolveProject(t.Context(), cwd)
	if err != nil {
		t.Fatalf("ResolveProject(direct) error = %v", err)
	}
	if direct.Kind != ProjectKindDirectory || direct.ProjectRoot != direct.InvocationCWD {
		t.Fatalf("non-Git identity = %#v", direct)
	}

	link := filepath.Join(root, "linked-cwd")
	if err := os.Symlink(cwd, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlinks unavailable: %v", err)
		}
		t.Fatalf("Symlink() error = %v", err)
	}
	linked, err := ResolveProject(t.Context(), link)
	if err != nil {
		t.Fatalf("ResolveProject(link) error = %v", err)
	}
	if linked != direct {
		t.Fatalf("symlink identity = %#v, want %#v", linked, direct)
	}
	restarted, err := ResolveProject(t.Context(), cwd)
	if err != nil {
		t.Fatalf("ResolveProject(restart) error = %v", err)
	}
	if restarted.ProjectKey != direct.ProjectKey {
		t.Fatalf("restart project key = %q, want %q", restarted.ProjectKey, direct.ProjectKey)
	}
}

func TestResolveProjectWithoutGitTreatsDirectoryAsNonGit(t *testing.T) {
	projectRoot := t.TempDir()
	t.Setenv("PATH", "")
	identity, err := ResolveProject(t.Context(), projectRoot)
	if err != nil {
		t.Fatalf("ResolveProject() error = %v", err)
	}
	if identity.Kind != ProjectKindDirectory || identity.GitCommonDir != "" {
		t.Fatalf("identity without Git = %#v", identity)
	}
}

func TestSanitizeGitRemote(t *testing.T) {
	tests := []struct {
		name   string
		remote string
		want   string
	}{
		{
			name:   "absolute URL",
			remote: "https://user:password@example.com/owner/repo.git?token=secret#fragment",
			want:   "https://example.com/owner/repo.git",
		},
		{
			name:   "scheme relative URL",
			remote: "//user:password@example.com/owner/repo.git?token=secret#fragment",
			want:   "//example.com/owner/repo.git",
		},
		{
			name:   "SCP-like",
			remote: "git@github.com:owner/repo.git",
			want:   "github.com:owner/repo.git",
		},
		{
			name:   "local path",
			remote: "../repositories/repo.git",
			want:   "../repositories/repo.git",
		},
		{
			name:   "absolute file URL",
			remote: "file:///repositories/repo.git?token=secret#fragment",
			want:   "file:///repositories/repo.git",
		},
		{
			name:   "opaque file URL",
			remote: "file:user:password@example.com/repo.git?token=secret#fragment",
			want:   "",
		},
		{
			name:   "malformed URL",
			remote: "https://%zz:secret@example.com/repo.git",
			want:   "",
		},
		{
			name:   "unclassified user info",
			remote: "user:token@example.com",
			want:   "",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := sanitizeGitRemote(test.remote); got != test.want {
				t.Fatalf("sanitizeGitRemote(%q) = %q, want %q", test.remote, got, test.want)
			}
		})
	}
}

func TestResolveProjectGitWorktreeObservations(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	runGit(t, root, "init", repository)
	runGit(t, repository, "config", "user.name", "Fixture")
	runGit(t, repository, "config", "user.email", "fixture@example.com")
	runGit(t, repository, "remote", "add", "origin", "https://secret@example.com/owner/repo.git")
	if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	runGit(t, repository, "add", "tracked.txt")
	runGit(t, repository, "commit", "-m", "fixture")
	branch := strings.TrimSpace(runGit(t, repository, "branch", "--show-current"))
	head := strings.TrimSpace(runGit(t, repository, "rev-parse", "HEAD"))

	nested := filepath.Join(repository, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	identity, err := ResolveProject(t.Context(), nested)
	if err != nil {
		t.Fatalf("ResolveProject() error = %v", err)
	}
	canonicalRepository, err := filepath.EvalSymlinks(repository)
	if err != nil {
		t.Fatalf("EvalSymlinks() error = %v", err)
	}
	canonicalNested, err := filepath.EvalSymlinks(nested)
	if err != nil {
		t.Fatalf("EvalSymlinks(nested) error = %v", err)
	}
	if identity.Kind != ProjectKindGitWorktree || identity.ProjectRoot != canonicalRepository ||
		identity.InvocationCWD != canonicalNested || identity.GitWorktreeRoot != canonicalRepository {
		t.Fatalf("Git identity roots = %#v", identity)
	}
	if identity.GitBranch != branch || identity.GitHead != head {
		t.Fatalf("Git ref metadata = branch %q head %q", identity.GitBranch, identity.GitHead)
	}
	if identity.GitOrigin != "https://example.com/owner/repo.git" {
		t.Fatalf("sanitized origin = %q", identity.GitOrigin)
	}
	if identity.GitCommonDir != filepath.Join(canonicalRepository, ".git") {
		t.Fatalf("common dir = %q", identity.GitCommonDir)
	}
}

func TestSeparateGitWorktreesAreSeparateProjects(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	worktree := filepath.Join(root, "worktree")
	runGit(t, root, "init", repository)
	runGit(t, repository, "config", "user.name", "Fixture")
	runGit(t, repository, "config", "user.email", "fixture@example.com")
	if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	runGit(t, repository, "add", "tracked.txt")
	runGit(t, repository, "commit", "-m", "fixture")
	runGit(t, repository, "worktree", "add", "--detach", worktree, "HEAD")

	mainIdentity, err := ResolveProject(t.Context(), repository)
	if err != nil {
		t.Fatalf("ResolveProject(main) error = %v", err)
	}
	worktreeIdentity, err := ResolveProject(t.Context(), worktree)
	if err != nil {
		t.Fatalf("ResolveProject(worktree) error = %v", err)
	}
	if mainIdentity.ProjectKey == worktreeIdentity.ProjectKey {
		t.Fatalf("separate worktrees share project key %q", mainIdentity.ProjectKey)
	}
	if mainIdentity.GitCommonDir != worktreeIdentity.GitCommonDir {
		t.Fatalf(
			"linked worktrees do not share common dir: %q / %q",
			mainIdentity.GitCommonDir,
			worktreeIdentity.GitCommonDir,
		)
	}
	if worktreeIdentity.GitBranch != "" || worktreeIdentity.GitHead == "" {
		t.Fatalf("detached worktree refs = branch %q head %q", worktreeIdentity.GitBranch, worktreeIdentity.GitHead)
	}
}

func TestResolveProjectUnbornGitRepository(t *testing.T) {
	requireGit(t)
	repository := filepath.Join(t.TempDir(), "repository")
	runGit(t, filepath.Dir(repository), "init", repository)
	identity, err := ResolveProject(t.Context(), repository)
	if err != nil {
		t.Fatalf("ResolveProject() error = %v", err)
	}
	if identity.Kind != ProjectKindGitWorktree || identity.GitBranch == "" || identity.GitHead != "" {
		t.Fatalf("unborn Git identity = %#v", identity)
	}
}

func TestInspectLocationReportsAvailableMissingMovedAndMismatch(t *testing.T) {
	root := t.TempDir()
	projectRoot := filepath.Join(root, "project")
	otherRoot := filepath.Join(root, "other")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(project) error = %v", err)
	}
	if err := os.MkdirAll(otherRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(other) error = %v", err)
	}
	identity, err := ResolveProject(t.Context(), projectRoot)
	if err != nil {
		t.Fatalf("ResolveProject() error = %v", err)
	}

	available, err := InspectLocation(t.Context(), identity, "")
	if err != nil || available.State != LocationAvailable {
		t.Fatalf("available inspection = %#v, %v", available, err)
	}
	mismatch, err := InspectLocation(t.Context(), identity, otherRoot)
	if err != nil || mismatch.State != LocationMismatch {
		t.Fatalf("mismatch inspection = %#v, %v", mismatch, err)
	}
	if err := os.Rename(projectRoot, filepath.Join(root, "moved")); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	missing, err := InspectLocation(t.Context(), identity, "")
	if err != nil || missing.State != LocationMissing {
		t.Fatalf("missing inspection = %#v, %v", missing, err)
	}
	moved, err := InspectLocation(t.Context(), identity, filepath.Join(root, "moved"))
	if err != nil || moved.State != LocationMoved || moved.Current == nil {
		t.Fatalf("moved inspection = %#v, %v", moved, err)
	}
	if moved.Persisted.ProjectKey == moved.Current.ProjectKey {
		t.Fatal("moved project was silently rebound to its previous key")
	}
}

func TestInspectLocationReportsDeletedInvocationDirectory(t *testing.T) {
	requireGit(t)
	repository := filepath.Join(t.TempDir(), "repository")
	runGit(t, filepath.Dir(repository), "init", repository)
	nested := filepath.Join(repository, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	identity, err := ResolveProject(t.Context(), nested)
	if err != nil {
		t.Fatalf("ResolveProject() error = %v", err)
	}
	if err := os.Remove(nested); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	inspection, err := InspectLocation(t.Context(), identity, "")
	if err != nil || inspection.State != LocationMissing {
		t.Fatalf("deleted invocation inspection = %#v, %v", inspection, err)
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
}

func runGit(t *testing.T, cwd string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", cwd}, args...)
	output, err := exec.Command("git", commandArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
