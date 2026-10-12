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

func TestDocumentIntakeFinishPreservesTheSelectedReviewOutputWithoutAScopeChange(t *testing.T) {
	view := privateIntakeTestView()
	view.Pass.Input.Text = "Finish and deliver the exact PDF from the current ready review"
	view.OutputMode, view.OutputScope = document.FormOutputEditableAcroForm, document.FormOutputEditableDraft
	provider := &documentAuditTestProvider{response: &providers.LLMResponse{Content: `{"intent":"finish"}`}}
	auditor := &documentFormAuditor{models: map[string]documentFormAuditModel{
		"configured": {provider: provider, model: "same-model"},
	}}
	result, err := auditor.InterpretFormIntake(t.Context(), "configured", view)
	if err != nil || result.Intent != document.FormIntakeIntentFinish || result.Output != nil ||
		view.OutputScope != document.FormOutputEditableDraft || provider.calls != 1 ||
		!strings.Contains(provider.messages[0].Content, "preserve its output scope") ||
		!strings.Contains(provider.messages[0].Content, "finish alone is not a scope change") {
		t.Fatal("finishing the selected review implicitly changed complete-versus-draft output")
	}
}
