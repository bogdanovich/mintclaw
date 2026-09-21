package skills

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	defaultCatalogCharacterBudget = 8_000
	catalogContextWindowPercent   = 2
	approxCharactersPerToken      = 4
)

type CatalogRenderOptions struct {
	ContextWindowTokens int
	AllowedNames        []string
}

type CatalogRenderReport struct {
	TotalCount                int `json:"total_count"`
	IncludedCount             int `json:"included_count"`
	OmittedCount              int `json:"omitted_count"`
	TruncatedDescriptionCount int `json:"truncated_description_count"`
	BudgetCharacters          int `json:"budget_characters"`
	UsedCharacters            int `json:"used_characters"`
}

type CatalogRenderResult struct {
	Text        string              `json:"text"`
	Catalog     SkillCatalog        `json:"catalog"`
	Report      CatalogRenderReport `json:"report"`
	Diagnostics []CatalogDiagnostic `json:"diagnostics,omitempty"`
}

func (sl *SkillsLoader) RenderCatalog(options CatalogRenderOptions) CatalogRenderResult {
	catalog := sl.Discover()
	skills := filterCatalogSkills(catalog.Skills, options.AllowedNames)
	budget := catalogCharacterBudget(options.ContextWindowTokens)
	report := CatalogRenderReport{
		TotalCount:       len(skills),
		BudgetCharacters: budget,
	}
	result := CatalogRenderResult{
		Catalog:     catalog,
		Report:      report,
		Diagnostics: append([]CatalogDiagnostic(nil), catalog.Diagnostics...),
	}
	if len(skills) == 0 || budget <= 0 {
		return result
	}

	full := renderSkillCatalog(skills, -1, 0)
	if utf8.RuneCountInString(full) <= budget {
		result.Text = full
		result.Report.IncludedCount = len(skills)
		result.Report.UsedCharacters = utf8.RuneCountInString(full)
		return result
	}

	minimum := renderSkillCatalog(skills, 0, 0)
	if utf8.RuneCountInString(minimum) <= budget {
		cap := largestDescriptionCapThatFits(skills, budget)
		text := renderSkillCatalog(skills, cap, 0)
		result.Text = text
		result.Report.IncludedCount = len(skills)
		result.Report.TruncatedDescriptionCount = countTruncatedDescriptions(skills, cap)
		result.Report.UsedCharacters = utf8.RuneCountInString(text)
		return result
	}

	included := largestMinimumCatalogPrefixThatFits(skills, budget)
	omitted := len(skills) - included
	text := renderSkillCatalog(skills[:included], 0, omitted)
	if utf8.RuneCountInString(text) > budget {
		text = ""
		included = 0
		omitted = len(skills)
	}
	result.Text = text
	result.Report.IncludedCount = included
	result.Report.OmittedCount = omitted
	result.Report.TruncatedDescriptionCount = countTruncatedDescriptions(skills[:included], 0)
	result.Report.UsedCharacters = utf8.RuneCountInString(text)
	if omitted > 0 {
		result.Diagnostics = append(result.Diagnostics, CatalogDiagnostic{
			Kind:    CatalogDiagnosticCatalogOmitted,
			Message: fmt.Sprintf("%d skill(s) omitted from the model catalog by the context budget", omitted),
		})
	}
	return result
}

func catalogCharacterBudget(contextWindowTokens int) int {
	if contextWindowTokens <= 0 {
		return defaultCatalogCharacterBudget
	}
	tokens := contextWindowTokens * catalogContextWindowPercent / 100
	if tokens < 1 {
		tokens = 1
	}
	return tokens * approxCharactersPerToken
}

func filterCatalogSkills(skills []SkillInfo, allowed []string) []SkillInfo {
	if len(allowed) == 0 {
		return append([]SkillInfo(nil), skills...)
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "" {
			allowedSet[name] = struct{}{}
		}
	}
	filtered := make([]SkillInfo, 0, len(skills))
	for _, skill := range skills {
		if _, ok := allowedSet[strings.ToLower(skill.Name)]; ok {
			filtered = append(filtered, skill)
		}
	}
	return filtered
}

func largestDescriptionCapThatFits(skills []SkillInfo, budget int) int {
	low := 0
	high := MaxDescriptionLength
	for low < high {
		mid := low + (high-low+1)/2
		if utf8.RuneCountInString(renderSkillCatalog(skills, mid, 0)) <= budget {
			low = mid
		} else {
			high = mid - 1
		}
	}
	return low
}

func largestMinimumCatalogPrefixThatFits(skills []SkillInfo, budget int) int {
	best := 0
	for included := 1; included <= len(skills); included++ {
		omitted := len(skills) - included
		text := renderSkillCatalog(skills[:included], 0, omitted)
		if utf8.RuneCountInString(text) > budget {
			break
		}
		best = included
	}
	return best
}

func countTruncatedDescriptions(skills []SkillInfo, cap int) int {
	count := 0
	for _, skill := range skills {
		if utf8.RuneCountInString(skill.Description) > cap {
			count++
		}
	}
	return count
}

func renderSkillCatalog(skills []SkillInfo, descriptionCap, omitted int) string {
	if len(skills) == 0 && omitted == 0 {
		return ""
	}
	var lines []string
	lines = append(lines, "<skills>")
	for _, skill := range skills {
		description := skill.Description
		if descriptionCap >= 0 {
			description = truncateDescription(description, descriptionCap)
		}
		lines = append(lines,
			"  <skill>",
			fmt.Sprintf("    <name>%s</name>", escapeXML(skill.Name)),
			fmt.Sprintf("    <description>%s</description>", escapeXML(description)),
			fmt.Sprintf("    <location>%s</location>", escapeXML(skill.Path)),
			fmt.Sprintf("    <source>%s</source>", escapeXML(skill.Source)),
			"  </skill>",
		)
	}
	if omitted > 0 {
		lines = append(lines, fmt.Sprintf(
			"  <omitted count=\"%d\" reason=\"catalog context budget exceeded\" />",
			omitted,
		))
	}
	lines = append(lines, "</skills>")
	return strings.Join(lines, "\n")
}

func truncateDescription(description string, cap int) string {
	if cap <= 0 {
		return ""
	}
	if utf8.RuneCountInString(description) <= cap {
		return description
	}
	if cap <= 3 {
		return strings.Repeat(".", cap)
	}
	runes := []rune(description)
	return string(runes[:cap-3]) + "..."
}
