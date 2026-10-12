package agent

import (
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func TestDocumentIntakeDistinguishesDraftScopeFromAnAdvisoryFinishRequest(t *testing.T) {
	const instruction = "Create and deliver the editable draft"
	view := privateIntakeTestView()
	view.Pass.Input.Text = instruction
	view.OutputMode, view.OutputScope = document.FormOutputEditableAcroForm, document.FormOutputEditableDraft
	body := `{"intent":"finish","output":{"mode":"editable_draft","literal":{"text":"` +
		instruction + `","occurrence":1}},"non_data":[]}`
	provider := &documentAuditTestProvider{response: &providers.LLMResponse{Content: body}}
	auditor := &documentFormAuditor{models: map[string]documentFormAuditModel{
		"configured": {provider: provider, model: "same-model"},
	}}
	result, err := auditor.InterpretFormIntake(t.Context(), "configured", view)
	if err != nil || result.Intent != document.FormIntakeIntentFinish || result.Output == nil ||
		result.Output.Mode != document.FormIntakeOutputDraft || provider.calls != 1 ||
		!strings.Contains(provider.messages[0].Content, "create or deliver") ||
		!strings.Contains(provider.messages[0].Content, "use intent finish") {
		t.Fatal("draft output was conflated with choosing scope instead of requesting creation")
	}
}
