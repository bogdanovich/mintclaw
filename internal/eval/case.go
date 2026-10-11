// Package eval implements the manual agent-evaluation contracts and runner.
package eval

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	Version          = 1
	MaxManifestBytes = 64 * 1024
	MaxCorpusCases   = 256
	MaxInputBytes    = 64 * 1024
)

var (
	ErrInvalidCase   = errors.New("eval: invalid case")
	ErrInvalidPreset = errors.New("eval: invalid preset")
	ErrInvalidJSON   = errors.New("eval: invalid manifest JSON")
	ErrInvalidCorpus = errors.New("eval: invalid corpus")
	ErrInvalidFilter = errors.New("eval: invalid selection")

	identifierPattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
)

type Case struct {
	Version     int         `json:"version"`
	ID          string      `json:"id"`
	Suite       string      `json:"suite"`
	Tags        []string    `json:"tags"`
	Description string      `json:"description"`
	Task        string      `json:"task"`
	Requires    []string    `json:"requires"`
	Fixtures    []Fixture   `json:"fixtures"`
	Driver      Driver      `json:"driver"`
	Limits      Limits      `json:"limits"`
	Checks      []Check     `json:"checks"`
	Rubric      []Criterion `json:"rubric"`
	PDF         *PDFCase    `json:"pdf,omitempty"`
}

type Fixture struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Kind   string `json:"kind"`
}

type Driver struct {
	Mode    string            `json:"mode"`
	Steps   []Step            `json:"steps,omitempty"`
	Persona string            `json:"persona,omitempty"`
	Facts   map[string]string `json:"facts,omitempty"`
}

type Step struct {
	Action  Action      `json:"action"`
	WaitFor Expectation `json:"wait_for"`
}

type Action struct {
	Kind string `json:"kind"`
	Text string `json:"text,omitempty"`
	Fact string `json:"fact,omitempty"`
}

type Expectation struct {
	Kind     string `json:"kind"`
	Contains string `json:"contains,omitempty"`
}

type Limits struct {
	MaxUserTurns   int    `json:"max_user_turns"`
	TurnTimeout    string `json:"turn_timeout"`
	CaseTimeout    string `json:"case_timeout"`
	MaxDriverCalls int    `json:"max_driver_calls"`
}

func DefaultLimits() Limits {
	return Limits{MaxUserTurns: 18, TurnTimeout: "3m", CaseTimeout: "10m", MaxDriverCalls: 18}
}

func (limits Limits) Validate() error {
	turn, turnErr := time.ParseDuration(limits.TurnTimeout)
	total, totalErr := time.ParseDuration(limits.CaseTimeout)
	if limits.MaxUserTurns < 1 || limits.MaxUserTurns > 64 ||
		limits.MaxDriverCalls < 1 || limits.MaxDriverCalls > 64 ||
		turnErr != nil || turn <= 0 || turn > 15*time.Minute ||
		totalErr != nil || total < turn || total > time.Hour {
		return ErrInvalidCase
	}
	return nil
}

type Check struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Expected string `json:"expected,omitempty"`
}

type Criterion struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

