package eval

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func testBrowserCase() Case {
	item := testCase()
	item.ID, item.Suite = "browser.synthetic.test", "browser"
	item.Task = "Help submit the synthetic form: {{fixture}}."
	item.Requires = []string{"conversation", "browser"}
	item.Driver.Facts = map[string]string{"name": "synthetic-private-name", "note": "synthetic-private-note"}
	item.Checks = []Check{
		{ID: "state", Kind: "browser_state"},
		{ID: "authority", Kind: "browser_authority"},
		{ID: "privacy", Kind: "private_surfaces"},
		{ID: "cleanup", Kind: "browser_cleanup"},
	}
	item.Browser = &BrowserCase{
		Fixture:  "local-form-v1",
		Profile:  "eval-browser",
		Scenario: "submit",
		Fields: []BrowserField{
			{Name: "name", Fact: "name"},
			{Name: "color", Value: "blue"},
			{Name: "note", Fact: "note"},
		},
	}
	return item
}

func TestBrowserCaseRequiresIndependentEvidenceAndBoundedKnownFixture(t *testing.T) {
	data, _ := json.Marshal(testBrowserCase())
	decoded, err := DecodeManifest[Case](data)
	if err != nil || decoded.Validate() != nil {
		t.Fatal("valid browser evidence contract refused", err)
	}
	for name, mutate := range map[string]func(*Case){
		"missing-browser-contract": func(c *Case) {
			c.Browser = nil
			c.Checks = []Check{{ID: "caption", Kind: "response_contains", Expected: "Done"}}
		},
		"capability-without-contract": func(c *Case) {
			c.ID, c.Suite, c.Browser = "general.browser", "general", nil
			c.Checks = []Check{{ID: "caption", Kind: "response_contains", Expected: "Done"}}
		},
		"suite-without-contract": func(c *Case) {
			c.Browser, c.Requires = nil, []string{"conversation"}
			c.Checks = []Check{{ID: "caption", Kind: "response_contains", Expected: "Done"}}
		},
		"wrong-domain": func(c *Case) { c.ID, c.Suite = "general.browser", "general" },
		"mixed-domain": func(c *Case) { c.PDF = &PDFCase{} },
		"external-fixture": func(c *Case) {
			c.Fixtures = []Fixture{{ID: "outside", Kind: "file", Path: "outside", SHA256: digest([]byte("outside"))}}
		},
		"unknown-fixture":     func(c *Case) { c.Browser.Fixture = "http://external.test" },
		"invalid-profile":     func(c *Case) { c.Browser.Profile = "../live-profile" },
		"unknown-scenario":    func(c *Case) { c.Browser.Scenario = "privileged_execute" },
		"missing-capability":  func(c *Case) { c.Requires = []string{"conversation"} },
		"missing-placeholder": func(c *Case) { c.Task = "Submit a form." },
		"duplicate-placeholder": func(c *Case) {
			c.Task += " {{fixture}}"
		},
		"missing-field":       func(c *Case) { c.Browser.Fields = c.Browser.Fields[:2] },
		"duplicate-field":     func(c *Case) { c.Browser.Fields[2] = c.Browser.Fields[0] },
		"unknown-field":       func(c *Case) { c.Browser.Fields[0].Name = "password" },
		"unknown-fact":        func(c *Case) { c.Browser.Fields[0].Fact = "missing" },
		"two-values":          func(c *Case) { c.Browser.Fields[0].Value = "other" },
		"empty-name":          func(c *Case) { c.Browser.Fields[0].Fact = "" },
		"invalid-color":       func(c *Case) { c.Browser.Fields[1].Value = "other" },
		"private-color":       func(c *Case) { c.Browser.Fields[1] = BrowserField{Name: "color", Fact: "name"} },
		"oversized-name":      func(c *Case) { c.Driver.Facts["name"] = strings.Repeat("x", 129) },
		"oversized-note":      func(c *Case) { c.Driver.Facts["note"] = strings.Repeat("x", 1025) },
		"missing-final-input": func(c *Case) { c.Browser.Fields[0].InputFacts = []string{"note"} },
		"unknown-input":       func(c *Case) { c.Browser.Fields[0].InputFacts = []string{"name", "missing"} },
		"duplicate-input":     func(c *Case) { c.Browser.Fields[0].InputFacts = []string{"name", "name"} },
		"cross-field-input":   func(c *Case) { c.Browser.Fields[0].InputFacts = []string{"name", "note"} },
		"static-input":        func(c *Case) { c.Browser.Fields[1].InputFacts = []string{"name"} },
		"oversized-input": func(c *Case) {
			c.Driver.Facts["initial"] = strings.Repeat("x", 129)
			c.Browser.Fields[0].InputFacts = []string{"name", "initial"}
		},
		"unexpected-account": func(c *Case) {
			c.Browser.Account = "south"
		},
		"missing-state":     func(c *Case) { c.Checks = c.Checks[1:] },
		"missing-authority": func(c *Case) { c.Checks[1].Kind = "last_outcome"; c.Checks[1].Expected = "final" },
		"missing-privacy":   func(c *Case) { c.Checks = append(c.Checks[:2], c.Checks[3]) },
		"missing-cleanup":   func(c *Case) { c.Checks = c.Checks[:3] },
		"contradictory-no-write": func(c *Case) {
			c.Checks = append(c.Checks, Check{ID: "no-write", Kind: "browser_no_write"})
		},
		"unexpected-context": func(c *Case) {
			c.Checks = append(c.Checks, Check{ID: "context", Kind: "browser_context"})
		},
		"unexpected-artifact": func(c *Case) {
			c.Checks = append(c.Checks, Check{ID: "artifact", Kind: "browser_artifact"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := testBrowserCase()
			mutate(&candidate)
			if !errors.Is(candidate.Validate(), ErrInvalidCase) {
				t.Fatal("unsafe or contradictory browser contract admitted")
			}
		})
	}
	for _, raw := range []string{
		strings.Replace(string(data), `"profile":"eval-browser"`, `"profile":"eval-browser","execute":true`, 1),
		strings.Replace(string(data), `"scenario":"submit"`, `"scenario":"submit","scenario":"read"`, 1),
	} {
		if _, err := DecodeManifest[Case]([]byte(raw)); !errors.Is(err, ErrInvalidJSON) {
			t.Fatal("unknown or duplicate browser JSON member admitted")
		}
	}
}

func TestBrowserExpectationsDoNotReachModelRolesOrPassFromProse(t *testing.T) {
	item := testBrowserCase()
	item.Browser.Profile = "hidden-oracle-profile"
	item.Driver.Mode, item.Driver.Steps, item.Driver.Persona = "llm_user", nil, "Answer only the question asked."
	item.Rubric = []Criterion{{ID: "coherent", Description: "The conversation is coherent.", Required: true}}
	events := []Event{{ID: "event-1", Turn: 1, Kind: "final", Content: "The form was submitted successfully."}}
	for _, check := range gradeConversation(item.Checks, events, events[0]) {
		if check.Verdict != Inconclusive {
			t.Fatal("browser authority, effects, privacy or cleanup passed from a caption")
		}
	}
	user, err := userInput(item, events, "final", 2)
	if err != nil {
		t.Fatal(err)
	}
	judge, _, err := judgeInput(item, events)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{user, judge} {
		if strings.Contains(input, "hidden-oracle-profile") || strings.Contains(input, "synthetic-private-name") ||
			strings.Contains(input, "synthetic-private-note") || strings.Contains(input, `"fields"`) {
			t.Fatal("browser oracle mapping, profile authority or private facts reached a model role")
		}
	}
}

func TestBrowserCorpusIsExplicitlyUnqualifiedWithoutTargetWork(t *testing.T) {
	fixture := newAdmissionFixture(t)
	corpus, err := LoadCorpus("../../evals")
	if err != nil {
		t.Fatal(err)
	}
	cases, err := Select(corpus, Selection{Suite: "browser"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runAdmissionFixture(t, fixture, cases...)
	if !errors.Is(err, ErrNotPassed) || !result.Complete || result.Verdict != Skipped ||
		len(result.Trials) != 6 || result.Coverage[Skipped] != 6 || fixture.tasks.Load() != 0 ||
		fixture.probes.Load() != 1 {
		t.Fatal("unimplemented browser evidence reached the target, disappeared or passed", err)
	}
	for _, trial := range result.Trials {
		if trial.Code != "browser_evidence_unavailable" {
			t.Fatal("unsupported browser coverage was not explicit")
		}
	}
}
