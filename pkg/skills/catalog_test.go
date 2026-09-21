package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGatewaySkillRootsOrderAndScopes(t *testing.T) {
	tmp := t.TempDir()
	workspace := filepath.Join(tmp, "workspace")
	mintclawHome := filepath.Join(tmp, "mintclaw-home")
	userHome := filepath.Join(tmp, "user-home")
	builtin := filepath.Join(tmp, "builtin")

	roots := GatewaySkillRoots(workspace, mintclawHome, userHome, builtin)

	require.Len(t, roots, 4)
	assert.Equal(t, filepath.Join(workspace, "skills"), roots[0].Path)
	assert.Equal(t, SkillScopeWorkspace, roots[0].Scope)
	assert.Equal(t, 0, roots[0].Priority)
	assert.Equal(t, filepath.Join(userHome, ".agents", "skills"), roots[1].Path)
	assert.Equal(t, SkillScopeUser, roots[1].Scope)
	assert.Equal(t, 1, roots[1].Priority)
	assert.Equal(t, filepath.Join(mintclawHome, "skills"), roots[2].Path)
	assert.Equal(t, SkillScopeGlobal, roots[2].Scope)
	assert.Equal(t, builtin, roots[3].Path)
	assert.Equal(t, SkillScopeBuiltin, roots[3].Scope)
}

func TestCodingSkillRootsWalkFromWorkingDirectoryToProjectRoot(t *testing.T) {
	tmp := t.TempDir()
	project := filepath.Join(tmp, "project")
	workingDirectory := filepath.Join(project, "a", "b")
	require.NoError(t, os.MkdirAll(workingDirectory, 0o755))

	roots, err := CodingSkillRoots(
		project,
		workingDirectory,
		filepath.Join(tmp, "mintclaw-home"),
		filepath.Join(tmp, "user-home"),
		filepath.Join(tmp, "builtin"),
	)
	require.NoError(t, err)
	canonicalProject, err := filepath.EvalSymlinks(project)
	require.NoError(t, err)
	canonicalWorkingDirectory, err := filepath.EvalSymlinks(workingDirectory)
	require.NoError(t, err)

	require.Len(t, roots, 6)
	assert.Equal(t, filepath.Join(canonicalWorkingDirectory, ".agents", "skills"), roots[0].Path)
	assert.Equal(t, filepath.Join(canonicalProject, "a", ".agents", "skills"), roots[1].Path)
	assert.Equal(t, filepath.Join(canonicalProject, ".agents", "skills"), roots[2].Path)
	for _, root := range roots[:3] {
		assert.Equal(t, SkillScopeRepository, root.Scope)
		assert.Equal(t, SkillRuntimeCoding, root.Runtime)
		assert.Equal(t, SkillTrustProject, root.Trust)
	}
	assert.Equal(t, SkillScopeUser, roots[3].Scope)
	assert.Equal(t, SkillScopeGlobal, roots[4].Scope)
	assert.Equal(t, SkillScopeBuiltin, roots[5].Scope)
}

func TestCodingSkillRootsRejectWorkingDirectoryOutsideProject(t *testing.T) {
	tmp := t.TempDir()
	project := filepath.Join(tmp, "project")
	outside := filepath.Join(tmp, "outside")
	require.NoError(t, os.MkdirAll(project, 0o755))
	require.NoError(t, os.MkdirAll(outside, 0o755))

	_, err := CodingSkillRoots(project, outside, "", "", "")

	require.Error(t, err)
	assert.ErrorContains(t, err, "must be inside project root")
}

func TestCodingSkillRootsDoNotRequireOrCreateProject(t *testing.T) {
	tmp := t.TempDir()
	project := filepath.Join(tmp, "missing-project")
	workingDirectory := filepath.Join(project, "nested")

	roots, err := CodingSkillRoots(project, workingDirectory, "", "", "")

	require.NoError(t, err)
	canonicalProject, err := canonicalProspectivePath(project)
	require.NoError(t, err)
	canonicalWorkingDirectory, err := canonicalProspectivePath(workingDirectory)
	require.NoError(t, err)
	require.Len(t, roots, 2)
	assert.Equal(t, filepath.Join(canonicalWorkingDirectory, ".agents", "skills"), roots[0].Path)
	assert.Equal(t, filepath.Join(canonicalProject, ".agents", "skills"), roots[1].Path)
	_, statErr := os.Stat(project)
	assert.True(t, os.IsNotExist(statErr))
}

