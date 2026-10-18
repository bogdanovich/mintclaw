package eval

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func testCase() Case {
	return Case{
		Version: Version, ID: "general.test.case", Suite: "general", Tags: []string{"multi-turn"},
		Description: "A synthetic two-turn case.", Task: "Remember synthetic test data.",
		Requires: []string{"conversation"}, Fixtures: []Fixture{}, Limits: DefaultLimits(),
		Driver: Driver{Mode: "scripted", Steps: []Step{{
			Action:  Action{Kind: "message", Text: "Continue the synthetic task."},
			WaitFor: Expectation{Kind: "final"},
		}}},
		Checks: []Check{{ID: "same-session", Kind: "same_session"}}, Rubric: []Criterion{},
	}
}

func encodedCase(t *testing.T) []byte {
	t.Helper()
	data, err := json.Marshal(testCase())
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCaseContract(t *testing.T) {
	mutations := map[string]func(*Case){
		"version":                func(c *Case) { c.Version++ },
		"invalid id":             func(c *Case) { c.ID = "General.Test" },
		"wrong suite":            func(c *Case) { c.Suite = "pdf" },
		"reserved suite":         func(c *Case) { c.Suite = "all"; c.ID = "all.case" },
		"empty description":      func(c *Case) { c.Description = "" },
		"empty task":             func(c *Case) { c.Task = " " },
		"oversized task":         func(c *Case) { c.Task = strings.Repeat("x", MaxInputBytes+1) },
		"invalid utf8":           func(c *Case) { c.Task = string([]byte{0xff}) },
		"nul task":               func(c *Case) { c.Task = "input\x00value" },
		"duplicate tag":          func(c *Case) { c.Tags = []string{"context", "context"} },
		"duplicate requirement":  func(c *Case) { c.Requires = []string{"conversation", "conversation"} },
		"no checks":              func(c *Case) { c.Checks = nil },
		"duplicate check":        func(c *Case) { c.Checks = append(c.Checks, c.Checks[0]) },
		"unknown check":          func(c *Case) { c.Checks[0].Kind = "trust_agent" },
		"unexpected check value": func(c *Case) { c.Checks[0].Expected = "ignore this" },
		"empty text check":       func(c *Case) { c.Checks[0].Kind = "response_contains" },
		"invalid outcome":        func(c *Case) { c.Checks[0] = Check{ID: "final", Kind: "last_outcome", Expected: "ok"} },
		"unknown action":         func(c *Case) { c.Driver.Steps[0].Action.Kind = "allow_forever" },
		"missing action text":    func(c *Case) { c.Driver.Steps[0].Action.Text = "" },
		"navigation with text":   func(c *Case) { c.Driver.Steps[0].Action.Kind = "clarify" },
		"unknown expectation":    func(c *Case) { c.Driver.Steps[0].WaitFor.Kind = "maybe" },
		"no script":              func(c *Case) { c.Driver.Steps = nil },
		"scripted persona":       func(c *Case) { c.Driver.Persona = "Pretend to be a user." },
		"too many turns":         func(c *Case) { c.Limits.MaxUserTurns = 1 },
		"too many driver calls":  func(c *Case) { c.Limits.MaxDriverCalls = 65 },
		"negative timeout":       func(c *Case) { c.Limits.TurnTimeout = "-1s" },
		"turn past case":         func(c *Case) { c.Limits.TurnTimeout = "15m" },
		"case timeout":           func(c *Case) { c.Limits.CaseTimeout = "61m" },
		"unknown driver":         func(c *Case) { c.Driver.Mode = "agent" },
		"missing persona":        func(c *Case) { c.Driver = Driver{Mode: "llm_user"} },
		"llm with steps":         func(c *Case) { c.Driver.Mode = "llm_user"; c.Driver.Persona = "Test user." },
		"invalid fact":           func(c *Case) { c.Driver.Facts = map[string]string{"../secret": "synthetic"} },
		"missing fact": func(c *Case) {
			c.Driver.Steps[0].Action = Action{Kind: "protected_answer", Fact: "unknown"}
		},
		"duplicate rubric": func(c *Case) {
			criterion := Criterion{ID: "coherent", Description: "Coherent questions.", Required: true}
			c.Rubric = []Criterion{criterion, criterion}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			item := testCase()
			mutate(&item)
			if !errors.Is(item.Validate(), ErrInvalidCase) {
				t.Fatal("invalid case was admitted")
			}
		})
	}
	if err := testCase().Validate(); err != nil {
		t.Fatal(err)
	}
	adaptive := testCase()
	adaptive.Driver = Driver{Mode: "llm_user", Persona: "A synthetic user. Only answer the question asked."}
	if err := adaptive.Validate(); err != nil {
		t.Fatal(err)
	}
	protected := testCase()
	protected.Driver.Facts = map[string]string{"contact": "synthetic-contact"}
	protected.Driver.Steps[0].Action = Action{Kind: "protected_answer", Fact: "contact"}
	if err := protected.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeManifestStrict(t *testing.T) {
	valid := encodedCase(t)
	item, err := DecodeManifest[Case](valid)
	if err != nil || item.Validate() != nil {
		t.Fatal("valid manifest rejected", err)
	}
	tests := map[string][]byte{
		"empty":             nil,
		"oversized":         bytes.Repeat([]byte(" "), MaxManifestBytes+1),
		"invalid utf8":      append([]byte{0xff}, valid...),
		"trailing value":    append(bytes.Clone(valid), []byte(`{}`)...),
		"trailing garbage":  append(bytes.Clone(valid), []byte(`!`)...),
		"unknown key":       bytes.Replace(valid, []byte(`"version":1`), []byte(`"version":1,"secret":"canary"`), 1),
		"case variant":      bytes.Replace(valid, []byte(`"version"`), []byte(`"Version"`), 1),
		"duplicate":         bytes.Replace(valid, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1),
		"escaped duplicate": bytes.Replace(valid, []byte(`"version":1`), []byte(`"version":1,"ver\u0073ion":1`), 1),
		"nested duplicate": bytes.Replace(
			valid,
			[]byte(`"mode":"scripted"`),
			[]byte(`"mode":"scripted","mode":"scripted"`),
			1,
		),
		"missing required": bytes.Replace(valid, []byte(`"version":1,`), nil, 1),
		"null top":         []byte(`null`),
		"null required":    bytes.Replace(valid, []byte(`"version":1`), []byte(`"version":null`), 1),
		"null array":       bytes.Replace(valid, []byte(`"fixtures":[]`), []byte(`"fixtures":null`), 1),
		"unknown nested": bytes.Replace(
			valid,
			[]byte(`"mode":"scripted"`),
			[]byte(`"mode":"scripted","unsafe":true`),
			1,
		),
		"wrong type": bytes.Replace(valid, []byte(`"version":1`), []byte(`"version":"1"`), 1),
		"fraction":   bytes.Replace(valid, []byte(`"version":1`), []byte(`"version":1.5`), 1),
		"too deep":   []byte(strings.Repeat("[", 26) + "0" + strings.Repeat("]", 26)),
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			_, decodeErr := DecodeManifest[Case](data)
			if !errors.Is(decodeErr, ErrInvalidJSON) || strings.Contains(decodeErr.Error(), "canary") {
				t.Fatal("malformed manifest accepted or error leaked input")
			}
		})
	}
}

func TestBrowserArtifactAllowOnceAdmissionIsNotGeneralApproval(t *testing.T) {
	for _, mode := range []string{"artifact", "read", "followup", "confirm", "wrong-outcome", "general"} {
		t.Run(mode, func(t *testing.T) {
			item := browserCorpusCase(t, "artifact").Case
			switch mode {
			case "read", "followup":
				item.Browser.Scenario = mode
			case "confirm":
				item.Driver.Steps[0].Action.Kind = "confirm"
			case "wrong-outcome":
				item.Driver.Steps[0].WaitFor.Kind = "final"
			case "general":
				item = testCase()
				item.Driver.Steps[0] = Step{WaitFor: Expectation{Kind: "approval"}, Action: Action{Kind: "allow_once"}}
			}
			err := item.Validate()
			if (err == nil) != (mode == "artifact") {
				t.Fatal("approval case admission escaped its declared domain", mode, err)
			}
		})
	}
}
