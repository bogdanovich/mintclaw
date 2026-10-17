package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/document"
)

func TestDocumentIntakePriorResolutionBindsOnlyOfferedPrivateInputs(t *testing.T) {
	view := privateIntakeTestView()
	view.Pass.Input.JobID, view.Pass.Input.ModeID, view.Pass.Input.Reference = "job", "mode", "current"
	view.RelatedInputs = []document.FormIntakeInput{{
		JobID: "job", ModeID: "mode", Reference: "earlier", Passes: 3, Text: "My name is REJECTED_LITERAL.",
		Findings: []document.FormFieldMappingBlocker{
			{FieldID: "field_name", Code: "field_invalid"},
			{FieldID: "not_in_view", Code: "field_conflicting"},
		},
	}}
	encoded, err := encodeDocumentIntakeView(view)
	if err != nil || !strings.Contains(string(encoded), `"related_input_context"`) ||
		!strings.Contains(
			string(encoded),
			`"reference":"earlier"`,
		) || strings.Contains(string(encoded), "not_in_view") {
		t.Fatal("private resolution context was missing or exceeded the candidate window")
	}
	const body = `{"intent":"facts","proposals":[{"field_id":"field_name",` +
		`"literals":[{"text":"Casey Mint","occurrence":1}],` +
		`"resolves":[{"reference":"earlier","literal":{"text":"REJECTED_LITERAL","occurrence":1}}]}]}`
	result, err := decodeDocumentIntakeInterpretation(body, view.Pass.CandidateIDs, view.RelatedInputs...)
	if err != nil || len(result.Proposals) != 1 || len(result.Proposals[0].Resolves) != 1 ||
		result.Proposals[0].Resolves[0].ExpectedPasses != 3 ||
		result.Proposals[0].Literals[0].Text != "Casey Mint" {
		t.Fatal("resolution changed the current value or lost its native reservation snapshot")
	}
	public, err := json.Marshal(result.Proposals[0])
	if err != nil || strings.Contains(string(public), "REJECTED_LITERAL") ||
		strings.Contains(string(public), "earlier") {
		t.Fatal("prior protected evidence escaped ordinary JSON")
	}
	for _, name := range []string{"not offered", "consumed", "exhausted", "zero passes", "wrong field", "advice"} {
		t.Run(name, func(t *testing.T) {
			prior := view.RelatedInputs[0]
			switch name {
			case "not offered":
				prior.Reference = "another"
			case "consumed":
				prior.Consumed = true
			case "exhausted":
				prior.Passes = document.MaxFormIntakeExtractionPasses + 1
			case "zero passes":
				prior.Passes = 0
			case "wrong field":
				prior.Findings = []document.FormFieldMappingBlocker{{FieldID: "field_birth", Code: "field_invalid"}}
			case "advice":
				prior.Findings = []document.FormFieldMappingBlocker{{FieldID: "field_name", Code: "field_ambiguous"}}
			}
			if _, err = decodeDocumentIntakeInterpretation(body, view.Pass.CandidateIDs, prior); err == nil {
				t.Fatal("unoffered or ineligible prior evidence was accepted")
			}
		})
	}
}
