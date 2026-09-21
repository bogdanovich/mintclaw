package skills

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelectFreezesOrderedDeduplicatedInstructionsAndRevision(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	createSkillDir(t, root, "first", "first", "first instructions")
	createSkillDir(t, root, "second", "second", "second instructions")
	loader := NewSkillsLoader([]SkillRoot{{
		Path: root, Scope: SkillScopeUser, Runtime: SkillRuntimeShared, Trust: SkillTrustUser,
	}})

	selected, err := loader.Select([]SkillSelector{
		{Name: "second"},
		{Name: "FIRST"},
		{Name: "second"},
	}, SkillSelectionOptions{Runtime: SkillRuntimeCoding})

	require.NoError(t, err)
	require.Len(t, selected, 2)
	assert.Equal(t, "second", selected[0].Name)
	assert.Equal(t, "# second", selected[0].Instructions)
	assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, selected[0].Revision)
	assert.Equal(t, "first", selected[1].Name)
	before := selected[0]
	require.NoError(t, os.WriteFile(selected[0].Path, []byte(
		"---\nname: second\ndescription: changed\n---\n\nchanged instructions\n",
	), 0o644))
	assert.Equal(t, before, selected[0], "the admitted selection must remain an immutable value snapshot")
}

func TestSelectReturnsTypedSelectionFailures(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	createSkillDir(t, root, "coding", "coding", "coding instructions")
	catalog := NewSkillsLoader([]SkillRoot{{
		Path: root, Scope: SkillScopeRepository, Runtime: SkillRuntimeCoding, Trust: SkillTrustProject,
	}})

	tests := []struct {
		name     string
		selector SkillSelector
		options  SkillSelectionOptions
		want     SkillSelectionFailure
	}{
		{name: "unknown", selector: SkillSelector{Name: "missing"}, want: SkillSelectionUnknown},
		{
			name: "disabled", selector: SkillSelector{Name: "coding"},
			options: SkillSelectionOptions{Runtime: SkillRuntimeCoding, AllowedNames: []string{}},
			want:    SkillSelectionDisabled,
		},
		{
			name: "incompatible", selector: SkillSelector{Name: "coding"},
			options: SkillSelectionOptions{Runtime: SkillRuntimeGateway}, want: SkillSelectionIncompatible,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := catalog.Select([]SkillSelector{test.selector}, test.options)
			var selectionErr *SkillSelectionError
			require.ErrorAs(t, err, &selectionErr)
			assert.Equal(t, test.want, selectionErr.Kind)
		})
	}

	_, err := resolveSkillSelector([]SkillInfo{
		{Name: "duplicate", Path: "/one"},
		{Name: "DUPLICATE", Path: "/two"},
	}, SkillSelector{Name: "duplicate"})
	var selectionErr *SkillSelectionError
	require.ErrorAs(t, err, &selectionErr)
	assert.Equal(t, SkillSelectionAmbiguous, selectionErr.Kind)
	assert.Equal(t, []string{"/one", "/two"}, selectionErr.Candidates)
}

func TestSelectRejectsOversizedAndReplacedSkillFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	largeDirectory := filepath.Join(root, "large")
	require.NoError(t, os.MkdirAll(largeDirectory, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(largeDirectory, "SKILL.md"), []byte(
		"---\nname: large\ndescription: oversized instructions\n---\n\n"+
			strings.Repeat("x", MaxSkillInstructionBytes),
	), 0o644))
	loader := NewSkillsLoader([]SkillRoot{{Path: root, Scope: SkillScopeUser}})

	_, err := loader.Select([]SkillSelector{{Name: "large"}}, SkillSelectionOptions{})
	var selectionErr *SkillSelectionError
	require.ErrorAs(t, err, &selectionErr)
	assert.Equal(t, SkillSelectionTooLarge, selectionErr.Kind)

	replacementRoot := filepath.Join(t.TempDir(), "replacement")
	createSkillDir(t, replacementRoot, "target", "target", "outside")
	createSkillDir(t, root, "replace", "replace", "inside")
	catalog := loader.Discover()
	var replaceInfo SkillInfo
	for _, skill := range catalog.Skills {
		if skill.Name == "replace" {
			replaceInfo = skill
		}
	}
	require.NotEmpty(t, replaceInfo.Path)
	require.NoError(t, os.Remove(replaceInfo.Path))
	if err = os.Symlink(filepath.Join(replacementRoot, "target", "SKILL.md"), replaceInfo.Path); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	_, _, err = loader.freezeSelectedSkill(replaceInfo, SkillSelector{Name: "replace"})
	require.ErrorAs(t, err, &selectionErr)
	assert.Equal(t, SkillSelectionUnreadable, selectionErr.Kind)
}

