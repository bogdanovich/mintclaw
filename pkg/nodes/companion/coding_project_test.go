package companion

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	worker "github.com/bogdanovich/mintclaw/pkg/coding/task"
	codingworker "github.com/bogdanovich/mintclaw/pkg/coding/worker"
	codingworktree "github.com/bogdanovich/mintclaw/pkg/coding/worktree"
)

func TestCodingProjectConfigurationIsDenyByDefault(t *testing.T) {
	cfg, err := (Config{GatewayURL: "wss://gateway.example"}).Normalize(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CodingProjects == nil || len(cfg.CodingProjects) != 0 {
		t.Fatalf("default coding projects = %#v", cfg.CodingProjects)
	}
	catalog, err := NewCodingProjectCatalog(cfg.CodingProjects)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.List()) != 0 {
		t.Fatalf("default descriptors = %#v", catalog.List())
	}
	if _, err := catalog.resolve(
		t.Context(),
		"missing",
		"revision",
		worker.TaskModeInvestigate,
	); !errors.Is(
		err,
		ErrCodingProjectNotFound,
	) {
		t.Fatalf("missing alias error = %v", err)
	}
}

func TestCodingProjectProtocolConstantsMatchNativeRuntime(t *testing.T) {
	if CodingWorkerProtocolV1 != codingworker.ProtocolV1 {
		t.Fatalf("worker protocol = %d, want %d", CodingWorkerProtocolV1, codingworker.ProtocolV1)
	}
	if CodingBranchPrefix != codingworktree.DefaultBranchPrefix {
		t.Fatalf("branch prefix = %q, want %q", CodingBranchPrefix, codingworktree.DefaultBranchPrefix)
	}
}

func TestConfigNormalizesEnabledCodingProject(t *testing.T) {
	fixture := newCodingProjectFixture(t, []worker.TaskMode{worker.TaskModeInvestigate})
	cfg, err := (Config{
		GatewayURL: "wss://gateway.example",
		CodingProjects: map[string]CodingProjectPolicy{
			"mintclaw": fixture.projects["mintclaw"],
		},
	}).Normalize(fixture.baseDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewCodingProjectCatalog(cfg.CodingProjects); err != nil {
		t.Fatal(err)
	}
}

func TestCodingProjectCatalogReturnsOnlySafeStableDescriptors(t *testing.T) {
	fixture := newCodingProjectFixture(t, []worker.TaskMode{
		worker.TaskModeMutate,
		worker.TaskModeInvestigate,
	})
	catalog, err := NewCodingProjectCatalog(fixture.projects)
	if err != nil {
		t.Fatal(err)
	}
	descriptors := catalog.List()
	if len(descriptors) != 1 || descriptors[0].Alias != "mintclaw" ||
		!validCodingDescriptorRevision(descriptors[0].Revision) ||
		len(descriptors[0].AllowedModes) != 2 ||
		descriptors[0].AllowedModes[0] != worker.TaskModeInvestigate ||
		descriptors[0].AllowedModes[1] != worker.TaskModeMutate ||
		descriptors[0].WorkerProtocolVersion != CodingWorkerProtocolV1 ||
		descriptors[0].MaxConcurrentTasks != 1 ||
		descriptors[0].TaskTimeoutSeconds != int(DefaultCodingTaskTimeout.Seconds()) ||
		descriptors[0].WorkerIdleTimeoutSeconds != int(DefaultCodingWorkerIdleTimeout.Seconds()) ||
		descriptors[0].EventBytesMax != DefaultCodingEventBytes ||
		descriptors[0].ResultBytesMax != DefaultCodingResultBytes ||
		descriptors[0].ArtifactCountMax != DefaultCodingArtifactCount ||
		descriptors[0].ArtifactBytesMax != DefaultCodingArtifactBytes ||
		descriptors[0].ArtifactsTotalBytesMax != DefaultCodingArtifactsTotal {
		t.Fatalf("descriptors = %#v", descriptors)
	}
	encoded, err := json.Marshal(descriptors)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		fixture.baseDir,
		fixture.root,
		fixture.home,
		fixture.worktreeParent,
		fixture.workerExecutable,
		"gpt-test",
		"openai",
	} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("descriptor leaked %q: %s", secret, encoded)
		}
	}
	if err := descriptors[0].Validate(); err != nil {
		t.Fatal(err)
	}
	changed := descriptors[0]
	changed.AllowedModes[0], changed.AllowedModes[1] = changed.AllowedModes[1], changed.AllowedModes[0]
	if err := changed.Validate(); err == nil {
		t.Fatal("descriptor accepted non-canonical modes")
	}
	if catalog.List()[0].AllowedModes[0] != worker.TaskModeInvestigate {
		t.Fatal("descriptor mutation changed catalog state")
	}
}