func (c Case) Validate() error {
	if c.Version != Version || !validID(c.ID) || !validID(c.Suite) || c.Suite == "all" ||
		!strings.HasPrefix(c.ID, c.Suite+".") || !boundedText(c.Description, 1024) ||
		!boundedText(c.Task, MaxInputBytes) || !validIDs(c.Tags, 32) ||
		!validIDs(c.Requires, 32) || len(c.Fixtures) > 16 || c.Limits.Validate() != nil ||
		len(c.Checks) == 0 || len(c.Checks) > 32 || len(c.Rubric) > 16 {
		return ErrInvalidCase
	}
	fixtureIDs := make(map[string]bool)
	for _, fixture := range c.Fixtures {
		if !validID(fixture.ID) || fixtureIDs[fixture.ID] || !safeRelativePath(fixture.Path) ||
			!validDigest(fixture.SHA256) || !slices.Contains([]string{"pdf", "file"}, fixture.Kind) {
			return ErrInvalidCase
		}
		fixtureIDs[fixture.ID] = true
	}
	if c.Driver.validate(c.Limits) != nil {
		return ErrInvalidCase
	}
	for _, step := range c.Driver.Steps {
		if step.Action.Kind == "confirm" || step.Action.Kind == "allow_once" {
			if c.PDF == nil || c.PDF.Output != "required" ||
				step.Action.Kind == "confirm" && step.WaitFor.Kind != "final" ||
				step.Action.Kind == "allow_once" && step.WaitFor.Kind != "approval" {
				return ErrInvalidCase
			}
		}
	}
	checkIDs := make(map[string]bool)
	for _, check := range c.Checks {
		if !validID(check.ID) || checkIDs[check.ID] {
			return ErrInvalidCase
		}
		switch check.Kind {
		case "response_contains", "response_absent":
			if !boundedText(check.Expected, 4096) {
				return ErrInvalidCase
			}
		case "last_outcome":
			if !slices.Contains([]string{"final", "question", "approval", "canceled"}, check.Expected) {
				return ErrInvalidCase
			}
		case "same_session", "interaction_observed":
			if check.Expected != "" {
				return ErrInvalidCase
			}
		case "pdf_fields", "pdf_structure", "pdf_delivery", "pdf_no_write", "private_surfaces":
			if c.PDF == nil || check.Expected != "" {
				return ErrInvalidCase
			}
		default:
			return ErrInvalidCase
		}
		checkIDs[check.ID] = true
	}
	criterionIDs := make(map[string]bool)
	for _, criterion := range c.Rubric {
		if !validID(criterion.ID) || criterionIDs[criterion.ID] || !boundedText(criterion.Description, 2048) {
			return ErrInvalidCase
		}
		criterionIDs[criterion.ID] = true
	}
	if c.PDF != nil && c.PDF.validate(c) != nil {
		return ErrInvalidCase
	}
	return nil
}

func (driver Driver) validate(limits Limits) error {
	if len(driver.Facts) > 64 {
		return ErrInvalidCase
	}
	for name, fact := range driver.Facts {
		if !validID(name) || !boundedText(fact, 4096) {
			return ErrInvalidCase
		}
	}
	switch driver.Mode {
	case "scripted":
		if len(driver.Steps) == 0 || len(driver.Steps)+1 > limits.MaxUserTurns || driver.Persona != "" {
			return ErrInvalidCase
		}
	case "llm_user":
		if len(driver.Steps) != 0 || !boundedText(driver.Persona, 16*1024) {
			return ErrInvalidCase
		}
	default:
		return ErrInvalidCase
	}
	for _, step := range driver.Steps {
		if step.Action.validate(driver.Facts) != nil ||
			!slices.Contains([]string{"final", "question", "approval"}, step.WaitFor.Kind) ||
			step.WaitFor.Contains != "" && !boundedText(step.WaitFor.Contains, 4096) {
			return ErrInvalidCase
		}
	}
	return nil
}

func (action Action) validate(facts map[string]string) error {
	switch action.Kind {
	case "message", "finish":
		if !boundedText(action.Text, MaxInputBytes) || action.Fact != "" {
			return ErrInvalidCase
		}
	case "protected_answer":
		if !validID(action.Fact) || facts[action.Fact] == "" || action.Text != "" {
			return ErrInvalidCase
		}
	case "clarify", "back", "skip", "not_applicable", "cancel", "stop", "confirm", "allow_once":
		if action.Text != "" || action.Fact != "" {
			return ErrInvalidCase
		}
	default:
		return ErrInvalidCase
	}
	return nil
}

func boundedText(value string, maximum int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= maximum && utf8.ValidString(value) &&
		!strings.ContainsRune(value, 0)
}

func validID(value string) bool {
	return len(value) <= 96 && identifierPattern.MatchString(value)
}

func validIDs(values []string, maximum int) bool {
	if len(values) > maximum {
		return false
	}
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if !validID(value) || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}