func TestSelectRejectsSymlinkSwapBetweenValidationAndOpen(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	createSkillDir(t, root, "deploy", "deploy", "inside instructions")
	loader := NewSkillsLoader([]SkillRoot{{Path: root, Scope: SkillScopeRepository}})
	catalog := loader.Discover()
	require.Len(t, catalog.Skills, 1)
	info := catalog.Skills[0]

	outsideRoot := t.TempDir()
	outsidePath := filepath.Join(outsideRoot, "outside.md")
	require.NoError(t, os.WriteFile(outsidePath, []byte(
		"---\nname: deploy\ndescription: outside\n---\n\noutside secret\n",
	), 0o644))

	_, _, err := loader.freezeSelectedSkillWithHook(info, SkillSelector{Name: "deploy"}, func() {
		require.NoError(t, os.Remove(info.Path))
		if symlinkErr := os.Symlink(outsidePath, info.Path); symlinkErr != nil {
			t.Skipf("symlinks are unavailable: %v", symlinkErr)
		}
	})
	var selectionErr *SkillSelectionError
	require.ErrorAs(t, err, &selectionErr)
	assert.Equal(t, SkillSelectionUnreadable, selectionErr.Kind)
	assert.NotContains(t, selectionErr.Error(), "outside secret")
}

func TestSelectRejectsParentSymlinkSwapBetweenValidationAndOpen(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	createSkillDir(t, root, "deploy", "deploy", "inside instructions")
	loader := NewSkillsLoader([]SkillRoot{{Path: root, Scope: SkillScopeRepository}})
	catalog := loader.Discover()
	require.Len(t, catalog.Skills, 1)
	info := catalog.Skills[0]

	outsideRoot := t.TempDir()
	createSkillDir(t, outsideRoot, "deploy", "deploy", "outside secret")
	skillDirectory := filepath.Dir(info.Path)
	originalDirectory := skillDirectory + ".original"
	outsideDirectory := filepath.Join(outsideRoot, "deploy")

	_, _, err := loader.freezeSelectedSkillWithHook(info, SkillSelector{Name: "deploy"}, func() {
		require.NoError(t, os.Rename(skillDirectory, originalDirectory))
		if symlinkErr := os.Symlink(outsideDirectory, skillDirectory); symlinkErr != nil {
			t.Skipf("directory symlinks are unavailable: %v", symlinkErr)
		}
	})
	var selectionErr *SkillSelectionError
	require.ErrorAs(t, err, &selectionErr)
	assert.Equal(t, SkillSelectionUnreadable, selectionErr.Kind)
	assert.NotContains(t, selectionErr.Error(), "outside secret")
}

func TestMentionedSelectorsKeepKnownTextMentionsAndIgnoreEnvironmentVariables(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	createSkillDir(t, root, "deploy", "deploy", "deployment workflow")
	createSkillDir(t, root, "review", "review", "review workflow")
	loader := NewSkillsLoader([]SkillRoot{{
		Path: root, Scope: SkillScopeUser, Runtime: SkillRuntimeShared, Trust: SkillTrustUser,
	}})

	selectors := loader.MentionedSelectors(
		"Use $review with $HOME, then $DEPLOY and dedupe $review; ignore $deploy_var, $deploy-x, α$deploy, and $deployβ.",
		SkillRuntimeCoding,
	)

	assert.Equal(t, []SkillSelector{{Name: "review"}, {Name: "deploy"}}, selectors)
}

func TestSkillSelectionErrorUnwrapsReadFailure(t *testing.T) {
	cause := errors.New("read failed")
	err := &SkillSelectionError{Kind: SkillSelectionUnreadable, Selector: SkillSelector{Name: "x"}, Err: cause}
	assert.ErrorIs(t, err, cause)
}
