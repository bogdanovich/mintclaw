package skills

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderCatalogIncludesFullMetadataWithinBudget(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	createSkillDir(t, root, "alpha", "alpha", `Use alpha & avoid <unsafe> "output".`)
	loader := NewSkillsLoader([]SkillRoot{{Path: root, Scope: SkillScopeUser}})

	result := loader.RenderCatalog(CatalogRenderOptions{ContextWindowTokens: 100_000})

	assert.Contains(t, result.Text, "Use alpha &amp; avoid &lt;unsafe&gt; &quot;output&quot;.")
	assert.Equal(t, 1, result.Report.TotalCount)
	assert.Equal(t, 1, result.Report.IncludedCount)
	assert.Zero(t, result.Report.OmittedCount)
	assert.Zero(t, result.Report.TruncatedDescriptionCount)
	assert.LessOrEqual(t, result.Report.UsedCharacters, result.Report.BudgetCharacters)
}

func TestRenderCatalogShortensDescriptionsBeforeOmittingSkills(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	for _, name := range []string{"alpha", "beta", "gamma"} {
		createSkillDir(t, root, name, name, strings.Repeat(name+" description ", 30))
	}
	loader := NewSkillsLoader([]SkillRoot{{Path: root, Scope: SkillScopeUser}})
	skills := loader.Discover().Skills
	minimumLength := utf8.RuneCountInString(renderSkillCatalog(skills, 0, 0))
	fullLength := utf8.RuneCountInString(renderSkillCatalog(skills, -1, 0))
	budget := minimumLength + (fullLength-minimumLength)/2

	result := loader.RenderCatalog(CatalogRenderOptions{
		ContextWindowTokens: contextWindowForCatalogBudget(budget),
	})

	assert.Equal(t, len(skills), result.Report.IncludedCount)
	assert.Zero(t, result.Report.OmittedCount)
	assert.Positive(t, result.Report.TruncatedDescriptionCount)
	assert.LessOrEqual(t, result.Report.UsedCharacters, result.Report.BudgetCharacters)
}

func TestRenderCatalogOmitsOnlyLowerPriorityEntriesWhenRequired(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	for _, name := range []string{"alpha", "beta", "gamma"} {
		createSkillDir(t, root, name, name, strings.Repeat(name+" description ", 30))
	}
	loader := NewSkillsLoader([]SkillRoot{{Path: root, Scope: SkillScopeUser}})
	skills := loader.Discover().Skills
	oneEntryBudget := utf8.RuneCountInString(renderSkillCatalog(skills[:1], 0, len(skills)-1))

	result := loader.RenderCatalog(CatalogRenderOptions{
		ContextWindowTokens: contextWindowForCatalogBudget(oneEntryBudget),
	})

	assert.Positive(t, result.Report.IncludedCount)
	assert.Less(t, result.Report.IncludedCount, len(skills))
	assert.Equal(t, len(skills)-result.Report.IncludedCount, result.Report.OmittedCount)
	assert.Contains(t, result.Text, "<name>alpha</name>")
	assert.NotContains(t, result.Text, "<name>gamma</name>")
	assert.LessOrEqual(t, result.Report.UsedCharacters, result.Report.BudgetCharacters)
	require.NotEmpty(t, result.Diagnostics)
	assert.Equal(t, CatalogDiagnosticCatalogOmitted, result.Diagnostics[len(result.Diagnostics)-1].Kind)
}

func TestRenderCatalogFiltersAllowedNamesCaseInsensitively(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	createSkillDir(t, root, "alpha", "alpha", "alpha description")
	createSkillDir(t, root, "beta", "beta", "beta description")
	loader := NewSkillsLoader([]SkillRoot{{Path: root, Scope: SkillScopeUser}})

	result := loader.RenderCatalog(CatalogRenderOptions{AllowedNames: []string{" BETA "}})

	assert.Equal(t, 1, result.Report.TotalCount)
	assert.NotContains(t, result.Text, "<name>alpha</name>")
	assert.Contains(t, result.Text, "<name>beta</name>")
}

func contextWindowForCatalogBudget(characters int) int {
	// The catalog receives two percent of the context window and assumes four
	// characters per token. Rounding upward keeps the requested fixture budget.
	return ((characters + approxCharactersPerToken - 1) / approxCharactersPerToken) *
		(100 / catalogContextWindowPercent)
}
