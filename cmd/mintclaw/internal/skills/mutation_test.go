package skills

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeskills "github.com/bogdanovich/mintclaw/pkg/skills"
)

func TestInstallCommandDefaultsToSharedUserScope(t *testing.T) {
	home := realTempDir(t)
	workspace := realTempDir(t)
	t.Setenv("HOME", home)
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v3/repos/foo/bar":
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"default_branch": "main"}))
		case "/api/v3/repos/foo/bar/contents/.agents/skills/pr-review":
			require.NoError(t, json.NewEncoder(w).Encode([]map[string]any{{
				"type": "file", "name": "SKILL.md",
				"download_url": server.URL + "/raw/foo/bar/main/.agents/skills/pr-review/SKILL.md",
			}}))
		case "/raw/foo/bar/main/.agents/skills/pr-review/SKILL.md":
			_, _ = w.Write([]byte("---\nname: pr-review\ndescription: PR review skill\n---\n# PR Review\n"))
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()
	githubRegistry, ok := cfg.Tools.Skills.Registries.Get("github")
	require.True(t, ok)
	githubRegistry.BaseURL = server.URL
	cfg.Tools.Skills.Registries.Set("github", githubRegistry)

	d := &deps{cfg: cfg, workspace: workspace}
	cmd := newInstallCommand(d)
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{server.URL + "/foo/bar/tree/main/.agents/skills/pr-review"})
	require.NoError(t, cmd.Execute())
	target := filepath.Join(home, ".agents", "skills", "pr-review")
	assert.DirExists(t, target)
	assert.NoDirExists(t, filepath.Join(workspace, "skills", "pr-review"))
	assert.Contains(t, output.String(), target+" (user)")

	origin, err := runtimeskills.ReadSkillOrigin(target)
	require.NoError(t, err)
	assert.Equal(t, "foo/bar/.agents/skills/pr-review", origin.Slug)
}

func TestSkillMutationDryRunDoesNotCreateUserCatalog(t *testing.T) {
	home := realTempDir(t)
	workspace := realTempDir(t)
	t.Setenv("HOME", home)
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	d := &deps{cfg: cfg, workspace: workspace}

	_, target, err := d.scopedSkillManager(t.Context(), "user", "")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".agents", "skills"), target.Root)
	assert.NoDirExists(t, filepath.Join(home, ".agents"))
}

func TestRenderSkillMutationPlanIncludesScopeTargetAndGaps(t *testing.T) {
	plan := runtimeskills.SkillMutationPlan{
		Operation: runtimeskills.SkillMutationInstall,
		Action:    runtimeskills.SkillMutationCreate,
		Scope:     runtimeskills.SkillInstallScopeUser,
		Target:    "/home/operator/.agents/skills/example",
		SkillName: "example",
		DryRun:    true,
		DependencyGaps: []runtimeskills.SkillRequirementCheck{{
			Kind: "executable", Name: "gh", State: runtimeskills.SkillRequirementMissing,
		}},
	}
	var output bytes.Buffer
	require.NoError(t, renderSkillMutationPlan(&output, plan, false))
	assert.Contains(t, output.String(), "Dry run: install example")
	assert.Contains(t, output.String(), "/home/operator/.agents/skills/example (user)")
	assert.Contains(t, output.String(), "missing executable gh")

	output.Reset()
	require.NoError(t, renderSkillMutationPlan(&output, plan, true))
	assert.Contains(t, output.String(), `"scope": "user"`)
	assert.Contains(t, output.String(), `"target": "/home/operator/.agents/skills/example"`)
}

func TestMoveCommandRequiresExplicitSourceScope(t *testing.T) {
	cmd := newMoveCommand(&deps{})
	assert.Equal(t, "user", cmd.Flags().Lookup("scope").DefValue)
	fromScope := cmd.Flags().Lookup("from-scope")
	require.NotNil(t, fromScope)
	assert.NotEmpty(t, fromScope.Annotations)
}

func TestSkillMutationRejectsSystemScope(t *testing.T) {
	_, err := runtimeskills.ParseSkillInstallScope("system", runtimeskills.SkillInstallScopeUser)
	assert.ErrorContains(t, err, "immutable")
}

func realTempDir(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return path
}
