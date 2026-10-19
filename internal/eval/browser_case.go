package eval

import (
	"slices"
	"strings"
)

// BrowserCase holds independent fixture expectations, never native authority.
// The model roles see only the natural task/persona and permitted fact handles.
type BrowserCase struct {
	Fixture  string         `json:"fixture"`
	Profile  string         `json:"profile"`
	Scenario string         `json:"scenario"`
	Fields   []BrowserField `json:"fields,omitempty"`
	Account  string         `json:"account,omitempty"`
}

type BrowserField struct {
	Name       string   `json:"name"`
	Fact       string   `json:"fact,omitempty"`
	Value      string   `json:"value,omitempty"`
	InputFacts []string `json:"input_facts,omitempty"`
}

func (field BrowserField) inputFacts() []string {
	if len(field.InputFacts) != 0 {
		return field.InputFacts
	}
	if field.Fact != "" {
		return []string{field.Fact}
	}
	return nil
}

func (browser BrowserCase) validate(item Case) error {
	if item.Suite != "browser" || item.PDF != nil || len(item.Fixtures) != 0 ||
		browser.Fixture != "local-form-v1" || !validID(browser.Profile) ||
		!slices.Contains([]string{"read", "submit", "followup", "ambiguous", "artifact", "denied"}, browser.Scenario) ||
		!slices.Contains(item.Requires, "browser") || strings.Count(item.Task, "{{fixture}}") != 1 {
		return ErrInvalidCase
	}
	form := browser.Scenario == "submit" || browser.Scenario == "followup"
	if form && len(browser.Fields) != 3 || !form && len(browser.Fields) != 0 ||
		browser.Scenario == "ambiguous" && browser.Account != "south" ||
		browser.Scenario == "denied" && browser.Account != "north" ||
		browser.Scenario != "ambiguous" && browser.Scenario != "denied" && browser.Account != "" {
		return ErrInvalidCase
	}
	seen := make(map[string]bool)
	inputFacts := make(map[string]bool)
	for _, field := range browser.Fields {
		if !slices.Contains([]string{"name", "color", "note"}, field.Name) || seen[field.Name] ||
			field.Fact != "" && (!validID(field.Fact) || item.Driver.Facts[field.Fact] == "" || field.Value != "") {
			return ErrInvalidCase
		}
		seen[field.Name] = true
		if len(field.InputFacts) > 8 || len(field.InputFacts) != 0 &&
			(field.Fact == "" || !slices.Contains(field.InputFacts, field.Fact)) {
			return ErrInvalidCase
		}
		for _, handle := range field.inputFacts() {
			if !validID(handle) || item.Driver.Facts[handle] == "" || inputFacts[handle] {
				return ErrInvalidCase
			}
			inputFacts[handle] = true
		}
		switch field.Name {
		case "color":
			if field.Fact != "" || !slices.Contains([]string{"green", "red", "blue"}, field.Value) {
				return ErrInvalidCase
			}
		case "name", "note":
			maximum := 1024
			if field.Name == "name" {
				maximum = 128
				if field.Fact == "" && field.Value == "" {
					return ErrInvalidCase
				}
			}
			if len(field.Value) > maximum || field.Value != "" && !boundedText(field.Value, maximum) {
				return ErrInvalidCase
			}
			for _, handle := range field.inputFacts() {
				if len(item.Driver.Facts[handle]) > maximum {
					return ErrInvalidCase
				}
			}
		}
	}
	checks := make(map[string]bool)
	for _, check := range item.Checks {
		checks[check.Kind] = true
	}
	readonly := slices.Contains([]string{"read", "artifact", "denied"}, browser.Scenario)
	if !checks["browser_state"] || !checks["browser_authority"] || !checks["browser_cleanup"] ||
		!checks["private_surfaces"] || checks["browser_no_write"] != readonly ||
		checks["browser_artifact"] != (browser.Scenario == "artifact") ||
		checks["browser_context"] != (browser.Scenario == "followup") ||
		browser.Scenario == "ambiguous" && !checks["interaction_observed"] {
		return ErrInvalidCase
	}
	return nil
}
