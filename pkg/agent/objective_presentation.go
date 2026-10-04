package agent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/taskresult"
)

// objectivePresentation selects, rather than rewrites, one complete answer.
// The coverage check is structural and deliberately conservative: facts must
// survive verbatim, including links and IDs. No semantic/language classifier
// or extra model pass is used. Incomplete status and blocker remain immutable.
func objectivePresentation(summary string, outcome *taskresult.Outcome) string {
	summary = strings.TrimSpace(summary)
	if summary == "" || outcome == nil || strings.Contains(summary, objectiveOutcomeStart) {
		return ""
	}
	if containsStructuredJSON(summary) {
		return ""
	}
	normalized := strings.Join(strings.Fields(summary), " ")
	contains := func(fact string) bool {
		return containsPresentationFact(normalized, strings.Join(strings.Fields(fact), " "))
	}
	if outcome.Status != taskresult.OutcomeSucceeded &&
		(strings.TrimSpace(outcome.Explanation) == "" || !contains(outcome.Explanation)) {
		return ""
	}
	for _, item := range outcome.CompletedItems {
		output := item.Output
		if output == nil {
			continue
		}
		switch output.Kind {
		case "text":
			if facts, structured := structuredTextFacts(output.Text); structured {
				if contains(output.Text) {
					return "" // Supporting JSON must not leak into ordinary prose.
				}
				for _, fact := range facts {
					if !contains(fact.value) {
						return ""
					}
				}
			} else if !contains(output.Text) {
				return ""
			}
		case "records":
			for _, record := range output.Records {
				for _, value := range record {
					if !contains(value) {
						return ""
					}
				}
			}
		case "artifact":
			for _, ref := range output.ArtifactRefs {
				if !contains(ref) {
					return ""
				}
			}
		}
	}
	return summary
}

func containsPresentationFact(text, fact string) bool {
	if fact == "" {
		return true
	}
	first, _ := utf8.DecodeRuneInString(fact)
	last, _ := utf8.DecodeLastRuneInString(fact)
	word := func(char rune) bool { return unicode.IsLetter(char) || unicode.IsDigit(char) || char == '_' }
	for offset := 0; offset < len(text); {
		index := strings.Index(text[offset:], fact)
		if index < 0 {
			return false
		}
		index += offset
		end := index + len(fact)
		left, _ := utf8.DecodeLastRuneInString(text[:index])
		right, _ := utf8.DecodeRuneInString(text[end:])
		if (!word(first) || !word(left)) && (!word(last) || !word(right)) {
			return true
		}
		offset = index + 1
	}
	return false
}

func containsStructuredJSON(text string) bool {
	for index, char := range text {
		if char != '{' && char != '[' {
			continue
		}
		var raw json.RawMessage
		if json.NewDecoder(strings.NewReader(text[index:])).Decode(&raw) == nil {
			return true
		}
	}
	return false
}

type objectivePresentationFact struct{ field, value string }

func structuredTextFacts(text string) ([]objectivePresentationFact, bool) {
	text = strings.TrimSpace(text)
	if text == "" || (text[0] != '{' && text[0] != '[') || !json.Valid([]byte(text)) {
		return nil, false
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return nil, false
	}
	var facts []objectivePresentationFact
	var collect func(string, any)
	collect = func(field string, value any) {
		switch value := value.(type) {
		case map[string]any:
			if len(value) == 0 {
				facts = append(facts, objectivePresentationFact{field, "(empty)"})
				return
			}
			keys := make([]string, 0, len(value))
			for key := range value {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				collect(strings.TrimPrefix(field+"."+key, "."), value[key])
			}
		case []any:
			if len(value) == 0 {
				facts = append(facts, objectivePresentationFact{field, "(empty)"})
				return
			}
			for index, item := range value {
				collect(fmt.Sprintf("%s[%d]", field, index), item)
			}
		case nil:
			facts = append(facts, objectivePresentationFact{field, "null"})
		default:
			facts = append(facts, objectivePresentationFact{field, fmt.Sprint(value)})
		}
	}
	collect("", value)
	return facts, true
}

func renderStructuredFacts(facts []objectivePresentationFact) string {
	lines := make([]string, 0, len(facts))
	for _, fact := range facts {
		if fact.field == "" {
			lines = append(lines, "- "+fact.value)
		} else {
			lines = append(lines, "- "+fact.field+": "+fact.value)
		}
	}
	return strings.Join(lines, "\n")
}