func TestCatalogNearestRepositorySkillShadowsOtherScopes(t *testing.T) {
	tmp := t.TempDir()
	project := filepath.Join(tmp, "project")
	workingDirectory := filepath.Join(project, "nested")
	userHome := filepath.Join(tmp, "user-home")
	require.NoError(t, os.MkdirAll(workingDirectory, 0o755))

	nearestRoot := filepath.Join(workingDirectory, ".agents", "skills")
	projectRoot := filepath.Join(project, ".agents", "skills")
	userRoot := filepath.Join(userHome, ".agents", "skills")
	createSkillDir(t, nearestRoot, "nearest", "shared-skill", "nearest repository version")
	createSkillDir(t, projectRoot, "project", "Shared-Skill", "project version")
	createSkillDir(t, userRoot, "user", "shared-skill", "user version")

	roots, err := CodingSkillRoots(project, workingDirectory, "", userHome, "")
	require.NoError(t, err)
	catalog := NewSkillsLoader(roots).Discover()

	require.Len(t, catalog.Skills, 1)
	assert.Equal(t, "nearest repository version", catalog.Skills[0].Description)
	assert.Equal(t, SkillScopeRepository, catalog.Skills[0].Scope)
	require.Len(t, catalog.Diagnostics, 2)
	for _, diagnostic := range catalog.Diagnostics {
		assert.Equal(t, CatalogDiagnosticShadowed, diagnostic.Kind)
		assert.Equal(t, catalog.Skills[0].Path, diagnostic.WinnerPath)
	}
}

func TestCatalogContainsSymlinksWithinRootAndRejectsEscapes(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "skills")
	insideParent := filepath.Join(root, "targets")
	outside := filepath.Join(tmp, "outside")
	createSkillDir(t, insideParent, "inside", "inside-skill", "inside target")
	createSkillDir(t, outside, "outside", "outside-skill", "outside target")
	require.NoError(t, os.MkdirAll(root, 0o755))

	if err := os.Symlink(filepath.Join(insideParent, "inside"), filepath.Join(root, "inside-link")); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	require.NoError(t, os.Symlink(filepath.Join(outside, "outside"), filepath.Join(root, "outside-link")))

	catalog := NewSkillsLoader([]SkillRoot{{
		Path: root, Scope: SkillScopeUser, Runtime: SkillRuntimeShared, Trust: SkillTrustUser,
	}}).Discover()

	require.Len(t, catalog.Skills, 1)
	assert.Equal(t, "inside-skill", catalog.Skills[0].Name)
	assert.Contains(t, catalog.Diagnostics, CatalogDiagnostic{
		Kind:    CatalogDiagnosticPathEscape,
		Scope:   SkillScopeUser,
		Path:    filepath.Join(root, "outside-link"),
		Message: "skill directory resolves outside its discovery root or is not a readable directory",
	})
}

func TestCatalogRejectsSkillFileSymlinkOutsideRoot(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "skills")
	skillDirectory := filepath.Join(root, "escaped-file")
	outsideFile := filepath.Join(tmp, "outside-SKILL.md")
	require.NoError(t, os.MkdirAll(skillDirectory, 0o755))
	require.NoError(t, os.WriteFile(outsideFile, []byte(
		"---\nname: escaped-file\ndescription: must not load\n---\n",
	), 0o644))
	if err := os.Symlink(outsideFile, filepath.Join(skillDirectory, "SKILL.md")); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}

	catalog := NewSkillsLoader([]SkillRoot{{
		Path: root, Scope: SkillScopeUser, Runtime: SkillRuntimeShared, Trust: SkillTrustUser,
	}}).Discover()

	assert.Empty(t, catalog.Skills)
	require.Len(t, catalog.Diagnostics, 1)
	assert.Equal(t, CatalogDiagnosticPathEscape, catalog.Diagnostics[0].Kind)
	canonicalSkillDirectory, err := filepath.EvalSymlinks(skillDirectory)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(canonicalSkillDirectory, "SKILL.md"), catalog.Diagnostics[0].Path)
}

func TestCatalogMetadataReadIsBoundedAndObservable(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "skills")
	skillDirectory := filepath.Join(root, "large-skill")
	require.NoError(t, os.MkdirAll(skillDirectory, 0o755))
	content := "---\nname: large-skill\ndescription: bounded metadata\n---\n\n" +
		strings.Repeat("body content that must not be read for discovery\n", MaxMetadataBytes)
	require.NoError(t, os.WriteFile(filepath.Join(skillDirectory, "SKILL.md"), []byte(content), 0o644))

	catalog := NewSkillsLoader([]SkillRoot{{
		Path: root, Scope: SkillScopeUser, Runtime: SkillRuntimeShared, Trust: SkillTrustUser,
	}}).Discover()

	require.Len(t, catalog.Skills, 1)
	assert.Equal(t, "bounded metadata", catalog.Skills[0].Description)
	require.Len(t, catalog.Diagnostics, 1)
	assert.Equal(t, CatalogDiagnosticMetadataTruncated, catalog.Diagnostics[0].Kind)
}
