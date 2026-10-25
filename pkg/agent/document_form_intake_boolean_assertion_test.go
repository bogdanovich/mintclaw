package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/providers/protocoltypes"
)

type documentBooleanVerificationTestProvider struct {
	documentAuditTestProvider
	responses []*providers.LLMResponse
	requests  [][]providers.Message
	verifyErr error
	private   bool
}

func (provider *documentBooleanVerificationTestProvider) Chat(
	ctx context.Context, messages []providers.Message, tools []providers.ToolDefinition,
	model string, options map[string]any,
) (*providers.LLMResponse, error) {
	provider.private = provider.private && protocoltypes.IsSensitiveRequest(ctx) && len(tools) == 0 &&
		options["native_search"] == false && options["temperature"] == 0 && protocoltypes.PromptCachePlanOptionPresent(options)
	provider.requests = append(provider.requests, append([]providers.Message(nil), messages...))
	if provider.calls >= len(provider.responses) {
		return nil, errors.New("unexpected private call")
	}
	provider.response = provider.responses[provider.calls]
	if provider.calls > 0 {
		provider.err = provider.verifyErr
	}
	return provider.documentAuditTestProvider.Chat(ctx, messages, tools, model, options)
}

func booleanAssertionTestView() document.FormIntakeInterpretationView {
	view := privateIntakeTestView()
	view.Pass.Input.Text = "I want postal delivery. My name is Casey Mint."
	view.Pass.CandidateIDs = []string{"field_checkbox", "field_name"}
	view.Pass.Fields = []document.FormField{
		{ID: "field_checkbox", Name: "Borrower postal delivery", Kind: document.FormFieldCheckbox},
		{ID: "field_name", Name: "Borrower name", Kind: document.FormFieldText},
	}
	return view
}

const booleanAssertionTestResponse = `{"intent":"facts","proposals":[{"field_id":"field_checkbox",` +
	`"boolean_assertion":true,"literals":[{"text":"I want postal delivery","occurrence":1}]},` +
	`{"field_id":"field_name","literals":[{"text":"Casey Mint","occurrence":1}]}]}`

func TestDocumentIntakeBooleanAssertionWirePreservesTheExactPrivateQuote(t *testing.T) {
	const body = `{"intent":"facts","proposals":[{"field_id":"field_checkbox",` +
		`"boolean_assertion":true,"literals":[{"text":"I want postal delivery","occurrence":1}]}]}`
	result, err := decodeDocumentIntakeInterpretation(body, []string{"field_checkbox"})
	if err != nil || len(result.Proposals) != 1 || result.Proposals[0].Literals[0].Text != "I want postal delivery" {
		t.Fatal("a source-quoted typed checkbox assertion was not preserved by the private wire")
	}
	public, err := json.Marshal(result.Proposals[0])
	if err != nil || strings.Contains(string(public), "postal") ||
		strings.Contains(string(public), "boolean_assertion") {
		t.Fatal("a protected assertion or its quote escaped public JSON")
	}
}

func TestDocumentIntakeBooleanAssertionsNeedSeparateCompletePrivateVerification(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		body := `{"verified":["field_checkbox"],"findings":[]}`
		if rejected {
			body = `{"verified":[],"findings":[{"field_id":"field_checkbox","code":"field_ambiguous"}]}`
		}
		provider := &documentBooleanVerificationTestProvider{private: true, responses: []*providers.LLMResponse{
			{Content: booleanAssertionTestResponse}, {Content: body},
		}}
		auditor := &documentFormAuditor{models: map[string]documentFormAuditModel{
			"allowed": {provider: provider, model: "configured-audit"},
		}}
		view := booleanAssertionTestView()
		result, err := auditor.InterpretFormIntake(t.Context(), "allowed", view)
		expected := 2
		if rejected {
			expected = 1
		}
		if err != nil || len(result.Proposals) != expected || provider.calls != 2 || !provider.private ||
			provider.model != "configured-audit" || protocoltypes.IsSensitiveRequest(t.Context()) {
			t.Fatal("assertion bypassed verification or changed the private call boundary")
		}
		if rejected && (len(result.Findings) != 1 || result.Proposals[0].FieldID != "field_name") {
			t.Fatal("role ambiguity erased an ordinary fact or accepted the rejected assertion")
		}
		if !rejected && (result.Proposals[0].BooleanAssertion == nil || !*result.Proposals[0].BooleanAssertion) {
			t.Fatal("verified assertion lost its typed value")
		}
		if !strings.Contains(provider.requests[1][1].Content, view.Pass.Input.Text) ||
			!strings.Contains(provider.requests[1][1].Content, "Borrower postal delivery") ||
			strings.Contains(provider.requests[1][0].Content, "Casey Mint") {
			t.Fatal("verification omitted exact statement/role/field context or leaked it into system instructions")
		}
	}
}

func TestDocumentIntakeBooleanVerificationRejectsMalformedPartitions(t *testing.T) {
	for _, body := range []string{
		`{}`, `{"verified":[]}`, `{"verified":["other"]}`, `{"verified":["field_checkbox","field_checkbox"]}`,
		`{"verified":["field_checkbox"],"findings":[{"field_id":"field_checkbox","code":"field_ambiguous"}]}`,
		`{"findings":[{"field_id":"field_checkbox","code":"field_invalid"}]}`,
		`{"verified":["field_checkbox"],"generated_value":true}`,
		` {"verified":["field_checkbox"]}`, `{"verified":["field_checkbox"]}{}`,
	} {
		if _, _, err := decodeDocumentIntakeFieldVerification(body, []string{"field_checkbox"}); err == nil {
			t.Fatal("incomplete, foreign or malformed assertion verification was accepted")
		}
	}
}