func TestCodingProjectCatalogRejectsPolicyMutationAfterNormalization(t *testing.T) {
	fixture := newCodingProjectFixture(t, []worker.TaskMode{worker.TaskModeInvestigate})
	policy := fixture.projects["mintclaw"]
	policy.Model = "changed-model"
	if _, err := NewCodingProjectCatalog(map[string]CodingProjectPolicy{"mintclaw": policy}); err == nil {
		t.Fatal("catalog accepted policy fields that no longer match the descriptor revision")
	}
}

func TestCodingProjectDescriptorRevisionBindsEffectiveAuthority(t *testing.T) {
	fixture := newCodingProjectFixture(t, []worker.TaskMode{worker.TaskModeInvestigate})
	original := fixture.projects["mintclaw"]
	changed := original
	changed.EventBytesMax /= 2
	normalized, err := normalizeCodingProjects(
		map[string]CodingProjectPolicy{"mintclaw": changed},
		fixture.baseDir,
	)
	if err != nil {
		t.Fatal(err)
	}
	if normalized["mintclaw"].descriptorRevision == original.descriptorRevision {
		t.Fatal("descriptor revision did not bind a changed event limit")
	}
}

func TestCodingProjectConfigurationRejectsOverlappingAliases(t *testing.T) {
	fixture := newCodingProjectFixture(t, []worker.TaskMode{worker.TaskModeInvestigate})
	first := fixture.projects["mintclaw"]
	second := first
	second.Revision = "revision-two"
	if _, err := normalizeCodingProjects(map[string]CodingProjectPolicy{
		"mintclaw": first,
		"mirror":   second,
	}, fixture.baseDir); err == nil || !strings.Contains(err.Error(), "overlapping") {
		t.Fatalf("overlapping alias error = %v", err)
	}
}

func TestCodingProjectPlatformSupportIsClosed(t *testing.T) {
	if !codingPlatformSupported("darwin") || !codingPlatformSupported("linux") ||
		codingPlatformSupported("windows") || codingPlatformSupported("freebsd") {
		t.Fatal("unexpected coding project platform support")
	}
}

func TestCodingProjectResolutionFailsClosedOnRevisionModeAndRepositoryChanges(t *testing.T) {
	fixture := newCodingProjectFixture(t, []worker.TaskMode{worker.TaskModeInvestigate})
	catalog, err := NewCodingProjectCatalog(fixture.projects)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.resolve(
		t.Context(), "mintclaw", "changed", worker.TaskModeInvestigate,
	); !errors.Is(err, ErrCodingProjectStale) {
		t.Fatalf("stale revision error = %v", err)
	}
	if _, err := catalog.resolve(
		t.Context(), "mintclaw", catalog.List()[0].Revision, worker.TaskModeMutate,
	); !errors.Is(err, ErrCodingModeDenied) {
		t.Fatalf("denied mode error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "second.txt"), []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runCodingProjectGit(t, fixture.root, "add", "second.txt")
	runCodingProjectGit(t, fixture.root, "commit", "-m", "second")
	if _, err := catalog.resolve(
		t.Context(), "mintclaw", catalog.List()[0].Revision, worker.TaskModeInvestigate,
	); !errors.Is(err, ErrCodingProjectChanged) {
		t.Fatalf("changed project error = %v", err)
	}
}

func TestCodingMutationResolutionRejectsDirtySource(t *testing.T) {
	fixture := newCodingProjectFixture(t, []worker.TaskMode{worker.TaskModeMutate})
	catalog, err := NewCodingProjectCatalog(fixture.projects)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "dirty.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.resolve(
		t.Context(), "mintclaw", catalog.List()[0].Revision, worker.TaskModeMutate,
	); err == nil || !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("dirty source error = %v", err)
	}
}

