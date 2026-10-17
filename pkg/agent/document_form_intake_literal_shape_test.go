package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func TestDocumentIntakeProjectsNativeScalarPolicyWithoutDiscardingEvidence(t *testing.T) {
	view := privateIntakeTestView()
	view.Pass.Fields[0].MaxLength = 4
	view.Pass.Fields[1].Kind = document.FormFieldCheckbox
	view.Pass.Input.Text = "My code is MINT and the checkbox is yes, true."
	encoded, err := encodeDocumentIntakeView(view)
	if err != nil || !strings.Contains(string(encoded), `"max_length":4`) ||
		!strings.Contains(string(encoded), `"boolean_literals":["yes","true","1","on","checked",`+
			`"no","false","0","off","unchecked"]`) {
		t.Fatal("native scalar constraints did not reach the bounded private call")
	}
	provider := &documentAuditTestProvider{response: &providers.LLMResponse{Content: `{"intent":"facts",` +
		`"proposals":[{"field_id":"field_birth","literals":[{"text":"yes","occurrence":1},` +
		`{"text":"true","occurrence":1}]}]}`}}
	auditor := &documentFormAuditor{models: map[string]documentFormAuditModel{
		"allowed": {provider: provider, model: "configured-model"},
	}}
	result, err := auditor.InterpretFormIntake(t.Context(), "allowed", view)
	if err != nil || len(result.Proposals) != 1 || len(result.Proposals[0].Literals) != 2 {
		t.Fatal("redundant checkbox evidence was dropped before native unanimity validation")
	}
	view.Pass.Fields[1].Kind, view.Pass.Fields[1].MultiSelect = document.FormFieldList, true
	result, err = auditor.InterpretFormIntake(t.Context(), "allowed", view)
	if err != nil || len(result.Proposals) != 1 || len(result.Proposals[0].Literals) != 2 {
		t.Fatal("a real multi-select list lost its supported cardinality")
	}
	public, err := json.Marshal(view)
	if err != nil || string(public) != "{}" {
		t.Fatal("typed private view escaped ordinary JSON")
	}
}
