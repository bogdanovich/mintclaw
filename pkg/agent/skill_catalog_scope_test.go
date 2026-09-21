package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/skills"
)

func TestGatewaySkillCatalogIncludesWorkspaceAndUserButNotRepository(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	userHome := filepath.Join(root, "user-home")
	t.Setenv("HOME", userHome)
	t.Setenv(config.EnvHome, filepath.Join(root, "mintclaw-home"))

	writeSkillCatalogFixture(t, filepath.Join(workspace, "skills"), "workspace-skill")
	writeSkillCatalogFixture(t, filepath.Join(workspace, ".agents", "skills"), "repository-skill")
	writeSkillCatalogFixture(t, filepath.Join(userHome, ".agents", "skills"), "user-skill")

	builder := NewContextBuilder(workspace)
	catalog := builder.skillsLoader.Discover()

	assert.Equal(t, []string{"workspace-skill", "user-skill"}, skillCatalogNames(catalog.Skills))
	assert.Equal(t, []skills.SkillScope{
		skills.SkillScopeWorkspace,
		skills.SkillScopeUser,
	}, skillCatalogScopes(catalog.Skills))
}

func TestCodingSkillCatalogIncludesNestedRepositoryAndUserButNotGatewayWorkspace(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	workingDirectory := filepath.Join(project, "nested")
	stateRoot := filepath.Join(root, "state")
	userHome := filepath.Join(root, "user-home")
	t.Setenv("HOME", userHome)
	t.Setenv(config.EnvHome, filepath.Join(root, "mintclaw-home"))
	require.NoError(t, os.MkdirAll(workingDirectory, 0o755))

	writeSkillCatalogFixture(t, filepath.Join(project, "skills"), "gateway-workspace-skill")
	writeSkillCatalogFixture(t, filepath.Join(project, ".agents", "skills"), "project-skill")
	writeSkillCatalogFixture(t, filepath.Join(workingDirectory, ".agents", "skills"), "nested-skill")
	writeSkillCatalogFixture(t, filepath.Join(userHome, ".agents", "skills"), "user-skill")

	layout, err := NewCodingRuntimeLayout(
		"thread-skill-scopes",
		project,
		stateRoot,
		[]string{project, workingDirectory},
	)
	require.NoError(t, err)
	builder, err := newCodingContextBuilder(layout)
	require.NoError(t, err)
	catalog := builder.skillsLoader.Discover()

	assert.Equal(t, []string{"nested-skill", "project-skill", "user-skill"}, skillCatalogNames(catalog.Skills))
	assert.Equal(t, []skills.SkillScope{
		skills.SkillScopeRepository,
		skills.SkillScopeRepository,
		skills.SkillScopeUser,
	}, skillCatalogScopes(catalog.Skills))
	assert.NotContains(t, builder.skillsLoader.SkillRoots(), filepath.Join(project, "skills"))
}

func TestGetSkillsInfoExposesCatalogReportAndDiagnostics(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	t.Setenv("HOME", filepath.Join(root, "user-home"))
	t.Setenv(config.EnvHome, filepath.Join(root, "mintclaw-home"))
	writeSkillCatalogFixture(t, filepath.Join(workspace, "skills"), "visible-skill")

	info := NewContextBuilder(workspace).WithSkillCatalogContextWindow(1_000).GetSkillsInfo()

	assert.Equal(t, 1, info["total"])
	assert.Equal(t, []string{"visible-skill"}, info["names"])
	report, ok := info["catalog_report"].(skills.CatalogRenderReport)
	require.True(t, ok)
	assert.Equal(t, 1, report.TotalCount)
	_, ok = info["diagnostics"].([]skills.CatalogDiagnostic)
	assert.True(t, ok)
}

func TestGatewayAndCodingCatalogsUseSameActiveSystemGeneration(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	stateRoot := filepath.Join(root, "state")
	userHome := filepath.Join(root, "user-home")
	mintclawHome := filepath.Join(root, "mintclaw-home")
	t.Setenv("HOME", userHome)
	t.Setenv(config.EnvHome, mintclawHome)
	require.NoError(t, os.MkdirAll(project, 0o755))
	bundle, err := skills.EnsureSystemBundle(mintclawHome)
	require.NoError(t, err)

	gateway := NewContextBuilder(project)
	layout, err := NewCodingRuntimeLayout("thread-shared-system", project, stateRoot, []string{project})
	require.NoError(t, err)
	coding, err := newCodingContextBuilder(layout)
	require.NoError(t, err)

	assert.Contains(t, gateway.skillsLoader.SkillRoots(), bundle.Root)
	assert.Contains(t, coding.skillsLoader.SkillRoots(), bundle.Root)
	gatewaySkill, gatewayOK := skillByName(gateway.skillsLoader.Discover().Skills, "mintclaw-agent")
	codingSkill, codingOK := skillByName(coding.skillsLoader.Discover().Skills, "mintclaw-agent")
	require.True(t, gatewayOK)
	require.True(t, codingOK)
	assert.Equal(t, skills.SkillScopeSystem, gatewaySkill.Scope)
	assert.Equal(t, gatewaySkill.Path, codingSkill.Path)
	assert.Contains(t, gatewaySkill.Description, "$HOME/.agents/skills")
	assert.Equal(t, gatewaySkill.Description, codingSkill.Description)
}

func writeSkillCatalogFixture(t *testing.T, root, name string) {
	t.Helper()
	directory := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(directory, 0o755))
	content := "---\nname: " + name + "\ndescription: " + name + " description\n---\n\n# " + name + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(content), 0o644))
}

func skillCatalogNames(entries []skills.SkillInfo) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	return names
}

func skillCatalogScopes(entries []skills.SkillInfo) []skills.SkillScope {
	scopes := make([]skills.SkillScope, 0, len(entries))
	for _, entry := range entries {
		scopes = append(scopes, entry.Scope)
	}
	return scopes
}

func skillByName(entries []skills.SkillInfo, name string) (skills.SkillInfo, bool) {
	for _, entry := range entries {
		if entry.Name == name {
			return entry, true
		}
	}
	return skills.SkillInfo{}, false
}