func TestCodingProjectConfigurationRejectsPathsAndAuthorityBroadening(t *testing.T) {
	fixture := newCodingProjectFixture(t, []worker.TaskMode{worker.TaskModeInvestigate})
	base := fixture.projects["mintclaw"]
	tests := []struct {
		name   string
		alias  string
		mutate func(*CodingProjectPolicy)
	}{
		{name: "path alias", alias: "../repo"},
		{name: "missing revision", alias: "mintclaw", mutate: func(policy *CodingProjectPolicy) {
			policy.Revision = ""
		}},
		{name: "missing source parent", alias: "mintclaw", mutate: func(policy *CodingProjectPolicy) {
			policy.SourceParent = ""
		}},
		{name: "root outside source parent", alias: "mintclaw", mutate: func(policy *CodingProjectPolicy) {
			policy.SourceParent = fixture.home
		}},
		{name: "unsupported worker protocol", alias: "mintclaw", mutate: func(policy *CodingProjectPolicy) {
			policy.WorkerProtocolVersion++
		}},
		{name: "unsupported credential source", alias: "mintclaw", mutate: func(policy *CodingProjectPolicy) {
			policy.CredentialSource = "gateway"
		}},
		{name: "duplicate modes", alias: "mintclaw", mutate: func(policy *CodingProjectPolicy) {
			policy.AllowedModes = []worker.TaskMode{worker.TaskModeInvestigate, worker.TaskModeInvestigate}
		}},
		{name: "unknown provider profile", alias: "mintclaw", mutate: func(policy *CodingProjectPolicy) {
			policy.ProviderProfile = "gateway-selected"
		}},
		{name: "model control", alias: "mintclaw", mutate: func(policy *CodingProjectPolicy) {
			policy.Model = "gpt-test\nsecret"
		}},
		{name: "root overlaps home", alias: "mintclaw", mutate: func(policy *CodingProjectPolicy) {
			policy.MintClawHome = fixture.baseDir
		}},
		{name: "worktree without mutation", alias: "mintclaw", mutate: func(policy *CodingProjectPolicy) {
			policy.WorktreeParent = t.TempDir()
		}},
		{name: "excessive concurrency", alias: "mintclaw", mutate: func(policy *CodingProjectPolicy) {
			policy.MaxConcurrentTasks = MaxCodingTaskConcurrency + 1
		}},
		{name: "excessive result", alias: "mintclaw", mutate: func(policy *CodingProjectPolicy) {
			policy.ResultBytesMax = MaxCodingResultBytes + 1
		}},
		{name: "overflowing timeout", alias: "mintclaw", mutate: func(policy *CodingProjectPolicy) {
			policy.TaskTimeoutSeconds = math.MaxInt
		}},
		{name: "unsupported worker idle timeout", alias: "mintclaw", mutate: func(policy *CodingProjectPolicy) {
			policy.WorkerIdleTimeoutSeconds = int(DefaultCodingWorkerIdleTimeout/time.Second) + 1
		}},
		{name: "aggregate artifact underflow", alias: "mintclaw", mutate: func(policy *CodingProjectPolicy) {
			policy.ArtifactBytesMax = 1024
			policy.ArtifactsTotalBytesMax = 512
		}},
		{name: "unadmitted cleanup", alias: "mintclaw", mutate: func(policy *CodingProjectPolicy) {
			policy.CleanupPolicy = "force"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := base
			policy.AllowedModes = append([]worker.TaskMode(nil), base.AllowedModes...)
			if test.mutate != nil {
				test.mutate(&policy)
			}
			if _, err := normalizeCodingProjects(
				map[string]CodingProjectPolicy{test.alias: policy}, fixture.baseDir,
			); err == nil {
				t.Fatalf("unsafe coding project accepted: %q %#v", test.alias, policy)
			}
		})
	}
}

