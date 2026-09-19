package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

type documentAuditTestProvider struct {
	response *providers.LLMResponse
	err      error
	calls    int
	messages []providers.Message
	model    string
	tools    []providers.ToolDefinition
	options  map[string]any
}

func (provider *documentAuditTestProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	options map[string]any,
) (*providers.LLMResponse, error) {
	provider.calls++
	provider.messages = append([]providers.Message(nil), messages...)
	provider.tools = append([]providers.ToolDefinition(nil), tools...)
	provider.model = model
	provider.options = options
	return provider.response, provider.err
}

func (*documentAuditTestProvider) GetDefaultModel() string { return "audit-model" }

func TestDocumentFormAuditorUsesEphemeralStrictTypedCall(t *testing.T) {
	provider := &documentAuditTestProvider{response: &providers.LLMResponse{
		Content: `{"decision":"block","findings":[{"field_id":"field_` + strings.Repeat("a", 64) +
			`","code":"field_confirmation_required"}]}`,
		FinishReason: "stop",
	}}
	auditor := &documentFormAuditor{models: map[string]documentFormAuditModel{
		"document-deliberative": {provider: provider, model: "gpt-audit"},
	}}
	privateValue := "MINTCLAW_PDF3_AUDITOR_EPHEMERAL_11e4"
	fieldID := "field_" + strings.Repeat("a", 64)
	proposal, err := auditor.AuditForm(t.Context(), "document-deliberative", document.FormAuditView{
		PolicyRevision: strings.Repeat("b", 64), SchemaDigest: strings.Repeat("c", 64),
		Fields: []document.FormAuditField{{
			Schema: document.FormField{ID: fieldID, Name: "Legal name", Kind: document.FormFieldText},
			State: document.FormJobFieldState{
				FieldID: fieldID, State: document.FormValueConfirmed,
				Source:     document.FormValueSourceDeterministic,
				Confidence: document.FormValueConfidenceExact,
				Validation: document.FormValueValidationValid,
			},
			Value: document.FormProtectedValue{Kind: document.ProtectedValueText, Text: privateValue},
		}},
	})
	if err != nil || proposal.Decision != document.FormAuditBlock || len(proposal.Findings) != 1 ||
		proposal.Findings[0].FieldID != fieldID {
		t.Fatalf("proposal = %#v, err=%v", proposal, err)
	}
	if provider.calls != 1 || provider.model != "gpt-audit" || len(provider.tools) != 0 ||
		provider.options["temperature"] != 0 || len(provider.messages) != 2 ||
		provider.messages[0].Role != "system" || provider.messages[1].Role != "user" ||
		strings.Contains(provider.messages[0].Content, privateValue) ||
		!strings.Contains(provider.messages[1].Content, privateValue) {
		t.Fatalf("audit call = %#v", provider)
	}
	if strings.Contains(provider.messages[1].Content, "/") {
		t.Fatalf("audit request unexpectedly contains a host-path-like separator: %s", provider.messages[1].Content)
	}
}

func TestDocumentFormAuditorFailsClosedOnAliasProviderAndShape(t *testing.T) {
	provider := &documentAuditTestProvider{response: &providers.LLMResponse{
		Content: "```json\n{\"decision\":\"pass\"}\n```",
	}}
	auditor := &documentFormAuditor{models: map[string]documentFormAuditModel{
		"declared": {provider: provider, model: "audit-model"},
	}}
	if _, err := auditor.AuditForm(
		t.Context(), "undeclared", document.FormAuditView{},
	); !errors.Is(err, document.ErrFormAuditUnavailable) || provider.calls != 0 {
		t.Fatalf("undeclared alias error = %v calls=%d", err, provider.calls)
	}
	if _, err := auditor.AuditForm(
		t.Context(), "declared", document.FormAuditView{},
	); !errors.Is(err, document.ErrFormAuditUnavailable) || provider.calls != 1 {
		t.Fatalf("markdown response error = %v calls=%d", err, provider.calls)
	}
	provider.response = &providers.LLMResponse{Content: `{"decision":"pass","extra":true}`}
	if _, err := auditor.AuditForm(
		t.Context(), "declared", document.FormAuditView{},
	); !errors.Is(err, document.ErrFormAuditUnavailable) || provider.calls != 2 {
		t.Fatalf("unknown response field error = %v calls=%d", err, provider.calls)
	}
	provider.err = errors.New("private provider body")
	provider.response = nil
	if _, err := auditor.AuditForm(
		t.Context(), "declared", document.FormAuditView{},
	); !errors.Is(err, document.ErrFormAuditUnavailable) || strings.Contains(err.Error(), "private provider body") {
		t.Fatalf("provider error = %v", err)
	}
}

func TestDocumentFormAuditPolicyUsesResolvedProviderModelIdentities(t *testing.T) {
	firstIdentity := documentFormAuditModelIdentity("document-audit", "openai", "gpt-audit")
	secondIdentity := documentFormAuditModelIdentity("document-audit", "gemini", "gemini-audit")
	if firstIdentity == secondIdentity || strings.Contains(firstIdentity, "gpt-audit") {
		t.Fatalf("resolved identities were not distinct opaque bindings: %q %q", firstIdentity, secondIdentity)
	}
	cfg := &config.Config{}
	cfg.Tools.Document.AuditModel = "document-audit"
	cfg.Tools.Document.AuditEquivalentFallbacks = []string{"document-fallback"}
	auditor := &documentFormAuditor{identities: map[string]string{
		"document-audit":    firstIdentity,
		"document-fallback": documentFormAuditModelIdentity("document-fallback", "anthropic", "claude-audit"),
	}}
	policy := documentFormAuditPolicy(cfg, auditor)
	if policy.PrimaryIdentity != firstIdentity || len(policy.EquivalentFallbackIdentities) != 1 ||
		policy.EquivalentFallbackIdentities[0] != auditor.identities["document-fallback"] {
		t.Fatalf("resolved policy = %#v", policy)
	}
}
