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

	codingscope "github.com/bogdanovich/mintclaw/pkg/coding/scope"
	worker "github.com/bogdanovich/mintclaw/pkg/coding/task"
	codingworker "github.com/bogdanovich/mintclaw/pkg/coding/worker"
	codingworktree "github.com/bogdanovich/mintclaw/pkg/coding/worktree"
)

func TestCodingScopeConfigurationIsDenyByDefault(t *testing.T) {
	cfg, err := (Config{GatewayURL: "wss://gateway.example"}).Normalize(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CodingScopes == nil || len(cfg.CodingScopes) != 0 {
		t.Fatalf("default coding scopes = %#v", cfg.CodingScopes)
	}
	catalog, err := NewCodingScopeCatalog(cfg.CodingScopes)
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
		ErrCodingScopeNotFound,
	) {
		t.Fatalf("missing alias error = %v", err)
	}
}

func TestLoadConfigRejectsLegacyCodingProjects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	raw := []byte(`{"gateway_url":"wss://gateway.example","coding_projects":{}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "unknown field \"coding_projects\"") {
		t.Fatalf("LoadConfig() error = %v, want rejected legacy config key", err)
	}
}

func TestCodingScopeProtocolConstantsMatchNativeRuntime(t *testing.T) {
	if CodingWorkerProtocolV3 != codingworker.ProtocolV3 {
		t.Fatalf("worker protocol = %d, want %d", CodingWorkerProtocolV3, codingworker.ProtocolV3)
	}
	if CodingBranchPrefix != codingworktree.DefaultBranchPrefix {
		t.Fatalf("branch prefix = %q, want %q", CodingBranchPrefix, codingworktree.DefaultBranchPrefix)
	}
}

func TestConfigNormalizesEnabledCodingScope(t *testing.T) {
	fixture := newCodingScopeFixture(t, []worker.TaskMode{worker.TaskModeInvestigate})
	cfg, err := (Config{
		GatewayURL: "wss://gateway.example",
		CodingScopes: map[string]CodingScopePolicy{
			"mintclaw": fixture.scopes["mintclaw"],
		},
	}).Normalize(fixture.baseDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewCodingScopeCatalog(cfg.CodingScopes); err != nil {
		t.Fatal(err)
	}
}

func TestResolveCodingScopeRejectsPlainDirectoryInvestigation(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolveCodingScope(t.Context(), root, worker.TaskModeInvestigate); err == nil ||
		!strings.Contains(err.Error(), "requires a Git worktree") {
		t.Fatalf("resolveCodingScope() error = %v, want Git worktree rejection", err)
	}
}

func TestCodingScopeCatalogReturnsOnlySafeStableDescriptors(t *testing.T) {
	fixture := newCodingScopeFixture(t, []worker.TaskMode{
		worker.TaskModeProjectYolo,
		worker.TaskModeMutate,
		worker.TaskModeInvestigate,
	})
	catalog, err := NewCodingScopeCatalog(fixture.scopes)
	if err != nil {
		t.Fatal(err)
	}
	descriptors := catalog.List()
	if len(descriptors) != 1 || descriptors[0].Alias != "mintclaw" ||
		!validCodingDescriptorRevision(descriptors[0].Revision) ||
		descriptors[0].Kind != codingscope.KindGitProject ||
		len(descriptors[0].AllowedProfiles) != 3 ||
		descriptors[0].AllowedProfiles[0] != worker.TaskModeInvestigate ||
		descriptors[0].AllowedProfiles[1] != worker.TaskModeMutate ||
		descriptors[0].AllowedProfiles[2] != worker.TaskModeProjectYolo ||
		descriptors[0].WorkerProtocolVersion != CodingWorkerProtocolV3 ||
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
	changed.AllowedProfiles[0], changed.AllowedProfiles[1] = changed.AllowedProfiles[1], changed.AllowedProfiles[0]
	if err := changed.Validate(); err == nil {
		t.Fatal("descriptor accepted non-canonical modes")
	}
	if catalog.List()[0].AllowedProfiles[0] != worker.TaskModeInvestigate {
		t.Fatal("descriptor mutation changed catalog state")
	}
}

func TestCodingScopeCatalogRejectsPolicyMutationAfterNormalization(t *testing.T) {
	fixture := newCodingScopeFixture(t, []worker.TaskMode{worker.TaskModeInvestigate})
	policy := fixture.scopes["mintclaw"]
	policy.Model = "changed-model"
	if _, err := NewCodingScopeCatalog(map[string]CodingScopePolicy{"mintclaw": policy}); err == nil {
		t.Fatal("catalog accepted policy fields that no longer match the descriptor revision")
	}
}

func TestCodingScopeDescriptorRevisionBindsEffectiveAuthority(t *testing.T) {
	fixture := newCodingScopeFixture(t, []worker.TaskMode{worker.TaskModeInvestigate})
	original := fixture.scopes["mintclaw"]
	changed := original
	changed.EventBytesMax /= 2
	normalized, err := normalizeCodingScopes(
		map[string]CodingScopePolicy{"mintclaw": changed},
		fixture.baseDir,
	)
	if err != nil {
		t.Fatal(err)
	}
	if normalized["mintclaw"].descriptorRevision == original.descriptorRevision {
		t.Fatal("descriptor revision did not bind a changed event limit")
	}
}

func TestCodingScopeConfigurationRejectsOverlappingAliases(t *testing.T) {
	fixture := newCodingScopeFixture(t, []worker.TaskMode{worker.TaskModeInvestigate})
	first := fixture.scopes["mintclaw"]
	second := first
	second.Revision = "revision-two"
	if _, err := normalizeCodingScopes(map[string]CodingScopePolicy{
		"mintclaw": first,
		"mirror":   second,
	}, fixture.baseDir); err == nil || !strings.Contains(err.Error(), "overlapping") {
		t.Fatalf("overlapping alias error = %v", err)
	}
}

func TestCodingScopeConfigurationRejectsOverlappingWorktreeParents(t *testing.T) {
	firstFixture := newCodingScopeFixture(t, []worker.TaskMode{worker.TaskModeMutate})
	secondFixture := newCodingScopeFixture(t, []worker.TaskMode{worker.TaskModeMutate})
	for _, test := range []struct {
		name   string
		parent string
	}{
		{name: "same", parent: firstFixture.worktreeParent},
		{name: "nested", parent: filepath.Join(firstFixture.worktreeParent, "nested")},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.parent != firstFixture.worktreeParent {
				if err := os.Mkdir(test.parent, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			first := firstFixture.scopes["mintclaw"]
			second := secondFixture.scopes["mintclaw"]
			first.Revision = "revision-first"
			second.Revision = "revision-second"
			second.WorktreeParent = test.parent
			if _, err := normalizeCodingScopes(map[string]CodingScopePolicy{
				"first":  first,
				"second": second,
			}, firstFixture.baseDir); err == nil || !strings.Contains(err.Error(), "overlapping") {
				t.Fatalf("overlapping worktree parent error = %v", err)
			}
		})
	}
}

func TestCodingScopePlatformSupportIsClosed(t *testing.T) {
	if !codingPlatformSupported("darwin") || !codingPlatformSupported("linux") ||
		codingPlatformSupported("windows") || codingPlatformSupported("freebsd") {
		t.Fatal("unexpected coding scope platform support")
	}
}

func TestCodingScopeResolutionFailsClosedOnRevisionModeAndRepositoryChanges(t *testing.T) {
	fixture := newCodingScopeFixture(t, []worker.TaskMode{worker.TaskModeInvestigate})
	catalog, err := NewCodingScopeCatalog(fixture.scopes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.resolve(
		t.Context(), "mintclaw", "changed", worker.TaskModeInvestigate,
	); !errors.Is(err, ErrCodingScopeStale) {
		t.Fatalf("stale revision error = %v", err)
	}
	if _, err := catalog.resolve(
		t.Context(), "mintclaw", catalog.List()[0].Revision, worker.TaskModeMutate,
	); !errors.Is(err, ErrCodingProfileDenied) {
		t.Fatalf("denied mode error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "second.txt"), []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runCodingScopeGit(t, fixture.root, "add", "second.txt")
	runCodingScopeGit(t, fixture.root, "commit", "-m", "second")
	if _, err := catalog.resolve(
		t.Context(), "mintclaw", catalog.List()[0].Revision, worker.TaskModeInvestigate,
	); !errors.Is(err, ErrCodingScopeChanged) {
		t.Fatalf("changed project error = %v", err)
	}
}

func TestCodingMutationResolutionRejectsDirtySource(t *testing.T) {
	fixture := newCodingScopeFixture(t, []worker.TaskMode{worker.TaskModeMutate})
	catalog, err := NewCodingScopeCatalog(fixture.scopes)
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

func TestCodingScopeConfigurationRejectsPathsAndAuthorityBroadening(t *testing.T) {
	fixture := newCodingScopeFixture(t, []worker.TaskMode{worker.TaskModeInvestigate})
	base := fixture.scopes["mintclaw"]
	tests := []struct {
		name   string
		alias  string
		mutate func(*CodingScopePolicy)
	}{
		{name: "path alias", alias: "../repo"},
		{name: "missing revision", alias: "mintclaw", mutate: func(policy *CodingScopePolicy) {
			policy.Revision = ""
		}},
		{name: "missing source parent", alias: "mintclaw", mutate: func(policy *CodingScopePolicy) {
			policy.SourceParent = ""
		}},
		{name: "root outside source parent", alias: "mintclaw", mutate: func(policy *CodingScopePolicy) {
			policy.SourceParent = fixture.home
		}},
		{name: "unsupported worker protocol", alias: "mintclaw", mutate: func(policy *CodingScopePolicy) {
			policy.WorkerProtocolVersion++
		}},
		{name: "unsupported credential source", alias: "mintclaw", mutate: func(policy *CodingScopePolicy) {
			policy.CredentialSource = "gateway"
		}},
		{name: "duplicate modes", alias: "mintclaw", mutate: func(policy *CodingScopePolicy) {
			policy.AllowedProfiles = []worker.TaskMode{worker.TaskModeInvestigate, worker.TaskModeInvestigate}
		}},
		{name: "machine yolo before scope migration", alias: "mintclaw", mutate: func(policy *CodingScopePolicy) {
			policy.AllowedProfiles = []worker.TaskMode{worker.TaskModeMachineYolo}
		}},
		{name: "unknown provider profile", alias: "mintclaw", mutate: func(policy *CodingScopePolicy) {
			policy.ProviderProfile = "gateway-selected"
		}},
		{name: "model control", alias: "mintclaw", mutate: func(policy *CodingScopePolicy) {
			policy.Model = "gpt-test\nsecret"
		}},
		{name: "root overlaps home", alias: "mintclaw", mutate: func(policy *CodingScopePolicy) {
			policy.MintClawHome = fixture.baseDir
		}},
		{name: "worktree without mutation", alias: "mintclaw", mutate: func(policy *CodingScopePolicy) {
			policy.WorktreeParent = t.TempDir()
		}},
		{name: "excessive concurrency", alias: "mintclaw", mutate: func(policy *CodingScopePolicy) {
			policy.MaxConcurrentTasks = MaxCodingTaskConcurrency + 1
		}},
		{name: "excessive result", alias: "mintclaw", mutate: func(policy *CodingScopePolicy) {
			policy.ResultBytesMax = MaxCodingResultBytes + 1
		}},
		{name: "overflowing timeout", alias: "mintclaw", mutate: func(policy *CodingScopePolicy) {
			policy.TaskTimeoutSeconds = math.MaxInt
		}},
		{name: "unsupported worker idle timeout", alias: "mintclaw", mutate: func(policy *CodingScopePolicy) {
			policy.WorkerIdleTimeoutSeconds = int(DefaultCodingWorkerIdleTimeout/time.Second) + 1
		}},
		{name: "aggregate artifact underflow", alias: "mintclaw", mutate: func(policy *CodingScopePolicy) {
			policy.ArtifactBytesMax = 1024
			policy.ArtifactsTotalBytesMax = 512
		}},
		{name: "unadmitted cleanup", alias: "mintclaw", mutate: func(policy *CodingScopePolicy) {
			policy.CleanupPolicy = "force"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := base
			policy.AllowedProfiles = append([]worker.TaskMode(nil), base.AllowedProfiles...)
			if test.mutate != nil {
				test.mutate(&policy)
			}
			if _, err := normalizeCodingScopes(
				map[string]CodingScopePolicy{test.alias: policy}, fixture.baseDir,
			); err == nil {
				t.Fatalf("unsafe coding scope accepted: %q %#v", test.alias, policy)
			}
		})
	}
}

func TestCodingScopeConfigurationRejectsSymlinkedAuthority(t *testing.T) {
	fixture := newCodingScopeFixture(t, []worker.TaskMode{worker.TaskModeMutate})
	base := fixture.scopes["mintclaw"]
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
	for _, mutate := range []func(*CodingScopePolicy){
		func(policy *CodingScopePolicy) { policy.Root = rootLink },
		func(policy *CodingScopePolicy) { policy.SourceParent = parentLink },
		func(policy *CodingScopePolicy) { policy.MintClawHome = homeLink },
		func(policy *CodingScopePolicy) { policy.WorktreeParent = worktreeLink },
		func(policy *CodingScopePolicy) { policy.WorkerExecutable = workerLink },
	} {
		policy := base
		mutate(&policy)
		if _, err := normalizeCodingScopes(
			map[string]CodingScopePolicy{"mintclaw": policy}, fixture.baseDir,
		); err == nil {
			t.Fatalf("symlinked coding authority accepted: %#v", policy)
		}
	}
}

func TestCodingMutationConfigurationSanitizesGitEnvironment(t *testing.T) {
	fixture := newCodingScopeFixture(t, []worker.TaskMode{worker.TaskModeMutate})
	policy := fixture.scopes["mintclaw"]
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "decoy.git"))
	t.Setenv("GIT_WORK_TREE", t.TempDir())
	t.Setenv("GIT_INDEX_FILE", filepath.Join(t.TempDir(), "index"))
	if _, err := normalizeCodingScopes(
		map[string]CodingScopePolicy{"mintclaw": policy},
		fixture.baseDir,
	); err != nil {
		t.Fatalf("ambient Git environment changed project authority: %v", err)
	}
}

func TestCodingMutationConfigurationDisablesRepositoryFSMonitor(t *testing.T) {
	fixture := newCodingScopeFixture(t, []worker.TaskMode{worker.TaskModeMutate})
	catalog, err := NewCodingScopeCatalog(fixture.scopes)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "fsmonitor-ran")
	hook := filepath.Join(t.TempDir(), "fsmonitor-hook")
	t.Setenv("MINTCLAW_TEST_FSMONITOR_MARKER", marker)
	if err = os.WriteFile(
		hook,
		[]byte("#!/bin/sh\nprintf invoked > \"$MINTCLAW_TEST_FSMONITOR_MARKER\"\nexit 1\n"),
		0o700,
	); err != nil {
		t.Fatal(err)
	}
	runCodingScopeGit(t, fixture.root, "config", "core.fsmonitor", hook)
	if _, err = catalog.resolve(
		t.Context(), "mintclaw", catalog.List()[0].Revision, worker.TaskModeMutate,
	); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository fsmonitor executed during clean inspection: %v", err)
	}
}

func TestCodingMutationConfigurationRejectsLinkedWorktreeSource(t *testing.T) {
	fixture := newCodingScopeFixture(t, []worker.TaskMode{worker.TaskModeMutate})
	linkedRoot := filepath.Join(fixture.baseDir, "linked-source")
	runCodingScopeGit(t, fixture.root, "worktree", "add", "-b", "linked-source", linkedRoot)
	policy := fixture.scopes["mintclaw"]
	policy.Root = linkedRoot
	if _, err := normalizeCodingScopes(
		map[string]CodingScopePolicy{"mintclaw": policy},
		fixture.baseDir,
	); err == nil || !strings.Contains(err.Error(), "linked worktree") {
		t.Fatalf("linked worktree source error = %v", err)
	}
}

func TestCodingMutationConfigurationRejectsSubmoduleSource(t *testing.T) {
	fixture := newCodingScopeFixture(t, []worker.TaskMode{worker.TaskModeMutate})
	origin := filepath.Join(fixture.baseDir, "submodule-origin")
	if err := os.Mkdir(origin, 0o700); err != nil {
		t.Fatal(err)
	}
	runCodingScopeGit(t, origin, "init", "-b", "main")
	runCodingScopeGit(t, origin, "config", "user.email", "test@example.com")
	runCodingScopeGit(t, origin, "config", "user.name", "MintClaw Test")
	if err := os.WriteFile(filepath.Join(origin, "README.md"), []byte("submodule\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runCodingScopeGit(t, origin, "add", "README.md")
	runCodingScopeGit(t, origin, "commit", "-m", "fixture")
	runCodingScopeGit(
		t,
		fixture.root,
		"-c",
		"protocol.file.allow=always",
		"submodule",
		"add",
		origin,
		"nested",
	)
	runCodingScopeGit(t, fixture.root, "commit", "-am", "add submodule")

	policy := fixture.scopes["mintclaw"]
	policy.Root = filepath.Join(fixture.root, "nested")
	if _, err := normalizeCodingScopes(
		map[string]CodingScopePolicy{"mintclaw": policy},
		fixture.baseDir,
	); err == nil || !strings.Contains(err.Error(), "submodule") {
		t.Fatalf("submodule source error = %v", err)
	}
}

func TestCodingMutationConfigurationRejectsDetachedAndUnbornSources(t *testing.T) {
	t.Run("detached", func(t *testing.T) {
		fixture := newCodingScopeFixture(t, []worker.TaskMode{worker.TaskModeMutate})
		runCodingScopeGit(t, fixture.root, "checkout", "--detach")
		if _, err := normalizeCodingScopes(fixture.scopes, fixture.baseDir); err == nil ||
			!strings.Contains(err.Error(), "checked-out Git branch") {
			t.Fatalf("detached source error = %v", err)
		}
	})
	t.Run("unborn", func(t *testing.T) {
		fixture := newCodingScopeFixture(t, []worker.TaskMode{worker.TaskModeMutate})
		unborn := filepath.Join(fixture.baseDir, "unborn")
		if err := os.Mkdir(unborn, 0o700); err != nil {
			t.Fatal(err)
		}
		runCodingScopeGit(t, unborn, "init", "-b", "main")
		policy := fixture.scopes["mintclaw"]
		policy.Root = unborn
		if _, err := normalizeCodingScopes(
			map[string]CodingScopePolicy{"mintclaw": policy},
			fixture.baseDir,
		); err == nil || !strings.Contains(err.Error(), "committed HEAD") {
			t.Fatalf("unborn source error = %v", err)
		}
	})
}

func TestCodingScopeCatalogRejectsSameContentExecutableReplacement(t *testing.T) {
	fixture := newCodingScopeFixture(t, []worker.TaskMode{worker.TaskModeInvestigate})
	catalog, err := NewCodingScopeCatalog(fixture.scopes)
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
	); !errors.Is(err, ErrCodingScopeChanged) {
		t.Fatalf("replaced executable error = %v", err)
	}
}

func TestCodingScopeCatalogRejectsDirectoryReplacement(t *testing.T) {
	fixture := newCodingScopeFixture(t, []worker.TaskMode{worker.TaskModeInvestigate})
	catalog, err := NewCodingScopeCatalog(fixture.scopes)
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
	); !errors.Is(err, ErrCodingScopeChanged) {
		t.Fatalf("replaced root error = %v", err)
	}
}

func TestCodingScopeCatalogRejectsIntermediateSymlinkReplacement(t *testing.T) {
	fixture := newCodingScopeFixture(t, []worker.TaskMode{worker.TaskModeInvestigate})
	catalog, err := NewCodingScopeCatalog(fixture.scopes)
	if err != nil {
		t.Fatal(err)
	}
	original := fixture.baseDir + "-original"
	if err = os.Rename(fixture.baseDir, original); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(original, fixture.baseDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Remove(fixture.baseDir)
		_ = os.Rename(original, fixture.baseDir)
	})
	if _, err = catalog.resolve(
		t.Context(), "mintclaw", catalog.List()[0].Revision, worker.TaskModeInvestigate,
	); !errors.Is(err, ErrCodingScopeChanged) {
		t.Fatalf("intermediate symlink replacement error = %v", err)
	}
}

type codingScopeFixture struct {
	baseDir          string
	root             string
	home             string
	worktreeParent   string
	workerExecutable string
	scopes           map[string]CodingScopePolicy
}

func newCodingScopeFixture(t *testing.T, modes []worker.TaskMode) codingScopeFixture {
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
	runCodingScopeGit(t, root, "init", "-b", "main")
	runCodingScopeGit(t, root, "config", "user.email", "test@example.com")
	runCodingScopeGit(t, root, "config", "user.name", "MintClaw Test")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runCodingScopeGit(t, root, "add", "README.md")
	runCodingScopeGit(t, root, "commit", "-m", "fixture")
	workerExecutable := filepath.Join(baseDir, "mintclaw")
	if err := os.WriteFile(workerExecutable, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	policy := CodingScopePolicy{
		Revision: "revision-one", Kind: codingscope.KindGitProject,
		SourceParent: baseDir, Root: root, AllowedProfiles: modes,
		WorkerExecutable: workerExecutable, WorkerProtocolVersion: CodingWorkerProtocolV3,
		MintClawHome: home, CredentialSource: CodingCredentialSourceNative,
		ProviderProfile: CodingProviderProfileDefault, Model: "gpt-test", Provider: "openai",
	}
	if containsIsolatedCodingProfile(modes) {
		policy.WorktreeParent = worktreeParent
		policy.BranchPrefix = CodingBranchPrefix
	}
	scopes, err := normalizeCodingScopes(
		map[string]CodingScopePolicy{"mintclaw": policy},
		baseDir,
	)
	if err != nil {
		t.Fatal(err)
	}
	normalized := scopes["mintclaw"]
	return codingScopeFixture{
		baseDir: baseDir, root: normalized.Root, home: normalized.MintClawHome,
		worktreeParent:   normalized.WorktreeParent,
		workerExecutable: normalized.WorkerExecutable, scopes: scopes,
	}
}

func runCodingScopeGit(t *testing.T, root string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}