func TestCodingProjectConfigurationRejectsSymlinkedAuthority(t *testing.T) {
	fixture := newCodingProjectFixture(t, []worker.TaskMode{worker.TaskModeMutate})
	base := fixture.projects["mintclaw"]
	rootLink := filepath.Join(fixture.baseDir, "root-link")
	parentLink := filepath.Join(t.TempDir(), "parent-link")
	homeLink := filepath.Join(fixture.baseDir, "home-link")
	worktreeLink := filepath.Join(fixture.baseDir, "worktree-link")
	workerLink := filepath.Join(fixture.baseDir, "worker-link")
	if err := os.Symlink(fixture.root, rootLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fixture.workerExecutable, workerLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fixture.baseDir, parentLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fixture.home, homeLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fixture.worktreeParent, worktreeLink); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*CodingProjectPolicy){
		func(policy *CodingProjectPolicy) { policy.Root = rootLink },
		func(policy *CodingProjectPolicy) { policy.SourceParent = parentLink },
		func(policy *CodingProjectPolicy) { policy.MintClawHome = homeLink },
		func(policy *CodingProjectPolicy) { policy.WorktreeParent = worktreeLink },
		func(policy *CodingProjectPolicy) { policy.WorkerExecutable = workerLink },
	} {
		policy := base
		mutate(&policy)
		if _, err := normalizeCodingProjects(
			map[string]CodingProjectPolicy{"mintclaw": policy}, fixture.baseDir,
		); err == nil {
			t.Fatalf("symlinked coding authority accepted: %#v", policy)
		}
	}
}

func TestCodingMutationConfigurationSanitizesGitEnvironment(t *testing.T) {
	fixture := newCodingProjectFixture(t, []worker.TaskMode{worker.TaskModeMutate})
	policy := fixture.projects["mintclaw"]
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "decoy.git"))
	t.Setenv("GIT_WORK_TREE", t.TempDir())
	t.Setenv("GIT_INDEX_FILE", filepath.Join(t.TempDir(), "index"))
	if _, err := normalizeCodingProjects(
		map[string]CodingProjectPolicy{"mintclaw": policy},
		fixture.baseDir,
	); err != nil {
		t.Fatalf("ambient Git environment changed project authority: %v", err)
	}
}

func TestCodingMutationConfigurationRejectsLinkedWorktreeSource(t *testing.T) {
	fixture := newCodingProjectFixture(t, []worker.TaskMode{worker.TaskModeMutate})
	linkedRoot := filepath.Join(fixture.baseDir, "linked-source")
	runCodingProjectGit(t, fixture.root, "worktree", "add", "-b", "linked-source", linkedRoot)
	policy := fixture.projects["mintclaw"]
	policy.Root = linkedRoot
	if _, err := normalizeCodingProjects(
		map[string]CodingProjectPolicy{"mintclaw": policy},
		fixture.baseDir,
	); err == nil || !strings.Contains(err.Error(), "linked worktree") {
		t.Fatalf("linked worktree source error = %v", err)
	}
}

