package skills

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type scopedManagerRegistry struct {
	content map[string]string
}

func (registry *scopedManagerRegistry) Name() string { return "fixture" }

func (registry *scopedManagerRegistry) ResolveInstallDirName(string) (string, error) {
	return "shared-skill", nil
}

func (registry *scopedManagerRegistry) SkillURL(slug, version string) string {
	return "https://example.test/" + slug + "/" + version
}

func (registry *scopedManagerRegistry) Search(context.Context, string, int) ([]SearchResult, error) {
	return nil, nil
}

func (registry *scopedManagerRegistry) GetSkillMeta(context.Context, string) (*SkillMeta, error) {
	return nil, nil
}

func (registry *scopedManagerRegistry) DownloadAndInstall(
	_ context.Context,
	slug string,
	version string,
	targetDir string,
) (*InstallResult, error) {
	if version == "" {
		version = "v1"
	}
	if err := os.MkdirAll(filepath.Join(targetDir, "agents"), 0o755); err != nil {
		return nil, err
	}
	body := registry.content[version]
	if body == "" {
		body = "# Shared skill\n"
	}
	markdown := fmt.Sprintf(
		"---\nname: shared-skill\ndescription: Shared fixture\n---\n%s",
		body,
	)
	if err := os.WriteFile(filepath.Join(targetDir, "SKILL.md"), []byte(markdown), 0o600); err != nil {
		return nil, err
	}
	manifest := "schema_version: 1\nproducts: [coding, gateway]\nrequirements:\n  executables: [missing-s6-bin]\n"
	if err := os.WriteFile(filepath.Join(targetDir, "agents", "mintclaw.yaml"), []byte(manifest), 0o600); err != nil {
		return nil, err
	}
	return &InstallResult{Version: version}, nil
}

func TestScopedSkillManagerUserInstallIsVisibleToBothRuntimes(t *testing.T) {
	home := canonicalInstallScopeTempDir(t)
	repository := canonicalInstallScopeTempDir(t)
	workspace := canonicalInstallScopeTempDir(t)
	target, err := ResolveSkillInstallTarget(SkillInstallScopeUser, SkillInstallContext{
		UserHome: home, RepositoryRoot: repository, Workspace: workspace,
	})
	require.NoError(t, err)
	manager := newScopedManagerFixture(t)
	plan, err := manager.Install(t.Context(), SkillInstallRequest{
		Target: target, Registry: "fixture", Slug: "owner/source-a/shared-skill",
	})
	require.NoError(t, err)
	assert.True(t, plan.Applied)
	assert.Equal(t, SkillInstallScopeUser, plan.Scope)
	assert.Equal(t, filepath.Join(home, ".agents", "skills", "shared-skill"), plan.Target)
	assert.Len(t, plan.Compatibility, 2)
	assert.NotEmpty(t, plan.DependencyGaps)

	gateway := NewSkillsLoader(GatewaySkillRoots(workspace, home, "")).Discover()
	codingRoots, err := CodingSkillRoots(repository, repository, home, "")
	require.NoError(t, err)
	coding := NewSkillsLoader(codingRoots).Discover()
	assert.Equal(t, plan.Target, filepath.Dir(catalogSkillPath(t, gateway, "shared-skill")))
	assert.Equal(t, plan.Target, filepath.Dir(catalogSkillPath(t, coding, "shared-skill")))
	assert.NoDirExists(t, filepath.Join(workspace, "skills", "shared-skill"))
	assert.NoDirExists(t, filepath.Join(repository, ".agents", "skills", "shared-skill"))
}

func TestScopedSkillManagerDryRunDoesNotCreateSelectedRoot(t *testing.T) {
	home := canonicalInstallScopeTempDir(t)
	target, err := ResolveSkillInstallTarget(SkillInstallScopeUser, SkillInstallContext{UserHome: home})
	require.NoError(t, err)
	plan, err := newScopedManagerFixture(t).Install(t.Context(), SkillInstallRequest{
		Target: target, Registry: "fixture", Slug: "owner/source-a/shared-skill", DryRun: true,
	})
	require.NoError(t, err)
	assert.True(t, plan.DryRun)
	assert.False(t, plan.Applied)
	assert.NoDirExists(t, filepath.Join(home, ".agents"))
}

