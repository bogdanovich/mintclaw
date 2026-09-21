package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bogdanovich/mintclaw/pkg/skills"
)

func TestCodingPromptPublishesCatalogAndInjectsFrozenSelectedSkill(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	skillDirectory := filepath.Join(root, "deploy")
	require.NoError(t, os.MkdirAll(skillDirectory, 0o755))
	skillPath := filepath.Join(skillDirectory, "SKILL.md")
	require.NoError(t, os.WriteFile(skillPath, []byte(
		"---\nname: deploy\ndescription: deploy the current project\n---\n\n# Original workflow\n\nRun the canary.\n",
	), 0o644))

	builder := newContextBuilderWithMemoryStoreAndSkills(t.TempDir(), NewMemoryStore(t.TempDir()), []skills.SkillRoot{{
		Path: root, Scope: skills.SkillScopeUser, Runtime: skills.SkillRuntimeShared, Trust: skills.SkillTrustUser,
	}})
	builder.codingPrompt = true
	builder.WithSkillCatalogContextWindow(128_000)

	staticPrompt := builder.BuildSystemPrompt()
	assert.Contains(t, staticPrompt, "<name>deploy</name>")
	assert.Contains(t, staticPrompt, "$skill-name")

	selected, err := builder.SelectSkillsForTurn([]string{"deploy"}, skills.SkillRuntimeCoding)
	require.NoError(t, err)
	require.Len(t, selected, 1)
	assert.NotEmpty(t, selected[0].Revision)
	require.NoError(t, os.WriteFile(skillPath, []byte(
		"---\nname: deploy\ndescription: deploy the current project\n---\n\n# Changed workflow\n\nSkip the canary.\n",
	), 0o644))

	messages := builder.BuildMessagesFromPrompt(PromptBuildRequest{
		CurrentMessage: "Use $deploy now",
		SelectedSkills: selected,
	})
	require.NotEmpty(t, messages)
	system := messages[0].Content
	assert.Contains(t, system, "# Original workflow")
	assert.Contains(t, system, "Run the canary.")
	assert.NotContains(t, system, "# Changed workflow")
	assert.Contains(t, system, selected[0].Revision)
	assert.Contains(t, system, selected[0].Path)

	rebuilt := builder.BuildMessagesFromPrompt(PromptBuildRequest{
		CurrentMessage: "continue",
		SelectedSkills: selected,
	})
	assert.Equal(t, system, rebuilt[0].Content)
}

func TestCodingSkillMentionsKeepRawTextAndIgnoreUnknownDollarTokens(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	for _, name := range []string{"deploy", "review"} {
		directory := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(directory, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(
			"---\nname: "+name+"\ndescription: "+name+" workflow\n---\n\n# "+name+"\n",
		), 0o644))
	}
	builder := newContextBuilderWithMemoryStoreAndSkills(t.TempDir(), NewMemoryStore(t.TempDir()), []skills.SkillRoot{{
		Path: root, Scope: skills.SkillScopeUser, Runtime: skills.SkillRuntimeShared, Trust: skills.SkillTrustUser,
	}})
	input := "Run $review, keep $HOME unchanged, then use $DEPLOY."

	names := builder.MentionedSkillNames(input, skills.SkillRuntimeCoding)

	assert.Equal(t, []string{"review", "deploy"}, names)
	assert.True(t, strings.Contains(input, "$review") && strings.Contains(input, "$DEPLOY"))
}

func TestCodingSkillCatalogAndSelectionRespectTurnProfileSuppression(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	skillDirectory := filepath.Join(root, "deploy")
	require.NoError(t, os.MkdirAll(skillDirectory, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDirectory, "SKILL.md"), []byte(
		"---\nname: deploy\ndescription: deploy workflow\n---\n\n# Deployment workflow\n",
	), 0o644))
	builder := newContextBuilderWithMemoryStoreAndSkills(t.TempDir(), NewMemoryStore(t.TempDir()), []skills.SkillRoot{{
		Path: root, Scope: skills.SkillScopeUser, Runtime: skills.SkillRuntimeShared, Trust: skills.SkillTrustUser,
	}})
	builder.codingPrompt = true
	selected, err := builder.SelectSkillsForTurn([]string{"deploy"}, skills.SkillRuntimeCoding)
	require.NoError(t, err)

	messages := builder.BuildMessagesFromPrompt(PromptBuildRequest{
		CurrentMessage:       "$deploy inspect",
		SelectedSkills:       selected,
		SuppressSkillContext: true,
	})

	require.NotEmpty(t, messages)
	assert.NotContains(t, messages[0].Content, "<name>deploy</name>")
	assert.NotContains(t, messages[0].Content, "# Deployment workflow")
}

func TestSelectedSkillContextPreservesSelectionOrder(t *testing.T) {
	context := buildSelectedSkillsContext([]skills.SelectedSkill{
		{Name: "second", Path: "/skills/second/SKILL.md", Revision: "sha256:2", Instructions: "second body"},
		{Name: "first", Path: "/skills/first/SKILL.md", Revision: "sha256:1", Instructions: "first body"},
	})

	assert.Less(t, strings.Index(context, "### Skill: second"), strings.Index(context, "### Skill: first"))
}

func TestSkillContextSnapshotCarriesStableRevisionIdentityWithoutInstructions(t *testing.T) {
	ts := &turnState{selectedSkills: []skills.SelectedSkill{{
		Name:         "deploy",
		Path:         "/skills/deploy/SKILL.md",
		Scope:        skills.SkillScopeUser,
		Revision:     "sha256:revision",
		Instructions: "secret prompt body",
	}}}

	ts.recordSkillContextSnapshot(skillContextTriggerInitialBuild, []string{"deploy"})
	snapshots := ts.skillContextSnapshotsSnapshot()

	require.Len(t, snapshots, 1)
	require.Len(t, snapshots[0].Selections, 1)
	assert.Equal(t, SkillRevisionIdentity{
		Name: "deploy", Path: "/skills/deploy/SKILL.md", Scope: "user", Revision: "sha256:revision",
	}, snapshots[0].Selections[0])
	assert.NotContains(t, snapshots[0].Selections[0].Revision, "secret prompt body")
}
