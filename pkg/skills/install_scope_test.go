package skills

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveSkillInstallTargetUsesExplicitScope(t *testing.T) {
	home := canonicalInstallScopeTempDir(t)
	repository := canonicalInstallScopeTempDir(t)
	workspace := canonicalInstallScopeTempDir(t)
	installContext := SkillInstallContext{UserHome: home, RepositoryRoot: repository, Workspace: workspace}

	tests := map[SkillInstallScope]string{
		SkillInstallScopeUser:       filepath.Join(home, ".agents", "skills"),
		SkillInstallScopeRepository: filepath.Join(repository, ".agents", "skills"),
		SkillInstallScopeWorkspace:  filepath.Join(workspace, "skills"),
	}
	for scope, expected := range tests {
		t.Run(string(scope), func(t *testing.T) {
			target, err := ResolveSkillInstallTarget(scope, installContext)
			require.NoError(t, err)
			assert.Equal(t, scope, target.Scope)
			assert.Equal(t, expected, target.Root)
		})
	}
	_, err := ResolveSkillInstallTarget(SkillInstallScopeSystem, installContext)
	assert.ErrorContains(t, err, "immutable")
}

func TestParseSkillInstallScopeUsesOnlyProvidedDefault(t *testing.T) {
	scope, err := ParseSkillInstallScope("", SkillInstallScopeUser)
	require.NoError(t, err)
	assert.Equal(t, SkillInstallScopeUser, scope)

	_, err = ParseSkillInstallScope("", "")
	assert.ErrorContains(t, err, "must be user, repository, or workspace")
}

func TestResolveSkillInstallTargetRejectsUnavailableExplicitScope(t *testing.T) {
	_, err := ResolveSkillInstallTarget(SkillInstallScopeRepository, SkillInstallContext{
		UserHome:  canonicalInstallScopeTempDir(t),
		Workspace: canonicalInstallScopeTempDir(t),
	})
	assert.ErrorContains(t, err, "repository skill install scope is unavailable")
}

func TestEnsureSkillInstallRootCreatesOnlySelectedScope(t *testing.T) {
	home := canonicalInstallScopeTempDir(t)
	repository := canonicalInstallScopeTempDir(t)
	workspace := canonicalInstallScopeTempDir(t)
	target, err := ResolveSkillInstallTarget(SkillInstallScopeRepository, SkillInstallContext{
		UserHome: home, RepositoryRoot: repository, Workspace: workspace,
	})
	require.NoError(t, err)
	require.NoError(t, EnsureSkillInstallRoot(target))
	assert.DirExists(t, filepath.Join(repository, ".agents", "skills"))
	assert.NoDirExists(t, filepath.Join(home, ".agents"))
	assert.NoDirExists(t, filepath.Join(workspace, "skills"))
}

func TestResolveSkillInstallTargetRejectsSymlinkInsideScope(t *testing.T) {
	home := canonicalInstallScopeTempDir(t)
	outside := canonicalInstallScopeTempDir(t)
	if err := os.Symlink(outside, filepath.Join(home, ".agents")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, err := ResolveSkillInstallTarget(SkillInstallScopeUser, SkillInstallContext{UserHome: home})
	assert.ErrorContains(t, err, "must be a real directory")
	assert.NoDirExists(t, filepath.Join(outside, "skills"))
}

func canonicalInstallScopeTempDir(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	resolved, err := filepath.EvalSymlinks(directory)
	require.NoError(t, err)
	return resolved
}