func TestScopedSkillManagerReplacementPreservesImmutableOrigin(t *testing.T) {
	home := canonicalInstallScopeTempDir(t)
	target, err := ResolveSkillInstallTarget(SkillInstallScopeUser, SkillInstallContext{UserHome: home})
	require.NoError(t, err)
	manager := newScopedManagerFixture(t)
	_, err = manager.Install(t.Context(), SkillInstallRequest{
		Target: target, Registry: "fixture", Slug: "owner/source-a/shared-skill",
	})
	require.NoError(t, err)

	_, err = manager.Install(t.Context(), SkillInstallRequest{
		Target: target, Registry: "fixture", Slug: "owner/source-b/shared-skill", Replace: true,
	})
	assert.ErrorContains(t, err, "would change immutable origin")
	origin, readErr := ReadSkillOrigin(filepath.Join(target.Root, "shared-skill"))
	require.NoError(t, readErr)
	assert.Equal(t, "owner/source-a/shared-skill", origin.Slug)
}

func TestScopedSkillManagerUpdateUsesRecordedOrigin(t *testing.T) {
	home := canonicalInstallScopeTempDir(t)
	target, err := ResolveSkillInstallTarget(SkillInstallScopeUser, SkillInstallContext{UserHome: home})
	require.NoError(t, err)
	manager := newScopedManagerFixture(t)
	_, err = manager.Install(t.Context(), SkillInstallRequest{
		Target: target, Registry: "fixture", Slug: "owner/source-a/shared-skill", Version: "v1",
	})
	require.NoError(t, err)
	plan, err := manager.Update(t.Context(), target, "shared-skill", "v2", false)
	require.NoError(t, err)
	assert.Equal(t, SkillMutationUpdate, plan.Operation)
	assert.Equal(t, SkillMutationReplace, plan.Action)
	assert.Equal(t, "owner/source-a/shared-skill", plan.Origin.Slug)
	assert.Equal(t, "v2", plan.ResolvedVersion)
	content, err := os.ReadFile(filepath.Join(target.Root, "shared-skill", "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "version two")
}

func TestScopedSkillManagerRemoveCannotMutateOtherScopes(t *testing.T) {
	home := canonicalInstallScopeTempDir(t)
	repository := canonicalInstallScopeTempDir(t)
	workspace := canonicalInstallScopeTempDir(t)
	installContext := SkillInstallContext{UserHome: home, RepositoryRoot: repository, Workspace: workspace}
	repositoryTarget, err := ResolveSkillInstallTarget(SkillInstallScopeRepository, installContext)
	require.NoError(t, err)
	userTarget, err := ResolveSkillInstallTarget(SkillInstallScopeUser, installContext)
	require.NoError(t, err)
	manager := newScopedManagerFixture(t)
	_, err = manager.Install(t.Context(), SkillInstallRequest{
		Target: repositoryTarget, Registry: "fixture", Slug: "owner/source-a/shared-skill",
	})
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(userTarget.Root, "keep"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(userTarget.Root, "keep", "sentinel"), []byte("keep"), 0o600))

	plan, err := manager.Remove(repositoryTarget, "shared-skill", false)
	require.NoError(t, err)
	assert.True(t, plan.Applied)
	assert.NoDirExists(t, filepath.Join(repositoryTarget.Root, "shared-skill"))
	assert.FileExists(t, filepath.Join(userTarget.Root, "keep", "sentinel"))
	assert.NoDirExists(t, filepath.Join(workspace, "skills"))
}

func TestScopedSkillManagerMovePlansAndPreservesValidatedSkill(t *testing.T) {
	home := canonicalInstallScopeTempDir(t)
	repository := canonicalInstallScopeTempDir(t)
	installContext := SkillInstallContext{UserHome: home, RepositoryRoot: repository}
	userTarget, err := ResolveSkillInstallTarget(SkillInstallScopeUser, installContext)
	require.NoError(t, err)
	repositoryTarget, err := ResolveSkillInstallTarget(SkillInstallScopeRepository, installContext)
	require.NoError(t, err)
	manager := newScopedManagerFixture(t)
	installed, err := manager.Install(t.Context(), SkillInstallRequest{
		Target: userTarget, Registry: "fixture", Slug: "owner/source-a/shared-skill",
	})
	require.NoError(t, err)

	dryRun, err := manager.Move(SkillMoveRequest{
		Source: userTarget, Target: repositoryTarget, Name: "shared-skill", DryRun: true,
	})
	require.NoError(t, err)
	assert.Equal(t, SkillMutationMove, dryRun.Operation)
	assert.Equal(t, SkillInstallScopeUser, dryRun.SourceScope)
	assert.Equal(t, SkillInstallScopeRepository, dryRun.Scope)
	assert.False(t, dryRun.Applied)
	assert.DirExists(t, installed.Target)
	assert.NoDirExists(t, repositoryTarget.Root)

	plan, err := manager.Move(SkillMoveRequest{
		Source: userTarget, Target: repositoryTarget, Name: "shared-skill",
	})
	require.NoError(t, err)
	assert.True(t, plan.Applied)
	assert.NoDirExists(t, installed.Target)
	assert.DirExists(t, plan.Target)
	origin, err := ReadSkillOrigin(plan.Target)
	require.NoError(t, err)
	assert.Equal(t, "owner/source-a/shared-skill", origin.Slug)
	assert.Equal(t, installed.Origin.InstalledVersion, origin.InstalledVersion)
}

func TestScopedSkillManagerMoveRetainsCompleteDestinationWhenSourceCleanupPartiallyFails(t *testing.T) {
	home := canonicalInstallScopeTempDir(t)
	repository := canonicalInstallScopeTempDir(t)
	installContext := SkillInstallContext{UserHome: home, RepositoryRoot: repository}
	userTarget, err := ResolveSkillInstallTarget(SkillInstallScopeUser, installContext)
	require.NoError(t, err)
	repositoryTarget, err := ResolveSkillInstallTarget(SkillInstallScopeRepository, installContext)
	require.NoError(t, err)
	manager := newScopedManagerFixture(t)
	installed, err := manager.Install(t.Context(), SkillInstallRequest{
		Target: userTarget, Registry: "fixture", Slug: "owner/source-a/shared-skill",
	})
	require.NoError(t, err)
	original, err := NewWorkspaceSkillInventory(userTarget.OwnerRoot).Inspect("shared-skill")
	require.NoError(t, err)
	require.True(t, original.Valid)
	manager.removeMovedSource = func(path string) error {
		if removeErr := os.Remove(filepath.Join(path, "SKILL.md")); removeErr != nil {
			return removeErr
		}
		return errors.New("injected partial source cleanup failure")
	}

	plan, err := manager.Move(SkillMoveRequest{
		Source: userTarget, Target: repositoryTarget, Name: "shared-skill",
	})
	require.ErrorContains(t, err, "complete destination retained")
	assert.False(t, plan.Applied)
	assert.NoDirExists(t, installed.Target)
	assert.DirExists(t, plan.Target)
	destination, inspectErr := NewWorkspaceSkillInventory(repositoryTarget.OwnerRoot).Inspect("shared-skill")
	require.NoError(t, inspectErr)
	assert.True(t, destination.Valid)
	assert.Equal(t, original.Revision, destination.Revision)
	backups, globErr := filepath.Glob(filepath.Join(userTarget.Root, ".shared-skill.mintclaw-move-*"))
	require.NoError(t, globErr)
	assert.Len(t, backups, 1)
	assert.NoFileExists(t, filepath.Join(backups[0], "SKILL.md"))
}

func newScopedManagerFixture(t *testing.T) *ScopedSkillManager {
	t.Helper()
	registries := NewRegistryManager()
	registries.AddRegistry(&scopedManagerRegistry{content: map[string]string{
		"v1": "version one\n",
		"v2": "version two\n",
	}})
	environments := map[SkillRuntime]SkillCompatibilityEnvironment{}
	for _, runtimeProduct := range []SkillRuntime{SkillRuntimeCoding, SkillRuntimeGateway} {
		environment := NewSkillCompatibilityEnvironment(runtimeProduct)
		environment.ExecutableAvailable = func(string) bool { return false }
		environments[runtimeProduct] = environment
	}
	return NewScopedSkillManager(registries, environments)
}

func catalogSkillPath(t *testing.T, catalog SkillCatalog, name string) string {
	t.Helper()
	for _, skill := range catalog.Skills {
		if skill.Name == name {
			return skill.Path
		}
	}
	t.Fatalf("skill %q not found in catalog", name)
	return ""
}