func TestCodingMutationConfigurationRejectsSubmoduleSource(t *testing.T) {
	fixture := newCodingProjectFixture(t, []worker.TaskMode{worker.TaskModeMutate})
	origin := filepath.Join(fixture.baseDir, "submodule-origin")
	if err := os.Mkdir(origin, 0o700); err != nil {
		t.Fatal(err)
	}
	runCodingProjectGit(t, origin, "init", "-b", "main")
	runCodingProjectGit(t, origin, "config", "user.email", "test@example.com")
	runCodingProjectGit(t, origin, "config", "user.name", "MintClaw Test")
	if err := os.WriteFile(filepath.Join(origin, "README.md"), []byte("submodule\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runCodingProjectGit(t, origin, "add", "README.md")
	runCodingProjectGit(t, origin, "commit", "-m", "fixture")
	runCodingProjectGit(
		t,
		fixture.root,
		"-c",
		"protocol.file.allow=always",
		"submodule",
		"add",
		origin,
		"nested",
	)
	runCodingProjectGit(t, fixture.root, "commit", "-am", "add submodule")

	policy := fixture.projects["mintclaw"]
	policy.Root = filepath.Join(fixture.root, "nested")
	if _, err := normalizeCodingProjects(
		map[string]CodingProjectPolicy{"mintclaw": policy},
		fixture.baseDir,
	); err == nil || !strings.Contains(err.Error(), "submodule") {
		t.Fatalf("submodule source error = %v", err)
	}
}

func TestCodingMutationConfigurationRejectsDetachedAndUnbornSources(t *testing.T) {
	t.Run("detached", func(t *testing.T) {
		fixture := newCodingProjectFixture(t, []worker.TaskMode{worker.TaskModeMutate})
		runCodingProjectGit(t, fixture.root, "checkout", "--detach")
		if _, err := normalizeCodingProjects(fixture.projects, fixture.baseDir); err == nil ||
			!strings.Contains(err.Error(), "checked-out Git branch") {
			t.Fatalf("detached source error = %v", err)
		}
	})
	t.Run("unborn", func(t *testing.T) {
		fixture := newCodingProjectFixture(t, []worker.TaskMode{worker.TaskModeMutate})
		unborn := filepath.Join(fixture.baseDir, "unborn")
		if err := os.Mkdir(unborn, 0o700); err != nil {
			t.Fatal(err)
		}
		runCodingProjectGit(t, unborn, "init", "-b", "main")
		policy := fixture.projects["mintclaw"]
		policy.Root = unborn
		if _, err := normalizeCodingProjects(
			map[string]CodingProjectPolicy{"mintclaw": policy},
			fixture.baseDir,
		); err == nil || !strings.Contains(err.Error(), "committed HEAD") {
			t.Fatalf("unborn source error = %v", err)
		}
	})
}

func TestCodingProjectCatalogRejectsSameContentExecutableReplacement(t *testing.T) {
	fixture := newCodingProjectFixture(t, []worker.TaskMode{worker.TaskModeInvestigate})
	catalog, err := NewCodingProjectCatalog(fixture.projects)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(fixture.workerExecutable)
	if err != nil {
		t.Fatal(err)
	}
	replaced := fixture.workerExecutable + ".replaced"
	if err = os.Rename(fixture.workerExecutable, replaced); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(fixture.workerExecutable, content, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err = catalog.resolve(
		t.Context(), "mintclaw", catalog.List()[0].Revision, worker.TaskModeInvestigate,
	); !errors.Is(err, ErrCodingProjectChanged) {
		t.Fatalf("replaced executable error = %v", err)
	}
}

func TestCodingProjectCatalogRejectsDirectoryReplacement(t *testing.T) {
	fixture := newCodingProjectFixture(t, []worker.TaskMode{worker.TaskModeInvestigate})
	catalog, err := NewCodingProjectCatalog(fixture.projects)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(fixture.root, fixture.root+".replaced"); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(fixture.root, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err = catalog.resolve(
		t.Context(), "mintclaw", catalog.List()[0].Revision, worker.TaskModeInvestigate,
	); !errors.Is(err, ErrCodingProjectChanged) {
		t.Fatalf("replaced root error = %v", err)
	}
}

type codingProjectFixture struct {
	baseDir          string
	root             string
	home             string
	worktreeParent   string
	workerExecutable string
	projects         map[string]CodingProjectPolicy
}

func newCodingProjectFixture(t *testing.T, modes []worker.TaskMode) codingProjectFixture {
	t.Helper()
	baseDir := t.TempDir()
	root := filepath.Join(baseDir, "repository")
	home := filepath.Join(baseDir, "mintclaw-home")
	worktreeParent := filepath.Join(baseDir, "worktrees")
	for _, path := range []string{root, home, worktreeParent} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	runCodingProjectGit(t, root, "init", "-b", "main")
	runCodingProjectGit(t, root, "config", "user.email", "test@example.com")
	runCodingProjectGit(t, root, "config", "user.name", "MintClaw Test")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runCodingProjectGit(t, root, "add", "README.md")
	runCodingProjectGit(t, root, "commit", "-m", "fixture")
	workerExecutable := filepath.Join(baseDir, "mintclaw")
	if err := os.WriteFile(workerExecutable, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	policy := CodingProjectPolicy{
		Revision: "revision-one", SourceParent: baseDir, Root: root, AllowedModes: modes,
		WorkerExecutable: workerExecutable, WorkerProtocolVersion: CodingWorkerProtocolV1,
		MintClawHome: home, CredentialSource: CodingCredentialSourceNative,
		ProviderProfile: CodingProviderProfileDefault, Model: "gpt-test", Provider: "openai",
	}
	if modeAllowed(modes, worker.TaskModeMutate) {
		policy.WorktreeParent = worktreeParent
		policy.BranchPrefix = CodingBranchPrefix
	}
	projects, err := normalizeCodingProjects(
		map[string]CodingProjectPolicy{"mintclaw": policy},
		baseDir,
	)
	if err != nil {
		t.Fatal(err)
	}
	normalized := projects["mintclaw"]
	return codingProjectFixture{
		baseDir: baseDir, root: normalized.Root, home: normalized.MintClawHome,
		worktreeParent:   normalized.WorktreeParent,
		workerExecutable: normalized.WorkerExecutable, projects: projects,
	}
}

func runCodingProjectGit(t *testing.T, root string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}
