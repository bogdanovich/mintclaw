package eval

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func testPDFCase() Case {
	item := testCase()
	item.ID, item.Suite = "pdf.synthetic.test", "pdf"
	item.Task = "Fill the admitted synthetic fixture: {{fixture}}."
	item.Requires = []string{"conversation", "pdf"}
	item.Fixtures = []Fixture{
		{ID: "source", Path: "fixtures/source.pdf", Kind: "pdf", SHA256: digest([]byte("source"))},
	}
	item.Driver.Facts = map[string]string{"contact": "synthetic-private-value"}
	item.Checks = []Check{
		{ID: "fields", Kind: "pdf_fields"},
		{ID: "structure", Kind: "pdf_structure"},
		{ID: "delivery", Kind: "pdf_delivery"},
		{ID: "privacy", Kind: "private_surfaces"},
	}
	item.PDF = &PDFCase{
		Fixture: "source", Output: "required", Pages: 2, FieldCount: 1,
		Fields: []PDFField{{Name: "contact", Kind: "text", Fact: "contact"}},
	}
	return item
}

func TestPDFCaseRequiresIndependentEvidenceContract(t *testing.T) {
	item := testPDFCase()
	data, _ := json.Marshal(item)
	decoded, err := DecodeManifest[Case](data)
	if err != nil || decoded.Validate() != nil {
		t.Fatal("valid bounded PDF case refused", err)
	}
	for name, mutate := range map[string]func(*Case){
		"wrong domain":     func(c *Case) { c.Suite = "general"; c.ID = "general.synthetic.test" },
		"unknown fixture":  func(c *Case) { c.PDF.Fixture = "other" },
		"file not pdf":     func(c *Case) { c.Fixtures[0].Kind = "file" },
		"missing path":     func(c *Case) { c.Task = "Fill a PDF." },
		"duplicate path":   func(c *Case) { c.Task += " {{fixture}}" },
		"missing proof":    func(c *Case) { c.Checks = c.Checks[1:] },
		"missing privacy":  func(c *Case) { c.Checks = c.Checks[:3] },
		"invented output":  func(c *Case) { c.PDF.Output = "trust_caption" },
		"too many pages":   func(c *Case) { c.PDF.Pages = 33 },
		"partial fields":   func(c *Case) { c.PDF.FieldCount = 2 },
		"unknown fact":     func(c *Case) { c.PDF.Fields[0].Fact = "unknown" },
		"two expectations": func(c *Case) { c.PDF.Fields[0].Value = "other" },
		"preserved fact":   func(c *Case) { c.PDF.Fields[0].Preserve = true },
		"wrong field kind": func(c *Case) { c.PDF.Fields[0].Kind = "radio" },
		"duplicate field": func(c *Case) {
			c.PDF.Fields = append(c.PDF.Fields, c.PDF.Fields[0])
			c.PDF.FieldCount++
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := testPDFCase()
			mutate(&candidate)
			if !errors.Is(candidate.Validate(), ErrInvalidCase) {
				t.Fatal("unsafe or incomplete PDF contract admitted")
			}
		})
	}
	negative := testPDFCase()
	negative.PDF.Output = "forbidden"
	negative.Checks = []Check{{ID: "write", Kind: "pdf_no_write"}, {ID: "privacy", Kind: "private_surfaces"}}
	if negative.Validate() != nil {
		t.Fatal("explicit no-write contract refused")
	}
	negative.PDF.Draft = true
	if negative.Validate() == nil {
		t.Fatal("forbidden output also requested a draft")
	}
}

func TestPDFExpectationsDoNotBecomeModelContextOrProseSuccess(t *testing.T) {
	item := testPDFCase()
	checks := gradeConversation(item.Checks, []Event{{ID: "event-1", Kind: "final", Content: "Done!"}}, Event{})
	for _, check := range checks {
		if check.Verdict != Inconclusive {
			t.Fatal("unobserved domain evidence passed from prose")
		}
	}
	item.PDF.Fields[0].Name = "hidden-oracle-field-map"
	item.Driver.Mode, item.Driver.Steps, item.Driver.Persona = "llm_user", nil, "Answer the question with the permitted handle."
	item.Rubric = []Criterion{{ID: "quality", Description: "Questions should be coherent.", Required: true}}
	events := []Event{{ID: "event-1", Turn: 1, Kind: "question", Content: "What contact should I use?"}}
	input, err := userInput(item, events, "question", 2)
	if err != nil || strings.Contains(input, "hidden-oracle-field-map") ||
		strings.Contains(input, "synthetic-private-value") {
		t.Fatal("user role received golden mapping or private value", err)
	}
	input, _, err = judgeInput(item, events)
	if err != nil || strings.Contains(input, "hidden-oracle-field-map") ||
		strings.Contains(input, "synthetic-private-value") {
		t.Fatal("judge received golden mapping or private value", err)
	}
}
