package agent

import (
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func TestDocumentIntakeInterpreterRequestsCompleteProseAccounting(t *testing.T) {
	provider := &documentAuditTestProvider{response: &providers.LLMResponse{Content: `{"intent":"facts",` +
		`"proposals":[],"non_data":[{"text":"I am","occurrence":1},{"text":"born","occurrence":1}],` +
		`"findings":[]}`}}
	auditor := &documentFormAuditor{models: map[string]documentFormAuditModel{
		"deliberative": {provider: provider, model: "configured-model"},
	}}
	view := privateIntakeTestView()
	view.Pass.Input.Accounted = []document.FormIntakeAccountedLiteral{
		{Literal: document.FormIntakeLiteral{Text: "Casey Mint", Occurrence: 1}, FieldID: "field_name"},
		{Literal: document.FormIntakeLiteral{Text: "1989-02-15", Occurrence: 1}, FieldID: "field_birth"},
	}
	result, err := auditor.InterpretFormIntake(t.Context(), "deliberative", view)
	if err != nil || len(result.NonData) != 2 || len(result.Proposals)+len(result.Findings) != 0 {
		t.Fatal("connective-only private interpretation was lost")
	}
	prompt := provider.messages[0].Content
	for _, required := range []string{
		"Finish accounting ordinary connective", "every supplied fact is already accounted",
		"complete literal accounting", "Facts outside this field view must NOT be non_data",
		"Do not overlap non_data, proposed facts or correction evidence",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatal("private interpretation does not request complete, non-overlapping accounting")
		}
	}
	if provider.calls != 1 || !provider.sensitive || len(provider.tools) != 0 {
		t.Fatal("connective guidance escaped the tools-disabled private boundary")
	}
}