func TestDocumentIntakeBooleanVerificationPartitionsEachOfferedAssertionExactlyOnce(t *testing.T) {
	const mixed = `{"verified":["borrower"],"findings":[{"field_id":"contact","code":"field_ambiguous"}]}`
	verified, findings, err := decodeDocumentIntakeFieldVerification(mixed, []string{"borrower", "contact"})
	if err != nil || len(verified) != 1 || verified[0] != "borrower" || len(findings) != 1 ||
		findings[0].FieldID != "contact" {
		t.Fatal("partial verification did not preserve the distinction between two people")
	}
	for _, body := range []string{
		`{"verified":["borrower","borrower"]}`,
		`{"verified":["borrower","foreign"]}`,
		`{"verified":["borrower"],"findings":[{"field_id":"borrower","code":"field_ambiguous"}]}`,
	} {
		if _, _, err = decodeDocumentIntakeFieldVerification(body, []string{"borrower", "contact"}); err == nil {
			t.Fatal("verification accepted a duplicate/foreign ID while another person's answer was unverified")
		}
	}
}

func TestDocumentIntakeBooleanFalseAssertionIsNotOmitted(t *testing.T) {
	view := booleanAssertionTestView()
	view.Pass.Input.Text = strings.Replace(view.Pass.Input.Text, "I want", "I do not want", 1)
	body := strings.Replace(booleanAssertionTestResponse, "I want", "I do not want", 1)
	body = strings.Replace(body, `"boolean_assertion":true`, `"boolean_assertion":false`, 1)
	provider := &documentBooleanVerificationTestProvider{private: true, responses: []*providers.LLMResponse{
		{Content: body}, {Content: `{"verified":["field_checkbox"]}`},
	}}
	auditor := &documentFormAuditor{models: map[string]documentFormAuditModel{
		"allowed": {provider: provider, model: "configured"},
	}}
	result, err := auditor.InterpretFormIntake(t.Context(), "allowed", view)
	if err != nil || len(result.Proposals) != 2 || result.Proposals[0].BooleanAssertion == nil ||
		*result.Proposals[0].BooleanAssertion || provider.calls != 2 || !provider.private ||
		!strings.Contains(provider.requests[1][1].Content, `"value":false`) {
		t.Fatal("false assertion was omitted or inverted before verification")
	}
}

func TestDocumentIntakeBooleanVerificationWireBudgetFailsBeforeProviderCall(t *testing.T) {
	provider := &documentBooleanVerificationTestProvider{private: true}
	value := true
	result := document.FormIntakeInterpretation{Proposals: []document.FormIntakeProposal{{
		FieldID: "field_checkbox", BooleanAssertion: &value,
		Literals: []document.FormIntakeLiteral{{Text: "I want postal delivery", Occurrence: 1}},
	}}}
	privateContext := []byte(`{"pad":"` + strings.Repeat("x", maxDocumentIntakeWireBytes) + `"}`)
	got, err := verifyDocumentIntakeBooleanAssertions(t.Context(), documentFormAuditModel{provider: provider},
		booleanAssertionTestView(), privateContext, result)
	if !errors.Is(err, errDocumentIntakeInterpretationUnavailable) || len(got.Proposals) != 0 || provider.calls != 0 {
		t.Fatal("over-budget assertion verification made a provider call or returned accepted-looking data")
	}
}

func TestDocumentIntakeBooleanVerificationFailureNeverReturnsAnAcceptedPrefix(t *testing.T) {
	for _, name := range []string{"missing", "malformed", "tools", "provider", "non-checkbox", "oversized"} {
		t.Run(name, func(t *testing.T) {
			verification := &providers.LLMResponse{Content: `{"verified":["field_checkbox"]}`}
			provider := &documentBooleanVerificationTestProvider{private: true, responses: []*providers.LLMResponse{
				{Content: booleanAssertionTestResponse}, verification,
			}}
			view := booleanAssertionTestView()
			switch name {
			case "missing":
				provider.responses[1] = nil
			case "malformed":
				verification.Content = `{"verified":[]}`
			case "tools":
				verification.ToolCalls = []providers.ToolCall{{Name: "forbidden"}}
			case "provider":
				provider.verifyErr = errors.New("PRIVATE_BOOLEAN_PROVIDER_ERROR")
			case "non-checkbox":
				view.Pass.Fields[0].Kind = document.FormFieldText
			case "oversized":
				verification.Content = strings.Repeat("x", maxDocumentIntakeWireBytes+1)
			}
			auditor := &documentFormAuditor{
				models: map[string]documentFormAuditModel{"allowed": {provider: provider, model: "configured"}},
			}
			result, err := auditor.InterpretFormIntake(t.Context(), "allowed", view)
			if !errors.Is(err, errDocumentIntakeInterpretationUnavailable) || len(result.Proposals) != 0 ||
				strings.Contains(err.Error(), "PRIVATE_BOOLEAN") || provider.calls > 2 || !provider.private {
				t.Fatal("failed verification returned a prefix, repeated extraction or disclosed a private error")
			}
		})
	}
}
